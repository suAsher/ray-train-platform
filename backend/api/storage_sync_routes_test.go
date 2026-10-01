package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"ray-train-platform-backend/auth"
)

func TestStorageSyncAllManagementRoutesRequireCurrentInteractiveSuperAdmin(t *testing.T) {
	paths := []struct{ method, path string }{{"GET", "/spaces"}, {"GET", "/plans"}, {"POST", "/plans"}, {"PATCH", "/plans/p"}, {"POST", "/previews"}, {"GET", "/previews/p"}, {"POST", "/plans/p/runs"}, {"GET", "/runs"}, {"GET", "/runs/r"}, {"GET", "/runs/r/files"}, {"POST", "/runs/r/pause"}, {"POST", "/runs/r/resume"}, {"POST", "/runs/r/cancel"}, {"POST", "/runs/r/retry"}, {"POST", "/browse-requests"}, {"GET", "/browse-requests/b"}}
	for _, kind := range []string{"anonymous", "pat", "engineer", "tenant-admin", "disabled", "demoted"} {
		t.Run(kind, func(t *testing.T) {
			resolver, identity := syncResolverFixture()
			p := auth.Principal{Subject: "admin", TenantID: "local", AuthType: auth.AuthTypeOAuth2Proxy, Roles: []string{"SuperAdmin"}}
			switch kind {
			case "pat":
				p.AuthType = auth.AuthTypePAT
			case "engineer":
				p.Roles = []string{"Engineer"}
			case "tenant-admin":
				p.Roles = []string{"TenantAdmin"}
			case "disabled":
				identity.user.Disabled = true
			case "demoted":
				identity.user.Roles = []string{"Engineer"}
			}
			router := gin.New()
			router.Use(func(c *gin.Context) {
				if kind != "anonymous" {
					c.Request = c.Request.WithContext(auth.SetPrincipalContext(c.Request.Context(), p))
				}
				c.Next()
			})
			h := NewStorageSyncHandler(nil, resolver, nil, []byte("test-key"))
			h.RegisterManagementRoutes(router.Group("/api/v1"))
			for _, route := range paths {
				req := httptest.NewRequest(route.method, "/api/v1/admin/storage-sync"+route.path, strings.NewReader("{}"))
				req.Header.Set("Content-Type", "application/json")
				w := httptest.NewRecorder()
				router.ServeHTTP(w, req)
				want := http.StatusForbidden
				if kind == "anonymous" {
					want = http.StatusUnauthorized
				}
				if w.Code != want {
					t.Errorf("%s %s got %d want %d: %s", route.method, route.path, w.Code, want, w.Body.String())
				}
			}
		})
	}
}

func TestStorageSyncWorkerTokenBoundToSubjectAttemptAndGeneration(t *testing.T) {
	key := []byte("test-key")
	first := StorageSyncWorkerToken(key, "run", "run-1", 1, 1)
	if first == "" || first == StorageSyncWorkerToken(key, "run", "run-1", 2, 1) || first == StorageSyncWorkerToken(key, "preview", "run-1", 1, 1) || first == StorageSyncWorkerToken(key, "run", "run-1", 1, 2) {
		t.Fatal("worker capability not fully scoped")
	}
}
