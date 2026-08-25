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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
	platformk8s "ray-train-platform-backend/k8s"
	"ray-train-platform-backend/objectstore"
)

type fakeDataSpaceStore struct {
	bindings []domain.DataMountBinding
	ensured  []domain.DataMountBinding
	shared   []domain.DataMountBinding
	root     []domain.DataMountBinding
	idc      []domain.DataMountBinding
}

func (store *fakeDataSpaceStore) EnsurePersonalDataBinding(_ context.Context, binding domain.DataMountBinding) (domain.DataMountBinding, error) {
	store.ensured = append(store.ensured, binding)
	for _, existing := range store.bindings {
		if existing.Scope == domain.DataMountScopePersonal && existing.TenantID == binding.TenantID && existing.UserID == binding.UserID {
			return existing, nil
		}
	}
	store.bindings = append(store.bindings, binding)
	return binding, nil
}

func (store *fakeDataSpaceStore) ListDataBindings(_ context.Context, tenantID, userID string) ([]domain.DataMountBinding, error) {
	visible := make([]domain.DataMountBinding, 0, len(store.bindings))
	for _, binding := range store.bindings {
		visibleForRequester := (binding.Scope == domain.DataMountScopePersonal && binding.TenantID == tenantID && binding.UserID == userID) ||
			((binding.Scope == domain.DataMountScopeTenant || binding.Scope == domain.DataMountScopeIDC) && binding.TenantID == tenantID && binding.UserID == "")
		if visibleForRequester {
			visible = append(visible, binding)
		}
	}
	return visible, nil
}

func (store *fakeDataSpaceStore) EnsureTenantSharedDataBindings(_ context.Context, bindings ...domain.DataMountBinding) ([]domain.DataMountBinding, error) {
	store.shared = append(store.shared, bindings...)
	for _, binding := range bindings {
		found := false
		for _, existing := range store.bindings {
			if existing.TenantID == binding.TenantID && existing.Scope == binding.Scope && existing.SpaceID == binding.SpaceID {
				found = true
				break
			}
		}
		if !found {
			store.bindings = append(store.bindings, binding)
		}
	}
	return bindings, nil
}

func (store *fakeDataSpaceStore) EnsureTenantRootDataBinding(_ context.Context, binding domain.DataMountBinding) (domain.DataMountBinding, error) {
	store.root = append(store.root, binding)
	for _, existing := range store.bindings {
		if existing.TenantID == binding.TenantID && existing.Scope == binding.Scope && existing.SpaceID == binding.SpaceID {
			return existing, nil
		}
	}
	store.bindings = append(store.bindings, binding)
	return binding, nil
}

func (store *fakeDataSpaceStore) EnsureIDCDataBindings(_ context.Context, bindings ...domain.DataMountBinding) ([]domain.DataMountBinding, error) {
	store.idc = append(store.idc, bindings...)
	result := make([]domain.DataMountBinding, 0, len(bindings))
	for _, binding := range bindings {
		for _, existing := range store.bindings {
			if existing.TenantID == binding.TenantID && existing.Scope == binding.Scope && existing.SpaceID == binding.SpaceID {
				result = append(result, existing)
				goto next
			}
		}
		store.bindings = append(store.bindings, binding)
		result = append(result, binding)
	next:
	}
	return result, nil
}

func dataSpaceRouter(handler *Handler, principal auth.Principal) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("ray-platform-principal", principal)
		c.Next()
	})
	handler.RegisterDataSpaceRoutes(router.Group("/api/v1"))
	return router
}

type fakeDataSpaceDirectoryLister struct {
	root   string
	path   string
	cursor string
	limit  int
	page   objectstore.DirectoryPage
}

func (lister *fakeDataSpaceDirectoryLister) ListDirectories(_ context.Context, rootPrefix, relativePath, cursor string, limit int) (objectstore.DirectoryPage, error) {
	lister.root, lister.path, lister.cursor, lister.limit = rootPrefix, relativePath, cursor, limit
	return lister.page, nil
}

type fakePersonalDataDirectoryInitializer struct {
	root  string
	err   error
	roots []string
}

func (initializer *fakePersonalDataDirectoryInitializer) EnsurePersonalDataDirectories(_ context.Context, rootPrefix string) error {
	initializer.root = rootPrefix
	return initializer.err
}

func (initializer *fakePersonalDataDirectoryInitializer) EnsureDataDirectory(_ context.Context, rootPrefix string) error {
	initializer.roots = append(initializer.roots, rootPrefix)
	return initializer.err
}

func TestDataSpacesListReturnsLogicalSpacesWithoutPersistingMountWhenMountingIsDisabled(t *testing.T) {
	store := &fakeDataSpaceStore{bindings: []domain.DataMountBinding{
		{ID: "team-a", TenantID: "tenant-a", Scope: domain.DataMountScopeTenant, SpaceID: domain.DataSpaceTeamShared, Status: domain.DataMountBindingReady, ReadOnly: true},
		{ID: "public", TenantID: "tenant-a", Scope: domain.DataMountScopeTenant, SpaceID: domain.DataSpacePublic, Status: domain.DataMountBindingReady, ReadOnly: true},
	}}
	repository := &fakeJobRepository{}
	handler := NewHandler(repository, Options{DataSpaces: store})
	handler.newID = func() (string, error) { return "personal-binding-a", nil }
	router := dataSpaceRouter(handler, auth.Principal{Subject: "user-a", TenantID: "tenant-a", Roles: []string{domain.RoleEngineer}, AuthType: auth.AuthTypeLocal})

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/data-spaces", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	for _, expected := range []string{"我的文件", "团队共享数据", "公共数据", "not-configured"} {
		if !strings.Contains(strings.ToLower(response.Body.String()), strings.ToLower(expected)) {
			t.Fatalf("response missing %q: %s", expected, response.Body.String())
		}
	}
	if strings.Contains(response.Body.String(), `"mountStatus":"ready"`) {
		t.Fatalf("disabled data mounts must not advertise a usable workload mount: %s", response.Body.String())
	}
	for _, forbidden := range []string{"rootPrefix", "claimName", "ray-train/tenants", "shanghai-data-transfer"} {
		if strings.Contains(response.Body.String(), forbidden) {
			t.Fatalf("response leaked %q: %s", forbidden, response.Body.String())
		}
	}
	if len(store.ensured) != 0 {
		t.Fatalf("disabled data mounts must not persist an incomplete binding: %#v", store.ensured)
	}
	if repository.identity != 1 {
		t.Fatalf("data-space initialization must persist the authenticated identity before creating its binding")
	}
}

func TestDataSpacesListReportsPrincipalWriteCapabilityWithoutChangingMountSemantics(t *testing.T) {
	tests := []struct {
		name     string
		roles    []string
		writable map[domain.DataSpaceID]bool
	}{
		{
			name:     "roleless principal",
			writable: map[domain.DataSpaceID]bool{},
		},
		{
			name:     "unrecognized role",
			roles:    []string{"Viewer"},
			writable: map[domain.DataSpaceID]bool{},
		},
		{
			name:  "engineer",
			roles: []string{domain.RoleEngineer},
			writable: map[domain.DataSpaceID]bool{
				domain.DataSpaceWorkspace: true,
				domain.DataSpaceMyStorage: true,
				domain.DataSpaceMyFiles:   true,
				domain.DataSpaceMyRuns:    true,
			},
		},
		{
			name:  "tenant admin",
			roles: []string{domain.RoleTenantAdmin},
			writable: map[domain.DataSpaceID]bool{
				domain.DataSpaceWorkspace:  true,
				domain.DataSpaceMyStorage:  true,
				domain.DataSpaceMyFiles:    true,
				domain.DataSpaceMyRuns:     true,
				domain.DataSpaceTeamShared: true,
			},
		},
		{
			name:  "super admin",
			roles: []string{domain.RoleSuperAdmin},
			writable: map[domain.DataSpaceID]bool{
				domain.DataSpaceWorkspace:  true,
				domain.DataSpaceMyStorage:  true,
				domain.DataSpaceMyFiles:    true,
				domain.DataSpaceMyRuns:     true,
				domain.DataSpaceTeamShared: true,
				domain.DataSpacePublic:     true,
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handler := NewHandler(&fakeJobRepository{}, Options{DataSpaces: &fakeDataSpaceStore{}})
			router := dataSpaceRouter(handler, auth.Principal{Subject: "user-a", TenantID: "tenant-a", Roles: test.roles, AuthType: auth.AuthTypeLocal})
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/data-spaces", nil))
			if response.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}

			var payload struct {
				Data []struct {
					ID       domain.DataSpaceID `json:"id"`
					Provider string             `json:"provider"`
					ReadOnly bool               `json:"readOnly"`
					CanWrite bool               `json:"canWrite"`
				} `json:"data"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if len(payload.Data) == 0 {
				t.Fatal("expected logical data spaces")
			}
			for _, space := range payload.Data {
				if got, want := space.CanWrite, test.writable[space.ID]; got != want {
					t.Errorf("space %s canWrite=%v want=%v", space.ID, got, want)
				}
				if space.ID == domain.DataSpaceTeamShared && !space.ReadOnly {
					t.Error("team publishing capability must not make the Pod mount writable")
				}
				if space.Provider == domain.StorageProviderIDC && (space.CanWrite || !space.ReadOnly) {
					t.Errorf("IDC space must remain read-only: %#v", space)
				}
			}
			for _, forbidden := range []string{"rootPrefix", "claimName", "bucket", "credentials", "ray-train/tenants"} {
				if strings.Contains(response.Body.String(), forbidden) {
					t.Fatalf("response leaked %q: %s", forbidden, response.Body.String())
				}
			}
		})
	}
}

func TestDataSpacesShowObjectStorageReadyWhenTOSBrowserIsAvailableButMountIsDisabled(t *testing.T) {
	store := &fakeDataSpaceStore{}
	handler := NewHandler(&fakeJobRepository{}, Options{DataSpaces: store, DataObjectStore: &fakeDataSpaceObjectStore{}})
	router := dataSpaceRouter(handler, auth.Principal{Subject: "user-a", TenantID: "tenant-a", Roles: []string{domain.RoleEngineer}, AuthType: auth.AuthTypeLocal})

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/data-spaces", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), `"storageStatus":"ready"`) || !strings.Contains(response.Body.String(), `"mountStatus":"not-configured"`) {
		t.Fatalf("object-store and GPU-mount states must be distinct: %s", response.Body.String())
	}
	for _, forbidden := range []string{"ray-train/tenants", "claimName", "shanghai-data-transfer"} {
		if strings.Contains(response.Body.String(), forbidden) {
			t.Fatalf("response leaked %q: %s", forbidden, response.Body.String())
		}
	}
}

func TestDataSpacesListBackfillsExistingPersonalObjectRootWithoutEnablingGPUMount(t *testing.T) {
	initializer := &fakePersonalDataDirectoryInitializer{}
	handler := NewHandler(&fakeJobRepository{}, Options{DataSpaces: &fakeDataSpaceStore{}, DataObjectStore: &fakeDataSpaceObjectStore{}, DirectoryInitializer: initializer})
	router := dataSpaceRouter(handler, auth.Principal{Subject: "user-a", TenantID: "tenant-a", Roles: []string{domain.RoleEngineer}, AuthType: auth.AuthTypeLocal})

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/data-spaces", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if initializer.root != "ray-train/tenants/tenant-a/users/user-a/" {
		t.Fatalf("existing identity must receive the same governed object root, got %q", initializer.root)
	}
	if strings.Contains(response.Body.String(), `"mountStatus":"ready"`) {
		t.Fatalf("object root initialization must not claim the disabled GPU mount is ready: %s", response.Body.String())
	}
}

func TestDataSpacesListStopsWhenIdentityPersistenceFails(t *testing.T) {
	store := &fakeDataSpaceStore{}
	repository := &fakeJobRepository{identityErr: context.DeadlineExceeded}
	handler := NewHandler(repository, Options{DataSpaces: store})
	router := dataSpaceRouter(handler, auth.Principal{Subject: "user-a", TenantID: "tenant-a", Roles: []string{domain.RoleEngineer}, AuthType: auth.AuthTypeLocal})

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/data-spaces", nil))
	if response.Code != http.StatusInternalServerError || len(store.ensured) != 0 || !strings.Contains(response.Body.String(), "DATA_SPACE_IDENTITY_PERSIST_FAILED") {
		t.Fatalf("identity persistence failure must stop mount initialization: status=%d ensured=%#v body=%s", response.Code, store.ensured, response.Body.String())
	}
}

func TestDataSpacesListDoesNotUseAnotherUsersBinding(t *testing.T) {
	store := &fakeDataSpaceStore{bindings: []domain.DataMountBinding{
		{ID: "other", TenantID: "tenant-a", UserID: "user-b", Scope: domain.DataMountScopePersonal, SpaceID: domain.DataSpaceWorkspace, Status: domain.DataMountBindingReady},
	}}
	handler := NewHandler(&fakeJobRepository{}, Options{DataSpaces: store})
	handler.newID = func() (string, error) { return "mine", nil }
	router := dataSpaceRouter(handler, auth.Principal{Subject: "user-a", TenantID: "tenant-a", Roles: []string{domain.RoleEngineer}, AuthType: auth.AuthTypeLocal})

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/data-spaces", nil))
	if response.Code != http.StatusOK || strings.Contains(response.Body.String(), "other") {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestDataSpacesListInitializesPersonalBindingOnlyWhenMountingIsEnabled(t *testing.T) {
	store := &fakeDataSpaceStore{}
	initializer := &fakePersonalDataDirectoryInitializer{}
	handler := NewHandler(&fakeJobRepository{}, Options{DataSpaces: store, Kubernetes: platformk8s.NewClientFromInterfaces(nil, k8sfake.NewSimpleClientset()), DataSpacesEnabled: true, DataSpacesFSXAttributes: `{"type":"TOS","bucket":"shanghai-data-transfer","server":"tos-cn-shanghai.ivolces.com","region":"cn-shanghai"}`, DataSpacesMountCapacity: "1Ti", DirectoryInitializer: initializer})
	handler.newID = func() (string, error) { return "mount-user-a", nil }
	router := dataSpaceRouter(handler, auth.Principal{Subject: "user-a", TenantID: "tenant-a", Roles: []string{domain.RoleEngineer}, AuthType: auth.AuthTypeLocal})

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/data-spaces", nil))
	if response.Code != http.StatusOK || len(store.ensured) != 1 {
		t.Fatalf("status=%d ensured=%#v body=%s", response.Code, store.ensured, response.Body.String())
	}
	binding := store.ensured[0]
	if binding.ClaimName != "data-mount-user-a" || binding.RootPrefix != "ray-train/tenants/tenant-a/users/user-a/" || binding.VolumeAttributesJSON == "" {
		t.Fatalf("personal binding was not derived safely: %#v", binding)
	}
	if initializer.root != binding.RootPrefix {
		t.Fatalf("personal data root was not initialized before its mount: got=%q want=%q", initializer.root, binding.RootPrefix)
	}
}

func TestDataSpacesListInitializesTenantLocalSharedAndPublicMounts(t *testing.T) {
	store := &fakeDataSpaceStore{}
	initializer := &fakePersonalDataDirectoryInitializer{}
	handler := NewHandler(&fakeJobRepository{}, Options{DataSpaces: store, Kubernetes: platformk8s.NewClientFromInterfaces(nil, k8sfake.NewSimpleClientset()), DataSpacesEnabled: true, DataSpacesFSXAttributes: `{"type":"TOS","bucket":"shanghai-data-transfer","server":"tos-cn-shanghai.ivolces.com","region":"cn-shanghai"}`, DataSpacesMountCapacity: "1Ti", DirectoryInitializer: initializer})
	handler.newID = func() (string, error) { return "mount-user-a", nil }
	router := dataSpaceRouter(handler, auth.Principal{Subject: "user-a", TenantID: "tenant-a", Roles: []string{domain.RoleEngineer}, AuthType: auth.AuthTypeLocal})

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/data-spaces", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if len(store.shared) != 2 || store.shared[0].SpaceID != domain.DataSpaceTeamShared || store.shared[1].SpaceID != domain.DataSpacePublic {
		t.Fatalf("shared data spaces were not initialized: %#v", store.shared)
	}
	for _, binding := range store.shared {
		if binding.TenantID != "tenant-a" || binding.Scope != domain.DataMountScopeTenant || !binding.ReadOnly || binding.ClaimName == "" {
			t.Fatalf("shared binding has an unsafe tenant-mount contract: %#v", binding)
		}
	}
	if len(store.root) != 1 || store.root[0].SpaceID != domain.DataSpaceTenantStorageRoot || store.root[0].ReadOnly || store.root[0].RootPrefix != "ray-train/" {
		t.Fatalf("tenant TOS root was not initialized as a writable internal adapter: %#v", store.root)
	}
	if store.shared[0].ID == "team-tenant-a" || store.shared[0].ClaimName == "data-team-tenant-a" || store.shared[1].ID == "public-tenant-a" || store.shared[1].ClaimName == "data-public-tenant-a" || store.shared[0].ID == store.shared[1].ID || len(store.shared[0].ID) > 40 || len(store.shared[1].ID) > 40 {
		t.Fatalf("shared bindings must have stable independent identities: %#v", store.shared)
	}
	if got, want := initializer.roots, []string{"ray-train/", "ray-train/tenants/tenant-a/shared/", "ray-train/public/"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("shared roots were not initialized before mounting: got=%#v want=%#v", got, want)
	}
}

func TestDataSpaceBrowserUsesConfiguredTemporaryPublicRoot(t *testing.T) {
	lister := &fakeDataSpaceDirectoryLister{}
	handler := NewHandler(&fakeJobRepository{}, Options{
		DataSpaces:           &fakeDataSpaceStore{},
		DirectoryLister:      lister,
		DataSpacesPublicRoot: "ray-train/tenants/local/datasets/public",
	})
	router := dataSpaceRouter(handler, auth.Principal{Subject: "user-a", TenantID: "local", Roles: []string{domain.RoleEngineer}, AuthType: auth.AuthTypeLocal})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/data-spaces/public/directories?path=bevfusion", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if lister.root != "ray-train/tenants/local/datasets/public/" || lister.path != "bevfusion" {
		t.Fatalf("public browser root=%q path=%q, want the configured public root", lister.root, lister.path)
	}
}

func TestDataSpacesListInitializesOnlyConfiguredIDCReadOnlyMounts(t *testing.T) {
	store := &fakeDataSpaceStore{}
	handler := NewHandler(&fakeJobRepository{}, Options{
		DataSpaces: store, Kubernetes: platformk8s.NewClientFromInterfaces(nil, k8sfake.NewSimpleClientset()),
		IDCDataSpacesEnabled: true, IDCDataSpacesMountCapacity: "1Pi",
		IDCDataSpaceSources: map[domain.DataSpaceID]platformk8s.IDCDataMountSource{
			domain.DataSpaceIDCOriginal:    {Server: "192.0.2.10", Path: "/exports/original"},
			domain.DataSpaceIDCWellspiking: {Server: "192.0.2.11", Path: "/exports/wellspiking"},
			domain.DataSpaceIDCShared:      {Server: "storage.example.internal", Path: "/exports/shared"},
		},
	})
	router := dataSpaceRouter(handler, auth.Principal{Subject: "user-a", TenantID: "tenant-a", Roles: []string{domain.RoleEngineer}, AuthType: auth.AuthTypeLocal})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/data-spaces", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if len(store.idc) != 3 {
		t.Fatalf("configured IDC spaces were not initialized: %#v", store.idc)
	}
	for _, binding := range store.idc {
		if binding.Scope != domain.DataMountScopeIDC || !binding.ReadOnly || binding.UserID != "" || binding.ClaimName == "" {
			t.Fatalf("unsafe IDC binding: %#v", binding)
		}
	}
	for _, forbidden := range []string{"192.0.2.10", "/exports/original", "claimName"} {
		if strings.Contains(response.Body.String(), forbidden) {
			t.Fatalf("IDC response leaked internal mount implementation %q: %s", forbidden, response.Body.String())
		}
	}
}

func TestDataSpacesListPreparesTenantNamespaceBeforeCreatingMounts(t *testing.T) {
	platformNamespace := "ray-train-platform"
	core := k8sfake.NewSimpleClientset()
	handler := NewHandler(&fakeJobRepository{}, Options{
		DataSpaces: &fakeDataSpaceStore{}, Kubernetes: platformk8s.NewClientFromInterfaces(nil, core), PlatformNamespace: platformNamespace,
		IDCDataSpacesEnabled: true, IDCDataSpacesMountCapacity: "1Pi",
		IDCDataSpaceSources: map[domain.DataSpaceID]platformk8s.IDCDataMountSource{
			domain.DataSpaceIDCOriginal:    {Server: "192.0.2.10", Path: "/exports/original"},
			domain.DataSpaceIDCWellspiking: {Server: "192.0.2.11", Path: "/exports/wellspiking"},
			domain.DataSpaceIDCShared:      {Server: "storage.example.internal", Path: "/exports/shared"},
		},
	})
	router := dataSpaceRouter(handler, auth.Principal{Subject: "user-a", TenantID: "tenant-a", Roles: []string{domain.RoleEngineer}, AuthType: auth.AuthTypeLocal})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/data-spaces", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	namespace, err := core.CoreV1().Namespaces().Get(context.Background(), "tenant-tenant-a", metav1.GetOptions{})
	if err != nil || namespace.Labels["ray.io/tenant-id"] != "tenant-a" {
		t.Fatalf("tenant namespace was not prepared before data mounts: namespace=%#v err=%v", namespace, err)
	}
}

func TestDataSpacesListRetriesARecoverableFailedIDCBinding(t *testing.T) {
	store := &fakeDataSpaceStore{bindings: []domain.DataMountBinding{
		{ID: "idc-original-9e6cc20568b7", TenantID: "tenant-a", Scope: domain.DataMountScopeIDC, SpaceID: domain.DataSpaceIDCOriginal, ClaimName: "idc-original-9e6cc20568b7", ReadOnly: true, Status: domain.DataMountBindingFailed},
		{ID: "idc-wellspiking-9e6cc20568b7", TenantID: "tenant-a", Scope: domain.DataMountScopeIDC, SpaceID: domain.DataSpaceIDCWellspiking, ClaimName: "idc-wellspiking-9e6cc20568b7", ReadOnly: true, Status: domain.DataMountBindingPending},
		{ID: "idc-shared-9e6cc20568b7", TenantID: "tenant-a", Scope: domain.DataMountScopeIDC, SpaceID: domain.DataSpaceIDCShared, ClaimName: "idc-shared-9e6cc20568b7", ReadOnly: true, Status: domain.DataMountBindingPending},
	}}
	core := k8sfake.NewSimpleClientset()
	handler := NewHandler(&fakeJobRepository{}, Options{
		DataSpaces: store, Kubernetes: platformk8s.NewClientFromInterfaces(nil, core),
		IDCDataSpacesEnabled: true, IDCDataSpacesMountCapacity: "1Pi",
		IDCDataSpaceSources: map[domain.DataSpaceID]platformk8s.IDCDataMountSource{
			domain.DataSpaceIDCOriginal:    {Server: "192.0.2.10", Path: "/exports/original"},
			domain.DataSpaceIDCWellspiking: {Server: "192.0.2.11", Path: "/exports/wellspiking"},
			domain.DataSpaceIDCShared:      {Server: "storage.example.internal", Path: "/exports/shared"},
		},
	})
	router := dataSpaceRouter(handler, auth.Principal{Subject: "user-a", TenantID: "tenant-a", Roles: []string{domain.RoleEngineer}, AuthType: auth.AuthTypeLocal})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/data-spaces", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if _, err := core.CoreV1().PersistentVolumes().Get(context.Background(), "ray-idc-idc-original-9e6cc20568b7", metav1.GetOptions{}); err != nil {
		t.Fatalf("failed binding was not retried as its owned static volume: %v", err)
	}
}

func TestDataSpacesListReturnsUnavailableWhenStoreIsMissing(t *testing.T) {
	handler := NewHandler(&fakeJobRepository{}, Options{})
	router := dataSpaceRouter(handler, auth.Principal{Subject: "user-a", TenantID: "tenant-a", Roles: []string{domain.RoleEngineer}, AuthType: auth.AuthTypeLocal})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/data-spaces", nil))
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "DATA_SPACES_UNAVAILABLE") {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestDataSpaceDirectoryRouteUsesCurrentUsersLogicalRoot(t *testing.T) {
	store := &fakeDataSpaceStore{}
	lister := &fakeDataSpaceDirectoryLister{page: objectstore.DirectoryPage{Directories: []string{"images", "labels"}, NextCursor: "opaque"}}
	handler := NewHandler(&fakeJobRepository{}, Options{DataSpaces: store, DirectoryLister: lister})
	handler.newID = func() (string, error) { return "mine", nil }
	router := dataSpaceRouter(handler, auth.Principal{Subject: "user-a", TenantID: "tenant-a", Roles: []string{domain.RoleEngineer}, AuthType: auth.AuthTypeLocal})

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/data-spaces/my-files/directories?path=train-v1&cursor=opaque", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if got, want := lister.root, "ray-train/tenants/tenant-a/users/user-a/files/"; got != want {
		t.Fatalf("root=%q want=%q", got, want)
	}
	if lister.path != "train-v1" || lister.cursor != "opaque" || lister.limit != 100 {
		t.Fatalf("unexpected list input: %#v", lister)
	}
	if strings.Contains(response.Body.String(), lister.root) || strings.Contains(response.Body.String(), "shanghai-data-transfer") {
		t.Fatalf("directory response leaked backing storage: %s", response.Body.String())
	}
}

func TestPersonalStorageDirectoryRouteUsesThePersonalRootWithoutAHiddenFilesSuffix(t *testing.T) {
	store := &fakeDataSpaceStore{}
	lister := &fakeDataSpaceDirectoryLister{page: objectstore.DirectoryPage{Directories: []string{"files", "runs"}}}
	handler := NewHandler(&fakeJobRepository{}, Options{DataSpaces: store, DirectoryLister: lister})
	handler.newID = func() (string, error) { return "mine", nil }
	router := dataSpaceRouter(handler, auth.Principal{Subject: "user-a", TenantID: "tenant-a", Roles: []string{domain.RoleEngineer}, AuthType: auth.AuthTypeLocal})

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/data-spaces/my-storage/directories", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if got, want := lister.root, "ray-train/tenants/tenant-a/users/user-a/"; got != want {
		t.Fatalf("root=%q want=%q", got, want)
	}
}

func TestDataSpaceDirectoryRouteRejectsTraversalBeforeStorage(t *testing.T) {
	store := &fakeDataSpaceStore{}
	lister := &fakeDataSpaceDirectoryLister{}
	handler := NewHandler(&fakeJobRepository{}, Options{DataSpaces: store, DirectoryLister: lister})
	handler.newID = func() (string, error) { return "mine", nil }
	router := dataSpaceRouter(handler, auth.Principal{Subject: "user-a", TenantID: "tenant-a", Roles: []string{domain.RoleEngineer}, AuthType: auth.AuthTypeLocal})

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/data-spaces/my-files/directories?path=../user-b", nil))
	if response.Code != http.StatusBadRequest || lister.root != "" {
		t.Fatalf("status=%d lister=%#v body=%s", response.Code, lister, response.Body.String())
	}
}

func TestDataSpaceDirectoryRouteDeclinesIDCWebBrowsing(t *testing.T) {
	handler := NewHandler(&fakeJobRepository{}, Options{DataSpaces: &fakeDataSpaceStore{}})
	handler.newID = func() (string, error) { return "mine", nil }
	router := dataSpaceRouter(handler, auth.Principal{Subject: "user-a", TenantID: "tenant-a", Roles: []string{domain.RoleEngineer}, AuthType: auth.AuthTypeLocal})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/data-spaces/idc-original/directories", nil))
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "DATA_SPACE_BROWSER_NOT_AVAILABLE") {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}
