package domain

import (
	"encoding/json"
	"fmt"
	"strings"
)

const FSXCSIDriver = "fsx.csi.volcengine.com"

type DataMountScope string

const (
	DataMountScopePersonal DataMountScope = "personal"
	DataMountScopeTenant   DataMountScope = "tenant"
	DataMountScopeIDC      DataMountScope = "idc"
)

type DataMountBindingStatus string

const (
	DataMountBindingPending DataMountBindingStatus = "PENDING"
	DataMountBindingReady   DataMountBindingStatus = "READY"
	DataMountBindingFailed  DataMountBindingStatus = "FAILED"
)

// DataMountBinding is platform-owned mount metadata. It is intentionally
// separate from a user request: a user may select a DataSpace, but never a
// PVC, CSI driver, TOS root, or credential.
type DataMountBinding struct {
	ID       string `json:"id"`
	TenantID string `json:"tenantId,omitempty"`
	UserID   string `json:"userId,omitempty"`
	// StorageKey is a stable object-root identity. It is deliberately distinct
	// from UserID: the latter is an opaque IdP/local account subject used for
	// ownership checks, while the former may be the administrator-approved
	// username visible in the bucket layout.
	StorageKey           string                 `json:"-"`
	Scope                DataMountScope         `json:"scope"`
	SpaceID              DataSpaceID            `json:"spaceId"`
	ClaimName            string                 `json:"claimName,omitempty"`
	ServiceAccountName   string                 `json:"serviceAccountName,omitempty"`
	Driver               string                 `json:"driver,omitempty"`
	VolumeAttributesJSON string                 `json:"-"`
	RootPrefix           string                 `json:"-"`
	ReadOnly             bool                   `json:"readOnly"`
	Status               DataMountBindingStatus `json:"status"`
	SecretName           string                 `json:"-"`
}

func (binding DataMountBinding) Validate() error {
	if strings.TrimSpace(binding.ID) == "" {
		return fmt.Errorf("data mount binding id is required")
	}
	if strings.TrimSpace(binding.SecretName) != "" {
		return fmt.Errorf("data mount bindings must not use a long-lived secret")
	}
	if err := validateDataMountScope(binding.Scope); err != nil {
		return err
	}
	if err := binding.validateSpace(); err != nil {
		return err
	}
	if err := validateDataMountStatus(binding.Status); err != nil {
		return err
	}
	if err := binding.validateScopeIdentity(); err != nil {
		return err
	}
	if binding.Status != DataMountBindingReady {
		return nil
	}
	if strings.TrimSpace(binding.ClaimName) == "" || !dnsLabel.MatchString(binding.ClaimName) {
		return fmt.Errorf("ready data mount binding requires a DNS-label claim name")
	}
	if binding.Scope == DataMountScopeIDC {
		return binding.validateIDCClaim()
	}
	// FSX uses the csi-fsx component's IRSA role, not a workload identity. A
	// registered workload ServiceAccount is therefore optional and never grants
	// object-storage authority to the Ray Pod.
	if name := strings.TrimSpace(binding.ServiceAccountName); name != "" && !dnsLabel.MatchString(name) {
		return fmt.Errorf("data mount binding service account must be a DNS-label when set")
	}
	if binding.Driver != FSXCSIDriver {
		return fmt.Errorf("ready data mount binding driver must be %q", FSXCSIDriver)
	}
	root, err := normalizeStorageRoot(binding.RootPrefix)
	if err != nil {
		return fmt.Errorf("data mount binding root prefix: %w", err)
	}
	if err := binding.validateExpectedRoot(root); err != nil {
		return err
	}
	if err := validateFSXAttributes(binding.VolumeAttributesJSON, root); err != nil {
		return err
	}
	if binding.Scope == DataMountScopeTenant && binding.SpaceID != DataSpaceTenantStorageRoot {
		if !binding.ReadOnly {
			return fmt.Errorf("%s data mount binding must be read-only", binding.Scope)
		}
	}
	return nil
}

func (binding DataMountBinding) validateIDCClaim() error {
	if !binding.ReadOnly {
		return fmt.Errorf("IDC data mount binding must be read-only")
	}
	if strings.TrimSpace(binding.ServiceAccountName) != "" || strings.TrimSpace(binding.Driver) != "" || strings.TrimSpace(binding.VolumeAttributesJSON) != "" || strings.TrimSpace(binding.RootPrefix) != "" {
		return fmt.Errorf("IDC data mount binding must reference only a registered claim")
	}
	return nil
}

func (binding DataMountBinding) validateSpace() error {
	if binding.SpaceID == "" {
		return fmt.Errorf("data mount binding data space is required")
	}
	if !IsKnownDataSpace(binding.SpaceID) && binding.SpaceID != DataSpaceTenantStorageRoot {
		return fmt.Errorf("data mount binding has unknown data space %q", binding.SpaceID)
	}
	switch binding.Scope {
	case DataMountScopePersonal:
		if binding.SpaceID != DataSpaceWorkspace {
			return fmt.Errorf("personal data mount binding must use %q", DataSpaceWorkspace)
		}
	case DataMountScopeTenant:
		if binding.SpaceID != DataSpaceTeamShared && binding.SpaceID != DataSpacePublic && binding.SpaceID != DataSpaceTenantStorageRoot {
			return fmt.Errorf("tenant data mount binding must use a governed shared or storage-root space")
		}
	case DataMountScopeIDC:
		if binding.SpaceID != DataSpaceIDCOriginal && binding.SpaceID != DataSpaceIDCWellspiking && binding.SpaceID != DataSpaceIDCShared {
			return fmt.Errorf("IDC data mount binding must use an IDC data space")
		}
	}
	return nil
}

func (binding DataMountBinding) validateScopeIdentity() error {
	switch binding.Scope {
	case DataMountScopePersonal:
		if err := validateDataSpaceIdentity("tenant", binding.TenantID); err != nil {
			return err
		}
		if err := validateDataSpaceIdentity("user", binding.UserID); err != nil {
			return err
		}
	case DataMountScopeTenant:
		if err := validateDataSpaceIdentity("tenant", binding.TenantID); err != nil {
			return err
		}
		if binding.UserID != "" {
			return fmt.Errorf("tenant data mount binding must not name a user")
		}
	case DataMountScopeIDC:
		if err := validateDataSpaceIdentity("tenant", binding.TenantID); err != nil {
			return err
		}
	}
	return nil
}

func (binding DataMountBinding) validateExpectedRoot(root string) error {
	switch binding.Scope {
	case DataMountScopePersonal:
		storageKey := binding.StorageKey
		// Existing bindings created before storage_key was introduced retain the
		// old subject root until an explicit, verified migration switches them.
		if storageKey == "" {
			storageKey = binding.UserID
		}
		if err := validateDataSpaceIdentity("storage key", storageKey); err != nil {
			return err
		}
		want := personalDataRoot(binding.TenantID, storageKey)
		if root != want {
			return fmt.Errorf("personal data mount binding root must equal the subject root")
		}
	case DataMountScopeTenant:
		if binding.SpaceID == DataSpaceTenantStorageRoot {
			if root != "ray-train/" {
				return fmt.Errorf("tenant storage root binding must equal the platform TOS root")
			}
			return nil
		}
		want := "ray-train/tenants/" + binding.TenantID + "/shared/"
		if binding.SpaceID == DataSpacePublic {
			if _, err := PublicDataRootForTenant(binding.TenantID, root); err != nil {
				return fmt.Errorf("public data mount binding root: %w", err)
			}
			return nil
		}
		if root != want {
			return fmt.Errorf("shared data mount binding root must equal its governed root")
		}
	}
	return nil
}

func validateDataMountScope(scope DataMountScope) error {
	switch scope {
	case DataMountScopePersonal, DataMountScopeTenant, DataMountScopeIDC:
		return nil
	default:
		return fmt.Errorf("unsupported data mount binding scope %q", scope)
	}
}

func validateDataMountStatus(status DataMountBindingStatus) error {
	switch status {
	case DataMountBindingPending, DataMountBindingReady, DataMountBindingFailed:
		return nil
	default:
		return fmt.Errorf("unsupported data mount binding status %q", status)
	}
}

func validateFSXAttributes(raw, root string) error {
	attributes := map[string]string{}
	if err := json.Unmarshal([]byte(raw), &attributes); err != nil {
		return fmt.Errorf("data mount binding volume attributes must be JSON strings: %w", err)
	}
	if attributes["type"] != "TOS" {
		return fmt.Errorf("data mount binding must use TOS volume attributes")
	}
	if strings.TrimSpace(attributes["bucket"]) == "" || strings.TrimSpace(attributes["path"]) == "" {
		return fmt.Errorf("data mount binding volume attributes require bucket and path")
	}
	if attributes["path"] != "/"+strings.TrimSuffix(root, "/") {
		return fmt.Errorf("data mount binding volume path must equal its governed root")
	}
	for key := range attributes {
		if strings.EqualFold(key, "secretName") || strings.EqualFold(key, "secretNamespace") {
			return fmt.Errorf("data mount binding volume attributes must not include a secret reference")
		}
	}
	return nil
}

// NewPersonalDataMountBinding derives the sole allowed user root from the
// authenticated tenant and subject. Operators may supply only shared FSX
// endpoint attributes; a configured path or Secret reference is rejected
// before a user-specific PV/PVC can be created.
func NewPersonalDataMountBinding(id, tenantID, userID, claimName, fsxAttributes string, storageKeys ...string) (DataMountBinding, error) {
	if err := validateDataSpaceIdentity("tenant", tenantID); err != nil {
		return DataMountBinding{}, err
	}
	if err := validateDataSpaceIdentity("user", userID); err != nil {
		return DataMountBinding{}, err
	}
	storageKey := userID
	if len(storageKeys) > 1 {
		return DataMountBinding{}, fmt.Errorf("only one storage key may be supplied")
	}
	if len(storageKeys) == 1 && strings.TrimSpace(storageKeys[0]) != "" {
		storageKey = strings.TrimSpace(storageKeys[0])
	}
	if err := validateDataSpaceIdentity("storage key", storageKey); err != nil {
		return DataMountBinding{}, err
	}
	if strings.TrimSpace(claimName) == "" || !dnsLabel.MatchString(claimName) {
		return DataMountBinding{}, fmt.Errorf("data mount claim name must be a DNS label")
	}
	attributes := map[string]string{}
	if err := json.Unmarshal([]byte(fsxAttributes), &attributes); err != nil {
		return DataMountBinding{}, fmt.Errorf("FSX volume attributes must be JSON strings: %w", err)
	}
	if attributes["type"] != "TOS" || strings.TrimSpace(attributes["bucket"]) == "" || strings.TrimSpace(attributes["server"]) == "" || strings.TrimSpace(attributes["region"]) == "" {
		return DataMountBinding{}, fmt.Errorf("FSX volume attributes require TOS type, bucket, server, and region")
	}
	for key := range attributes {
		if key == "path" || strings.EqualFold(key, "secretName") || strings.EqualFold(key, "secretNamespace") {
			return DataMountBinding{}, fmt.Errorf("FSX volume attributes must not preconfigure a path or secret reference")
		}
	}
	root := personalDataRoot(tenantID, storageKey)
	attributes["path"] = "/" + strings.TrimSuffix(root, "/")
	canonicalAttributes, err := json.Marshal(attributes)
	if err != nil {
		return DataMountBinding{}, fmt.Errorf("encode FSX volume attributes: %w", err)
	}
	binding := DataMountBinding{
		ID: id, TenantID: tenantID, UserID: userID, StorageKey: storageKey, Scope: DataMountScopePersonal, SpaceID: DataSpaceWorkspace,
		ClaimName: claimName, Driver: FSXCSIDriver, VolumeAttributesJSON: string(canonicalAttributes), RootPrefix: root,
		Status: DataMountBindingPending,
	}
	if err := binding.Validate(); err != nil {
		return DataMountBinding{}, err
	}
	return binding, nil
}

// NewSharedDataMountBinding creates an immutable, read-only mount adapter for
// a tenant-visible shared TOS root. Public data is intentionally mirrored per
// tenant: Kubernetes PVCs are namespaced, so sharing one public claim across
// tenant namespaces would otherwise leave workloads referencing a claim that
// does not exist in their namespace. Both shared kinds therefore use tenant
// scope for the workload binding, while their TOS roots differ.
func NewSharedDataMountBinding(id, tenantID string, spaceID DataSpaceID, claimName, fsxAttributes string) (DataMountBinding, error) {
	if spaceID == DataSpacePublic {
		return NewPublicDataMountBinding(id, tenantID, claimName, fsxAttributes, DefaultPublicDataRoot)
	}
	if err := validateDataSpaceIdentity("tenant", tenantID); err != nil {
		return DataMountBinding{}, err
	}
	if spaceID != DataSpaceTeamShared {
		return DataMountBinding{}, fmt.Errorf("shared data mount space must be team-shared")
	}
	if strings.TrimSpace(claimName) == "" || !dnsLabel.MatchString(claimName) {
		return DataMountBinding{}, fmt.Errorf("data mount claim name must be a DNS label")
	}
	attributes := map[string]string{}
	if err := json.Unmarshal([]byte(fsxAttributes), &attributes); err != nil {
		return DataMountBinding{}, fmt.Errorf("FSX volume attributes must be JSON strings: %w", err)
	}
	if attributes["type"] != "TOS" || strings.TrimSpace(attributes["bucket"]) == "" || strings.TrimSpace(attributes["server"]) == "" || strings.TrimSpace(attributes["region"]) == "" {
		return DataMountBinding{}, fmt.Errorf("FSX volume attributes require TOS type, bucket, server, and region")
	}
	for key := range attributes {
		if key == "path" || strings.EqualFold(key, "secretName") || strings.EqualFold(key, "secretNamespace") {
			return DataMountBinding{}, fmt.Errorf("FSX volume attributes must not preconfigure a path or secret reference")
		}
	}
	root := "ray-train/tenants/" + tenantID + "/shared/"
	attributes["path"] = "/" + strings.TrimSuffix(root, "/")
	canonicalAttributes, err := json.Marshal(attributes)
	if err != nil {
		return DataMountBinding{}, fmt.Errorf("encode FSX volume attributes: %w", err)
	}
	binding := DataMountBinding{
		ID: id, TenantID: tenantID, Scope: DataMountScopeTenant, SpaceID: spaceID,
		ClaimName: claimName, Driver: FSXCSIDriver, VolumeAttributesJSON: string(canonicalAttributes), RootPrefix: root,
		ReadOnly: true, Status: DataMountBindingPending,
	}
	if err := binding.Validate(); err != nil {
		return DataMountBinding{}, err
	}
	return binding, nil
}

// NewPublicDataMountBinding creates the tenant-local PVC adapter for a
// platform-configured public root. It accepts only the canonical shared root
// or the calling tenant's temporary datasets/public migration root.
func NewPublicDataMountBinding(id, tenantID, claimName, fsxAttributes, publicRoot string) (DataMountBinding, error) {
	if err := validateDataSpaceIdentity("tenant", tenantID); err != nil {
		return DataMountBinding{}, err
	}
	if strings.TrimSpace(claimName) == "" || !dnsLabel.MatchString(claimName) {
		return DataMountBinding{}, fmt.Errorf("data mount claim name must be a DNS label")
	}
	root, err := PublicDataRootForTenant(tenantID, publicRoot)
	if err != nil {
		return DataMountBinding{}, err
	}
	attributes := map[string]string{}
	if err := json.Unmarshal([]byte(fsxAttributes), &attributes); err != nil {
		return DataMountBinding{}, fmt.Errorf("FSX volume attributes must be JSON strings: %w", err)
	}
	if attributes["type"] != "TOS" || strings.TrimSpace(attributes["bucket"]) == "" || strings.TrimSpace(attributes["server"]) == "" || strings.TrimSpace(attributes["region"]) == "" {
		return DataMountBinding{}, fmt.Errorf("FSX volume attributes require TOS type, bucket, server, and region")
	}
	for key := range attributes {
		if key == "path" || strings.EqualFold(key, "secretName") || strings.EqualFold(key, "secretNamespace") {
			return DataMountBinding{}, fmt.Errorf("FSX volume attributes must not preconfigure a path or secret reference")
		}
	}
	attributes["path"] = "/" + strings.TrimSuffix(root, "/")
	canonicalAttributes, err := json.Marshal(attributes)
	if err != nil {
		return DataMountBinding{}, fmt.Errorf("encode FSX volume attributes: %w", err)
	}
	binding := DataMountBinding{
		ID: id, TenantID: tenantID, Scope: DataMountScopeTenant, SpaceID: DataSpacePublic,
		ClaimName: claimName, Driver: FSXCSIDriver, VolumeAttributesJSON: string(canonicalAttributes), RootPrefix: root,
		ReadOnly: true, Status: DataMountBindingPending,
	}
	if err := binding.Validate(); err != nil {
		return DataMountBinding{}, err
	}
	return binding, nil
}

// NewTenantRootDataMountBinding builds the single writable FSX PVC used by a
// tenant's workloads. It is an internal adapter, not a user-selectable data
// space: individual workload mounts are confined to known subdirectories
// below this root by the control plane.
func NewTenantRootDataMountBinding(id, tenantID, claimName, fsxAttributes string) (DataMountBinding, error) {
	if err := validateDataSpaceIdentity("tenant", tenantID); err != nil {
		return DataMountBinding{}, err
	}
	if strings.TrimSpace(claimName) == "" || !dnsLabel.MatchString(claimName) {
		return DataMountBinding{}, fmt.Errorf("data mount claim name must be a DNS label")
	}
	attributes := map[string]string{}
	if err := json.Unmarshal([]byte(fsxAttributes), &attributes); err != nil {
		return DataMountBinding{}, fmt.Errorf("FSX volume attributes must be JSON strings: %w", err)
	}
	if attributes["type"] != "TOS" || strings.TrimSpace(attributes["bucket"]) == "" || strings.TrimSpace(attributes["server"]) == "" || strings.TrimSpace(attributes["region"]) == "" {
		return DataMountBinding{}, fmt.Errorf("FSX volume attributes require TOS type, bucket, server, and region")
	}
	for key := range attributes {
		if key == "path" || strings.EqualFold(key, "secretName") || strings.EqualFold(key, "secretNamespace") {
			return DataMountBinding{}, fmt.Errorf("FSX volume attributes must not preconfigure a path or secret reference")
		}
	}
	root := "ray-train/"
	attributes["path"] = "/" + strings.TrimSuffix(root, "/")
	canonicalAttributes, err := json.Marshal(attributes)
	if err != nil {
		return DataMountBinding{}, fmt.Errorf("encode FSX volume attributes: %w", err)
	}
	binding := DataMountBinding{
		ID: id, TenantID: tenantID, Scope: DataMountScopeTenant, SpaceID: DataSpaceTenantStorageRoot,
		ClaimName: claimName, Driver: FSXCSIDriver, VolumeAttributesJSON: string(canonicalAttributes), RootPrefix: root,
		Status: DataMountBindingPending,
	}
	if err := binding.Validate(); err != nil {
		return DataMountBinding{}, err
	}
	return binding, nil
}

// NewIDCDataMountBinding registers one administrator-configured, tenant-local
// NFS claim. The NFS server and export path deliberately do not enter the
// database: they remain deployment configuration and are materialized only by
// the platform controller after this governed binding has been validated.
func NewIDCDataMountBinding(id, tenantID string, spaceID DataSpaceID, claimName string) (DataMountBinding, error) {
	if err := validateDataSpaceIdentity("tenant", tenantID); err != nil {
		return DataMountBinding{}, err
	}
	if spaceID != DataSpaceIDCOriginal && spaceID != DataSpaceIDCWellspiking && spaceID != DataSpaceIDCShared {
		return DataMountBinding{}, fmt.Errorf("IDC data mount space must be an IDC data space")
	}
	if strings.TrimSpace(claimName) == "" || !dnsLabel.MatchString(claimName) {
		return DataMountBinding{}, fmt.Errorf("IDC data mount claim name must be a DNS label")
	}
	binding := DataMountBinding{
		ID: id, TenantID: tenantID, Scope: DataMountScopeIDC, SpaceID: spaceID,
		ClaimName: claimName, ReadOnly: true, Status: DataMountBindingPending,
	}
	if err := binding.Validate(); err != nil {
		return DataMountBinding{}, err
	}
	return binding, nil
}
