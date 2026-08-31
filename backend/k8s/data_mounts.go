package k8s

import (
	"encoding/json"
	"fmt"
	"path"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"ray-train-platform-backend/domain"
)

// DataMountPlan contains only platform-resolved claims. It intentionally has
// no object-store credential, bucket or PV fields: CSI mounting is a node-side
// concern and user workloads must never need those implementation details.
type DataMountPlan struct {
	Personal       *DataMountRoot
	Team           *DataMountRoot
	Public         *DataMountRoot
	IDCOriginal    *DataMountRoot
	IDCWellspiking *DataMountRoot
	IDCShared      *DataMountRoot
	IDCSPKHybrid   *DataMountRoot
	IDCSPKSSD      *DataMountRoot
}

type DataMountRoot struct {
	ClaimName string
	// SubPath is an internal, platform-resolved directory below ClaimName. It
	// is never populated from an API request.  A tenant-root claim can therefore
	// be staged once by the node while each container only sees its own governed
	// directory.
	SubPath  string
	ReadOnly bool
}

// IDCDataMountSource is deployment-owned NFS configuration. It is accepted
// only from the Helm/environment configuration and never from an API request
// or a data-mount binding, which prevents a tenant from choosing an arbitrary
// internal NFS export.
type IDCDataMountSource struct {
	// ID and presentation fields are deployment-owned metadata. They are never
	// accepted from an end-user request; the renderer only receives claims that
	// were created for one of these registered sources.
	ID           domain.DataSpaceID
	Name         string
	Description  string
	MountPath    string
	Server       string
	Path         string
	MountOptions []string
}

const managedDataMountLabel = "ray-train-platform/data-mount-id"

// governedFSXMountOptions keeps the FUSE ownership contract aligned with the
// non-root Ray workspace image. They are deliberately platform constants:
// users must not be able to add arbitrary CSI/FUSE flags through an API or a
// data-space binding. Read-only spaces additionally never receive the delete
// capability, even though their Pod volume mount is read-only as well.
func governedFSXMountOptions(readOnly bool) []string {
	options := []string{
		"no_writeback_cache",
		"uid=1000",
		"gid=1000",
		"file_mode=770",
		"dir_mode=770",
	}
	if !readOnly {
		options = append(options, "tos_allow_delete=true")
	}
	return options
}

// BuildDataMountResources translates one approved FSX binding to a static PV
// and namespaced PVC. It deliberately omits every Secret reference so the
// csi-fsx component's IRSA identity is the only object-store credential path.
func BuildDataMountResources(binding domain.DataMountBinding, namespace, size string) (*corev1.PersistentVolume, *corev1.PersistentVolumeClaim, error) {
	if binding.Scope == domain.DataMountScopeIDC {
		return nil, nil, fmt.Errorf("IDC data mounts use administrator-registered claims and cannot create an FSX resource")
	}
	if binding.Status != domain.DataMountBindingPending && binding.Status != domain.DataMountBindingReady {
		return nil, nil, fmt.Errorf("data mount binding must be PENDING or READY before creating a PVC")
	}
	provisionable := binding
	provisionable.Status = domain.DataMountBindingReady
	if err := provisionable.Validate(); err != nil {
		return nil, nil, fmt.Errorf("validate data mount binding: %w", err)
	}
	if !isDNSLabel(namespace) {
		return nil, nil, fmt.Errorf("data mount namespace must be a DNS label")
	}
	capacity, err := resource.ParseQuantity(strings.TrimSpace(size))
	if err != nil || capacity.Sign() <= 0 {
		return nil, nil, fmt.Errorf("data mount capacity must be a positive Kubernetes quantity")
	}
	attributes := map[string]string{}
	if err := json.Unmarshal([]byte(binding.VolumeAttributesJSON), &attributes); err != nil {
		return nil, nil, fmt.Errorf("decode FSX data mount attributes: %w", err)
	}
	for key := range attributes {
		if strings.EqualFold(key, "secretName") || strings.EqualFold(key, "secretNamespace") {
			return nil, nil, fmt.Errorf("governed FSX data mount attributes must not reference a secret")
		}
	}
	volumeName := "ray-data-" + binding.ID
	if len(volumeName) > 63 {
		return nil, nil, fmt.Errorf("data mount binding id is too long for a volume name")
	}
	labels := map[string]string{
		"app.kubernetes.io/part-of":    "ray-train-platform",
		"app.kubernetes.io/managed-by": "ray-train-platform",
		managedDataMountLabel:          binding.ID,
	}
	pv := &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{Name: volumeName, Labels: labels},
		Spec: corev1.PersistentVolumeSpec{
			Capacity:                      corev1.ResourceList{corev1.ResourceStorage: capacity},
			AccessModes:                   []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany},
			MountOptions:                  governedFSXMountOptions(binding.ReadOnly),
			PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimRetain,
			PersistentVolumeSource: corev1.PersistentVolumeSource{CSI: &corev1.CSIPersistentVolumeSource{
				Driver: binding.Driver, VolumeHandle: volumeName, VolumeAttributes: attributes,
			}},
		},
	}
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: binding.ClaimName, Namespace: namespace, Labels: labels},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany}, VolumeName: volumeName,
			StorageClassName: noStorageClass(),
			Resources:        corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: capacity}},
		},
	}
	return pv, pvc, nil
}

// BuildIDCDataMountResources translates an administrator-configured NFS
// source into a tenant-local, retained ReadOnlyMany PV/PVC pair. The object
// name is derived from the immutable binding, while the NFS source is verified
// again before an existing PV is used by the client helper.
func BuildIDCDataMountResources(binding domain.DataMountBinding, namespace, size string, source IDCDataMountSource) (*corev1.PersistentVolume, *corev1.PersistentVolumeClaim, error) {
	if binding.Scope != domain.DataMountScopeIDC {
		return nil, nil, fmt.Errorf("IDC data mount resource requires an IDC binding")
	}
	if binding.Status != domain.DataMountBindingPending && binding.Status != domain.DataMountBindingReady {
		return nil, nil, fmt.Errorf("IDC data mount binding must be PENDING or READY before creating a PVC")
	}
	provisionable := binding
	provisionable.Status = domain.DataMountBindingReady
	if err := provisionable.Validate(); err != nil {
		return nil, nil, fmt.Errorf("validate IDC data mount binding: %w", err)
	}
	if !isDNSLabel(namespace) {
		return nil, nil, fmt.Errorf("IDC data mount namespace must be a DNS label")
	}
	if strings.TrimSpace(source.Server) == "" || strings.ContainsAny(source.Server, " \t/\\") {
		return nil, nil, fmt.Errorf("IDC data mount NFS server must be a bare hostname or IP address")
	}
	if !strings.HasPrefix(source.Path, "/") || source.Path == "/" || strings.TrimSpace(source.Path) != source.Path || path.Clean(source.Path) != source.Path {
		return nil, nil, fmt.Errorf("IDC data mount NFS path must be a clean non-root absolute export path")
	}
	if err := validateIDCNFSMountOptions(source.MountOptions); err != nil {
		return nil, nil, err
	}
	capacity, err := resource.ParseQuantity(strings.TrimSpace(size))
	if err != nil || capacity.Sign() <= 0 {
		return nil, nil, fmt.Errorf("IDC data mount capacity must be a positive Kubernetes quantity")
	}
	volumeName := "ray-idc-" + binding.ID
	if len(volumeName) > 63 {
		return nil, nil, fmt.Errorf("IDC data mount binding id is too long for a volume name")
	}
	labels := map[string]string{
		"app.kubernetes.io/part-of":    "ray-train-platform",
		"app.kubernetes.io/managed-by": "ray-train-platform",
		managedDataMountLabel:          binding.ID,
	}
	pv := &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{Name: volumeName, Labels: labels},
		Spec: corev1.PersistentVolumeSpec{
			Capacity:                      corev1.ResourceList{corev1.ResourceStorage: capacity},
			AccessModes:                   []corev1.PersistentVolumeAccessMode{corev1.ReadOnlyMany},
			MountOptions:                  append([]string(nil), source.MountOptions...),
			PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimRetain,
			PersistentVolumeSource:        corev1.PersistentVolumeSource{NFS: &corev1.NFSVolumeSource{Server: source.Server, Path: source.Path, ReadOnly: true}},
		},
	}
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: binding.ClaimName, Namespace: namespace, Labels: labels},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadOnlyMany}, VolumeName: volumeName,
			StorageClassName: noStorageClass(),
			Resources:        corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: capacity}},
		},
	}
	return pv, pvc, nil
}

// validateIDCNFSMountOptions deliberately accepts only the read-only NFS
// tuning flags that the platform supports. This keeps a deployment profile
// from accidentally turning a governed IDC source into a writable or
// executable host escape route.
func validateIDCNFSMountOptions(options []string) error {
	allowed := map[string]bool{
		"ro": true, "hard": true, "noatime": true, "_netdev": true, "nofail": true,
		"vers=3": true, "timeo=600": true, "retrans=2": true,
		"rsize=1048576": true, "wsize=1048576": true,
	}
	seenRO := false
	for _, raw := range options {
		option := strings.TrimSpace(raw)
		if option == "" || option != raw || !allowed[option] {
			return fmt.Errorf("IDC data mount option %q is not in the read-only allowlist", raw)
		}
		if option == "ro" {
			seenRO = true
		}
	}
	if len(options) > 0 && !seenRO {
		return fmt.Errorf("IDC data mount options must include ro")
	}
	return nil
}

func noStorageClass() *string {
	value := ""
	return &value
}

func (plan DataMountPlan) Validate() error {
	if plan.Personal != nil {
		if err := plan.Personal.validate("personal", false); err != nil {
			return err
		}
	}
	for _, root := range []struct {
		name string
		root *DataMountRoot
	}{
		{name: "team", root: plan.Team},
		{name: "public", root: plan.Public},
		{name: "IDC original", root: plan.IDCOriginal},
		{name: "IDC Wellspiking", root: plan.IDCWellspiking},
		{name: "IDC shared", root: plan.IDCShared},
		{name: "IDC SPK Hybrid", root: plan.IDCSPKHybrid},
		{name: "IDC SPK SSD", root: plan.IDCSPKSSD},
	} {
		if root.root != nil {
			if err := root.root.validate(root.name, true); err != nil {
				return err
			}
		}
	}
	return nil
}

func (plan DataMountPlan) hasGovernedIDC() bool {
	return plan.IDCOriginal != nil || plan.IDCWellspiking != nil || plan.IDCShared != nil || plan.IDCSPKHybrid != nil || plan.IDCSPKSSD != nil
}

func (root DataMountRoot) validate(name string, readOnly bool) error {
	if strings.TrimSpace(root.ClaimName) == "" || !isDNSLabel(root.ClaimName) {
		return fmt.Errorf("%s data mount requires a DNS-label claim name", name)
	}
	if root.ReadOnly != readOnly {
		return fmt.Errorf("%s data mount read-only contract is invalid", name)
	}
	if root.SubPath != "" {
		normalized, err := domain.NormalizeStorageRelativePath(root.SubPath)
		if err != nil || normalized != root.SubPath {
			return fmt.Errorf("%s data mount has an invalid confined subpath", name)
		}
	}
	return nil
}

func appendDataMountPlan(volumeMounts, volumes []any, plan DataMountPlan) ([]any, []any) {
	if plan.Personal != nil {
		if plan.Personal.SubPath == "" {
			// Legacy per-personal-root claims keep their historical render shape so
			// previously persisted jobs and their manifest tests remain compatible.
			volumeMounts = append(volumeMounts,
				map[string]any{"name": "platform-data-personal-workspace", "mountPath": domain.WorkspaceMountPath, "subPath": "workspace", "readOnly": false},
				map[string]any{"name": "platform-data-personal", "mountPath": domain.MyStorageMountPath, "readOnly": false},
			)
			volumes = append(volumes,
				pvcVolume("platform-data-personal-workspace", plan.Personal.ClaimName, false),
				pvcVolume("platform-data-personal", plan.Personal.ClaimName, false),
			)
		} else {
			volumeName, updatedVolumes := ensurePVCVolume(volumes, "platform-data-personal", plan.Personal.ClaimName, false)
			volumes = updatedVolumes
			volumeMounts = append(volumeMounts,
				pvcMount(volumeName, domain.WorkspaceMountPath, joinRootSubPath(plan.Personal.SubPath, "workspace"), false),
				pvcMount(volumeName, domain.MyStorageMountPath, plan.Personal.SubPath, false),
			)
		}
	}
	for _, root := range []struct {
		volumeName string
		mountPath  string
		root       *DataMountRoot
	}{
		{volumeName: "platform-data-team", mountPath: domain.TeamStorageMountPath, root: plan.Team},
		{volumeName: "platform-data-public", mountPath: domain.PublicStorageMountPath, root: plan.Public},
		{volumeName: "platform-data-idc-original", mountPath: domain.IDCOriginalMountPath, root: plan.IDCOriginal},
		{volumeName: "platform-data-idc-wellspiking", mountPath: domain.IDCWellspikingMountPath, root: plan.IDCWellspiking},
		{volumeName: "platform-data-idc-shared", mountPath: domain.IDCSharedMountPath, root: plan.IDCShared},
		{volumeName: "platform-data-idc-spk-hybrid", mountPath: domain.IDCSPKHybridMountPath, root: plan.IDCSPKHybrid},
		{volumeName: "platform-data-idc-spk-ssd", mountPath: domain.IDCSPKSSDMountPath, root: plan.IDCSPKSSD},
	} {
		if root.root == nil {
			continue
		}
		volumeName := root.volumeName
		if root.root.SubPath != "" {
			volumeName, volumes = ensurePVCVolume(volumes, root.volumeName, root.root.ClaimName, root.root.ReadOnly)
		} else {
			volumes = append(volumes, pvcVolume(volumeName, root.root.ClaimName, true))
		}
		volumeMounts = append(volumeMounts, pvcMount(volumeName, root.mountPath, root.root.SubPath, true))
	}
	return volumeMounts, volumes
}

func dataMountPlanFromResolvedRoots(roots domain.ResolvedDataSpaceRoots) DataMountPlan {
	root := func(value *domain.ResolvedDataRoot) *DataMountRoot {
		if value == nil {
			return nil
		}
		return &DataMountRoot{ClaimName: value.ClaimName, SubPath: value.SubPath, ReadOnly: value.ReadOnly}
	}
	return DataMountPlan{
		Personal: root(roots.Personal), Team: root(roots.Team), Public: root(roots.Public),
		IDCOriginal: root(roots.IDCOriginal), IDCWellspiking: root(roots.IDCWellspiking), IDCShared: root(roots.IDCShared), IDCSPKHybrid: root(roots.IDCSPKHybrid), IDCSPKSSD: root(roots.IDCSPKSSD),
	}
}

// appendTrainingDataRoots mounts the persistent user-visible roots without
// mounting the personal workspace at /workspace. Training code is first
// materialized into an isolated emptyDir; replacing it with the editable
// development workspace would make a submitted job non-reproducible.
func appendTrainingDataRoots(volumeMounts, volumes []any, roots domain.ResolvedDataSpaceRoots) ([]any, []any) {
	plan := dataMountPlanFromResolvedRoots(roots)
	if plan.Personal != nil {
		volumeName := "platform-data-personal"
		if plan.Personal.SubPath != "" {
			volumeName, volumes = ensurePVCVolume(volumes, volumeName, plan.Personal.ClaimName, false)
		} else {
			volumes = append(volumes, pvcVolume(volumeName, plan.Personal.ClaimName, false))
		}
		volumeMounts = append(volumeMounts, pvcMount(volumeName, domain.MyStorageMountPath, plan.Personal.SubPath, false))
	}
	for _, root := range []struct {
		volumeName string
		mountPath  string
		root       *DataMountRoot
	}{
		{volumeName: "platform-data-team", mountPath: domain.TeamStorageMountPath, root: plan.Team},
		{volumeName: "platform-data-public", mountPath: domain.PublicStorageMountPath, root: plan.Public},
		{volumeName: "platform-data-idc-original", mountPath: domain.IDCOriginalMountPath, root: plan.IDCOriginal},
		{volumeName: "platform-data-idc-wellspiking", mountPath: domain.IDCWellspikingMountPath, root: plan.IDCWellspiking},
		{volumeName: "platform-data-idc-shared", mountPath: domain.IDCSharedMountPath, root: plan.IDCShared},
		{volumeName: "platform-data-idc-spk-hybrid", mountPath: domain.IDCSPKHybridMountPath, root: plan.IDCSPKHybrid},
		{volumeName: "platform-data-idc-spk-ssd", mountPath: domain.IDCSPKSSDMountPath, root: plan.IDCSPKSSD},
	} {
		if root.root == nil {
			continue
		}
		volumeName := root.volumeName
		if root.root.SubPath != "" {
			volumeName, volumes = ensurePVCVolume(volumes, volumeName, root.root.ClaimName, root.root.ReadOnly)
		} else {
			volumes = append(volumes, pvcVolume(volumeName, root.root.ClaimName, true))
		}
		volumeMounts = append(volumeMounts, pvcMount(volumeName, root.mountPath, root.root.SubPath, true))
	}
	return volumeMounts, volumes
}

func joinRootSubPath(root, child string) string {
	if root == "" {
		return child
	}
	return root + "/" + child
}

func pvcMount(volumeName, mountPath, subPath string, readOnly bool) map[string]any {
	mount := map[string]any{"name": volumeName, "mountPath": mountPath, "readOnly": readOnly}
	if subPath != "" {
		mount["subPath"] = subPath
	}
	return mount
}

// ensurePVCVolume reuses an existing PVC source when several safe subpaths
// share a tenant root.  The PVC itself stays writable when any mount needs to
// write; read-only user-visible paths are enforced at the container mount.
func ensurePVCVolume(volumes []any, preferredName, claimName string, readOnly bool) (string, []any) {
	if name := pvcVolumeName(volumes, claimName); name != "" {
		return name, volumes
	}
	return preferredName, append(volumes, pvcVolume(preferredName, claimName, readOnly))
}

func pvcVolumeName(volumes []any, claimName string) string {
	for _, value := range volumes {
		volume, ok := value.(map[string]any)
		if !ok {
			continue
		}
		claim, ok := volume["persistentVolumeClaim"].(map[string]any)
		if ok && claim["claimName"] == claimName {
			name, _ := volume["name"].(string)
			if name != "" {
				return name
			}
		}
	}
	return ""
}

func pvcVolume(name, claimName string, readOnly bool) map[string]any {
	return map[string]any{"name": name, "persistentVolumeClaim": map[string]any{"claimName": claimName, "readOnly": readOnly}}
}
