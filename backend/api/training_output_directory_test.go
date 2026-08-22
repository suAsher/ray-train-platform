package api

import (
	"context"
	"testing"

	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
)

func TestEnsureTrainingOutputDirectoryAcceptsTenantRootSubPath(t *testing.T) {
	store := &fakeDataSpaceObjectStore{}
	handler := NewHandler(&fakeJobRepository{}, Options{DataObjectStore: store})
	principal := auth.Principal{Subject: "user-a", Username: "alice", TenantID: "tenant-a", Roles: []string{domain.RoleEngineer}}
	mounts := domain.ResolvedDataSpaceMounts{Output: &domain.ResolvedDataMount{
		Space: domain.DataSpaceMyRuns, BindingSpace: domain.DataSpaceWorkspace,
		ClaimName: "data-tenant-a", SubPath: "tenants/tenant-a/users/alice/runs/experiments/run-1",
		MountPath: domain.DataMountOutputPath,
	}}

	if err := handler.ensureTrainingOutputDirectory(context.Background(), principal, mounts); err != nil {
		t.Fatalf("ensure tenant-root output directory: %v", err)
	}
	if store.folderRoot != "ray-train/tenants/tenant-a/users/alice/runs/" || store.folderPath != "experiments/run-1" {
		t.Fatalf("unexpected output directory root=%q path=%q", store.folderRoot, store.folderPath)
	}
}

func TestEnsureTrainingOutputDirectoryRetainsLegacyPersonalSubPath(t *testing.T) {
	store := &fakeDataSpaceObjectStore{}
	handler := NewHandler(&fakeJobRepository{}, Options{DataObjectStore: store})
	principal := auth.Principal{Subject: "user-a", Username: "alice", TenantID: "tenant-a", Roles: []string{domain.RoleEngineer}}
	mounts := domain.ResolvedDataSpaceMounts{Output: &domain.ResolvedDataMount{
		Space: domain.DataSpaceMyRuns, BindingSpace: domain.DataSpaceWorkspace,
		ClaimName: "data-alice", SubPath: "runs/experiments/run-1", MountPath: domain.DataMountOutputPath,
	}}

	if err := handler.ensureTrainingOutputDirectory(context.Background(), principal, mounts); err != nil {
		t.Fatalf("ensure legacy output directory: %v", err)
	}
	if store.folderRoot != "ray-train/tenants/tenant-a/users/alice/runs/" || store.folderPath != "experiments/run-1" {
		t.Fatalf("unexpected output directory root=%q path=%q", store.folderRoot, store.folderPath)
	}
}
