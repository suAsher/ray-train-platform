package k8s

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
	"ray-train-platform-backend/domain"
)

func TestEnsureDataMountResourcesIsIdempotentAndWaitsForBinding(t *testing.T) {
	binding := domain.DataMountBinding{
		ID: "mount-abc123", TenantID: "tenant-a", UserID: "user-a", Scope: domain.DataMountScopePersonal, SpaceID: domain.DataSpaceWorkspace,
		ClaimName: "data-user-a", Driver: domain.FSXCSIDriver, RootPrefix: "ray-train/tenants/tenant-a/users/user-a/",
		VolumeAttributesJSON: `{"type":"TOS","bucket":"shanghai-data-transfer","server":"tos-cn-shanghai.ivolces.com","region":"cn-shanghai","path":"/ray-train/tenants/tenant-a/users/user-a"}`,
		Status:               domain.DataMountBindingPending,
	}
	client := NewClientFromInterfaces(nil, k8sfake.NewSimpleClientset())
	ready, err := client.EnsureDataMountResources(context.Background(), binding, "tenant-tenant-a", "1Ti")
	if err != nil || ready {
		t.Fatalf("new PVC should be pending: ready=%t err=%v", ready, err)
	}
	if _, err := client.kubernetes.CoreV1().PersistentVolumes().Get(context.Background(), "ray-data-mount-abc123", metav1.GetOptions{}); err != nil {
		t.Fatalf("PV not created: %v", err)
	}
	pvc, err := client.kubernetes.CoreV1().PersistentVolumeClaims("tenant-tenant-a").Get(context.Background(), "data-user-a", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("PVC not created: %v", err)
	}
	pvc.Status.Phase = corev1.ClaimBound
	if _, err := client.kubernetes.CoreV1().PersistentVolumeClaims("tenant-tenant-a").UpdateStatus(context.Background(), pvc, metav1.UpdateOptions{}); err != nil {
		t.Fatalf("bind PVC: %v", err)
	}
	ready, err = client.EnsureDataMountResources(context.Background(), binding, "tenant-tenant-a", "1Ti")
	if err != nil || !ready {
		t.Fatalf("bound PVC should become ready: ready=%t err=%v", ready, err)
	}
}

func TestEnsureDataMountResourcesRefusesForeignClaim(t *testing.T) {
	binding := domain.DataMountBinding{
		ID: "mount-abc123", TenantID: "tenant-a", UserID: "user-a", Scope: domain.DataMountScopePersonal, SpaceID: domain.DataSpaceWorkspace,
		ClaimName: "data-user-a", Driver: domain.FSXCSIDriver, RootPrefix: "ray-train/tenants/tenant-a/users/user-a/",
		VolumeAttributesJSON: `{"type":"TOS","bucket":"shanghai-data-transfer","server":"tos-cn-shanghai.ivolces.com","region":"cn-shanghai","path":"/ray-train/tenants/tenant-a/users/user-a"}`,
		Status:               domain.DataMountBindingPending,
	}
	client := NewClientFromInterfaces(nil, k8sfake.NewSimpleClientset(&corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "data-user-a", Namespace: "tenant-tenant-a"}}))
	if _, err := client.EnsureDataMountResources(context.Background(), binding, "tenant-tenant-a", "1Ti"); err == nil {
		t.Fatal("foreign PVC was adopted")
	}
}

func TestEnsureDataMountResourcesRefusesLegacyFUSEPermissions(t *testing.T) {
	binding := domain.DataMountBinding{
		ID: "mount-abc123", TenantID: "tenant-a", UserID: "user-a", Scope: domain.DataMountScopePersonal, SpaceID: domain.DataSpaceWorkspace,
		ClaimName: "data-user-a", Driver: domain.FSXCSIDriver, RootPrefix: "ray-train/tenants/tenant-a/users/user-a/",
		VolumeAttributesJSON: `{"type":"TOS","bucket":"shanghai-data-transfer","server":"tos-cn-shanghai.ivolces.com","region":"cn-shanghai","path":"/ray-train/tenants/tenant-a/users/user-a"}`,
		Status:               domain.DataMountBindingPending,
	}
	legacy, _, err := BuildDataMountResources(binding, "tenant-tenant-a", "1Ti")
	if err != nil {
		t.Fatal(err)
	}
	legacy.Spec.MountOptions = nil
	client := NewClientFromInterfaces(nil, k8sfake.NewSimpleClientset(legacy))
	if _, err := client.EnsureDataMountResources(context.Background(), binding, "tenant-tenant-a", "1Ti"); err == nil {
		t.Fatal("legacy FSX PV without the governed non-root mount contract was accepted")
	}
}

func TestEnsureIDCDataMountResourcesIsIdempotentAndDoesNotAdoptOtherNFSExport(t *testing.T) {
	binding, err := domain.NewIDCDataMountBinding("idc-original-a", "tenant-a", domain.DataSpaceIDCOriginal, "idc-original-a")
	if err != nil {
		t.Fatal(err)
	}
	client := NewClientFromInterfaces(nil, k8sfake.NewSimpleClientset())
	source := IDCDataMountSource{Server: "192.0.2.20", Path: "/exports/original"}
	ready, err := client.EnsureIDCDataMountResources(context.Background(), binding, "tenant-tenant-a", "1Pi", source)
	if err != nil || ready {
		t.Fatalf("new IDC PVC should be pending: ready=%t err=%v", ready, err)
	}
	pvc, err := client.kubernetes.CoreV1().PersistentVolumeClaims("tenant-tenant-a").Get(context.Background(), binding.ClaimName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get IDC PVC: %v", err)
	}
	pvc.Status.Phase = corev1.ClaimBound
	if _, err := client.kubernetes.CoreV1().PersistentVolumeClaims("tenant-tenant-a").UpdateStatus(context.Background(), pvc, metav1.UpdateOptions{}); err != nil {
		t.Fatalf("mark IDC PVC bound: %v", err)
	}
	ready, err = client.EnsureIDCDataMountResources(context.Background(), binding, "tenant-tenant-a", "1Pi", source)
	if err != nil || !ready {
		t.Fatalf("bound IDC PVC should become ready: ready=%t err=%v", ready, err)
	}
	if _, err := client.EnsureIDCDataMountResources(context.Background(), binding, "tenant-tenant-a", "1Pi", IDCDataMountSource{Server: "192.0.2.20", Path: "/exports/other"}); err == nil {
		t.Fatal("IDC mount controller adopted a differently configured NFS export")
	}
}

func TestEnsureRayJobIsIdempotent(t *testing.T) {
	client := &Client{dynamic: dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())}
	job := validRenderJob()
	manifest, err := RenderRayJob(job, testRenderOptions())
	if err != nil {
		t.Fatalf("render ray job: %v", err)
	}
	first, err := client.EnsureRayJob(context.Background(), manifest)
	if err != nil {
		t.Fatalf("first ensure: %v", err)
	}
	second, err := client.EnsureRayJob(context.Background(), manifest)
	if err != nil {
		t.Fatalf("second ensure: %v", err)
	}
	if first.GetUID() != second.GetUID() || first.GetResourceVersion() != second.GetResourceVersion() {
		t.Fatalf("expected idempotent resource: first=%v second=%v", first, second)
	}
}

func TestEnsureRayJobDoesNotAdoptForeignResource(t *testing.T) {
	job := validRenderJob()
	manifest, err := RenderRayJob(job, testRenderOptions())
	if err != nil {
		t.Fatalf("render ray job: %v", err)
	}
	foreign := manifest.DeepCopy()
	labels := foreign.GetLabels()
	labels["ray.io/job-id"] = "other-job"
	foreign.SetLabels(labels)
	client := &Client{dynamic: dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), foreign)}

	if _, err := client.EnsureRayJob(context.Background(), manifest); err == nil {
		t.Fatal("expected ownership error")
	}
}

func TestActivateManagedRayJobRequiresExactAdoptedIdentityAndEnablesKueue(t *testing.T) {
	job := managedEmptyAttempt(2, domain.StateRecovering)
	job.RayJobName = job.ID + "-a2"
	options := testRenderOptions()
	options.managedCreationFence = 4
	manifest, err := RenderRayJob(job, options)
	if err != nil {
		t.Fatal(err)
	}
	manifest.SetUID(types.UID("uid-attempt-2"))
	dynamicClient := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), manifest)
	client := NewClientFromInterfaces(dynamicClient, nil)

	if _, err := client.ActivateManagedRayJob(context.Background(), manifest.GetNamespace(), manifest.GetName(), job.ID, "uid-wrong", 2, 4, job.Spec.Queue); err == nil {
		t.Fatal("activation accepted a stale UID")
	}
	active, err := client.ActivateManagedRayJob(context.Background(), manifest.GetNamespace(), manifest.GetName(), job.ID, "uid-attempt-2", 2, 4, job.Spec.Queue)
	if err != nil {
		t.Fatal(err)
	}
	if active.GetLabels()["kueue.x-k8s.io/queue-name"] != job.Spec.Queue {
		t.Fatalf("adopted RayJob was not enabled for Kueue: %v", active.GetLabels())
	}
	if active.GetAnnotations()[managedPendingAdoptionKey] != "" {
		t.Fatalf("adopted RayJob retained quarantine marker: %v", active.GetAnnotations())
	}
}

func TestDeleteRayJobRequiresExpectedUIDAndUsesForegroundPrecondition(t *testing.T) {
	manifest, err := RenderRayJob(validRenderJob(), testRenderOptions())
	if err != nil {
		t.Fatal(err)
	}
	manifest.SetUID(types.UID("uid-attempt-1"))
	dynamicClient := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), manifest)
	client := NewClientFromInterfaces(dynamicClient, nil)

	if err := client.DeleteRayJob(context.Background(), manifest.GetNamespace(), manifest.GetName(), validRenderJob().ID, "uid-wrong"); err == nil {
		t.Fatal("deletion with a stale UID was accepted")
	}
	if _, err := client.GetRayJob(context.Background(), manifest.GetNamespace(), manifest.GetName()); err != nil {
		t.Fatalf("stale UID deleted the RayJob: %v", err)
	}

	if err := client.DeleteRayJob(context.Background(), manifest.GetNamespace(), manifest.GetName(), validRenderJob().ID, "uid-attempt-1"); err != nil {
		t.Fatal(err)
	}
	deleteOptions := rayJobDeleteOptions("uid-attempt-1")
	if deleteOptions.Preconditions == nil || deleteOptions.Preconditions.UID == nil || string(*deleteOptions.Preconditions.UID) != "uid-attempt-1" {
		t.Fatalf("delete did not carry UID precondition: %+v", deleteOptions)
	}
	if deleteOptions.PropagationPolicy == nil || *deleteOptions.PropagationPolicy != metav1.DeletePropagationForeground {
		t.Fatalf("delete did not use foreground propagation: %+v", deleteOptions)
	}
}

func TestUpdateRayJobCleanupTTLRetriesResourceVersionConflict(t *testing.T) {
	manifest, err := RenderRayJob(validRenderJob(), testRenderOptions())
	if err != nil {
		t.Fatalf("render RayJob: %v", err)
	}
	dynamicClient := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), manifest)
	conflicts := 0
	dynamicClient.PrependReactor("update", "rayjobs", func(action k8stesting.Action) (bool, runtime.Object, error) {
		if conflicts == 0 {
			conflicts++
			return true, nil, apierrors.NewConflict(schema.GroupResource{Group: "ray.io", Resource: "rayjobs"}, manifest.GetName(), errors.New("stale resource version"))
		}
		return false, nil, nil
	})
	client := NewClientFromInterfaces(dynamicClient, nil)

	updated, err := client.UpdateRayJobCleanupTTL(context.Background(), manifest, validRenderJob().ID, 60)
	if err != nil {
		t.Fatalf("update cleanup TTL after conflict: %v", err)
	}
	ttl, found, err := unstructured.NestedInt64(updated.Object, "spec", "ttlSecondsAfterFinished")
	if err != nil || !found || ttl != 60 || conflicts != 1 {
		t.Fatalf("ttl=%d found=%v conflicts=%d err=%v", ttl, found, conflicts, err)
	}
}

func TestEnsureRayJobRejectsNewManifestWithoutSubmitterRestartPolicy(t *testing.T) {
	client := &Client{dynamic: dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())}
	manifest, err := RenderRayJob(validRenderJob(), testRenderOptions())
	if err != nil {
		t.Fatalf("render ray job: %v", err)
	}
	unstructured.RemoveNestedField(manifest.Object, "spec", "submitterPodTemplate", "spec", "restartPolicy")

	if _, err := client.EnsureRayJob(context.Background(), manifest); err == nil {
		t.Fatal("expected missing submitter restart policy to be rejected before creation")
	}
}

func TestRayJobResourceUsesExpectedGroupVersion(t *testing.T) {
	if rayJobGVR != (schema.GroupVersionResource{Group: "ray.io", Version: "v1", Resource: "rayjobs"}) {
		t.Fatalf("unexpected ray job resource: %s", rayJobGVR.String())
	}
	if (&unstructured.Unstructured{}).GetKind() != "" {
		t.Fatal("sanity check failed")
	}
}
