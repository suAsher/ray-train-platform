package domain

import (
	"fmt"
	"regexp"
	"strings"
)

// DataSpaceID is a stable, user-facing identifier for a governed data root.
// The implementation root stays server-side and is never serialised to the
// browser.
type DataSpaceID string

// MaxDataSpaceUploadBytes is retained as the bound for the legacy single HTTP
// request. Larger files use resumable multipart uploads; it is not a product
// level whole-file limit.
const MaxDataSpaceUploadBytes int64 = DataSpaceMultipartThresholdBytes

const (
	DataSpaceWorkspace      DataSpaceID = "workspace"
	DataSpaceMyStorage      DataSpaceID = "my-storage"
	DataSpaceMyFiles        DataSpaceID = "my-files"
	DataSpaceMyRuns         DataSpaceID = "my-runs"
	DataSpaceTeamShared     DataSpaceID = "team-shared"
	DataSpacePublic         DataSpaceID = "public"
	DataSpaceIDCOriginal    DataSpaceID = "idc-original"
	DataSpaceIDCWellspiking DataSpaceID = "idc-wellspiking"
	DataSpaceIDCShared      DataSpaceID = "idc-shared"
	DataSpaceIDCSPKHybrid   DataSpaceID = "idc-spk-hybrid"
	DataSpaceIDCSPKSSD      DataSpaceID = "idc-spk-ssd"
	// DataSpaceTenantStorageRoot is a control-plane-only adapter. It is never
	// returned in the data-space catalogue and cannot be selected in a request.
	// A single PVC rooted at ray-train/ is staged once and user-visible paths
	// are then mounted through server-resolved Kubernetes subPaths.
	DataSpaceTenantStorageRoot DataSpaceID = "tenant-storage-root"
)

const (
	WorkspaceMountPath      = "/workspace"
	MyStorageMountPath      = "/mnt/storage/me"
	MyFilesMountPath        = MyStorageMountPath + "/files"
	TeamStorageMountPath    = "/mnt/storage/team"
	PublicStorageMountPath  = "/mnt/storage/public"
	IDCOriginalMountPath    = "/mnt/.original"
	IDCWellspikingMountPath = "/mnt/.wellspiking"
	IDCSharedMountPath      = "/mnt/.shared"
	IDCSPKHybridMountPath   = "/mnt/.spk-hybrid"
	IDCSPKSSDMountPath      = "/mnt/.spk-ssd"
	// DefaultPublicDataRoot is the stable production location for data made
	// available to every tenant. A tenant-confined temporary root may be used
	// only during an explicitly configured migration; see NormalizePublicDataRoot.
	DefaultPublicDataRoot = "ray-train/public/"
)

// DataSpace is a logical user-visible storage root. RootPrefix is an
// implementation detail used by the backend only; returning it would expose
// bucket layout to callers and make authorization bugs easier to exploit.
type DataSpace struct {
	ID            DataSpaceID `json:"id"`
	Name          string      `json:"name"`
	Description   string      `json:"description"`
	Provider      string      `json:"provider"`
	MountPath     string      `json:"mountPath"`
	ReadOnly      bool        `json:"readOnly"`
	BrowseEnabled bool        `json:"browseEnabled"`
	RootPrefix    string      `json:"-"`
}

var dataSpaceIdentity = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// PersonalDataSpaces returns the standard catalogue for a trusted identity.
// API boundaries should use PersonalDataSpacesFor so invalid identities are
// rejected rather than silently mapped to an unexpected TOS prefix.
func PersonalDataSpaces(tenantID, subject string) []DataSpace {
	spaces, err := PersonalDataSpacesFor(tenantID, subject)
	if err != nil {
		return nil
	}
	return spaces
}

func PersonalDataSpacesFor(tenantID, subject string) ([]DataSpace, error) {
	if err := validateDataSpaceIdentity("tenant", tenantID); err != nil {
		return nil, err
	}
	if err := validateDataSpaceIdentity("subject", subject); err != nil {
		return nil, err
	}
	return personalDataSpacesForRoot(tenantID, personalDataRoot(tenantID, subject), DefaultPublicDataRoot)
}

// PersonalDataSpacesForRoot maps a previously platform-created personal root
// back to the fixed logical space catalogue. The caller receives the root only
// from a trusted data-mount binding; no HTTP request may supply it.
func PersonalDataSpacesForRoot(tenantID, personalRoot string) ([]DataSpace, error) {
	return PersonalDataSpacesForRoots(tenantID, personalRoot, DefaultPublicDataRoot)
}

// PersonalDataSpacesForRoots builds the user-visible catalogue from two
// platform-controlled roots. publicRoot is deliberately not supplied by an
// HTTP request: it comes only from the deployment's governed data-space
// configuration. This keeps a temporary migration root tenant-confined.
func PersonalDataSpacesForRoots(tenantID, personalRoot, publicRoot string) ([]DataSpace, error) {
	if err := validateDataSpaceIdentity("tenant", tenantID); err != nil {
		return nil, err
	}
	prefix := "ray-train/tenants/" + tenantID + "/users/"
	if !strings.HasPrefix(personalRoot, prefix) || !strings.HasSuffix(personalRoot, "/") {
		return nil, fmt.Errorf("personal data root is outside the tenant")
	}
	storageKey := strings.TrimSuffix(strings.TrimPrefix(personalRoot, prefix), "/")
	if err := validateDataSpaceIdentity("storage key", storageKey); err != nil || personalRoot != personalDataRoot(tenantID, storageKey) {
		return nil, fmt.Errorf("personal data root is invalid")
	}
	normalizedPublicRoot, err := PublicDataRootForTenant(tenantID, publicRoot)
	if err != nil {
		return nil, err
	}
	return personalDataSpacesForRoot(tenantID, personalRoot, normalizedPublicRoot)
}

func personalDataSpacesForRoot(tenantID, personalRoot, publicRoot string) ([]DataSpace, error) {
	spaces := []DataSpace{
		{
			ID: DataSpaceWorkspace, Name: "我的工作区", Description: "调试代码与训练快照的来源",
			Provider: StorageProviderTOS, MountPath: WorkspaceMountPath, BrowseEnabled: true,
			RootPrefix: personalRoot + "workspace/",
		},
		{
			ID: DataSpaceMyStorage, Name: "我的文件", Description: "个人持久化数据、训练结果与 Checkpoint",
			Provider: StorageProviderTOS, MountPath: MyStorageMountPath, BrowseEnabled: true,
			RootPrefix: personalRoot,
		},
		{
			ID: DataSpaceMyFiles, Name: "我的文件", Description: "个人上传的数据和文件",
			Provider: StorageProviderTOS, MountPath: MyFilesMountPath, BrowseEnabled: true,
			RootPrefix: personalRoot + "files/",
		},
		{
			ID: DataSpaceMyRuns, Name: "我的运行结果", Description: "训练输出、检查点与可浏览产物",
			Provider: StorageProviderTOS, MountPath: MyStorageMountPath + "/runs", BrowseEnabled: true,
			RootPrefix: personalRoot + "runs/",
		},
		{
			ID: DataSpaceTeamShared, Name: "团队共享数据", Description: "租户内已发布的数据，只读",
			Provider: StorageProviderTOS, MountPath: TeamStorageMountPath, ReadOnly: true, BrowseEnabled: true,
			RootPrefix: "ray-train/tenants/" + tenantID + "/shared/",
		},
		{
			ID: DataSpacePublic, Name: "公共数据", Description: "平台发布的公共数据，只读",
			Provider: StorageProviderTOS, MountPath: PublicStorageMountPath, ReadOnly: true, BrowseEnabled: true,
			RootPrefix: publicRoot,
		},
		{
			ID: DataSpaceIDCOriginal, Name: "IDC 原始数据", Description: "IDC 只读原始数据",
			Provider: StorageProviderIDC, MountPath: IDCOriginalMountPath, ReadOnly: true,
		},
		{
			ID: DataSpaceIDCWellspiking, Name: "IDC Wellspiking 数据", Description: "IDC 只读 Wellspiking 数据",
			Provider: StorageProviderIDC, MountPath: IDCWellspikingMountPath, ReadOnly: true,
		},
		{
			ID: DataSpaceIDCShared, Name: "IDC 共享数据", Description: "IDC 只读共享数据",
			Provider: StorageProviderIDC, MountPath: IDCSharedMountPath, ReadOnly: true,
		},
		{
			ID: DataSpaceIDCSPKHybrid, Name: "IDC SPK Hybrid 数据", Description: "IDC 只读混合存储数据",
			Provider: StorageProviderIDC, MountPath: IDCSPKHybridMountPath, ReadOnly: true,
		},
		{
			ID: DataSpaceIDCSPKSSD, Name: "IDC SPK SSD 数据", Description: "IDC 只读 SSD 存储数据",
			Provider: StorageProviderIDC, MountPath: IDCSPKSSDMountPath, ReadOnly: true,
		},
	}
	return spaces, nil
}

// NormalizePublicDataRoot accepts only the standard shared public root or a
// tenant-local temporary dataset root. The latter is intentionally narrow:
// it cannot point at user workspaces, team roots, or another arbitrary TOS
// prefix while a dataset migration is in progress.
func NormalizePublicDataRoot(raw string) (string, error) {
	root, err := normalizeStorageRoot(raw)
	if err != nil {
		return "", fmt.Errorf("public data root: %w", err)
	}
	if root == DefaultPublicDataRoot {
		return root, nil
	}
	segments := strings.Split(strings.TrimSuffix(root, "/"), "/")
	if len(segments) != 5 || segments[0] != "ray-train" || segments[1] != "tenants" || segments[3] != "datasets" || segments[4] != "public" {
		return "", fmt.Errorf("public data root must be %q or a tenant datasets/public root", DefaultPublicDataRoot)
	}
	if err := validateDataSpaceIdentity("public data tenant", segments[2]); err != nil {
		return "", err
	}
	return root, nil
}

// PublicDataRootForTenant verifies that a configured temporary root belongs
// to the authenticated tenant. The global standard root remains available to
// every tenant.
func PublicDataRootForTenant(tenantID, raw string) (string, error) {
	if err := validateDataSpaceIdentity("tenant", tenantID); err != nil {
		return "", err
	}
	root, err := NormalizePublicDataRoot(raw)
	if err != nil {
		return "", err
	}
	if root == DefaultPublicDataRoot {
		return root, nil
	}
	segments := strings.Split(strings.TrimSuffix(root, "/"), "/")
	if segments[2] != tenantID {
		return "", fmt.Errorf("temporary public data root must belong to the tenant")
	}
	return root, nil
}

func FindDataSpace(spaces []DataSpace, id DataSpaceID) (DataSpace, bool) {
	for _, space := range spaces {
		if space.ID == id {
			return space, true
		}
	}
	return DataSpace{}, false
}

func IsKnownDataSpace(id DataSpaceID) bool {
	for _, candidate := range []DataSpaceID{
		DataSpaceWorkspace, DataSpaceMyStorage, DataSpaceMyFiles, DataSpaceMyRuns, DataSpaceTeamShared, DataSpacePublic,
		DataSpaceIDCOriginal, DataSpaceIDCWellspiking, DataSpaceIDCShared, DataSpaceIDCSPKHybrid, DataSpaceIDCSPKSSD,
	} {
		if id == candidate {
			return true
		}
	}
	return false
}

func IsWritableDataSpace(id DataSpaceID) bool {
	switch id {
	case DataSpaceWorkspace, DataSpaceMyStorage, DataSpaceMyFiles, DataSpaceMyRuns:
		return true
	default:
		return false
	}
}

// CanManageDataSpace separates an administrator's publishing privilege from
// a workload's mount privilege. Shared roots are always read-only inside Ray
// Pods, while the responsible administrator may populate them through the
// authenticated Portal API.
func CanManageDataSpace(id DataSpaceID, tenantAdmin, superAdmin bool) bool {
	if IsWritableDataSpace(id) {
		return true
	}
	if id == DataSpaceTeamShared {
		return tenantAdmin || superAdmin
	}
	return id == DataSpacePublic && superAdmin
}

// IsTrainingOutputSpace is deliberately narrower than IsWritableDataSpace.
// Workspaces and personal files are writable during development, but a RayJob
// must write results beneath the managed runs root so artifacts can be
// isolated, browsed, and cleaned up predictably.
func IsTrainingOutputSpace(id DataSpaceID) bool {
	return id == DataSpaceMyRuns
}

// PersonalDataRootFor derives the single root that a platform identity owns
// in the object store. Callers use this only for backend-controlled setup;
// HTTP responses must continue exposing logical data spaces instead.
func PersonalDataRootFor(tenantID, subject string) (string, error) {
	if err := validateDataSpaceIdentity("tenant", tenantID); err != nil {
		return "", err
	}
	if err := validateDataSpaceIdentity("subject", subject); err != nil {
		return "", err
	}
	return personalDataRoot(tenantID, subject), nil
}

func personalDataRoot(tenantID, subject string) string {
	return "ray-train/tenants/" + tenantID + "/users/" + subject + "/"
}

func validateDataSpaceIdentity(field, value string) error {
	if strings.TrimSpace(value) != value || !dataSpaceIdentity.MatchString(value) {
		return fmt.Errorf("%s must be a stable path-safe identifier", field)
	}
	return nil
}
