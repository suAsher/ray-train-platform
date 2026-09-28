package k8s

import (
	"context"
	"reflect"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	"ray-train-platform-backend/domain"
)

func workspacePersonalBinding() domain.DataMountBinding {
	return domain.DataMountBinding{
		ID: "personal-current", TenantID: "tenant-a", UserID: "user-a", StorageKey: "user.a", StorageTenantID: "original-team",
		Scope: domain.DataMountScopePersonal, SpaceID: domain.DataSpaceWorkspace, ClaimName: "personal-current",
		Driver: domain.FSXCSIDriver, RootPrefix: "ray-train/tenants/original-team/users/user.a/",
		VolumeAttributesJSON: `{"type":"TOS","bucket":"shanghai-data-transfer","server":"tos-cn-shanghai.ivolces.com","region":"cn-shanghai","path":"/ray-train/tenants/original-team/users/user.a"}`,
		Status:               domain.DataMountBindingReady,
	}
}

func TestBuildWorkspaceMountResourcesUsesRetainedCompatibleExistingPrefix(t *testing.T) {
	binding := workspacePersonalBinding()
	pv, pvc, err := BuildWorkspaceMountResources(binding, "tenant-tenant-a", "1Ti")
	if err != nil {
		t.Fatal(err)
	}
	if pv.Spec.CSI == nil || pv.Spec.CSI.Driver != domain.FSXCSIDriver || pv.Spec.CSI.VolumeAttributes["path"] != "/ray-train/tenants/original-team/users/user.a/workspace" {
		t.Fatalf("workspace must use the stable existing personal workspace prefix: %#v", pv.Spec.CSI)
	}
	wantOptions := []string{"no_writeback_cache", "uid=1000", "gid=1000", "file_mode=770", "dir_mode=770", "tos_allow_delete=true", "compatible_mode=true"}
	if !reflect.DeepEqual(pv.Spec.MountOptions, wantOptions) {
		t.Fatalf("workspace compatibility options: got %#v want %#v", pv.Spec.MountOptions, wantOptions)
	}
	if pv.Spec.PersistentVolumeReclaimPolicy != corev1.PersistentVolumeReclaimRetain || pv.Spec.StorageClassName != "" || pvc.Spec.StorageClassName == nil || *pvc.Spec.StorageClassName != "" {
		t.Fatalf("workspace must be retained static storage without dynamic disk provisioning: pv=%#v pvc=%#v", pv.Spec, pvc.Spec)
	}
	if pv.Spec.ClaimRef == nil || pv.Spec.ClaimRef.Name != pvc.Name || pv.Spec.ClaimRef.Namespace != pvc.Namespace || pvc.Spec.VolumeName != pv.Name || pvc.Namespace != "tenant-tenant-a" {
		t.Fatalf("workspace PV/PVC must be mutually prebound in the current namespace: pv=%#v pvc=%#v", pv.Spec, pvc)
	}
	if pv.Spec.CSI.VolumeHandle != pv.Name || pv.Name == "ray-data-"+binding.ID || pvc.Name == binding.ClaimName || !strings.Contains(pv.Name, "fsx-compat-v1") {
		t.Fatalf("workspace needs a separate versioned CSI identity: pv=%q handle=%q pvc=%q", pv.Name, pv.Spec.CSI.VolumeHandle, pvc.Name)
	}
	if pv.Labels[managedDataMountLabel] == binding.ID || pv.Labels[managedDataMountLabel] == "" || pvc.Labels[managedDataMountLabel] != pv.Labels[managedDataMountLabel] {
		t.Fatalf("workspace needs its own shared ownership identity: pv=%#v pvc=%#v", pv.Labels, pvc.Labels)
	}
	if pv.Spec.CSI.NodePublishSecretRef != nil || pv.Spec.CSI.NodeStageSecretRef != nil || pv.Spec.CSI.ControllerPublishSecretRef != nil || pv.Spec.CSI.ControllerExpandSecretRef != nil || pv.Spec.CSI.NodeExpandSecretRef != nil {
		t.Fatalf("workspace must remain secretless: %#v", pv.Spec.CSI)
	}
	if !reflect.DeepEqual(binding, workspacePersonalBinding()) {
		t.Fatal("workspace resource construction changed the personal binding")
	}
	legacy, _, err := BuildDataMountResources(binding, "tenant-tenant-a", "1Ti")
	if err != nil || legacy.Spec.CSI.VolumeAttributes["path"] != "/ray-train/tenants/original-team/users/user.a" || !reflect.DeepEqual(legacy.Spec.MountOptions, wantOptions[:len(wantOptions)-1]) {
		t.Fatalf("personal root contract must remain unchanged: pv=%#v err=%v", legacy, err)
	}
}

func TestBuildWorkspaceMountResourcesIdentityUsesNamespaceAndStableRoot(t *testing.T) {
	binding := workspacePersonalBinding()
	first, firstClaim, err := BuildWorkspaceMountResources(binding, "tenant-tenant-a", "1Ti")
	if err != nil {
		t.Fatal(err)
	}
	recreated := binding
	recreated.ID, recreated.ClaimName = "personal-recreated", "personal-recreated"
	recreated.Status = domain.DataMountBindingPending
	same, sameClaim, err := BuildWorkspaceMountResources(recreated, "tenant-tenant-a", "1Ti")
	if err != nil || same.Name != first.Name || sameClaim.Name != firstClaim.Name {
		t.Fatalf("recreating binding for the same home must preserve workspace storage: pv=%#v pvc=%#v err=%v", same, sameClaim, err)
	}
	for _, change := range []struct {
		name      string
		namespace string
		binding   domain.DataMountBinding
	}{
		{name: "namespace", namespace: "tenant-other", binding: binding},
		{name: "root", namespace: "tenant-tenant-a", binding: func() domain.DataMountBinding {
			other := binding
			other.StorageKey = "other.user"
			other.RootPrefix = strings.ReplaceAll(other.RootPrefix, "user.a", "other.user")
			other.VolumeAttributesJSON = strings.ReplaceAll(other.VolumeAttributesJSON, "user.a", "other.user")
			return other
		}()},
		{name: "bucket", namespace: "tenant-tenant-a", binding: func() domain.DataMountBinding {
			other := binding
			other.VolumeAttributesJSON = strings.ReplaceAll(other.VolumeAttributesJSON, "shanghai-data-transfer", "other-approved-bucket")
			return other
		}()},
	} {
		t.Run(change.name, func(t *testing.T) {
			pv, pvc, err := BuildWorkspaceMountResources(change.binding, change.namespace, "1Ti")
			if err != nil {
				t.Fatal(err)
			}
			if pv.Name == first.Name || pvc.Name == firstClaim.Name || pv.Spec.CSI.VolumeHandle == first.Spec.CSI.VolumeHandle || pv.Labels[managedDataMountLabel] == first.Labels[managedDataMountLabel] {
				t.Fatal("distinct namespace or storage home reused the same workspace identity")
			}
		})
	}
}

func TestBuildWorkspaceMountResourcesRejectsInvalidOriginalBinding(t *testing.T) {
	for name, change := range map[string]func(*domain.DataMountBinding){
		"wrong root": func(b *domain.DataMountBinding) {
			b.RootPrefix = "ray-train/tenants/original-team/users/other/"
			b.VolumeAttributesJSON = strings.ReplaceAll(b.VolumeAttributesJSON, "user.a", "other")
		},
		"workspace child as personal root": func(b *domain.DataMountBinding) {
			b.RootPrefix += "workspace/"
			b.VolumeAttributesJSON = strings.ReplaceAll(b.VolumeAttributesJSON, "users/user.a", "users/user.a/workspace")
		},
		"mismatched attributes": func(b *domain.DataMountBinding) {
			b.VolumeAttributesJSON = strings.ReplaceAll(b.VolumeAttributesJSON, "user.a", "other")
		},
		"readonly personal":  func(b *domain.DataMountBinding) { b.ReadOnly = true },
		"failed binding":     func(b *domain.DataMountBinding) { b.Status = domain.DataMountBindingFailed },
		"non-personal scope": func(b *domain.DataMountBinding) { b.Scope = domain.DataMountScopeTenant },
		"wrong space":        func(b *domain.DataMountBinding) { b.SpaceID = domain.DataSpacePublic },
		"wrong driver":       func(b *domain.DataMountBinding) { b.Driver = "disk.csi.volcengine.com" },
		"long-lived secret":  func(b *domain.DataMountBinding) { b.SecretName = "secret" },
		"attribute secret": func(b *domain.DataMountBinding) {
			b.VolumeAttributesJSON = strings.Replace(b.VolumeAttributesJSON, `"type":"TOS"`, `"type":"TOS","secretName":"secret"`, 1)
		},
	} {
		t.Run(name, func(t *testing.T) {
			binding := workspacePersonalBinding()
			change(&binding)
			if _, _, err := BuildWorkspaceMountResources(binding, "tenant-tenant-a", "1Ti"); err == nil {
				t.Fatal("invalid original personal binding was accepted")
			}
		})
	}
	for _, input := range [][2]string{{"bad/namespace", "1Ti"}, {"tenant-a", "0"}, {"tenant-a", "invalid"}} {
		if _, _, err := BuildWorkspaceMountResources(workspacePersonalBinding(), input[0], input[1]); err == nil {
			t.Fatalf("invalid namespace/capacity accepted: %#v", input)
		}
	}
}

func TestEnsureWorkspaceMountResourcesIsIdempotentAndWaitsForBinding(t *testing.T) {
	ctx := context.Background()
	binding := workspacePersonalBinding()
	fake := k8sfake.NewSimpleClientset()
	client := NewClientFromInterfaces(nil, fake)
	claim, ready, err := client.EnsureWorkspaceMountResources(ctx, binding, "tenant-tenant-a", "1Ti")
	if err != nil || ready || claim == "" || claim == binding.ClaimName {
		t.Fatalf("new static workspace claim must wait for binding: claim=%q ready=%t err=%v", claim, ready, err)
	}
	pvc, err := fake.CoreV1().PersistentVolumeClaims("tenant-tenant-a").Get(ctx, claim, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	pvc.Status.Phase = corev1.ClaimBound
	if _, err := fake.CoreV1().PersistentVolumeClaims(pvc.Namespace).UpdateStatus(ctx, pvc, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	claimAgain, ready, err := client.EnsureWorkspaceMountResources(ctx, binding, "tenant-tenant-a", "1Ti")
	if err != nil || !ready || claimAgain != claim {
		t.Fatalf("bound workspace claim must be reused: claim=%q ready=%t err=%v", claimAgain, ready, err)
	}
	pvs, _ := fake.CoreV1().PersistentVolumes().List(ctx, metav1.ListOptions{})
	pvcs, _ := fake.CoreV1().PersistentVolumeClaims(pvc.Namespace).List(ctx, metav1.ListOptions{})
	if len(pvs.Items) != 1 || len(pvcs.Items) != 1 {
		t.Fatalf("idempotent ensure created extra resources: PVs=%d PVCs=%d", len(pvs.Items), len(pvcs.Items))
	}
}

func TestEnsureWorkspaceMountResourcesRefusesConflictingContracts(t *testing.T) {
	for name, change := range map[string]func(*corev1.PersistentVolume, *corev1.PersistentVolumeClaim){
		"foreign PV":  func(pv *corev1.PersistentVolume, _ *corev1.PersistentVolumeClaim) { pv.Labels = nil },
		"foreign PVC": func(_ *corev1.PersistentVolume, pvc *corev1.PersistentVolumeClaim) { pvc.Labels = nil },
		"wrong handle": func(pv *corev1.PersistentVolume, _ *corev1.PersistentVolumeClaim) {
			pv.Spec.CSI.VolumeHandle = "tenant-root"
		},
		"delete reclaim": func(pv *corev1.PersistentVolume, _ *corev1.PersistentVolumeClaim) {
			pv.Spec.PersistentVolumeReclaimPolicy = corev1.PersistentVolumeReclaimDelete
		},
		"wrong mount mode": func(pv *corev1.PersistentVolume, _ *corev1.PersistentVolumeClaim) {
			pv.Spec.MountOptions = governedFSXMountOptions(false)
		},
		"wrong PV capacity": func(pv *corev1.PersistentVolume, _ *corev1.PersistentVolumeClaim) {
			pv.Spec.Capacity[corev1.ResourceStorage] = resource.MustParse("2Ti")
		},
		"wrong PVC capacity": func(_ *corev1.PersistentVolume, pvc *corev1.PersistentVolumeClaim) {
			pvc.Spec.Resources.Requests[corev1.ResourceStorage] = resource.MustParse("2Ti")
		},
		"dynamic PV class": func(pv *corev1.PersistentVolume, _ *corev1.PersistentVolumeClaim) { pv.Spec.StorageClassName = "disk" },
		"dynamic PVC class": func(_ *corev1.PersistentVolume, pvc *corev1.PersistentVolumeClaim) {
			value := "disk"
			pvc.Spec.StorageClassName = &value
		},
		"wrong namespace": func(pv *corev1.PersistentVolume, _ *corev1.PersistentVolumeClaim) {
			pv.Spec.ClaimRef.Namespace = "other"
		},
		"wrong claim": func(pv *corev1.PersistentVolume, _ *corev1.PersistentVolumeClaim) { pv.Spec.ClaimRef.Name = "other" },
		"stale claim UID": func(pv *corev1.PersistentVolume, pvc *corev1.PersistentVolumeClaim) {
			pv.Spec.ClaimRef.UID = "old"
			pvc.UID = "new"
		},
		"wrong volume": func(_ *corev1.PersistentVolume, pvc *corev1.PersistentVolumeClaim) {
			pvc.Spec.VolumeName = "tenant-root"
		},
		"wrong access mode": func(pv *corev1.PersistentVolume, _ *corev1.PersistentVolumeClaim) {
			pv.Spec.AccessModes = []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}
		},
		"deleting PV": func(pv *corev1.PersistentVolume, _ *corev1.PersistentVolumeClaim) {
			now := metav1.Now()
			pv.DeletionTimestamp = &now
		},
		"deleting PVC": func(_ *corev1.PersistentVolume, pvc *corev1.PersistentVolumeClaim) {
			now := metav1.Now()
			pvc.DeletionTimestamp = &now
		},
		"block PV": func(pv *corev1.PersistentVolume, _ *corev1.PersistentVolumeClaim) {
			mode := corev1.PersistentVolumeBlock
			pv.Spec.VolumeMode = &mode
		},
		"block PVC": func(_ *corev1.PersistentVolume, pvc *corev1.PersistentVolumeClaim) {
			mode := corev1.PersistentVolumeBlock
			pvc.Spec.VolumeMode = &mode
		},
		"secret reference": func(pv *corev1.PersistentVolume, _ *corev1.PersistentVolumeClaim) {
			pv.Spec.CSI.NodePublishSecretRef = &corev1.SecretReference{Name: "secret", Namespace: "tenant-tenant-a"}
		},
	} {
		t.Run(name, func(t *testing.T) {
			binding := workspacePersonalBinding()
			pv, pvc, err := BuildWorkspaceMountResources(binding, "tenant-tenant-a", "1Ti")
			if err != nil {
				t.Fatal(err)
			}
			pvc.Status.Phase = corev1.ClaimBound
			change(pv, pvc)
			fake := k8sfake.NewSimpleClientset(pv, pvc)
			client := NewClientFromInterfaces(nil, fake)
			if _, ready, err := client.EnsureWorkspaceMountResources(context.Background(), binding, pvc.Namespace, "1Ti"); err == nil || ready {
				t.Fatalf("conflicting workspace resource was accepted: ready=%t err=%v", ready, err)
			}
			for _, action := range fake.Actions() {
				if action.GetVerb() != "get" {
					t.Fatalf("conflict must never change existing resources: %#v", action)
				}
			}
		})
	}
}

func TestEnsureWorkspaceMountResourcesRequiresInitializedClient(t *testing.T) {
	for _, client := range []*Client{nil, NewClientFromInterfaces(nil, nil)} {
		if claim, ready, err := client.EnsureWorkspaceMountResources(context.Background(), workspacePersonalBinding(), "tenant-tenant-a", "1Ti"); err == nil || ready || claim != "" {
			t.Fatalf("uninitialized client accepted workspace mount: claim=%q ready=%t err=%v", claim, ready, err)
		}
	}
}

func TestDevWorkspaceCompatibleMountOverridesBothWorkspaceAliases(t *testing.T) {
	for _, subPath := range []string{"", "tenants/original-team/users/user.a"} {
		t.Run(subPath, func(t *testing.T) {
			plan := DataMountPlan{
				Personal:  &DataMountRoot{ClaimName: "personal-root", SubPath: subPath},
				Workspace: &DataMountRoot{ClaimName: "workspace-compatible"},
				Team:      &DataMountRoot{ClaimName: "team-data", ReadOnly: true},
			}
			manifest, err := RenderDevRayCluster(domain.DevWorkspace{ID: "ws-a", TenantID: "tenant-a", UserID: "user-a", Name: "debug-a", Namespace: "tenant-a"}, WorkspaceRenderOptions{
				Image: "registry.example/dev@sha256:" + strings.Repeat("a", 64), DataMounts: plan,
			})
			if err != nil {
				t.Fatal(err)
			}
			for _, group := range []string{"head", "worker"} {
				pod := dataMountDevPodSpec(t, manifest, group)
				if subPath == "" {
					assertStorageMount(t, pod, "platform-data-personal", "personal-root", domain.MyStorageMountPath, false)
				} else {
					assertStorageMountWithSubPath(t, pod, "platform-data-personal", "personal-root", domain.MyStorageMountPath, subPath, false)
				}
				assertStorageMount(t, pod, "platform-data-workspace", "workspace-compatible", domain.WorkspaceMountPath, false)
				assertStorageMount(t, pod, "platform-data-workspace", "workspace-compatible", domain.MyStorageMountPath+"/workspace", false)
				assertStorageMount(t, pod, "platform-data-team", "team-data", domain.TeamStorageMountPath, true)
				if countPVCVolumes(pod, "workspace-compatible") != 1 || countPVCVolumes(pod, "personal-root") != 1 {
					t.Fatalf("each claim must be mounted once in %s: %#v", group, pod["volumes"])
				}
				containers, _, _ := nestedSlice(pod, "containers")
				seen := map[string]bool{}
				for _, raw := range containers[0].(map[string]any)["volumeMounts"].([]any) {
					mount := raw.(map[string]any)
					mountPath := mount["mountPath"].(string)
					if seen[mountPath] {
						t.Fatalf("duplicate mount target %q in %s", mountPath, group)
					}
					seen[mountPath] = true
				}
			}
		})
	}
}

func TestDataMountPlanValidatesCompatibleWorkspace(t *testing.T) {
	for name, plan := range map[string]DataMountPlan{
		"missing personal": {Workspace: &DataMountRoot{ClaimName: "workspace-compatible"}},
		"readonly":         {Personal: &DataMountRoot{ClaimName: "personal-root"}, Workspace: &DataMountRoot{ClaimName: "workspace-compatible", ReadOnly: true}},
		"unsafe claim":     {Personal: &DataMountRoot{ClaimName: "personal-root"}, Workspace: &DataMountRoot{ClaimName: "unsafe/claim"}},
		"unsafe path":      {Personal: &DataMountRoot{ClaimName: "personal-root"}, Workspace: &DataMountRoot{ClaimName: "workspace-compatible", SubPath: "../other"}},
	} {
		t.Run(name, func(t *testing.T) {
			if err := plan.Validate(); err == nil {
				t.Fatal("unsafe workspace plan accepted")
			}
		})
	}
}
