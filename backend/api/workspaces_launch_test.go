package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic/fake"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
	"ray-train-platform-backend/k8s"
	"ray-train-platform-backend/repositories"
)

func TestLaunchWorkspaceRequiresReadyPersonalDataMountWhenEnabled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	workspaces := &fakeWorkspaceStore{getErr: context.Canceled}
	initializer := &fakePersonalDataDirectoryInitializer{}
	handler := NewHandler(&fakeJobRepository{}, Options{
		Workspaces: workspaces, Kubernetes: k8s.NewClientFromInterfaces(nil, k8sfake.NewSimpleClientset()), RayVersion: "2.35.0", DataSpacesEnabled: true,
		DataSpaces: &fakeDataSpaceStore{bindings: []domain.DataMountBinding{{
			ID: "personal", TenantID: "team-a", UserID: "subject-1", Scope: domain.DataMountScopePersonal, SpaceID: domain.DataSpaceWorkspace, Status: domain.DataMountBindingPending,
		}}},
		DataSpacesFSXAttributes: `{"type":"TOS","bucket":"test","server":"tos.example.internal","region":"cn-test"}`,
		DataSpacesMountCapacity: "1Ti", DirectoryInitializer: initializer,
		WorkspaceImage: "registry.example/workspace@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
	})
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("ray-platform-principal", auth.Principal{Subject: "subject-1", TenantID: "team-a", Roles: []string{"Engineer"}, AuthType: auth.AuthTypeOIDC})
		c.Next()
	})
	handler.RegisterWorkspaceRoutes(router.Group("/api/v1"))

	request := httptest.NewRequest(http.MethodPost, "/api/v1/dev-workspaces", strings.NewReader(`{}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "DATA_SPACE_MOUNT_NOT_READY") {
		t.Fatalf("expected a data-mount readiness conflict, got status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestResolveWorkspaceDataMountPlanUsesOneTenantRootWithConfinedPaths(t *testing.T) {
	handler := NewHandler(&fakeJobRepository{}, Options{
		DataSpacesEnabled: true,
		DataSpaces: &fakeDataSpaceStore{bindings: []domain.DataMountBinding{
			{ID: "root", TenantID: "team-a", Scope: domain.DataMountScopeTenant, SpaceID: domain.DataSpaceTenantStorageRoot, ClaimName: "data-tenant-team-a", RootPrefix: "ray-train/", Status: domain.DataMountBindingReady},
			{ID: "mine", TenantID: "team-a", UserID: "subject-1", StorageKey: "guofeng.su", Scope: domain.DataMountScopePersonal, SpaceID: domain.DataSpaceWorkspace, ClaimName: "data-legacy-personal", RootPrefix: "ray-train/tenants/team-a/users/guofeng.su/", Status: domain.DataMountBindingReady},
			{ID: "team", TenantID: "team-a", Scope: domain.DataMountScopeTenant, SpaceID: domain.DataSpaceTeamShared, ClaimName: "data-legacy-team", RootPrefix: "ray-train/tenants/team-a/shared/", ReadOnly: true, Status: domain.DataMountBindingReady},
			{ID: "public", TenantID: "team-a", Scope: domain.DataMountScopeTenant, SpaceID: domain.DataSpacePublic, ClaimName: "data-legacy-public", RootPrefix: "ray-train/public/", ReadOnly: true, Status: domain.DataMountBindingReady},
		}},
	})
	plan, err := handler.resolveWorkspaceDataMountPlan(context.Background(), auth.Principal{Subject: "subject-1", TenantID: "team-a"})
	if err != nil {
		t.Fatal(err)
	}
	for name, root := range map[string]*k8s.DataMountRoot{"personal": plan.Personal, "team": plan.Team, "public": plan.Public} {
		if root == nil || root.ClaimName != "data-tenant-team-a" {
			t.Fatalf("%s must use the tenant root claim: %#v", name, root)
		}
	}
	if plan.Personal.SubPath != "tenants/team-a/users/guofeng.su" || plan.Team.SubPath != "tenants/team-a/shared" || plan.Public.SubPath != "public" {
		t.Fatalf("workspace roots were not confined below the tenant root: %#v", plan)
	}
}

func TestResolveWorkspaceDataMountPlanUsesConfiguredTemporaryPublicSubPath(t *testing.T) {
	handler := NewHandler(&fakeJobRepository{}, Options{
		DataSpacesEnabled:    true,
		DataSpacesPublicRoot: "ray-train/tenants/local/datasets/public",
		DataSpaces: &fakeDataSpaceStore{bindings: []domain.DataMountBinding{
			{ID: "root", TenantID: "local", Scope: domain.DataMountScopeTenant, SpaceID: domain.DataSpaceTenantStorageRoot, ClaimName: "data-tenant-local", RootPrefix: "ray-train/", Status: domain.DataMountBindingReady},
			{ID: "mine", TenantID: "local", UserID: "subject-1", StorageKey: "guofeng.su", Scope: domain.DataMountScopePersonal, SpaceID: domain.DataSpaceWorkspace, ClaimName: "data-legacy-personal", RootPrefix: "ray-train/tenants/local/users/guofeng.su/", Status: domain.DataMountBindingReady},
			{ID: "team", TenantID: "local", Scope: domain.DataMountScopeTenant, SpaceID: domain.DataSpaceTeamShared, ClaimName: "data-legacy-team", RootPrefix: "ray-train/tenants/local/shared/", ReadOnly: true, Status: domain.DataMountBindingReady},
			{ID: "public", TenantID: "local", Scope: domain.DataMountScopeTenant, SpaceID: domain.DataSpacePublic, ClaimName: "data-legacy-public", RootPrefix: "ray-train/public/", ReadOnly: true, Status: domain.DataMountBindingReady},
		}},
	})
	plan, err := handler.resolveWorkspaceDataMountPlan(context.Background(), auth.Principal{Subject: "subject-1", TenantID: "local"})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Public == nil || plan.Public.ClaimName != "data-tenant-local" || plan.Public.SubPath != "tenants/local/datasets/public" || !plan.Public.ReadOnly {
		t.Fatalf("public workspace mount=%#v, want the configured confined subpath", plan.Public)
	}
}

func TestLaunchWorkspaceWaitsForIDCDataSpaceToBindBeforeCreatingCluster(t *testing.T) {
	gin.SetMode(gin.TestMode)
	workspaces := &fakeWorkspaceStore{getErr: context.Canceled}
	core := k8sfake.NewSimpleClientset()
	dynamic := fake.NewSimpleDynamicClient(runtime.NewScheme())
	handler := NewHandler(&fakeJobRepository{}, Options{
		Workspaces: workspaces, Kubernetes: k8s.NewClientFromInterfaces(dynamic, core), RayVersion: "2.35.0",
		WorkspaceImage: "registry.example/workspace@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		DataSpaces:     &fakeDataSpaceStore{}, IDCDataSpacesEnabled: true, IDCDataSpacesMountCapacity: "1Pi",
		IDCDataSpaceSources: map[domain.DataSpaceID]k8s.IDCDataMountSource{
			domain.DataSpaceIDCOriginal:    {Server: "192.0.2.10", Path: "/exports/original"},
			domain.DataSpaceIDCWellspiking: {Server: "192.0.2.11", Path: "/exports/wellspiking"},
			domain.DataSpaceIDCShared:      {Server: "storage.example.internal", Path: "/exports/shared"},
		},
	})
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("ray-platform-principal", auth.Principal{Subject: "subject-1", TenantID: "team-a", Roles: []string{"Engineer"}, AuthType: auth.AuthTypeOIDC})
		c.Next()
	})
	handler.RegisterWorkspaceRoutes(router.Group("/api/v1"))
	request := httptest.NewRequest(http.MethodPost, "/api/v1/dev-workspaces", strings.NewReader(`{}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "DATA_SPACE_MOUNT_NOT_READY") {
		t.Fatalf("expected workspace to wait for pending IDC data, status=%d body=%s", response.Code, response.Body.String())
	}
	if _, err := core.CoreV1().PersistentVolumeClaims("tenant-team-a").Get(context.Background(), "idc-original-"+dataMountTenantKey("team-a"), metav1.GetOptions{}); err != nil {
		t.Fatalf("IDC data adapter was not initialized before workspace launch: %v", err)
	}
}

func TestLaunchWorkspaceReportsGPUQuotaExceeded(t *testing.T) {
	gin.SetMode(gin.TestMode)
	workspaces := &fakeWorkspaceStore{getErr: context.Canceled, createErr: &repositories.GPUQuotaExceededError{Quota: 24, Used: 24, Requested: 1}}
	handler := NewHandler(&fakeJobRepository{}, Options{
		Workspaces: workspaces, Kubernetes: &k8s.Client{}, RayVersion: "2.35.0",
		WorkspaceImage: "registry.example/workspace@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
	})
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("ray-platform-principal", auth.Principal{Subject: "subject-1", TenantID: "team-a", Roles: []string{"Engineer"}, AuthType: auth.AuthTypeOIDC})
		c.Next()
	})
	handler.RegisterWorkspaceRoutes(router.Group("/api/v1"))

	request := httptest.NewRequest(http.MethodPost, "/api/v1/dev-workspaces", strings.NewReader(`{}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "GPU_QUOTA_EXCEEDED") {
		t.Fatalf("expected a clear GPU quota conflict, got status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestLaunchWorkspacePreparesTenantNamespaceAndRegistrySecret(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const platformNamespace = "ray-train-platform"
	const tenantNamespace = "tenant-team-a"
	const registrySecret = "registry-pull"
	core := k8sfake.NewSimpleClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: registrySecret, Namespace: platformNamespace},
		Type:       corev1.SecretTypeDockerConfigJson,
		Data:       map[string][]byte{corev1.DockerConfigJsonKey: []byte(`{"auths":{"registry.example":{}}}`)},
	})
	dynamic := fake.NewSimpleDynamicClient(runtime.NewScheme())
	handler := NewHandler(&fakeJobRepository{}, Options{
		Workspaces:        &fakeWorkspaceStore{getErr: context.Canceled},
		Kubernetes:        k8s.NewClientFromInterfaces(dynamic, core),
		WorkspaceImage:    "registry.example/workspace@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		RayVersion:        "2.35.0",
		ImagePullSecrets:  []string{registrySecret},
		PlatformNamespace: platformNamespace,
	})
	handler.newID = func() (string, error) { return "job-workspace", nil }
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("ray-platform-principal", auth.Principal{Subject: "subject-1", TenantID: "team-a", Roles: []string{"Engineer"}, AuthType: auth.AuthTypeOIDC})
		c.Next()
	})
	handler.RegisterWorkspaceRoutes(router.Group("/api/v1"))

	request := httptest.NewRequest(http.MethodPost, "/api/v1/dev-workspaces", strings.NewReader(`{}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusAccepted {
		t.Fatalf("expected workspace launch to be accepted, got status=%d body=%s", response.Code, response.Body.String())
	}
	namespace, err := core.CoreV1().Namespaces().Get(context.Background(), tenantNamespace, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("expected tenant namespace to be prepared: %v", err)
	}
	if namespace.Labels["ray.io/tenant-id"] != "team-a" {
		t.Fatalf("tenant namespace labels were not applied: %#v", namespace.Labels)
	}
	secret, err := core.CoreV1().Secrets(tenantNamespace).Get(context.Background(), registrySecret, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("expected registry Secret to be copied into tenant namespace: %v", err)
	}
	if secret.Type != corev1.SecretTypeDockerConfigJson || len(secret.Data[corev1.DockerConfigJsonKey]) == 0 {
		t.Fatalf("unexpected tenant registry Secret: %#v", secret)
	}
}
