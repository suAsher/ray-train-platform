package k8s

import (
	"context"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"ray-train-platform-backend/domain"
)

type memoryJobStore struct {
	job      *domain.TrainingJob
	observed []domain.ObservedJobState
}

func (s *memoryJobStore) GetByID(context.Context, string) (*domain.TrainingJob, error) {
	copy := *s.job
	return &copy, nil
}

func (s *memoryJobStore) ListReconcileCandidates(context.Context, int) ([]string, error) {
	return []string{s.job.ID}, nil
}

func (s *memoryJobStore) ApplyObservedState(_ context.Context, state domain.ObservedJobState) error {
	s.observed = append(s.observed, state)
	s.job.ObservedState = state.State
	s.job.KubernetesNS = state.KubernetesNS
	s.job.RayJobName = state.RayJobName
	return nil
}

func (s *memoryJobStore) ClaimOutbox(context.Context, int) ([]domain.OutboxEvent, error) {
	return nil, nil
}

func (s *memoryJobStore) MarkOutboxDone(context.Context, string) error { return nil }

func (s *memoryJobStore) MarkOutboxRetry(context.Context, string, time.Time, string) error {
	return nil
}

func newFakeDynamicClient() dynamic.Interface {
	return dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
}

func TestReconcileActiveJobEnsuresRayJobAndPersistsProvisioning(t *testing.T) {
	store := &memoryJobStore{job: func() *domain.TrainingJob { job := validRenderJob(); return &job }()}
	client := NewClientFromInterfaces(newFakeDynamicClient(), nil)
	options := testRenderOptions()
	options.ClusterSpecField = "rayClusterConfig"
	reconciler := NewReconciler(store, client, options)
	if err := reconciler.ReconcileJob(context.Background(), "job-01"); err != nil {
		t.Fatalf("reconcile active job: %v", err)
	}
	if len(store.observed) != 1 || store.observed[0].State != domain.StateQueued {
		t.Fatalf("expected queued observation: %+v", store.observed)
	}
	if store.observed[0].RayJobName != "llm-train" || store.observed[0].KubernetesNS != "tenant-a" {
		t.Fatalf("missing RayJob references: %+v", store.observed[0])
	}
}

func TestReconcileCanceledJobDeletesRayJobBeforeMarkingCanceled(t *testing.T) {
	job := validRenderJob()
	job.DesiredState = domain.DesiredCanceled
	store := &memoryJobStore{job: &job}
	dynamicClient := newFakeDynamicClient()
	client := NewClientFromInterfaces(dynamicClient, nil)
	manifest, err := RenderRayJob(job, testRenderOptions())
	if err != nil {
		t.Fatalf("render ray job: %v", err)
	}
	if _, err := client.EnsureRayJob(context.Background(), manifest); err != nil {
		t.Fatalf("seed ray job: %v", err)
	}
	reconciler := NewReconciler(store, client, testRenderOptions())
	if err := reconciler.ReconcileJob(context.Background(), job.ID); err != nil {
		t.Fatalf("reconcile canceled job: %v", err)
	}
	if len(store.observed) != 1 || store.observed[0].State != domain.StateDeleting {
		t.Fatalf("expected deleting observation: %+v", store.observed)
	}
}
