package api

import (
	"github.com/gin-gonic/gin"
	"net/http/httptest"
	"testing"
)

func TestCLICompatibilityGuardOnlyBlocksIdentifiedIncompatibleSubmissions(t *testing.T) {
	for _, tc := range []struct {
		version, method, path string
		want                  int
	}{
		{"release-20260906-01", "POST", "/api/v1/jobs", 426},
		{"release-20260906-10", "POST", "/api/v1/jobs", 200},
		{"dev", "POST", "/api/v1/jobs", 400},
		{"", "POST", "/api/v1/jobs", 200},
		{"", "POST", "/ray/api/jobs/", 200},
		{"release-20260906-01", "GET", "/api/v1/jobs", 200},
		{"release-20260906-01", "POST", "/api/v1/jobs/j/cancel", 200},
		{"release-20260906-01", "POST", "/api/v1/source-artifacts", 426},
	} {
		router := gin.New()
		router.Use(CLICompatibilityGuard("release-20260906-02"))
		router.Handle(tc.method, tc.path, func(c *gin.Context) { c.Status(200) })
		request := httptest.NewRequest(tc.method, tc.path, nil)
		request.Header.Set("X-Spk-Rayjob-Version", tc.version)
		writer := httptest.NewRecorder()
		router.ServeHTTP(writer, request)
		if writer.Code != tc.want {
			t.Fatalf("%s %s %q: %d want%d", tc.method, tc.path, tc.version, writer.Code, tc.want)
		}
	}
}

func TestCLICompatibilityDisabledDoesNotRejectDev(t *testing.T) {
	router := gin.New()
	router.Use(CLICompatibilityGuard(""))
	router.POST("/api/v1/jobs", func(c *gin.Context) { c.Status(200) })
	request := httptest.NewRequest("POST", "/api/v1/jobs", nil)
	request.Header.Set("X-Spk-Rayjob-Version", "dev")
	writer := httptest.NewRecorder()
	router.ServeHTTP(writer, request)
	if writer.Code != 200 {
		t.Fatal(writer.Code)
	}
}
