package repositories

import (
	"context"
	"testing"
	"time"

	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
)

func dataBindingRepo(t *testing.T) *GormRepository {
	t.Helper()
	repo := testRepository(t)
	if err := repo.db.AutoMigrate(&DataMountBindingRecord{}); err != nil {
		t.Fatalf("migrate data mount bindings: %v", err)
	}
	return repo
}

func ensureDataBindingIdentity(t *testing.T, repo *GormRepository, tenantID, subject string) {
	t.Helper()
	if err := repo.EnsureIdentity(context.Background(), auth.Principal{
		TenantID: tenantID,
		Subject:  subject,
		Username: subject,
		Roles:    []string{"Engineer"},
	}); err != nil {
		t.Fatalf("ensure identity %s/%s: %v", tenantID, subject, err)
	}
}

func pendingPersonalBinding(id, tenantID, userID string) domain.DataMountBinding {
	return domain.DataMountBinding{
		ID: id, TenantID: tenantID, UserID: userID,
		Scope: domain.DataMountScopePersonal, SpaceID: domain.DataSpaceWorkspace, Status: domain.DataMountBindingPending,
	}
}

func pendingReadyBinding(id, tenantID, userID string) domain.DataMountBinding {
	return domain.DataMountBinding{
		ID:                   id,
		TenantID:             tenantID,
		UserID:               userID,
		Scope:                domain.DataMountScopePersonal,
		SpaceID:              domain.DataSpaceWorkspace,
		ClaimName:            "data-user-a",
		ServiceAccountName:   "ray-data-user-a",
		Driver:               domain.FSXCSIDriver,
		VolumeAttributesJSON: `{"type":"TOS","bucket":"shanghai-data-transfer","path":"/ray-train/tenants/tenant-a/users/user-a"}`,
		RootPrefix:           "ray-train/tenants/tenant-a/users/user-a/",
		Status:               domain.DataMountBindingPending,
	}
}

func TestEnsurePersonalDataBindingUsesStableOwnerScope(t *testing.T) {
	repo := dataBindingRepo(t)
	ensureDataBindingIdentity(t, repo, "tenant-a", "user-a")
	ctx := context.Background()

	first, err := repo.EnsurePersonalDataBinding(ctx, pendingPersonalBinding("binding-a", "tenant-a", "user-a"))
	if err != nil {
		t.Fatalf("ensure first binding: %v", err)
	}
	second, err := repo.EnsurePersonalDataBinding(ctx, pendingPersonalBinding("replacement-id", "tenant-a", "user-a"))
	if err != nil {
		t.Fatalf("ensure second binding: %v", err)
	}
	if first.ID != second.ID {
		t.Fatalf("same owner scope created two bindings: first=%q second=%q", first.ID, second.ID)
	}
	if second.Status != domain.DataMountBindingPending {
		t.Fatalf("status = %q, want pending", second.Status)
	}
}

func TestEnsurePersonalDataBindingCompletesLegacyPendingBinding(t *testing.T) {
	repo := dataBindingRepo(t)
	ensureDataBindingIdentity(t, repo, "tenant-a", "user-a")
	ctx := context.Background()
	if _, err := repo.EnsurePersonalDataBinding(ctx, pendingPersonalBinding("legacy-binding", "tenant-a", "user-a")); err != nil {
		t.Fatal(err)
	}
	requested, err := domain.NewPersonalDataMountBinding(
		"new-binding", "tenant-a", "user-a", "data-user-a",
		`{"type":"TOS","bucket":"shanghai-data-transfer","server":"tos-cn-shanghai.ivolces.com","region":"cn-shanghai"}`,
	)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := repo.EnsurePersonalDataBinding(ctx, requested)
	if err != nil {
		t.Fatal(err)
	}
	if binding.ID != "legacy-binding" || binding.ClaimName != "data-user-a" || binding.Driver != domain.FSXCSIDriver || binding.RootPrefix != "ray-train/tenants/tenant-a/users/user-a/" {
		t.Fatalf("legacy pending binding was not safely completed: %#v", binding)
	}
}

func TestEnsureTenantSharedDataBindingsUsesTenantLocalPublicAdapter(t *testing.T) {
	repo := dataBindingRepo(t)
	ctx := context.Background()
	ensureDataBindingIdentity(t, repo, "tenant-a", "user-a")
	attributes := `{"type":"TOS","bucket":"shanghai-data-transfer","server":"tos-cn-shanghai.ivolces.com","region":"cn-shanghai"}`
	team, err := domain.NewSharedDataMountBinding("team-a", "tenant-a", domain.DataSpaceTeamShared, "data-team-tenant-a", attributes)
	if err != nil {
		t.Fatal(err)
	}
	public, err := domain.NewSharedDataMountBinding("public-a", "tenant-a", domain.DataSpacePublic, "data-public-tenant-a", attributes)
	if err != nil {
		t.Fatal(err)
	}
	created, err := repo.EnsureTenantSharedDataBindings(ctx, team, public)
	if err != nil {
		t.Fatal(err)
	}
	if len(created) != 2 || created[0].SpaceID != domain.DataSpaceTeamShared || created[1].SpaceID != domain.DataSpacePublic || created[1].TenantID != "tenant-a" || created[1].RootPrefix != "ray-train/public/" {
		t.Fatalf("unexpected shared bindings: %#v", created)
	}
	repeated, err := repo.EnsureTenantSharedDataBindings(ctx, team, public)
	if err != nil {
		t.Fatal(err)
	}
	if len(repeated) != 2 || repeated[0].ID != "team-a" || repeated[1].ID != "public-a" {
		t.Fatalf("shared bindings must be idempotent: %#v", repeated)
	}
}

func TestEnsureTenantRootDataBindingIsIdempotentAndNotUserScoped(t *testing.T) {
	repo := dataBindingRepo(t)
	ctx := context.Background()
	ensureDataBindingIdentity(t, repo, "tenant-a", "user-a")
	root, err := domain.NewTenantRootDataMountBinding(
		"tenant-root-a", "tenant-a", "data-tenant-a",
		`{"type":"TOS","bucket":"shanghai-data-transfer","server":"tos-cn-shanghai.ivolces.com","region":"cn-shanghai"}`,
	)
	if err != nil {
		t.Fatal(err)
	}
	first, err := repo.EnsureTenantRootDataBinding(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	second, err := repo.EnsureTenantRootDataBinding(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != "tenant-root-a" || second.ID != first.ID || first.UserID != "" || first.SpaceID != domain.DataSpaceTenantStorageRoot || first.RootPrefix != "ray-train/" {
		t.Fatalf("tenant root binding is not a stable internal tenant resource: first=%#v second=%#v", first, second)
	}
}

func TestEnsureIDCDataBindingsUsesTenantLocalReadOnlyClaims(t *testing.T) {
	repo := dataBindingRepo(t)
	ctx := context.Background()
	ensureDataBindingIdentity(t, repo, "tenant-a", "user-a")
	bindings := make([]domain.DataMountBinding, 0, 3)
	for _, item := range []struct {
		id    string
		space domain.DataSpaceID
	}{
		{id: "idc-original-a", space: domain.DataSpaceIDCOriginal},
		{id: "idc-wellspiking-a", space: domain.DataSpaceIDCWellspiking},
		{id: "idc-shared-a", space: domain.DataSpaceIDCShared},
	} {
		binding, err := domain.NewIDCDataMountBinding(item.id, "tenant-a", item.space, item.id)
		if err != nil {
			t.Fatal(err)
		}
		bindings = append(bindings, binding)
	}
	created, err := repo.EnsureIDCDataBindings(ctx, bindings...)
	if err != nil {
		t.Fatalf("ensure IDC bindings: %v", err)
	}
	if len(created) != 3 || created[0].Scope != domain.DataMountScopeIDC || !created[0].ReadOnly {
		t.Fatalf("unexpected IDC bindings: %#v", created)
	}
	repeated, err := repo.EnsureIDCDataBindings(ctx, bindings...)
	if err != nil || repeated[0].ID != bindings[0].ID {
		t.Fatalf("IDC bindings must be idempotent: bindings=%#v err=%v", repeated, err)
	}
}

func TestIDCBindingUsesValidJSONStoragePlaceholderWithoutChangingDomainContract(t *testing.T) {
	binding, err := domain.NewIDCDataMountBinding("idc-original-a", "tenant-a", domain.DataSpaceIDCOriginal, "idc-original-a")
	if err != nil {
		t.Fatal(err)
	}
	record := dataMountBindingRecordFromDomain(binding, time.Now())
	if record.VolumeAttributesJSON != "{}" {
		t.Fatalf("IDC binding must persist a valid JSON placeholder, got %q", record.VolumeAttributesJSON)
	}
	restored, err := record.toDomain()
	if err != nil {
		t.Fatal(err)
	}
	if restored.VolumeAttributesJSON != "" || restored.Driver != "" || !restored.ReadOnly {
		t.Fatalf("IDC domain contract must not expose JSON/driver implementation fields: %#v", restored)
	}
}

func TestDataMountBindingsNeverExposeAnotherUsersPersonalBinding(t *testing.T) {
	repo := dataBindingRepo(t)
	ctx := context.Background()
	ensureDataBindingIdentity(t, repo, "tenant-a", "user-a")
	ensureDataBindingIdentity(t, repo, "tenant-a", "user-b")
	ensureDataBindingIdentity(t, repo, "tenant-b", "user-c")

	bindings := []domain.DataMountBinding{
		pendingPersonalBinding("mine", "tenant-a", "user-a"),
		pendingPersonalBinding("other-user", "tenant-a", "user-b"),
		pendingPersonalBinding("other-tenant", "tenant-b", "user-c"),
		{ID: "tenant-shared", TenantID: "tenant-a", Scope: domain.DataMountScopeTenant, SpaceID: domain.DataSpaceTeamShared, Status: domain.DataMountBindingPending, ReadOnly: true},
		{ID: "public", TenantID: "tenant-a", Scope: domain.DataMountScopeTenant, SpaceID: domain.DataSpacePublic, Status: domain.DataMountBindingPending, ReadOnly: true},
	}
	for _, binding := range bindings {
		if err := repo.CreateDataMountBinding(ctx, binding); err != nil {
			t.Fatalf("create %s: %v", binding.ID, err)
		}
	}

	visible, err := repo.ListDataBindings(ctx, "tenant-a", "user-a")
	if err != nil {
		t.Fatalf("list bindings: %v", err)
	}
	ids := make(map[string]bool, len(visible))
	for _, binding := range visible {
		ids[binding.ID] = true
	}
	for _, expected := range []string{"mine", "tenant-shared", "public"} {
		if !ids[expected] {
			t.Fatalf("missing visible binding %q: %#v", expected, ids)
		}
	}
	for _, hidden := range []string{"other-user", "other-tenant"} {
		if ids[hidden] {
			t.Fatalf("binding %q leaked: %#v", hidden, ids)
		}
	}

	if _, err := repo.GetDataBinding(ctx, "tenant-a", "user-a", "other-user"); err != ErrDataMountBindingNotFound {
		t.Fatalf("expected scoped not found, got %v", err)
	}
}

func TestUpdateDataMountBindingStatusAcceptsOnlyKnownState(t *testing.T) {
	repo := dataBindingRepo(t)
	ensureDataBindingIdentity(t, repo, "tenant-a", "user-a")
	ctx := context.Background()
	if err := repo.CreateDataMountBinding(ctx, pendingReadyBinding("binding-a", "tenant-a", "user-a")); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpdateDataBindingStatus(ctx, "binding-a", domain.DataMountBindingReady); err != nil {
		t.Fatalf("mark ready: %v", err)
	}
	if err := repo.UpdateDataBindingStatus(ctx, "binding-a", "UNSAFE"); err == nil {
		t.Fatal("unknown data mount status was accepted")
	}
}
