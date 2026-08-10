package repositories

import (
	"context"
	"errors"
	"testing"

	"ray-train-platform-backend/auth"
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
	if !errors.As(err, &quotaErr) || quotaErr.Quota != defaultTenantGPUQuota {
		t.Fatalf("expected quota error, got %v", err)
	}
}

func testPrincipalForRepository() auth.Principal {
	return auth.Principal{Subject: "quota-user", Username: "quota-user", TenantID: "quota-team", Roles: []string{"Engineer"}}
}
