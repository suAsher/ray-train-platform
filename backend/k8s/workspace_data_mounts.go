package k8s

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"reflect"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"ray-train-platform-backend/domain"
)

// Changing this profile requires new resources: CSI stages mount options by
// volume handle, and a live tenant-root volume must never be reconfigured.
const workspaceFSXProfileVersion = "fsx-compat-v1"

// BuildWorkspaceMountResources creates a separate static adapter for the
// existing personal workspace prefix. The original binding is validated before
// deriving the child path; the personal-root validation is never relaxed.
func BuildWorkspaceMountResources(personal domain.DataMountBinding, namespace, capacity string) (*corev1.PersistentVolume, *corev1.PersistentVolumeClaim, error) {
	if personal.Scope != domain.DataMountScopePersonal || personal.SpaceID != domain.DataSpaceWorkspace || personal.ReadOnly {
		return nil, nil, fmt.Errorf("workspace mount requires a writable personal workspace binding")
	}
	basePV, _, err := BuildDataMountResources(personal, namespace, capacity)
	if err != nil {
		return nil, nil, err
	}
	attributes := make(map[string]string, len(basePV.Spec.CSI.VolumeAttributes))
	for key, value := range basePV.Spec.CSI.VolumeAttributes {
		attributes[key] = value
	}
	attributes["path"] += "/workspace"
	identity, err := json.Marshal(struct {
		Profile    string
		Namespace  string
		Driver     string
		Attributes map[string]string
	}{workspaceFSXProfileVersion, namespace, personal.Driver, attributes})
	if err != nil {
		return nil, nil, fmt.Errorf("encode workspace mount identity: %w", err)
	}
	digest := sha256.Sum256(identity)
	owner := fmt.Sprintf("workspace-%s-%x", workspaceFSXProfileVersion, digest[:12])
	name := fmt.Sprintf("ray-ws-%s-%x", workspaceFSXProfileVersion, digest[:12])
	labels := func() map[string]string {
		return map[string]string{
			"app.kubernetes.io/part-of":    "ray-train-platform",
			"app.kubernetes.io/managed-by": "ray-train-platform",
			managedDataMountLabel:          owner,
		}
	}
	pv := &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels()},
		Spec: corev1.PersistentVolumeSpec{
			Capacity:                      basePV.Spec.Capacity.DeepCopy(),
			AccessModes:                   []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany},
			PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimRetain,
			MountOptions:                  append(governedFSXMountOptions(false), "compatible_mode=true"),
			ClaimRef:                      &corev1.ObjectReference{APIVersion: "v1", Kind: "PersistentVolumeClaim", Namespace: namespace, Name: name},
			PersistentVolumeSource: corev1.PersistentVolumeSource{CSI: &corev1.CSIPersistentVolumeSource{
				Driver: personal.Driver, VolumeHandle: name, VolumeAttributes: attributes,
			}},
		},
	}
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, Labels: labels()},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany}, VolumeName: name,
			StorageClassName: noStorageClass(),
			Resources:        corev1.VolumeResourceRequirements{Requests: basePV.Spec.Capacity.DeepCopy()},
		},
	}
	return pv, pvc, nil
}

// EnsureWorkspaceMountResources never updates or adopts an existing resource.
// A profile-specific identity keeps recreation independent of workspace IDs
// while current namespace and stable storage home remain part of ownership.
func (c *Client) EnsureWorkspaceMountResources(ctx context.Context, personal domain.DataMountBinding, namespace, capacity string) (string, bool, error) {
	if c == nil || c.kubernetes == nil {
		return "", false, fmt.Errorf("Kubernetes client is not initialized")
	}
	pv, pvc, err := BuildWorkspaceMountResources(personal, namespace, capacity)
	if err != nil {
		return "", false, err
	}
	owner := pv.Labels[managedDataMountLabel]
	volumes := c.kubernetes.CoreV1().PersistentVolumes()
	if err := ensureOwnedPersistentVolume(ctx, volumes, pv, owner); err != nil {
		return "", false, err
	}
	existingPV, err := volumes.Get(ctx, pv.Name, metav1.GetOptions{})
	if err != nil {
		return "", false, fmt.Errorf("get workspace PV %s: %w", pv.Name, err)
	}
	if err := verifyWorkspacePersistentVolume(existingPV, pv); err != nil {
		return "", false, err
	}
	existingPVC, err := ensureOwnedPersistentVolumeClaim(ctx, c.kubernetes.CoreV1().PersistentVolumeClaims(namespace), pvc, owner)
	if err != nil {
		return "", false, err
	}
	if err := verifyWorkspacePersistentVolumeClaim(existingPVC, pvc, existingPV.Spec.ClaimRef); err != nil {
		return "", false, err
	}
	return pvc.Name, existingPVC.Status.Phase == corev1.ClaimBound, nil
}

// The existing shared-root helper retains its historical contract. Workspace
// adapters additionally require the complete static-storage and CSI identity,
// including secret references, to match before reporting a claim as usable.
func verifyWorkspacePersistentVolume(existing, desired *corev1.PersistentVolume) error {
	if err := verifyDataMountOwnership(existing.Labels, desired.Labels[managedDataMountLabel]); err != nil {
		return fmt.Errorf("refusing to use workspace PV %s: %w", desired.Name, err)
	}
	if existing.DeletionTimestamp != nil || !reflect.DeepEqual(existing.Spec.PersistentVolumeSource, desired.Spec.PersistentVolumeSource) ||
		!reflect.DeepEqual(existing.Spec.AccessModes, desired.Spec.AccessModes) ||
		!reflect.DeepEqual(existing.Spec.MountOptions, desired.Spec.MountOptions) ||
		existing.Spec.Capacity.Storage().Cmp(*desired.Spec.Capacity.Storage()) != 0 ||
		existing.Spec.StorageClassName != desired.Spec.StorageClassName ||
		existing.Spec.PersistentVolumeReclaimPolicy != desired.Spec.PersistentVolumeReclaimPolicy ||
		!workspaceFilesystemMode(existing.Spec.VolumeMode) {
		return fmt.Errorf("refusing to use workspace PV %s with a different retained static FSX contract", desired.Name)
	}
	ref := existing.Spec.ClaimRef
	if ref == nil || ref.Name != desired.Spec.ClaimRef.Name || ref.Namespace != desired.Spec.ClaimRef.Namespace {
		return fmt.Errorf("refusing to use workspace PV %s reserved for another claim", desired.Name)
	}
	return nil
}

func verifyWorkspacePersistentVolumeClaim(existing, desired *corev1.PersistentVolumeClaim, volumeClaim *corev1.ObjectReference) error {
	if existing.DeletionTimestamp != nil || existing.Spec.Resources.Requests.Storage().Cmp(*desired.Spec.Resources.Requests.Storage()) != 0 ||
		!workspaceFilesystemMode(existing.Spec.VolumeMode) || existing.Spec.DataSource != nil || existing.Spec.DataSourceRef != nil ||
		(volumeClaim.UID != "" && volumeClaim.UID != existing.UID) {
		return fmt.Errorf("refusing to use workspace PVC %s/%s with a different static binding contract", desired.Namespace, desired.Name)
	}
	return nil
}

func workspaceFilesystemMode(mode *corev1.PersistentVolumeMode) bool {
	return mode == nil || *mode == corev1.PersistentVolumeFilesystem
}

func appendWorkspaceDataMounts(volumeMounts, volumes []any, plan DataMountPlan) ([]any, []any) {
	personalName, volumes := ensurePVCVolume(volumes, "platform-data-personal", plan.Personal.ClaimName, false)
	workspaceName, volumes := ensurePVCVolume(volumes, "platform-data-workspace", plan.Workspace.ClaimName, false)
	volumeMounts = append(volumeMounts,
		pvcMount(personalName, domain.MyStorageMountPath, plan.Personal.SubPath, false),
		pvcMount(workspaceName, domain.WorkspaceMountPath, plan.Workspace.SubPath, false),
		pvcMount(workspaceName, domain.MyStorageMountPath+"/workspace", plan.Workspace.SubPath, false),
	)
	return volumeMounts, volumes
}
