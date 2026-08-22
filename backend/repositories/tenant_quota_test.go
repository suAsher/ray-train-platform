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
	if quota.GPULimit != defaultTenantGPUQuota() || quota.GPUUsed != 0 {
		t.Fatalf("expected an empty tenant to report zero usage, got %+v", quota)
	}
	if quota.GPUAvailable != defaultTenantGPUQuota() {
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
	if quota.GPUAvailable != defaultTenantGPUQuota()-8 {
		t.Fatalf("unexpected available capacity: %+v", quota)
	}
}

func TestTenantGPUQuotaReportsExplicitZeroAsDisabled(t *testing.T) {
	repo := testRepository(t)
	tenant := domain.Tenant{
		ID: "disabled-team", Name: "Disabled Team", Namespace: "tenant-disabled-team",
		LocalQueue: "disabled-team-gpu", GPUQuotaLimit: 0,
	}
	if err := repo.CreateTenant(context.Background(), tenant); err != nil {
		t.Fatalf("create tenant: %v", err)
	}

	quota, err := repo.TenantGPUQuota(context.Background(), tenant.ID)
	if err != nil {
		t.Fatalf("read quota: %v", err)
	}
	if quota.TenantID != tenant.ID || quota.GPULimit != 0 || quota.GPUUsed != 0 || quota.GPUAvailable != 0 {
		t.Fatalf("explicit zero quota must be reported as disabled, got %+v", quota)
	}
}

func TestTenantGPUQuotaFailsForMissingTenant(t *testing.T) {
	repo := testRepository(t)

	if _, err := repo.TenantGPUQuota(context.Background(), "missing-team"); err == nil {
		t.Fatal("missing tenant quota lookup must fail closed")
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

func TestTenantGPUQuotaIncludesActiveDebugWorkspaces(t *testing.T) {
	repo := testRepository(t)
	principal := testPrincipalForRepository()
	if err := repo.EnsureIdentity(context.Background(), principal); err != nil {
		t.Fatalf("ensure identity: %v", err)
	}
	workspace := &domain.DevWorkspace{
		ID: "ws-quota", TenantID: principal.TenantID, UserID: principal.Subject,
		Name: "debug-quota", Namespace: "tenant-" + principal.TenantID,
		RayClusterName: "debug-quota", GPUCount: 1, State: domain.WorkspaceRunning,
	}
	if err := repo.CreateWorkspace(context.Background(), workspace, 3600); err != nil {
		t.Fatalf("create workspace: %v", err)
	}

	quota, err := repo.TenantGPUQuota(context.Background(), principal.TenantID)
	if err != nil {
		t.Fatalf("read quota: %v", err)
	}
	if quota.GPUUsed != 1 || quota.GPUAvailable != defaultTenantGPUQuota()-1 {
		t.Fatalf("an active one-GPU workspace must consume tenant quota, got %+v", quota)
	}
}

func TestTenantGPUQuotaReleasesTerminalDebugWorkspaces(t *testing.T) {
	for _, terminalState := range []domain.WorkspaceState{domain.WorkspaceStopped, domain.WorkspaceFailed} {
		t.Run(string(terminalState), func(t *testing.T) {
			repo := testRepository(t)
			principal := testPrincipalForRepository()
			if err := repo.EnsureIdentity(context.Background(), principal); err != nil {
				t.Fatalf("ensure identity: %v", err)
			}
			workspace := &domain.DevWorkspace{
				ID: "ws-terminal-" + string(terminalState), TenantID: principal.TenantID, UserID: principal.Subject,
				Name: "debug-terminal", Namespace: "tenant-" + principal.TenantID,
				RayClusterName: "debug-terminal", GPUCount: 1, State: domain.WorkspaceRunning,
			}
			if err := repo.CreateWorkspace(context.Background(), workspace, 3600); err != nil {
				t.Fatalf("create workspace: %v", err)
			}
			if err := repo.UpdateWorkspaceState(context.Background(), principal.TenantID, principal.Subject, terminalState); err != nil {
				t.Fatalf("mark workspace terminal: %v", err)
			}

			quota, err := repo.TenantGPUQuota(context.Background(), principal.TenantID)
			if err != nil {
				t.Fatalf("read quota: %v", err)
			}
			if quota.GPUUsed != 0 || quota.GPUAvailable != defaultTenantGPUQuota() {
				t.Fatalf("a terminal workspace must return its GPU quota, got %+v", quota)
			}
		})
	}
}
