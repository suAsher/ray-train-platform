package k8s

import (
	"reflect"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"ray-train-platform-backend/domain"
)

func TestBuildIDCDataMountResourcesCreatesRetainedReadOnlyNFSVolume(t *testing.T) {
	binding, err := domain.NewIDCDataMountBinding("idc-original-a", "tenant-a", domain.DataSpaceIDCOriginal, "idc-original-a")
	if err != nil {
		t.Fatal(err)
	}
	pv, pvc, err := BuildIDCDataMountResources(binding, "tenant-tenant-a", "1Pi", IDCDataMountSource{Server: "192.0.2.20", Path: "/exports/original"})
	if err != nil {
		t.Fatal(err)
	}
	if pv.Spec.PersistentVolumeReclaimPolicy != "Retain" || pv.Spec.NFS == nil || pv.Spec.NFS.Server != "192.0.2.20" || pv.Spec.NFS.Path != "/exports/original" || !pv.Spec.NFS.ReadOnly {
		t.Fatalf("unexpected IDC PV contract: %#v", pv.Spec)
	}
	if got := pvc.Spec.AccessModes; len(got) != 1 || got[0] != "ReadOnlyMany" || pvc.Spec.VolumeName != pv.Name || pvc.Spec.StorageClassName == nil || *pvc.Spec.StorageClassName != "" {
		t.Fatalf("unexpected IDC PVC contract: %#v", pvc.Spec)
	}
}

func TestBuildDataMountResourcesCreatesSecretlessRetainedFSXVolume(t *testing.T) {
	binding := domain.DataMountBinding{
		ID: "mount-abc123", TenantID: "tenant-a", UserID: "user-a", Scope: domain.DataMountScopePersonal, SpaceID: domain.DataSpaceWorkspace,
		ClaimName: "data-user-a", Driver: domain.FSXCSIDriver, RootPrefix: "ray-train/tenants/tenant-a/users/user-a/",
		VolumeAttributesJSON: `{"type":"TOS","bucket":"shanghai-data-transfer","server":"tos-cn-shanghai.ivolces.com","region":"cn-shanghai","path":"/ray-train/tenants/tenant-a/users/user-a"}`,
		Status:               domain.DataMountBindingPending,
	}
	pv, pvc, err := BuildDataMountResources(binding, "tenant-tenant-a", "1Ti")
	if err != nil {
		t.Fatal(err)
	}
	if pv.Spec.PersistentVolumeReclaimPolicy != "Retain" || pv.Spec.CSI == nil || pv.Spec.CSI.Driver != domain.FSXCSIDriver {
		t.Fatalf("unexpected PV contract: %#v", pv.Spec)
	}
	if pv.Spec.CSI.VolumeAttributes["path"] != "/ray-train/tenants/tenant-a/users/user-a" {
		t.Fatalf("PV path was not governed: %#v", pv.Spec.CSI.VolumeAttributes)
	}
	if pv.Spec.CSI.NodePublishSecretRef != nil || pv.Spec.CSI.ControllerPublishSecretRef != nil || pv.Spec.CSI.NodeStageSecretRef != nil {
		t.Fatalf("governed FSX PV must not contain any secret reference: %#v", pv.Spec.CSI)
	}
	wantMountOptions := []string{"no_writeback_cache", "uid=1000", "gid=1000", "file_mode=770", "dir_mode=770", "tos_allow_delete=true"}
	if !reflect.DeepEqual(pv.Spec.MountOptions, wantMountOptions) {
		t.Fatalf("personal FSX PV must be writable by the non-root Ray runtime: got %#v want %#v", pv.Spec.MountOptions, wantMountOptions)
	}
	if pvc.Namespace != "tenant-tenant-a" || pvc.Name != binding.ClaimName || pvc.Spec.VolumeName != pv.Name || pvc.Spec.StorageClassName == nil || *pvc.Spec.StorageClassName != "" {
		t.Fatalf("unexpected PVC: %#v", pvc)
	}
	if pv.Labels["app.kubernetes.io/part-of"] != "ray-train-platform" || pvc.Labels["app.kubernetes.io/part-of"] != "ray-train-platform" {
		t.Fatalf("data mount resources must carry the platform ownership label: pv=%#v pvc=%#v", pv.Labels, pvc.Labels)
	}
}

func TestBuildDataMountResourcesKeepsTenantSharedFSXVolumeReadOnly(t *testing.T) {
	binding := domain.DataMountBinding{
		ID: "mount-shared", TenantID: "tenant-a", Scope: domain.DataMountScopeTenant, SpaceID: domain.DataSpaceTeamShared,
		ClaimName: "data-team-a", Driver: domain.FSXCSIDriver, RootPrefix: "ray-train/tenants/tenant-a/shared/", ReadOnly: true,
		VolumeAttributesJSON: `{"type":"TOS","bucket":"shanghai-data-transfer","server":"tos-cn-shanghai.ivolces.com","region":"cn-shanghai","path":"/ray-train/tenants/tenant-a/shared"}`,
		Status:               domain.DataMountBindingReady,
	}
	pv, _, err := BuildDataMountResources(binding, "tenant-tenant-a", "1Ti")
	if err != nil {
		t.Fatal(err)
	}
	for _, option := range pv.Spec.MountOptions {
		if option == "tos_allow_delete=true" {
			t.Fatalf("read-only tenant space must not enable TOS deletion: %#v", pv.Spec.MountOptions)
		}
	}
}

func TestBuildDataMountResourcesRejectsIDCAndSecretBackedBindings(t *testing.T) {
	for _, binding := range []domain.DataMountBinding{
		{ID: "idc", TenantID: "tenant-a", Scope: domain.DataMountScopeIDC, SpaceID: domain.DataSpaceIDCOriginal, ClaimName: "idc-original", ReadOnly: true, Status: domain.DataMountBindingReady},
		{ID: "secret", TenantID: "tenant-a", UserID: "user-a", Scope: domain.DataMountScopePersonal, SpaceID: domain.DataSpaceWorkspace, ClaimName: "data-user-a", Driver: domain.FSXCSIDriver, RootPrefix: "ray-train/tenants/tenant-a/users/user-a/", VolumeAttributesJSON: `{"type":"TOS","bucket":"b","path":"/ray-train/tenants/tenant-a/users/user-a","secretName":"not-allowed"}`, Status: domain.DataMountBindingReady},
	} {
		if _, _, err := BuildDataMountResources(binding, "tenant-tenant-a", "1Ti"); err == nil {
			t.Fatalf("unsafe binding was accepted: %#v", binding)
		}
	}
}

func TestDevWorkspaceMountsPersonalAndReadonlyDataSpaces(t *testing.T) {
	workspace := domain.DevWorkspace{
		ID: "ws-01", TenantID: "tenant-a", UserID: "user-a", Name: "dev-user-a", Namespace: "tenant-a", GPUCount: 1,
	}
	plan := DataMountPlan{
		Personal:    &DataMountRoot{ClaimName: "data-user-a"},
		Team:        &DataMountRoot{ClaimName: "data-team-a", ReadOnly: true},
		Public:      &DataMountRoot{ClaimName: "data-public", ReadOnly: true},
		IDCOriginal: &DataMountRoot{ClaimName: "idc-original-ro", ReadOnly: true},
	}
	manifest, err := RenderDevRayCluster(workspace, WorkspaceRenderOptions{
		Image: "registry.example/dev@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", DataMounts: plan,
	})
	if err != nil {
		t.Fatal(err)
	}
	for group, pod := range map[string]map[string]any{
		"head":   dataMountDevPodSpec(t, manifest, "head"),
		"worker": dataMountDevPodSpec(t, manifest, "worker"),
	} {
		assertGovernedDataMount(t, pod, "platform-data-personal-workspace", "data-user-a", domain.WorkspaceMountPath, "workspace", false)
		assertStorageMount(t, pod, "platform-data-personal", "data-user-a", domain.MyStorageMountPath, false)
		assertStorageMount(t, pod, "platform-data-team", "data-team-a", domain.TeamStorageMountPath, true)
		assertStorageMount(t, pod, "platform-data-public", "data-public", domain.PublicStorageMountPath, true)
		assertStorageMount(t, pod, "platform-data-idc-original", "idc-original-ro", domain.IDCOriginalMountPath, true)
		if got := pod["automountServiceAccountToken"]; got != false {
			t.Fatalf("%s pod must not mount a Kubernetes API token by default: %#v", group, pod)
		}
	}
}

func TestDevWorkspaceUsesOneTenantRootPVCWithConfinedSubPaths(t *testing.T) {
	workspace := domain.DevWorkspace{
		ID: "ws-tenant-root", TenantID: "local", UserID: "user-a", Name: "dev-tenant-root", Namespace: "tenant-local", GPUCount: 1,
	}
	plan := DataMountPlan{
		Personal: &DataMountRoot{ClaimName: "data-tenant-local", SubPath: "tenants/local/users/guofeng.su"},
		Team:     &DataMountRoot{ClaimName: "data-tenant-local", SubPath: "tenants/local/shared", ReadOnly: true},
		Public:   &DataMountRoot{ClaimName: "data-tenant-local", SubPath: "public", ReadOnly: true},
	}
	manifest, err := RenderDevRayCluster(workspace, WorkspaceRenderOptions{
		Image: "registry.example/dev@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", DataMounts: plan,
	})
	if err != nil {
		t.Fatal(err)
	}
	for group, pod := range map[string]map[string]any{
		"head":   dataMountDevPodSpec(t, manifest, "head"),
		"worker": dataMountDevPodSpec(t, manifest, "worker"),
	} {
		assertStorageMountWithSubPath(t, pod, "platform-data-personal", "data-tenant-local", domain.WorkspaceMountPath, "tenants/local/users/guofeng.su/workspace", false)
		assertStorageMountWithSubPath(t, pod, "platform-data-personal", "data-tenant-local", domain.MyStorageMountPath, "tenants/local/users/guofeng.su", false)
		assertStorageMountWithSubPath(t, pod, "platform-data-personal", "data-tenant-local", domain.TeamStorageMountPath, "tenants/local/shared", true)
		assertStorageMountWithSubPath(t, pod, "platform-data-personal", "data-tenant-local", domain.PublicStorageMountPath, "public", true)
		if countPVCVolumes(pod, "data-tenant-local") != 1 {
			t.Fatalf("%s must stage the tenant TOS root exactly once: %#v", group, pod["volumes"])
		}
	}
}

func TestDataMountPlanRejectsWritableSharedOrWrongPersonalBinding(t *testing.T) {
	plan := DataMountPlan{
		Personal: &DataMountRoot{ClaimName: "data-user-a", ReadOnly: true},
		Team:     &DataMountRoot{ClaimName: "data-team-a", ReadOnly: false},
	}
	if err := plan.Validate(); err == nil {
		t.Fatal("unsafe data mount plan was accepted")
	}
}

func dataMountDevPodSpec(t *testing.T, manifest any, group string) map[string]any {
	t.Helper()
	resource := manifest.(*unstructured.Unstructured)
	if group == "head" {
		pod, found, err := nestedMap(resource.Object, "spec", "headGroupSpec", "template", "spec")
		if err != nil || !found {
			t.Fatalf("head pod: %v", err)
		}
		return pod
	}
	workers, found, err := nestedSlice(resource.Object, "spec", "workerGroupSpecs")
	if err != nil || !found || len(workers) != 1 {
		t.Fatalf("workers: %v", err)
	}
	pod, found, err := nestedMap(workers[0].(map[string]any), "template", "spec")
	if err != nil || !found {
		t.Fatalf("worker pod: %v", err)
	}
	return pod
}

func assertStorageMountWithSubPath(t *testing.T, podSpec map[string]any, volumeName, claimName, mountPath, subPath string, readOnly bool) {
	t.Helper()
	volumes, _, _ := nestedSlice(podSpec, "volumes")
	foundVolume := false
	for _, value := range volumes {
		volume, _ := value.(map[string]any)
		if volume["name"] != volumeName {
			continue
		}
		claim, _ := volume["persistentVolumeClaim"].(map[string]any)
		if claim["claimName"] == claimName {
			foundVolume = true
		}
	}
	if !foundVolume {
		t.Fatalf("missing %s volume for %s: %#v", volumeName, claimName, volumes)
	}
	containers, _, _ := nestedSlice(podSpec, "containers")
	mounts, _ := containers[0].(map[string]any)["volumeMounts"].([]any)
	for _, value := range mounts {
		mount, _ := value.(map[string]any)
		if mount["name"] == volumeName && mount["mountPath"] == mountPath && mount["subPath"] == subPath && mount["readOnly"] == readOnly {
			return
		}
	}
	t.Fatalf("missing subpath mount %s at %s: %#v", subPath, mountPath, mounts)
}

func countPVCVolumes(podSpec map[string]any, claimName string) int {
	volumes, _ := podSpec["volumes"].([]any)
	count := 0
	for _, value := range volumes {
		volume, _ := value.(map[string]any)
		claim, _ := volume["persistentVolumeClaim"].(map[string]any)
		if claim["claimName"] == claimName {
			count++
		}
	}
	return count
}
