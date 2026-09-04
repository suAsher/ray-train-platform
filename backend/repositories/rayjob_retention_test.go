package repositories

import (
	"context"
	"testing"
	"time"

	"ray-train-platform-backend/domain"
)

func seedRetentionJob(t *testing.T, repository *GormRepository, id, state string, finished time.Time, resources bool) {
	t.Helper()
	record := JobRecord{
		ID: id, TenantID: "tenant-a", ObservedState: state, DesiredState: string(domain.DesiredActive),
		CreatedAt: finished, UpdatedAt: finished, FinishedAt: &finished,
	}
	if resources {
		record.KubernetesNS, record.RayJobName, record.RayJobUID = "tenant-local", id, "uid-"+id
	}
	if err := repository.db.Create(&record).Error; err != nil {
		t.Fatalf("seed job %s: %v", id, err)
	}
}

// The sweep must only ever reach finished runs that are past the window and
// still hold cluster resources. Anything else would either delete a live run's
// objects or churn on rows it cannot act upon.
func TestListExpiredRayJobsSelectsOnlyRetiredEligibleJobs(t *testing.T) {
	repository := testRepository(t)
	now := time.Now().UTC()
	old := now.Add(-40 * 24 * time.Hour)
	recent := now.Add(-2 * 24 * time.Hour)

	seedRetentionJob(t, repository, "job-old-succeeded", string(domain.StateSucceeded), old, true)
	seedRetentionJob(t, repository, "job-old-failed", string(domain.StateFailed), old, true)
	seedRetentionJob(t, repository, "job-recent-succeeded", string(domain.StateSucceeded), recent, true)
	seedRetentionJob(t, repository, "job-old-running", string(domain.StateRunning), old, true)
	seedRetentionJob(t, repository, "job-old-no-resources", string(domain.StateSucceeded), old, false)

	expired, err := repository.ListExpiredRayJobs(context.Background(), now.Add(-30*24*time.Hour), 20)
	if err != nil {
		t.Fatalf("ListExpiredRayJobs() error = %v", err)
	}

	selected := map[string]bool{}
	for _, job := range expired {
		selected[job.JobID] = true
	}
	for _, id := range []string{"job-old-succeeded", "job-old-failed"} {
		if !selected[id] {
			t.Fatalf("%s is terminal, past the window and has resources: it must be selected", id)
		}
	}
	for _, id := range []string{"job-recent-succeeded", "job-old-running", "job-old-no-resources"} {
		if selected[id] {
			t.Fatalf("%s must not be selected for retirement", id)
		}
	}
}

// Marking is what stops a deleted RayJob from being re-selected forever, since
// the job record keeps its resource identity for provenance.
func TestMarkRayJobRetiredRemovesTheJobFromCandidates(t *testing.T) {
	repository := testRepository(t)
	now := time.Now().UTC()
	seedRetentionJob(t, repository, "job-a", string(domain.StateSucceeded), now.Add(-40*24*time.Hour), true)
	horizon := now.Add(-30 * 24 * time.Hour)

	if err := repository.MarkRayJobRetired(context.Background(), "job-a", now); err != nil {
		t.Fatalf("MarkRayJobRetired() error = %v", err)
	}

	expired, err := repository.ListExpiredRayJobs(context.Background(), horizon, 20)
	if err != nil {
		t.Fatalf("ListExpiredRayJobs() error = %v", err)
	}
	if len(expired) != 0 {
		t.Fatalf("a retired job must not be selected again: %v", expired)
	}
	// A second call must stay harmless: the sweep may retry after a crash
	// between the delete and the mark.
	if err := repository.MarkRayJobRetired(context.Background(), "job-a", now); err != nil {
		t.Fatalf("MarkRayJobRetired() must be idempotent: %v", err)
	}
}
