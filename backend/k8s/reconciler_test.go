package k8s

import (
	"context"
	"errors"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
	"ray-train-platform-backend/domain"
)

type memoryJobStore struct {
	job      *domain.TrainingJob
	observed []domain.ObservedJobState
}

type recordingGitCredentialResolver struct {
	tenantID string
	userID   string
	url      string
}

func (resolver *recordingGitCredentialResolver) GitCredentialSecretFor(_ context.Context, tenantID, userID, repositoryURL string) string {
	resolver.tenantID, resolver.userID, resolver.url = tenantID, userID, repositoryURL
	return "git-cred-personal"
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

func assertNoKueueUpdateActions(t *testing.T, client *dynamicfake.FakeDynamicClient) {
	t.Helper()
	for _, action := range client.Actions() {
		if action.GetVerb() == "update" {
			t.Fatalf("unexpected Kueue update action: %+v", action)
		}
	}
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
	if store.observed[0].RayJobName != "job-01" || store.observed[0].KubernetesNS != "tenant-a" {
		t.Fatalf("missing RayJob references: %+v", store.observed[0])
	}
}

func TestReconcileSucceededJobShortensCleanupTTL(t *testing.T) {
	job := validRenderJob()
	job.Spec.CleanupPolicy = domain.CleanupPolicy{SuccessTTLSeconds: 75, FailureTTLSeconds: 900}
	store := &memoryJobStore{job: &job}
	dynamicClient := newFakeDynamicClient().(*dynamicfake.FakeDynamicClient)
	client := NewClientFromInterfaces(dynamicClient, nil)
	manifest, err := RenderRayJob(job, testRenderOptions())
	if err != nil {
		t.Fatalf("render RayJob: %v", err)
	}
	if err := unstructured.SetNestedMap(manifest.Object, map[string]any{"jobStatus": "SUCCEEDED"}, "status"); err != nil {
		t.Fatalf("set RayJob status: %v", err)
	}
	if _, err := dynamicClient.Resource(rayJobGVR).Namespace(manifest.GetNamespace()).Create(context.Background(), manifest, metav1.CreateOptions{}); err != nil {
		t.Fatalf("seed succeeded RayJob: %v", err)
	}

	reconciler := NewReconciler(store, client, testRenderOptions())
	if err := reconciler.ReconcileJob(context.Background(), job.ID); err != nil {
		t.Fatalf("reconcile succeeded job: %v", err)
	}
	updated, err := client.GetRayJob(context.Background(), manifest.GetNamespace(), manifest.GetName())
	if err != nil {
		t.Fatalf("get updated RayJob: %v", err)
	}
	ttl, found, err := unstructured.NestedInt64(updated.Object, "spec", "ttlSecondsAfterFinished")
	if err != nil || !found || ttl != 75 {
		t.Fatalf("expected success cleanup TTL 75, got ttl=%d found=%v err=%v", ttl, found, err)
	}
}

func TestReconcileSucceededJobPersistsTerminalStateWhenTTLUpdateFails(t *testing.T) {
	job := validRenderJob()
	store := &memoryJobStore{job: &job}
	dynamicClient := newFakeDynamicClient().(*dynamicfake.FakeDynamicClient)
	client := NewClientFromInterfaces(dynamicClient, nil)
	manifest, err := RenderRayJob(job, testRenderOptions())
	if err != nil {
		t.Fatalf("render RayJob: %v", err)
	}
	if err := unstructured.SetNestedMap(manifest.Object, map[string]any{"jobStatus": "SUCCEEDED"}, "status"); err != nil {
		t.Fatalf("set RayJob status: %v", err)
	}
	if _, err := dynamicClient.Resource(rayJobGVR).Namespace(manifest.GetNamespace()).Create(context.Background(), manifest, metav1.CreateOptions{}); err != nil {
		t.Fatalf("seed succeeded RayJob: %v", err)
	}
	dynamicClient.PrependReactor("update", "rayjobs", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("RayJob update unavailable")
	})

	reconciler := NewReconciler(store, client, testRenderOptions())
	if err := reconciler.ReconcileJob(context.Background(), job.ID); err == nil {
		t.Fatal("expected TTL update error")
	}
	if len(store.observed) != 1 || store.observed[0].State != domain.StateSucceeded {
		t.Fatalf("terminal state must persist before best-effort TTL update: %+v", store.observed)
	}
}

func TestReconcileGitJobResolvesCredentialForSubmittingUser(t *testing.T) {
	job := validRenderJob()
	job.UserID = "user-a"
	job.Spec.Source = domain.CodeSource{Type: "git", URL: "https://git.example.com/team/private.git", Commit: "0123456789abcdef"}
	store := &memoryJobStore{job: &job}
	client := NewClientFromInterfaces(newFakeDynamicClient(), nil)
	resolver := &recordingGitCredentialResolver{}
	reconciler := NewReconciler(store, client, testRenderOptions()).WithGitCredentials(resolver)

	if err := reconciler.ReconcileJob(context.Background(), job.ID); err != nil {
		t.Fatalf("reconcile git job: %v", err)
	}
	if resolver.tenantID != job.TenantID || resolver.userID != "user-a" || resolver.url != job.Spec.Source.URL {
		t.Fatalf("credential resolver must receive submitting identity: %+v", resolver)
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

func TestQuotaSyncRefreshesRuntimeLimitsWhenKueueAlreadyMatches(t *testing.T) {
	t.Cleanup(func() { domain.SetResourceLimits(domain.ResourceLimits{}) })
	domain.SetResourceLimits(domain.ResourceLimits{MaxWorkerReplicas: 3, MaxGPUsPerWorker: 8, MaxTotalGPUs: 24})

	client, dynamicClient := quotaTestClient(clusterQueueObject("cluster-gpu-queue", "14", "24", "96Gi"))
	client.kubernetes = fake.NewSimpleClientset(
		trainingNode("gpu-1", trainingPool, "8", "8", "32Gi"),
		trainingNode("gpu-2", trainingPool, "4", "8", "32Gi"),
		trainingNode("gpu-3", trainingPool, "2", "8", "32Gi"),
	)
	reconciler := NewReconciler(nil, client, RenderOptions{NodeSelector: trainingPool}).WithQuotaSync(QuotaSyncOptions{
		ClusterQueueName: "cluster-gpu-queue",
		Enabled:          true,
	})

	reconciler.syncClusterQueueQuota(context.Background())

	want := domain.ResourceLimits{MaxWorkerReplicas: 3, MaxGPUsPerWorker: 2, MaxTotalGPUs: 14}
	if got := domain.CurrentResourceLimits(); got != want {
		t.Fatalf("expected runtime limits %+v even when Kueue needs no update, got %+v", want, got)
	}
	assertNoKueueUpdateActions(t, dynamicClient)
}

func TestQuotaSyncPreservesLastKnownGoodLimitsOnEmptyOrFailedObservation(t *testing.T) {
	t.Cleanup(func() { domain.SetResourceLimits(domain.ResourceLimits{}) })
	want := domain.ResourceLimits{MaxWorkerReplicas: 4, MaxGPUsPerWorker: 6, MaxTotalGPUs: 28}

	tests := []struct {
		name       string
		kubernetes *fake.Clientset
	}{
		{name: "empty", kubernetes: fake.NewSimpleClientset()},
		{name: "list error", kubernetes: func() *fake.Clientset {
			client := fake.NewSimpleClientset()
			client.PrependReactor("list", "nodes", func(k8stesting.Action) (bool, runtime.Object, error) {
				return true, nil, errors.New("node API unavailable")
			})
			return client
		}()},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			domain.SetResourceLimits(domain.ResourceLimits{MaxWorkerReplicas: 3, MaxGPUsPerWorker: 8, MaxTotalGPUs: 24})
			if err := domain.UpdateResourceLimitsFromCapacity(4, 6, 28); err != nil {
				t.Fatalf("seed last-known-good observed capacity: %v", err)
			}
			client, dynamicClient := quotaTestClient(clusterQueueObject("cluster-gpu-queue", "28", "40", "160Gi"))
			client.kubernetes = test.kubernetes
			reconciler := NewReconciler(nil, client, RenderOptions{NodeSelector: trainingPool}).WithQuotaSync(QuotaSyncOptions{
				ClusterQueueName: "cluster-gpu-queue",
				Enabled:          true,
			})

			reconciler.syncClusterQueueQuota(context.Background())

			if got := domain.CurrentResourceLimits(); got != want {
				t.Fatalf("observation replaced last-known-good limits: got %+v, want %+v", got, want)
			}
			assertNoKueueUpdateActions(t, dynamicClient)
		})
	}
}
