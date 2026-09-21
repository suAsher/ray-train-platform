package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"ray-train-platform-backend/auth"
	eb "ray-train-platform-backend/environmentbuild"
)

type environmentAPIStore struct{ eb.Store }
type environmentAPIRunner struct{ eb.Runner }
type environmentAPIVault struct{ eb.Vault }
type environmentAPIRegistry struct {
	eb.Registry
	calls int
}

func (r *environmentAPIRegistry) Authenticate(context.Context, eb.Credentials) error {
	r.calls++
	return eb.ErrAuthorization
}
func TestEnvironmentBuildAPIRejectsMachineCredentialsAndMalformedSecrets(t *testing.T) {
	for _, tc := range []struct {
		name string
		kind auth.AuthenticationType
		body string
		want int
	}{
		{"PAT", auth.AuthTypePAT, `{"username":"person","secret":"test-secret"}`, 403},
		{"demo", auth.AuthTypeDemo, `{"username":"person","secret":"test-secret"}`, 403},
		{"unknown owner", auth.AuthTypeLocal, `{"username":"person","secret":"test-secret","ownerId":"victim"}`, 400},
		{"oversized", auth.AuthTypeLocal, `{"username":"person","secret":"` + strings.Repeat("x", 17000) + `"}`, 400},
		{"trailing JSON", auth.AuthTypeLocal, `{"username":"person","secret":"test-secret"}{}`, 400},
		{"duplicate field", auth.AuthTypeLocal, `{"username":"person","secret":"one","secret":"two"}`, 400},
		{"uppercase field", auth.AuthTypeLocal, `{"USERNAME":"person","secret":"one"}`, 400},
		{"wrong secret", auth.AuthTypeLocal, `{"username":"person","secret":"test-secret"}`, 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			registry := &environmentAPIRegistry{}
			service, err := eb.NewService(&environmentAPIStore{}, &environmentAPIRunner{}, registry, &environmentAPIVault{}, eb.Config{Enabled: true, BaseImage: "example.com/base@sha256:" + strings.Repeat("1", 64), WorkspaceImage: "example.com/debug@sha256:" + strings.Repeat("2", 64), EncryptionKey: []byte(strings.Repeat("k", 32)), AuthorizationTTL: time.Hour})
			if err != nil {
				t.Fatal(err)
			}
			router := gin.New()
			router.Use(func(c *gin.Context) {
				c.Set("ray-platform-principal", auth.Principal{Subject: "person", TenantID: "team", AuthType: tc.kind, Roles: []string{"Engineer"}})
				c.Next()
			})
			RegisterEnvironmentBuildRoutes(router.Group("/api/v1"), service)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/registry-authorizations", strings.NewReader(tc.body)))
			if response.Code != tc.want {
				t.Fatalf("status %d: %s", response.Code, response.Body.String())
			}
			if strings.Contains(response.Body.String(), "test-secret") {
				t.Fatal("credential leaked in response")
			}
			if response.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("credential response cacheable")
			}
			if tc.name != "wrong secret" && registry.calls != 0 {
				t.Fatal("malformed/unprivileged request used Harbor credential")
			}
		})
	}
}
