package k8s

import (
	"reflect"
	"strconv"
	"strings"
	"testing"

	"ray-train-platform-backend/domain"
)

func TestRenderDevRayClusterPermitsSudoOnlyOnInteractiveWorker(t *testing.T) {
	for _, gpuCount := range []int{0, 1, 8} {
		t.Run("gpus-"+strconv.Itoa(gpuCount), func(t *testing.T) {
			workspace := domain.DevWorkspace{ID: "ws-sudo", TenantID: "tenant-a", UserID: "user-a", Name: "debug-sudo", Namespace: "tenant-a", GPUCount: gpuCount}
			manifest, err := RenderDevRayCluster(workspace, WorkspaceRenderOptions{Image: "registry.example/dev@sha256:" + strings.Repeat("a", 64)})
			if err != nil {
				t.Fatal(err)
			}
			worker := dataMountDevPodSpec(t, manifest, "worker")
			workerContainers, _, _ := nestedSlice(worker, "containers")
			security, _, _ := nestedMap(workerContainers[0].(map[string]any), "securityContext")
			if security["allowPrivilegeEscalation"] != true {
				t.Fatalf("interactive worker must permit setuid sudo: %#v", security)
			}
			if security["privileged"] != false {
				t.Fatalf("container-local sudo must explicitly remain unprivileged: %#v", security)
			}
			capabilities, _, _ := nestedMap(security, "capabilities")
			if !reflect.DeepEqual(capabilities["drop"], []any{"ALL"}) {
				t.Fatalf("worker must start from an empty capability set: %#v", capabilities)
			}
			want := map[string]bool{"SETUID": true, "SETGID": true, "CHOWN": true, "DAC_OVERRIDE": true, "FOWNER": true, "FSETID": true, "SYS_CHROOT": true, "AUDIT_WRITE": true}
			added, ok := capabilities["add"].([]any)
			if !ok || len(added) != len(want) {
				t.Fatalf("worker must add only the reviewed sudo/apt capability set: %#v", capabilities)
			}
			seen := make(map[string]bool)
			for _, value := range added {
				capability, ok := value.(string)
				if !ok || !want[capability] || seen[capability] {
					t.Fatalf("unexpected or duplicate worker capability: %#v", capabilities)
				}
				seen[capability] = true
			}
			if _, set := security["runAsUser"]; set {
				t.Fatalf("workspace must retain the image user until explicit sudo: %#v", security)
			}
			head := dataMountDevPodSpec(t, manifest, "head")
			assertNoWorkspaceSudoPrivileges(t, "workspace head", head)
		})
	}
}

func TestRenderDevRayClusterSudoKeepsPodAndStorageIsolation(t *testing.T) {
	for _, serviceAccount := range []string{"", "workspace-runtime"} {
		t.Run("service-account-"+serviceAccount, func(t *testing.T) {
			workspace := domain.DevWorkspace{ID: "ws-sudo", TenantID: "tenant-a", UserID: "user-a", Name: "debug-sudo", Namespace: "tenant-a", GPUCount: 0}
			manifest, err := RenderDevRayCluster(workspace, WorkspaceRenderOptions{
				Image: "registry.example/dev@sha256:" + strings.Repeat("a", 64), ServiceAccount: serviceAccount,
				DataMounts: DataMountPlan{
					Personal: &DataMountRoot{ClaimName: "data-user-a"},
					Team: &DataMountRoot{ClaimName: "data-team-a", ReadOnly: true},
					Public: &DataMountRoot{ClaimName: "data-public", ReadOnly: true},
					IDCOriginal: &DataMountRoot{ClaimName: "idc-original-ro", ReadOnly: true},
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			for _, role := range []string{"head", "worker"} {
				pod := dataMountDevPodSpec(t, manifest, role)
				if pod["automountServiceAccountToken"] != false {
					t.Errorf("%s must never mount Kubernetes tokens, including with an explicit service account", role)
				}
				for _, field := range []string{"hostNetwork", "hostPID", "hostIPC", "shareProcessNamespace"} {
					if pod[field] == true {
						t.Fatalf("%s must not gain %s", role, field)
					}
				}
				security, _, _ := nestedMap(pod, "securityContext")
				seccomp, _, _ := nestedMap(security, "seccompProfile")
				if seccomp["type"] != "RuntimeDefault" || security["fsGroup"] != int64(1000) || security["fsGroupChangePolicy"] != "OnRootMismatch" {
					t.Fatalf("%s lost existing seccomp/storage isolation: %#v", role, security)
				}
				volumes, _, _ := nestedSlice(pod, "volumes")
				for _, raw := range volumes {
					volume := raw.(map[string]any)
					if _, found := volume["hostPath"]; found {
						t.Fatalf("%s must not gain hostPath: %#v", role, volume)
					}
					if _, found := volume["projected"]; found {
						t.Fatalf("%s must not gain a projected token volume: %#v", role, volume)
					}
				}
				assertStorageMount(t, pod, "platform-data-personal", "data-user-a", domain.MyStorageMountPath, false)
				assertStorageMount(t, pod, "platform-data-team", "data-team-a", domain.TeamStorageMountPath, true)
				assertStorageMount(t, pod, "platform-data-public", "data-public", domain.PublicStorageMountPath, true)
				assertStorageMount(t, pod, "platform-data-idc-original", "idc-original-ro", domain.IDCOriginalMountPath, true)
			}
		})
	}
}

func TestWorkspaceSudoDoesNotChangeTrainingRayJobPrivileges(t *testing.T) {
	manifest, err := RenderRayJob(validRenderJob(), testRenderOptions())
	if err != nil {
		t.Fatal(err)
	}
	cluster, _, _ := nestedMap(manifest.Object, "spec", "rayClusterSpec")
	head, _, _ := nestedMap(cluster, "headGroupSpec", "template", "spec")
	assertNoWorkspaceSudoPrivileges(t, "training head", head)
	workers, _, _ := nestedSlice(cluster, "workerGroupSpecs")
	for _, value := range workers {
		worker, _, _ := nestedMap(value.(map[string]any), "template", "spec")
		assertNoWorkspaceSudoPrivileges(t, "training worker", worker)
	}
}

func assertNoWorkspaceSudoPrivileges(t *testing.T, label string, pod map[string]any) {
	t.Helper()
	containers, _, _ := nestedSlice(pod, "containers")
	if len(containers) == 0 {
		t.Fatalf("%s has no containers", label)
	}
	for _, value := range containers {
		security, _, _ := nestedMap(value.(map[string]any), "securityContext")
		capabilities, _, _ := nestedMap(security, "capabilities")
		if security["allowPrivilegeEscalation"] != false || security["privileged"] == true || !reflect.DeepEqual(capabilities["drop"], []any{"ALL"}) {
			t.Fatalf("%s acquired interactive worker privileges: %#v", label, security)
		}
		if additions, found := capabilities["add"]; found && !reflect.DeepEqual(additions, []any{}) {
			t.Fatalf("%s acquired added capabilities: %#v", label, capabilities)
		}
	}
}
