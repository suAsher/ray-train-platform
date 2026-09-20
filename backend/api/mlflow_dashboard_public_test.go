package api

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/repositories"
)

func TestPublicMLflowDashboardAllowsDirectLinksAssetsAndAnonymousOperations(t *testing.T) {
	store := newFakeMLflowDashboardStore()
	store.accessAllowed = false
	calls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		for _, key := range []string{"Authorization", "Cookie", "X-Auth-Request-Access-Token", "X-Forwarded-Access-Token", "X-Api-Key"} {
			if r.Header.Get(key) != "" { t.Errorf("leaked header %s", key) }
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = io.WriteString(w, "response-bytes")
	}))
	defer upstream.Close()
	h := newMLflowDashboardTestHandler(store, time.Now())
	h.mlflowDashboardPublicEnabled = true
	h.mlflowTrackingURL = upstream.URL + "/mlflow"
	r := mlflowProxyRouter(h)
	for _, tc := range []struct{ method, path string }{
		{"GET", "/mlflow/"}, {"GET", "/mlflow/static-files/main.js"},
		{"GET", "/mlflow/ajax-api/2.0/mlflow/runs/get?run_id=abc"},
		{"POST", "/mlflow/ajax-api/2.0/mlflow/runs/create"},
		{"PUT", "/mlflow/api/2.0/mlflow-artifacts/artifacts/a.bin"},
		{"DELETE", "/mlflow/ajax-api/2.0/mlflow/registered-models/delete"},
	} {
		req := httptest.NewRequest(tc.method, tc.path, strings.NewReader("payload"))
		req.Header.Set("Origin", "https://portal.example.com")
		for _, key := range []string{"Authorization", "Cookie", "X-Auth-Request-Access-Token", "X-Api-Key"} { req.Header.Set(key, "stale-secret") }
		req.AddCookie(&http.Cookie{Name: mlflowDashboardCookieName, Value: "expired"})
		w := newMLflowResponseRecorder()
		r.ServeHTTP(w, req)
		if w.Code != 200 || w.Body.String() != "response-bytes" { t.Fatalf("%s %s: %d %s", tc.method, tc.path, w.Code, w.Body.String()) }
	}
	if calls != 6 || len(store.accessPrincipals) != 0 { t.Fatalf("calls=%d reauthorizations=%d", calls, len(store.accessPrincipals)) }
	if len(store.audits) == 0 { t.Fatal("missing anonymous audit") }
	for _, event := range store.audits {
		if event.Principal.AuthType != auth.AuthTypeAnonymous || event.Principal.Subject != "mlflow-anonymous" || event.Principal.TenantID != "" || strings.Contains(event.Path, "?") { t.Fatalf("incorrect anonymous audit: %+v", event) }
	}
}

func TestPublicMLflowDashboardKeepsMutationOriginAndAuditBoundaries(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("blocked request reached upstream") }))
	defer upstream.Close()
	for _, tc := range []struct{ origin string; auditErr error; want int }{
		{"", nil, 403}, {"https://other.example", nil, 403}, {"https://portal.example.com", errors.New("offline"), 503},
	} {
		store := newFakeMLflowDashboardStore()
		store.auditErr = tc.auditErr
		h := newMLflowDashboardTestHandler(store, time.Now())
		h.mlflowDashboardPublicEnabled = true
		h.mlflowTrackingURL = upstream.URL
		w := newMLflowResponseRecorder()
		req := httptest.NewRequest("POST", "/mlflow/ajax-api/2.0/mlflow/runs/delete", strings.NewReader(`{}`))
		req.Header.Set("Origin", tc.origin)
		mlflowProxyRouter(h).ServeHTTP(w, req)
		if w.Code != tc.want { t.Fatalf("status=%d want=%d body=%s", w.Code, tc.want, w.Body.String()) }
	}
}

func TestPublicMLflowDashboardExpiredTicketDoesNotRequireLogin(t *testing.T) {
	h := newMLflowDashboardTestHandler(newFakeMLflowDashboardStore(), time.Now())
	h.mlflowDashboardPublicEnabled = true
	w := httptest.NewRecorder()
	mlflowProxyRouter(h).ServeHTTP(w, httptest.NewRequest("GET", "/mlflow/?access_token=expired", nil))
	if w.Code != 302 || w.Header().Get("Location") != "/mlflow/" || w.Header().Get("Set-Cookie") != "" { t.Fatalf("%d %v", w.Code, w.Header()) }
}

func TestPublicMLflowDashboardKeepsPortalRunNavigationWithoutSession(t *testing.T) {
	now := time.Now()
	store := newFakeMLflowDashboardStore()
	store.accessAllowed = false
	hash := sha256.Sum256([]byte("navigation-ticket"))
	runID := "4c184d47979943de9d144ede96c8f59c"
	store.tickets[hex.EncodeToString(hash[:])] = repositories.MLflowDashboardTicketRecord{RedirectFragment: "#/experiments/6/runs/"+runID, ExpiresAt: now.Add(time.Minute)}
	h := newMLflowDashboardTestHandler(store, now)
	h.mlflowDashboardPublicEnabled = true
	w := httptest.NewRecorder()
	mlflowProxyRouter(h).ServeHTTP(w, httptest.NewRequest("GET", "/mlflow/?access_token=navigation-ticket", nil))
	if w.Code != 302 || w.Header().Get("Location") != "/mlflow/#/experiments/6/runs/"+runID || w.Header().Get("Set-Cookie") != "" || len(store.accessPrincipals) != 0 { t.Fatalf("portal navigation: %d %v", w.Code, w.Header()) }
}
