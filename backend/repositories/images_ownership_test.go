package repositories

import (
	"context"
	"errors"
	"ray-train-platform-backend/domain"
	"testing"
)

func TestPersonalImagesRequireOwnerAndCurrentTenant(t *testing.T) {
	repo := imageRepo(t)
	ctx := context.Background()
	personal := testImage("personal", "personal", domain.ImageKindTraining, "team-a", false, '1')
	personal.OwnerUserID, personal.Visibility, personal.EnvironmentVersionID = "owner", domain.ImageVisibilityPersonal, "version-1"
	legacy := testImage("legacy", "legacy", domain.ImageKindTraining, "", true, '2')
	team := testImage("team", "team", domain.ImageKindTraining, "team-a", false, '3')
	team.OwnerUserID, team.Visibility = "owner", domain.ImageVisibilityTeam
	for _, image := range []domain.PlatformImage{personal, legacy, team} {
		if err := repo.CreateImage(ctx, image); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		tenant, user string
		count        int
	}{{"team-a", "owner", 3}, {"team-a", "other", 2}, {"team-b", "owner", 1}, {"team-a", "", 2}} {
		images, err := repo.ListImagesForUser(ctx, tc.tenant, tc.user, domain.ImageKindTraining)
		if err != nil || len(images) != tc.count {
			t.Fatalf("%+v: images=%+v err=%v", tc, images, err)
		}
	}
	for _, tc := range []struct{ tenant, user string }{{"team-a", "other"}, {"team-b", "owner"}, {"team-a", ""}} {
		if _, err := repo.ImageByReferenceForUser(ctx, tc.tenant, tc.user, personal.Kind, personal.Reference); !errors.Is(err, ErrImageNotFound) {
			t.Fatalf("private reference leaked to %+v: %v", tc, err)
		}
	}
	ordinary, _ := repo.ListImages(ctx, "team-a", personal.Kind)
	all, _ := repo.ListAllImages(ctx, personal.Kind)
	if len(ordinary) != 2 || len(all) != 2 {
		t.Fatal("personal image leaked through ownerless catalogue")
	}
	if _, err := repo.ImageByReference(ctx, "team-a", personal.Kind, personal.Reference); !errors.Is(err, ErrImageNotFound) {
		t.Fatal("ownerless reference resolved")
	}
	got, err := repo.ImageByReferenceForUser(ctx, "team-a", "owner", personal.Kind, personal.Reference)
	if err != nil || got.EnvironmentVersionID != "version-1" || got.OwnerUserID != "owner" {
		t.Fatalf("round trip: %+v %v", got, err)
	}
	if _, err = repo.SetImageShared(ctx, "team-a", personal.ID, true, ""); !errors.Is(err, ErrImageNotFound) {
		t.Fatal("admin scope edit promoted private image")
	}
	if err = repo.DeleteImage(ctx, "team-a", personal.ID, true); !errors.Is(err, ErrImageNotFound) {
		t.Fatal("ownerless admin delete touched private image")
	}
}

func TestOwnedImagesRejectInvalidScopeWithoutChangingLegacyDefault(t *testing.T) {
	repo := imageRepo(t)
	ctx := context.Background()
	legacy := testImage("base", "base", domain.ImageKindTraining, "", true, '1')
	if err := repo.CreateImage(ctx, legacy); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		tenant, owner, visibility string
		def                       bool
	}{{"", "owner", "personal", false}, {"team-a", "", "personal", false}, {"team-a", "owner", "personal", true}, {"", "owner", "team", false}, {"team-a", "owner", "unknown", false}} {
		image := testImage("invalid", "invalid", domain.ImageKindTraining, tc.tenant, tc.def, '2')
		image.OwnerUserID, image.Visibility = tc.owner, tc.visibility
		if err := repo.CreateImage(ctx, image); err == nil {
			t.Fatalf("accepted invalid scope %+v", tc)
		}
	}
	image, err := repo.DefaultImage(ctx, "team-a", domain.ImageKindTraining)
	if err != nil || image.ID != "base" {
		t.Fatalf("legacy default changed: %+v %v", image, err)
	}
}
