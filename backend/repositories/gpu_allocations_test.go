package repositories

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"ray-train-platform-backend/domain"
)

func TestListGPUAllocationsProjectsMixedWorkloadsInStableOrder(t *testing.T) {
	repo := testRepository(t)
	createdAt := time.Date(2026, time.August, 24, 9, 0, 0, 0, time.UTC)
	startedAt := createdAt.Add(3 * time.Minute)
	seedGPUAllocationUser(t, repo, UserRecord{ID: "user-alice", OIDCSubject: "oidc-alice", Username: "alice", TenantID: "tenant-a"})
	seedGPUAllocationJob(t, repo, JobRecord{
		ID: "job-b", TenantID: "tenant-a", UserID: "user-alice", Name: "training-b",
		ObservedState: string(domain.StateRunning), KubernetesNS: "tenant-a-ns", RayJobName: "ray-job-b",
		CreatedAt: createdAt, StartedAt: &startedAt,
	}, domain.JobSpec{Resources: domain.Resources{WorkerReplicas: 2, GPUsPerWorker: 3}})
	seedGPUAllocationWorkspace(t, repo, WorkspaceRecord{
		ID: "workspace-a", TenantID: "tenant-a", UserID: "user-alice", Name: "debug-a",
		ObservedState: string(domain.WorkspaceSubmitted), GPUCount: 1, Namespace: "tenant-a-ns",
		RayClusterName: "ray-cluster-a", CreatedAt: createdAt,
	})

	got, err := repo.ListGPUAllocations(context.Background(), "tenant-a", false)
	if err != nil {
		t.Fatalf("list GPU allocations: %v", err)
	}
	want := []domain.GPUAllocation{
		{
			ID: "job-b", Type: domain.GPUAllocationTrainingJob, Name: "training-b",
			TenantID: "tenant-a", UserID: "user-alice", Username: "alice",
			State: string(domain.StateRunning), GPUCount: 6, Namespace: "tenant-a-ns",
			ResourceName: "ray-job-b", CreatedAt: createdAt, StartedAt: &startedAt,
		},
		{
			ID: "workspace-a", Type: domain.GPUAllocationDebugWorkspace, Name: "debug-a",
			TenantID: "tenant-a", UserID: "user-alice", Username: "alice",
			State: string(domain.WorkspaceSubmitted), GPUCount: 1, Namespace: "tenant-a-ns",
			ResourceName: "ray-cluster-a", CreatedAt: createdAt,
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected GPU allocations:\n got: %+v\nwant: %+v", got, want)
	}
}

func TestListGPUAllocationsIncludesOnlyDeclaredActiveStates(t *testing.T) {
	repo := testRepository(t)
	createdAt := time.Date(2026, time.August, 24, 10, 0, 0, 0, time.UTC)
	activeJobStates := []domain.State{
		domain.StateSubmitted,
		domain.StateValidating,
		domain.StateQueued,
		domain.StateAdmitted,
		domain.StateProvisioning,
		domain.StateRunning,
		domain.StateCanceling,
		domain.StateDeleting,
		domain.StateUnknown,
	}
	for _, state := range activeJobStates {
		seedGPUAllocationJob(t, repo, JobRecord{
			ID: "active-job-" + strings.ToLower(string(state)), TenantID: "tenant-a", UserID: "user-active",
			Name: "active-job", ObservedState: string(state), CreatedAt: createdAt,
		}, domain.JobSpec{Resources: domain.Resources{WorkerReplicas: 1, GPUsPerWorker: 1}})
	}
	excludedJobStates := []domain.State{
		domain.StateSucceeded,
		domain.StateFailed,
		domain.StateCanceled,
		domain.StateTimedOut,
	}
	for _, state := range excludedJobStates {
		seedGPUAllocationJob(t, repo, JobRecord{
			ID: "excluded-job-" + strings.ToLower(string(state)), TenantID: "tenant-a", UserID: "user-excluded",
			Name: "excluded-job", ObservedState: string(state), CreatedAt: createdAt,
		}, domain.JobSpec{Resources: domain.Resources{WorkerReplicas: 1, GPUsPerWorker: 8}})
	}
	for _, state := range []domain.WorkspaceState{domain.WorkspaceSubmitted, domain.WorkspaceRunning, domain.WorkspaceStopping} {
		seedGPUAllocationWorkspace(t, repo, WorkspaceRecord{
			ID: "active-workspace-" + strings.ToLower(string(state)), TenantID: "tenant-a", UserID: "user-active",
			Name: "active-workspace", ObservedState: string(state), GPUCount: 1, CreatedAt: createdAt,
		})
	}
	for _, state := range []domain.WorkspaceState{domain.WorkspaceStopped, domain.WorkspaceFailed} {
		seedGPUAllocationWorkspace(t, repo, WorkspaceRecord{
			ID: "excluded-workspace-" + strings.ToLower(string(state)), TenantID: "tenant-a", UserID: "user-excluded",
			Name: "excluded-workspace", ObservedState: string(state), GPUCount: 8, CreatedAt: createdAt,
		})
	}

	got, err := repo.ListGPUAllocations(context.Background(), "tenant-a", false)
	if err != nil {
		t.Fatalf("list GPU allocations: %v", err)
	}
	if len(got) != len(activeJobStates)+3 {
		t.Fatalf("expected only declared active states, got %d allocations: %+v", len(got), got)
	}
	for _, allocation := range got {
		if strings.HasPrefix(allocation.ID, "excluded-") {
			t.Fatalf("excluded state leaked into GPU allocations: %+v", allocation)
		}
	}
}

func TestListGPUAllocationsScopesWorkloadQueriesToOwnTenant(t *testing.T) {
	repo := testRepository(t)
	createdAt := time.Date(2026, time.August, 24, 11, 0, 0, 0, time.UTC)
	seedGPUAllocationJob(t, repo, JobRecord{ID: "job-own", TenantID: "tenant-a", UserID: "user-a", Name: "job-own", ObservedState: string(domain.StateQueued), CreatedAt: createdAt}, domain.JobSpec{Resources: domain.Resources{WorkerReplicas: 1, GPUsPerWorker: 1}})
	seedGPUAllocationJob(t, repo, JobRecord{ID: "job-other", TenantID: "team-a", UserID: "user-b", Name: "job-other", ObservedState: string(domain.StateQueued), CreatedAt: createdAt}, domain.JobSpec{Resources: domain.Resources{WorkerReplicas: 1, GPUsPerWorker: 1}})
	seedGPUAllocationWorkspace(t, repo, WorkspaceRecord{ID: "workspace-own", TenantID: "tenant-a", UserID: "user-a", Name: "workspace-own", ObservedState: string(domain.WorkspaceRunning), GPUCount: 1, CreatedAt: createdAt})
	seedGPUAllocationWorkspace(t, repo, WorkspaceRecord{ID: "workspace-other", TenantID: "team-a", UserID: "user-b", Name: "workspace-other", ObservedState: string(domain.WorkspaceRunning), GPUCount: 1, CreatedAt: createdAt})

	queryLog := &gpuAllocationQueryLog{}
	scopedRepo := NewGormRepository(repo.db.Session(&gorm.Session{Logger: queryLog}))
	got, err := scopedRepo.ListGPUAllocations(context.Background(), "tenant-a", false)
	if err != nil {
		t.Fatalf("list own-tenant GPU allocations: %v", err)
	}
	if gotIDs := gpuAllocationIDs(got); !reflect.DeepEqual(gotIDs, []string{"job-own", "workspace-own"}) {
		t.Fatalf("tenant boundary failed, got allocation IDs %v", gotIDs)
	}
	queryLog.requireTenantScopedSelect(t, "training_jobs")
	queryLog.requireTenantScopedSelect(t, "dev_workspaces")
}

func TestListGPUAllocationsCanListAllTenants(t *testing.T) {
	repo := testRepository(t)
	createdAt := time.Date(2026, time.August, 24, 12, 0, 0, 0, time.UTC)
	seedGPUAllocationJob(t, repo, JobRecord{ID: "job-own", TenantID: "tenant-a", UserID: "user-a", Name: "job-own", ObservedState: string(domain.StateRunning), CreatedAt: createdAt}, domain.JobSpec{Resources: domain.Resources{WorkerReplicas: 1, GPUsPerWorker: 1}})
	seedGPUAllocationWorkspace(t, repo, WorkspaceRecord{ID: "workspace-other", TenantID: "team-a", UserID: "user-b", Name: "workspace-other", ObservedState: string(domain.WorkspaceRunning), GPUCount: 1, CreatedAt: createdAt})

	got, err := repo.ListGPUAllocations(context.Background(), "tenant-a", true)
	if err != nil {
		t.Fatalf("list all-tenant GPU allocations: %v", err)
	}
	if gotIDs := gpuAllocationIDs(got); !reflect.DeepEqual(gotIDs, []string{"job-own", "workspace-other"}) {
		t.Fatalf("expected allocations across tenant boundaries, got %v", gotIDs)
	}
}

func TestListGPUAllocationsFallsBackToUserIDForMissingUsername(t *testing.T) {
	repo := testRepository(t)
	seedGPUAllocationJob(t, repo, JobRecord{
		ID: "job-fallback", TenantID: "tenant-a", UserID: "missing-user", Name: "job-fallback",
		ObservedState: string(domain.StateRunning), CreatedAt: time.Now().UTC(),
	}, domain.JobSpec{Resources: domain.Resources{WorkerReplicas: 1, GPUsPerWorker: 1}})

	got, err := repo.ListGPUAllocations(context.Background(), "tenant-a", false)
	if err != nil {
		t.Fatalf("list GPU allocations: %v", err)
	}
	if len(got) != 1 || got[0].Username != "missing-user" {
		t.Fatalf("expected user ID fallback, got %+v", got)
	}
}

func TestListGPUAllocationsLoadsUsernamesWithOneBatchQuery(t *testing.T) {
	repo := testRepository(t)
	createdAt := time.Date(2026, time.August, 24, 12, 30, 0, 0, time.UTC)
	seedGPUAllocationUser(t, repo, UserRecord{ID: "user-a", OIDCSubject: "oidc-a", Username: "alice", TenantID: "tenant-a"})
	seedGPUAllocationUser(t, repo, UserRecord{ID: "user-b", OIDCSubject: "oidc-b", Username: "bob", TenantID: "tenant-a"})
	seedGPUAllocationJob(t, repo, JobRecord{ID: "job-a", TenantID: "tenant-a", UserID: "user-a", Name: "job-a", ObservedState: string(domain.StateRunning), CreatedAt: createdAt}, domain.JobSpec{Resources: domain.Resources{WorkerReplicas: 1, GPUsPerWorker: 1}})
	seedGPUAllocationWorkspace(t, repo, WorkspaceRecord{ID: "workspace-b", TenantID: "tenant-a", UserID: "user-b", Name: "workspace-b", ObservedState: string(domain.WorkspaceRunning), GPUCount: 1, CreatedAt: createdAt})

	queryLog := &gpuAllocationQueryLog{}
	scopedRepo := NewGormRepository(repo.db.Session(&gorm.Session{Logger: queryLog}))
	got, err := scopedRepo.ListGPUAllocations(context.Background(), "tenant-a", false)
	if err != nil {
		t.Fatalf("list GPU allocations: %v", err)
	}
	if got[0].Username != "alice" || got[1].Username != "bob" {
		t.Fatalf("unexpected usernames: %+v", got)
	}
	if count := queryLog.selectCount("users"); count != 1 {
		t.Fatalf("expected one batch user query, got %d: %v", count, queryLog.statements)
	}
}

func TestListGPUAllocationsReturnsErrorForMalformedActiveJobSpec(t *testing.T) {
	repo := testRepository(t)
	if err := repo.db.Create(&JobRecord{
		ID: "job-malformed", TenantID: "tenant-a", UserID: "user-a", Name: "job-malformed",
		ObservedState: string(domain.StateRunning), SpecJSON: "{not-json", CreatedAt: time.Now().UTC(),
	}).Error; err != nil {
		t.Fatalf("seed malformed job: %v", err)
	}

	if _, err := repo.ListGPUAllocations(context.Background(), "tenant-a", false); err == nil {
		t.Fatal("expected malformed active job spec to return an error")
	}
}

func seedGPUAllocationUser(t *testing.T, repo *GormRepository, record UserRecord) {
	t.Helper()
	if err := repo.db.Create(&record).Error; err != nil {
		t.Fatalf("seed user %q: %v", record.ID, err)
	}
}

func seedGPUAllocationJob(t *testing.T, repo *GormRepository, record JobRecord, spec domain.JobSpec) {
	t.Helper()
	specJSON, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("marshal job spec for %q: %v", record.ID, err)
	}
	record.SpecJSON = string(specJSON)
	if err := repo.db.Create(&record).Error; err != nil {
		t.Fatalf("seed job %q: %v", record.ID, err)
	}
}

func seedGPUAllocationWorkspace(t *testing.T, repo *GormRepository, record WorkspaceRecord) {
	t.Helper()
	if err := repo.db.Create(&record).Error; err != nil {
		t.Fatalf("seed workspace %q: %v", record.ID, err)
	}
}

func gpuAllocationIDs(allocations []domain.GPUAllocation) []string {
	ids := make([]string, 0, len(allocations))
	for _, allocation := range allocations {
		ids = append(ids, allocation.ID)
	}
	return ids
}

type gpuAllocationQueryLog struct {
	statements []string
}

func (log *gpuAllocationQueryLog) LogMode(logger.LogLevel) logger.Interface      { return log }
func (log *gpuAllocationQueryLog) Info(context.Context, string, ...interface{})  {}
func (log *gpuAllocationQueryLog) Warn(context.Context, string, ...interface{})  {}
func (log *gpuAllocationQueryLog) Error(context.Context, string, ...interface{}) {}

func (log *gpuAllocationQueryLog) Trace(_ context.Context, _ time.Time, trace func() (string, int64), _ error) {
	statement, _ := trace()
	log.statements = append(log.statements, statement)
}

func (log *gpuAllocationQueryLog) selectCount(table string) int {
	count := 0
	for _, statement := range log.statements {
		if strings.Contains(statement, "FROM `"+table+"`") {
			count++
		}
	}
	return count
}

func (log *gpuAllocationQueryLog) requireTenantScopedSelect(t *testing.T, table string) {
	t.Helper()
	for _, statement := range log.statements {
		if strings.Contains(statement, "FROM `"+table+"`") {
			if !strings.Contains(statement, "tenant_id") {
				t.Fatalf("%s query was not tenant-scoped at the database: %s", table, statement)
			}
			return
		}
	}
	t.Fatalf("no %s SELECT captured in %v", table, log.statements)
}
