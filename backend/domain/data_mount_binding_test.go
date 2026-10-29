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

func TestDataMountBindingRejectsRetiredIDCClaim(t *testing.T) {
	binding := DataMountBinding{
		ID:        "idc-original",
		TenantID:  "local",
		Scope:     DataMountScopeIDC,
		SpaceID:   DataSpaceIDCOriginal,
		ClaimName: "idc-original-ro",
		ReadOnly:  true,
		Status:    DataMountBindingReady,
	}
	if err := binding.Validate(); err == nil {
		t.Fatal("retired IDC claim was accepted")
	}
}


func TestNewIDCDataMountBindingCannotRestoreRetiredSpaces(t *testing.T) {
	for _, space := range []DataSpaceID{DataSpaceIDCOriginal, DataSpaceIDCWellspiking, DataSpaceIDCShared, DataSpaceMyFiles} {
		if _, err := NewIDCDataMountBinding("old-binding", "local", space, "old-claim"); err == nil {
			t.Fatalf("retired IDC constructor accepted %q", space)
		}
	}
}
