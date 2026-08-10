package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
	"ray-train-platform-backend/objectstore"
)

type ttlCapturingArtifactStore struct {
	ttl time.Duration
}

func (store *ttlCapturingArtifactStore) PresignPut(_ context.Context, _ string, digest string, size int64, ttl time.Duration) (objectstore.PresignedPut, error) {
	store.ttl = ttl
	return objectstore.PresignedPut{
		URL:       "https://private-bucket.tos-cn-beijing.volces.com/source.zip?X-Tos-Signature=redacted",
		ExpiresAt: time.Date(2026, 8, 10, 2, 15, 0, 0, time.UTC), ContentLength: size,
		RequiredHeaders: map[string]string{
			"Content-Type": "application/zip", "x-tos-meta-sha256": digest,
			"If-None-Match": "*", "x-tos-forbid-overwrite": "true",
		},
	}, nil
}

func (store *ttlCapturingArtifactStore) Head(context.Context, string) (objectstore.ObjectInfo, error) {
	return objectstore.ObjectInfo{}, nil
}

func TestSourceArtifactUploadTTLAndBrowserResponseContractAreFixed(t *testing.T) {
	if SourceArtifactUploadTTL != 15*time.Minute {
		t.Fatalf("source artifact TTL=%s, want 15m", SourceArtifactUploadTTL)
	}
	now := time.Date(2026, 8, 10, 2, 0, 0, 0, time.UTC)
	repo := &fakeSourceArtifactRepository{}
	store := &ttlCapturingArtifactStore{}
	handler, err := NewSourceArtifactHandler(repo, store, SourceArtifactOptions{
		Now: func() time.Time { return now }, NewID: func() (string, error) { return "artifact-fixed", nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	principal := auth.Principal{Subject: "user", TenantID: "tenant", Roles: []string{"Engineer"}, AuthType: auth.AuthTypePAT, Scopes: []string{domain.PATScopeSourcesWrite}}
	response := performArtifactRequest(artifactRouterForHandler(handler, principal), http.MethodPost, "/api/v1/source-artifacts", `{"sha256":"`+apiArtifactDigest+`","sizeBytes":100}`)
	if response.Code != http.StatusCreated || store.ttl != SourceArtifactUploadTTL {
		t.Fatalf("create status=%d ttl=%s", response.Code, store.ttl)
	}
	var envelope struct {
		Data struct {
			RequiredHeaders map[string]string `json:"requiredHeaders"`
			ContentLength   int64             `json:"contentLength"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Data.ContentLength != 100 {
		t.Fatalf("contentLength=%d, want 100", envelope.Data.ContentLength)
	}
	if _, exists := envelope.Data.RequiredHeaders["Content-Length"]; exists {
		t.Fatal("browser requiredHeaders must not contain forbidden Content-Length")
	}
	for _, header := range []string{"Content-Type", "x-tos-meta-sha256", "If-None-Match", "x-tos-forbid-overwrite"} {
		if envelope.Data.RequiredHeaders[header] == "" {
			t.Fatalf("missing browser-settable required header %q", header)
		}
	}
}

func artifactRouterForHandler(handler *SourceArtifactHandler, principal auth.Principal) http.Handler {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(artifactPrincipalMiddleware(principal))
	handler.RegisterRoutes(router.Group("/api/v1"))
	return router
}
