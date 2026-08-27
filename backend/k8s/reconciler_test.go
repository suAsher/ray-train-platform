package k8s

import (
	"context"
	"errors"
	"strings"
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
	job                  *domain.TrainingJob
	observed             []domain.ObservedJobState
	recoveryAllowed      bool
	recoveryCheckpointID string
	recoveryRequests     []domain.ManagedRecoveryRequest
	cancelDuringRecovery bool
}

type recordingGitCredentialResolver struct {
	tenantID string
	userID   string
	url      string
}

type recordingExperimentFinalizer struct {
	tenantID string
	jobID    string
	state    domain.State
	finished time.Time
	err      error
}

func (finalizer *recordingExperimentFinalizer) FinalizeJobRuns(_ context.Context, tenantID, jobID string, state domain.State, finishedAt time.Time) error {
	finalizer.tenantID = tenantID
	finalizer.jobID = jobID
	finalizer.state = state
	finalizer.finished = finishedAt
	return finalizer.err
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

func (s *memoryJobStore) BeginManagedRecovery(_ context.Context, request domain.ManagedRecoveryRequest) (*domain.TrainingJob, bool, error) {
	s.recoveryRequests = append(s.recoveryRequests, request)
	if s.cancelDuringRecovery {
		s.job.DesiredState = domain.DesiredCanceled
	}
	if !s.recoveryAllowed || s.job.DesiredState != domain.DesiredActive || s.job.ClusterAttempt != request.ExpectedClusterAttempt {
		copy := *s.job
		return &copy, false, nil
	}
	s.job.ClusterAttempt++
	s.job.ObservedState = domain.StateRecovering
	s.job.ResumeCheckpointID = s.recoveryCheckpointID
	s.job.StatusReason = request.FailureClass
	s.job.StatusMessage = request.FailureMessage
	s.job.RayJobName = ""
	s.job.RayJobUID = ""
	s.job.RayClusterName = ""
	s.job.ResourceVersion = ""
	copy := *s.job
	return &copy, true, nil
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

func managedRecoveryFixture(t *testing.T) (*memoryJobStore, *Client, *dynamicfake.FakeDynamicClient, domain.TrainingJob) {
	t.Helper()
	job := managedRenderJob(domain.RayVersionProduction)
	job.DesiredState = domain.DesiredActive
	job.ObservedState = domain.StateRunning
	job.ClusterAttempt = 1
	job.RayJobName = job.ID
	job.Spec.Managed.MaxFailures = 2
	job.Spec.ResolvedDataMounts.Output = &domain.ResolvedDataMount{
		Space: domain.DataSpaceMyRuns, BindingSpace: domain.DataSpaceWorkspace,
		ClaimName: "tenant-data", SubPath: "tenants/tenant-a/users/user-01/runs/job-01",
		MountPath: domain.DataMountOutputPath, ReadOnly: false,
	}
	store := &memoryJobStore{job: &job, recoveryAllowed: true, recoveryCheckpointID: "checkpoint-4"}
	dynamicClient := newFakeDynamicClient().(*dynamicfake.FakeDynamicClient)
	client := NewClientFromInterfaces(dynamicClient, nil)
	manifest := managedManifest(t, job)
	if err := unstructured.SetNestedMap(manifest.Object, map[string]any{
		"jobStatus": "FAILED", "reason": "HEAD_POD_LOST", "message": "head node disappeared",
	}, "status"); err != nil {
		t.Fatalf("set failed RayJob status: %v", err)
	}
	if _, err := dynamicClient.Resource(rayJobGVR).Namespace(manifest.GetNamespace()).Create(context.Background(), manifest, metav1.CreateOptions{}); err != nil {
		t.Fatalf("seed failed RayJob: %v", err)
	}
	return store, client, dynamicClient, job
}

func TestFailedManagedClusterCreatesNextAttemptFromCheckpoint(t *testing.T) {
	store, client, _, job := managedRecoveryFixture(t)
	reconciler := NewReconciler(store, client, testRenderOptions())
	if err := reconciler.ReconcileJob(context.Background(), job.ID); err != nil {
		t.Fatalf("reconcile managed recovery: %v", err)
	}
	if len(store.recoveryRequests) != 1 || store.recoveryRequests[0].FailureClass != "HEAD_POD_LOST" {
		t.Fatalf("unexpected recovery classification: %+v", store.recoveryRequests)
	}
	if store.job.ClusterAttempt != 2 || store.job.ObservedState != domain.StateRecovering || store.job.ResumeCheckpointID != "checkpoint-4" {
		t.Fatalf("unexpected recovered job: %+v", store.job)
	}
	if _, err := client.GetRayJob(context.Background(), job.KubernetesNS, job.ID+"-a2"); err != nil {
		t.Fatalf("next recovery RayJob was not created: %v", err)
	}
}

func TestManagedRecoveryUsesExplicitInfrastructureReasonAllowlist(t *testing.T) {
	for _, test := range []struct {
		reason string
		want   bool
	}{
		{reason: "DRIVER_POD_LOST", want: true},
		{reason: "DRIVER_POD_EVICTED", want: true},
		{reason: "DRIVER_POD_DELETED", want: true},
		{reason: "DRIVER_POD_NOT_FOUND", want: true},
		{reason: "HEAD_POD_LOST", want: true},
		{reason: "HEAD_POD_EVICTED", want: true},
		{reason: "HEAD_POD_DELETED", want: true},
		{reason: "HEAD_POD_NOT_FOUND", want: true},
		{reason: "RAY_CLUSTER_FAILED", want: true},
		{reason: "RAY_CLUSTER_UNAVAILABLE", want: true},
		{reason: "RAY_CLUSTER_DELETED", want: true},
		{reason: "WHOLE_CLUSTER_UNAVAILABLE", want: true},
		{reason: "", want: false},
		{reason: "UNKNOWN", want: false},
		{reason: "USER_CODE_FAILED", want: false},
		{reason: "INVALID_CONFIG", want: false},
		{reason: "IMPORT_ERROR", want: false},
		{reason: "NONZERO_EXIT", want: false},
		{reason: "OOM_KILLED", want: false},
		{reason: "NAN_DETECTED", want: false},
	} {
		t.Run(test.reason, func(t *testing.T) {
			if got := isManagedInfrastructureFailure(test.reason); got != test.want {
				t.Fatalf("reason %q recoverable=%v want=%v", test.reason, got, test.want)
			}
		})
	}
}

func TestManagedRecoveryBoundsUntrustedFailureMessage(t *testing.T) {
	message := strings.Repeat("故", domain.ManagedRecoveryFailureMessageMaxBytes)
	bounded := managedRecoveryFailureMessage(message)
	if len(bounded) > domain.ManagedRecoveryFailureMessageMaxBytes || !strings.HasPrefix(message, bounded) {
		t.Fatalf("failure message was not safely bounded: bytes=%d", len(bounded))
	}
}

func TestLegacyAndProvisioningFailuresNeverEnterManagedRecovery(t *testing.T) {
	for _, mutate := range []func(*domain.TrainingJob){
		func(job *domain.TrainingJob) {
			job.Spec.TrainingEngine = domain.TrainingEngineRayDDP
			job.Spec.Managed = domain.ManagedTrainingPolicy{}
		},
		func(job *domain.TrainingJob) { job.ObservedState = domain.StateProvisioning },
	} {
		store, client, _, job := managedRecoveryFixture(t)
		mutate(store.job)
		reconciler := NewReconciler(store, client, testRenderOptions())
		if err := reconciler.ReconcileJob(context.Background(), job.ID); err != nil {
			t.Fatalf("reconcile non-recoverable job: %v", err)
		}
		if len(store.recoveryRequests) != 0 || store.job.ObservedState != domain.StateFailed {
			t.Fatalf("non-managed/provisioning job entered recovery: requests=%+v job=%+v", store.recoveryRequests, store.job)
		}
	}
}

func TestRecoveryCreationFailureRetriesSameAttemptWithoutIncrementing(t *testing.T) {
	store, client, dynamicClient, job := managedRecoveryFixture(t)
	failCreation := true
	dynamicClient.PrependReactor("create", "rayjobs", func(action k8stesting.Action) (bool, runtime.Object, error) {
		create := action.(k8stesting.CreateAction)
		resource := create.GetObject().(*unstructured.Unstructured)
		if failCreation && resource.GetName() == job.ID+"-a2" {
			return true, nil, errors.New("apiserver unavailable")
		}
		return false, nil, nil
	})
	reconciler := NewReconciler(store, client, testRenderOptions())
	if err := reconciler.ReconcileJob(context.Background(), job.ID); err == nil {
		t.Fatal("expected recovery creation failure")
	}
	if store.job.ClusterAttempt != 2 || store.job.ObservedState != domain.StateRecovering || len(store.recoveryRequests) != 1 {
		t.Fatalf("failed creation lost recoverable snapshot: job=%+v requests=%+v", store.job, store.recoveryRequests)
	}
	failCreation = false
	if err := reconciler.ReconcileJob(context.Background(), job.ID); err != nil {
		t.Fatalf("retry same recovery attempt: %v", err)
	}
	if store.job.ClusterAttempt != 2 || len(store.recoveryRequests) != 1 {
		t.Fatalf("retry incorrectly advanced another attempt: job=%+v requests=%+v", store.job, store.recoveryRequests)
	}
	if _, err := client.GetRayJob(context.Background(), job.KubernetesNS, job.ID+"-a2"); err != nil {
		t.Fatalf("same recovery attempt was not retried: %v", err)
	}
}

func TestCancellationWinsRecoveryRace(t *testing.T) {
	store, client, _, job := managedRecoveryFixture(t)
	store.cancelDuringRecovery = true
	reconciler := NewReconciler(store, client, testRenderOptions())
	if err := reconciler.ReconcileJob(context.Background(), job.ID); err != nil {
		t.Fatalf("reconcile cancellation race: %v", err)
	}
	if store.job.ClusterAttempt != 1 || store.job.DesiredState != domain.DesiredCanceled {
		t.Fatalf("recovery overrode cancellation: %+v", store.job)
	}
	if len(store.observed) == 0 || store.observed[len(store.observed)-1].State != domain.StateDeleting {
		t.Fatalf("cancellation did not delete existing RayJob: %+v", store.observed)
	}
}

func TestRecoveringCancellationTransitionsDirectlyToCanceled(t *testing.T) {
	store, client, dynamicClient, job := managedRecoveryFixture(t)
	store.job.ClusterAttempt = 2
	store.job.ObservedState = domain.StateRecovering
	store.job.DesiredState = domain.DesiredCanceled
	store.job.ResumeCheckpointID = "checkpoint-4"
	store.job.RayJobName = job.ID + "-a2"
	manifest := managedManifest(t, *store.job)
	if _, err := dynamicClient.Resource(rayJobGVR).Namespace(job.KubernetesNS).Create(context.Background(), manifest, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	reconciler := NewReconciler(store, client, testRenderOptions())
	if err := reconciler.ReconcileJob(context.Background(), job.ID); err != nil {
		t.Fatal(err)
	}
	if store.job.ObservedState != domain.StateCanceled {
		t.Fatalf("RECOVERING cancellation did not reach CANCELED: %+v", store.job)
	}
}

func TestUnknownAndUserFailuresBecomeFailedWithoutRecovery(t *testing.T) {
	for _, reason := range []string{"", "UNKNOWN", "IMPORT_ERROR", "NONZERO_EXIT", "OOM_KILLED", "NAN_DETECTED"} {
		t.Run(reason, func(t *testing.T) {
			store, client, dynamicClient, job := managedRecoveryFixture(t)
			manifest, err := client.GetRayJob(context.Background(), job.KubernetesNS, job.ID)
			if err != nil {
				t.Fatal(err)
			}
			if err := unstructured.SetNestedMap(manifest.Object, map[string]any{
				"jobStatus": "FAILED", "reason": reason, "message": "user workload failed",
			}, "status"); err != nil {
				t.Fatal(err)
			}
			if _, err := dynamicClient.Resource(rayJobGVR).Namespace(job.KubernetesNS).Update(context.Background(), manifest, metav1.UpdateOptions{}); err != nil {
				t.Fatal(err)
			}
			reconciler := NewReconciler(store, client, testRenderOptions())
			if err := reconciler.ReconcileJob(context.Background(), job.ID); err != nil {
				t.Fatal(err)
			}
			if len(store.recoveryRequests) != 0 || store.job.ObservedState != domain.StateFailed {
				t.Fatalf("user/unknown failure retried: reason=%q requests=%+v job=%+v", reason, store.recoveryRequests, store.job)
			}
		})
	}
}

func TestRecoveredAttemptTransitionsFromRecoveringToRunning(t *testing.T) {
	store, client, dynamicClient, job := managedRecoveryFixture(t)
	store.job.ClusterAttempt = 2
	store.job.ObservedState = domain.StateRecovering
	store.job.ResumeCheckpointID = "checkpoint-4"
	store.job.RayJobName = job.ID + "-a2"
	manifest := managedManifest(t, *store.job)
	if err := unstructured.SetNestedMap(manifest.Object, map[string]any{"jobStatus": "RUNNING"}, "status"); err != nil {
		t.Fatal(err)
	}
	if _, err := dynamicClient.Resource(rayJobGVR).Namespace(job.KubernetesNS).Create(context.Background(), manifest, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	reconciler := NewReconciler(store, client, testRenderOptions())
	if err := reconciler.ReconcileJob(context.Background(), job.ID); err != nil {
		t.Fatal(err)
	}
	if store.job.ObservedState != domain.StateRunning || len(store.recoveryRequests) != 0 {
		t.Fatalf("recovered attempt did not return to RUNNING: job=%+v requests=%+v", store.job, store.recoveryRequests)
	}
}

func TestProcessTerminalEventFinalizesExperimentRuns(t *testing.T) {
	finishedAt := time.Date(2026, 8, 24, 10, 15, 30, 0, time.UTC)
	job := validRenderJob()
	job.ObservedState = domain.StateCanceled
	job.FinishedAt = &finishedAt
	store := &memoryJobStore{job: &job}
	finalizer := &recordingExperimentFinalizer{}
	reconciler := NewReconciler(store, NewClientFromInterfaces(newFakeDynamicClient(), nil), testRenderOptions()).WithExperimentFinalizer(finalizer)
	event := domain.OutboxEvent{ID: job.ID + "-terminal", EventType: "TRAINING_JOB_TERMINAL", Payload: []byte(`{"job_id":"` + job.ID + `"}`)}

	if err := reconciler.processEvent(context.Background(), event); err != nil {
		t.Fatalf("process terminal event: %v", err)
	}
	if finalizer.tenantID != job.TenantID || finalizer.jobID != job.ID || finalizer.state != domain.StateCanceled || !finalizer.finished.Equal(finishedAt) {
		t.Fatalf("unexpected finalization: %+v", finalizer)
	}
}

func TestProcessTerminalEventReturnsFinalizerErrorForOutboxRetry(t *testing.T) {
	job := validRenderJob()
	job.ObservedState = domain.StateFailed
	store := &memoryJobStore{job: &job}
	finalizer := &recordingExperimentFinalizer{err: errors.New("MLflow unavailable")}
	reconciler := NewReconciler(store, NewClientFromInterfaces(newFakeDynamicClient(), nil), testRenderOptions()).WithExperimentFinalizer(finalizer)
	event := domain.OutboxEvent{ID: job.ID + "-terminal", EventType: "TRAINING_JOB_TERMINAL", Payload: []byte(`{"job_id":"` + job.ID + `"}`)}

	if err := reconciler.processEvent(context.Background(), event); err == nil {
		t.Fatal("expected finalizer error so the outbox event is retried")
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
