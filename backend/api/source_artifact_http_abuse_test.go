package api

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
	"ray-train-platform-backend/objectstore"
	"ray-train-platform-backend/repositories"
)

type denyArtifactLimiter struct {
	key    string
	action sourceArtifactAction
}

func (limiter *denyArtifactLimiter) Allow(key string, action sourceArtifactAction) (bool, time.Duration) {
	limiter.key, limiter.action = key, action
	return false, 7 * time.Second
}

func abuseTestRouter(t *testing.T, repo *fakeSourceArtifactRepository, store *fakeArtifactStore, principal auth.Principal, options SourceArtifactOptions) *gin.Engine {
	t.Helper()
	handler, err := NewSourceArtifactHandler(repo, store, options)
	if err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	router.Use(artifactPrincipalMiddleware(principal))
	handler.RegisterRoutes(router.Group("/api/v1"))
	return router
}

func TestSourceArtifactHandlersRejectOversizedBodies(t *testing.T) {
	principal := auth.Principal{Subject: "user", TenantID: "tenant", Roles: []string{"Engineer"}, AuthType: auth.AuthTypeOIDC}
	repo := &fakeSourceArtifactRepository{}
	store := &fakeArtifactStore{}
	router := abuseTestRouter(t, repo, store, principal, SourceArtifactOptions{})
	oversized := strings.Repeat("x", int(SourceArtifactJSONBodyLimit)+1)
	for _, test := range []struct{ path, body string }{
		{path: "/api/v1/source-artifacts", body: oversized},
		{path: "/api/v1/source-artifacts/missing/complete", body: oversized},
	} {
		response := performArtifactRequest(router, http.MethodPost, test.path, test.body)
		if response.Code != http.StatusRequestEntityTooLarge || !strings.Contains(response.Body.String(), "REQUEST_BODY_TOO_LARGE") {
			t.Fatalf("oversized %s status=%d body=%s", test.path, response.Code, response.Body.String())
		}
	}
}

func TestSourceArtifactHandlerRateLimitsByPrincipal(t *testing.T) {
	principal := auth.Principal{Subject: "user", TenantID: "tenant", Roles: []string{"Engineer"}, AuthType: auth.AuthTypeOIDC}
	limiter := &denyArtifactLimiter{}
	router := abuseTestRouter(t, &fakeSourceArtifactRepository{}, &fakeArtifactStore{}, principal, SourceArtifactOptions{Limiter: limiter})
	response := performArtifactRequest(router, http.MethodPost, "/api/v1/source-artifacts", `{"sha256":"`+apiArtifactDigest+`","sizeBytes":100}`)
	if response.Code != http.StatusTooManyRequests || response.Header().Get("Retry-After") != "7" || !strings.Contains(response.Body.String(), "SOURCE_ARTIFACT_RATE_LIMITED") {
		t.Fatalf("rate limit response status=%d retry=%q body=%s", response.Code, response.Header().Get("Retry-After"), response.Body.String())
	}
	if limiter.key != "tenant\x00user" || limiter.action != sourceArtifactActionCreate {
		t.Fatalf("unexpected limiter identity/action")
	}
}

func TestSourceArtifactHandlerReturnsStableQuotaError(t *testing.T) {
	principal := auth.Principal{Subject: "user", TenantID: "tenant", Roles: []string{"Engineer"}, AuthType: auth.AuthTypeOIDC}
	repo := &fakeSourceArtifactRepository{createErr: repositories.ErrSourceArtifactQuotaExceeded}
	router := abuseTestRouter(t, repo, &fakeArtifactStore{}, principal, SourceArtifactOptions{})
	response := performArtifactRequest(router, http.MethodPost, "/api/v1/source-artifacts", `{"sha256":"`+apiArtifactDigest+`","sizeBytes":100}`)
	if response.Code != http.StatusTooManyRequests || !strings.Contains(response.Body.String(), "SOURCE_ARTIFACT_QUOTA_EXCEEDED") {
		t.Fatalf("quota response status=%d body=%s", response.Code, response.Body.String())
	}
}

var _ context.Context
var _ domain.SourceArtifact
var _ objectstore.Store
