package repositories

import (
	"context"
	"strings"
	"testing"

	"ray-train-platform-backend/auth"
	eb "ray-train-platform-backend/environmentbuild"
)

func TestEnvironmentCapabilitiesOwnerPostgres(t *testing.T) {
	store, _ := evaluationPostgresStores(t, false)
	repo := NewGormRepository(store.db)
	ctx := context.Background()
	owner := environmentOwner()
	other := eb.Owner{TenantID: owner.TenantID, UserID: "hint-other-owner"}
	for _, principal := range []eb.Owner{owner, other} {
		ensurePostgresArtifactPrincipal(t, repo, ctx, auth.Principal{Subject: principal.UserID, Username: principal.UserID, TenantID: principal.TenantID, Roles: []string{"Engineer"}})
	}
	for _, record := range []WorkspaceRecord{
		{ID: "hint-own-workspace", TenantID: owner.TenantID, UserID: owner.UserID, Namespace: "tenant-a", RayClusterName: "own-cluster", ObservedState: "RUNNING"},
		{ID: "hint-other-workspace", TenantID: other.TenantID, UserID: other.UserID, Namespace: "tenant-a", RayClusterName: "other-cluster", ObservedState: "RUNNING"},
	} {
		if err := repo.db.Create(&record).Error; err != nil { t.Fatal(err) }
	}
	image := func(name, digest string) string { return eb.RegistryHost + "/public/" + name + "@sha256:" + strings.Repeat(digest, 64) }
	profile := eb.WorkspaceProfile{WorkspaceImage: image("cu126-debug", "5"), BaseImage: image("cu126-base", "6"), PrepareImage: image("cu126-prepare", "7")}
	primary, base, compatible := image("debug", "1"), image("base", "0"), image("legacy-debug", "2")
	runner := &environmentProfileRunnerFake{environmentRunnerFake: &environmentRunnerFake{}, image: profile.WorkspaceImage}
	service, err := eb.NewService(repo, runner, &environmentRegistryFake{}, &environmentVaultFake{}, eb.Config{Enabled: true, BaseImage: base, WorkspaceImage: primary, CompatibleWorkspaceImages: []string{compatible}, WorkspaceProfiles: []eb.WorkspaceProfile{profile}, EncryptionKey: []byte(strings.Repeat("k", 32))})
	if err != nil { t.Fatal(err) }
	for _, test := range []struct{name string; owner eb.Owner; image, state, wantID string}{
		{"profile", owner, profile.WorkspaceImage, "RUNNING", "hint-own-workspace"},
		{"primary", owner, primary, "RUNNING", "hint-own-workspace"},
		{"legacy", owner, compatible, "RUNNING", "hint-own-workspace"},
		{"unsupported", owner, image("unknown", "8"), "RUNNING", ""},
		{"stopped", owner, profile.WorkspaceImage, "STOPPED", ""},
		{"other-owner", other, profile.WorkspaceImage, "RUNNING", "hint-other-workspace"},
		{"other-tenant", eb.Owner{TenantID: "other-team", UserID: owner.UserID}, profile.WorkspaceImage, "RUNNING", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := repo.db.Model(&WorkspaceRecord{}).Where("id = ?", "hint-own-workspace").Update("observed_state", test.state).Error; err != nil { t.Fatal(err) }
			runner.image = test.image
			caps := service.CapabilitiesForOwner(ctx, test.owner)
			hint, _ := caps["eligibleWorkspaceId"].(string)
			if hint != test.wantID { t.Fatalf("owner-scoped hint = %q, want %q", hint, test.wantID) }
			if caps["workspaceImage"] != primary || caps["baseImage"] != base { t.Fatal("hint changed primary runtime") }
		})
	}
}
