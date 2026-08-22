package repositories

import (
	"context"
	"errors"
	"testing"

	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
)

func TestCreateRejectsTenantGPUQuotaOverflow(t *testing.T) {
	repo := testRepository(t)
	principal := testPrincipalForRepository()
	if err := repo.EnsureIdentity(context.Background(), principal); err != nil {
		t.Fatalf("ensure identity: %v", err)
	}
	first := testJob()
	first.TenantID = principal.TenantID
	first.UserID = principal.Subject
	first.Spec.Resources.WorkerReplicas = 3
	first.Spec.Resources.GPUsPerWorker = 8
	if err := repo.Create(context.Background(), &first, "quota-1"); err != nil {
		t.Fatalf("create first job: %v", err)
	}
	second := testJob()
	second.ID = "job-2"
	second.Spec.Name = "job-2"
	second.TenantID = principal.TenantID
	second.UserID = principal.Subject
	second.Spec.Resources.WorkerReplicas = 1
	second.Spec.Resources.GPUsPerWorker = 1
	err := repo.Create(context.Background(), &second, "quota-2")
	var quotaErr *GPUQuotaExceededError
	if !errors.As(err, &quotaErr) || quotaErr.Quota != defaultTenantGPUQuota() {
		t.Fatalf("expected quota error, got %v", err)
	}
}

func TestCreateRejectsExplicitZeroTenantGPUQuota(t *testing.T) {
	repo := testRepository(t)
	principal := auth.Principal{Subject: "disabled-user", Username: "disabled-user", TenantID: "disabled-team", Roles: []string{domain.RoleEngineer}}
	tenant := domain.Tenant{
		ID: principal.TenantID, Name: "Disabled Team", Namespace: "tenant-disabled-team",
		LocalQueue: "disabled-team-gpu", GPUQuotaLimit: 0,
	}
	if err := repo.CreateTenant(context.Background(), tenant); err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	if err := repo.EnsureIdentity(context.Background(), principal); err != nil {
		t.Fatalf("ensure identity: %v", err)
	}

	job := testJob()
	job.TenantID = principal.TenantID
	job.UserID = principal.Subject
	job.Spec.Resources.WorkerReplicas = 1
	job.Spec.Resources.GPUsPerWorker = 1
	err := repo.Create(context.Background(), &job, "disabled-quota")

	var quotaErr *GPUQuotaExceededError
	if !errors.As(err, &quotaErr) || quotaErr.Quota != 0 {
		t.Fatalf("explicit zero quota must reject GPU submission, got %v", err)
	}
}

func TestQuotaEnforcementFailsForMissingTenant(t *testing.T) {
	repo := testRepository(t)

	if err := enforceTenantGPUQuotaRequest(repo.db, "missing-team", 1); err == nil {
		t.Fatal("missing tenant must not bypass submission quota enforcement")
	}
}

func testPrincipalForRepository() auth.Principal {
	return auth.Principal{Subject: "quota-user", Username: "quota-user", TenantID: "quota-team", Roles: []string{"Engineer"}}
}
