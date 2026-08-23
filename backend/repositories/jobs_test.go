package repositories

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"ray-train-platform-backend/domain"
)

func testRepository(t *testing.T) *GormRepository {
	t.Helper()
	database, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	if err := database.AutoMigrate(&JobRecord{}, &OutboxRecord{}, &TenantRecord{}, &UserRecord{}, &WorkspaceRecord{}, &IdempotencyRecord{}); err != nil {
		t.Fatalf("migrate test database: %v", err)
	}
	now := time.Now().UTC()
	tenants := []TenantRecord{
		{ID: "tenant-a", Name: "tenant-a", Namespace: "tenant-tenant-a", LocalQueue: "tenant-a-gpu", GPUQuotaLimit: defaultTenantGPUQuota(), MaxPriority: "normal", CreatedAt: now, UpdatedAt: now},
		{ID: "team-a", Name: "team-a", Namespace: "tenant-team-a", LocalQueue: "team-a-gpu", GPUQuotaLimit: defaultTenantGPUQuota(), MaxPriority: "normal", CreatedAt: now, UpdatedAt: now},
	}
	if err := database.Create(&tenants).Error; err != nil {
		t.Fatalf("seed controlled test tenants: %v", err)
	}
	return NewGormRepository(database)
}

func testJob() domain.TrainingJob {
	return domain.TrainingJob{
		ID:            "job-1",
		TenantID:      "tenant-a",
		UserID:        "user-a",
		Spec:          domain.JobSpec{Name: "job-1", Image: "registry/ray@sha256:" + strings.Repeat("a", 64), Source: domain.CodeSource{Type: "git", URL: "https://git.example/train", Commit: "abc1234"}, Entrypoint: domain.Entrypoint{Command: []string{"python", "train.py"}}, Resources: domain.Resources{WorkerReplicas: 1, GPUsPerWorker: 1}, Queue: "tenant-a-queue"},
		DesiredState:  domain.DesiredActive,
		ObservedState: domain.StateSubmitted,
		KubernetesNS:  "tenant-a",
	}
}

func TestCreateWritesJobAndOutboxAtomically(t *testing.T) {
	repo := testRepository(t)
	job := testJob()
	if err := repo.Create(context.Background(), &job, "request-1"); err != nil {
		t.Fatalf("create job: %v", err)
	}
	got, err := repo.Get(context.Background(), "tenant-a", "job-1")
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if got.Spec.Name != "job-1" || got.TenantID != "tenant-a" {
		t.Fatalf("unexpected job: %+v", got)
	}
	var count int64
	if err := repo.db.Model(&OutboxRecord{}).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("expected one outbox record, count=%d err=%v", count, err)
	}
}

func TestCreateRejectsDuplicateIdempotencyKeyWithOriginalJob(t *testing.T) {
	repo := testRepository(t)
	first := testJob()
	if err := repo.Create(context.Background(), &first, "same-request"); err != nil {
		t.Fatalf("create first job: %v", err)
	}
	second := testJob()
	second.ID = "job-2"
	err := repo.Create(context.Background(), &second, "same-request")
	var conflict *IdempotencyConflictError
	if !errors.As(err, &conflict) || conflict.JobID != "job-1" {
		t.Fatalf("expected idempotency conflict for job-1, got %v", err)
	}
}

func TestGetDoesNotCrossTenantBoundary(t *testing.T) {
	repo := testRepository(t)
	job := testJob()
	if err := repo.Create(context.Background(), &job, "request-2"); err != nil {
		t.Fatalf("create job: %v", err)
	}
	if _, err := repo.Get(context.Background(), "tenant-b", "job-1"); err == nil {
		t.Fatal("expected tenant boundary error")
	}
}

func TestListExcludesArchivedJobsButGetRetainsAuditRecord(t *testing.T) {
	repo := testRepository(t)
	job := testJob()
	if err := repo.Create(context.Background(), &job, "request-archive"); err != nil {
		t.Fatalf("create job: %v", err)
	}
	archivedAt := time.Now().UTC()
	if err := repo.db.Model(&JobRecord{}).Where("id = ?", job.ID).Update("archived_at", archivedAt).Error; err != nil {
		t.Fatalf("archive job: %v", err)
	}
	page, err := repo.List(context.Background(), domain.JobFilter{TenantID: job.TenantID})
	if err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	if page.Total != 0 || len(page.Items) != 0 {
		t.Fatalf("archived job leaked into the default list: total=%d items=%d", page.Total, len(page.Items))
	}
	if _, err := repo.Get(context.Background(), job.TenantID, job.ID); err != nil {
		t.Fatalf("archived audit record should remain addressable: %v", err)
	}
}

func TestListAllTenantsReturnsActiveJobsAcrossTenantBoundaries(t *testing.T) {
	repo := testRepository(t)
	first := testJob()
	if err := repo.Create(context.Background(), &first, "request-list-all-a"); err != nil {
		t.Fatalf("create first job: %v", err)
	}
	second := testJob()
	second.ID = "job-team-a"
	second.TenantID = "team-a"
	second.Spec.Name = second.ID
	second.Spec.Queue = "team-a-gpu"
	if err := repo.Create(context.Background(), &second, "request-list-all-b"); err != nil {
		t.Fatalf("create second job: %v", err)
	}

	page, err := repo.List(context.Background(), domain.JobFilter{AllTenants: true})
	if err != nil {
		t.Fatalf("list all tenant jobs: %v", err)
	}
	if page.Total != 2 || len(page.Items) != 2 {
		t.Fatalf("expected both tenant jobs, got total=%d items=%+v", page.Total, page.Items)
	}
}

func TestApplyObservedStateUpdatesKubernetesReferences(t *testing.T) {
	repo := testRepository(t)
	job := testJob()
	if err := repo.Create(context.Background(), &job, "request-3"); err != nil {
		t.Fatalf("create job: %v", err)
	}
	if err := repo.ApplyObservedState(context.Background(), domain.ObservedJobState{ID: "job-1", State: domain.StateRunning, KubernetesNS: "tenant-a", RayJobName: "job-1", RayJobUID: "uid-1", RayClusterName: "job-1-cluster", ResourceVersion: "rv-2"}); err != nil {
		t.Fatalf("apply observed state: %v", err)
	}
	got, err := repo.Get(context.Background(), "tenant-a", "job-1")
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if got.ObservedState != domain.StateRunning || got.RayJobUID != "uid-1" || got.RayJobName != "job-1" || got.ResourceVersion != "rv-2" {
		t.Fatalf("unexpected observed job: %+v", got)
	}
}

func TestCancelCreatesIdempotentOutboxEvent(t *testing.T) {
	repo := testRepository(t)
	job := testJob()
	if err := repo.Create(context.Background(), &job, "request-cancel"); err != nil {
		t.Fatalf("create job: %v", err)
	}
	if err := repo.SetDesiredState(context.Background(), "tenant-a", "job-1", domain.DesiredCanceled); err != nil {
		t.Fatalf("cancel job: %v", err)
	}
	if err := repo.SetDesiredState(context.Background(), "tenant-a", "job-1", domain.DesiredCanceled); err != nil {
		t.Fatalf("repeat cancel job: %v", err)
	}
	var count int64
	if err := repo.db.Model(&OutboxRecord{}).Where("event_type = ?", "TRAINING_JOB_CANCEL_REQUESTED").Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("expected one cancel event, count=%d err=%v", count, err)
	}
}

func TestListReconcileCandidatesExcludesTerminalActiveJobs(t *testing.T) {
	repo := testRepository(t)
	job := testJob()
	if err := repo.Create(context.Background(), &job, "request-list"); err != nil {
		t.Fatalf("create job: %v", err)
	}
	if err := repo.ApplyObservedState(context.Background(), domain.ObservedJobState{ID: "job-1", State: domain.StateSucceeded}); err != nil {
		t.Fatalf("mark succeeded: %v", err)
	}
	ids, err := repo.ListReconcileCandidates(context.Background(), 10)
	if err != nil {
		t.Fatalf("list candidates: %v", err)
	}
	if len(ids) != 0 {
		t.Fatalf("expected no candidates, got %v", ids)
	}
}

func TestCreateRoundTripsArtifactSubmissionMetadata(t *testing.T) {
	repo := testRepository(t)
	job := testJob()
	job.SourceArtifactID = "artifact-1"
	job.SubmissionOrigin = domain.SubmissionOriginRayCLI
	job.ExternalSubmissionID = "ray-submission-1"
	if err := repo.Create(context.Background(), &job, "artifact-roundtrip"); err != nil {
		t.Fatalf("create artifact-backed job: %v", err)
	}
	got, err := repo.Get(context.Background(), job.TenantID, job.ID)
	if err != nil {
		t.Fatalf("get artifact-backed job: %v", err)
	}
	if got.SourceArtifactID != job.SourceArtifactID || got.SubmissionOrigin != job.SubmissionOrigin || got.ExternalSubmissionID != job.ExternalSubmissionID {
		t.Fatalf("artifact submission metadata did not round-trip: artifact=%q origin=%q external=%q", got.SourceArtifactID, got.SubmissionOrigin, got.ExternalSubmissionID)
	}
	page, err := repo.List(context.Background(), domain.JobFilter{TenantID: job.TenantID})
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("list artifact-backed jobs: count=%d err=%v", len(page.Items), err)
	}
	listed := page.Items[0]
	if listed.SourceArtifactID != job.SourceArtifactID || listed.SubmissionOrigin != job.SubmissionOrigin || listed.ExternalSubmissionID != job.ExternalSubmissionID {
		t.Fatalf("list metadata did not round-trip: artifact=%q origin=%q external=%q", listed.SourceArtifactID, listed.SubmissionOrigin, listed.ExternalSubmissionID)
	}
}
