package domain

import "testing"

func TestNewPersonalDataMountBindingDerivesOnlyUsersGovernedRoot(t *testing.T) {
	binding, err := NewPersonalDataMountBinding(
		"mount-a", "tenant-a", "user-a", "data-user-a",
		`{"type":"TOS","bucket":"shanghai-data-transfer","server":"tos-cn-shanghai.ivolces.com","region":"cn-shanghai"}`,
	)
	if err != nil {
		t.Fatal(err)
	}
	if binding.Status != DataMountBindingPending || binding.RootPrefix != "ray-train/tenants/tenant-a/users/user-a/" {
		t.Fatalf("unexpected binding: %#v", binding)
	}
	if binding.VolumeAttributesJSON != `{"bucket":"shanghai-data-transfer","path":"/ray-train/tenants/tenant-a/users/user-a","region":"cn-shanghai","server":"tos-cn-shanghai.ivolces.com","type":"TOS"}` {
		t.Fatalf("attributes must receive only the governed path: %s", binding.VolumeAttributesJSON)
	}
}

func TestNewPersonalDataMountBindingKeepsInternalOwnerAndUsesStableStorageKey(t *testing.T) {
	binding, err := NewPersonalDataMountBinding(
		"mount-a", "tenant-a", "oidc-62a5e911", "data-user-a",
		`{"type":"TOS","bucket":"shanghai-data-transfer","server":"tos-cn-shanghai.ivolces.com","region":"cn-shanghai"}`,
		"guofeng.su",
	)
	if err != nil {
		t.Fatal(err)
	}
	if binding.UserID != "oidc-62a5e911" || binding.StorageKey != "guofeng.su" {
		t.Fatalf("owner and storage identity must remain distinct: %#v", binding)
	}
	if binding.RootPrefix != "ray-train/tenants/tenant-a/users/guofeng.su/" {
		t.Fatalf("unexpected username storage root: %q", binding.RootPrefix)
	}
}

func TestNewPersonalDataMountBindingRejectsConfiguredPathOrSecret(t *testing.T) {
	for _, attributes := range []string{
		`{"type":"TOS","bucket":"b","server":"s","region":"r","path":"/other"}`,
		`{"type":"TOS","bucket":"b","server":"s","region":"r","secretName":"legacy"}`,
	} {
		if _, err := NewPersonalDataMountBinding("mount-a", "tenant-a", "user-a", "data-user-a", attributes); err == nil {
			t.Fatalf("unsafe configuration was accepted: %s", attributes)
		}
	}
}

func TestNewSharedDataMountBindingDerivesOnlyGovernedSharedRoots(t *testing.T) {
	attributes := `{"type":"TOS","bucket":"shanghai-data-transfer","server":"tos-cn-shanghai.ivolces.com","region":"cn-shanghai"}`
	testCases := []struct {
		name      string
		space     DataSpaceID
		claimName string
		wantRoot  string
	}{
		{name: "tenant", space: DataSpaceTeamShared, claimName: "data-team-tenant-a", wantRoot: "ray-train/tenants/tenant-a/shared/"},
		{name: "public mirror", space: DataSpacePublic, claimName: "data-public-tenant-a", wantRoot: "ray-train/public/"},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			binding, err := NewSharedDataMountBinding("mount-"+testCase.name, "tenant-a", testCase.space, testCase.claimName, attributes)
			if err != nil {
				t.Fatal(err)
			}
			if binding.Scope != DataMountScopeTenant || binding.SpaceID != testCase.space || binding.RootPrefix != testCase.wantRoot || !binding.ReadOnly || binding.Status != DataMountBindingPending {
				t.Fatalf("unexpected shared binding: %#v", binding)
			}
			if binding.UserID != "" || binding.SecretName != "" {
				t.Fatalf("shared binding must not name a user or a secret: %#v", binding)
			}
		})
	}
}

func TestNewTenantRootDataMountBindingDerivesTheOnlyWritableTOSRoot(t *testing.T) {
	binding, err := NewTenantRootDataMountBinding(
		"tenant-root-a", "tenant-a", "data-tenant-a",
		`{"type":"TOS","bucket":"shanghai-data-transfer","server":"tos-cn-shanghai.ivolces.com","region":"cn-shanghai"}`,
	)
	if err != nil {
		t.Fatal(err)
	}
	if binding.Scope != DataMountScopeTenant || binding.SpaceID != DataSpaceTenantStorageRoot || binding.ReadOnly || binding.RootPrefix != "ray-train/" {
		t.Fatalf("unexpected tenant root binding: %#v", binding)
	}
	if binding.VolumeAttributesJSON != `{"bucket":"shanghai-data-transfer","path":"/ray-train","region":"cn-shanghai","server":"tos-cn-shanghai.ivolces.com","type":"TOS"}` {
		t.Fatalf("tenant root attributes must be fully governed: %s", binding.VolumeAttributesJSON)
	}
}

func TestNewPublicDataMountBindingUsesTheConfiguredTenantTemporaryRoot(t *testing.T) {
	binding, err := NewPublicDataMountBinding(
		"public-local", "local", "data-public-local",
		`{"type":"TOS","bucket":"shanghai-data-transfer","server":"tos-cn-shanghai.ivolces.com","region":"cn-shanghai"}`,
		"ray-train/tenants/local/datasets/public",
	)
	if err != nil {
		t.Fatalf("new configured public binding: %v", err)
	}
	if binding.RootPrefix != "ray-train/tenants/local/datasets/public/" || binding.VolumeAttributesJSON != `{"bucket":"shanghai-data-transfer","path":"/ray-train/tenants/local/datasets/public","region":"cn-shanghai","server":"tos-cn-shanghai.ivolces.com","type":"TOS"}` {
		t.Fatalf("configured public binding is not confined: %#v", binding)
	}
	if _, err := NewPublicDataMountBinding("public-other", "other", "data-public-other", `{"type":"TOS","bucket":"b","server":"s","region":"r"}`, "ray-train/tenants/local/datasets/public"); err == nil {
		t.Fatal("cross-tenant temporary public root was accepted")
	}
}
