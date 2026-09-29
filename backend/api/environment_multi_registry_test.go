package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"ray-train-platform-backend/auth"
	eb "ray-train-platform-backend/environmentbuild"
)

type environmentHostRegistry struct {
	eb.Registry
	hosts []string
}

func (r *environmentHostRegistry) Authenticate(_ context.Context, host string, _ eb.Credentials) error {
	r.hosts = append(r.hosts, host)
	return eb.ErrAuthorization
}

func TestEnvironmentAuthorizationAPIRoutesOnlyApprovedRegistryHosts(t *testing.T) {
	for _, tc := range []struct { name, body, host string; status int }{
		{"legacy", `{"username":"person","secret":"fixture-secret"}`, eb.RegistryHost, 403},
		{"Wellspiking", `{"registryHost":"harbor.wellspiking.ai","username":"person","secret":"fixture-secret"}`, eb.RegistryHost, 403},
		{"Qomolo", `{"registryHost":"harbor.qomolo.com","username":"person","secret":"fixture-secret"}`, "harbor.qomolo.com", 403},
		{"forged", `{"registryHost":"evil.invalid","username":"person","secret":"fixture-secret"}`, "", 400},
		{"URL", `{"registryHost":"https://harbor.qomolo.com","username":"person","secret":"fixture-secret"}`, "", 400},
		{"duplicate host", `{"registryHost":"harbor.qomolo.com","registryHost":"harbor.wellspiking.ai","username":"person","secret":"fixture-secret"}`, "", 400},
		{"unknown password", `{"registryHost":"harbor.qomolo.com","username":"person","password":"fixture-secret"}`, "", 400},
		{"unknown token", `{"registryHost":"harbor.qomolo.com","username":"person","secret":"fixture-secret","token":"fixture"}`, "", 400},
		{"uppercase host field", `{"RegistryHost":"harbor.qomolo.com","username":"person","secret":"fixture-secret"}`, "", 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			registry := &environmentHostRegistry{}
			service, err := eb.NewService(&environmentAPIStore{}, &environmentAPIRunner{}, registry, &environmentAPIVault{}, eb.Config{Enabled: true, RegistryHosts: []string{eb.RegistryHost, "harbor.qomolo.com"}, BaseImage: "example.com/base@sha256:"+strings.Repeat("1", 64), WorkspaceImage: "example.com/debug@sha256:"+strings.Repeat("2", 64), EncryptionKey: []byte(strings.Repeat("k", 32))})
			if err != nil { t.Fatal(err) }
			router := gin.New()
			router.Use(func(c *gin.Context) { c.Set("ray-platform-principal", auth.Principal{Subject: "person", TenantID: "team", AuthType: auth.AuthTypeLocal, Roles: []string{"Engineer"}}); c.Next() })
			RegisterEnvironmentBuildRoutes(router.Group("/api/v1"), service)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/registry-authorizations", strings.NewReader(tc.body)))
			if response.Code != tc.status || strings.Contains(response.Body.String(), "fixture-secret") || response.Header().Get("Cache-Control") != "no-store" { t.Fatalf("unsafe authorization response: %d %s", response.Code, response.Body.String()) }
			if tc.host == "" { if len(registry.hosts) != 0 { t.Fatal("malformed host reached registry") } } else if len(registry.hosts) != 1 || registry.hosts[0] != tc.host { t.Fatalf("wrong credential destination: %v", registry.hosts) }
			if tc.host == "harbor.qomolo.com" && !strings.Contains(response.Body.String(), "密码") { t.Fatal("Qomolo denial incorrectly requires CLI Secret only") }
		})
	}
}
