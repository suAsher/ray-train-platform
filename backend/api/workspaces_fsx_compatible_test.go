package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic/fake"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
	"ray-train-platform-backend/k8s"
)

const workspaceCompatibleFSXAttributes = `{"type":"TOS","bucket":"workspace-test","server":"tos.example.internal","region":"cn-test"}`

func workspaceCompatibleBindings(t *testing.T, tenant, storageTenant string) []domain.DataMountBinding {
	t.Helper()
	personal, err := domain.NewPersonalDataMountBindingForStorageHome("personal", tenant, storageTenant, "subject-1", "data-personal", workspaceCompatibleFSXAttributes, "alice")
	if err != nil {
		t.Fatal(err)
	}
	root, err := domain.NewTenantRootDataMountBinding("root", tenant, "data-tenant", workspaceCompatibleFSXAttributes)
	if err != nil {
		t.Fatal(err)
	}
	personal.Status, root.Status = domain.DataMountBindingReady, domain.DataMountBindingReady
	return []domain.DataMountBinding{root, personal}
}

func bindWorkspaceCompatiblePVCs(core *k8sfake.Clientset) {
	core.PrependReactor("create", "persistentvolumeclaims", func(action k8stesting.Action) (bool, runtime.Object, error) {
		claim := action.(k8stesting.CreateAction).GetObject().(*corev1.PersistentVolumeClaim).DeepCopy()
		claim.Status.Phase = corev1.ClaimBound
		if err := core.Tracker().Create(corev1.SchemeGroupVersion.WithResource("persistentvolumeclaims"), claim, claim.Namespace); err != nil {
			return true, nil, err
		}
		return true, claim, nil
	})
}

func TestWorkspaceFSXCompatibleUsesDedicatedClaimAndPreservesStorageHome(t *testing.T) {
	for _, tenant := range []string{"team-a", "team-b"} {
		t.Run(tenant, func(t *testing.T) {
			core := k8sfake.NewSimpleClientset()
			bindWorkspaceCompatiblePVCs(core)
			store := &fakeDataSpaceStore{bindings: workspaceCompatibleBindings(t, tenant, "team-a")}
			handler := NewHandler(&fakeJobRepository{}, Options{
				DataSpaces: store, DataSpacesEnabled: true, WorkspaceFSXCompatibleEnabled: true,
				DataSpacesMountCapacity: "1Ti", Kubernetes: k8s.NewClientFromInterfaces(nil, core),
			})
			principal := auth.Principal{Subject: "subject-1", TenantID: tenant}
			plan, err := handler.resolveWorkspaceDataMountPlan(context.Background(), principal)
			if err != nil {
				t.Fatal(err)
			}
			if plan.Workspace == nil || plan.Workspace.ClaimName == "" || plan.Workspace.ClaimName == plan.Personal.ClaimName || plan.Workspace.SubPath != "" || plan.Workspace.ReadOnly {
				t.Fatalf("workspace must have its own writable claim mounted directly: %#v", plan)
			}
			claim, err := core.CoreV1().PersistentVolumeClaims("tenant-" + tenant).Get(context.Background(), plan.Workspace.ClaimName, metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			pv, err := core.CoreV1().PersistentVolumes().Get(context.Background(), claim.Spec.VolumeName, metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if pv.Spec.CSI.VolumeAttributes["path"] != "/ray-train/tenants/team-a/users/alice/workspace" {
				t.Fatalf("team changes must retain the stable workspace prefix: %#v", pv.Spec.CSI.VolumeAttributes)
			}
			if plan.Personal.ClaimName != "data-tenant" || plan.Personal.SubPath != "tenants/team-a/users/alice" {
				t.Fatalf("ordinary personal data mounts changed: %#v", plan.Personal)
			}
			core.ClearActions()
			roots, err := handler.submission.resolveDataSpaceRoots(context.Background(), principal)
			if err != nil || roots.Personal == nil || roots.Personal.ClaimName != "data-tenant" || roots.Personal.SubPath != plan.Personal.SubPath || len(core.Actions()) != 0 {
				t.Fatalf("training mounts must retain their prior path without provisioning workspace storage: roots=%#v err=%v", roots, err)
			}
		})
	}
}

func TestWorkspaceFSXCompatibleDisabledKeepsLegacyPlan(t *testing.T) {
	core := k8sfake.NewSimpleClientset()
	handler := NewHandler(&fakeJobRepository{}, Options{
		DataSpaces: &fakeDataSpaceStore{bindings: workspaceCompatibleBindings(t, "team-a", "team-a")},
		DataSpacesEnabled: true, Kubernetes: k8s.NewClientFromInterfaces(nil, core),
	})
	plan, err := handler.resolveWorkspaceDataMountPlan(context.Background(), auth.Principal{Subject: "subject-1", TenantID: "team-a"})
	if err != nil || plan.Workspace != nil || plan.Personal == nil || plan.Personal.ClaimName != "data-tenant" || len(core.Actions()) != 0 {
		t.Fatalf("disabled compatibility must preserve the legacy mount without Kubernetes writes: plan=%#v err=%v", plan, err)
	}
}

func TestWorkspaceFSXCompatibleWaitsForInitialBinding(t *testing.T) {
	bindings := workspaceCompatibleBindings(t, "team-a", "team-a")
	pv, pvc, err := k8s.BuildWorkspaceMountResources(bindings[1], "tenant-team-a", "1Ti")
	if err != nil {
		t.Fatal(err)
	}
	core := k8sfake.NewSimpleClientset(pv, pvc)
	reads := 0
	core.PrependReactor("get", "persistentvolumeclaims", func(action k8stesting.Action) (bool, runtime.Object, error) {
		reads++
		claim := pvc.DeepCopy()
		if reads > 1 {
			claim.Status.Phase = corev1.ClaimBound
		}
		return true, claim, nil
	})
	handler := NewHandler(&fakeJobRepository{}, Options{
		DataSpaces: &fakeDataSpaceStore{bindings: bindings}, DataSpacesEnabled: true,
		WorkspaceFSXCompatibleEnabled: true, DataSpacesMountCapacity: "1Ti", Kubernetes: k8s.NewClientFromInterfaces(nil, core),
	})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	plan, err := handler.resolveWorkspaceDataMountPlan(ctx, auth.Principal{Subject: "subject-1", TenantID: "team-a"})
	if err != nil || plan.Workspace == nil || reads != 2 {
		t.Fatalf("a newly bound claim must become usable within the launch request: reads=%d plan=%#v err=%v", reads, plan, err)
	}
}

func TestWorkspaceFSXCompatibleStopsPollingWhenRequestIsCanceled(t *testing.T) {
	bindings := workspaceCompatibleBindings(t, "team-a", "team-a")
	pv, pvc, err := k8s.BuildWorkspaceMountResources(bindings[1], "tenant-team-a", "1Ti")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	core := k8sfake.NewSimpleClientset(pv, pvc)
	reads := 0
	core.PrependReactor("get", "persistentvolumeclaims", func(action k8stesting.Action) (bool, runtime.Object, error) {
		reads++
		cancel()
		return true, pvc.DeepCopy(), nil
	})
	handler := NewHandler(&fakeJobRepository{}, Options{
		DataSpaces: &fakeDataSpaceStore{bindings: bindings}, DataSpacesEnabled: true,
		WorkspaceFSXCompatibleEnabled: true, DataSpacesMountCapacity: "1Ti", Kubernetes: k8s.NewClientFromInterfaces(nil, core),
	})
	_, err = handler.resolveWorkspaceDataMountPlan(ctx, auth.Principal{Subject: "subject-1", TenantID: "team-a"})
	if !errors.Is(err, context.Canceled) || reads != 1 {
		t.Fatalf("cancelled launch must stop after the in-flight read: reads=%d err=%v", reads, err)
	}
}

// Deliberately return unfiltered storage rows to verify the API boundary does
// not trust the repository to enforce ownership or binding readiness.
type workspaceUnfilteredBindings struct {
	*fakeDataSpaceStore
}

func (store workspaceUnfilteredBindings) ListDataBindings(context.Context, string, string) ([]domain.DataMountBinding, error) {
	return append([]domain.DataMountBinding(nil), store.bindings...), nil
}

func TestWorkspaceFSXCompatibleRejectsUnreadyOrUnauthorizedPersonalBinding(t *testing.T) {
	for _, scenario := range []string{"pending", "other-user", "other-tenant", "tenant-scope"} {
		t.Run(scenario, func(t *testing.T) {
			bindings := workspaceCompatibleBindings(t, "team-a", "team-a")
			switch scenario {
			case "pending":
				bindings[1].Status = domain.DataMountBindingPending
			case "other-user":
				bindings[1].UserID = "subject-2"
			case "other-tenant":
				bindings[1].TenantID = "team-b"
			case "tenant-scope":
				bindings[1].Scope, bindings[1].UserID = domain.DataMountScopeTenant, ""
			}
			core := k8sfake.NewSimpleClientset()
			handler := NewHandler(&fakeJobRepository{}, Options{
				DataSpaces: workspaceUnfilteredBindings{&fakeDataSpaceStore{bindings: bindings}},
				DataSpacesEnabled: true, WorkspaceFSXCompatibleEnabled: true, DataSpacesMountCapacity: "1Ti",
				Kubernetes: k8s.NewClientFromInterfaces(nil, core),
			})
			_, err := handler.resolveWorkspaceDataMountPlan(context.Background(), auth.Principal{Subject: "subject-1", TenantID: "team-a"})
			if !errors.Is(err, ErrSubmissionDataMountNotReady) || len(core.Actions()) != 0 {
				t.Fatalf("invalid personal binding must fail before any storage write: err=%v actions=%#v", err, core.Actions())
			}
		})
	}
}

func TestWorkspaceFSXCompatibleLaunchWaitsForStorageBeforeCreatingCompute(t *testing.T) {
	for _, scenario := range []string{"pending", "error"} {
		t.Run(scenario, func(t *testing.T) {
			core := k8sfake.NewSimpleClientset()
			if scenario == "error" {
				core.PrependReactor("create", "persistentvolumes", func(action k8stesting.Action) (bool, runtime.Object, error) {
					pv := action.(k8stesting.CreateAction).GetObject().(*corev1.PersistentVolume)
					if pv.Spec.CSI != nil && strings.HasSuffix(pv.Spec.CSI.VolumeAttributes["path"], "/workspace") {
						return true, nil, errors.New("workspace storage unavailable")
					}
					return false, nil, nil
				})
			}
			dynamic := fake.NewSimpleDynamicClient(runtime.NewScheme())
			workspaces := &fakeWorkspaceStore{getErr: context.Canceled}
			handler := NewHandler(&fakeJobRepository{}, Options{
				Workspaces: workspaces, Kubernetes: k8s.NewClientFromInterfaces(dynamic, core),
				DataSpaces: &fakeDataSpaceStore{bindings: workspaceCompatibleBindings(t, "team-a", "team-a")},
				DataSpacesEnabled: true, WorkspaceFSXCompatibleEnabled: true,
				DataSpacesFSXAttributes: workspaceCompatibleFSXAttributes, DataSpacesMountCapacity: "1Ti",
				DirectoryInitializer: &fakePersonalDataDirectoryInitializer{},
				WorkspaceImage: "registry.example/workspace@sha256:" + strings.Repeat("a", 64),
			})
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
			defer cancel()
			response := launchWorkspaceCompatibleRequestWithContext(ctx, handler)
			if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "DATA_SPACE_MOUNT_NOT_READY") {
				t.Fatalf("storage %s must return a retryable readiness response: %d %s", scenario, response.Code, response.Body.String())
			}
			if workspaces.workspace.ID != "" || len(dynamic.Actions()) != 0 {
				t.Fatal("unready workspace storage must not persist a workspace or create compute")
			}
			wantRetryAfter := ""
			if scenario == "pending" {
				wantRetryAfter = "2"
			}
			if response.Header().Get("Retry-After") != wantRetryAfter {
				t.Fatalf("only a still-binding compatible claim should advertise a short retry: %v", response.Header())
			}
		})
	}
}

func TestWorkspaceFSXCompatibleLeavesExistingWorkspaceUntouched(t *testing.T) {
	core := k8sfake.NewSimpleClientset()
	dynamic := fake.NewSimpleDynamicClient(runtime.NewScheme())
	workspaces := &fakeWorkspaceStore{workspace: domain.DevWorkspace{ID: "existing", State: domain.WorkspaceRunning}}
	handler := NewHandler(&fakeJobRepository{}, Options{
		Workspaces: workspaces, Kubernetes: k8s.NewClientFromInterfaces(dynamic, core),
		DataSpacesEnabled: true, WorkspaceFSXCompatibleEnabled: true,
	})
	response := launchWorkspaceCompatibleRequest(handler)
	if response.Code != http.StatusOK || len(core.Actions()) != 0 || len(dynamic.Actions()) != 0 || workspaces.workspace.ID != "existing" {
		t.Fatalf("existing workspaces must remain unchanged: %d %s", response.Code, response.Body.String())
	}
}

func TestWorkspaceFSXCompatibleDoesNotProvisionOnDataSpaceList(t *testing.T) {
	core := k8sfake.NewSimpleClientset()
	handler := NewHandler(&fakeJobRepository{}, Options{
		Kubernetes: k8s.NewClientFromInterfaces(nil, core),
		DataSpaces: &fakeDataSpaceStore{bindings: workspaceCompatibleBindings(t, "team-a", "team-a")},
		DataSpacesEnabled: true, WorkspaceFSXCompatibleEnabled: true,
		DataSpacesFSXAttributes: workspaceCompatibleFSXAttributes, DataSpacesMountCapacity: "1Ti",
		DirectoryInitializer: &fakePersonalDataDirectoryInitializer{},
	})
	router := dataSpaceRouter(handler, auth.Principal{Subject: "subject-1", TenantID: "team-a", Roles: []string{domain.RoleEngineer}, AuthType: auth.AuthTypeOIDC})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/data-spaces", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("ordinary data-space listing failed: %d %s", response.Code, response.Body.String())
	}
	volumes, err := core.CoreV1().PersistentVolumes().List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, volume := range volumes.Items {
		if volume.Spec.CSI != nil && strings.HasSuffix(volume.Spec.CSI.VolumeAttributes["path"], "/workspace") {
			t.Fatal("ordinary data-space listing must not provision a compatible workspace mount")
		}
	}
}

func launchWorkspaceCompatibleRequest(handler *Handler) *httptest.ResponseRecorder {
	return launchWorkspaceCompatibleRequestWithContext(context.Background(), handler)
}

func launchWorkspaceCompatibleRequestWithContext(ctx context.Context, handler *Handler) *httptest.ResponseRecorder {
	router := dataSpaceRouter(handler, auth.Principal{Subject: "subject-1", TenantID: "team-a", Roles: []string{domain.RoleEngineer}, AuthType: auth.AuthTypeOIDC})
	handler.RegisterWorkspaceRoutes(router.Group("/api/v1"))
	request := httptest.NewRequest(http.MethodPost, "/api/v1/dev-workspaces", strings.NewReader(`{"gpuCount":0}`)).WithContext(ctx)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}
