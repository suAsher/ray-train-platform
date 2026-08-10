package rayapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"ray-train-platform-backend/api"
	"ray-train-platform-backend/auth"
)

type deniedUploadLimiter struct{ retryAfter time.Duration }

func (limiter deniedUploadLimiter) Allow(string) (bool, time.Duration) {
	return false, limiter.retryAfter
}

func limiterRouter(t *testing.T, limiter UploadLimiter, fillSemaphore bool) (*gin.Engine, *rayTestRepository) {
	t.Helper()
	principal := auth.Principal{Subject: "user-a", TenantID: "tenant-a", Roles: []string{"Engineer"}, AuthType: auth.AuthTypeOIDC}
	repository := &rayTestRepository{}
	submission := api.NewSubmissionService(repository, api.SubmissionServiceOptions{NewID: func() (string, error) { return "job-ray", nil }})
	handler, err := NewHandler(repository, &recoveryStore{}, submission, Options{SpoolDir: t.TempDir(), UploadLimiter: limiter, UploadMaxConcurrent: 1})
	if err != nil {
		t.Fatal(err)
	}
	if fillSemaphore {
		handler.uploads <- struct{}{}
	}
	router := gin.New()
	router.Use(func(c *gin.Context) { c.Set("ray-platform-principal", principal); c.Next() })
	handler.RegisterRoutes(router.Group("/ray"))
	return router, repository
}

func TestRayPackagePutLimitsBeforeSpooling(t *testing.T) {
	packagePath := "/ray/api/packages/gcs/" + testPackageSHA256 + ".zip"
	for _, test := range []struct {
		name    string
		limiter UploadLimiter
		fill    bool
		retry   string
	}{
		{name: "rate", limiter: deniedUploadLimiter{retryAfter: time.Minute}, retry: "60"},
		{name: "concurrency", limiter: newFixedWindowUploadLimiter(20, 10, time.Now), fill: true, retry: "5"},
	} {
		t.Run(test.name, func(t *testing.T) {
			router, repository := limiterRouter(t, test.limiter, test.fill)
			request := httptest.NewRequest(http.MethodPut, packagePath, http.NoBody)
			request.ContentLength = 1
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != http.StatusTooManyRequests || response.Header().Get("Retry-After") != test.retry || repository.artifacts != nil {
				t.Fatalf("status=%d retry=%q artifacts=%v", response.Code, response.Header().Get("Retry-After"), repository.artifacts)
			}
		})
	}
}
