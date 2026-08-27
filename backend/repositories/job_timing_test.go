package repositories

import (
	"context"
	"testing"
	"time"

	"ray-train-platform-backend/domain"
)

func seedTimingJob(t *testing.T, repository *GormRepository) domain.TrainingJob {
	t.Helper()
	job := testJob()
	if err := repository.Create(context.Background(), &job, ""); err != nil {
		t.Fatalf("seed job: %v", err)
	}
	return job
}

// Reported times must be the workload's own, not the moment the reconciler
// happened to poll. A poll landing minutes after a job finished previously
// pushed the recorded end time minutes past reality.
func TestApplyObservedStateRecordsTheWorkloadExecutionWindow(t *testing.T) {
	repository := testRepository(t)
	job := seedTimingJob(t, repository)

	started := time.Date(2026, 8, 17, 9, 15, 28, 0, time.UTC)
	finished := time.Date(2026, 8, 17, 9, 17, 18, 0, time.UTC)
	observed := observedStateForTest(job, domain.StateSucceeded)
	observed.StartedAt, observed.FinishedAt = &started, &finished
	if err := repository.ApplyObservedState(context.Background(), observed); err != nil {
		t.Fatalf("apply observed state: %v", err)
	}
	stored, err := repository.Get(context.Background(), job.TenantID, job.ID)
	if err != nil {
		t.Fatalf("read job: %v", err)
	}
	if stored.StartedAt == nil || !stored.StartedAt.UTC().Equal(started) {
		t.Fatalf("expected the reported start time, got %v", stored.StartedAt)
	}
	if stored.FinishedAt == nil || !stored.FinishedAt.UTC().Equal(finished) {
		t.Fatalf("expected the reported end time, got %v", stored.FinishedAt)
	}
}

// A running job records its start and stays without an end, so the Portal can
// show elapsed training time instead of a finished run.
func TestApplyObservedStateRecordsStartWithoutFinishWhileRunning(t *testing.T) {
	repository := testRepository(t)
	job := seedTimingJob(t, repository)

	started := time.Date(2026, 8, 17, 9, 15, 28, 0, time.UTC)
	observed := observedStateForTest(job, domain.StateRunning)
	observed.StartedAt = &started
	if err := repository.ApplyObservedState(context.Background(), observed); err != nil {
		t.Fatalf("apply observed state: %v", err)
	}
	stored, _ := repository.Get(context.Background(), job.TenantID, job.ID)
	if stored.StartedAt == nil || stored.FinishedAt != nil {
		t.Fatalf("expected a started, unfinished job, got start=%v finish=%v", stored.StartedAt, stored.FinishedAt)
	}
}

// If the workload never published an end time, a terminal job must still get
// one so it does not render as perpetually running.
func TestApplyObservedStateFallsBackToObservationTimeForATerminalJob(t *testing.T) {
	repository := testRepository(t)
	job := seedTimingJob(t, repository)

	before := time.Now().UTC().Add(-time.Second)
	if err := repository.ApplyObservedState(context.Background(), observedStateForTest(job, domain.StateFailed)); err != nil {
		t.Fatalf("apply observed state: %v", err)
	}
	stored, _ := repository.Get(context.Background(), job.TenantID, job.ID)
	if stored.FinishedAt == nil || stored.FinishedAt.UTC().Before(before) {
		t.Fatalf("expected a fallback finish time, got %v", stored.FinishedAt)
	}
}

// A queued job has not started, so it must not be given a start time.
func TestApplyObservedStateLeavesAQueuedJobWithoutExecutionTimes(t *testing.T) {
	repository := testRepository(t)
	job := seedTimingJob(t, repository)

	if err := repository.ApplyObservedState(context.Background(), observedStateForTest(job, domain.StateQueued)); err != nil {
		t.Fatalf("apply observed state: %v", err)
	}
	stored, _ := repository.Get(context.Background(), job.TenantID, job.ID)
	if stored.StartedAt != nil || stored.FinishedAt != nil {
		t.Fatalf("a queued job has no execution window, got start=%v finish=%v", stored.StartedAt, stored.FinishedAt)
	}
}
