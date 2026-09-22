package repositories

import (
	"context"
	"strings"
	"testing"
	"time"

	"ray-train-platform-backend/domain"
)

func TestAggregateAssistantDemandCountsOnlyPendingGPUWork(t *testing.T) {
	repo := testRepository(t)
	createdAt := time.Date(2026, time.September, 22, 10, 0, 0, 0, time.UTC)
	for _, item := range []struct {
		id    string
		state domain.State
		gpus  int
		want  bool
	}{
		{"submitted", domain.StateSubmitted, 2, true},
		{"validating", domain.StateValidating, 1, true},
		{"queued", domain.StateQueued, 3, true},
		{"admitted", domain.StateAdmitted, 4, true},
		{"provisioning", domain.StateProvisioning, 5, true},
		{"recovering", domain.StateRecovering, 6, true},
		{"running", domain.StateRunning, 7, false},
		{"canceling", domain.StateCanceling, 8, false},
		{"unknown", domain.StateUnknown, 9, true},
		{"succeeded", domain.StateSucceeded, 10, false},
		{"zero-gpu", domain.StateSubmitted, 0, false},
		{"inactive", domain.StateSubmitted, 11, false},
	} {
		desired := string(domain.DesiredActive)
		if item.id == "inactive" {
			desired = string(domain.DesiredCanceled)
		}
		workers, perWorker := 1, item.gpus
		if item.id == "zero-gpu" {
			workers, perWorker = 1, 0
		}
		seedGPUAllocationJob(t, repo, JobRecord{
			ID: item.id, TenantID: "tenant-a", UserID: "user-a", Name: item.id,
			DesiredState: desired, ObservedState: string(item.state), CreatedAt: createdAt,
		}, domain.JobSpec{Resources: domain.Resources{WorkerReplicas: workers, GPUsPerWorker: perWorker}})
	}
	seedGPUAllocationWorkspace(t, repo, WorkspaceRecord{ID: "workspace-submitted", TenantID: "tenant-a", UserID: "user-a", ObservedState: string(domain.WorkspaceSubmitted), GPUCount: 2})
	seedGPUAllocationWorkspace(t, repo, WorkspaceRecord{ID: "workspace-running", TenantID: "tenant-a", UserID: "user-b", ObservedState: string(domain.WorkspaceRunning), GPUCount: 8})
	seedGPUAllocationWorkspace(t, repo, WorkspaceRecord{ID: "workspace-zero", TenantID: "tenant-a", UserID: "user-c", ObservedState: string(domain.WorkspaceSubmitted), GPUCount: 0})

	got, err := repo.AggregateAssistantDemand(context.Background())
	if err != nil {
		t.Fatalf("aggregate assistant demand: %v", err)
	}
	if !got.HasDemand || got.TrainingJobCount != 7 || got.WorkspaceCount != 1 || got.GPUCount != 32 || got.ObservedAt.IsZero() {
		t.Fatalf("unexpected demand snapshot: %+v", got)
	}
}

func TestAggregateAssistantDemandReturnsErrorForMalformedPendingSpec(t *testing.T) {
	repo := testRepository(t)
	if err := repo.db.Create(&JobRecord{
		ID: "bad-pending", TenantID: "tenant-a", UserID: "user-a", Name: "bad",
		DesiredState: string(domain.DesiredActive), ObservedState: string(domain.StateSubmitted), SpecJSON: "{",
	}).Error; err != nil {
		t.Fatalf("seed malformed pending job: %v", err)
	}
	if _, err := repo.AggregateAssistantDemand(context.Background()); err == nil {
		t.Fatal("expected malformed pending job spec to fail closed")
	}
}

func TestAggregateAssistantDemandFailsClosedOnOversizedPendingSpec(t *testing.T) {
	repo := testRepository(t)
	if err := repo.db.Create(&JobRecord{
		ID: "huge-pending", TenantID: "tenant-a", UserID: "user-a", Name: "huge",
		DesiredState: string(domain.DesiredActive), ObservedState: string(domain.StateSubmitted), SpecJSON: strings.Repeat(" ", assistantDemandMaxSpecBytes+1),
	}).Error; err != nil {
		t.Fatalf("seed oversized pending job: %v", err)
	}
	if _, err := repo.AggregateAssistantDemand(context.Background()); err == nil {
		t.Fatal("expected oversized pending job spec to fail closed")
	}
}

func TestAggregateAssistantDemandFailsClosedOnTooManyPendingJobs(t *testing.T) {
	repo := testRepository(t)
	for i := 0; i < assistantDemandRecordLimit+1; i++ {
		seedGPUAllocationJob(t, repo, JobRecord{
			ID: strings.Join([]string{"pending", time.Unix(0, int64(i)).Format("150405.000000000")}, "-"), TenantID: "tenant-a", UserID: "user-a", Name: "pending",
			DesiredState: string(domain.DesiredActive), ObservedState: string(domain.StateSubmitted),
		}, domain.JobSpec{Resources: domain.Resources{WorkerReplicas: 1, GPUsPerWorker: 0}})
	}
	if _, err := repo.AggregateAssistantDemand(context.Background()); err == nil {
		t.Fatal("expected too many pending jobs to fail closed")
	}
}
