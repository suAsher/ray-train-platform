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

func TestRecordTrainingEventIsIdempotentAndCountsOnlyNewWorkerGeneration(t *testing.T) {
	repository, job := managedCheckpointRepository(t)
	token := randomJobToken(t)
	now := time.Now().UTC()
	if err := repository.EnsureTrainingEventToken(context.Background(), job.ID, token, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	event := domain.TrainingEvent{ID: "worker-generation-2", Type: domain.TrainingEventWorkerGroupStarted, Generation: 2}
	first, err := repository.RecordTrainingEvent(context.Background(), job.ID, token, event, now)
	if err != nil {
		t.Fatal(err)
	}
	second, err := repository.RecordTrainingEvent(context.Background(), job.ID, token, event, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if first.EventID != second.EventID || first.WorkerRestartCount != second.WorkerRestartCount || first.Replayed || !second.Replayed || first.WorkerRestartCount != 1 {
		t.Fatalf("replay changed result: first=%+v second=%+v", first, second)
	}
	stored, err := repository.GetByID(context.Background(), job.ID)
	if err != nil || stored.WorkerRestartCount != 1 {
		t.Fatalf("worker restart count=%d err=%v", stored.WorkerRestartCount, err)
	}

	_, err = repository.RecordTrainingEvent(context.Background(), job.ID, token, domain.TrainingEvent{ID: "same-generation", Type: domain.TrainingEventWorkerGroupStarted, Generation: 2}, now.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	stored, _ = repository.GetByID(context.Background(), job.ID)
	if stored.WorkerRestartCount != 1 {
		t.Fatalf("same generation incremented restart count: %d", stored.WorkerRestartCount)
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
	result, err := repository.RecordTrainingEvent(context.Background(), job.ID, token, domain.TrainingEvent{ID: "checkpoint-event-2", Type: domain.TrainingEventCheckpointComplete, Epoch: 2, Step: 40, Checkpoint: checkpoint}, now)
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

func TestRecordCheckpointRejectsPathOutsideAuthenticatedJobOutput(t *testing.T) {
	repository, job := managedCheckpointRepository(t)
	token := randomJobToken(t)
	now := time.Now().UTC()
	if err := repository.EnsureTrainingEventToken(context.Background(), job.ID, token, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	event := domain.TrainingEvent{ID: "checkpoint-escape", Type: domain.TrainingEventCheckpointComplete, Epoch: 1, Step: 1, Checkpoint: &domain.TrainingCheckpoint{
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
	if _, err := repository.RecordTrainingEvent(context.Background(), job.ID, token, domain.TrainingEvent{ID: "progress-10", Type: domain.TrainingEventProgress, Epoch: 3, Step: 10}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.RecordTrainingEvent(context.Background(), job.ID, token, domain.TrainingEvent{ID: "progress-9", Type: domain.TrainingEventProgress, Epoch: 3, Step: 9}, now); !errors.Is(err, ErrTrainingEventInvalid) {
		t.Fatalf("expected monotonic progress rejection, got %v", err)
	}
	for index := 0; index < TrainingEventRateLimit-1; index++ {
		event := domain.TrainingEvent{ID: eventID(index), Type: domain.TrainingEventProgress, Epoch: 3, Step: int64(11 + index)}
		if _, err := repository.RecordTrainingEvent(context.Background(), job.ID, token, event, now); err != nil {
			t.Fatalf("event %d unexpectedly rejected: %v", index, err)
		}
	}
	if _, err := repository.RecordTrainingEvent(context.Background(), job.ID, token, domain.TrainingEvent{ID: "rate-limit", Type: domain.TrainingEventProgress, Epoch: 4, Step: 1}, now); !errors.Is(err, ErrTrainingEventRateLimited) {
		t.Fatalf("expected rate limit, got %v", err)
	}
}

func eventID(index int) string {
	return "progress-event-" + time.Unix(int64(index), 0).UTC().Format("150405.000000000")
}
func floatPointerRepository(value float64) *float64 { return &value }
