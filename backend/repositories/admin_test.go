package repositories

import (
	"context"
	"testing"

	"ray-train-platform-backend/domain"
)

func TestListTenantSummariesPreservesDisabledZeroGPUQuota(t *testing.T) {
	repo := testRepository(t)
	if err := repo.SetTenantGPUQuota(context.Background(), "tenant-a", 0); err != nil {
		t.Fatalf("disable tenant quota: %v", err)
	}

	summary := tenantSummaryForTest(t, repo, "tenant-a")
	if summary.GPUQuotaLimit != 0 {
		t.Fatalf("explicit zero quota must remain disabled in admin summary, got %+v", summary)
	}
}

func TestListTenantSummariesUsesTheSameGPUUsageAsEnforcement(t *testing.T) {
	repo := testRepository(t)
	job := testJob()
	job.Spec.Resources.WorkerReplicas = 2
	job.Spec.Resources.GPUsPerWorker = 2
	if err := repo.Create(context.Background(), &job, "admin-summary-job"); err != nil {
		t.Fatalf("create active job: %v", err)
	}
	workspace := &domain.DevWorkspace{
		ID: "admin-summary-workspace", TenantID: job.TenantID, UserID: job.UserID,
		Name: "admin-summary-workspace", Namespace: job.KubernetesNS,
		RayClusterName: "admin-summary-workspace", GPUCount: 1, State: domain.WorkspaceRunning,
	}
	if err := repo.CreateWorkspace(context.Background(), workspace, 3600); err != nil {
		t.Fatalf("create active workspace: %v", err)
	}

	quota, err := repo.TenantGPUQuota(context.Background(), job.TenantID)
	if err != nil {
		t.Fatalf("read enforced quota: %v", err)
	}
	summary := tenantSummaryForTest(t, repo, job.TenantID)
	if summary.GPUQuotaUsed != quota.GPUUsed || summary.GPUQuotaUsed != 5 {
		t.Fatalf("admin usage must match enforcement usage, summary=%+v quota=%+v", summary, quota)
	}
}

func TestListTenantSummariesCountsRecoveringJobOnceAsActiveAndAllocated(t *testing.T) {
	repo := testRepository(t)
	job := testJob()
	job.Spec.Resources.WorkerReplicas = 2
	job.Spec.Resources.GPUsPerWorker = 4
	if err := repo.Create(context.Background(), &job, "admin-recovering-job"); err != nil {
		t.Fatal(err)
	}
	if err := repo.db.Model(&JobRecord{}).Where("id = ?", job.ID).Update("observed_state", domain.StateRecovering).Error; err != nil {
		t.Fatal(err)
	}

	summary := tenantSummaryForTest(t, repo, job.TenantID)
	if summary.ActiveJobsCount != 1 || summary.GPUQuotaUsed != 8 {
		t.Fatalf("recovering job was not counted exactly once as active: %+v", summary)
	}
}

func TestSetTenantGPUQuotaRejectsNegativeLimit(t *testing.T) {
	repo := testRepository(t)

	if err := repo.SetTenantGPUQuota(context.Background(), "tenant-a", -1); err == nil {
		t.Fatal("repository boundary must reject a negative tenant GPU quota")
	}

	var tenant TenantRecord
	if err := repo.db.First(&tenant, "id = ?", "tenant-a").Error; err != nil {
		t.Fatalf("read tenant after rejected update: %v", err)
	}
	if tenant.GPUQuotaLimit != defaultTenantGPUQuota() {
		t.Fatalf("rejected update changed stored quota to %d", tenant.GPUQuotaLimit)
	}
}

func tenantSummaryForTest(t *testing.T, repo *GormRepository, tenantID string) TenantSummary {
	t.Helper()
	summaries, err := repo.ListTenantSummaries(context.Background())
	if err != nil {
		t.Fatalf("list tenant summaries: %v", err)
	}
	for _, summary := range summaries {
		if summary.ID == tenantID {
			return summary
		}
	}
	t.Fatalf("tenant %q missing from summaries: %+v", tenantID, summaries)
	return TenantSummary{}
}
