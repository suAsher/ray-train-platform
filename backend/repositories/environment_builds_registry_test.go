package repositories

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"ray-train-platform-backend/auth"
	eb "ray-train-platform-backend/environmentbuild"
)

func TestEnvironmentRegistryPersistence(t *testing.T) {
	for _, scenario := range []struct {
		name string
		run  func(*testing.T, *GormRepository)
	}{
		{"authorization host", assertEnvironmentAuthorizationHostPersistence},
		{"build host", assertEnvironmentBuildHostPersistence},
		{"finalized image host", assertEnvironmentFinalizedImageHostPersistence},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			repo, _, _, _ := environmentTestService(t)
			scenario.run(t, repo)
		})
	}
}

func TestEnvironmentRegistryPersistencePostgres(t *testing.T) {
	for _, scenario := range []struct {
		name string
		run  func(*testing.T, *GormRepository)
	}{
		{"authorization host", assertEnvironmentAuthorizationHostPersistence},
		{"build host", assertEnvironmentBuildHostPersistence},
		{"finalized image host", assertEnvironmentFinalizedImageHostPersistence},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			store, _ := evaluationPostgresStores(t, false)
			repo := NewGormRepository(store.db)
			owner := environmentOwner()
			ensurePostgresArtifactPrincipal(t, repo, context.Background(), auth.Principal{Subject: owner.UserID, Username: owner.UserID, TenantID: owner.TenantID, Roles: []string{"Engineer"}})
			scenario.run(t, repo)
		})
	}
}

func assertEnvironmentAuthorizationHostPersistence(t *testing.T, repo *GormRepository) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	for _, host := range []string{"", "harbor.qomolo.com"} {
		a := eb.Authorization{ID: "auth-" + host, TenantID: "team-a", OwnerID: "user-a", Username: "registry-user", RegistryHost: host, SecretRef: "test-material", ExpiresAt: now.Add(time.Hour), CreatedAt: now}
		if err := repo.SaveEnvironmentAuthorization(ctx, a); err != nil {
			t.Fatal(err)
		}
		stored, err := repo.EnvironmentAuthorization(ctx, environmentOwner(), a.ID)
		if err != nil || stored.RegistryHost != a.Host() {
			t.Fatalf("authorization host = %q, want %q; error = %v", stored.RegistryHost, a.Host(), err)
		}
		changed := stored
		changed.RegistryHost = "harbor.qomolo.com"
		if host != "" {
			changed.RegistryHost = ""
		}
		if err = repo.SaveEnvironmentAuthorization(ctx, changed); !errors.Is(err, eb.ErrConflict) {
			t.Fatalf("authorization host replacement accepted: %v", err)
		}
		bound := stored
		bound.BuildID = "env-bound"
		bound.Target = a.Host() + "/public/environment:env-bound"
		if host == "" {
			bound.RegistryHost = "" // Legacy callers still mean the original Harbor.
		}
		if err = repo.SaveEnvironmentAuthorization(ctx, bound); err != nil {
			t.Fatalf("binding same-host authorization: %v", err)
		}
		stored, err = repo.EnvironmentAuthorization(ctx, environmentOwner(), a.ID)
		if err != nil || stored.RegistryHost != a.Host() || stored.BuildID != bound.BuildID {
			t.Fatalf("authorization binding lost immutable host: host=%q build=%q error=%v", stored.RegistryHost, stored.BuildID, err)
		}
	}
}

func registryPersistenceBuild(host string) eb.Build {
	now := time.Now().UTC().Truncate(time.Microsecond)
	digest := "sha256:" + strings.Repeat("3", 64)
	return eb.Build{ID: "env-registry", TenantID: "team-a", OwnerID: "user-a", WorkspaceID: "workspace-a", Namespace: "tenant-a", WorkspaceResourceName: "ws", WorkspaceUID: "uid", BaseImage: "harbor.wellspiking.ai/public/base@" + digest, WorkspaceImage: "harbor.wellspiking.ai/public/debug@" + digest, Name: "Environment", Visibility: "personal", RegistryHost: host, Project: "public", Repository: "environment", Tag: "env-registry", Status: eb.Queued, IdempotencyKey: "registry-request", Attempt: 1, ArtifactExpiresAt: now.Add(time.Hour), CreatedAt: now, UpdatedAt: now}
}

func assertEnvironmentBuildHostPersistence(t *testing.T, repo *GormRepository) {
	t.Helper()
	ctx := context.Background()
	b := registryPersistenceBuild("")
	stored, err := repo.CreateEnvironmentBuild(ctx, b)
	if err != nil || stored.RegistryHost != "harbor.wellspiking.ai" {
		t.Fatalf("build default host = %q; error = %v", stored.RegistryHost, err)
	}
	duplicate := b
	duplicate.RegistryHost = "harbor.wellspiking.ai"
	if got, err := repo.CreateEnvironmentBuild(ctx, duplicate); err != nil || got.ID != b.ID {
		t.Fatalf("legacy/default idempotency rejected: %v", err)
	}
	changed := b
	changed.RegistryHost = "harbor.qomolo.com"
	if _, err = repo.CreateEnvironmentBuild(ctx, changed); !errors.Is(err, eb.ErrConflict) {
		t.Fatalf("cross-registry idempotency accepted: %v", err)
	}
	// A second request may use the same repository in the other Harbor.
	changed.ID, changed.Tag, changed.IdempotencyKey = "env-qomolo", "env-qomolo", "qomolo-request"
	if got, err := repo.CreateEnvironmentBuild(ctx, changed); err != nil || got.RegistryHost != changed.RegistryHost {
		t.Fatalf("second Harbor persistence failed: %v", err)
	}
	expected := map[string]eb.Build{stored.ID: stored, changed.ID: changed}
	for i := 0; i < 2; i++ {
		claimed, err := repo.ClaimEnvironmentBuild(ctx, "host-controller", time.Now().UTC(), time.Minute, 1, 1)
		if err != nil || claimed == nil {
			t.Fatalf("claim build for host immutability: %v", err)
		}
		original, ok := expected[claimed.ID]
		if !ok {
			t.Fatalf("claimed unexpected build %q", claimed.ID)
		}
		invalid := *claimed
		invalid.RegistryHost = "harbor.qomolo.com"
		if claimed.RegistryHost == invalid.RegistryHost {
			invalid.RegistryHost = ""
		}
		if err = repo.SaveEnvironmentBuild(ctx, invalid, "host-controller"); !errors.Is(err, eb.ErrConflict) {
			t.Fatalf("leased build host replacement accepted: %v", err)
		}
		failed := *claimed
		now := time.Now().UTC()
		failed.Status, failed.CleanedAt = eb.Failed, &now
		if err = repo.SaveEnvironmentBuild(ctx, failed, "host-controller"); err != nil {
			t.Fatal(err)
		}
		retried, err := repo.RetryEnvironmentBuild(ctx, environmentOwner(), failed.ID, "same-host-auth", now)
		if err != nil || retried.RegistryHost != original.RegistryHost {
			t.Fatalf("retry changed registry host: host=%q error=%v", retried.RegistryHost, err)
		}
		// End this fixture so the other registry can be admitted independently.
		if err = repo.db.Model(&eb.Build{}).Where("id = ?", retried.ID).Updates(map[string]any{"status": eb.Canceled, "cleaned_at": now}).Error; err != nil {
			t.Fatal(err)
		}
	}
}

func assertEnvironmentFinalizedImageHostPersistence(t *testing.T, repo *GormRepository) {
	t.Helper()
	ctx := context.Background()
	b := registryPersistenceBuild("harbor.qomolo.com")
	b.Status, b.ImageDigest = eb.VerifyingPull, "sha256:" + strings.Repeat("3", 64)
	b.ImageReference = b.Host() + "/public/environment@" + b.ImageDigest
	if _, err := repo.CreateEnvironmentBuild(ctx, b); err != nil {
		t.Fatal(err)
	}
	claimed, err := repo.ClaimEnvironmentBuild(ctx, "finalizer", time.Now().UTC(), time.Minute, 1, 1)
	if err != nil || claimed == nil {
		t.Fatalf("claim finalizer: %v", err)
	}
	ready := *claimed
	ready.Status, ready.ImageID = eb.Ready, "image-registry"
	invalid := ready
	invalid.RegistryHost = "harbor.wellspiking.ai"
	invalid.ImageReference = invalid.Host() + "/public/environment@" + invalid.ImageDigest
	if err = repo.FinalizeEnvironmentBuild(ctx, invalid, "finalizer"); !errors.Is(err, eb.ErrConflict) {
		t.Fatalf("finalize replaced selected Harbor: %v", err)
	}
	invalid.RegistryHost = ready.RegistryHost
	if err = repo.FinalizeEnvironmentBuild(ctx, invalid, "finalizer"); !errors.Is(err, eb.ErrConflict) {
		t.Fatalf("finalize accepted image reference from another Harbor: %v", err)
	}
	if err = repo.FinalizeEnvironmentBuild(ctx, ready, "finalizer"); err != nil {
		t.Fatal(err)
	}
	images, err := repo.ListImagesForUser(ctx, "team-a", "user-a", "training")
	if err != nil || len(images) != 1 || images[0].Reference != ready.ImageReference {
		t.Fatalf("catalog lost selected image: count=%d error=%v", len(images), err)
	}
	versions, err := repo.ListEnvironmentVersions(ctx, environmentOwner())
	if err != nil || len(versions) != 1 || versions[0].ImageReference != ready.ImageReference {
		t.Fatalf("version lost selected image: count=%d error=%v", len(versions), err)
	}
}
