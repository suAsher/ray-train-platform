package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
	"ray-train-platform-backend/observability"
)

func logTestRouter(repository *fakeJobRepository, provider LogProvider) *gin.Engine {
	gin.SetMode(gin.TestMode)
	handler := NewHandler(repository, Options{Logs: provider})
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("ray-platform-principal", auth.Principal{
			Subject: "subject-a", TenantID: "team-a", Roles: []string{domain.RoleEngineer}, AuthType: auth.AuthTypeLocal,
		})
		c.Next()
	})
	handler.RegisterTrainingRoutes(router.Group("/api/v1"))
	return router
}

func TestGetJobLogsReturnsBackwardPageMetadataAndLatestLines(t *testing.T) {
	created := time.Date(2026, 8, 22, 16, 0, 0, 0, time.UTC)
	repository := &fakeJobRepository{jobs: []domain.TrainingJob{{ID: "job-01", TenantID: "team-a", CreatedAt: created}}}
	provider := &pagedLogProvider{lines: []observability.LogLine{
		{Timestamp: created.Add(time.Second), Line: "first"},
		{Timestamp: created.Add(2 * time.Second), Line: "second"},
		{Timestamp: created.Add(3 * time.Second), Line: "third"},
	}}
	router := logTestRouter(repository, provider)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/jobs/job-01/logs?limit=2&direction=backward", nil))

	body := response.Body.String()
	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", response.Code, body)
	}
	for _, marker := range []string{`"line":"second"`, `"line":"third"`, `"hasMore":true`, `"direction":"backward"`, `"nextCursor":"2026-08-22T16:00:02Z~1"`} {
		if !strings.Contains(body, marker) {
			t.Fatalf("response missing %s: %s", marker, body)
		}
	}
	if strings.Contains(body, `"line":"first"`) {
		t.Fatalf("backward page included an older overflow line: %s", body)
	}
}

func TestGetJobLogsRejectsLimitThatWouldExceedLokiPageCeiling(t *testing.T) {
	created := time.Date(2026, 8, 22, 16, 0, 0, 0, time.UTC)
	repository := &fakeJobRepository{jobs: []domain.TrainingJob{{ID: "job-01", TenantID: "team-a", CreatedAt: created}}}
	provider := &pagedLogProvider{}
	router := logTestRouter(repository, provider)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/jobs/job-01/logs?limit=10000", nil))

	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "INVALID_LOG_QUERY") {
		t.Fatalf("expected explicit query validation error, got %d: %s", response.Code, response.Body.String())
	}
	if provider.limit != 0 {
		t.Fatalf("invalid request reached Loki provider with limit %d", provider.limit)
	}
}
