package repositories

import (
	"context"
	"crypto/rand"
	"errors"
	"testing"
	"time"

	"ray-train-platform-backend/domain"
)

func managedCheckpointRepository(t *testing.T) (*GormRepository, domain.TrainingJob) {
	t.Helper()
	repository := testRepository(t)
	if err := repository.db.AutoMigrate(&TrainingCheckpointRecord{}, &TrainingJobEventTokenRecord{}, &TrainingJobEventRecord{}); err != nil {
		t.Fatalf("migrate checkpoint records: %v", err)
	}
	job := testJob()
	job.ID = "job-0123456789abcdef01234567"
	job.Spec.TrainingEngine = domain.TrainingEngineRayTrain
	job.Spec.RayVersion = domain.RayVersionProduction
	job.Spec.ResolvedDataMounts.Output = &domain.ResolvedDataMount{
		Space: domain.DataSpaceMyRuns, BindingSpace: domain.DataSpaceWorkspace,
		ClaimName: "data-tenant-a", SubPath: "tenants/tenant-a/users/user-a/runs/managed",
		MountPath: domain.DataMountOutputPath,
	}
	if err := repository.Create(context.Background(), &job, "managed-checkpoint-job"); err != nil {
		t.Fatalf("create managed job: %v", err)
	}
	return repository, job
}

func randomJobToken(t *testing.T) []byte {
	t.Helper()
	token := make([]byte, 32)
	if _, err := rand.Read(token); err != nil {
		t.Fatalf("generate token: %v", err)
	}
	return token
}

func TestRecordTrainingEventRejectsAnotherJobsToken(t *testing.T) {
	repository, job := managedCheckpointRepository(t)
	other := testJob()
	other.ID = "job-abcdef0123456789abcdef01"
	other.Spec.TrainingEngine = domain.TrainingEngineRayTrain
	other.Spec.RayVersion = domain.RayVersionProduction
	if err := repository.Create(context.Background(), &other, "other-managed-job"); err != nil {
		t.Fatal(err)
	}
	jobToken := randomJobToken(t)
	otherToken := randomJobToken(t)
	if err := repository.EnsureTrainingEventToken(context.Background(), job.ID, jobToken, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := repository.EnsureTrainingEventToken(context.Background(), other.ID, otherToken, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	_, err := repository.RecordTrainingEvent(context.Background(), job.ID, otherToken, domain.TrainingEvent{ID: "event-1", Type: domain.TrainingEventWorkerGroupStarted, Generation: 1}, time.Now())
	if !errors.Is(err, ErrTrainingEventUnauthorized) {
		t.Fatalf("expected unauthorized token, got %v", err)
	}
}

func TestRecordTrainingEventTreatsFirstWorkerGenerationAsInitialObservation(t *testing.T) {
	repository, job := managedCheckpointRepository(t)
	token := randomJobToken(t)
	now := time.Now().UTC()
	if err := repository.EnsureTrainingEventToken(context.Background(), job.ID, token, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	event := domain.TrainingEvent{ID: "worker-generation-1", Type: domain.TrainingEventWorkerGroupStarted, Generation: 1}
	first, err := repository.RecordTrainingEvent(context.Background(), job.ID, token, event, now)
	if err != nil {
		t.Fatal(err)
	}
	second, err := repository.RecordTrainingEvent(context.Background(), job.ID, token, event, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if first.EventID != second.EventID || first.WorkerRestartCount != second.WorkerRestartCount || first.Replayed || !second.Replayed || first.WorkerRestartCount != 0 {
		t.Fatalf("replay changed result: first=%+v second=%+v", first, second)
	}
	stored, err := repository.GetByID(context.Background(), job.ID)
	if err != nil || stored.WorkerRestartCount != 0 {
		t.Fatalf("worker restart count=%d err=%v", stored.WorkerRestartCount, err)
	}

	secondGeneration, err := repository.RecordTrainingEvent(context.Background(), job.ID, token, domain.TrainingEvent{ID: "worker-generation-2", Type: domain.TrainingEventWorkerGroupStarted, Generation: 2}, now.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if secondGeneration.WorkerRestartCount != 1 {
		t.Fatalf("second generation restart count=%d, want 1", secondGeneration.WorkerRestartCount)
	}
	stored, _ = repository.GetByID(context.Background(), job.ID)
	if stored.WorkerRestartCount != 1 {
		t.Fatalf("second generation did not increment restart count: %d", stored.WorkerRestartCount)
	}
}

func TestRecordTrainingEventRejectsStaleGenerationForProgressAndCheckpoint(t *testing.T) {
	repository, job := managedCheckpointRepository(t)
	token := randomJobToken(t)
	now := time.Now().UTC()
	if err := repository.EnsureTrainingEventToken(context.Background(), job.ID, token, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.RecordTrainingEvent(context.Background(), job.ID, token, domain.TrainingEvent{
		ID: "worker-generation-3", Type: domain.TrainingEventWorkerGroupStarted, Generation: 3,
	}, now); err != nil {
		t.Fatal(err)
	}

	progress := domain.TrainingEvent{ID: "stale-progress", Type: domain.TrainingEventProgress, Generation: 2, Epoch: 1, Step: 1}
	if _, err := repository.RecordTrainingEvent(context.Background(), job.ID, token, progress, now.Add(time.Second)); !errors.Is(err, ErrTrainingEventInvalid) {
		t.Fatalf("expected stale progress generation rejection, got %v", err)
	}
	checkpoint := &domain.TrainingCheckpoint{
		ID: "stale-checkpoint", Epoch: 1, Step: 2,
		ObjectPath:     "/mnt/data/output/.platform/ray-train/" + job.ID + "/checkpoints/stale",
		ManifestSHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		Complete:       true,
	}
	checkpointEvent := domain.TrainingEvent{ID: "stale-checkpoint-event", Type: domain.TrainingEventCheckpointComplete, Generation: 2, Epoch: 1, Step: 2, Checkpoint: checkpoint}
	if _, err := repository.RecordTrainingEvent(context.Background(), job.ID, token, checkpointEvent, now.Add(2*time.Second)); !errors.Is(err, ErrTrainingEventInvalid) {
		t.Fatalf("expected stale checkpoint generation rejection, got %v", err)
	}
}

func TestRecordTrainingEventRejectsOmittedGenerationBeforePersistence(t *testing.T) {
	repository, job := managedCheckpointRepository(t)
	token := randomJobToken(t)
	now := time.Now().UTC()
	if err := repository.EnsureTrainingEventToken(context.Background(), job.ID, token, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	checkpoint := &domain.TrainingCheckpoint{
		ID: "checkpoint-no-generation", Epoch: 1, Step: 2,
		ObjectPath:     "/mnt/data/output/.platform/ray-train/" + job.ID + "/checkpoints/no-generation",
		ManifestSHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		Complete:       true,
	}
	for _, event := range []domain.TrainingEvent{
		{ID: "progress-no-generation", Type: domain.TrainingEventProgress, Epoch: 1, Step: 1},
		{ID: "checkpoint-no-generation", Type: domain.TrainingEventCheckpointComplete, Epoch: 1, Step: 2, Checkpoint: checkpoint},
	} {
		if _, err := repository.RecordTrainingEvent(context.Background(), job.ID, token, event, now); !errors.Is(err, ErrTrainingEventInvalid) {
			t.Fatalf("event type %q without generation returned %v", event.Type, err)
		}
	}
	var eventCount, checkpointCount int64
	if err := repository.db.Model(&TrainingJobEventRecord{}).Where("job_id = ?", job.ID).Count(&eventCount).Error; err != nil {
		t.Fatal(err)
	}
	if err := repository.db.Model(&TrainingCheckpointRecord{}).Where("job_id = ?", job.ID).Count(&checkpointCount).Error; err != nil {
		t.Fatal(err)
	}
	var cursor TrainingJobEventTokenRecord
	if err := repository.db.Where("job_id = ?", job.ID).First(&cursor).Error; err != nil {
		t.Fatal(err)
	}
	if eventCount != 0 || checkpointCount != 0 || cursor.RateCount != 0 || cursor.LastGeneration != 0 {
		t.Fatalf("omitted generation mutated persistence: events=%d checkpoints=%d cursor=%+v", eventCount, checkpointCount, cursor)
	}
}

func TestRecordTrainingEventRejectsReplayWithOmittedGenerationBeforeRatePersistence(t *testing.T) {
	repository, job := managedCheckpointRepository(t)
	token := randomJobToken(t)
	now := time.Now().UTC()
	if err := repository.EnsureTrainingEventToken(context.Background(), job.ID, token, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	valid := domain.TrainingEvent{ID: "progress-replay-generation", Type: domain.TrainingEventProgress, Generation: 1, Epoch: 1, Step: 1}
	if _, err := repository.RecordTrainingEvent(context.Background(), job.ID, token, valid, now); err != nil {
		t.Fatal(err)
	}
	omitted := valid
	omitted.Generation = 0
	if _, err := repository.RecordTrainingEvent(context.Background(), job.ID, token, omitted, now); !errors.Is(err, ErrTrainingEventInvalid) {
		t.Fatalf("replay without generation returned %v", err)
	}
	var cursor TrainingJobEventTokenRecord
	if err := repository.db.Where("job_id = ?", job.ID).First(&cursor).Error; err != nil {
		t.Fatal(err)
	}
	if cursor.RateCount != 1 || cursor.LastGeneration != 1 {
		t.Fatalf("invalid replay mutated cursor: %+v", cursor)
	}
}

func TestRecordTrainingEventAcceptsProgressAndCheckpointAtCurrentGeneration(t *testing.T) {
	repository, job := managedCheckpointRepository(t)
	token := randomJobToken(t)
	now := time.Now().UTC()
	if err := repository.EnsureTrainingEventToken(context.Background(), job.ID, token, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.RecordTrainingEvent(context.Background(), job.ID, token, domain.TrainingEvent{
		ID: "worker-generation-2", Type: domain.TrainingEventWorkerGroupStarted, Generation: 2,
	}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.RecordTrainingEvent(context.Background(), job.ID, token, domain.TrainingEvent{
		ID: "progress-generation-2", Type: domain.TrainingEventProgress, Generation: 2, Epoch: 1, Step: 1,
	}, now.Add(time.Second)); err != nil {
		t.Fatalf("record progress at current generation: %v", err)
	}
	checkpoint := &domain.TrainingCheckpoint{
		ID: "checkpoint-generation-2", Epoch: 1, Step: 2,
		ObjectPath:     "/mnt/data/output/.platform/ray-train/" + job.ID + "/checkpoints/generation-2",
		ManifestSHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		Complete:       true,
	}
	if _, err := repository.RecordTrainingEvent(context.Background(), job.ID, token, domain.TrainingEvent{
		ID: "checkpoint-event-generation-2", Type: domain.TrainingEventCheckpointComplete,
		Generation: 2, Epoch: 1, Step: 2, Checkpoint: checkpoint,
	}, now.Add(2*time.Second)); err != nil {
		t.Fatalf("record checkpoint at current generation: %v", err)
	}
	items, err := repository.ListUsableCheckpoints(context.Background(), job.TenantID, job.UserID, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ID != checkpoint.ID {
		t.Fatalf("current-generation checkpoint was not persisted: %+v", items)
	}
}

func TestRecordTrainingEventReplayConsumesPerJobRateLimit(t *testing.T) {
	repository, job := managedCheckpointRepository(t)
	token := randomJobToken(t)
	now := time.Now().UTC()
	if err := repository.EnsureTrainingEventToken(context.Background(), job.ID, token, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	event := domain.TrainingEvent{ID: "replayed-progress", Type: domain.TrainingEventProgress, Generation: 1, Epoch: 1, Step: 1}
	if _, err := repository.RecordTrainingEvent(context.Background(), job.ID, token, event, now); err != nil {
		t.Fatal(err)
	}
	for replay := 1; replay < TrainingEventRateLimit; replay++ {
		result, err := repository.RecordTrainingEvent(context.Background(), job.ID, token, event, now)
		if err != nil {
			t.Fatalf("replay %d unexpectedly rejected: %v", replay, err)
		}
		if !result.Replayed || result.EventID != event.ID {
			t.Fatalf("replay %d changed idempotent result: %+v", replay, result)
		}
	}
	if _, err := repository.RecordTrainingEvent(context.Background(), job.ID, token, event, now); !errors.Is(err, ErrTrainingEventRateLimited) {
		t.Fatalf("expected replay rate limit, got %v", err)
	}
	var cursor TrainingJobEventTokenRecord
	if err := repository.db.Where("job_id = ?", job.ID).First(&cursor).Error; err != nil {
		t.Fatal(err)
	}
	if cursor.RateCount != TrainingEventRateLimit {
		t.Fatalf("persisted replay rate count=%d, want %d", cursor.RateCount, TrainingEventRateLimit)
	}
}

func TestRecordCheckpointCompletePersistsTransactionallyAndListsOnlyUsableRows(t *testing.T) {
	repository, job := managedCheckpointRepository(t)
	token := randomJobToken(t)
	now := time.Now().UTC()
	if err := repository.EnsureTrainingEventToken(context.Background(), job.ID, token, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	checkpoint := &domain.TrainingCheckpoint{
		ID: "checkpoint-epoch-2", Epoch: 2, Step: 40,
		ObjectPath:     "/mnt/data/output/.platform/ray-train/" + job.ID + "/checkpoints/epoch-2",
		ManifestSHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		Complete:       true, IsBest: true, MetricName: "mAP", MetricValue: floatPointerRepository(0.61),
	}
	result, err := repository.RecordTrainingEvent(context.Background(), job.ID, token, domain.TrainingEvent{ID: "checkpoint-event-2", Type: domain.TrainingEventCheckpointComplete, Generation: 1, Epoch: 2, Step: 40, Checkpoint: checkpoint}, now)
	if err != nil || result.CheckpointID != checkpoint.ID {
		t.Fatalf("record checkpoint: result=%+v err=%v", result, err)
	}
	if err := repository.db.Create(&TrainingCheckpointRecord{ID: "incomplete", JobID: job.ID, TenantID: job.TenantID, UserID: job.UserID, ObjectPath: checkpoint.ObjectPath + "-incomplete", Complete: false, CreatedAt: now}).Error; err != nil {
		t.Fatal(err)
	}
	items, err := repository.ListUsableCheckpoints(context.Background(), job.TenantID, job.UserID, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ID != checkpoint.ID || !items[0].Complete {
		t.Fatalf("unexpected usable checkpoints: %+v", items)
	}
}

func TestRecordCheckpointIdentityIsScopedToJob(t *testing.T) {
	repository, firstJob := managedCheckpointRepository(t)
	secondJob := testJob()
	secondJob.ID = "job-fedcba9876543210fedcba98"
	secondJob.TenantID = "team-a"
	secondJob.UserID = "user-b"
	secondJob.Spec.Name = "second-managed-checkpoint-job"
	secondJob.Spec.Queue = "team-a-gpu"
	secondJob.Spec.TrainingEngine = domain.TrainingEngineRayTrain
	secondJob.Spec.RayVersion = domain.RayVersionProduction
	secondJob.Spec.ResolvedDataMounts.Output = &domain.ResolvedDataMount{
		Space: domain.DataSpaceMyRuns, BindingSpace: domain.DataSpaceWorkspace,
		ClaimName: "data-team-a", SubPath: "tenants/team-a/users/user-b/runs/managed",
		MountPath: domain.DataMountOutputPath,
	}
	if err := repository.Create(context.Background(), &secondJob, "second-managed-checkpoint-job"); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	firstToken := randomJobToken(t)
	secondToken := randomJobToken(t)
	if err := repository.EnsureTrainingEventToken(context.Background(), firstJob.ID, firstToken, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := repository.EnsureTrainingEventToken(context.Background(), secondJob.ID, secondToken, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	const checkpointID = "checkpoint-shared-name"
	for index, item := range []struct {
		job   domain.TrainingJob
		token []byte
	}{
		{job: firstJob, token: firstToken},
		{job: secondJob, token: secondToken},
	} {
		checkpoint := &domain.TrainingCheckpoint{
			ID: checkpointID, Epoch: 1, Step: 10,
			ObjectPath:     "/mnt/data/output/.platform/ray-train/" + item.job.ID + "/checkpoints/shared",
			ManifestSHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			Complete:       true,
		}
		event := domain.TrainingEvent{
			ID: "checkpoint-event-" + item.job.ID, Type: domain.TrainingEventCheckpointComplete,
			Generation: 1, Epoch: 1, Step: 10, Checkpoint: checkpoint,
		}
		if _, err := repository.RecordTrainingEvent(context.Background(), item.job.ID, item.token, event, now.Add(time.Duration(index)*time.Second)); err != nil {
			t.Fatalf("persist checkpoint for job %s: %v", item.job.ID, err)
		}
	}
	for _, job := range []domain.TrainingJob{firstJob, secondJob} {
		items, err := repository.ListUsableCheckpoints(context.Background(), job.TenantID, job.UserID, job.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(items) != 1 || items[0].ID != checkpointID || items[0].JobID != job.ID {
			t.Fatalf("job-scoped checkpoint lookup for %s returned %+v", job.ID, items)
		}
	}
}

func TestRecordCheckpointRejectsPathOutsideAuthenticatedJobOutput(t *testing.T) {
	repository, job := managedCheckpointRepository(t)
	token := randomJobToken(t)
	now := time.Now().UTC()
	if err := repository.EnsureTrainingEventToken(context.Background(), job.ID, token, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	event := domain.TrainingEvent{ID: "checkpoint-escape", Type: domain.TrainingEventCheckpointComplete, Generation: 1, Epoch: 1, Step: 1, Checkpoint: &domain.TrainingCheckpoint{
		ID: "checkpoint-escape", ObjectPath: "/mnt/data/output/../../storage/team/checkpoint", Complete: true,
		ManifestSHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
	}}
	if _, err := repository.RecordTrainingEvent(context.Background(), job.ID, token, event, now); !errors.Is(err, ErrTrainingEventInvalid) {
		t.Fatalf("expected path rejection, got %v", err)
	}
}

func TestRecordTrainingEventRejectsRegressingProgressAndRateLimitsPerJob(t *testing.T) {
	repository, job := managedCheckpointRepository(t)
	token := randomJobToken(t)
	now := time.Now().UTC()
	if err := repository.EnsureTrainingEventToken(context.Background(), job.ID, token, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.RecordTrainingEvent(context.Background(), job.ID, token, domain.TrainingEvent{ID: "progress-10", Type: domain.TrainingEventProgress, Generation: 1, Epoch: 3, Step: 10}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.RecordTrainingEvent(context.Background(), job.ID, token, domain.TrainingEvent{ID: "progress-9", Type: domain.TrainingEventProgress, Generation: 1, Epoch: 3, Step: 9}, now); !errors.Is(err, ErrTrainingEventInvalid) {
		t.Fatalf("expected monotonic progress rejection, got %v", err)
	}
	for index := 0; index < TrainingEventRateLimit-1; index++ {
		event := domain.TrainingEvent{ID: eventID(index), Type: domain.TrainingEventProgress, Generation: 1, Epoch: 3, Step: int64(11 + index)}
		if _, err := repository.RecordTrainingEvent(context.Background(), job.ID, token, event, now); err != nil {
			t.Fatalf("event %d unexpectedly rejected: %v", index, err)
		}
	}
	if _, err := repository.RecordTrainingEvent(context.Background(), job.ID, token, domain.TrainingEvent{ID: "rate-limit", Type: domain.TrainingEventProgress, Generation: 1, Epoch: 4, Step: 1}, now); !errors.Is(err, ErrTrainingEventRateLimited) {
		t.Fatalf("expected rate limit, got %v", err)
	}
}

func eventID(index int) string {
	return "progress-event-" + time.Unix(int64(index), 0).UTC().Format("150405.000000000")
}
func floatPointerRepository(value float64) *float64 { return &value }
