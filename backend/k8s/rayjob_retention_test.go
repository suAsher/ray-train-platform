package k8s

import (
	"context"
	"testing"
	"time"

	"ray-train-platform-backend/domain"
)

func retentionReconciler(store *memoryJobStore, retention time.Duration, now time.Time) *Reconciler {
	reconciler := NewReconciler(store, nil, RenderOptions{}).WithRayJobRetention(retention)
	reconciler.now = func() time.Time { return now }
	return reconciler
}

// The sweep must ask for jobs that finished before the retention window, not
// for everything terminal: a run that is still within the window keeps its
// cluster objects so it can be inspected.
func TestRayJobRetentionQueriesTheRetentionHorizon(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	store := &memoryJobStore{}

	retentionReconciler(store, 30*24*time.Hour, now).retireExpiredRayJobs(context.Background())

	if want := now.Add(-30 * 24 * time.Hour); !store.expiredBefore.Equal(want) {
		t.Fatalf("sweep must select jobs finished before the horizon: got %s want %s", store.expiredBefore, want)
	}
}

// A non-positive retention keeps the previous unbounded behaviour, so an
// operator can turn the sweep off without redeploying a different image.
func TestRayJobRetentionDisabledDoesNotTouchTheStore(t *testing.T) {
	store := &memoryJobStore{expiredRayJobs: []domain.ExpiredRayJob{{JobID: "job-a"}}}

	retentionReconciler(store, 0, time.Now().UTC()).retireExpiredRayJobs(context.Background())

	if !store.expiredBefore.IsZero() || len(store.retiredRayJobs) != 0 {
		t.Fatalf("a disabled sweep must not query or retire anything: before=%s retired=%v", store.expiredBefore, store.retiredRayJobs)
	}
}

// Deleting the RayJob is what releases the submitter Job and Pod, so a job is
// only marked retired once that delete succeeded. Without a client the delete
// fails, and the job must stay a candidate for the next tick.
func TestRayJobRetentionKeepsCandidateWhenDeleteFails(t *testing.T) {
	store := &memoryJobStore{expiredRayJobs: []domain.ExpiredRayJob{{
		JobID: "job-a", KubernetesNS: "tenant-local", RayJobName: "job-a", RayJobUID: "uid-a",
	}}}

	retentionReconciler(store, 30*24*time.Hour, time.Now().UTC()).retireExpiredRayJobs(context.Background())

	if len(store.retiredRayJobs) != 0 {
		t.Fatalf("a failed delete must not mark the job retired: %v", store.retiredRayJobs)
	}
}
