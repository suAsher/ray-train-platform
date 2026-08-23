package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
)

func imageScopeRouter(store ImageStore, principal auth.Principal) *gin.Engine {
	gin.SetMode(gin.TestMode)
	handler := NewHandler(nil, Options{Images: store})
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("ray-platform-principal", principal)
		c.Next()
	})
	handler.RegisterImageRoutes(router.Group("/api/v1"))
	return router
}

func TestSuperAdminCanPromoteExistingImageToPlatformScope(t *testing.T) {
	store := &stubImageStore{}
	router := imageScopeRouter(store, auth.Principal{
		Subject: "root-admin", TenantID: "team-a", Roles: []string{domain.RoleSuperAdmin}, AuthType: auth.AuthTypeLocal,
	})
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPatch, "/api/v1/images/img-team", strings.NewReader(`{"shared":true}`))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("expected scope update to succeed, got %d: %s", response.Code, response.Body.String())
	}
	if store.updatedID != "img-team" || !store.updatedShared {
		t.Fatalf("scope update did not reach the image store: %+v", store)
	}
}

func TestTenantAdminCannotPublishExistingImagePlatformWide(t *testing.T) {
	store := &stubImageStore{}
	router := imageScopeRouter(store, auth.Principal{
		Subject: "team-admin", TenantID: "team-a", Roles: []string{domain.RoleTenantAdmin}, AuthType: auth.AuthTypeLocal,
	})
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPatch, "/api/v1/images/img-team", strings.NewReader(`{"shared":true}`))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(response, request)

	if response.Code != http.StatusForbidden {
		t.Fatalf("expected tenant administrator to be denied, got %d: %s", response.Code, response.Body.String())
	}
	if store.updatedID != "" {
		t.Fatalf("forbidden scope update reached the image store: %+v", store)
	}
}

func TestImageScopeUpdateRequiresSharedField(t *testing.T) {
	store := &stubImageStore{}
	router := imageScopeRouter(store, auth.Principal{
		Subject: "root-admin", TenantID: "team-a", Roles: []string{domain.RoleSuperAdmin}, AuthType: auth.AuthTypeLocal,
	})
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPatch, "/api/v1/images/img-team", strings.NewReader(`{}`))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("expected missing shared field to be rejected, got %d: %s", response.Code, response.Body.String())
	}
	if store.updatedID != "" {
		t.Fatalf("invalid scope update reached the image store: %+v", store)
	}
}

func TestSharedImageDemotionRequiresTargetTenant(t *testing.T) {
	store := &stubImageStore{}
	router := imageScopeRouter(store, auth.Principal{
		Subject: "root-admin", TenantID: "team-a", Roles: []string{domain.RoleSuperAdmin}, AuthType: auth.AuthTypeLocal,
	})
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPatch, "/api/v1/images/img-shared", strings.NewReader(`{"shared":false}`))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("expected demotion without a target tenant to be rejected, got %d: %s", response.Code, response.Body.String())
	}
}
