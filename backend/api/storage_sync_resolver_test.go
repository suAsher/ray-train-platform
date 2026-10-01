package api

import (
	"context"
	"errors"
	"testing"

	"ray-train-platform-backend/domain"
	"ray-train-platform-backend/k8s"
	"ray-train-platform-backend/repositories"
	ss "ray-train-platform-backend/storagesync"
)

type syncIdentityFixture struct {
	user     domain.LocalUser
	bindings []domain.DataMountBinding
	teams    []repositories.TenantSummary
	err      error
}

func (f *syncIdentityFixture) FindLocalUserByID(context.Context, string) (domain.LocalUser, error) {
	return f.user, f.err
}
func (f *syncIdentityFixture) ListDataBindings(context.Context, string, string) ([]domain.DataMountBinding, error) {
	return f.bindings, f.err
}
func (f *syncIdentityFixture) ListTenantSummaries(context.Context) ([]repositories.TenantSummary, error) {
	return f.teams, f.err
}
func syncResolverFixture() (*AdminStorageResolver, *syncIdentityFixture) {
	f := &syncIdentityFixture{user: domain.LocalUser{ID: "admin", Username: "guofeng.su", TenantID: "local", StorageTenantID: "pending-users", StorageKey: "guofeng.su", Roles: []string{"SuperAdmin"}}, teams: []repositories.TenantSummary{{ID: "local", Name: "Local"}, {ID: "yolo", Name: "YOLO"}}}
	r := NewAdminStorageResolver(f, AdminStorageResolverOptions{Bucket: "bucket", Region: "cn-shanghai", PublicRoot: domain.DefaultPublicDataRoot, IDCSources: map[domain.DataSpaceID]k8s.IDCDataMountSource{domain.DataSpaceIDCSPKHybrid: {Server: "192.0.2.10", Path: "/exports/hybrid"}}})
	return r, f
}
func TestStorageSyncResolverUsesExplicitTeamAndStablePersonalHome(t *testing.T) {
	r, _ := syncResolverFixture()
	ctx := context.Background()
	team, err := r.Resolve(ctx, "admin", ss.Location{SpaceID: "team-shared", TenantID: "yolo", RelativePath: "set a/中文"})
	if err != nil || team.Prefix != "ray-train/tenants/yolo/shared/set a/中文" {
		t.Fatalf("explicit team resolved incorrectly: %#v %v", team, err)
	}
	personal, err := r.Resolve(ctx, "admin", ss.Location{SpaceID: "my-files", RelativePath: "acceptance"})
	if err != nil || personal.Prefix != "ray-train/tenants/pending-users/users/guofeng.su/files/acceptance" {
		t.Fatalf("personal home migrated with team: %#v %v", personal, err)
	}
	alias, err := r.Resolve(ctx, "admin", ss.Location{SpaceID: "my-storage", RelativePath: "files/acceptance"})
	if err != nil || alias.Prefix != personal.Prefix || alias.StorageID != personal.StorageID {
		t.Fatalf("aliases don't share lock identity: %#v %v", alias, err)
	}
}
func TestStorageSyncResolverRejectsUnknownRootsAndInactiveAuthority(t *testing.T) {
	r, f := syncResolverFixture()
	for _, loc := range []ss.Location{{SpaceID: "team-shared"}, {SpaceID: "team-shared", TenantID: "missing"}, {SpaceID: "my-files", TenantID: "yolo"}, {SpaceID: "my-files", RelativePath: "../escape"}, {SpaceID: "my-files", RelativePath: "/absolute"}, {SpaceID: "arbitrary-bucket"}, {SpaceID: "tenant-storage-root"}} {
		if _, err := r.Resolve(context.Background(), "admin", loc); err == nil {
			t.Errorf("unsafe location accepted: %#v", loc)
		}
	}
	f.user.Disabled = true
	if err := r.IsAuthorized(context.Background(), "admin"); !errors.Is(err, ss.ErrForbidden) {
		t.Fatalf("disabled actor authorized: %v", err)
	}
	f.user.Disabled = false
	f.user.Roles = []string{"TenantAdmin"}
	if err := r.IsAuthorized(context.Background(), "admin"); !errors.Is(err, ss.ErrForbidden) {
		t.Fatalf("demoted actor authorized: %v", err)
	}
}
func TestStorageSyncCatalogDoesNotExposeBackendRoots(t *testing.T) {
	r, _ := syncResolverFixture()
	spaces, err := r.Spaces(context.Background(), "admin")
	if err != nil {
		t.Fatal(err)
	}
	var team, personal, idc bool
	for _, space := range spaces {
		if space.SpaceID == "team-shared" && space.TenantID == "yolo" {
			team = true
		}
		if space.SpaceID == "my-files" {
			personal = true
		}
		if space.SpaceID == "idc-spk-hybrid" {
			idc = true
			if space.CanWrite {
				t.Fatal("IDC writable")
			}
		}
	}
	if !team || !personal || !idc {
		t.Fatalf("catalog missing registered roots: %#v", spaces)
	}
}
