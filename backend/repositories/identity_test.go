package repositories

import (
	"context"
	"testing"

	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
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
	if err := repo.db.Model(&TenantRecord{}).Where("id = ?", principal.TenantID).Count(&tenants).Error; err != nil || tenants != 1 {
		t.Fatalf("expected one matching tenant, count=%d err=%v", tenants, err)
	}
	var users int64
	if err := repo.db.Model(&UserRecord{}).Count(&users).Error; err != nil || users != 1 {
		t.Fatalf("expected one user, count=%d err=%v", users, err)
	}
	var tenant TenantRecord
	if err := repo.db.First(&tenant, "id = ?", principal.TenantID).Error; err != nil {
		t.Fatalf("load tenant: %v", err)
	}
	if tenant.GPUQuotaLimit != defaultTenantGPUQuota() {
		t.Fatalf("expected default GPU quota %d, got %d", defaultTenantGPUQuota(), tenant.GPUQuotaLimit)
	}
}

func TestNewTenantGPUQuotaFollowsConfiguredClusterCeiling(t *testing.T) {
	previous := domain.CurrentResourceLimits()
	domain.SetResourceLimits(domain.ResourceLimits{MaxWorkerReplicas: 2, MaxGPUsPerWorker: 8, MaxTotalGPUs: 16})
	t.Cleanup(func() { domain.SetResourceLimits(previous) })

	repo := testRepository(t)
	principal := auth.Principal{Subject: "subject-16", Username: "engineer", TenantID: "new-team", Roles: []string{"Engineer"}}
	if err := repo.EnsureIdentity(context.Background(), principal); err != nil {
		t.Fatalf("ensure identity: %v", err)
	}
	var tenant TenantRecord
	if err := repo.db.First(&tenant, "id = ?", principal.TenantID).Error; err != nil {
		t.Fatalf("load tenant: %v", err)
	}
	if tenant.GPUQuotaLimit != 16 {
		t.Fatalf("new tenant quota must follow the configured 16-GPU ceiling, got %d", tenant.GPUQuotaLimit)
	}
}

func TestCreateTenantPersistsExplicitZeroGPUQuota(t *testing.T) {
	repo := testRepository(t)
	tenant := domain.Tenant{
		ID: "disabled-team", Name: "Disabled Team", Namespace: "tenant-disabled-team",
		LocalQueue: "disabled-team-gpu", GPUQuotaLimit: 0,
	}

	if err := repo.CreateTenant(context.Background(), tenant); err != nil {
		t.Fatalf("create tenant: %v", err)
	}

	var stored TenantRecord
	if err := repo.db.First(&stored, "id = ?", tenant.ID).Error; err != nil {
		t.Fatalf("load tenant: %v", err)
	}
	if stored.GPUQuotaLimit != 0 {
		t.Fatalf("explicit zero quota must remain disabled, got %d", stored.GPUQuotaLimit)
	}
}
