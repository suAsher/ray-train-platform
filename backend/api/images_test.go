package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
)

type compatibilityImageStore struct {
	stubImageStore
	created          domain.PlatformImage
	mutateStoreInput bool
}

func (s *compatibilityImageStore) CreateImage(_ context.Context, image domain.PlatformImage) error {
	if err := image.Validate(); err != nil {
		return err
	}
	s.created = image
	s.created.SupportedEngines = append([]domain.TrainingEngine(nil), image.SupportedEngines...)
	if s.mutateStoreInput {
		image.SupportedEngines[0] = "store-mutated"
	}
	return nil
}

func postImage(t *testing.T, store ImageStore, principal auth.Principal, body string) *httptest.ResponseRecorder {
	t.Helper()
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/images", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	imageScopeRouter(store, principal).ServeHTTP(response, request)
	return response
}

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

func TestImageReadAndManagementRoutesUseSeparateAuthenticationBoundaries(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := NewHandler(nil, Options{Images: &stubImageStore{}})
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("ray-platform-principal", auth.Principal{
			Subject: "cli-user", TenantID: "team-a", Roles: []string{domain.RoleEngineer}, AuthType: auth.AuthTypePAT,
			Scopes: []string{domain.PATScopeJobsWrite},
		})
		c.Next()
	})
	handler.RegisterImageReadRoutes(router.Group("/api/v1"))
	management := router.Group("/api/v1")
	management.Use(auth.RequireInteractiveSession(false))
	handler.RegisterImageManagementRoutes(management)

	readResponse := httptest.NewRecorder()
	router.ServeHTTP(readResponse, httptest.NewRequest(http.MethodGet, "/api/v1/images?kind=training", nil))
	if readResponse.Code != http.StatusOK {
		t.Fatalf("PAT image list status=%d body=%s", readResponse.Code, readResponse.Body.String())
	}

	writeResponse := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/images", strings.NewReader(`{}`))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(writeResponse, request)
	if writeResponse.Code != http.StatusForbidden || !strings.Contains(writeResponse.Body.String(), "INTERACTIVE_LOGIN_REQUIRED") {
		t.Fatalf("PAT image management status=%d body=%s", writeResponse.Code, writeResponse.Body.String())
	}
}

func TestImageReadRouteRequiresJobWriteScopeForPAT(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := NewHandler(nil, Options{Images: &stubImageStore{}})
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("ray-platform-principal", auth.Principal{
			Subject: "read-only-cli", TenantID: "team-a", Roles: []string{domain.RoleEngineer}, AuthType: auth.AuthTypePAT,
			Scopes: []string{domain.PATScopeJobsRead},
		})
		c.Next()
	})
	handler.RegisterImageReadRoutes(router.Group("/api/v1"))

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/images?kind=training", nil))
	if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), "INSUFFICIENT_SCOPE") {
		t.Fatalf("read-only PAT image list status=%d body=%s", response.Code, response.Body.String())
	}
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

func TestImageCompatibilityTenantAdminPublishesTenantLocalMetadata(t *testing.T) {
	store := &compatibilityImageStore{mutateStoreInput: true}
	principal := auth.Principal{
		Subject: "team-admin", TenantID: "team-a", Roles: []string{domain.RoleTenantAdmin}, AuthType: auth.AuthTypeLocal,
	}
	response := postImage(t, store, principal, `{
		"name":"Ray production","reference":"registry.example/runtime:2.56.1","kind":"training",
		"rayVersion":"2.56.1","supportedEngines":["ray-ddp","ray-train"],"shared":false
	}`)
	if response.Code != http.StatusCreated {
		t.Fatalf("tenant-local image status = %d, body=%s", response.Code, response.Body.String())
	}
	wantEngines := []domain.TrainingEngine{domain.TrainingEngineRayDDP, domain.TrainingEngineRayTrain}
	if store.created.TenantID != "team-a" || store.created.RayVersion != domain.RayVersionProduction || !reflect.DeepEqual(store.created.SupportedEngines, wantEngines) {
		t.Fatalf("store received wrong compatibility metadata: %+v", store.created)
	}
	var envelope struct {
		Data domain.PlatformImage `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	if envelope.Data.RayVersion != domain.RayVersionProduction || !reflect.DeepEqual(envelope.Data.SupportedEngines, wantEngines) {
		t.Fatalf("response did not preserve accepted metadata independently of store mutation: %+v", envelope.Data)
	}
}

func TestImageCompatibilitySharedPublishRequiresSuperAdmin(t *testing.T) {
	body := `{
		"name":"Ray shared","reference":"registry.example/runtime:2.58.0","kind":"training",
		"rayVersion":"2.58.0","supportedEngines":["ray-train"],"shared":true
	}`

	tenantStore := &compatibilityImageStore{}
	tenantResponse := postImage(t, tenantStore, auth.Principal{
		Subject: "team-admin", TenantID: "team-a", Roles: []string{domain.RoleTenantAdmin}, AuthType: auth.AuthTypeLocal,
	}, body)
	if tenantResponse.Code != http.StatusForbidden || tenantStore.created.ID != "" {
		t.Fatalf("tenant admin shared publish status=%d image=%+v body=%s", tenantResponse.Code, tenantStore.created, tenantResponse.Body.String())
	}

	superStore := &compatibilityImageStore{}
	superResponse := postImage(t, superStore, auth.Principal{
		Subject: "root", TenantID: "team-a", Roles: []string{domain.RoleSuperAdmin}, AuthType: auth.AuthTypeLocal,
	}, body)
	if superResponse.Code != http.StatusCreated || superStore.created.TenantID != "" {
		t.Fatalf("super admin shared publish status=%d image=%+v body=%s", superResponse.Code, superStore.created, superResponse.Body.String())
	}
}

func TestImageCompatibilityInvalidMetadataReturnsInvalidImage(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "unknown Ray version", body: `"rayVersion":"2.99.0","supportedEngines":["ray-ddp"]`},
		{name: "unknown engine", body: `"rayVersion":"2.56.1","supportedEngines":["pytorch"]`},
		{name: "empty engines", body: `"rayVersion":"2.56.1","supportedEngines":[]`},
		{name: "duplicate engines", body: `"rayVersion":"2.56.1","supportedEngines":["ray-ddp","ray-ddp"]`},
		{name: "legacy Ray Train", body: `"rayVersion":"2.35.0","supportedEngines":["ray-train"]`},
		{name: "empty engine is not normalized", body: `"rayVersion":"2.56.1","supportedEngines":[""]`},
	}
	principal := auth.Principal{
		Subject: "team-admin", TenantID: "team-a", Roles: []string{domain.RoleTenantAdmin}, AuthType: auth.AuthTypeLocal,
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &compatibilityImageStore{}
			body := `{"name":"invalid","reference":"registry.example/runtime:invalid","kind":"training",` + test.body + `}`
			response := postImage(t, store, principal, body)
			if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), `"code":"INVALID_IMAGE"`) {
				t.Fatalf("invalid compatibility status=%d body=%s", response.Code, response.Body.String())
			}
			if store.created.ID != "" {
				t.Fatalf("invalid compatibility reached persistence: %+v", store.created)
			}
		})
	}
}

func TestImageCompatibilityLegacyPayloadDefaultsOmittedOrNullMetadata(t *testing.T) {
	principal := auth.Principal{
		Subject: "team-admin", TenantID: "team-a", Roles: []string{domain.RoleTenantAdmin}, AuthType: auth.AuthTypeLocal,
	}
	tests := []struct {
		name string
		body string
	}{
		{name: "omitted", body: `{"name":"legacy","reference":"registry.example/runtime:legacy","kind":"training"}`},
		{name: "null", body: `{"name":"legacy","reference":"registry.example/runtime:legacy","kind":"training","rayVersion":null,"supportedEngines":null}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &compatibilityImageStore{}
			response := postImage(t, store, principal, test.body)
			if response.Code != http.StatusCreated {
				t.Fatalf("legacy payload status=%d body=%s", response.Code, response.Body.String())
			}
			wantEngines := []domain.TrainingEngine{domain.TrainingEngineRayDDP}
			if store.created.RayVersion != domain.RayVersionLegacy || !reflect.DeepEqual(store.created.SupportedEngines, wantEngines) {
				t.Fatalf("legacy defaults not persisted: %+v", store.created)
			}
			var envelope struct {
				Data domain.PlatformImage `json:"data"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
				t.Fatalf("decode legacy response: %v", err)
			}
			if envelope.Data.RayVersion != domain.RayVersionLegacy || !reflect.DeepEqual(envelope.Data.SupportedEngines, wantEngines) {
				t.Fatalf("legacy defaults not returned: %+v", envelope.Data)
			}
		})
	}
}

func TestImageCompatibilityDefaultsOmittedFieldsIndependently(t *testing.T) {
	principal := auth.Principal{
		Subject: "team-admin", TenantID: "team-a", Roles: []string{domain.RoleTenantAdmin}, AuthType: auth.AuthTypeLocal,
	}
	tests := []struct {
		name        string
		metadata    string
		wantStatus  int
		wantVersion string
		wantEngines []domain.TrainingEngine
	}{
		{
			name: "omitted engines use ray-ddp", metadata: `"rayVersion":"2.56.1"`, wantStatus: http.StatusCreated,
			wantVersion: domain.RayVersionProduction, wantEngines: []domain.TrainingEngine{domain.TrainingEngineRayDDP},
		},
		{
			name: "omitted version uses legacy", metadata: `"supportedEngines":["ray-ddp"]`, wantStatus: http.StatusCreated,
			wantVersion: domain.RayVersionLegacy, wantEngines: []domain.TrainingEngine{domain.TrainingEngineRayDDP},
		},
		{
			name: "omitted version still validates compatibility", metadata: `"supportedEngines":["ray-train"]`, wantStatus: http.StatusBadRequest,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &compatibilityImageStore{}
			body := `{"name":"mixed","reference":"registry.example/runtime:mixed","kind":"training",` + test.metadata + `}`
			response := postImage(t, store, principal, body)
			if response.Code != test.wantStatus {
				t.Fatalf("mixed omission status=%d want=%d body=%s", response.Code, test.wantStatus, response.Body.String())
			}
			if test.wantStatus == http.StatusCreated && (store.created.RayVersion != test.wantVersion || !reflect.DeepEqual(store.created.SupportedEngines, test.wantEngines)) {
				t.Fatalf("mixed omission defaults = %+v", store.created)
			}
			if test.wantStatus == http.StatusBadRequest && !strings.Contains(response.Body.String(), `"code":"INVALID_IMAGE"`) {
				t.Fatalf("mixed omission did not return INVALID_IMAGE: %s", response.Body.String())
			}
		})
	}
}

func TestImageCompatibilityExplicitEmptyMetadataRemainsInvalid(t *testing.T) {
	principal := auth.Principal{
		Subject: "team-admin", TenantID: "team-a", Roles: []string{domain.RoleTenantAdmin}, AuthType: auth.AuthTypeLocal,
	}
	tests := []struct {
		name     string
		metadata string
	}{
		{name: "empty version", metadata: `"rayVersion":""`},
		{name: "empty engines", metadata: `"supportedEngines":[]`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &compatibilityImageStore{}
			body := `{"name":"empty","reference":"registry.example/runtime:empty","kind":"training",` + test.metadata + `}`
			response := postImage(t, store, principal, body)
			if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), `"code":"INVALID_IMAGE"`) {
				t.Fatalf("explicit empty metadata status=%d body=%s", response.Code, response.Body.String())
			}
			if store.created.ID != "" {
				t.Fatalf("explicit empty metadata reached persistence: %+v", store.created)
			}
		})
	}
}
