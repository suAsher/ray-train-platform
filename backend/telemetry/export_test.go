package telemetry

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	collectlogs "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	collecttrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/protobuf/proto"
	"ray-train-platform-backend/config"
)

func gateway(t *testing.T, ca testCA, dns string, handler http.Handler) *httptest.Server {
	t.Helper()
	cert, key := issue(t, ca, []string{dns}, "")
	pair, err := tls.X509KeyPair(cert, key)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(ca.pem)
	server := httptest.NewUnstartedServer(handler)
	server.TLS = &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{pair}, ClientCAs: roots, ClientAuth: tls.RequireAndVerifyClientCert, VerifyConnection: func(state tls.ConnectionState) error {
		if !hasIdentity(state.PeerCertificates[0], backendIdentity) {
			return errors.New("wrong backend client identity")
		}
		return nil
	}}
	server.StartTLS()
	t.Cleanup(server.Close)
	return server
}
func gatewayTransport(cfg config.TelemetryConfig, server *httptest.Server) *rotatingTransport {
	return &rotatingTransport{cfg: cfg, dialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
	}}
}

func TestRealOTLPProtobufUsesMTLSAndWhitelist(t *testing.T) {
	cfg, ca := tlsFixture(t)
	var mu sync.Mutex
	var paths []string
	var traceID, logID []byte
	server := gateway(t, ca, "otel-gateway.opentelemetry.svc", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		if r.Header.Get("Authorization") != "" {
			t.Error("environment OTLP header leaked")
		}
		mu.Lock()
		defer mu.Unlock()
		paths = append(paths, r.URL.Path)
		switch r.URL.Path {
		case "/v1/traces":
			req := &collecttrace.ExportTraceServiceRequest{}
			if err := proto.Unmarshal(body, req); err != nil {
				t.Error(err)
				return
			}
			span := req.ResourceSpans[0].ScopeSpans[0].Spans[0]
			traceID = span.TraceId
			if span.Name != "GET /jobs/:id" || len(span.Attributes) != 3 {
				t.Error("unapproved span payload")
			}
			attrs := req.ResourceSpans[0].Resource.Attributes
			if len(attrs) != 1 || attrs[0].Value.GetStringValue() != "ray-train-backend" {
				t.Error("unapproved resource payload")
			}
		case "/v1/logs":
			req := &collectlogs.ExportLogsServiceRequest{}
			if err := proto.Unmarshal(body, req); err != nil {
				t.Error(err)
				return
			}
			record := req.ResourceLogs[0].ScopeLogs[0].LogRecords[0]
			logID = record.TraceId
			if record.Body.GetStringValue() != "http.request.completed" || len(record.Attributes) != 4 {
				t.Error("unapproved log payload")
			}
		default:
			t.Error("incorrect OTLP path")
		}
		w.Header().Set("Content-Type", "application/x-protobuf")
		w.WriteHeader(200)
	}))
	t.Setenv("OTEL_EXPORTER_OTLP_HEADERS", "Authorization=private-environment-token")
	traces, logs, err := exporters(cfg, gatewayTransport(cfg, server))
	if err != nil {
		t.Fatal(err)
	}
	s := newService(traces, logs, 16)
	defer s.Shutdown(t.Context())
	router := gin.New()
	router.Use(s.Middleware())
	router.GET("/jobs/:id", func(c *gin.Context) { c.String(200, "private-response") })
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("GET", "/jobs/private-job?token=private-token", nil))
	if err := s.flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(paths) != 2 || len(traceID) != 16 || string(traceID) != string(logID) {
		t.Fatalf("OTLP correlation missing paths=%v", paths)
	}
}
func TestOTLPClientReloadAndStrictServerDNS(t *testing.T) {
	cfg, ca := tlsFixture(t)
	var serial string
	var serialMu sync.Mutex
	server := gateway(t, ca, "otel-gateway.opentelemetry.svc", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serialMu.Lock(); serial = r.TLS.PeerCertificates[0].SerialNumber.String(); serialMu.Unlock()
		w.WriteHeader(200)
	}))
	client := &http.Client{Transport: gatewayTransport(cfg, server), Timeout: time.Second}
	send := func() error {
		resp, err := client.Post(cfg.OTLPEndpoint+"/v1/traces", "application/x-protobuf", strings.NewReader(""))
		if err == nil {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}
		return err
	}
	if err := send(); err != nil {
		t.Fatal(err)
	}
	serialMu.Lock(); first := serial; serialMu.Unlock()
	cert, key := issue(t, ca, []string{metricsDNS, metricsDNS + ".cluster.local"}, backendIdentity)
	writePair(t, cfg.OTLPCertFile, cfg.OTLPKeyFile, cert, key)
	if err := send(); err != nil {
		t.Fatal(err)
	}
	serialMu.Lock(); same := serial == first; serialMu.Unlock(); if same {
		t.Fatal("client certificate did not rotate")
	}
	cert, key = issue(t, ca, nil, scraperIdentity)
	writePair(t, cfg.OTLPCertFile, cfg.OTLPKeyFile, cert, key)
	if err := send(); err == nil {
		t.Fatal("accepted wrong OTLP client URI")
	}
	cert, key = issue(t, ca, nil, backendIdentity)
	writePair(t, cfg.OTLPCertFile, cfg.OTLPKeyFile, cert, key)
	wrongDNS := gateway(t, ca, "wrong.example", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	client.Transport = gatewayTransport(cfg, wrongDNS)
	if err := send(); err == nil {
		t.Fatal("accepted wrong OTLP server DNS")
	}
	client.Transport = gatewayTransport(cfg, server)
	if err := os.WriteFile(cfg.CAFile, makeCA(t).pem, 0600); err != nil {
		t.Fatal(err)
	}
	if err := send(); err == nil {
		t.Fatal("retained old OTLP CA")
	}
}
func TestEndpointOfflineDoesNotPreventStartupOrBusinessResponse(t *testing.T) {
	cfg, _ := tlsFixture(t)
	start := time.Now()
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("startup connected to exporter")
	}
	defer s.Shutdown(t.Context())
	traces, logs, err := exporters(cfg, &rotatingTransport{cfg: cfg, dialContext: func(context.Context, string, string) (net.Conn, error) { return nil, errors.New("offline") }})
	if err != nil {
		t.Fatal(err)
	}
	offline := newService(traces, logs, 16)
	defer offline.Shutdown(t.Context())
	router := gin.New()
	router.Use(offline.Middleware())
	router.GET("/ok", func(c *gin.Context) { c.Status(204) })
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("GET", "/ok", nil))
	if w.Code != 204 {
		t.Fatal("export outage changed response")
	}
	if err := offline.flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	offline.metricsHandler().ServeHTTP(w, httptest.NewRequest("GET", "/metrics", nil))
	if !strings.Contains(w.Body.String(), `reason="export_failed",signal="traces"} 1`) {
		t.Fatal("missing failed-export drop count")
	}
}
func TestDisabledTelemetryDoesNotExposeTraceHeader(t *testing.T) {
	s, err := New(config.TelemetryConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	router.Use(s.Middleware())
	router.GET("/", func(c *gin.Context) { c.Status(204) })
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	if w.Header().Get("X-Trace-ID") != "" || w.Code != 204 {
		t.Fatal("disabled telemetry changed business response")
	}
	if err := s.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
}
