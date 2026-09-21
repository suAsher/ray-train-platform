package repositories

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"ray-train-platform-backend/auth"
	eb "ray-train-platform-backend/environmentbuild"
)

func TestEnvironmentBuildPostgresAdmissionOwnerAndCredentialRoundTrip(t *testing.T) {
	a, b := evaluationPostgresStores(t, false)
	one, two := NewGormRepository(a.db), NewGormRepository(b.db)
	ctx := context.Background()
	owner := environmentOwner()
	ensurePostgresArtifactPrincipal(t, one, ctx, auth.Principal{Subject: owner.UserID, Username: owner.UserID, TenantID: owner.TenantID, Roles: []string{"Engineer"}})
	if err := one.db.Create(&WorkspaceRecord{ID: "workspace-a", TenantID: owner.TenantID, UserID: owner.UserID, Namespace: "tenant-a", RayClusterName: "workspace-cluster", ObservedState: "RUNNING"}).Error; err != nil {
		t.Fatal(err)
	}
	vault := &environmentVaultFake{values: map[string][]byte{}}
	s, err := eb.NewService(one, &environmentRunnerFake{}, &environmentRegistryFake{}, vault, eb.Config{Enabled: true, BaseImage: "harbor.wellspiking.ai/public/base@sha256:" + strings.Repeat("0", 64), WorkspaceImage: "harbor.wellspiking.ai/public/debug@sha256:" + strings.Repeat("1", 64), EncryptionKey: []byte(strings.Repeat("k", 32)), GlobalConcurrency: 1, UserConcurrency: 1})
	if err != nil {
		t.Fatal(err)
	}
	authorization := createEnvironmentAuthorization(t, s)
	// Authentication must survive PostgreSQL timestamptz precision conversion.
	if err = s.CheckTarget(ctx, owner, authorization.ID, "public", "test-env"); err != nil {
		t.Fatalf("PG credential round trip: %v", err)
	}
	first, err := s.Create(ctx, owner, "workspace-a", environmentRequest(authorization.ID))
	if err != nil {
		t.Fatal(err)
	}
	second := first
	second.ID = "env-second"
	second.IdempotencyKey = "second-request"
	second.Tag = "env-second"
	second.OwnerID = "other-owner"
	if _, err = one.CreateEnvironmentBuild(ctx, second); err != nil {
		t.Fatal(err)
	}
	type result struct {
		build *eb.Build
		err   error
	}
	results := make(chan result, 2)
	start := make(chan struct{})
	for i, r := range []*GormRepository{one, two} {
		go func(i int, r *GormRepository) {
			<-start
			out, err := r.ClaimEnvironmentBuild(ctx, fmt.Sprintf("worker-%d", i), time.Now().UTC(), time.Minute, 1, 1)
			results <- result{out, err}
		}(i, r)
	}
	close(start)
	x, y := <-results, <-results
	if x.err != nil || y.err != nil || (x.build == nil) == (y.build == nil) {
		t.Fatalf("global concurrent admission: %+v %+v", x, y)
	}
	// An expired lease can be reclaimed without launching a second operation.
	selected := x.build
	if selected == nil {
		selected = y.build
	}
	reclaimed, err := two.ClaimEnvironmentBuild(ctx, "replacement", time.Now().UTC().Add(2*time.Minute), time.Minute, 1, 1)
	if err != nil || reclaimed == nil || reclaimed.ID != selected.ID {
		t.Fatalf("lease recovery: %+v %v", reclaimed, err)
	}
	if err = one.SaveEnvironmentBuild(ctx, *selected, selected.LeaseOwner); err != eb.ErrConflict {
		t.Fatalf("stale controller saved: %v", err)
	}
}
func TestEnvironmentBuildPostgresRegistrationRollsBackAndRetainsPublishedDigest(t *testing.T) {
	a, _ := evaluationPostgresStores(t, false)
	repo := NewGormRepository(a.db)
	ctx := context.Background()
	now := time.Now().UTC()
	owner := environmentOwner()
	ensurePostgresArtifactPrincipal(t, repo, ctx, auth.Principal{Subject: owner.UserID, Username: owner.UserID, TenantID: owner.TenantID, Roles: []string{"Engineer"}})
	digest := "sha256:" + strings.Repeat("3", 64)
	b := eb.Build{ID: "env-finalize", TenantID: owner.TenantID, OwnerID: owner.UserID, WorkspaceID: "workspace-a", Namespace: "tenant-a", WorkspaceResourceName: "ws", WorkspaceUID: "uid", BaseImage: "harbor.wellspiking.ai/public/base@" + digest, WorkspaceImage: "harbor.wellspiking.ai/public/debug@" + digest, Name: "Environment", Visibility: "personal", Project: "public", Repository: "test-env", Tag: "env-finalize", Status: eb.VerifyingPull, IdempotencyKey: "finalize-request", ImageDigest: digest, ImageReference: "harbor.wellspiking.ai/public/test-env@" + digest, Attempt: 1, ArtifactExpiresAt: now.Add(time.Hour), CreatedAt: now, UpdatedAt: now}
	if _, err := repo.CreateEnvironmentBuild(ctx, b); err != nil {
		t.Fatal(err)
	}
	leased, err := repo.ClaimEnvironmentBuild(ctx, "finalizer", now, time.Minute, 1, 1)
	if err != nil || leased == nil {
		t.Fatal(err)
	}
	ready := *leased
	ready.Status = eb.Ready
	ready.ImageID = "image-env-finalize"
	// Force the second insert to fail after the image insert: no half-visible
	// training image is allowed if environment version registration rolls back.
	if err = repo.db.Exec("CREATE FUNCTION reject_environment_version() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected version write failure'; END; $$").Error; err != nil {
		t.Fatal(err)
	}
	if err = repo.db.Exec("CREATE TRIGGER reject_environment_version BEFORE INSERT ON environment_versions FOR EACH ROW EXECUTE FUNCTION reject_environment_version()").Error; err != nil {
		t.Fatal(err)
	}
	if err = repo.FinalizeEnvironmentBuild(ctx, ready, "finalizer"); err == nil {
		t.Fatal("injected version failure ignored")
	}
	var count int64
	if err = repo.db.Model(&PlatformImageRecord{}).Where("id = ?", ready.ImageID).Count(&count).Error; err != nil || count != 0 {
		t.Fatal("half-registered catalog image survived transaction")
	}
	kept, err := repo.EnvironmentBuild(ctx, owner, ready.ID)
	if err != nil || kept.Status != eb.VerifyingPull || kept.ImageDigest != digest {
		t.Fatal("published digest/state lost on registration failure")
	}
	if err = repo.db.Exec("DROP TRIGGER reject_environment_version ON environment_versions").Error; err != nil {
		t.Fatal(err)
	}
	if err = repo.FinalizeEnvironmentBuild(ctx, ready, "finalizer"); err != nil {
		t.Fatal(err)
	}
}

func TestEnvironmentBuildPostgresOrphanMaterialReclaimedWithoutSecretList(t *testing.T) {
	a, _ := evaluationPostgresStores(t, false)
	repo := NewGormRepository(a.db)
	ctx := context.Background()
	vault := &environmentVaultFake{values: map[string][]byte{}, failDelete: true}
	service, err := eb.NewService(repo, &environmentRunnerFake{}, &environmentRegistryFake{}, vault, eb.Config{Enabled: true, BaseImage: "harbor.wellspiking.ai/public/base@sha256:" + strings.Repeat("0", 64), WorkspaceImage: "harbor.wellspiking.ai/public/debug@sha256:" + strings.Repeat("1", 64), EncryptionKey: []byte(strings.Repeat("k", 32))})
	if err != nil {
		t.Fatal(err)
	}
	if err = repo.db.Exec("CREATE FUNCTION fail_environment_auth_metadata() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected auth metadata failure'; END; $$").Error; err != nil {
		t.Fatal(err)
	}
	if err = repo.db.Exec("CREATE TRIGGER fail_environment_auth_metadata BEFORE INSERT ON environment_registry_authorizations FOR EACH ROW EXECUTE FUNCTION fail_environment_auth_metadata()").Error; err != nil {
		t.Fatal(err)
	}
	if _, err = service.CreateAuthorization(ctx, environmentOwner(), eb.Credentials{Username: "test-user", Secret: "test-only-secret"}); err == nil {
		t.Fatal("injected metadata failure ignored")
	}
	var count int64
	if err = repo.db.Model(&eb.Authorization{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatal("authorization unexpectedly committed")
	}
	if err = repo.db.Model(&eb.CredentialMaterial{}).Count(&count).Error; err != nil || count != 1 || len(vault.values) != 1 {
		t.Fatal("orphan material was not durably indexed before credential write")
	}
	if err = repo.db.Model(&eb.CredentialMaterial{}).Where("1 = 1").Update("expires_at", time.Now().UTC().Add(-time.Hour)).Error; err != nil {
		t.Fatal(err)
	}
	vault.failDelete = false
	if err = service.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if err = repo.db.Model(&eb.CredentialMaterial{}).Count(&count).Error; err != nil || count != 0 || len(vault.values) != 0 {
		t.Fatal("orphan material survived cleanup")
	}
}

func TestEnvironmentBuildPostgresConcurrentArtifactCapacity(t *testing.T) {
	for _, scope := range []string{"owner", "global"} {
		t.Run(scope, func(t *testing.T) {
			a, b := evaluationPostgresStores(t, false)
			repos := []*GormRepository{NewGormRepository(a.db), NewGormRepository(b.db)}
			now := time.Now().UTC()
			ctx := context.Background()
			total, limit := 5, 3
			if scope == "global" {
				total, limit = 10, 8
			}
			outcomes := make(chan error, total)
			start := make(chan struct{})
			for i := 0; i < total; i++ {
				go func(i int) {
					<-start
					owner := "same-owner"
					if scope == "global" {
						owner = fmt.Sprintf("owner-%d", i)
					}
					id := fmt.Sprintf("env-capacity-%d", i)
					operation := eb.Build{ID: id, TenantID: "team", OwnerID: owner, WorkspaceID: "ws", Namespace: "tenant-team", WorkspaceResourceName: "ws", WorkspaceUID: "uid", BaseImage: "base", WorkspaceImage: "debug", Name: "Environment", Visibility: "personal", Project: "public", Repository: "env", Tag: id, Status: eb.Queued, IdempotencyKey: id, Attempt: 1, ArtifactExpiresAt: now.Add(time.Hour), CreatedAt: now, UpdatedAt: now}
					_, err := repos[i%2].CreateEnvironmentBuild(ctx, operation)
					outcomes <- err
				}(i)
			}
			close(start)
			successes := 0
			for i := 0; i < total; i++ {
				err := <-outcomes
				if err == nil {
					successes++
				} else if err != eb.ErrCapacity {
					t.Fatal(err)
				}
			}
			if successes != limit {
				t.Fatalf("capacity oversubscribed: got %d want %d", successes, limit)
			}
		})
	}
}

func TestEnvironmentBuildPostgresConcurrentCredentialReservations(t *testing.T) {
	for _, scope := range []string{"owner", "global"} {
		t.Run(scope, func(t *testing.T) {
			a, b := evaluationPostgresStores(t, false)
			repos := []*GormRepository{NewGormRepository(a.db), NewGormRepository(b.db)}
			ctx := context.Background()
			now := time.Now().UTC().Truncate(time.Microsecond)
			total, limit := 7, 5
			if scope == "global" {
				total, limit = 52, 50
			}
			results := make(chan error, total)
			start := make(chan struct{})
			for i := 0; i < total; i++ {
				go func(i int) {
					<-start
					owner := "same-owner"
					if scope == "global" {
						owner = fmt.Sprintf("owner-%d", i)
					}
					id := fmt.Sprintf("auth-capacity-%d", i)
					// Expired-but-not-cleaned allocations must still occupy a slot.
					material := eb.CredentialMaterial{Ref: id, AuthorizationID: id, TenantID: "team", OwnerID: owner, ExpiresAt: now.Add(-time.Hour), CreatedAt: now.Add(-2 * time.Hour)}
					results <- repos[i%2].ReserveEnvironmentCredentialMaterial(ctx, material)
				}(i)
			}
			close(start)
			successes := 0
			for i := 0; i < total; i++ {
				err := <-results
				if err == nil {
					successes++
				} else if err != eb.ErrCredentialCapacity {
					t.Fatal(err)
				}
			}
			if successes != limit {
				t.Fatalf("credential capacity oversubscribed: got %d want %d", successes, limit)
			}
		})
	}
}
