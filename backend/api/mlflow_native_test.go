package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
)

func nativeMLflowTestRouter(t *testing.T, upstream string, principal auth.Principal, store *fakeMLflowDashboardStore) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		if principal.Subject != "" {
			c.Set("ray-platform-principal", principal)
		}
		c.Next()
	})
	handler := NewHandler(&fakeJobRepository{}, Options{
		MLflowDashboardEnabled: true,
		MLflowDashboardStore:   store,
		MLflowTrackingURL:      upstream,
	})
	handler.RegisterMLflowNativeRoutes(router.Group("/api/v1"))
	return router
}

func nativeMLflowFullPrincipal() auth.Principal {
	return auth.Principal{
		Subject:  "user-a",
		Username: "engineer",
		TenantID: "team-a",
		Roles:    []string{domain.RoleEngineer},
		AuthType: auth.AuthTypePAT,
		Scopes:   []string{domain.PATScopeMLflowFull},
	}
}

func TestNativeMLflowForwardsOnlyAllowedHeadersAndStreamsBodies(t *testing.T) {
	body := "\x00\x01model-bytes\xff"
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodPut || r.URL.Path != "/mlflow/api/2.0/mlflow-artifacts/artifacts/7/run/artifacts/model.bin" {
			t.Fatalf("unexpected upstream request method=%s path=%s", r.Method, r.URL.Path)
		}
		for key, want := range map[string]string{
			"Accept":                  "application/octet-stream",
			"Content-Type":            "application/octet-stream",
			"Content-Md5":             "digest",
			"Range":                   "bytes=0-4",
			"User-Agent":              "mlflow-python-client",
			"X-Mlflow-Client-Version": "3.14.0",
		} {
			if got := r.Header.Get(key); got != want {
				t.Fatalf("header %s=%q want %q", key, got, want)
			}
		}
		for _, key := range []string{"Authorization", "Cookie", "Forwarded", "X-Forwarded-For", "X-Auth-Request-Access-Token", "X-Api-Key"} {
			if r.Header.Get(key) != "" {
				t.Fatalf("credential header forwarded: %s", key)
			}
		}
		got, _ := io.ReadAll(r.Body)
		if string(got) != body {
			t.Fatalf("artifact body changed: %q", got)
		}
		w.Header().Set("Set-Cookie", "upstream=private")
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write([]byte(body))
	}))
	defer upstream.Close()

	router := nativeMLflowTestRouter(t, upstream.URL+"/mlflow", nativeMLflowFullPrincipal(), newFakeMLflowDashboardStore())
	request := httptest.NewRequest(http.MethodPut, "/api/v1/mlflow-native/api/2.0/mlflow-artifacts/artifacts/7/run/artifacts/model.bin", strings.NewReader(body))
	request.Header.Set("Accept", "application/octet-stream")
	request.Header.Set("Authorization", "Bearer private")
	request.Header.Set("Content-Md5", "digest")
	request.Header.Set("Content-Type", "application/octet-stream")
	request.Header.Set("Cookie", "private=true")
	request.Header.Set("Forwarded", "for=private")
	request.Header.Set("Range", "bytes=0-4")
	request.Header.Set("User-Agent", "mlflow-python-client")
	request.Header.Set("X-Api-Key", "private")
	request.Header.Set("X-Auth-Request-Access-Token", "private")
	request.Header.Set("X-Forwarded-For", "private")
	request.Header.Set("X-Mlflow-Client-Version", "3.14.0")
	response := httptest.NewRecorder()

	router.ServeHTTP(response, request)

	if response.Code != http.StatusOK || response.Body.String() != body || calls.Load() != 1 {
		t.Fatalf("native artifact proxy status=%d calls=%d body=%q", response.Code, calls.Load(), response.Body.String())
	}
	if response.Header().Get("Set-Cookie") != "" || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("unsafe response headers: %v", response.Header())
	}
}

func TestNativeMLflowRejectsUnsafePathsAndCredentialQueries(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		calls.Add(1)
	}))
	defer upstream.Close()
	router := nativeMLflowTestRouter(t, upstream.URL+"/mlflow", nativeMLflowFullPrincipal(), newFakeMLflowDashboardStore())

	for _, path := range []string{
		"/api/v1/mlflow-native/ajax-api/2.0/mlflow/runs/get",
		"/api/v1/mlflow-native/api/2.0/mlflow/%2e%2e/runs/get",
		"/api/v1/mlflow-native/api/2.0/mlflow/%252e%252e/runs/get",
		"/api/v1/mlflow-native/api/2.0/mlflow-artifacts/artifacts/%2e%2e/model.bin",
		"/api/v1/mlflow-native/api/2.0/mlflow/runs/get?access_token=private",
		"/api/v1/mlflow-native/api/2.0/mlflow/runs/get?Authorization=private",
		"/api/v1/mlflow-native/api/2.0/mlflow/runs/get?api_key=private",
	} {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusBadRequest {
			t.Fatalf("path %s status=%d body=%s", path, response.Code, response.Body.String())
		}
	}
	if calls.Load() != 0 {
		t.Fatalf("unsafe request reached upstream %d times", calls.Load())
	}
}

func TestNativeMLflowRewritesOnlySafeUpstreamRedirects(t *testing.T) {
	for _, test := range []struct {
		name         string
		location     string
		wantStatus   int
		wantLocation string
	}{
		{
			name:         "native API redirect",
			location:     "/mlflow/api/2.0/mlflow/runs/get?run_id=abc",
			wantStatus:   http.StatusTemporaryRedirect,
			wantLocation: "/api/v1/mlflow-native/api/2.0/mlflow/runs/get?run_id=abc",
		},
		{
			name:       "external host",
			location:   "https://evil.example/mlflow/api/2.0/mlflow/runs/get",
			wantStatus: http.StatusBadGateway,
		},
		{
			name:       "upstream path escape",
			location:   "/api/2.0/mlflow/runs/get",
			wantStatus: http.StatusBadGateway,
		},
		{
			name:       "credential query",
			location:   "/mlflow/api/2.0/mlflow/runs/get?access_token=private",
			wantStatus: http.StatusBadGateway,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Location", test.location)
				w.WriteHeader(http.StatusTemporaryRedirect)
			}))
			defer upstream.Close()
			router := nativeMLflowTestRouter(t, upstream.URL+"/mlflow", nativeMLflowFullPrincipal(), newFakeMLflowDashboardStore())

			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/mlflow-native/api/2.0/mlflow/runs/get?run_id=abc", nil))

			if response.Code != test.wantStatus {
				t.Fatalf("status=%d want=%d body=%s", response.Code, test.wantStatus, response.Body.String())
			}
			if got := response.Header().Get("Location"); got != test.wantLocation {
				t.Fatalf("Location=%q want %q", got, test.wantLocation)
			}
		})
	}
}

func TestNativeMLflowRegistrationRejectsAmbiguousUpstreams(t *testing.T) {
	for _, upstream := range []string{
		"",
		"http://user:pass@mlflow:5000/mlflow",
		"https://mlflow.example.com/mlflow?token=private",
		"https://mlflow.example.com/other",
	} {
		router := nativeMLflowTestRouter(t, upstream, nativeMLflowFullPrincipal(), newFakeMLflowDashboardStore())
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/mlflow-native/api/2.0/mlflow/runs/get", nil))
		if response.Code != http.StatusNotFound {
			t.Fatalf("upstream %q registered native route with status %d", upstream, response.Code)
		}
	}
}
