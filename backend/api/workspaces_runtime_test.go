package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
	"ray-train-platform-backend/k8s"
	"ray-train-platform-backend/repositories"
)

type workspaceRuntimeCatalog struct {
	ownedImageStore
	defaultImage domain.PlatformImage
}

func (s *workspaceRuntimeCatalog) DefaultImage(context.Context, string, string) (domain.PlatformImage, error) {
	if s.defaultImage.Reference == "" {
		return domain.PlatformImage{}, repositories.ErrImageNotFound
	}
	return s.defaultImage, nil
}

func TestLaunchWorkspacePreservesCatalogRayVersionAndOwnerBoundary(t *testing.T) {
	gin.SetMode(gin.TestMode)
	base := domain.PlatformImage{ID: "environment-base", Kind: domain.ImageKindWorkspace, Reference: "registry.example/environment-workspace@sha256:" + strings.Repeat("a", 64), RayVersion: "2.58.0"}
	legacy := domain.PlatformImage{ID: "bev-workspace", Kind: domain.ImageKindWorkspace, Reference: "registry.example/bev-workspace@sha256:" + strings.Repeat("b", 64), RayVersion: "2.35.0"}
	personal := base
	personal.ID = "personal-workspace"
	personal.TenantID = "team-a"
	personal.OwnerUserID = "owner"
	personal.Visibility = domain.ImageVisibilityPersonal
	fallback := "registry.example/deployment-workspace@sha256:" + strings.Repeat("c", 64)
	explicitFallback := "registry.example/manual-workspace@sha256:" + strings.Repeat("d", 64)
	cases := []struct {
		name                           string
		image                          domain.PlatformImage
		requested                      string
		isDefault, noCatalog           bool
		user, tenant, role             string
		expectedImage, expectedVersion string
		status                         int
	}{
		{name: "new base selected", image: base, requested: base.Reference, expectedImage: base.Reference, expectedVersion: "2.58.0", status: http.StatusAccepted},
		{name: "new base default", image: base, isDefault: true, expectedImage: base.Reference, expectedVersion: "2.58.0", status: http.StatusAccepted},
		{name: "legacy selected", image: legacy, requested: legacy.Reference, expectedImage: legacy.Reference, expectedVersion: "2.35.0", status: http.StatusAccepted},
		{name: "legacy default", image: legacy, isDefault: true, expectedImage: legacy.Reference, expectedVersion: "2.35.0", status: http.StatusAccepted},
		{name: "deployment fallback", noCatalog: true, expectedImage: fallback, expectedVersion: "2.35.0", status: http.StatusAccepted},
		{name: "explicit fallback without catalog", noCatalog: true, requested: explicitFallback, expectedImage: explicitFallback, expectedVersion: "2.35.0", status: http.StatusAccepted},
		{name: "personal owner", image: personal, requested: personal.Reference, user: "owner", expectedImage: personal.Reference, expectedVersion: "2.58.0", status: http.StatusAccepted},
		{name: "personal denied to peer", image: personal, requested: personal.Reference, user: "other", status: http.StatusBadRequest},
		{name: "personal denied to administrator", image: personal, requested: personal.Reference, user: "other", role: domain.RoleSuperAdmin, status: http.StatusBadRequest},
		{name: "personal denied across teams", image: personal, requested: personal.Reference, user: "owner", tenant: "team-b", status: http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var catalog ImageStore
			if !tc.noCatalog {
				store := &workspaceRuntimeCatalog{ownedImageStore: ownedImageStore{stubImageStore{images: []domain.PlatformImage{tc.image}}}}
				if tc.isDefault {
					store.defaultImage = tc.image
				}
				catalog = store
			}
			dynamic := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
			handler := NewHandler(&fakeJobRepository{}, Options{Workspaces: &fakeWorkspaceStore{getErr: context.Canceled}, Kubernetes: k8s.NewClientFromInterfaces(dynamic, k8sfake.NewSimpleClientset()), Images: catalog, WorkspaceImage: fallback, RayVersion: "2.35.0"})
			handler.newID = func() (string, error) { return "job-runtime-version", nil }
			tenant := tc.tenant
			if tenant == "" {
				tenant = "team-a"
			}
			user := tc.user
			if user == "" {
				user = "subject-1"
			}
			role := tc.role
			if role == "" {
				role = domain.RoleEngineer
			}
			router := gin.New()
			router.Use(func(c *gin.Context) {
				c.Set("ray-platform-principal", auth.Principal{TenantID: tenant, Subject: user, Roles: []string{role}, AuthType: auth.AuthTypeOIDC})
				c.Next()
			})
			handler.RegisterWorkspaceRoutes(router.Group("/api/v1"))
			// The caller cannot override trusted catalogue metadata with a JSON field.
			request := httptest.NewRequest(http.MethodPost, "/api/v1/dev-workspaces", strings.NewReader(fmt.Sprintf(`{"image":%q,"gpuCount":0,"rayVersion":"9.99.0"}`, tc.requested)))
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != tc.status {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			cluster, err := dynamic.Resource(schema.GroupVersionResource{Group: "ray.io", Version: "v1", Resource: "rayclusters"}).Namespace("tenant-"+tenant).Get(context.Background(), "dev-runtime-version", metav1.GetOptions{})
			if tc.status != http.StatusAccepted {
				if err == nil {
					t.Fatal("denied private image created a RayCluster")
				}
				if !strings.Contains(response.Body.String(), "IMAGE_NOT_ALLOWED") {
					t.Fatalf("unexpected ACL response %s", response.Body.String())
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			version, _, _ := unstructured.NestedString(cluster.Object, "spec", "rayVersion")
			if version != tc.expectedVersion {
				t.Fatalf("RayCluster version=%q want=%q", version, tc.expectedVersion)
			}
			containers, _, _ := unstructured.NestedSlice(cluster.Object, "spec", "headGroupSpec", "template", "spec", "containers")
			if len(containers) != 1 || containers[0].(map[string]any)["image"] != tc.expectedImage {
				t.Fatalf("image and Ray version were separated: %v", containers)
			}
		})
	}
}
