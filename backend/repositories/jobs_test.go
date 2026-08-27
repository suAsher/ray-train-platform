package repositories

import (
	"context"
	"encoding/json"
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

func observedStateForTest(job domain.TrainingJob, state domain.State) domain.ObservedJobState {
	attempt := job.ClusterAttempt
	if attempt < 1 {
		attempt = 1
	}
	resourceName := job.RayJobName
	if resourceName == "" {
		resourceName = job.ID
	}
	return domain.ObservedJobState{
		ID: job.ID, State: state,
		ExpectedClusterAttempt: attempt, ExpectedRayJobName: job.RayJobName, ExpectedRayJobUID: job.RayJobUID,
		RayJobName: resourceName, RayJobUID: job.RayJobUID,
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
	observed := observedStateForTest(job, domain.StateRunning)
	observed.KubernetesNS, observed.RayJobName, observed.RayJobUID = "tenant-a", "job-1", "uid-1"
	observed.RayClusterName, observed.ResourceVersion = "job-1-cluster", "rv-2"
	if err := repo.ApplyObservedState(context.Background(), observed); err != nil {
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

func TestApplyObservedStateNeverRegressesTerminalLegacyJob(t *testing.T) {
	repo := testRepository(t)
	job := testJob()
	job.Spec.TrainingEngine = domain.TrainingEngineRayDDP
	if err := repo.Create(context.Background(), &job, "terminal-legacy"); err != nil {
		t.Fatal(err)
	}
	failed := observedStateForTest(job, domain.StateFailed)
	failed.Reason = "NONZERO_EXIT"
	if err := repo.ApplyObservedState(context.Background(), failed); err != nil {
		t.Fatal(err)
	}
	stale := observedStateForTest(job, domain.StateRunning)
	stale.RayJobName = "recreated"
	if err := repo.ApplyObservedState(context.Background(), stale); err != nil {
		t.Fatal(err)
	}
	got, err := repo.Get(context.Background(), job.TenantID, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ObservedState != domain.StateFailed || got.RayJobName == "recreated" {
		t.Fatalf("terminal legacy identity was overwritten: %+v", got)
	}
}

func TestApplyObservedStateQueuesOneTerminalExperimentSyncEvent(t *testing.T) {
	repo := testRepository(t)
	job := testJob()
	if err := repo.Create(context.Background(), &job, "terminal-event"); err != nil {
		t.Fatalf("create job: %v", err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		if err := repo.ApplyObservedState(context.Background(), observedStateForTest(job, domain.StateCanceled)); err != nil {
			t.Fatalf("apply terminal state: %v", err)
		}
	}
	var events []OutboxRecord
	if err := repo.db.Where("event_type = ?", "TRAINING_JOB_TERMINAL").Find(&events).Error; err != nil {
		t.Fatalf("list terminal events: %v", err)
	}
	if len(events) != 1 || events[0].AggregateID != job.ID {
		t.Fatalf("expected one terminal event, got %+v", events)
	}
	if !strings.Contains(events[0].PayloadJSON, `"job_id":"job-1"`) {
		t.Fatalf("terminal event payload = %s", events[0].PayloadJSON)
	}
}

func TestApplyObservedStateDoesNotQueueExperimentSyncBeforeTerminalState(t *testing.T) {
	repo := testRepository(t)
	job := testJob()
	if err := repo.Create(context.Background(), &job, "nonterminal-event"); err != nil {
		t.Fatalf("create job: %v", err)
	}
	if err := repo.ApplyObservedState(context.Background(), observedStateForTest(job, domain.StateRunning)); err != nil {
		t.Fatalf("apply running state: %v", err)
	}
	var count int64
	if err := repo.db.Model(&OutboxRecord{}).Where("event_type = ?", "TRAINING_JOB_TERMINAL").Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("non-terminal state queued %d terminal events: %v", count, err)
	}
}

func TestApplyObservedStateDropsStaleAttemptNonTerminalObservation(t *testing.T) {
	repo, job := managedRecoveryJob(t, 2)
	if err := repo.db.Model(&JobRecord{}).Where("id = ?", job.ID).Updates(map[string]any{
		"cluster_attempt": 2,
		"observed_state":  domain.StateRunning,
		"ray_job_name":    job.ID + "-a2",
		"ray_job_uid":     "uid-attempt-2",
	}).Error; err != nil {
		t.Fatal(err)
	}

	err := repo.ApplyObservedState(context.Background(), domain.ObservedJobState{
		ID: job.ID, State: domain.StateProvisioning,
		ExpectedClusterAttempt: 1, ExpectedRayJobName: job.ID, ExpectedRayJobUID: "uid-attempt-1",
		RayJobName: job.ID, RayJobUID: "uid-attempt-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := repo.Get(context.Background(), job.TenantID, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ClusterAttempt != 2 || got.ObservedState != domain.StateRunning || got.RayJobName != job.ID+"-a2" || got.RayJobUID != "uid-attempt-2" {
		t.Fatalf("stale attempt overwrote current nonterminal state: %+v", got)
	}
}

func TestApplyObservedStateDropsStaleAttemptTerminalObservationWithoutOutbox(t *testing.T) {
	repo, job := managedRecoveryJob(t, 2)
	if err := repo.db.Model(&JobRecord{}).Where("id = ?", job.ID).Updates(map[string]any{
		"cluster_attempt": 2,
		"observed_state":  domain.StateRecovering,
		"ray_job_name":    job.ID,
		"ray_job_uid":     "uid-attempt-1",
	}).Error; err != nil {
		t.Fatal(err)
	}

	err := repo.ApplyObservedState(context.Background(), domain.ObservedJobState{
		ID: job.ID, State: domain.StateFailed, Reason: "stale terminal",
		ExpectedClusterAttempt: 1, ExpectedRayJobName: job.ID, ExpectedRayJobUID: "uid-attempt-1",
		RayJobName: job.ID, RayJobUID: "uid-attempt-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := repo.Get(context.Background(), job.TenantID, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ObservedState != domain.StateRecovering || got.ClusterAttempt != 2 {
		t.Fatalf("stale terminal observation overwrote recovery: %+v", got)
	}
	var terminalEvents int64
	if err := repo.db.Model(&OutboxRecord{}).Where("event_type = ?", "TRAINING_JOB_TERMINAL").Count(&terminalEvents).Error; err != nil {
		t.Fatal(err)
	}
	if terminalEvents != 0 {
		t.Fatalf("stale terminal observation emitted %d terminal events", terminalEvents)
	}
}

func TestApplyObservedStateEstablishesOnlyCurrentDeterministicRecoveryIdentity(t *testing.T) {
	repo, job := managedRecoveryJob(t, 2)
	if err := repo.db.Model(&JobRecord{}).Where("id = ?", job.ID).Updates(map[string]any{
		"cluster_attempt": 2,
		"observed_state":  domain.StateRecovering,
		"ray_job_name":    "",
		"ray_job_uid":     "",
	}).Error; err != nil {
		t.Fatal(err)
	}

	wrong := domain.ObservedJobState{
		ID: job.ID, State: domain.StateRunning,
		ExpectedClusterAttempt: 2,
		RayJobName:             job.ID + "-wrong", RayJobUID: "uid-wrong",
	}
	if err := repo.ApplyObservedState(context.Background(), wrong); err != nil {
		t.Fatal(err)
	}
	current, err := repo.Get(context.Background(), job.TenantID, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.ObservedState != domain.StateRecovering || current.RayJobName != "" || current.RayJobUID != "" {
		t.Fatalf("non-deterministic recovery identity was adopted: %+v", current)
	}

	correct := wrong
	correct.RayJobName = job.ID + "-a2"
	correct.RayJobUID = "uid-attempt-2"
	if err := repo.ApplyObservedState(context.Background(), correct); err != nil {
		t.Fatal(err)
	}
	current, err = repo.Get(context.Background(), job.TenantID, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.ObservedState != domain.StateRunning || current.RayJobName != job.ID+"-a2" || current.RayJobUID != "uid-attempt-2" {
		t.Fatalf("current deterministic recovery identity was not established: %+v", current)
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
	if err := repo.ApplyObservedState(context.Background(), observedStateForTest(job, domain.StateSucceeded)); err != nil {
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

func TestCreateRoundTripsManagedRuntimeMetadata(t *testing.T) {
	repo := testRepository(t)
	job := testJob()
	job.Spec.TrainingEngine = domain.TrainingEngineRayTrain
	job.Spec.RayVersion = domain.RayVersionProduction
	job.Spec.ParentJobID = "job-0123456789abcdef01234567"
	job.ClusterAttempt = 2
	job.WorkerRestartCount = 3
	job.ResumeCheckpointID = "checkpoint-7"
	if err := repo.Create(context.Background(), &job, "runtime-metadata"); err != nil {
		t.Fatalf("create managed runtime job: %v", err)
	}

	var record JobRecord
	if err := repo.db.Where("id = ?", job.ID).First(&record).Error; err != nil {
		t.Fatalf("load managed runtime record: %v", err)
	}
	if record.TrainingEngine != string(domain.TrainingEngineRayTrain) || record.RayVersion != domain.RayVersionProduction || record.ParentJobID != job.Spec.ParentJobID || record.ClusterAttempt != 2 || record.WorkerRestartCount != 3 || record.ResumeCheckpointID != "checkpoint-7" {
		t.Fatalf("normalized runtime metadata not persisted: %+v", record)
	}

	got, err := repo.Get(context.Background(), job.TenantID, job.ID)
	if err != nil {
		t.Fatalf("get managed runtime job: %v", err)
	}
	if got.Spec.TrainingEngine != job.Spec.TrainingEngine || got.Spec.RayVersion != job.Spec.RayVersion || got.Spec.ParentJobID != job.Spec.ParentJobID || got.ClusterAttempt != 2 || got.WorkerRestartCount != 3 || got.ResumeCheckpointID != "checkpoint-7" {
		t.Fatalf("managed runtime metadata did not round-trip: %+v", got)
	}
}

func TestCreatePersistsResolvedRuntimeMetadataDefaults(t *testing.T) {
	repo := testRepository(t)
	job := testJob()
	if err := repo.Create(context.Background(), &job, "runtime-defaults"); err != nil {
		t.Fatalf("create legacy runtime job: %v", err)
	}

	var record JobRecord
	if err := repo.db.Where("id = ?", job.ID).First(&record).Error; err != nil {
		t.Fatalf("load legacy runtime record: %v", err)
	}
	if record.TrainingEngine != string(domain.TrainingEngineRayDDP) || record.RayVersion != domain.RayVersionLegacy || record.ClusterAttempt != 1 {
		t.Fatalf("resolved runtime defaults not persisted: %+v", record)
	}
}

func TestCreateNormalizesRuntimeMetadataConsistently(t *testing.T) {
	repo := testRepository(t)
	job := testJob()
	if err := repo.Create(context.Background(), &job, "runtime-consistency"); err != nil {
		t.Fatalf("create legacy runtime job: %v", err)
	}

	if job.Spec.TrainingEngine != domain.TrainingEngineRayDDP || job.Spec.RayVersion != domain.RayVersionLegacy || job.ClusterAttempt != 1 {
		t.Fatalf("created job was not normalized in memory: %+v", job)
	}

	var record JobRecord
	if err := repo.db.Where("id = ?", job.ID).First(&record).Error; err != nil {
		t.Fatalf("load legacy runtime record: %v", err)
	}
	var storedSpec domain.JobSpec
	if err := json.Unmarshal([]byte(record.SpecJSON), &storedSpec); err != nil {
		t.Fatalf("decode stored job spec: %v", err)
	}
	if storedSpec.TrainingEngine != domain.TrainingEngineRayDDP || storedSpec.RayVersion != domain.RayVersionLegacy {
		t.Fatalf("spec_json was not normalized: %+v", storedSpec)
	}
	if record.TrainingEngine != string(domain.TrainingEngineRayDDP) || record.RayVersion != domain.RayVersionLegacy || record.ClusterAttempt != 1 {
		t.Fatalf("normalized columns disagree with created job: %+v", record)
	}

	got, err := repo.Get(context.Background(), job.TenantID, job.ID)
	if err != nil {
		t.Fatalf("get legacy runtime job: %v", err)
	}
	if got.Spec.TrainingEngine != job.Spec.TrainingEngine || got.Spec.RayVersion != job.Spec.RayVersion || got.ClusterAttempt != job.ClusterAttempt {
		t.Fatalf("read job disagrees with create response: created=%+v read=%+v", job, got)
	}
}

func managedRecoveryJob(t *testing.T, maxFailures int) (*GormRepository, domain.TrainingJob) {
	t.Helper()
	repo := testRepository(t)
	if err := repo.db.AutoMigrate(&TrainingCheckpointRecord{}); err != nil {
		t.Fatalf("migrate checkpoints: %v", err)
	}
	job := testJob()
	job.Spec.TrainingEngine = domain.TrainingEngineRayTrain
	job.Spec.RayVersion = domain.RayVersionProduction
	job.Spec.Managed.MaxFailures = maxFailures
	job.Spec.ResolvedDataMounts.Output = &domain.ResolvedDataMount{
		Space: domain.DataSpaceMyRuns, BindingSpace: domain.DataSpaceWorkspace,
		ClaimName: "tenant-data", SubPath: "tenants/tenant-a/users/user-a/runs/job-1",
		MountPath: domain.DataMountOutputPath, ReadOnly: false,
	}
	job.ObservedState = domain.StateRunning
	job.ClusterAttempt = 1
	job.RayJobName = job.ID
	job.RayJobUID = "uid-attempt-1"
	if err := repo.Create(context.Background(), &job, "managed-recovery"); err != nil {
		t.Fatalf("create managed recovery job: %v", err)
	}
	if err := repo.db.Model(&JobRecord{}).Where("id = ?", job.ID).Updates(map[string]any{
		"ray_job_name": job.RayJobName, "ray_job_uid": job.RayJobUID,
	}).Error; err != nil {
		t.Fatalf("persist managed recovery identity: %v", err)
	}
	return repo, job
}

func persistRecoveryCheckpoint(t *testing.T, repo *GormRepository, job domain.TrainingJob, id string, complete bool) {
	t.Helper()
	record := TrainingCheckpointRecord{
		JobID: job.ID, ID: id, TenantID: job.TenantID, UserID: job.UserID,
		Epoch: 4, Step: 40, Complete: complete,
		ObjectPath:     domain.DataMountOutputPath + "/.platform/ray-train/" + job.ID + "/checkpoints/" + id,
		ManifestSHA256: strings.Repeat("a", 64), CreatedAt: time.Now().UTC(),
	}
	if err := repo.db.Create(&record).Error; err != nil {
		t.Fatalf("persist recovery checkpoint: %v", err)
	}
}

func managedRecoveryRequest(job domain.TrainingJob, failureClass string) domain.ManagedRecoveryRequest {
	return domain.ManagedRecoveryRequest{
		JobID: job.ID, ExpectedClusterAttempt: job.ClusterAttempt,
		ExpectedRayJobName: job.RayJobName, ExpectedRayJobUID: job.RayJobUID,
		FailureClass: failureClass,
	}
}

func TestBeginManagedRecoverySnapshotsLatestCompleteCheckpointAndCASAttempt(t *testing.T) {
	repo, job := managedRecoveryJob(t, 2)
	persistRecoveryCheckpoint(t, repo, job, "checkpoint-4", true)
	if err := repo.db.Create(&TrainingCheckpointRecord{
		JobID: job.ID, ID: "newer-invalid", TenantID: job.TenantID, UserID: job.UserID,
		Epoch: 5, Step: 50, Complete: true,
		ObjectPath:     domain.DataMountOutputPath + "/.platform/ray-train/" + job.ID + "/checkpoints/nested/newer-invalid",
		ManifestSHA256: strings.Repeat("b", 64), CreatedAt: time.Now().UTC().Add(time.Second),
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := repo.db.Model(&JobRecord{}).Where("id = ?", job.ID).Update("resume_checkpoint_id", "caller-supplied-unverified").Error; err != nil {
		t.Fatal(err)
	}

	request := managedRecoveryRequest(job, "HEAD_POD_LOST")
	request.FailureMessage = "head node disappeared"
	recovered, transitioned, err := repo.BeginManagedRecovery(context.Background(), request)
	if err != nil {
		t.Fatalf("begin managed recovery: %v", err)
	}
	if !transitioned || recovered.ClusterAttempt != 2 || recovered.ObservedState != domain.StateRecovering {
		t.Fatalf("unexpected recovery transition: transitioned=%v job=%+v", transitioned, recovered)
	}
	if recovered.ResumeCheckpointID != "checkpoint-4" || recovered.RayJobName != job.ID || recovered.RayJobUID != "uid-attempt-1" {
		t.Fatalf("recovery did not snapshot checkpoint while preserving retiring identity: %+v", recovered)
	}
	if recovered.StatusReason != "HEAD_POD_LOST" || recovered.StatusMessage != "head node disappeared" {
		t.Fatalf("recovery failure provenance was not preserved: %+v", recovered)
	}

	duplicateRequest := managedRecoveryRequest(job, "HEAD_POD_LOST")
	duplicateRequest.FailureMessage = "duplicate replica"
	duplicate, transitioned, err := repo.BeginManagedRecovery(context.Background(), duplicateRequest)
	if err != nil {
		t.Fatalf("duplicate recovery: %v", err)
	}
	if transitioned || duplicate.ClusterAttempt != 2 || duplicate.ResumeCheckpointID != "checkpoint-4" {
		t.Fatalf("stale replica advanced another attempt: transitioned=%v job=%+v", transitioned, duplicate)
	}
}

func TestClearManagedRecoveryRetiringIdentityUsesAttemptNameAndUIDCAS(t *testing.T) {
	repo, job := managedRecoveryJob(t, 2)
	persistRecoveryCheckpoint(t, repo, job, "checkpoint-4", true)
	recovered, transitioned, err := repo.BeginManagedRecovery(context.Background(), managedRecoveryRequest(job, "HEAD_POD_LOST"))
	if err != nil || !transitioned {
		t.Fatalf("begin recovery: transitioned=%v err=%v", transitioned, err)
	}

	for _, stale := range []domain.ManagedRetiringIdentityRequest{
		{JobID: job.ID, ExpectedClusterAttempt: 1, RayJobName: job.ID, RayJobUID: "uid-attempt-1"},
		{JobID: job.ID, ExpectedClusterAttempt: 2, RayJobName: job.ID + "-wrong", RayJobUID: "uid-attempt-1"},
		{JobID: job.ID, ExpectedClusterAttempt: 2, RayJobName: job.ID, RayJobUID: "uid-wrong"},
	} {
		current, cleared, clearErr := repo.ClearManagedRecoveryRetiringIdentity(context.Background(), stale)
		if clearErr != nil {
			t.Fatal(clearErr)
		}
		if cleared || current.RayJobName != job.ID || current.RayJobUID != "uid-attempt-1" {
			t.Fatalf("stale retirement CAS cleared identity: request=%+v job=%+v", stale, current)
		}
	}

	current, cleared, err := repo.ClearManagedRecoveryRetiringIdentity(context.Background(), domain.ManagedRetiringIdentityRequest{
		JobID: job.ID, ExpectedClusterAttempt: recovered.ClusterAttempt,
		RayJobName: recovered.RayJobName, RayJobUID: recovered.RayJobUID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !cleared || current.ClusterAttempt != 2 || current.ObservedState != domain.StateRecovering || current.RayJobName != "" || current.RayJobUID != "" {
		t.Fatalf("current retiring identity was not cleared: cleared=%v job=%+v", cleared, current)
	}
	var terminalEvents int64
	if err := repo.db.Model(&OutboxRecord{}).Where("event_type = ?", "TRAINING_JOB_TERMINAL").Count(&terminalEvents).Error; err != nil {
		t.Fatal(err)
	}
	if terminalEvents != 0 {
		t.Fatalf("recovery retirement emitted %d terminal events", terminalEvents)
	}
}

func TestClearManagedRecoveryRetiringIdentityLosesToCancellation(t *testing.T) {
	repo, job := managedRecoveryJob(t, 2)
	persistRecoveryCheckpoint(t, repo, job, "checkpoint-4", true)
	recovered, transitioned, err := repo.BeginManagedRecovery(context.Background(), managedRecoveryRequest(job, "HEAD_POD_LOST"))
	if err != nil || !transitioned {
		t.Fatalf("begin recovery: transitioned=%v err=%v", transitioned, err)
	}
	if err := repo.SetDesiredState(context.Background(), job.TenantID, job.ID, domain.DesiredCanceled); err != nil {
		t.Fatal(err)
	}
	current, cleared, err := repo.ClearManagedRecoveryRetiringIdentity(context.Background(), domain.ManagedRetiringIdentityRequest{
		JobID: job.ID, ExpectedClusterAttempt: recovered.ClusterAttempt,
		RayJobName: recovered.RayJobName, RayJobUID: recovered.RayJobUID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if cleared || current.DesiredState != domain.DesiredCanceled || current.RayJobName != job.ID || current.RayJobUID != "uid-attempt-1" {
		t.Fatalf("retirement clear overrode cancellation: cleared=%v job=%+v", cleared, current)
	}
}

func TestBeginManagedRecoveryMaxFailuresCountsRetriesAfterInitialAttempt(t *testing.T) {
	repo, job := managedRecoveryJob(t, 2)
	persistRecoveryCheckpoint(t, repo, job, "checkpoint-4", true)

	current := job
	for _, want := range []int{2, 3} {
		recovered, transitioned, err := repo.BeginManagedRecovery(context.Background(), managedRecoveryRequest(current, "RAY_CLUSTER_UNAVAILABLE"))
		if err != nil || !transitioned || recovered.ClusterAttempt != want {
			t.Fatalf("attempt %d: transitioned=%v recovered=%+v err=%v", want, transitioned, recovered, err)
		}
		cleared, didClear, err := repo.ClearManagedRecoveryRetiringIdentity(context.Background(), domain.ManagedRetiringIdentityRequest{
			JobID: job.ID, ExpectedClusterAttempt: recovered.ClusterAttempt,
			RayJobName: recovered.RayJobName, RayJobUID: recovered.RayJobUID,
		})
		if err != nil || !didClear {
			t.Fatalf("clear attempt %d retiring identity: cleared=%v err=%v", want, didClear, err)
		}
		name, uid := job.ID+"-a"+fmt.Sprint(want), "uid-attempt-"+fmt.Sprint(want)
		if err := repo.db.Model(&JobRecord{}).Where("id = ?", job.ID).Updates(map[string]any{
			"observed_state": domain.StateRunning, "ray_job_name": name, "ray_job_uid": uid,
		}).Error; err != nil {
			t.Fatal(err)
		}
		current = *cleared
		current.ObservedState, current.RayJobName, current.RayJobUID = domain.StateRunning, name, uid
	}
	exhausted, transitioned, err := repo.BeginManagedRecovery(context.Background(), managedRecoveryRequest(current, "RAY_CLUSTER_UNAVAILABLE"))
	if err != nil {
		t.Fatalf("exhausted recovery: %v", err)
	}
	if transitioned || exhausted.ClusterAttempt != 3 {
		t.Fatalf("maxFailures=2 must permit exactly attempts 2 and 3: transitioned=%v job=%+v", transitioned, exhausted)
	}
}

func TestBeginManagedRecoveryRequiresUsableOwnedCheckpointAndActiveDesire(t *testing.T) {
	tests := []struct {
		name       string
		checkpoint func(*testing.T, *GormRepository, domain.TrainingJob)
		cancel     bool
	}{
		{name: "missing"},
		{name: "incomplete", checkpoint: func(t *testing.T, repo *GormRepository, job domain.TrainingJob) {
			persistRecoveryCheckpoint(t, repo, job, "checkpoint-4", false)
		}},
		{name: "wrong owner", checkpoint: func(t *testing.T, repo *GormRepository, job domain.TrainingJob) {
			persistRecoveryCheckpoint(t, repo, job, "checkpoint-4", true)
			if err := repo.db.Model(&TrainingCheckpointRecord{}).Where("job_id = ?", job.ID).Update("user_id", "other-user").Error; err != nil {
				t.Fatal(err)
			}
		}},
		{name: "canceled", checkpoint: func(t *testing.T, repo *GormRepository, job domain.TrainingJob) {
			persistRecoveryCheckpoint(t, repo, job, "checkpoint-4", true)
		}, cancel: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repo, job := managedRecoveryJob(t, 2)
			if test.checkpoint != nil {
				test.checkpoint(t, repo, job)
			}
			if test.cancel {
				if err := repo.SetDesiredState(context.Background(), job.TenantID, job.ID, domain.DesiredCanceled); err != nil {
					t.Fatal(err)
				}
			}
			recovered, transitioned, err := repo.BeginManagedRecovery(context.Background(), managedRecoveryRequest(job, "DRIVER_POD_LOST"))
			if err != nil {
				t.Fatalf("begin recovery: %v", err)
			}
			if transitioned || recovered.ClusterAttempt != 1 || recovered.ObservedState != domain.StateRunning {
				t.Fatalf("unusable recovery input changed job: transitioned=%v job=%+v", transitioned, recovered)
			}
		})
	}
}

func TestBeginManagedRecoveryRequiresGovernedWritableOutputMount(t *testing.T) {
	repo, job := managedRecoveryJob(t, 2)
	persistRecoveryCheckpoint(t, repo, job, "checkpoint-4", true)
	job.Spec.ResolvedDataMounts.Output = nil
	specJSON, err := json.Marshal(job.Spec)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.db.Model(&JobRecord{}).Where("id = ?", job.ID).Update("spec_json", string(specJSON)).Error; err != nil {
		t.Fatal(err)
	}

	recovered, transitioned, err := repo.BeginManagedRecovery(context.Background(), managedRecoveryRequest(job, "HEAD_POD_LOST"))
	if err != nil {
		t.Fatal(err)
	}
	if transitioned || recovered.ClusterAttempt != 1 || recovered.ObservedState != domain.StateRunning {
		t.Fatalf("checkpoint without governed output mount was treated as usable: transitioned=%v job=%+v", transitioned, recovered)
	}
}

func TestBeginManagedRecoveryRequiresCompleteRetiringIdentity(t *testing.T) {
	repo, job := managedRecoveryJob(t, 2)
	persistRecoveryCheckpoint(t, repo, job, "checkpoint-4", true)
	if err := repo.db.Model(&JobRecord{}).Where("id = ?", job.ID).Update("ray_job_uid", "").Error; err != nil {
		t.Fatal(err)
	}
	job.RayJobUID = ""
	if _, _, err := repo.BeginManagedRecovery(context.Background(), managedRecoveryRequest(job, "HEAD_POD_LOST")); err == nil {
		t.Fatal("recovery accepted an incomplete retiring identity")
	}
}

func TestRecoveringJobRemainsAnActiveReconcileAndQuotaCandidate(t *testing.T) {
	repo, job := managedRecoveryJob(t, 2)
	if err := repo.db.Model(&JobRecord{}).Where("id = ?", job.ID).Update("observed_state", domain.StateRecovering).Error; err != nil {
		t.Fatal(err)
	}
	ids, err := repo.ListReconcileCandidates(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != job.ID {
		t.Fatalf("RECOVERING was treated as terminal: %v", ids)
	}
	quota, err := repo.TenantGPUQuota(context.Background(), job.TenantID)
	if err != nil {
		t.Fatal(err)
	}
	want := job.Spec.Resources.WorkerReplicas * job.Spec.Resources.GPUsPerWorker
	if quota.GPUUsed != want {
		t.Fatalf("RECOVERING job released quota early: used=%d want=%d", quota.GPUUsed, want)
	}
}
