package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
	"ray-train-platform-backend/repositories"
)

type fakeMLflowDashboardStore struct {
	mu       sync.Mutex
	tickets  map[string]repositories.MLflowDashboardTicketRecord
	created  []repositories.MLflowDashboardTicketRecord
	audits   []repositories.MLflowAuditEvent
	auditErr error
}

type mlflowResponseRecorder struct {
	*httptest.ResponseRecorder
	closed chan bool
}

type countingReadCloser struct {
	reader    io.Reader
	bytesRead int
	closed    bool
}

func (r *countingReadCloser) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	r.bytesRead += n
	return n, err
}

func (r *countingReadCloser) Close() error {
	r.closed = true
	return nil
}

func newMLflowResponseRecorder() *mlflowResponseRecorder {
	return &mlflowResponseRecorder{ResponseRecorder: httptest.NewRecorder(), closed: make(chan bool)}
}

func (r *mlflowResponseRecorder) CloseNotify() <-chan bool { return r.closed }

func newFakeMLflowDashboardStore() *fakeMLflowDashboardStore {
	return &fakeMLflowDashboardStore{tickets: make(map[string]repositories.MLflowDashboardTicketRecord)}
}

func (s *fakeMLflowDashboardStore) CreateMLflowDashboardTicket(_ context.Context, record repositories.MLflowDashboardTicketRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tickets[record.TokenHash] = record
	s.created = append(s.created, record)
	return nil
}

func (s *fakeMLflowDashboardStore) ConsumeMLflowDashboardTicket(_ context.Context, tokenHash string, now time.Time) (repositories.MLflowDashboardTicketRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.tickets[tokenHash]
	if !ok || record.ConsumedAt != nil || !record.ExpiresAt.After(now) {
		return repositories.MLflowDashboardTicketRecord{}, repositories.ErrMLflowDashboardTicketInvalid
	}
	consumedAt := now
	record.ConsumedAt = &consumedAt
	s.tickets[tokenHash] = record
	return record, nil
}

func (s *fakeMLflowDashboardStore) CreateMLflowAuditLog(_ context.Context, event repositories.MLflowAuditEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.audits = append(s.audits, event)
	return s.auditErr
}

func newMLflowDashboardTestHandler(store *fakeMLflowDashboardStore, now time.Time) *Handler {
	randomTicket := make([]byte, 32)
	for index := range randomTicket {
		randomTicket[index] = byte(index + 1)
	}
	return NewHandler(&fakeJobRepository{}, Options{
		MLflowDashboardEnabled:    true,
		MLflowDashboardStore:      store,
		MLflowTrackingURL:         "http://mlflow.mlflow-system.svc.cluster.local:5000",
		MLflowPublicOrigin:        "https://portal.example.com",
		MLflowDashboardPepper:     []byte(strings.Repeat("p", 32)),
		MLflowDashboardSessionTTL: 8 * time.Hour,
		MLflowDashboardNow:        func() time.Time { return now },
		MLflowDashboardRandom:     bytes.NewReader(randomTicket),
	})
}

func mlflowAccessRouter(handler *Handler, principal *auth.Principal, interactiveGuard bool) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	if principal != nil {
		router.Use(func(c *gin.Context) {
			c.Set("ray-platform-principal", *principal)
			c.Next()
		})
	}
	group := router.Group("/api/v1")
	if interactiveGuard {
		group.Use(auth.RequireInteractiveSession(false))
	}
	handler.RegisterMLflowDashboardAccessRoute(group)
	return router
}

func mlflowProxyRouter(handler *Handler) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	handler.RegisterMLflowDashboardProxyRoute(router.Group(""))
	return router
}

func issueMLflowAccessURL(t *testing.T, router http.Handler) string {
	t.Helper()
	response := newMLflowResponseRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/mlflow-dashboard-access", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("issue dashboard access: status=%d body=%s", response.Code, response.Body.String())
	}
	var envelope struct {
		Data struct {
			URL string `json:"url"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode access response: %v", err)
	}
	return envelope.Data.URL
}

func TestMLflowDashboardAccessRequiresAuthentication(t *testing.T) {
	now := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	handler := newMLflowDashboardTestHandler(newFakeMLflowDashboardStore(), now)
	response := newMLflowResponseRecorder()
	mlflowAccessRouter(handler, nil, false).ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/mlflow-dashboard-access", nil))
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", response.Code)
	}
}

func TestMLflowDashboardAccessInteractiveGuardAllowsEveryRoleAndRejectsPAT(t *testing.T) {
	now := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	for _, role := range []string{domain.RoleEngineer, domain.RoleTenantAdmin, domain.RoleSuperAdmin} {
		t.Run(role, func(t *testing.T) {
			principal := auth.Principal{Subject: "user-a", Username: "alice", TenantID: "tenant-a", Roles: []string{role}, AuthType: auth.AuthTypeOIDC}
			response := newMLflowResponseRecorder()
			mlflowAccessRouter(newMLflowDashboardTestHandler(newFakeMLflowDashboardStore(), now), &principal, true).ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/mlflow-dashboard-access", nil))
			if response.Code != http.StatusOK {
				t.Fatalf("status = %d body=%s", response.Code, response.Body.String())
			}
		})
	}

	pat := auth.Principal{Subject: "user-a", TenantID: "tenant-a", Roles: []string{domain.RoleEngineer}, AuthType: auth.AuthTypePAT}
	response := newMLflowResponseRecorder()
	mlflowAccessRouter(newMLflowDashboardTestHandler(newFakeMLflowDashboardStore(), now), &pat, true).ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/mlflow-dashboard-access", nil))
	if response.Code != http.StatusForbidden {
		t.Fatalf("PAT status = %d, want 403", response.Code)
	}
}

func TestMLflowDashboardAccessStoresOnlyTicketHashAndReturnsCleanURL(t *testing.T) {
	now := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	store := newFakeMLflowDashboardStore()
	principal := auth.Principal{Subject: "user-a", Username: "alice", TenantID: "tenant-a", Roles: []string{domain.RoleEngineer}, AuthType: auth.AuthTypeLocal}
	router := mlflowAccessRouter(newMLflowDashboardTestHandler(store, now), &principal, false)

	response := newMLflowResponseRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/mlflow-dashboard-access", nil))
	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("status=%d cache-control=%q body=%s", response.Code, response.Header().Get("Cache-Control"), response.Body.String())
	}
	var envelope struct {
		Data struct {
			URL string `json:"url"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	parsed, err := url.Parse(envelope.Data.URL)
	if err != nil {
		t.Fatalf("parse access URL: %v", err)
	}
	if parsed.Path != "/mlflow/" || len(parsed.Query()) != 1 || parsed.Query().Get("access_token") == "" {
		t.Fatalf("unclean access URL: %q", envelope.Data.URL)
	}
	if strings.Contains(envelope.Data.URL, principal.TenantID) || strings.Contains(envelope.Data.URL, principal.Subject) {
		t.Fatalf("identity leaked in access URL: %q", envelope.Data.URL)
	}
	rawTicket := parsed.Query().Get("access_token")
	decoded, err := base64.RawURLEncoding.DecodeString(rawTicket)
	if err != nil || len(decoded) != 32 {
		t.Fatalf("ticket is not 32 raw URL-safe bytes: length=%d err=%v", len(decoded), err)
	}
	if len(store.created) != 1 {
		t.Fatalf("created records = %d, want 1", len(store.created))
	}
	record := store.created[0]
	digest := sha256.Sum256([]byte(rawTicket))
	if record.TokenHash != hex.EncodeToString(digest[:]) || record.TenantID != principal.TenantID || record.UserID != principal.Subject {
		t.Fatalf("unexpected stored ticket: %+v", record)
	}
	if record.ExpiresAt != now.Add(2*time.Minute) || record.CreatedAt != now {
		t.Fatalf("unexpected ticket lifetime: created=%v expires=%v", record.CreatedAt, record.ExpiresAt)
	}
	storedJSON, _ := json.Marshal(record)
	if bytes.Contains(storedJSON, []byte(rawTicket)) {
		t.Fatalf("raw ticket persisted: %s", storedJSON)
	}
}

func TestMLflowDashboardTicketExchangeIsSingleUseAndSetsOneStrictCookie(t *testing.T) {
	now := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	store := newFakeMLflowDashboardStore()
	principal := auth.Principal{Subject: "user-a", TenantID: "tenant-a", Roles: []string{domain.RoleEngineer}, AuthType: auth.AuthTypeOIDC}
	handler := newMLflowDashboardTestHandler(store, now)
	accessURL := issueMLflowAccessURL(t, mlflowAccessRouter(handler, &principal, false))
	ticket := url.QueryEscape(strings.TrimPrefix(accessURL, "/mlflow/?access_token="))
	requestPath := "/mlflow/experiments/7?keep=1&access_token=" + ticket + "&other=2"

	response := httptest.NewRecorder()
	mlflowProxyRouter(handler).ServeHTTP(response, httptest.NewRequest(http.MethodGet, requestPath, nil))
	if response.Code != http.StatusFound || response.Header().Get("Location") != "/mlflow/experiments/7?keep=1&other=2" {
		t.Fatalf("exchange status=%d location=%q body=%s", response.Code, response.Header().Get("Location"), response.Body.String())
	}
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("cache-control = %q", response.Header().Get("Cache-Control"))
	}
	cookies := response.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("cookies = %d, want exactly one: %v", len(cookies), cookies)
	}
	cookie := cookies[0]
	if cookie.Name != "ray_mlflow_dashboard" || cookie.Path != "/mlflow/" || !cookie.HttpOnly || !cookie.Secure || cookie.SameSite != http.SameSiteStrictMode || cookie.MaxAge != int((8*time.Hour).Seconds()) {
		t.Fatalf("unexpected dashboard cookie: %+v", cookie)
	}
	claims, err := domain.VerifyMLflowDashboardSessionClaims(cookie.Value, []byte(strings.Repeat("p", 32)), now)
	if err != nil || claims.TenantID != principal.TenantID || claims.Subject != principal.Subject || claims.Nonce == "" || !claims.ExpiresAt.Equal(now.Add(8*time.Hour)) {
		t.Fatalf("unexpected cookie claims=%+v err=%v", claims, err)
	}

	replayed := newMLflowResponseRecorder()
	mlflowProxyRouter(handler).ServeHTTP(replayed, httptest.NewRequest(http.MethodGet, accessURL, nil))
	if replayed.Code != http.StatusUnauthorized {
		t.Fatalf("replayed ticket status = %d, want 401", replayed.Code)
	}
}

func TestMLflowDashboardTicketExchangeRejectsExpiredTamperedAndMalformedTickets(t *testing.T) {
	now := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	store := newFakeMLflowDashboardStore()
	principal := auth.Principal{Subject: "user-a", TenantID: "tenant-a", Roles: []string{domain.RoleEngineer}, AuthType: auth.AuthTypeOIDC}
	handler := newMLflowDashboardTestHandler(store, now)
	accessURL := issueMLflowAccessURL(t, mlflowAccessRouter(handler, &principal, false))
	rawTicket := strings.TrimPrefix(accessURL, "/mlflow/?access_token=")

	digest := sha256.Sum256([]byte(rawTicket))
	record := store.tickets[hex.EncodeToString(digest[:])]
	record.ExpiresAt = now
	store.tickets[record.TokenHash] = record

	for name, candidate := range map[string]string{
		"expired":   rawTicket,
		"tampered":  rawTicket[:len(rawTicket)-1] + "A",
		"malformed": "not-base64!",
		"short":     base64.RawURLEncoding.EncodeToString([]byte("short")),
	} {
		t.Run(name, func(t *testing.T) {
			response := newMLflowResponseRecorder()
			mlflowProxyRouter(handler).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/mlflow/?access_token="+url.QueryEscape(candidate), nil))
			if response.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", response.Code)
			}
			if strings.Contains(response.Body.String(), candidate) {
				t.Fatalf("response leaked ticket: %s", response.Body.String())
			}
		})
	}
}

func mlflowSessionCookie(t *testing.T, now time.Time) *http.Cookie {
	t.Helper()
	token, err := domain.IssueMLflowDashboardSession("tenant-a", "user-a", "nonce-a", []byte(strings.Repeat("p", 32)), now, 8*time.Hour)
	if err != nil {
		t.Fatalf("issue dashboard session: %v", err)
	}
	return &http.Cookie{Name: mlflowDashboardCookieName, Value: token}
}

func newMLflowProxyTestHandler(store *fakeMLflowDashboardStore, now time.Time, upstream string) *Handler {
	handler := newMLflowDashboardTestHandler(store, now)
	handler.mlflowTrackingURL = upstream
	return handler
}

func TestMLflowDashboardProxyForwardsAllMethodsAndStripsBrowserCredentials(t *testing.T) {
	now := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	type forwardedRequest struct {
		request *http.Request
		body    string
	}
	seen := make(chan forwardedRequest, 7)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)
		clone := request.Clone(context.Background())
		clone.Body = nil
		seen <- forwardedRequest{request: clone, body: string(body)}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()
	handler := newMLflowProxyTestHandler(newFakeMLflowDashboardStore(), now, upstream.URL)
	router := mlflowProxyRouter(handler)
	target, _ := url.Parse(upstream.URL)

	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		request := httptest.NewRequest(method, "/mlflow/ajax-api/2.0/mlflow/runs/search?keep=1", strings.NewReader("upload-body"))
		request.AddCookie(mlflowSessionCookie(t, now))
		request.AddCookie(&http.Cookie{Name: "unrelated", Value: "must-not-reach-upstream"})
		request.Header.Set("Authorization", "Bearer browser-secret")
		request.Header.Set("X-Forwarded-Access-Token", "forwarded-secret")
		if method == http.MethodPost || method == http.MethodPut || method == http.MethodPatch || method == http.MethodDelete {
			request.Header.Set("Origin", "https://portal.example.com")
		}
		response := newMLflowResponseRecorder()
		router.ServeHTTP(response, request)
		if response.Code != http.StatusNoContent {
			t.Fatalf("%s status = %d body=%s", method, response.Code, response.Body.String())
		}
		forwarded := <-seen
		if forwarded.request.Method != method || forwarded.request.URL.Path != "/mlflow/ajax-api/2.0/mlflow/runs/search" || forwarded.request.URL.RawQuery != "keep=1" || forwarded.body != "upload-body" {
			t.Fatalf("unexpected forwarded %s request: method=%s path=%q query=%q body=%q", method, forwarded.request.Method, forwarded.request.URL.Path, forwarded.request.URL.RawQuery, forwarded.body)
		}
		if forwarded.request.Host != target.Host {
			t.Fatalf("upstream Host = %q, want %q", forwarded.request.Host, target.Host)
		}
		for _, header := range []string{"Authorization", "Cookie", "X-Forwarded-Access-Token"} {
			if forwarded.request.Header.Get(header) != "" {
				t.Fatalf("%s forwarded browser %s header", method, header)
			}
		}
	}
}

func TestMLflowDashboardProxyRebuildsTrustedForwardingHeaders(t *testing.T) {
	now := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	seen := make(chan http.Header, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		seen <- request.Header.Clone()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()

	request := httptest.NewRequest(http.MethodGet, "/mlflow/", nil)
	request.AddCookie(mlflowSessionCookie(t, now))
	request.Header.Set("Forwarded", "for=attacker;host=evil.example;proto=http")
	request.Header.Set("X-Forwarded-For", "203.0.113.66")
	request.Header.Set("X-Forwarded-Host", "evil.example")
	request.Header.Set("X-Forwarded-Proto", "http")
	request.Header.Set("X-Real-IP", "203.0.113.77")
	response := newMLflowResponseRecorder()
	mlflowProxyRouter(newMLflowProxyTestHandler(newFakeMLflowDashboardStore(), now, upstream.URL)).ServeHTTP(response, request)

	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d body=%s", response.Code, response.Body.String())
	}
	forwarded := <-seen
	for _, header := range []string{"Forwarded", "X-Real-IP"} {
		if values, present := forwarded[http.CanonicalHeaderKey(header)]; present || len(values) != 0 {
			t.Fatalf("upstream %s = %q, want absent", header, values)
		}
	}
	if got := forwarded.Values("X-Forwarded-For"); len(got) != 1 || got[0] != "192.0.2.1" {
		t.Fatalf("upstream X-Forwarded-For = %q, want fresh backend client address", got)
	}
	if got := forwarded.Values("X-Forwarded-Host"); len(got) != 1 || got[0] != "portal.example.com" {
		t.Fatalf("upstream X-Forwarded-Host = %q, want configured public host", got)
	}
	if got := forwarded.Values("X-Forwarded-Proto"); len(got) != 1 || got[0] != "https" {
		t.Fatalf("upstream X-Forwarded-Proto = %q, want configured public scheme", got)
	}
}

func TestMLflowDashboardProxyRemovesAcceptEncodingBeforeTextRewrite(t *testing.T) {
	now := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	type acceptEncodingHeader struct {
		value   string
		present bool
	}
	seenAcceptEncoding := make(chan acceptEncodingHeader, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		acceptEncoding := request.Header.Get("Accept-Encoding")
		_, present := request.Header["Accept-Encoding"]
		seenAcceptEncoding <- acceptEncodingHeader{value: acceptEncoding, present: present}
		w.Header().Set("Content-Type", "application/javascript")
		if acceptEncoding != "" {
			w.Header().Set("Content-Encoding", "gzip")
		}
		_, _ = io.WriteString(w, `fetch("/api/2.0/mlflow/runs/search")`)
	}))
	defer upstream.Close()

	request := httptest.NewRequest(http.MethodGet, "/mlflow/static/app.js", nil)
	request.Header.Set("Accept-Encoding", "gzip, br")
	request.AddCookie(mlflowSessionCookie(t, now))
	response := newMLflowResponseRecorder()
	mlflowProxyRouter(newMLflowProxyTestHandler(newFakeMLflowDashboardStore(), now, upstream.URL)).ServeHTTP(response, request)

	if got := <-seenAcceptEncoding; got.present || got.value != "" {
		t.Fatalf("upstream Accept-Encoding = %q present=%t, want removed", got.value, got.present)
	}
	if response.Header().Get("Content-Encoding") != "" {
		t.Fatalf("rewritten response remained encoded: %q", response.Header().Get("Content-Encoding"))
	}
	if got := response.Body.String(); got != `fetch("/mlflow/api/2.0/mlflow/runs/search")` {
		t.Fatalf("JavaScript body was not rewritten: %q", got)
	}
}

func TestMLflowDashboardProxyRejectsUnsupportedMethodsBeforeCredentials(t *testing.T) {
	now := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	handler := newMLflowDashboardTestHandler(newFakeMLflowDashboardStore(), now)
	const allowedMethods = "GET, HEAD, OPTIONS, POST, PUT, PATCH, DELETE"

	t.Run(http.MethodTrace, func(t *testing.T) {
		request := httptest.NewRequest(http.MethodTrace, "/mlflow/?access_token=not-a-valid-ticket", nil)
		response := newMLflowResponseRecorder()
		mlflowProxyRouter(handler).ServeHTTP(response, request)
		if response.Code != http.StatusMethodNotAllowed || response.Header().Get("Allow") != allowedMethods {
			t.Fatalf("TRACE status=%d Allow=%q body=%s", response.Code, response.Header().Get("Allow"), response.Body.String())
		}
	})

	t.Run(http.MethodConnect, func(t *testing.T) {
		response := newMLflowResponseRecorder()
		context, _ := gin.CreateTestContext(response)
		context.Request = httptest.NewRequest(http.MethodConnect, "/mlflow/api/2.0", nil)
		handler.proxyMLflowDashboard(context)
		if response.Code != http.StatusMethodNotAllowed || response.Header().Get("Allow") != allowedMethods {
			t.Fatalf("CONNECT status=%d Allow=%q body=%s", response.Code, response.Header().Get("Allow"), response.Body.String())
		}
	})
}

func TestMLflowDashboardProxyRejectsCrossOriginMutationsBeforeUpstream(t *testing.T) {
	now := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	var upstreamCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		upstreamCalls.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()
	router := mlflowProxyRouter(newMLflowProxyTestHandler(newFakeMLflowDashboardStore(), now, upstream.URL))

	for name, headers := range map[string]map[string]string{
		"foreign origin":     {"Origin": "https://evil.example"},
		"foreign referer":    {"Referer": "https://evil.example/mlflow/"},
		"missing provenance": map[string]string{},
	} {
		t.Run(name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/mlflow/api/2.0/action", nil)
			request.AddCookie(mlflowSessionCookie(t, now))
			for key, value := range headers {
				request.Header.Set(key, value)
			}
			response := newMLflowResponseRecorder()
			router.ServeHTTP(response, request)
			if response.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403", response.Code)
			}
		})
	}
	if upstreamCalls.Load() != 0 {
		t.Fatalf("cross-origin requests reached upstream %d times", upstreamCalls.Load())
	}
}

func TestMLflowDashboardProxyAcceptsExactOriginOrRefererForMutations(t *testing.T) {
	now := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	defer upstream.Close()
	router := mlflowProxyRouter(newMLflowProxyTestHandler(newFakeMLflowDashboardStore(), now, upstream.URL))

	for name, headers := range map[string]map[string]string{
		"origin":  {"Origin": "https://portal.example.com"},
		"referer": {"Referer": "https://portal.example.com/mlflow/experiments/7?tab=runs"},
	} {
		t.Run(name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPatch, "/mlflow/api/2.0/action", strings.NewReader("payload"))
			request.AddCookie(mlflowSessionCookie(t, now))
			for key, value := range headers {
				request.Header.Set(key, value)
			}
			response := newMLflowResponseRecorder()
			router.ServeHTTP(response, request)
			if response.Code != http.StatusNoContent {
				t.Fatalf("status = %d body=%s", response.Code, response.Body.String())
			}
		})
	}
}

func TestMLflowDashboardProxyRewritesLocationTextAndSecurityHeaders(t *testing.T) {
	now := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	var upstreamURL string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Location", upstreamURL+"/api/2.0/mlflow/experiments?next=1")
		w.Header().Add("Set-Cookie", "upstream=secret; Path=/")
		w.WriteHeader(http.StatusTemporaryRedirect)
		_, _ = io.WriteString(w, `<script src="/api/main.js"></script><script>fetch('/ajax-api/2.0/mlflow/runs')</script>`)
	}))
	defer upstream.Close()
	upstreamURL = upstream.URL
	router := mlflowProxyRouter(newMLflowProxyTestHandler(newFakeMLflowDashboardStore(), now, upstream.URL))
	request := httptest.NewRequest(http.MethodGet, "/mlflow/experiments/7", nil)
	request.AddCookie(mlflowSessionCookie(t, now))
	response := newMLflowResponseRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusTemporaryRedirect || response.Header().Get("Location") != "/mlflow/api/2.0/mlflow/experiments?next=1" {
		t.Fatalf("status=%d location=%q", response.Code, response.Header().Get("Location"))
	}
	if got := response.Body.String(); got != `<script src="/mlflow/api/main.js"></script><script>fetch('/mlflow/ajax-api/2.0/mlflow/runs')</script>` {
		t.Fatalf("rewritten body = %q", got)
	}
	if response.Header().Get("Content-Length") != fmt.Sprint(response.Body.Len()) {
		t.Fatalf("content-length=%q body=%d", response.Header().Get("Content-Length"), response.Body.Len())
	}
	if response.Header().Values("Set-Cookie") != nil {
		t.Fatalf("upstream cookie leaked: %v", response.Header().Values("Set-Cookie"))
	}
	for _, header := range []string{"Content-Security-Policy", "Referrer-Policy", "X-Content-Type-Options", "X-Frame-Options"} {
		if response.Header().Get(header) == "" {
			t.Fatalf("missing security header %s", header)
		}
	}
}

func TestRewriteMLflowDashboardTextResponseBoundsMislabelledLargeBody(t *testing.T) {
	const expectedRewriteLimit = 2 << 20
	original := []byte(strings.Repeat("a", expectedRewriteLimit+1) + `"/api/must-not-change"`)
	originalBody := &countingReadCloser{reader: bytes.NewReader(original)}
	response := &http.Response{
		Header:        make(http.Header),
		Body:          originalBody,
		ContentLength: int64(len(original)),
		Request: &http.Request{
			Method: http.MethodGet,
			URL:    &url.URL{Path: "/mlflow/static/mislabelled.txt"},
		},
	}
	response.Header.Set("Content-Type", "text/html")
	response.Header.Set("Content-Length", fmt.Sprint(len(original)))

	if err := rewriteMLflowDashboardTextResponse(response); err != nil {
		t.Fatalf("rewrite response: %v", err)
	}
	if originalBody.bytesRead > expectedRewriteLimit+1 {
		t.Fatalf("rewrite eagerly read %d bytes, want at most %d", originalBody.bytesRead, expectedRewriteLimit+1)
	}
	if originalBody.closed {
		t.Fatal("oversized response body was closed before pass-through completed")
	}
	got, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read pass-through response: %v", err)
	}
	if err := response.Body.Close(); err != nil {
		t.Fatalf("close pass-through response: %v", err)
	}
	if !bytes.Equal(got, original) {
		t.Fatal("oversized text-labelled response was changed")
	}
	if response.ContentLength != int64(len(original)) || response.Header.Get("Content-Length") != fmt.Sprint(len(original)) {
		t.Fatalf("content length changed: field=%d header=%q", response.ContentLength, response.Header.Get("Content-Length"))
	}
	if !originalBody.closed {
		t.Fatal("closing pass-through response did not close upstream body")
	}
}

func TestRewriteMLflowDashboardTextResponseSkipsArtifactDownloadPath(t *testing.T) {
	original := []byte(`artifact bytes with misleading marker "/api/must-not-change"`)
	originalBody := &countingReadCloser{reader: bytes.NewReader(original)}
	response := &http.Response{
		Header:        make(http.Header),
		Body:          originalBody,
		ContentLength: int64(len(original)),
		Request: &http.Request{
			Method: http.MethodGet,
			URL:    &url.URL{Path: "/mlflow-artifacts/tenant-a/model.bin"},
		},
	}
	response.Header.Set("Content-Type", "text/html")
	response.Header.Set("Content-Length", fmt.Sprint(len(original)))

	if err := rewriteMLflowDashboardTextResponse(response); err != nil {
		t.Fatalf("rewrite response: %v", err)
	}
	if originalBody.bytesRead != 0 {
		t.Fatalf("artifact response was probed: read %d bytes", originalBody.bytesRead)
	}
	got, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read artifact response: %v", err)
	}
	if !bytes.Equal(got, original) {
		t.Fatalf("artifact response changed: got %q", got)
	}
	if response.ContentLength != int64(len(original)) || response.Header.Get("Content-Length") != fmt.Sprint(len(original)) {
		t.Fatalf("content length changed: field=%d header=%q", response.ContentLength, response.Header.Get("Content-Length"))
	}
}

func TestMLflowDashboardProxyDoesNotRewriteJSONOrBufferArtifacts(t *testing.T) {
	now := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	t.Run("JSON", func(t *testing.T) {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"path":"/api/2.0","ajax":"/ajax-api/2.0"}`)
		}))
		defer upstream.Close()
		request := httptest.NewRequest(http.MethodGet, "/mlflow/api/data", nil)
		request.AddCookie(mlflowSessionCookie(t, now))
		response := newMLflowResponseRecorder()
		mlflowProxyRouter(newMLflowProxyTestHandler(newFakeMLflowDashboardStore(), now, upstream.URL)).ServeHTTP(response, request)
		if response.Body.String() != `{"path":"/api/2.0","ajax":"/ajax-api/2.0"}` {
			t.Fatalf("JSON body was rewritten: %s", response.Body.String())
		}
	})

	t.Run("streaming artifact", func(t *testing.T) {
		release := make(chan struct{})
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = io.WriteString(w, "first")
			w.(http.Flusher).Flush()
			<-release
			_, _ = io.WriteString(w, `"/api/artifact"`)
		}))
		defer upstream.Close()
		portal := httptest.NewServer(mlflowProxyRouter(newMLflowProxyTestHandler(newFakeMLflowDashboardStore(), now, upstream.URL)))
		defer portal.Close()
		request, _ := http.NewRequest(http.MethodGet, portal.URL+"/mlflow/get-artifact", nil)
		request.AddCookie(mlflowSessionCookie(t, now))
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			close(release)
			t.Fatalf("request streaming artifact: %v", err)
		}
		defer response.Body.Close()
		first := make([]byte, 5)
		if _, err := io.ReadFull(response.Body, first); err != nil || string(first) != "first" {
			close(release)
			t.Fatalf("first streaming chunk=%q err=%v", first, err)
		}
		close(release)
		remainder, err := io.ReadAll(response.Body)
		if err != nil || string(remainder) != `"/api/artifact"` {
			t.Fatalf("artifact remainder=%q err=%v", remainder, err)
		}
	})
}

func TestMLflowDashboardProxyAuditsOnlyAllowlistedMetadataAndSurvivesAuditFailure(t *testing.T) {
	now := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	store := newFakeMLflowDashboardStore()
	store.auditErr = errors.New("audit unavailable")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusCreated) }))
	defer upstream.Close()
	request := httptest.NewRequest(http.MethodPost, "/mlflow/ajax-api/2.0/mlflow/runs?token=query-secret", strings.NewReader("body-secret"))
	request.Header.Set("Origin", "https://portal.example.com")
	request.Header.Set("X-Request-ID", "request-123")
	request.AddCookie(mlflowSessionCookie(t, now))
	request.AddCookie(&http.Cookie{Name: "other", Value: "cookie-secret"})
	response := newMLflowResponseRecorder()
	mlflowProxyRouter(newMLflowProxyTestHandler(store, now, upstream.URL)).ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("audit failure corrupted proxy response: status=%d body=%s", response.Code, response.Body.String())
	}
	if len(store.audits) != 1 {
		t.Fatalf("audits = %d, want 1", len(store.audits))
	}
	audit := store.audits[0]
	if audit.Principal.TenantID != "tenant-a" || audit.Principal.Subject != "user-a" || audit.Principal.Username != "user-a" || audit.Principal.AuthType != auth.AuthenticationType("mlflow-session") || audit.Method != http.MethodPost || audit.Path != "/mlflow/ajax-api/2.0/mlflow/runs" || audit.Status != http.StatusCreated || audit.RequestID != "request-123" {
		t.Fatalf("unexpected audit: %+v", audit)
	}
	auditText := fmt.Sprintf("%+v", audit)
	for _, secret := range []string{"query-secret", "body-secret", "cookie-secret"} {
		if strings.Contains(auditText, secret) {
			t.Fatalf("audit leaked %q: %s", secret, auditText)
		}
	}
}

func TestMLflowDashboardProxyUpstreamFailureReturnsSafe502AndAudits(t *testing.T) {
	now := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	store := newFakeMLflowDashboardStore()
	handler := newMLflowProxyTestHandler(store, now, "http://127.0.0.1:1")
	request := httptest.NewRequest(http.MethodGet, "/mlflow/experiments?secret=must-not-leak", nil)
	request.AddCookie(mlflowSessionCookie(t, now))
	response := newMLflowResponseRecorder()
	mlflowProxyRouter(handler).ServeHTTP(response, request)
	if response.Code != http.StatusBadGateway || strings.Contains(response.Body.String(), "127.0.0.1") || strings.Contains(response.Body.String(), "must-not-leak") {
		t.Fatalf("unsafe upstream failure: status=%d body=%s", response.Code, response.Body.String())
	}
	if len(store.audits) != 1 || store.audits[0].Status != http.StatusBadGateway || store.audits[0].Path != "/mlflow/experiments" {
		t.Fatalf("unexpected failure audit: %+v", store.audits)
	}
}

var _ MLflowDashboardStore = (*fakeMLflowDashboardStore)(nil)
