package api

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
)

func dashboardTestHandler(t *testing.T, upstream string) (*Handler, *fakeJobRepository) {
	t.Helper()
	repository := &fakeJobRepository{jobs: []domain.TrainingJob{{
		ID: "job-1", TenantID: "tenant-a", UserID: "user-1", KubernetesNS: "tenant-a", RayClusterName: "train-a",
	}}}
	handler := NewHandler(repository, Options{WorkspacePepper: []byte(strings.Repeat("p", 32))})
	handler.dashboardUpstream = func(context.Context, *domain.TrainingJob) (string, error) { return upstream, nil }
	return handler, repository
}

func TestJobDashboardAccessReturnsShortLivedSameOriginURL(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler, _ := dashboardTestHandler(t, "http://dashboard.invalid")
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("ray-platform-principal", auth.Principal{Subject: "user-1", TenantID: "tenant-a", AuthType: auth.AuthTypeLocal})
		c.Next()
	})
	handler.RegisterTrainingRoutes(router.Group("/api/v1"))

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/jobs/job-1/dashboard-access", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", response.Code, response.Body.String())
	}
	for _, fragment := range []string{"/api/v1/jobs/job-1/dashboard/", "access_token=", "tenant=tenant-a", "subject=user-1"} {
		if !strings.Contains(response.Body.String(), fragment) {
			t.Fatalf("response must contain %q: %s", fragment, response.Body.String())
		}
	}
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("credential-bearing access response must not be cached")
	}
}

func TestJobDashboardAccessRejectsAnotherUsersJob(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler, _ := dashboardTestHandler(t, "http://dashboard.invalid")
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("ray-platform-principal", auth.Principal{Subject: "user-2", TenantID: "tenant-a", AuthType: auth.AuthTypeLocal})
		c.Next()
	})
	handler.RegisterTrainingRoutes(router.Group("/api/v1"))
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/jobs/job-1/dashboard-access", nil))
	if response.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d body=%s", response.Code, response.Body.String())
	}
}

func TestJobDashboardProxyExchangesQueryTokenAndRewritesAbsoluteRayAPIPaths(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/static/main.js" {
			t.Errorf("expected stripped upstream path, got %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/javascript")
		_, _ = io.WriteString(w, `fetch("/api/v0/nodes");fetch("api/jobs")`)
	}))
	defer upstream.Close()
	handler, _ := dashboardTestHandler(t, upstream.URL)
	router := gin.New()
	handler.RegisterJobDashboardProxyRoute(router.Group("/api/v1"))
	portal := httptest.NewServer(router)
	defer portal.Close()

	accessToken, err := domain.IssueJobDashboardAccessToken("tenant-a", "job-1", "user-1", handler.workspacePepper, time.Now(), domain.JobDashboardAccessTokenTTL)
	if err != nil {
		t.Fatalf("issue access token: %v", err)
	}
	client := &http.Client{CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	first, err := client.Get(portal.URL + "/api/v1/jobs/job-1/dashboard/static/main.js?access_token=" + accessToken + "&tenant=tenant-a&subject=user-1")
	if err != nil {
		t.Fatalf("exchange access token: %v", err)
	}
	defer first.Body.Close()
	if first.StatusCode != http.StatusFound {
		t.Fatalf("expected clean-URL redirect, got %d", first.StatusCode)
	}
	if strings.Contains(first.Header.Get("Location"), "access_token") {
		t.Fatalf("redirect must remove credentials: %q", first.Header.Get("Location"))
	}
	cookies := first.Cookies()
	if len(cookies) < 3 {
		t.Fatalf("expected dashboard session cookies, got %d", len(cookies))
	}

	secondRequest, _ := http.NewRequest(http.MethodGet, portal.URL+first.Header.Get("Location"), nil)
	for _, cookie := range cookies {
		secondRequest.AddCookie(cookie)
	}
	second, err := http.DefaultClient.Do(secondRequest)
	if err != nil {
		t.Fatalf("proxy dashboard asset: %v", err)
	}
	defer second.Body.Close()
	body, _ := io.ReadAll(second.Body)
	want := `fetch("/api/v1/jobs/job-1/dashboard/api/v0/nodes");fetch("api/jobs")`
	if string(body) != want {
		t.Fatalf("unexpected rewritten script %q", string(body))
	}

	mutatingRequest, _ := http.NewRequest(http.MethodPost, portal.URL+first.Header.Get("Location"), strings.NewReader("{}"))
	for _, cookie := range cookies {
		mutatingRequest.AddCookie(cookie)
	}
	mutatingResponse, err := http.DefaultClient.Do(mutatingRequest)
	if err != nil {
		t.Fatalf("attempt mutating dashboard request: %v", err)
	}
	defer mutatingResponse.Body.Close()
	if mutatingResponse.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("dashboard proxy must be read-only, got %d", mutatingResponse.StatusCode)
	}
}

func TestJobDashboardProxyRejectsPlainNavigation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler, _ := dashboardTestHandler(t, "http://dashboard.invalid")
	router := gin.New()
	handler.RegisterJobDashboardProxyRoute(router.Group("/api/v1"))
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/jobs/job-1/dashboard/", nil))
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", response.Code)
	}
}
