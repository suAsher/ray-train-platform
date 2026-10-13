package telemetry

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"go.opentelemetry.io/otel/codes"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

type captureSpans struct {
	mu      sync.Mutex
	spans   []sdktrace.ReadOnlySpan
	blocked chan struct{}
}

func (e *captureSpans) ExportSpans(ctx context.Context, spans []sdktrace.ReadOnlySpan) error {
	if e.blocked != nil {
		select {
		case <-e.blocked:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.spans = append(e.spans, spans...)
	return nil
}
func (e *captureSpans) Shutdown(context.Context) error { return nil }

type captureLogs struct {
	mu      sync.Mutex
	records []sdklog.Record
	fail    bool
}

func (e *captureLogs) Export(_ context.Context, records []sdklog.Record) error {
	if e.fail {
		return errors.New("private exporter failure")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, r := range records {
		e.records = append(e.records, r.Clone())
	}
	return nil
}
func (e *captureLogs) Shutdown(context.Context) error   { return nil }
func (e *captureLogs) ForceFlush(context.Context) error { return nil }

func TestMiddlewarePrivacyStatusAndCardinality(t *testing.T) {
	gin.SetMode(gin.TestMode)
	traces := &captureSpans{}
	logs := &captureLogs{}
	s := newService(traces, logs, 128)
	defer s.Shutdown(t.Context())
	router := gin.New()
	router.Use(s.Middleware())
	router.GET("/jobs/:id", func(c *gin.Context) { c.Status(200) })
	router.GET("/forbidden", func(c *gin.Context) { c.Status(403) })
	router.GET("/failure", func(c *gin.Context) { c.Status(503) })
	for _, p := range []string{"/jobs/private-job?token=secret-query", "/jobs/other-private", "/forbidden", "/failure", "/unmatched-secret"} {
		r := httptest.NewRequest("GET", p, nil)
		r.Header.Set("Authorization", "Bearer private-auth")
		r.Header.Set("Cookie", "private-cookie")
		r.Header.Set("X-Request-ID", "private-request")
		r.Header.Set("traceparent", "00-11111111111111111111111111111111-2222222222222222-01")
		r.Header.Set("baggage", "user=private-user")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		id := w.Header().Get("X-Trace-ID")
		if len(id) != 32 || id == "11111111111111111111111111111111" {
			t.Fatalf("invalid independent trace ID %q", id)
		}
	}
	r := httptest.NewRequest("PRIVATE-METHOD", "/another-secret", nil)
	router.ServeHTTP(httptest.NewRecorder(), r)
	if err := s.flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	traces.mu.Lock()
	defer traces.mu.Unlock()
	logs.mu.Lock()
	defer logs.mu.Unlock()
	if len(traces.spans) != 6 || len(logs.records) != 6 {
		t.Fatalf("missing exports: spans=%d logs=%d", len(traces.spans), len(logs.records))
	}
	for _, span := range traces.spans {
		if span.Parent().IsValid() {
			t.Fatal("inbound parent accepted")
		}
		if len(span.Attributes()) != 3 {
			t.Fatalf("unexpected span attributes %v", span.Attributes())
		}
		if strings.Contains(span.Name(), "private") || strings.Contains(span.Name(), "secret") {
			t.Fatal("raw path leaked")
		}
		if span.Name() == "GET /failure" && span.Status().Code != codes.Error {
			t.Fatal("5xx not error")
		}
		if span.Name() == "GET /forbidden" && span.Status().Code != codes.Unset {
			t.Fatal("4xx marked error")
		}
	}
	w := httptest.NewRecorder()
	s.metricsHandler().ServeHTTP(w, httptest.NewRequest("GET", "/metrics", nil))
	body := w.Body.String()
	for _, secret := range []string{"private", "secret", "Authorization", "Cookie", "11111111"} {
		if strings.Contains(body, secret) {
			t.Fatalf("metrics leaked %q", secret)
		}
	}
	for _, expected := range []string{`route="/jobs/:id"`, `route="unmatched"`, `method="OTHER"`, `status_class="5xx"`, `raytrain_http_requests_total`} {
		if !strings.Contains(body, expected) {
			t.Fatalf("missing metric %s", expected)
		}
	}
	for _, record := range logs.records {
		if record.Body().AsString() != "http.request.completed" || record.AttributesLen() != 4 || !record.TraceID().IsValid() {
			t.Fatal("log whitelist or correlation failed")
		}
	}
}
func TestQueueFullAndExportFailureDoNotBlockRequests(t *testing.T) {
	gin.SetMode(gin.TestMode)
	block := make(chan struct{})
	traces := &captureSpans{blocked: block}
	logs := &captureLogs{fail: true}
	s := newService(traces, logs, 1)
	router := gin.New()
	router.Use(s.Middleware())
	router.GET("/ok", func(c *gin.Context) { c.String(202, "business response") })
	started := time.Now()
	for i := 0; i < 100; i++ {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest("GET", "/ok", nil))
		if w.Code != 202 || w.Body.String() != "business response" {
			t.Fatal("telemetry changed response")
		}
	}
	if time.Since(started) > time.Second {
		t.Fatal("blocked on exporter")
	}
	close(block)
	if err := s.flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	s.metricsHandler().ServeHTTP(w, httptest.NewRequest("GET", "/metrics", nil))
	if !strings.Contains(w.Body.String(), `reason="queue_full"`) {
		t.Fatal("missing bounded drop counter")
	}
	if !strings.Contains(w.Body.String(), `signal="logs"`) {
		t.Fatal("missing export failure counter")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := s.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
}
func TestShutdownIsBounded(t *testing.T) {
	block := make(chan struct{})
	s := newService(&captureSpans{blocked: block}, &captureLogs{}, 1)
	router := gin.New()
	router.Use(s.Middleware())
	router.GET("/", func(c *gin.Context) { c.Status(http.StatusOK) })
	router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	started := time.Now()
	_ = s.Shutdown(ctx)
	if time.Since(started) > time.Second {
		t.Fatal("shutdown exceeded deadline")
	}
	close(block)
}

func TestShutdownWithCanceledContextStopsBothWorkers(t *testing.T) {
	s := newService(&captureSpans{}, &captureLogs{}, 2)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_ = s.Shutdown(ctx)
	for _, done := range []<-chan struct{}{s.traceQueue.done, s.logQueue.done} {
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("SDK skipped queue shutdown after canceled context")
		}
	}
}
func TestConcurrentRequestsDuringShutdown(t *testing.T) {
	s := newService(&captureSpans{}, &captureLogs{}, 16)
	router := gin.New()
	router.Use(s.Middleware())
	router.GET("/ok", func(c *gin.Context) { c.Status(200) })
	var wg sync.WaitGroup
	for n := 0; n < 4; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/ok", nil))
			}
		}()
	}
	_ = s.Shutdown(t.Context())
	wg.Wait()
}
