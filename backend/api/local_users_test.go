package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
	"ray-train-platform-backend/objectstore"
)

type fakePersonalDataInitializer struct {
	principal auth.Principal
	err       error
}

type fakePersonalStorageQuotaManager struct {
	prepared      int
	ensuredTenant string
	ensuredUser   string
	ensuredBytes  int64
	setTenant     string
	setUser       string
	setBytes      int64
	err           error
}

func (manager *fakePersonalStorageQuotaManager) PrepareBucket(context.Context) error {
	manager.prepared++
	return manager.err
}

func (manager *fakePersonalStorageQuotaManager) EnsurePersonalQuota(_ context.Context, tenantID, userID string, bytes int64) (objectstore.PersonalStorageQuota, error) {
	manager.ensuredTenant, manager.ensuredUser, manager.ensuredBytes = tenantID, userID, bytes
	if manager.err != nil {
		return objectstore.PersonalStorageQuota{}, manager.err
	}
	if bytes == 0 {
		bytes = 100 * objectstore.GiB
	}
	return objectstore.PersonalStorageQuota{Bytes: bytes, Enforced: true}, nil
}

func (manager *fakePersonalStorageQuotaManager) SetPersonalQuota(_ context.Context, tenantID, userID string, bytes int64) (objectstore.PersonalStorageQuota, error) {
	manager.setTenant, manager.setUser, manager.setBytes = tenantID, userID, bytes
	if manager.err != nil {
		return objectstore.PersonalStorageQuota{}, manager.err
	}
	return objectstore.PersonalStorageQuota{Bytes: bytes, Enforced: true}, nil
}

func (manager *fakePersonalStorageQuotaManager) GetPersonalQuota(context.Context, string, string) (objectstore.PersonalStorageQuota, error) {
	return objectstore.PersonalStorageQuota{Bytes: 100 * objectstore.GiB, Enforced: true}, manager.err
}

func (initializer *fakePersonalDataInitializer) EnsurePersonalDataSpace(_ context.Context, principal auth.Principal) error {
	initializer.principal = principal
	return initializer.err
}

func localAuthHandlerWithInitializer(store LocalAuthStore, initializer PersonalDataInitializer) *LocalAuthHandler {
	handler := localAuthHandler(store)
	handler.personalDataInitializer = initializer
	return handler
}

func localUserAdminRouter(handler *LocalAuthHandler, principal auth.Principal) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("ray-platform-principal", principal)
		c.Next()
	})
	handler.RegisterUserAdminRoutes(router.Group("/api/v1"))
	return router
}

func userAdminRouter(store LocalAuthStore, principal auth.Principal) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("ray-platform-principal", principal)
		c.Next()
	})
	localAuthHandler(store).RegisterUserAdminRoutes(router.Group("/api/v1"))
	return router
}

func adminPrincipal() auth.Principal {
	return auth.Principal{Subject: "admin-1", Username: "admin", TenantID: "team-a",
		Roles: []string{domain.RoleTenantAdmin}, AuthType: auth.AuthTypeLocal}
}

func engineerPrincipal() auth.Principal {
	return auth.Principal{Subject: "eng-1", Username: "bob", TenantID: "team-a",
		Roles: []string{domain.RoleEngineer}, AuthType: auth.AuthTypeLocal}
}

func superAdminPrincipal() auth.Principal {
	return auth.Principal{Subject: "super-1", Username: "root", TenantID: "platform",
		Roles: []string{domain.RoleSuperAdmin}, AuthType: auth.AuthTypeLocal}
}

func postUser(router *gin.Engine, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, "/api/v1/local-users", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

func postUserAction(router *gin.Engine, path, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

type fakeLocalUserDecommissionStore struct {
	*fakeLocalAuthStore
	decommissioned []string
	err            error
}

func (store *fakeLocalUserDecommissionStore) DecommissionLocalUser(_ context.Context, userID string, _ time.Time) error {
	if store.err != nil {
		return store.err
	}
	store.decommissioned = append(store.decommissioned, userID)
	return store.SetLocalUserDisabled(context.Background(), userID, true)
}

func TestAdminCanDecommissionAnInactiveLocalAccountWithoutDeletingItsData(t *testing.T) {
	store := &fakeLocalUserDecommissionStore{fakeLocalAuthStore: newFakeLocalAuthStore().withUser(t, "alice", "correct-horse", false)}
	router := localUserAdminRouter(localAuthHandler(store), adminPrincipal())
	request := httptest.NewRequest(http.MethodDelete, "/api/v1/local-users/user-alice", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusOK || len(store.decommissioned) != 1 || !store.disabled["user-alice"] {
		t.Fatalf("unexpected decommission result code=%d decommissioned=%#v disabled=%#v body=%s", response.Code, store.decommissioned, store.disabled, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), "storageRetained") || len(store.revokedAll) != 1 {
		t.Fatalf("decommission must retain storage and revoke sessions: %s revoked=%#v", response.Body.String(), store.revokedAll)
	}
}

func TestAdminCreatesEngineerAccountInOwnTenant(t *testing.T) {
	store := newFakeLocalAuthStore()
	response := postUser(userAdminRouter(store, adminPrincipal()),
		`{"username":"alice","password":"engineer-pass","roles":["Engineer"]}`)

	if response.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d body=%s", response.Code, response.Body.String())
	}
	created := store.users["alice"]
	if created.TenantID != "team-a" {
		t.Fatalf("new account must land in the admin's tenant, got %+v", created)
	}
	if len(created.Roles) != 1 || created.Roles[0] != domain.RoleEngineer {
		t.Fatalf("unexpected roles: %+v", created.Roles)
	}
	if !domain.VerifyPassword(created.PasswordHash, "engineer-pass") {
		t.Fatalf("password was not stored as a usable bcrypt hash")
	}
	// The response must never echo the password back.
	if strings.Contains(response.Body.String(), "engineer-pass") {
		t.Fatalf("response leaked the password: %s", response.Body.String())
	}
}

func TestAdminCreatesAccountWithFinitePersonalStorageQuota(t *testing.T) {
	store := newFakeLocalAuthStore()
	quota := &fakePersonalStorageQuotaManager{}
	handler := localAuthHandler(store)
	handler.personalStorageQuota = quota
	handler.personalStorageQuotaEnabled = true
	response := postUser(localUserAdminRouter(handler, adminPrincipal()),
		`{"username":"alice","password":"engineer-pass","roles":["Engineer"],"storageQuotaGiB":200}`)

	if response.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d body=%s", response.Code, response.Body.String())
	}
	if quota.ensuredTenant != "team-a" || quota.ensuredUser == "" || quota.ensuredBytes != 200*objectstore.GiB {
		t.Fatalf("unexpected managed quota: %#v", quota)
	}
}

func TestAdminCanExpandManagedPersonalStorageQuota(t *testing.T) {
	store := newFakeLocalAuthStore().withUser(t, "alice", "correct-horse", false)
	quota := &fakePersonalStorageQuotaManager{}
	handler := localAuthHandler(store)
	handler.personalStorageQuota = quota
	handler.personalStorageQuotaEnabled = true
	router := localUserAdminRouter(handler, adminPrincipal())

	response := postUserAction(router, "/api/v1/local-users/user-alice/storage-quota", `{"storageQuotaGiB":300}`)
	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", response.Code, response.Body.String())
	}
	if quota.setTenant != "team-a" || quota.setUser != "user-alice" || quota.setBytes != 300*objectstore.GiB {
		t.Fatalf("unexpected quota update: %#v", quota)
	}
}

func TestAdminCannotSetUnlimitedPersonalStorageQuota(t *testing.T) {
	store := newFakeLocalAuthStore().withUser(t, "alice", "correct-horse", false)
	quota := &fakePersonalStorageQuotaManager{}
	handler := localAuthHandler(store)
	handler.personalStorageQuota = quota
	handler.personalStorageQuotaEnabled = true
	router := localUserAdminRouter(handler, adminPrincipal())

	response := postUserAction(router, "/api/v1/local-users/user-alice/storage-quota", `{"storageQuotaGiB":0}`)
	if response.Code != http.StatusBadRequest || quota.setBytes != 0 {
		t.Fatalf("expected unlimited quota rejection, got %d manager=%#v", response.Code, quota)
	}
}

func TestOnlySuperAdminCanInitializeTheBucketWideObjectSetLayout(t *testing.T) {
	quota := &fakePersonalStorageQuotaManager{}
	for name, principal := range map[string]auth.Principal{
		"tenant admin": adminPrincipal(),
		"super admin":  superAdminPrincipal(),
	} {
		t.Run(name, func(t *testing.T) {
			handler := localAuthHandler(newFakeLocalAuthStore())
			handler.personalStorageQuota = quota
			handler.personalStorageQuotaEnabled = true
			router := localUserAdminRouter(handler, principal)
			response := postUserAction(router, "/api/v1/storage-governance/objectset/prepare", `{}`)
			if principal.HasRole(domain.RoleSuperAdmin) {
				if response.Code != http.StatusOK {
					t.Fatalf("expected super admin to initialize ObjectSet, got %d body=%s", response.Code, response.Body.String())
				}
			} else if response.Code != http.StatusForbidden {
				t.Fatalf("expected tenant admin rejection, got %d body=%s", response.Code, response.Body.String())
			}
		})
	}
	if quota.prepared != 1 {
		t.Fatalf("bucket-wide preparation must be called exactly once, got %d", quota.prepared)
	}
}

func TestAdminCreatesAccountOnlyAfterPersonalDataSpaceIsInitialized(t *testing.T) {
	store := newFakeLocalAuthStore()
	initializer := &fakePersonalDataInitializer{}
	response := postUser(localUserAdminRouter(localAuthHandlerWithInitializer(store, initializer), adminPrincipal()),
		`{"username":"alice","password":"engineer-pass","roles":["Engineer"]}`)

	if response.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d body=%s", response.Code, response.Body.String())
	}
	if initializer.principal.Subject == "" || initializer.principal.Username != "alice" || initializer.principal.TenantID != "team-a" || len(initializer.principal.Roles) != 1 || initializer.principal.Roles[0] != domain.RoleEngineer {
		t.Fatalf("personal data initialization must receive the new governed identity, got %+v", initializer.principal)
	}
	if _, exists := store.users["alice"]; !exists {
		t.Fatalf("account must be persisted after its personal data space is initialized")
	}
}

func TestAdminCannotCreateAccountWhenPersonalDataSpaceInitializationFails(t *testing.T) {
	store := newFakeLocalAuthStore()
	initializer := &fakePersonalDataInitializer{err: objectstore.ErrUnavailable}
	response := postUser(localUserAdminRouter(localAuthHandlerWithInitializer(store, initializer), adminPrincipal()),
		`{"username":"alice","password":"engineer-pass","roles":["Engineer"]}`)

	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "PERSONAL_DATA_SPACE_INITIALIZATION_FAILED") {
		t.Fatalf("expected retryable initialization failure, got %d body=%s", response.Code, response.Body.String())
	}
	if _, exists := store.users["alice"]; exists {
		t.Fatalf("an unusable account must not be persisted")
	}
	if !errors.Is(initializer.err, objectstore.ErrUnavailable) {
		t.Fatalf("test setup lost storage failure: %v", initializer.err)
	}
}

func TestPersonalDataSpaceInitializerCreatesOnlyFixedTOSMarkers(t *testing.T) {
	directories := &fakePersonalDataDirectoryInitializer{}
	initializer := NewPersonalDataSpaceInitializer(directories)
	err := initializer.EnsurePersonalDataSpace(context.Background(), auth.Principal{
		Subject: "user-a", TenantID: "tenant-a", AuthType: auth.AuthTypeLocal,
	})
	if err != nil {
		t.Fatalf("initialize personal data space: %v", err)
	}
	if directories.root != "ray-train/tenants/tenant-a/users/user-a/" {
		t.Fatalf("initializer must derive only the governed personal root, got %q", directories.root)
	}
}

func TestEngineerCannotCreateAccounts(t *testing.T) {
	store := newFakeLocalAuthStore()
	response := postUser(userAdminRouter(store, engineerPrincipal()),
		`{"username":"mallory","password":"engineer-pass","roles":["SuperAdmin"]}`)

	if response.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", response.Code)
	}
	if len(store.users) != 0 {
		t.Fatalf("no account may be created by a non-admin")
	}
}

// A tenant admin must not be able to mint an account outside their own tenant.
func TestTenantAdminCannotCreateAccountInAnotherTenant(t *testing.T) {
	store := newFakeLocalAuthStore()
	response := postUser(userAdminRouter(store, adminPrincipal()),
		`{"username":"alice","password":"engineer-pass","roles":["Engineer"],"tenantId":"team-b"}`)

	if response.Code != http.StatusForbidden || len(store.users) != 0 {
		t.Fatalf("tenant admin must not create users in another tenant: status=%d users=%+v body=%s", response.Code, store.users, response.Body.String())
	}
}

func TestSuperAdminCreatesTenantAdministratorInSelectedExistingTenant(t *testing.T) {
	store := newFakeLocalAuthStore()
	response := postUser(userAdminRouter(store, superAdminPrincipal()),
		`{"username":"team-b-admin","password":"engineer-pass","roles":["TenantAdmin"],"tenantId":"team-b"}`)

	if response.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d body=%s", response.Code, response.Body.String())
	}
	created := store.users["team-b-admin"]
	if created.TenantID != "team-b" || len(created.Roles) != 1 || created.Roles[0] != domain.RoleTenantAdmin {
		t.Fatalf("super admin must create the requested tenant admin in the selected tenant: %+v", created)
	}
}

func TestSuperAdminCannotCreateUserInUnknownTenant(t *testing.T) {
	store := newFakeLocalAuthStore()
	response := postUser(userAdminRouter(store, superAdminPrincipal()),
		`{"username":"orphan","password":"engineer-pass","roles":["Engineer"],"tenantId":"missing-team"}`)

	if response.Code != http.StatusBadRequest || len(store.users) != 0 {
		t.Fatalf("a user must not create an implicit tenant: status=%d users=%+v body=%s", response.Code, store.users, response.Body.String())
	}
}

func TestCreateUserRejectsWeakPasswordAndBadRole(t *testing.T) {
	store := newFakeLocalAuthStore()
	router := userAdminRouter(store, adminPrincipal())

	if response := postUser(router, `{"username":"alice","password":"short","roles":["Engineer"]}`); response.Code != http.StatusBadRequest {
		t.Fatalf("expected weak password rejection, got %d", response.Code)
	}
	if response := postUser(router, `{"username":"alice","password":"engineer-pass","roles":["root"]}`); response.Code != http.StatusBadRequest {
		t.Fatalf("expected unknown role rejection, got %d", response.Code)
	}
}

func TestTenantAdminCannotCreatePrivilegedAccount(t *testing.T) {
	store := newFakeLocalAuthStore()
	router := userAdminRouter(store, adminPrincipal())
	response := postUser(router, `{"username":"admin-two","password":"engineer-pass","roles":["TenantAdmin"]}`)
	if response.Code != http.StatusForbidden {
		t.Fatalf("tenant admin must not create privileged account, got %d body=%s", response.Code, response.Body.String())
	}
	if len(store.users) != 0 {
		t.Fatalf("privileged account must not be created: %+v", store.users)
	}
}

func TestTenantAdminCanResetEngineerButNotAdministrator(t *testing.T) {
	store := newFakeLocalAuthStore().withUser(t, "alice", "before-reset", false).withUser(t, "team-admin", "before-reset", false)
	tenantAdmin := store.users["team-admin"]
	tenantAdmin.Roles = []string{domain.RoleTenantAdmin}
	store.users["team-admin"] = tenantAdmin
	router := userAdminRouter(store, adminPrincipal())

	if response := postUserAction(router, "/api/v1/local-users/user-alice/reset-password", `{"newPassword":"after-reset"}`); response.Code != http.StatusOK {
		t.Fatalf("tenant admin should reset engineer password, got %d body=%s", response.Code, response.Body.String())
	}
	if !domain.VerifyPassword(store.users["alice"].PasswordHash, "after-reset") || len(store.revokedAll) != 1 {
		t.Fatalf("reset must change password and revoke all sessions: users=%+v revoked=%+v", store.users["alice"], store.revokedAll)
	}
	if response := postUserAction(router, "/api/v1/local-users/"+tenantAdmin.ID+"/reset-password", `{"newPassword":"after-reset"}`); response.Code != http.StatusForbidden {
		t.Fatalf("tenant admin must not reset another tenant admin, got %d body=%s", response.Code, response.Body.String())
	}
}

func TestDisableLocalUserRevokesSessionsAndPreventsLogin(t *testing.T) {
	store := newFakeLocalAuthStore().withUser(t, "alice", "correct-horse", false)
	router := userAdminRouter(store, adminPrincipal())
	response := postUserAction(router, "/api/v1/local-users/user-alice/disable", `{}`)
	if response.Code != http.StatusOK {
		t.Fatalf("expected disable success, got %d body=%s", response.Code, response.Body.String())
	}
	if !store.users["alice"].Disabled || len(store.revokedAll) != 1 {
		t.Fatalf("disable must persist and revoke sessions: %+v revoked=%+v", store.users["alice"], store.revokedAll)
	}
	if response := postLogin(localAuthHandler(store), `{"username":"alice","password":"correct-horse"}`); response.Code != http.StatusUnauthorized {
		t.Fatalf("disabled user must not sign in, got %d", response.Code)
	}
}

func TestListUsersReturnsOnlyOwnTenantForTenantAdmin(t *testing.T) {
	store := newFakeLocalAuthStore()
	store.users["alice"] = domain.LocalUser{ID: "u1", Username: "alice", TenantID: "team-a", Roles: []string{domain.RoleEngineer}}
	store.users["carol"] = domain.LocalUser{ID: "u2", Username: "carol", TenantID: "team-b", Roles: []string{domain.RoleEngineer}}

	router := userAdminRouter(store, adminPrincipal())
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/local-users", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", response.Code)
	}
	var envelope struct {
		Data []domain.LocalUser `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(envelope.Data) != 1 || envelope.Data[0].Username != "alice" {
		t.Fatalf("tenant admin must only see their own tenant, got %+v", envelope.Data)
	}
	if envelope.Data[0].PasswordHash != "" {
		t.Fatalf("password hash must never be serialized")
	}
}
