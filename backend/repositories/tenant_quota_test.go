package repositories

import (
	"context"
	"testing"

	"ray-train-platform-backend/domain"
)

// The Portal shows a tenant its remaining GPU budget. That number has to come
// from the same computation the submission path enforces, otherwise a user is
// told they have capacity and then gets rejected on submit.
func TestTenantGPUQuotaMatchesWhatSubmissionEnforces(t *testing.T) {
	repo := testRepository(t)
	principal := testPrincipalForRepository()
	if err := repo.EnsureIdentity(context.Background(), principal); err != nil {
		t.Fatalf("ensure identity: %v", err)
	}

	quota, err := repo.TenantGPUQuota(context.Background(), principal.TenantID)
	if err != nil {
		t.Fatalf("read quota: %v", err)
	}
	if quota.GPULimit != defaultTenantGPUQuota || quota.GPUUsed != 0 {
		t.Fatalf("expected an empty tenant to report zero usage, got %+v", quota)
	}
	if quota.GPUAvailable != defaultTenantGPUQuota {
		t.Fatalf("available should equal the limit when nothing runs, got %+v", quota)
	}

	job := testJob()
	job.TenantID = principal.TenantID
	job.UserID = principal.Subject
	job.Spec.Resources.WorkerReplicas = 2
	job.Spec.Resources.GPUsPerWorker = 4
	if err := repo.Create(context.Background(), &job, "quota-usage"); err != nil {
		t.Fatalf("create job: %v", err)
	}

	quota, err = repo.TenantGPUQuota(context.Background(), principal.TenantID)
	if err != nil {
		t.Fatalf("read quota after submit: %v", err)
	}
	if quota.GPUUsed != 8 {
		t.Fatalf("expected 8 GPUs in use, got %+v", quota)
	}
	if quota.GPUAvailable != defaultTenantGPUQuota-8 {
		t.Fatalf("unexpected available capacity: %+v", quota)
	}
}

// A finished job must give its GPUs back, otherwise the tenant looks full
// forever.
func TestTenantGPUQuotaExcludesTerminalJobs(t *testing.T) {
	repo := testRepository(t)
	principal := testPrincipalForRepository()
	if err := repo.EnsureIdentity(context.Background(), principal); err != nil {
		t.Fatalf("ensure identity: %v", err)
	}
	job := testJob()
	job.TenantID = principal.TenantID
	job.UserID = principal.Subject
	job.Spec.Resources.WorkerReplicas = 1
	job.Spec.Resources.GPUsPerWorker = 4
	if err := repo.Create(context.Background(), &job, "quota-terminal"); err != nil {
		t.Fatalf("create job: %v", err)
	}
	if err := repo.ApplyObservedState(context.Background(), domain.ObservedJobState{ID: job.ID, State: domain.StateSucceeded}); err != nil {
		t.Fatalf("apply terminal state: %v", err)
	}

	quota, err := repo.TenantGPUQuota(context.Background(), principal.TenantID)
	if err != nil {
		t.Fatalf("read quota: %v", err)
	}
	if quota.GPUUsed != 0 {
		t.Fatalf("a SUCCEEDED job must not hold quota, got %+v", quota)
	}
}
