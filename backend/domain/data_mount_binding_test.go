package domain

import "testing"

func TestDataMountBindingRejectsLongLivedSecret(t *testing.T) {
	binding := DataMountBinding{
		ID:         "binding-a",
		SpaceID:    DataSpaceWorkspace,
		ClaimName:  "data-a",
		Driver:     FSXCSIDriver,
		SecretName: "tos-fsx-credentials",
	}
	if err := binding.Validate(); err == nil {
		t.Fatal("secret-backed binding was accepted")
	}
}

func TestDataMountBindingRequiresReadyContract(t *testing.T) {
	binding := DataMountBinding{
		ID:                   "binding-a",
		TenantID:             "local",
		UserID:               "kc-7f3a",
		Scope:                DataMountScopePersonal,
		SpaceID:              DataSpaceWorkspace,
		ClaimName:            "data-kc-7f3a",
		ServiceAccountName:   "ray-data-kc-7f3a",
		Driver:               FSXCSIDriver,
		VolumeAttributesJSON: `{"type":"TOS","bucket":"shanghai-data-transfer","path":"/ray-train/tenants/local/users/kc-7f3a"}`,
		RootPrefix:           "ray-train/tenants/local/users/kc-7f3a/",
		Status:               DataMountBindingReady,
	}
	if err := binding.Validate(); err != nil {
		t.Fatalf("valid binding rejected: %v", err)
	}
}

func TestDataMountBindingDoesNotRequireAWorkloadServiceAccountForFSXIRSA(t *testing.T) {
	binding := DataMountBinding{
		ID:                   "binding-a",
		TenantID:             "local",
		UserID:               "kc-7f3a",
		Scope:                DataMountScopePersonal,
		SpaceID:              DataSpaceWorkspace,
		ClaimName:            "data-kc-7f3a",
		Driver:               FSXCSIDriver,
		VolumeAttributesJSON: `{"type":"TOS","bucket":"shanghai-data-transfer","path":"/ray-train/tenants/local/users/kc-7f3a"}`,
		RootPrefix:           "ray-train/tenants/local/users/kc-7f3a/",
		Status:               DataMountBindingReady,
	}
	if err := binding.Validate(); err != nil {
		t.Fatalf("component-level FSX IRSA must not require a workload service account: %v", err)
	}
}

func TestDataMountBindingRejectsMismatchedPersonalRoot(t *testing.T) {
	binding := DataMountBinding{
		ID:                   "binding-a",
		TenantID:             "local",
		UserID:               "kc-7f3a",
		Scope:                DataMountScopePersonal,
		SpaceID:              DataSpaceWorkspace,
		ClaimName:            "data-kc-7f3a",
		ServiceAccountName:   "ray-data-kc-7f3a",
		Driver:               FSXCSIDriver,
		VolumeAttributesJSON: `{"type":"TOS"}`,
		RootPrefix:           "ray-train/tenants/local/users/another-user/",
		Status:               DataMountBindingReady,
	}
	if err := binding.Validate(); err == nil {
		t.Fatal("binding with another user's root was accepted")
	}
}

func TestDataMountBindingAcceptsRegisteredReadOnlyIDCClaim(t *testing.T) {
	binding := DataMountBinding{
		ID:        "idc-original",
		TenantID:  "local",
		Scope:     DataMountScopeIDC,
		SpaceID:   DataSpaceIDCOriginal,
		ClaimName: "idc-original-ro",
		ReadOnly:  true,
		Status:    DataMountBindingReady,
	}
	if err := binding.Validate(); err != nil {
		t.Fatalf("registered IDC claim rejected: %v", err)
	}
}

func TestNewIDCDataMountBindingAcceptsOnlyGovernedIDCSpaces(t *testing.T) {
	binding, err := NewIDCDataMountBinding("idc-original-a", "local", DataSpaceIDCOriginal, "idc-original-local")
	if err != nil {
		t.Fatalf("new IDC binding: %v", err)
	}
	if binding.Scope != DataMountScopeIDC || !binding.ReadOnly || binding.Status != DataMountBindingPending || binding.ClaimName != "idc-original-local" {
		t.Fatalf("unexpected IDC binding: %#v", binding)
	}
	if _, err := NewIDCDataMountBinding("bad", "local", DataSpaceMyFiles, "idc-files-local"); err == nil {
		t.Fatal("non-IDC logical space was accepted as an IDC mount")
	}
}
