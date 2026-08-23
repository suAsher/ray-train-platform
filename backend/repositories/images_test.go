package repositories

import (
	"context"
	"errors"
	"strings"
	"testing"

	"ray-train-platform-backend/domain"
)

func pinnedRef(seed byte) string {
	return "registry.example/img@sha256:" + strings.Repeat(string(seed), 64)
}

func testImage(id, name, kind string, tenantID string, isDefault bool, seed byte) domain.PlatformImage {
	return domain.PlatformImage{
		ID: id, TenantID: tenantID, Name: name, Kind: kind,
		Reference: pinnedRef(seed), IsDefault: isDefault,
	}
}

func imageRepo(t *testing.T) *GormRepository {
	t.Helper()
	repo := testRepository(t)
	if err := repo.db.AutoMigrate(&PlatformImageRecord{}); err != nil {
		t.Fatalf("migrate images: %v", err)
	}
	return repo
}

// A tenant sees its own images plus the shared catalogue, and nothing from
// another tenant.
func TestListImagesScopesToTenantPlusShared(t *testing.T) {
	repo := imageRepo(t)
	ctx := context.Background()

	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("create image: %v", err)
		}
	}
	must(repo.CreateImage(ctx, testImage("img-shared", "shared-pytorch", domain.ImageKindTraining, "", false, '1')))
	must(repo.CreateImage(ctx, testImage("img-mine", "team-a-custom", domain.ImageKindTraining, "team-a", false, '2')))
	must(repo.CreateImage(ctx, testImage("img-other", "team-b-custom", domain.ImageKindTraining, "team-b", false, '3')))

	images, err := repo.ListImages(ctx, "team-a", domain.ImageKindTraining)
	if err != nil {
		t.Fatalf("list images: %v", err)
	}
	names := map[string]bool{}
	for _, image := range images {
		names[image.Name] = true
	}
	if !names["shared-pytorch"] || !names["team-a-custom"] {
		t.Fatalf("tenant must see its own and shared images, got %v", names)
	}
	if names["team-b-custom"] {
		t.Fatalf("another tenant's image leaked into the catalogue: %v", names)
	}
}

func TestListImagesFiltersByKind(t *testing.T) {
	repo := imageRepo(t)
	ctx := context.Background()
	_ = repo.CreateImage(ctx, testImage("img-t", "train", domain.ImageKindTraining, "", false, '1'))
	_ = repo.CreateImage(ctx, testImage("img-w", "workspace", domain.ImageKindWorkspace, "", false, '2'))

	images, err := repo.ListImages(ctx, "team-a", domain.ImageKindWorkspace)
	if err != nil {
		t.Fatalf("list images: %v", err)
	}
	if len(images) != 1 || images[0].Name != "workspace" {
		t.Fatalf("expected only workspace images, got %+v", images)
	}
}

// Two defaults for one kind would make the form's preselection arbitrary.
func TestCreateImageKeepsASingleDefaultPerKind(t *testing.T) {
	repo := imageRepo(t)
	ctx := context.Background()
	_ = repo.CreateImage(ctx, testImage("img-1", "first", domain.ImageKindTraining, "", true, '1'))
	_ = repo.CreateImage(ctx, testImage("img-2", "second", domain.ImageKindTraining, "", true, '2'))

	images, _ := repo.ListImages(ctx, "team-a", domain.ImageKindTraining)
	defaults := 0
	for _, image := range images {
		if image.IsDefault {
			defaults++
		}
	}
	if defaults != 1 {
		t.Fatalf("expected exactly one default, got %d", defaults)
	}
	preferred, err := repo.DefaultImage(ctx, "team-a", domain.ImageKindTraining)
	if err != nil || preferred.Name != "second" {
		t.Fatalf("the newest default should win, got %+v err=%v", preferred, err)
	}
}

// A tenant may see both its own default and the shared fallback default. The
// tenant-specific entry must be listed first because CLI clients select the
// first default returned by the catalogue.
func TestListImagesOrdersTenantDefaultBeforeSharedDefault(t *testing.T) {
	repo := imageRepo(t)
	ctx := context.Background()
	_ = repo.CreateImage(ctx, testImage("img-shared", "aaa-shared", domain.ImageKindTraining, "", true, '1'))
	_ = repo.CreateImage(ctx, testImage("img-tenant", "zzz-tenant", domain.ImageKindTraining, "team-a", true, '2'))

	images, err := repo.ListImages(ctx, "team-a", domain.ImageKindTraining)
	if err != nil {
		t.Fatalf("list images: %v", err)
	}
	if len(images) < 2 || images[0].ID != "img-tenant" || !images[0].IsDefault {
		t.Fatalf("tenant default must precede shared fallback, got %+v", images)
	}
	preferred, err := repo.DefaultImage(ctx, "team-a", domain.ImageKindTraining)
	if err != nil || preferred.ID != "img-tenant" {
		t.Fatalf("tenant default must win, got %+v err=%v", preferred, err)
	}
}

func TestCreateImageAcceptsExplicitTag(t *testing.T) {
	repo := imageRepo(t)
	image := testImage("img-1", "tagged", domain.ImageKindTraining, "", false, '1')
	image.Reference = "registry.example/team/img:release-2026-08"
	if err := repo.CreateImage(context.Background(), image); err != nil {
		t.Fatalf("an explicit tag published by an administrator must be accepted: %v", err)
	}
}

// The catalogue is also the allowlist: a reference outside it must not resolve.
func TestImageByReferenceOnlyMatchesTheCatalogue(t *testing.T) {
	repo := imageRepo(t)
	ctx := context.Background()
	_ = repo.CreateImage(ctx, testImage("img-1", "ok", domain.ImageKindTraining, "", false, '1'))

	if _, err := repo.ImageByReference(ctx, "team-a", domain.ImageKindTraining, pinnedRef('1')); err != nil {
		t.Fatalf("a catalogued image must resolve: %v", err)
	}
	_, err := repo.ImageByReference(ctx, "team-a", domain.ImageKindTraining, pinnedRef('9'))
	if !errors.Is(err, ErrImageNotFound) {
		t.Fatalf("an uncatalogued reference must not resolve, got %v", err)
	}
}

func TestDeleteImageProtectsSharedCatalogueFromTenantAdmins(t *testing.T) {
	repo := imageRepo(t)
	ctx := context.Background()
	_ = repo.CreateImage(ctx, testImage("img-shared", "shared", domain.ImageKindTraining, "", false, '1'))

	if err := repo.DeleteImage(ctx, "team-a", "img-shared", false); !errors.Is(err, ErrImageNotFound) {
		t.Fatalf("a tenant admin must not delete a shared image, got %v", err)
	}
	if err := repo.DeleteImage(ctx, "team-a", "img-shared", true); err != nil {
		t.Fatalf("a super admin must be able to delete it: %v", err)
	}
}

func TestSetImageSharedMovesBetweenTenantAndPlatformScopes(t *testing.T) {
	repo := imageRepo(t)
	ctx := context.Background()
	_ = repo.CreateImage(ctx, testImage("img-team", "team runtime", domain.ImageKindTraining, "team-a", true, '1'))

	shared, err := repo.SetImageShared(ctx, "team-a", "img-team", true, "")
	if err != nil {
		t.Fatalf("promote image to platform scope: %v", err)
	}
	if shared.TenantID != "" {
		t.Fatalf("shared image must have an empty tenant ID, got %+v", shared)
	}
	visibleToOtherTeam, err := repo.ListImages(ctx, "team-b", domain.ImageKindTraining)
	if err != nil || len(visibleToOtherTeam) != 1 || visibleToOtherTeam[0].ID != "img-team" {
		t.Fatalf("platform image must be visible to another team, got %+v err=%v", visibleToOtherTeam, err)
	}

	teamOnly, err := repo.SetImageShared(ctx, "team-a", "img-team", false, "team-a")
	if err != nil {
		t.Fatalf("demote image to team scope: %v", err)
	}
	if teamOnly.TenantID != "team-a" {
		t.Fatalf("team image must return to the acting administrator's team, got %+v", teamOnly)
	}
	visibleToOtherTeam, err = repo.ListImages(ctx, "team-b", domain.ImageKindTraining)
	if err != nil || len(visibleToOtherTeam) != 0 {
		t.Fatalf("team image must not leak to another team, got %+v err=%v", visibleToOtherTeam, err)
	}
}
