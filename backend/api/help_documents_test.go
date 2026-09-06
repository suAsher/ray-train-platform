package api

import (
	"github.com/gin-gonic/gin"
	"net/http/httptest"
	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
	"strings"
	"testing"
)

func TestHelpAdminAuthorizationAllRoutes(t *testing.T) {
	for _, role := range []string{domain.RoleEngineer, domain.RoleTenantAdmin, domain.RoleSuperAdmin} {
		for _, authType := range []auth.AuthenticationType{auth.AuthTypeLocal, auth.AuthTypePAT} {
			h := NewHandler(nil, Options{})
			router := gin.New()
			router.Use(func(c *gin.Context) {
				c.Set("ray-platform-principal", auth.Principal{Subject: "test", TenantID: "team", Roles: []string{role}, AuthType: authType})
			})
			h.RegisterHelpManagementRoutes(router.Group("/api/v1"))
			for _, route := range []struct{ method, path string }{{"GET", ""}, {"POST", ""}, {"PUT", "/example"}, {"POST", "/example/publish"}, {"POST", "/example/unpublish"}, {"GET", "/example/history"}, {"POST", "/example/restore"}} {
				w := httptest.NewRecorder()
				req := httptest.NewRequest(route.method, "/api/v1/admin/help/documents"+route.path, strings.NewReader(`{}`))
				req.Header.Set("Content-Type", "application/json")
				router.ServeHTTP(w, req)
				want := 403
				if role == domain.RoleSuperAdmin && authType == auth.AuthTypeLocal {
					want = 503
				}
				if w.Code != want {
					t.Fatalf("%s %s %s: got %d want %d: %s", role, authType, route.path, w.Code, want, w.Body.String())
				}
			}
		}
	}
}

func TestHelpReaderRequiresAuthentication(t *testing.T) {
	h := NewHandler(nil, Options{})
	r := gin.New()
	h.RegisterHelpReadRoutes(r.Group("/api/v1"))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/help/documents", nil))
	if w.Code != 401 {
		t.Fatal(w.Code)
	}
}
