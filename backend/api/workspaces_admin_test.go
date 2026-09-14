package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
	"ray-train-platform-backend/k8s"
	"ray-train-platform-backend/repositories"
)

type administrativeWorkspaceStore struct {
	fakeWorkspaceStore
	lookupTenant string
	lookupCalls int
	updatedID string
	updateErr error
}

func (s *administrativeWorkspaceStore) GetWorkspaceByID(_ context.Context, id, tenantID string) (*domain.DevWorkspace, error) {
	s.lookupCalls++
	s.lookupTenant = tenantID
	if id != s.workspace.ID || (tenantID != "" && tenantID != s.workspace.TenantID) {
		return nil, errors.New("workspace not found")
	}
	copy := s.workspace
	return &copy, nil
}

func (s *administrativeWorkspaceStore) GetWorkspace(_ context.Context, tenantID, userID string) (*domain.DevWorkspace, error) {
	if tenantID != s.workspace.TenantID || userID != s.workspace.UserID {
		return nil, errors.New("workspace not found")
	}
	copy := s.workspace
	return &copy, nil
}

func (s *administrativeWorkspaceStore) GetWorkspaceByUser(_ context.Context, userID string) (*domain.DevWorkspace, error) {
	if userID != s.workspace.UserID { return nil, errors.New("workspace not found") }
	copy := s.workspace
	return &copy, nil
}

func (s *administrativeWorkspaceStore) UpdateWorkspaceStateByID(_ context.Context, id string, state domain.WorkspaceState) error {
	if s.updateErr != nil {
		return s.updateErr
	}
	if state != domain.WorkspaceStopped {
		return errors.New("unexpected state")
	}
	s.updatedID = id
	return nil
}

type workspaceAdministrativeAuditStore struct {
	AdminStore
	events []repositories.AdministrativeAuditEvent
}

func (s *workspaceAdministrativeAuditStore) CreateAdministrativeAuditLog(_ context.Context, event repositories.AdministrativeAuditEvent) error {
	s.events = append(s.events, event)
	return nil
}

func TestAdminStopWorkspaceAuthorizationAndCleanup(t *testing.T) {
	for _, test := range []struct {
		name, subject, tenant, role string
		status int
		lookupTenant string
	}{
		{"super admin cross team", "root", "platform", domain.RoleSuperAdmin, http.StatusAccepted, ""},
		{"team admin own team", "lead", "team-a", domain.RoleTenantAdmin, http.StatusAccepted, "team-a"},
		{"team admin other team", "lead", "team-b", domain.RoleTenantAdmin, http.StatusNotFound, "team-b"},
		{"team admin missing team", "lead", "", domain.RoleTenantAdmin, http.StatusForbidden, ""},
		{"engineer own workspace uses me only", "owner", "team-a", domain.RoleEngineer, http.StatusForbidden, ""},
		{"engineer other workspace", "other", "team-a", domain.RoleEngineer, http.StatusForbidden, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			router, store, audit, dynamic, core := administrativeWorkspaceRouter(auth.Principal{Subject: test.subject, TenantID: test.tenant, Roles: []string{test.role}})
			response := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodDelete, "/api/v1/admin/dev-workspaces/ws-target", nil)
			request.Header.Set("X-Request-ID", "stop-test")
			router.ServeHTTP(response, request)
			if response.Code != test.status {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			if store.lookupTenant != test.lookupTenant || (test.status == http.StatusForbidden && store.lookupCalls != 0) {
				t.Fatalf("unauthorized lookup: %+v", store)
			}
			if test.status != http.StatusAccepted {
				if len(dynamic.Actions()) != 0 || len(core.Actions()) != 0 || store.updatedID != "" || len(audit.events) != 0 {
					t.Fatal("denied request changed workspace resources")
				}
				return
			}
			if store.updatedID != "ws-target" || !strings.Contains(response.Body.String(), `"STOPPED"`) {
				t.Fatalf("wrong target/status: %+v %s", store, response.Body.String())
			}
			if _, err := dynamic.Resource(schema.GroupVersionResource{Group: "ray.io", Version: "v1", Resource: "rayclusters"}).Namespace("tenant-team-a").Get(context.Background(), "dev-target", metav1.GetOptions{}); err == nil {
				t.Fatal("workspace cluster was not deleted")
			}
			if _, err := core.CoreV1().Services("tenant-team-a").Get(context.Background(), "dev-target-dev-svc", metav1.GetOptions{}); err == nil {
				t.Fatal("workspace service was not deleted")
			}
			if len(audit.events) != 1 || audit.events[0].ResourceID != "ws-target" || audit.events[0].Action != "workspace.stopped" || audit.events[0].TargetTenantID != "team-a" || audit.events[0].Principal.Subject != test.subject || audit.events[0].RequestID != "stop-test" {
				t.Fatalf("wrong audit: %+v", audit.events)
			}
		})
	}
}

func administrativeWorkspaceRouter(principal auth.Principal) (*gin.Engine, *administrativeWorkspaceStore, *workspaceAdministrativeAuditStore, *dynamicfake.FakeDynamicClient, *k8sfake.Clientset) {
	gin.SetMode(gin.TestMode)
	store := &administrativeWorkspaceStore{fakeWorkspaceStore: fakeWorkspaceStore{workspace: domain.DevWorkspace{ID: "ws-target", TenantID: "team-a", UserID: "owner", Namespace: "tenant-team-a", RayClusterName: "dev-target", State: domain.WorkspaceRunning}}}
	cluster := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "ray.io/v1", "kind": "RayCluster", "metadata": map[string]any{"name": "dev-target", "namespace": "tenant-team-a", "labels": map[string]any{"ray.io/workspace-id": "ws-target"}}}}
	dynamic := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), cluster)
	core := k8sfake.NewSimpleClientset(&corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "dev-target-dev-svc", Namespace: "tenant-team-a", Labels: map[string]string{"ray.io/workspace-id": "ws-target"}}})
	audit := &workspaceAdministrativeAuditStore{}
	handler := NewHandler(&fakeJobRepository{}, Options{Workspaces: store, Kubernetes: k8s.NewClientFromInterfaces(dynamic, core), Admin: audit, WorkspacePepper: []byte(strings.Repeat("p", 32))})
	router := gin.New()
	router.Use(func(c *gin.Context) {
		if principal.Subject != "" { c.Set("ray-platform-principal", principal) }
		c.Next()
	})
	handler.RegisterAdminRoutes(router.Group("/api/v1"))
	handler.RegisterWorkspaceRoutes(router.Group("/api/v1"))
	handler.RegisterWorkspaceProxyRoute(router.Group("/api/v1"))
	return router, store, audit, dynamic, core
}

func TestAdminStopWorkspaceFailureDoesNotClaimStopped(t *testing.T) {
	for _, failure := range []string{"cluster", "service", "state"} {
		t.Run(failure, func(t *testing.T) {
			router, store, audit, dynamic, core := administrativeWorkspaceRouter(auth.Principal{Subject: "root", TenantID: "platform", Roles: []string{domain.RoleSuperAdmin}})
			reactor := func(k8stesting.Action) (bool, runtime.Object, error) { return true, nil, errors.New("unavailable") }
			want := http.StatusBadGateway
			switch failure {
			case "cluster": dynamic.PrependReactor("delete", "rayclusters", reactor)
			case "service": core.PrependReactor("delete", "services", reactor)
			case "state": store.updateErr = errors.New("unavailable"); want = http.StatusInternalServerError
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodDelete, "/api/v1/admin/dev-workspaces/ws-target", nil))
			if response.Code != want || store.updatedID != "" || len(audit.events) != 0 {
				t.Fatalf("failure falsely reported success: %d %s audit=%+v", response.Code, response.Body.String(), audit.events)
			}
		})
	}
}

func TestAdminWorkspaceManagementDoesNotGrantEditorAccess(t *testing.T) {
	for _, role := range []string{domain.RoleSuperAdmin, domain.RoleTenantAdmin} {
		router, _, _, dynamic, core := administrativeWorkspaceRouter(auth.Principal{Subject: "admin", TenantID: "team-a", Roles: []string{role}})
		for _, path := range []string{"/api/v1/dev-workspaces/ws-target/access", "/api/v1/dev-workspaces/ws-target/proxy/", "/api/v1/dev-workspaces/ws-target/vscode/"} {
			method := http.MethodGet
			if strings.HasSuffix(path, "/access") { method = http.MethodPost }
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(method, path, nil))
			if response.Code != http.StatusNotFound {
				t.Fatalf("%s accessed another user's editor at %s: %d %s", role, path, response.Code, response.Body.String())
			}
		}
		if len(dynamic.Actions()) != 0 || len(core.Actions()) != 0 { t.Fatal("editor request reached Kubernetes") }
	}
}

func TestAdminStopWorkspaceUnauthenticatedAndUnknown(t *testing.T) {
	for _, test := range []struct { principal auth.Principal; id string; status int }{
		{auth.Principal{}, "ws-target", http.StatusUnauthorized},
		{auth.Principal{Subject: "root", Roles: []string{domain.RoleSuperAdmin}}, "ws-missing", http.StatusNotFound},
	} {
		router, store, audit, dynamic, core := administrativeWorkspaceRouter(test.principal)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodDelete, "/api/v1/admin/dev-workspaces/"+test.id, nil))
		if response.Code != test.status || store.updatedID != "" || len(audit.events) != 0 || len(dynamic.Actions()) != 0 || len(core.Actions()) != 0 {
			t.Fatalf("unexpected response: %d %s", response.Code, response.Body.String())
		}
	}
}

func TestWorkspaceSelfStopRemainsOwnerOnly(t *testing.T) {
	for _, test := range []struct { subject, role string; status int }{
		{"owner", domain.RoleEngineer, http.StatusAccepted},
		{"admin", domain.RoleSuperAdmin, http.StatusNotFound},
		{"other", domain.RoleEngineer, http.StatusNotFound},
	} {
		router, store, _, dynamic, core := administrativeWorkspaceRouter(auth.Principal{Subject: test.subject, TenantID: "team-a", Roles: []string{test.role}})
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodDelete, "/api/v1/dev-workspaces/me?userId=owner", nil))
		if response.Code != test.status || store.lookupCalls != 0 { t.Fatalf("self stop changed ownership: %d %s", response.Code, response.Body.String()) }
		if test.status != http.StatusAccepted && (len(dynamic.Actions()) != 0 || len(core.Actions()) != 0) { t.Fatal("self stop touched another user's resources") }
	}
}

func TestAdminStopWorkspaceIsIdempotent(t *testing.T) {
	router, store, _, _, _ := administrativeWorkspaceRouter(auth.Principal{Subject: "root", Roles: []string{domain.RoleSuperAdmin}})
	for i := 0; i < 2; i++ {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodDelete, "/api/v1/admin/dev-workspaces/ws-target", nil))
		if response.Code != http.StatusAccepted { t.Fatalf("repeat stop failed: %d %s", response.Code, response.Body.String()) }
		store.workspace.State = domain.WorkspaceStopped
	}
}
