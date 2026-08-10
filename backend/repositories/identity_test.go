package repositories

import (
	"context"
	"testing"

	"ray-train-platform-backend/auth"
)

func TestEnsureIdentityIsIdempotent(t *testing.T) {
	repo := testRepository(t)
	principal := auth.Principal{Subject: "kc-subject-1", Username: "engineer", Email: "engineer@example.com", TenantID: "LLM.Team", Roles: []string{"Engineer"}}
	if err := repo.EnsureIdentity(context.Background(), principal); err != nil {
		t.Fatalf("ensure identity: %v", err)
	}
	if err := repo.EnsureIdentity(context.Background(), principal); err != nil {
		t.Fatalf("repeat ensure identity: %v", err)
	}
	var tenants int64
	if err := repo.db.Model(&TenantRecord{}).Count(&tenants).Error; err != nil || tenants != 1 {
		t.Fatalf("expected one tenant, count=%d err=%v", tenants, err)
	}
	var users int64
	if err := repo.db.Model(&UserRecord{}).Count(&users).Error; err != nil || users != 1 {
		t.Fatalf("expected one user, count=%d err=%v", users, err)
	}
	var tenant TenantRecord
	if err := repo.db.First(&tenant, "id = ?", principal.TenantID).Error; err != nil {
		t.Fatalf("load tenant: %v", err)
	}
	if tenant.GPUQuotaLimit != defaultTenantGPUQuota {
		t.Fatalf("expected default GPU quota %d, got %d", defaultTenantGPUQuota, tenant.GPUQuotaLimit)
	}
}
