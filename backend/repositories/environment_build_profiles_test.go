package repositories

import (
	"context"
	"errors"
	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
	eb "ray-train-platform-backend/environmentbuild"
	"strings"
	"testing"
	"time"
)

func TestEnvironmentProfileMetadataPersistence(t *testing.T) {
	repo, _, _, _ := environmentTestService(t)
	assertEnvironmentProfileMetadataPersistence(t, repo)
}
func TestEnvironmentProfileMetadataPersistencePostgres(t *testing.T) {
	store, _ := evaluationPostgresStores(t, false)
	repo := NewGormRepository(store.db)
	owner := environmentOwner()
	ensurePostgresArtifactPrincipal(t, repo, context.Background(), auth.Principal{Subject: owner.UserID, Username: owner.UserID, TenantID: owner.TenantID, Roles: []string{"Engineer"}})
	assertEnvironmentProfileMetadataPersistence(t, repo)
}
func assertEnvironmentProfileMetadataPersistence(t *testing.T, repo *GormRepository) {
	t.Helper()
	ctx := context.Background()
	b := registryPersistenceBuild("")
	b.Status, b.ImageDigest = eb.VerifyingPull, "sha256:3333333333333333333333333333333333333333333333333333333333333333"
	b.ImageReference = b.Host() + "/public/environment@" + b.ImageDigest
	if _, err := repo.CreateEnvironmentBuild(ctx, b); err != nil {
		t.Fatal(err)
	}
	claimed, err := repo.ClaimEnvironmentBuild(ctx, "profile-finalizer", time.Now().UTC(), time.Minute, 1, 1)
	if err != nil || claimed == nil {
		t.Fatalf("claim: %v", err)
	}
	for _, field := range []string{"base", "workspace", "owner"} {
		invalid := *claimed
		switch field {
		case "base":
			invalid.BaseImage += "forged"
		case "workspace":
			invalid.WorkspaceImage += "forged"
		case "owner":
			invalid.OwnerID = "other"
		}
		if err := repo.SaveEnvironmentBuild(ctx, invalid, "profile-finalizer"); !errors.Is(err, eb.ErrConflict) {
			t.Fatalf("phase save accepted changed %s: %v", field, err)
		}
	}
	ready := *claimed
	ready.Status, ready.ImageID = eb.Ready, "image-profile"
	ready.TrustedEnvironment = domain.ImageEnvironment{Python: "3.10", CUDA: "12.6", PyTorch: "2.6.0+cu126", ValidationNotes: "CPU import checked; GPU validation not run"}
	for _, field := range []string{"base", "workspace", "owner"} {
		invalid := ready
		switch field {
		case "base":
			invalid.BaseImage += "forged"
		case "workspace":
			invalid.WorkspaceImage += "forged"
		case "owner":
			invalid.OwnerID = "other"
		}
		if err := repo.FinalizeEnvironmentBuild(ctx, invalid, "profile-finalizer"); !errors.Is(err, eb.ErrConflict) {
			t.Fatalf("finalize accepted changed %s: %v", field, err)
		}
	}
	if err := repo.FinalizeEnvironmentBuild(ctx, ready, "profile-finalizer"); err != nil {
		t.Fatal(err)
	}
	images, err := repo.ListImagesForUser(ctx, "team-a", "user-a", "training")
	if err != nil || len(images) != 1 || images[0].Environment != ready.TrustedEnvironment || images[0].IsDefault {
		t.Fatalf("catalog lost profile or changed default: %+v %v", images, err)
	}
	versions, err := repo.ListEnvironmentVersions(ctx, environmentOwner())
	if err != nil || len(versions) != 1 || versions[0].BaseImage != b.BaseImage {
		t.Fatalf("version changed immutable base: %+v %v", versions, err)
	}
}

type environmentProfileRunnerFake struct {
	*environmentRunnerFake
	image string
}

func (r *environmentProfileRunnerFake) InspectWorkspace(context.Context, eb.Workspace) (eb.WorkspaceSnapshot, error) {
	return eb.WorkspaceSnapshot{UID: "workspace-uid", Image: r.image}, nil
}
func TestEnvironmentProfileFullLifecyclePostgres(t *testing.T) {
	store, _ := evaluationPostgresStores(t, false)
	repo := NewGormRepository(store.db)
	ctx := context.Background()
	owner := environmentOwner()
	ensurePostgresArtifactPrincipal(t, repo, ctx, auth.Principal{Subject: owner.UserID, Username: owner.UserID, TenantID: owner.TenantID, Roles: []string{"Engineer"}})
	if err := repo.db.Create(&WorkspaceRecord{ID: "workspace-a", TenantID: owner.TenantID, UserID: owner.UserID, Namespace: "tenant-a", RayClusterName: "workspace-cluster", ObservedState: "RUNNING"}).Error; err != nil {
		t.Fatal(err)
	}
	image := func(name, digest string) string {
		return "harbor.wellspiking.ai/public/" + name + "@sha256:" + strings.Repeat(digest, 64)
	}
	profile := eb.WorkspaceProfile{WorkspaceImage: image("cu126-debug", "5"), BaseImage: image("cu126-base", "6"), PrepareImage: image("cu126-prepare", "7"), Environment: domain.ImageEnvironment{Python: "3.10", CUDA: "12.6", PyTorch: "2.6.0+cu126"}}
	runner := &environmentProfileRunnerFake{environmentRunnerFake: &environmentRunnerFake{}, image: profile.WorkspaceImage}
	service, err := eb.NewService(repo, runner, &environmentRegistryFake{}, &environmentVaultFake{values: map[string][]byte{}}, eb.Config{Enabled: true, BaseImage: image("base", "0"), WorkspaceImage: image("debug", "1"), WorkspaceProfiles: []eb.WorkspaceProfile{profile}, EncryptionKey: []byte(strings.Repeat("k", 32)), GlobalConcurrency: 1, UserConcurrency: 1})
	if err != nil {
		t.Fatal(err)
	}
	auth := createEnvironmentAuthorization(t, service)
	build, err := service.Create(ctx, owner, "workspace-a", environmentRequest(auth.ID))
	if err != nil || build.BaseImage != profile.BaseImage {
		t.Fatalf("PG create lost selected base: %+v %v", build, err)
	}
	for i := 0; i < 8; i++ {
		if err := service.Reconcile(ctx); err != nil {
			t.Fatal(err)
		}
	}
	ready, err := service.Get(ctx, owner, build.ID)
	if err != nil || ready.Status != eb.Ready || ready.BaseImage != profile.BaseImage || ready.WorkspaceImage != profile.WorkspaceImage || ready.CleanedAt == nil {
		t.Fatalf("profile lifecycle: %+v %v", ready, err)
	}
	images, err := repo.ListImagesForUser(ctx, owner.TenantID, owner.UserID, "training")
	if err != nil || len(images) != 1 || images[0].Environment != profile.Environment || images[0].IsDefault {
		t.Fatalf("PG catalog metadata: %+v %v", images, err)
	}
	assertEnvironmentImageTrainingRuntime(t, images[0])
	versions, err := repo.ListEnvironmentVersions(ctx, owner)
	if err != nil || len(versions) != 1 || versions[0].BaseImage != profile.BaseImage {
		t.Fatalf("PG immutable version base: %+v %v", versions, err)
	}
	other, err := repo.ListImagesForUser(ctx, owner.TenantID, "other", "training")
	if err != nil || len(other) != 0 {
		t.Fatal("profile publication leaked personal image")
	}
}
