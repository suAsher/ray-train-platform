package k8s

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
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
	retirementRequests   []domain.ManagedRetiringIdentityRequest
	reservationRequests  []domain.ManagedAttemptReservationRequest
	adoptionRequests     []domain.ManagedAttemptAdoptionRequest
	cancelDuringRecovery bool
	cancelDuringRetire   bool
	advanceDuringAdopt   bool
	cancelDuringAdopt    bool
	managedResources     map[int]domain.ManagedAttemptResource
	issuedFences         map[int]map[int64]bool
	quarantineEvents     []domain.ManagedAttemptCleanupFailureRequest
	reservationCalls     int
	reservationCallLimit int
}

func (s *memoryJobStore) IsManagedAttemptFenceIssued(_ context.Context, _ string, attempt int, fence int64) (bool, error) {
	if fences := s.issuedFences[attempt]; fences != nil {
		return fences[fence], nil
	}
	resource, ok := s.managedResources[attempt]
	return ok && resource.ResourceFence == fence, nil
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
	if s.job.ClusterAttempt != state.ExpectedClusterAttempt || s.job.RayJobName != state.ExpectedRayJobName || s.job.RayJobUID != state.ExpectedRayJobUID {
		return nil
	}
	if s.job.RayJobName != "" && s.job.RayJobName != state.RayJobName {
		return nil
	}
	if s.job.RayJobUID != "" && s.job.RayJobUID != state.RayJobUID {
		return nil
	}
	s.job.ObservedState = state.State
	s.job.StatusReason = state.Reason
	s.job.StatusMessage = state.Message
	s.job.KubernetesNS = state.KubernetesNS
	s.job.RayJobName = state.RayJobName
	s.job.RayJobUID = state.RayJobUID
	return nil
}

func (s *memoryJobStore) BeginManagedRecovery(_ context.Context, request domain.ManagedRecoveryRequest) (*domain.TrainingJob, bool, error) {
	s.recoveryRequests = append(s.recoveryRequests, request)
	if s.cancelDuringRecovery {
		s.job.DesiredState = domain.DesiredCanceled
	}
	if !s.recoveryAllowed || s.job.DesiredState != domain.DesiredActive ||
		s.job.ClusterAttempt != request.ExpectedClusterAttempt ||
		s.job.RayJobName != request.ExpectedRayJobName || s.job.RayJobUID != request.ExpectedRayJobUID {
		copy := *s.job
		return &copy, false, nil
	}
	s.ensureManagedResources()
	resource := s.managedResources[s.job.ClusterAttempt]
	resource.JobID, resource.ClusterAttempt = s.job.ID, s.job.ClusterAttempt
	resource.KubernetesNS, resource.RayJobName, resource.RayJobUID = s.job.KubernetesNS, s.job.RayJobName, s.job.RayJobUID
	if resource.ResourceFence == 0 {
		resource.ResourceFence, resource.LeaseVersion = 1, 1
	}
	resource.State, resource.LeaseOwner, resource.LeaseExpiresAt = domain.ManagedAttemptResourceRetiring, "", nil
	s.managedResources[s.job.ClusterAttempt] = resource
	s.job.ClusterAttempt++
	s.job.ObservedState = domain.StateRecovering
	s.job.ResumeCheckpointID = s.recoveryCheckpointID
	s.job.StatusReason = request.FailureClass
	s.job.StatusMessage = request.FailureMessage
	copy := *s.job
	return &copy, true, nil
}

func (s *memoryJobStore) ClearManagedRecoveryRetiringIdentity(_ context.Context, request domain.ManagedRetiringIdentityRequest) (*domain.TrainingJob, bool, error) {
	s.retirementRequests = append(s.retirementRequests, request)
	if s.cancelDuringRetire {
		s.job.DesiredState = domain.DesiredCanceled
	}
	if s.job.DesiredState != domain.DesiredActive || s.job.ObservedState != domain.StateRecovering ||
		s.job.ClusterAttempt != request.ExpectedClusterAttempt || s.job.RayJobName != request.RayJobName || s.job.RayJobUID != request.RayJobUID {
		copy := *s.job
		return &copy, false, nil
	}
	for _, resource := range s.managedResources {
		if resource.JobID == s.job.ID && resource.RayJobName == request.RayJobName && resource.RayJobUID == request.RayJobUID {
			copy := *s.job
			return &copy, false, nil
		}
	}
	s.job.RayJobName = ""
	s.job.RayJobUID = ""
	s.job.RayClusterName = ""
	s.job.ResourceVersion = ""
	copy := *s.job
	return &copy, true, nil
}

func (s *memoryJobStore) ReserveManagedAttemptIdentity(_ context.Context, request domain.ManagedAttemptReservationRequest) (*domain.TrainingJob, bool, error) {
	s.reservationCalls++
	if s.reservationCallLimit > 0 && s.reservationCalls > s.reservationCallLimit {
		return nil, false, errors.New("managed reservation recursion exceeded test bound")
	}
	s.reservationRequests = append(s.reservationRequests, request)
	if s.job.Spec.TrainingEngine.Resolved() != domain.TrainingEngineRayTrain ||
		s.job.DesiredState != domain.DesiredActive || s.job.ClusterAttempt != request.ExpectedClusterAttempt ||
		s.job.ObservedState != request.ExpectedState || s.job.RayJobUID != "" || s.job.RayJobName != request.ExpectedRayJobName {
		copy := *s.job
		return &copy, false, nil
	}
	if s.job.RayJobName == "" {
		s.job.RayJobName = request.RayJobName
		s.ensureManagedResources()
		s.managedResources[request.ExpectedClusterAttempt] = domain.ManagedAttemptResource{
			JobID: request.JobID, ClusterAttempt: request.ExpectedClusterAttempt,
			KubernetesNS: request.KubernetesNS, RayJobName: request.RayJobName,
			State: domain.ManagedAttemptResourceReserved,
		}
	}
	resource, exists := s.managedResources[request.ExpectedClusterAttempt]
	copy := *s.job
	return &copy, s.job.RayJobName == request.RayJobName && exists && resource.RayJobUID == "" &&
		(resource.State == domain.ManagedAttemptResourceReserved || resource.State == domain.ManagedAttemptResourceCreating), nil
}

func (s *memoryJobStore) GetManagedAttemptResource(_ context.Context, _ string, attempt int) (*domain.ManagedAttemptResource, error) {
	resource, ok := s.managedResources[attempt]
	if !ok {
		return nil, errors.New("managed attempt resource not found")
	}
	copy := resource
	return &copy, nil
}

func (s *memoryJobStore) AdoptManagedAttemptIdentity(_ context.Context, request domain.ManagedAttemptAdoptionRequest) (*domain.TrainingJob, bool, error) {
	s.adoptionRequests = append(s.adoptionRequests, request)
	if s.cancelDuringAdopt {
		s.cancelDuringAdopt = false
		s.job.DesiredState = domain.DesiredCanceled
		if resource, ok := s.managedResources[request.ExpectedClusterAttempt]; ok {
			resource.State, resource.LeaseOwner, resource.LeaseExpiresAt = domain.ManagedAttemptResourceRetiring, "", nil
			s.managedResources[request.ExpectedClusterAttempt] = resource
		}
	}
	if s.advanceDuringAdopt {
		s.advanceDuringAdopt = false
		s.job.ClusterAttempt++
		s.job.ObservedState = domain.StateRecovering
		// Model replica A adopting this exact resource and advancing recovery
		// while replica B is still between Ensure and adoption. Begin recovery
		// preserves the old identity until retirement reaches NotFound.
		s.job.RayJobName = request.RayJobName
		s.job.RayJobUID = request.RayJobUID
	}
	if s.job.ClusterAttempt == request.ExpectedClusterAttempt && s.job.RayJobName == request.RayJobName && s.job.RayJobUID == request.RayJobUID {
		resource := s.managedResources[request.ExpectedClusterAttempt]
		copy := *s.job
		return &copy, resource.ResourceFence == request.ResourceFence, nil
	}
	if s.job.Spec.TrainingEngine.Resolved() != domain.TrainingEngineRayTrain ||
		s.job.DesiredState != domain.DesiredActive || s.job.ClusterAttempt != request.ExpectedClusterAttempt ||
		s.job.ObservedState != request.ExpectedState || s.job.RayJobName != request.RayJobName || s.job.RayJobUID != "" {
		copy := *s.job
		return &copy, false, nil
	}
	resource := s.managedResources[request.ExpectedClusterAttempt]
	if request.ResourceFence != resource.LeaseVersion {
		copy := *s.job
		return &copy, false, nil
	}
	s.job.RayJobUID = request.RayJobUID
	s.job.KubernetesNS = request.KubernetesNS
	s.job.ResourceVersion = request.ResourceVersion
	resource.RayJobUID, resource.ResourceFence, resource.State, resource.LeaseOwner, resource.LeaseExpiresAt = request.RayJobUID, request.ResourceFence, domain.ManagedAttemptResourceActivating, "", nil
	s.managedResources[request.ExpectedClusterAttempt] = resource
	copy := *s.job
	return &copy, true, nil
}

func (s *memoryJobStore) ensureManagedResources() {
	if s.managedResources == nil {
		s.managedResources = map[int]domain.ManagedAttemptResource{}
	}
}

func (s *memoryJobStore) AcquireManagedAttemptCreation(_ context.Context, request domain.ManagedAttemptCreationLeaseRequest, now time.Time) (*domain.TrainingJob, *domain.ManagedAttemptResource, bool, error) {
	s.ensureManagedResources()
	copyJob := *s.job
	resource, ok := s.managedResources[request.ExpectedClusterAttempt]
	if !ok || s.job.DesiredState != domain.DesiredActive || s.job.ClusterAttempt != request.ExpectedClusterAttempt ||
		s.job.ObservedState != request.ExpectedState || s.job.RayJobName != request.RayJobName || s.job.RayJobUID != "" {
		return &copyJob, nil, false, nil
	}
	for attempt := range s.managedResources {
		if attempt < request.ExpectedClusterAttempt && s.managedResources[attempt].State != domain.ManagedAttemptResourceCleaned {
			resourceCopy := resource
			return &copyJob, &resourceCopy, false, nil
		}
	}
	if resource.State == domain.ManagedAttemptResourceCreating && resource.LeaseExpiresAt != nil && resource.LeaseExpiresAt.After(now) {
		resourceCopy := resource
		return &copyJob, &resourceCopy, resource.LeaseOwner == request.LeaseOwner, nil
	}
	expires := now.Add(request.LeaseDuration)
	resource.State, resource.LeaseOwner, resource.LeaseVersion, resource.ResourceFence, resource.LeaseExpiresAt = domain.ManagedAttemptResourceCreating, request.LeaseOwner, resource.LeaseVersion+1, resource.LeaseVersion+1, &expires
	s.managedResources[request.ExpectedClusterAttempt] = resource
	if s.issuedFences == nil {
		s.issuedFences = map[int]map[int64]bool{}
	}
	if s.issuedFences[request.ExpectedClusterAttempt] == nil {
		s.issuedFences[request.ExpectedClusterAttempt] = map[int64]bool{}
	}
	s.issuedFences[request.ExpectedClusterAttempt][resource.ResourceFence] = true
	resourceCopy := resource
	return &copyJob, &resourceCopy, true, nil
}

func (s *memoryJobStore) AuthorizeManagedAttemptActivation(_ context.Context, request domain.ManagedAttemptActivationRequest) (*domain.TrainingJob, *domain.ManagedAttemptResource, bool, error) {
	s.ensureManagedResources()
	resource, exists := s.managedResources[request.ExpectedClusterAttempt]
	if !exists && s.job.ClusterAttempt == request.ExpectedClusterAttempt && s.job.RayJobName == request.RayJobName && s.job.RayJobUID == request.RayJobUID {
		resource = domain.ManagedAttemptResource{
			JobID: request.JobID, ClusterAttempt: request.ExpectedClusterAttempt,
			KubernetesNS: s.job.KubernetesNS, RayJobName: request.RayJobName, RayJobUID: request.RayJobUID,
			State: domain.ManagedAttemptResourceActive, LeaseVersion: request.ResourceFence, ResourceFence: request.ResourceFence,
		}
		s.managedResources[request.ExpectedClusterAttempt] = resource
	}
	authorized := s.job.DesiredState == domain.DesiredActive && s.job.ClusterAttempt == request.ExpectedClusterAttempt &&
		s.job.RayJobName == request.RayJobName && s.job.RayJobUID == request.RayJobUID &&
		resource.RayJobName == request.RayJobName && resource.RayJobUID == request.RayJobUID && resource.ResourceFence == request.ResourceFence &&
		(resource.State == domain.ManagedAttemptResourceActivating || resource.State == domain.ManagedAttemptResourceActive)
	jobCopy, resourceCopy := *s.job, resource
	return &jobCopy, &resourceCopy, authorized, nil
}

func (s *memoryJobStore) ConfirmManagedAttemptActivation(_ context.Context, request domain.ManagedAttemptActivationRequest) (*domain.TrainingJob, bool, error) {
	s.ensureManagedResources()
	resource := s.managedResources[request.ExpectedClusterAttempt]
	confirmed := s.job.DesiredState == domain.DesiredActive && s.job.ClusterAttempt == request.ExpectedClusterAttempt &&
		s.job.RayJobName == request.RayJobName && s.job.RayJobUID == request.RayJobUID &&
		resource.RayJobName == request.RayJobName && resource.RayJobUID == request.RayJobUID && resource.ResourceFence == request.ResourceFence &&
		(resource.State == domain.ManagedAttemptResourceActivating || resource.State == domain.ManagedAttemptResourceActive)
	if confirmed {
		resource.State = domain.ManagedAttemptResourceActive
		s.managedResources[request.ExpectedClusterAttempt] = resource
	}
	copy := *s.job
	return &copy, confirmed, nil
}

func (s *memoryJobStore) RetireManagedAttemptResource(_ context.Context, request domain.ManagedAttemptRetireRequest) (*domain.ManagedAttemptResource, bool, error) {
	s.ensureManagedResources()
	resource, ok := s.managedResources[request.ClusterAttempt]
	if ok && (resource.KubernetesNS != request.KubernetesNS || resource.RayJobName != request.RayJobName ||
		(resource.RayJobUID != "" && request.RayJobUID != "" && resource.RayJobUID != request.RayJobUID)) {
		return nil, false, errors.New("managed attempt retirement identity mismatch")
	}
	if !ok {
		resource = domain.ManagedAttemptResource{
			JobID: request.JobID, ClusterAttempt: request.ClusterAttempt, KubernetesNS: request.KubernetesNS, RayJobName: request.RayJobName,
			LeaseVersion: 1, ResourceFence: 1,
		}
	}
	if resource.State == domain.ManagedAttemptResourceQuarantined {
		copyResource := resource
		return &copyResource, false, nil
	}
	changed := resource.State != domain.ManagedAttemptResourceRetiring || (resource.RayJobUID == "" && request.RayJobUID != "")
	if resource.RayJobUID == "" {
		resource.RayJobUID = request.RayJobUID
	}
	resource.State, resource.LeaseOwner, resource.LeaseExpiresAt = domain.ManagedAttemptResourceRetiring, "", nil
	resource.NextCheckAt = time.Now().UTC()
	s.managedResources[request.ClusterAttempt] = resource
	copyResource := resource
	return &copyResource, changed, nil
}

func (s *memoryJobStore) ListManagedAttemptCleanup(_ context.Context, limit int, now time.Time) ([]domain.ManagedAttemptResource, error) {
	s.ensureManagedResources()
	items := make([]domain.ManagedAttemptResource, 0, len(s.managedResources))
	for _, resource := range s.managedResources {
		if !resource.NextCheckAt.IsZero() && resource.NextCheckAt.After(now) {
			continue
		}
		if resource.State == domain.ManagedAttemptResourceRetiring ||
			((resource.State == domain.ManagedAttemptResourceReserved || resource.State == domain.ManagedAttemptResourceCreating) &&
				(resource.ClusterAttempt < s.job.ClusterAttempt || s.job.DesiredState == domain.DesiredCanceled || terminalJobState(s.job.ObservedState))) {
			items = append(items, resource)
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].NextCheckAt.Equal(items[j].NextCheckAt) {
			return items[i].ClusterAttempt < items[j].ClusterAttempt
		}
		return items[i].NextCheckAt.Before(items[j].NextCheckAt)
	})
	if limit > 0 && len(items) > limit {
		items = items[:limit]
	}
	return items, nil
}

func (s *memoryJobStore) ListManagedAttemptTombstoneAudit(_ context.Context, limit int, _ time.Time) ([]domain.ManagedAttemptResource, error) {
	items := make([]domain.ManagedAttemptResource, 0)
	for _, resource := range s.managedResources {
		if resource.State == domain.ManagedAttemptResourceCleaned {
			items = append(items, resource)
			if limit > 0 && len(items) == limit {
				break
			}
		}
	}
	return items, nil
}

func (s *memoryJobStore) RecordManagedAttemptCleanupFailure(_ context.Context, request domain.ManagedAttemptCleanupFailureRequest) error {
	resource := s.managedResources[request.ClusterAttempt]
	resource.CleanupFailures++
	resource.CleanupLastError = request.Message
	resource.NextCheckAt = request.ObservedAt.Add(5 * time.Second)
	if request.Permanent {
		resource.State = domain.ManagedAttemptResourceQuarantined
		s.quarantineEvents = append(s.quarantineEvents, request)
	}
	s.managedResources[request.ClusterAttempt] = resource
	return nil
}

func (s *memoryJobStore) CompleteManagedAttemptCleanup(_ context.Context, request domain.ManagedAttemptCleanupRequest) (bool, error) {
	s.ensureManagedResources()
	resource, ok := s.managedResources[request.ClusterAttempt]
	if !ok || (resource.State != domain.ManagedAttemptResourceRetiring && resource.State != domain.ManagedAttemptResourceCleaned) || resource.RayJobName != request.RayJobName || resource.RayJobUID != request.RayJobUID {
		return false, nil
	}
	resource.State = domain.ManagedAttemptResourceCleaned
	resource.RayJobUID = ""
	resource.LeaseOwner = ""
	resource.LeaseExpiresAt = nil
	resource.NextCheckAt = time.Now().UTC().Add(time.Minute)
	s.managedResources[request.ClusterAttempt] = resource
	return true, nil
}

func TestCancellationWinningManagedAdoptionDeletesCreatedAttempt(t *testing.T) {
	attemptTwo := managedEmptyAttempt(2, domain.StateRecovering)
	store := &memoryJobStore{job: &attemptTwo, cancelDuringAdopt: true}
	dynamicClient := newFakeDynamicClient().(*dynamicfake.FakeDynamicClient)
	client := NewClientFromInterfaces(dynamicClient, nil)
	reconciler := NewReconciler(store, client, testRenderOptions())

	if err := reconciler.ReconcileJob(context.Background(), attemptTwo.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := client.GetRayJob(context.Background(), attemptTwo.KubernetesNS, attemptTwo.ID+"-a2"); !apierrors.IsNotFound(err) {
		t.Fatalf("canceled attempt 2 resource was not compensated: %v", err)
	}
	if store.job.ObservedState != domain.StateCanceled {
		t.Fatalf("cancellation did not win the adoption race: %+v", store.job)
	}
}

func (s *memoryJobStore) ClaimOutbox(context.Context, int) ([]domain.OutboxEvent, error) {
	return nil, nil
}

func (s *memoryJobStore) MarkOutboxDone(context.Context, string) error { return nil }

func (s *memoryJobStore) MarkOutboxRetry(context.Context, string, time.Time, string) error {
	return nil
}

func newFakeDynamicClient() dynamic.Interface {
	client := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
	client.PrependReactor("create", "rayjobs", func(action k8stesting.Action) (bool, runtime.Object, error) {
		create := action.(k8stesting.CreateAction)
		resource := create.GetObject().(*unstructured.Unstructured).DeepCopy()
		if resource.GetUID() == "" {
			resource.SetUID(types.UID("uid-" + resource.GetName()))
		}
		createWithOptions, ok := action.(interface{ GetCreateOptions() metav1.CreateOptions })
		if ok && len(createWithOptions.GetCreateOptions().DryRun) > 0 {
			return true, resource, nil
		}
		if err := client.Tracker().Create(rayJobGVR, resource, resource.GetNamespace()); err != nil {
			return true, nil, err
		}
		return true, resource, nil
	})
	return client
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
	dynamicClient := newFakeDynamicClient()
	client := NewClientFromInterfaces(dynamicClient, nil)
	manifest, err := RenderRayJob(job, testRenderOptions())
	if err != nil {
		t.Fatalf("render ray job: %v", err)
	}
	manifest.SetUID("uid-cancel")
	job.RayJobName = manifest.GetName()
	job.RayJobUID = string(manifest.GetUID())
	store := &memoryJobStore{job: &job}
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
	job.RayJobUID = "uid-attempt-1"
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
	manifest.SetUID("uid-attempt-1")
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

func assignFakeRayJobUID(t *testing.T, dynamicClient *dynamicfake.FakeDynamicClient, store *memoryJobStore, namespace, name, uid string) {
	t.Helper()
	resource, err := dynamicClient.Resource(rayJobGVR).Namespace(namespace).Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get fake RayJob %s: %v", name, err)
	}
	resource.SetUID(types.UID(uid))
	if _, err := dynamicClient.Resource(rayJobGVR).Namespace(namespace).Update(context.Background(), resource, metav1.UpdateOptions{}); err != nil {
		t.Fatalf("assign fake apiserver UID to %s: %v", name, err)
	}
	store.job.RayJobUID = uid
	if resource, ok := store.managedResources[store.job.ClusterAttempt]; ok {
		resource.RayJobUID = uid
		store.managedResources[store.job.ClusterAttempt] = resource
	}
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

func TestManagedRecoveryRetiresOldUIDBeforeCreatingNextAttempt(t *testing.T) {
	store, client, dynamicClient, job := managedRecoveryFixture(t)
	dynamicClient.ClearActions()
	reconciler := NewReconciler(store, client, testRenderOptions())
	if err := reconciler.ReconcileJob(context.Background(), job.ID); err != nil {
		t.Fatal(err)
	}
	deleteIndex, createIndex := -1, -1
	for index, action := range dynamicClient.Actions() {
		switch action.GetVerb() {
		case "delete":
			if action.(k8stesting.DeleteAction).GetName() == job.ID {
				deleteIndex = index
			}
		case "create":
			resource := action.(k8stesting.CreateAction).GetObject().(*unstructured.Unstructured)
			if resource.GetName() == job.ID+"-a2" {
				createIndex = index
			}
		}
	}
	if deleteIndex < 0 || createIndex < 0 || deleteIndex >= createIndex {
		t.Fatalf("recovery order must be delete old then create new: delete=%d create=%d actions=%+v", deleteIndex, createIndex, dynamicClient.Actions())
	}
	if len(store.retirementRequests) != 1 || store.retirementRequests[0].RayJobUID != "uid-attempt-1" {
		t.Fatalf("old identity was not retired with its UID: %+v", store.retirementRequests)
	}
}

func TestManagedRecoveryDeleteFailureKeepsRetiringIdentityAndCreatesNothing(t *testing.T) {
	store, client, dynamicClient, job := managedRecoveryFixture(t)
	dynamicClient.PrependReactor("delete", "rayjobs", func(action k8stesting.Action) (bool, runtime.Object, error) {
		if action.(k8stesting.DeleteAction).GetName() == job.ID {
			return true, nil, errors.New("apiserver delete failed")
		}
		return false, nil, nil
	})
	reconciler := NewReconciler(store, client, testRenderOptions())
	if err := reconciler.ReconcileJob(context.Background(), job.ID); err == nil {
		t.Fatal("expected retirement deletion failure")
	}
	if store.job.ClusterAttempt != 2 || store.job.ObservedState != domain.StateRecovering || store.job.RayJobName != job.ID || store.job.RayJobUID != "uid-attempt-1" {
		t.Fatalf("delete failure lost retiring identity: %+v", store.job)
	}
	if _, err := client.GetRayJob(context.Background(), job.KubernetesNS, job.ID+"-a2"); !apierrors.IsNotFound(err) {
		t.Fatalf("new attempt was created before retirement: %v", err)
	}
}

func TestManagedRecoveryWaitsForOldRayJobNotFoundBeforeClearingIdentity(t *testing.T) {
	store, client, dynamicClient, job := managedRecoveryFixture(t)
	allowDelete := false
	dynamicClient.PrependReactor("delete", "rayjobs", func(action k8stesting.Action) (bool, runtime.Object, error) {
		if action.(k8stesting.DeleteAction).GetName() == job.ID && !allowDelete {
			return true, nil, nil
		}
		return false, nil, nil
	})
	reconciler := NewReconciler(store, client, testRenderOptions())
	now := time.Now().UTC()
	reconciler.now = func() time.Time { return now }
	if err := reconciler.ReconcileJob(context.Background(), job.ID); err != nil {
		t.Fatal(err)
	}
	if store.job.RayJobName != job.ID || len(store.retirementRequests) != 0 {
		t.Fatalf("identity cleared before old workload reached NotFound: job=%+v requests=%+v", store.job, store.retirementRequests)
	}
	if _, err := client.GetRayJob(context.Background(), job.KubernetesNS, job.ID+"-a2"); !apierrors.IsNotFound(err) {
		t.Fatalf("new attempt exists while old workload is not quiescent: %v", err)
	}

	allowDelete = true
	now = now.Add(6 * time.Second)
	if err := reconciler.ReconcileJob(context.Background(), job.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := client.GetRayJob(context.Background(), job.KubernetesNS, job.ID+"-a2"); err != nil {
		t.Fatalf("new attempt was not created after retirement: %v", err)
	}
}

func TestTwoReconcilersDoNotCreateAnExtraRecoveryAttempt(t *testing.T) {
	store, client, dynamicClient, job := managedRecoveryFixture(t)
	first := NewReconciler(store, client, testRenderOptions())
	second := NewReconciler(store, client, testRenderOptions())
	if err := first.ReconcileJob(context.Background(), job.ID); err != nil {
		t.Fatal(err)
	}
	assignFakeRayJobUID(t, dynamicClient, store, job.KubernetesNS, job.ID+"-a2", "uid-attempt-2")
	if err := second.ReconcileJob(context.Background(), job.ID); err != nil {
		t.Fatal(err)
	}
	if store.job.ClusterAttempt != 2 || len(store.recoveryRequests) != 1 {
		t.Fatalf("two reconcilers created an extra attempt: job=%+v recoveries=%+v", store.job, store.recoveryRequests)
	}
}

func TestStaleReplicaNeverRecreatesRetiredManagedAttempt(t *testing.T) {
	store, client, dynamicClient, job := managedRecoveryFixture(t)
	staleAttemptOne := job
	first := NewReconciler(store, client, testRenderOptions())
	if err := first.ReconcileJob(context.Background(), job.ID); err != nil {
		t.Fatal(err)
	}
	if store.job.ClusterAttempt != 2 {
		t.Fatalf("first replica did not advance recovery: %+v", store.job)
	}
	if _, err := client.GetRayJob(context.Background(), job.KubernetesNS, job.ID); !apierrors.IsNotFound(err) {
		t.Fatalf("attempt 1 still exists before stale replica resumes: %v", err)
	}
	if _, err := client.GetRayJob(context.Background(), job.KubernetesNS, job.ID+"-a2"); err != nil {
		t.Fatalf("attempt 2 was not created: %v", err)
	}
	assignFakeRayJobUID(t, dynamicClient, store, job.KubernetesNS, job.ID+"-a2", "uid-attempt-2")

	dynamicClient.ClearActions()
	second := NewReconciler(store, client, testRenderOptions())
	if err := second.reconcileLoadedJob(context.Background(), &staleAttemptOne); err != nil {
		t.Fatal(err)
	}
	if _, err := client.GetRayJob(context.Background(), job.KubernetesNS, job.ID); !apierrors.IsNotFound(err) {
		t.Fatalf("stale replica recreated attempt 1: %v", err)
	}
	if _, err := client.GetRayJob(context.Background(), job.KubernetesNS, job.ID+"-a2"); err != nil {
		t.Fatalf("stale replica disturbed attempt 2: %v", err)
	}
	for _, action := range dynamicClient.Actions() {
		if action.GetVerb() == "create" && action.(k8stesting.CreateAction).GetObject().(*unstructured.Unstructured).GetName() == job.ID {
			t.Fatalf("stale replica issued create for attempt 1: %+v", action)
		}
	}
}

func managedEmptyAttempt(attempt int, state domain.State) domain.TrainingJob {
	job := managedRenderJob(domain.RayVersionProduction)
	job.DesiredState = domain.DesiredActive
	job.ObservedState = state
	job.ClusterAttempt = attempt
	job.RayJobName = ""
	job.RayJobUID = ""
	if attempt > 1 {
		job.ResumeCheckpointID = "checkpoint-previous"
		job.Spec.ResolvedDataMounts.Output = &domain.ResolvedDataMount{
			Space: domain.DataSpaceMyRuns, BindingSpace: domain.DataSpaceWorkspace,
			ClaimName: "tenant-data", SubPath: "tenants/tenant-a/users/user-01/runs/job-01",
			MountPath: domain.DataMountOutputPath, ReadOnly: false,
		}
	}
	return job
}

func timePointer(value time.Time) *time.Time { return &value }

func TestStaleEmptyAttemptReservationReconcilesNewerAttemptWithoutCreatingOld(t *testing.T) {
	staleAttemptTwo := managedEmptyAttempt(2, domain.StateRecovering)
	currentAttemptThree := managedEmptyAttempt(3, domain.StateRecovering)
	store := &memoryJobStore{job: &currentAttemptThree}
	dynamicClient := newFakeDynamicClient().(*dynamicfake.FakeDynamicClient)
	client := NewClientFromInterfaces(dynamicClient, nil)
	reconciler := NewReconciler(store, client, testRenderOptions())

	if err := reconciler.reconcileLoadedJob(context.Background(), &staleAttemptTwo); err != nil {
		t.Fatal(err)
	}
	if _, err := client.GetRayJob(context.Background(), staleAttemptTwo.KubernetesNS, staleAttemptTwo.ID+"-a2"); !apierrors.IsNotFound(err) {
		t.Fatalf("stale empty attempt 2 was created: %v", err)
	}
	if _, err := client.GetRayJob(context.Background(), currentAttemptThree.KubernetesNS, currentAttemptThree.ID+"-a3"); err != nil {
		t.Fatalf("current attempt 3 was not reconciled: %v", err)
	}
}

func TestCanceledEmptyAttemptReservationCreatesNothing(t *testing.T) {
	stale := managedEmptyAttempt(2, domain.StateRecovering)
	current := stale
	current.DesiredState = domain.DesiredCanceled
	store := &memoryJobStore{job: &current}
	client := NewClientFromInterfaces(newFakeDynamicClient(), nil)
	reconciler := NewReconciler(store, client, testRenderOptions())

	if err := reconciler.reconcileLoadedJob(context.Background(), &stale); err != nil {
		t.Fatal(err)
	}
	if _, err := client.GetRayJob(context.Background(), stale.KubernetesNS, stale.ID+"-a2"); !apierrors.IsNotFound(err) {
		t.Fatalf("canceled empty attempt was created: %v", err)
	}
	if store.job.ObservedState != domain.StateCanceled {
		t.Fatalf("latest canceled state was not reconciled: %+v", store.job)
	}
}

func TestLosingManagedAdoptionDeletesExactCreatedUIDAndReconcilesNewAttempt(t *testing.T) {
	attemptTwo := managedEmptyAttempt(2, domain.StateRecovering)
	store := &memoryJobStore{job: &attemptTwo, advanceDuringAdopt: true}
	dynamicClient := newFakeDynamicClient().(*dynamicfake.FakeDynamicClient)
	client := NewClientFromInterfaces(dynamicClient, nil)
	reconciler := NewReconciler(store, client, testRenderOptions())

	if err := reconciler.ReconcileJob(context.Background(), attemptTwo.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := client.GetRayJob(context.Background(), attemptTwo.KubernetesNS, attemptTwo.ID+"-a2"); !apierrors.IsNotFound(err) {
		t.Fatalf("losing attempt 2 resource was not compensated: %v", err)
	}
	if _, err := client.GetRayJob(context.Background(), attemptTwo.KubernetesNS, attemptTwo.ID+"-a3"); err != nil {
		t.Fatalf("new attempt 3 was not reconciled after compensation: %v", err)
	}
	if len(store.adoptionRequests) < 2 || store.adoptionRequests[0].RayJobUID == "" {
		t.Fatalf("created resource was not adopted before status classification: %+v", store.adoptionRequests)
	}
}

func TestInitialManagedAttemptConcurrentCreatorUsesOneReservedIdentity(t *testing.T) {
	initial := managedEmptyAttempt(1, domain.StateSubmitted)
	stale := initial
	store := &memoryJobStore{job: &initial}
	dynamicClient := newFakeDynamicClient().(*dynamicfake.FakeDynamicClient)
	client := NewClientFromInterfaces(dynamicClient, nil)
	first := NewReconciler(store, client, testRenderOptions())
	second := NewReconciler(store, client, testRenderOptions())
	if err := first.reconcileLoadedJob(context.Background(), &initial); err != nil {
		t.Fatal(err)
	}
	dynamicClient.ClearActions()
	if err := second.reconcileLoadedJob(context.Background(), &stale); err != nil {
		t.Fatal(err)
	}
	for _, action := range dynamicClient.Actions() {
		if action.GetVerb() == "create" {
			t.Fatalf("second initial-attempt replica issued another create: %+v", action)
		}
	}
	if store.job.ClusterAttempt != 1 || store.job.RayJobName != initial.ID || store.job.RayJobUID == "" {
		t.Fatalf("initial attempt identity was not adopted exactly once: %+v", store.job)
	}
}

func TestReservedManagedAttemptCrashToleranceAbsentAndPresentResource(t *testing.T) {
	for _, resourcePresent := range []bool{false, true} {
		t.Run(fmt.Sprintf("resource-present-%v", resourcePresent), func(t *testing.T) {
			job := managedEmptyAttempt(2, domain.StateRecovering)
			job.RayJobName = job.ID + "-a2"
			store := &memoryJobStore{job: &job, managedResources: map[int]domain.ManagedAttemptResource{
				2: {JobID: job.ID, ClusterAttempt: 2, KubernetesNS: job.KubernetesNS, RayJobName: job.RayJobName, State: domain.ManagedAttemptResourceReserved},
			}}
			dynamicClient := newFakeDynamicClient().(*dynamicfake.FakeDynamicClient)
			client := NewClientFromInterfaces(dynamicClient, nil)
			if resourcePresent {
				options := testRenderOptions()
				options.managedCreationFence = 1
				manifest, err := RenderRayJob(job, options)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := dynamicClient.Resource(rayJobGVR).Namespace(job.KubernetesNS).Create(context.Background(), manifest, metav1.CreateOptions{}); err != nil {
					t.Fatal(err)
				}
			}
			reconciler := NewReconciler(store, client, testRenderOptions())
			if err := reconciler.ReconcileJob(context.Background(), job.ID); err != nil {
				t.Fatal(err)
			}
			if store.job.RayJobName != job.ID+"-a2" || store.job.RayJobUID == "" {
				t.Fatalf("reserved attempt was not created/adopted after restart: %+v", store.job)
			}
		})
	}
}

func TestCreatorCrashAdoptsFailedResourceBeforeManagedRecovery(t *testing.T) {
	job := managedEmptyAttempt(2, domain.StateRecovering)
	job.RayJobName = job.ID + "-a2"
	job.Spec.Managed.MaxFailures = 3
	job.Spec.ResolvedDataMounts.Output = &domain.ResolvedDataMount{
		Space: domain.DataSpaceMyRuns, BindingSpace: domain.DataSpaceWorkspace,
		ClaimName: "tenant-data", SubPath: "tenants/tenant-a/users/user-01/runs/job-01",
		MountPath: domain.DataMountOutputPath, ReadOnly: false,
	}
	store := &memoryJobStore{job: &job, recoveryAllowed: true, recoveryCheckpointID: "checkpoint-current", managedResources: map[int]domain.ManagedAttemptResource{
		2: {JobID: job.ID, ClusterAttempt: 2, KubernetesNS: job.KubernetesNS, RayJobName: job.RayJobName, State: domain.ManagedAttemptResourceCreating, LeaseOwner: "crashed", LeaseVersion: 1, LeaseExpiresAt: timePointer(time.Now().Add(-time.Minute))},
	}, issuedFences: map[int]map[int64]bool{2: {1: true}}}
	dynamicClient := newFakeDynamicClient().(*dynamicfake.FakeDynamicClient)
	client := NewClientFromInterfaces(dynamicClient, nil)
	options := testRenderOptions()
	options.managedCreationFence = 1
	manifest, err := RenderRayJob(job, options)
	if err != nil {
		t.Fatal(err)
	}
	if err := unstructured.SetNestedMap(manifest.Object, map[string]any{
		"jobStatus": "FAILED", "reason": "HEAD_POD_LOST", "message": "head disappeared after create",
	}, "status"); err != nil {
		t.Fatal(err)
	}
	created, err := dynamicClient.Resource(rayJobGVR).Namespace(job.KubernetesNS).Create(context.Background(), manifest, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	reconciler := NewReconciler(store, client, testRenderOptions())
	if err := reconciler.ReconcileJob(context.Background(), job.ID); err != nil {
		t.Fatal(err)
	}
	if err := reconciler.ReconcileJob(context.Background(), job.ID); err != nil {
		t.Fatal(err)
	}
	replacement, err := dynamicClient.Resource(rayJobGVR).Namespace(job.KubernetesNS).Get(context.Background(), job.RayJobName, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := unstructured.SetNestedMap(replacement.Object, map[string]any{
		"jobStatus": "FAILED", "reason": "HEAD_POD_LOST", "message": "replacement head disappeared",
	}, "status"); err != nil {
		t.Fatal(err)
	}
	if _, err := dynamicClient.Resource(rayJobGVR).Namespace(job.KubernetesNS).Update(context.Background(), replacement, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := reconciler.ReconcileJob(context.Background(), job.ID); err != nil {
		t.Fatal(err)
	}
	if len(store.recoveryRequests) != 1 || store.recoveryRequests[0].ExpectedRayJobUID != string(created.GetUID()) {
		t.Fatalf("FAILED resource was classified before exact adoption: created=%s requests=%+v", created.GetUID(), store.recoveryRequests)
	}
	if store.job.ClusterAttempt != 3 || store.job.RayJobName != job.ID+"-a3" || store.job.RayJobUID == "" {
		t.Fatalf("adopted FAILED attempt did not recover to attempt 3: %+v", store.job)
	}
}

func TestCreatorCrashAdoptsFailedResourceAndFailsClosedWithoutCheckpoint(t *testing.T) {
	job := managedEmptyAttempt(2, domain.StateRecovering)
	job.RayJobName = job.ID + "-a2"
	store := &memoryJobStore{job: &job, recoveryAllowed: false, managedResources: map[int]domain.ManagedAttemptResource{
		2: {JobID: job.ID, ClusterAttempt: 2, KubernetesNS: job.KubernetesNS, RayJobName: job.RayJobName, State: domain.ManagedAttemptResourceCreating, LeaseOwner: "crashed", LeaseVersion: 1, LeaseExpiresAt: timePointer(time.Now().Add(-time.Minute))},
	}, issuedFences: map[int]map[int64]bool{2: {1: true}}}
	dynamicClient := newFakeDynamicClient().(*dynamicfake.FakeDynamicClient)
	client := NewClientFromInterfaces(dynamicClient, nil)
	options := testRenderOptions()
	options.managedCreationFence = 1
	manifest, err := RenderRayJob(job, options)
	if err != nil {
		t.Fatal(err)
	}
	if err := unstructured.SetNestedMap(manifest.Object, map[string]any{
		"jobStatus": "FAILED", "reason": "HEAD_POD_LOST", "message": "head disappeared after create",
	}, "status"); err != nil {
		t.Fatal(err)
	}
	created, err := dynamicClient.Resource(rayJobGVR).Namespace(job.KubernetesNS).Create(context.Background(), manifest, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	reconciler := NewReconciler(store, client, testRenderOptions())
	if err := reconciler.ReconcileJob(context.Background(), job.ID); err != nil {
		t.Fatal(err)
	}
	if err := reconciler.ReconcileJob(context.Background(), job.ID); err != nil {
		t.Fatal(err)
	}
	replacement, err := dynamicClient.Resource(rayJobGVR).Namespace(job.KubernetesNS).Get(context.Background(), job.RayJobName, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := unstructured.SetNestedMap(replacement.Object, map[string]any{
		"jobStatus": "FAILED", "reason": "HEAD_POD_LOST", "message": "replacement head disappeared",
	}, "status"); err != nil {
		t.Fatal(err)
	}
	if _, err := dynamicClient.Resource(rayJobGVR).Namespace(job.KubernetesNS).Update(context.Background(), replacement, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := reconciler.ReconcileJob(context.Background(), job.ID); err != nil {
		t.Fatal(err)
	}
	if store.job.ObservedState != domain.StateFailed || store.job.RayJobUID != string(created.GetUID()) {
		t.Fatalf("FAILED resource was not adopted before fail-closed state: %+v", store.job)
	}
}

func TestMissingManagedWorkloadWithoutCheckpointFailsWithoutRecreation(t *testing.T) {
	job := managedRenderJob(domain.RayVersionProduction)
	job.DesiredState = domain.DesiredActive
	job.ObservedState = domain.StateRunning
	job.ClusterAttempt = 1
	job.RayJobName = job.ID
	job.RayJobUID = "uid-attempt-1"
	store := &memoryJobStore{job: &job, recoveryAllowed: false}
	client := NewClientFromInterfaces(newFakeDynamicClient(), nil)
	reconciler := NewReconciler(store, client, testRenderOptions())

	if err := reconciler.ReconcileJob(context.Background(), job.ID); err != nil {
		t.Fatal(err)
	}
	if store.job.ObservedState != domain.StateFailed || store.job.StatusReason != "RAY_CLUSTER_DELETED" {
		t.Fatalf("missing workload did not become an explicit infrastructure failure: %+v", store.job)
	}
	if _, err := client.GetRayJob(context.Background(), job.KubernetesNS, job.ID); !apierrors.IsNotFound(err) {
		t.Fatalf("missing historical workload was recreated: %v", err)
	}
}

func TestManagedPersistedIdentityGetRejectsForeignOwnerAndUID(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*unstructured.Unstructured)
	}{
		{
			name: "foreign owner",
			mutate: func(resource *unstructured.Unstructured) {
				labels := resource.GetLabels()
				labels["ray.io/job-id"] = "other-job"
				resource.SetLabels(labels)
			},
		},
		{
			name: "replaced UID",
			mutate: func(resource *unstructured.Unstructured) {
				resource.SetUID("uid-replacement")
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			job := managedRenderJob(domain.RayVersionProduction)
			job.DesiredState = domain.DesiredActive
			job.ObservedState = domain.StateRunning
			job.ClusterAttempt = 1
			job.RayJobName = job.ID
			job.RayJobUID = "uid-attempt-1"
			resource := managedManifest(t, job)
			resource.SetUID(types.UID(job.RayJobUID))
			test.mutate(resource)
			store := &memoryJobStore{job: &job}
			client := NewClientFromInterfaces(dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), resource), nil)
			reconciler := NewReconciler(store, client, testRenderOptions())

			if err := reconciler.ReconcileJob(context.Background(), job.ID); err == nil {
				t.Fatal("managed reconciler adopted a foreign or replaced RayJob")
			}
			if len(store.observed) != 0 {
				t.Fatalf("foreign resource produced an observation: %+v", store.observed)
			}
		})
	}
}

func TestCancellationDuringRetirementPreventsNewAttempt(t *testing.T) {
	store, client, _, job := managedRecoveryFixture(t)
	store.cancelDuringRetire = true
	reconciler := NewReconciler(store, client, testRenderOptions())
	if err := reconciler.ReconcileJob(context.Background(), job.ID); err != nil {
		t.Fatal(err)
	}
	if store.job.DesiredState != domain.DesiredCanceled || store.job.ObservedState != domain.StateCanceled {
		t.Fatalf("cancellation did not win retirement race: %+v", store.job)
	}
	if _, err := client.GetRayJob(context.Background(), job.KubernetesNS, job.ID+"-a2"); !apierrors.IsNotFound(err) {
		t.Fatalf("new recovery attempt was created after cancellation: %v", err)
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
	if len(store.observed) == 0 || store.observed[len(store.observed)-1].State != domain.StateCanceled {
		t.Fatalf("managed cancellation did not wait for deletion and become canceled: %+v", store.observed)
	}
}

func TestRecoveringCancellationTransitionsDirectlyToCanceled(t *testing.T) {
	store, client, dynamicClient, job := managedRecoveryFixture(t)
	store.job.ClusterAttempt = 2
	store.job.ObservedState = domain.StateRecovering
	store.job.DesiredState = domain.DesiredCanceled
	store.job.ResumeCheckpointID = "checkpoint-4"
	store.job.RayJobName = job.ID + "-a2"
	store.job.RayJobUID = "uid-attempt-2"
	manifest := managedManifest(t, *store.job)
	manifest.SetUID("uid-attempt-2")
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

func TestCancellationAfterRetiringRayJobAlreadyGoneStillReachesCanceled(t *testing.T) {
	store, client, dynamicClient, job := managedRecoveryFixture(t)
	store.job.ClusterAttempt = 2
	store.job.ObservedState = domain.StateRecovering
	store.job.DesiredState = domain.DesiredCanceled

	if err := dynamicClient.Resource(rayJobGVR).Namespace(job.KubernetesNS).Delete(context.Background(), job.ID, metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	reconciler := NewReconciler(store, client, testRenderOptions())
	if err := reconciler.ReconcileJob(context.Background(), job.ID); err != nil {
		t.Fatal(err)
	}
	if store.job.ObservedState != domain.StateCanceled {
		t.Fatalf("missing retiring resource left cancellation stuck: %+v", store.job)
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
	store.job.RayJobUID = "uid-attempt-2"
	manifest := managedManifest(t, *store.job)
	manifest.SetUID("uid-attempt-2")
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

func TestProcessTerminalEventNeverFinalizesRecoveringJob(t *testing.T) {
	job := validRenderJob()
	job.ObservedState = domain.StateRecovering
	store := &memoryJobStore{job: &job}
	finalizer := &recordingExperimentFinalizer{}
	reconciler := NewReconciler(store, NewClientFromInterfaces(newFakeDynamicClient(), nil), testRenderOptions()).WithExperimentFinalizer(finalizer)
	event := domain.OutboxEvent{ID: job.ID + "-terminal", EventType: "TRAINING_JOB_TERMINAL", Payload: []byte(`{"job_id":"` + job.ID + `"}`)}

	if err := reconciler.processEvent(context.Background(), event); err == nil {
		t.Fatal("stale terminal event for RECOVERING was accepted")
	}
	if finalizer.jobID != "" || finalizer.state != "" {
		t.Fatalf("RECOVERING job was finalized in MLflow: %+v", finalizer)
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
