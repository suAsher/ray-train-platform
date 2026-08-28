package k8s

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"
	"ray-train-platform-backend/domain"
)

func retiringAttemptResource(job domain.TrainingJob, attempt int, name, uid string) domain.ManagedAttemptResource {
	return domain.ManagedAttemptResource{
		JobID: job.ID, ClusterAttempt: attempt, KubernetesNS: job.KubernetesNS,
		RayJobName: name, RayJobUID: uid, State: domain.ManagedAttemptResourceRetiring,
		LeaseVersion: 1, ResourceFence: 1,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
}

func createManagedAttemptResource(t *testing.T, client *dynamicfake.FakeDynamicClient, job domain.TrainingJob, attempt int, name, uid string) {
	createManagedAttemptResourceWithFence(t, client, job, attempt, 1, name, uid)
}

func createManagedAttemptResourceWithFence(t *testing.T, client *dynamicfake.FakeDynamicClient, job domain.TrainingJob, attempt int, fence int64, name, uid string) {
	t.Helper()
	resource := managedManifest(t, job)
	resource.SetName(name)
	resource.SetUID(types.UID(uid))
	labels := resource.GetLabels()
	labels[managedAttemptIdentityKey] = managedAttemptString(attempt)
	resource.SetLabels(labels)
	annotations := resource.GetAnnotations()
	annotations[managedAttemptIdentityKey] = managedAttemptString(attempt)
	if fence > 0 {
		fenceText := fmt.Sprintf("%d", fence)
		labels[managedCreationFenceKey] = fenceText
		annotations[managedCreationFenceKey] = fenceText
	} else {
		delete(labels, managedCreationFenceKey)
		delete(annotations, managedCreationFenceKey)
	}
	resource.SetLabels(labels)
	resource.SetAnnotations(annotations)
	if _, err := client.Resource(rayJobGVR).Namespace(job.KubernetesNS).Create(context.Background(), resource, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
}

func TestManagedCleanupWaitsForForegroundNotFoundBeforeCreatingNextAttempt(t *testing.T) {
	job := managedEmptyAttempt(2, domain.StateRecovering)
	job.RayJobName, job.RayJobUID = job.ID, "uid-attempt-1"
	store := &memoryJobStore{job: &job, managedResources: map[int]domain.ManagedAttemptResource{
		1: retiringAttemptResource(job, 1, job.ID, job.RayJobUID),
	}}
	dynamicClient := newFakeDynamicClient().(*dynamicfake.FakeDynamicClient)
	createManagedAttemptResource(t, dynamicClient, job, 1, job.ID, job.RayJobUID)
	holdDeletion := true
	dynamicClient.PrependReactor("delete", "rayjobs", func(action k8stesting.Action) (bool, runtime.Object, error) {
		if holdDeletion {
			return true, nil, nil
		}
		return false, nil, nil
	})
	reconciler := NewReconciler(store, NewClientFromInterfaces(dynamicClient, nil), testRenderOptions())
	reconciler.cleanupWait = 10 * time.Millisecond
	now := time.Now().UTC()
	reconciler.now = func() time.Time { return now }

	if err := reconciler.ProcessOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := dynamicClient.Resource(rayJobGVR).Namespace(job.KubernetesNS).Get(context.Background(), job.ID+"-a2", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("attempt 2 created before attempt 1 reached NotFound: %v", err)
	}
	if _, ok := store.managedResources[1]; !ok {
		t.Fatal("retirement ledger was cleared while exact resource still existed")
	}
	if resource := store.managedResources[1]; resource.CleanupFailures != 1 || !resource.NextCheckAt.After(now) {
		t.Fatalf("pending foreground deletion was not durably backed off: %+v", resource)
	}

	holdDeletion = false
	now = now.Add(6 * time.Second)
	if err := reconciler.ProcessOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := dynamicClient.Resource(rayJobGVR).Namespace(job.KubernetesNS).Get(context.Background(), job.ID+"-a2", metav1.GetOptions{}); err != nil {
		t.Fatalf("attempt 2 was not created after attempt 1 reached NotFound: %v", err)
	}
}

func TestManagedCleanupAcceptsOnlyExactlyIssuedCreationFences(t *testing.T) {
	for _, test := range []struct {
		name        string
		fence       int64
		ledgerFence int64
		issued      map[int64]bool
		wantClean   bool
		wantReason  string
	}{
		{name: "issued old fence", fence: 1, ledgerFence: 3, issued: map[int64]bool{1: true, 3: true}, wantClean: true},
		{name: "copied unissued fence", fence: 2, ledgerFence: 3, issued: map[int64]bool{1: true, 3: true}, wantReason: "unexpected creation fence"},
		{name: "issued current fence", fence: 3, ledgerFence: 3, issued: map[int64]bool{1: true, 3: true}, wantClean: true},
		{name: "missing legacy fence", fence: 0, ledgerFence: 0, issued: map[int64]bool{}, wantReason: "invalid creation fence"},
	} {
		t.Run(test.name, func(t *testing.T) {
			job := managedEmptyAttempt(4, domain.StateCanceled)
			name, uid := job.ID+"-a3", "uid-a3"
			resource := retiringAttemptResource(job, 3, name, uid)
			resource.LeaseVersion, resource.ResourceFence = test.ledgerFence, test.ledgerFence
			store := &memoryJobStore{
				job:              &job,
				managedResources: map[int]domain.ManagedAttemptResource{3: resource},
				issuedFences:     map[int]map[int64]bool{3: test.issued},
			}
			dynamicClient := newFakeDynamicClient().(*dynamicfake.FakeDynamicClient)
			createManagedAttemptResourceWithFence(t, dynamicClient, job, 3, test.fence, name, uid)
			reconciler := NewReconciler(store, NewClientFromInterfaces(dynamicClient, nil), testRenderOptions())
			if err := reconciler.ProcessOnce(context.Background()); err != nil {
				t.Fatal(err)
			}
			got := store.managedResources[3]
			if test.wantClean {
				if got.State != domain.ManagedAttemptResourceCleaned {
					t.Fatalf("issued fence %d was not cleaned: %+v", test.fence, got)
				}
				return
			}
			if got.State != domain.ManagedAttemptResourceQuarantined || !strings.Contains(got.CleanupLastError, test.wantReason) {
				t.Fatalf("unissued fence %d was not quarantined: %+v", test.fence, got)
			}
		})
	}
}

func TestSupersededManagedCreationDeletesExactlyIssuedLowerFence(t *testing.T) {
	job := managedEmptyAttempt(2, domain.StateRecovering)
	job.RayJobName = job.ID + "-a2"
	name, uid := job.RayJobName, "uid-issued-lower"
	store := &memoryJobStore{
		job: &job,
		managedResources: map[int]domain.ManagedAttemptResource{2: {
			JobID: job.ID, ClusterAttempt: 2, KubernetesNS: job.KubernetesNS,
			RayJobName: name, State: domain.ManagedAttemptResourceCreating,
			LeaseOwner: "replica-current", LeaseVersion: 3, ResourceFence: 3,
		}},
		issuedFences: map[int]map[int64]bool{2: {1: true, 3: true}},
	}
	dynamicClient := newFakeDynamicClient().(*dynamicfake.FakeDynamicClient)
	createManagedAttemptResourceWithFence(t, dynamicClient, job, 2, 1, name, uid)
	reconciler := NewReconciler(store, NewClientFromInterfaces(dynamicClient, nil), testRenderOptions())
	resource, err := reconciler.client.GetRayJob(context.Background(), job.KubernetesNS, name)
	if err != nil {
		t.Fatal(err)
	}
	if err := reconciler.removeSupersededManagedCreation(context.Background(), &job, resource, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := reconciler.client.GetRayJob(context.Background(), job.KubernetesNS, name); !apierrors.IsNotFound(err) {
		t.Fatalf("exactly issued lower fence was not deleted: %v", err)
	}
	if len(store.quarantineEvents) != 0 {
		t.Fatalf("issued lower fence was incorrectly quarantined: %+v", store.quarantineEvents)
	}
}

func TestSupersededManagedCreationQuarantinesUnissuedLowerFenceWithoutDeleting(t *testing.T) {
	job := managedEmptyAttempt(2, domain.StateRecovering)
	job.RayJobName = job.ID + "-a2"
	name, uid := job.RayJobName, "uid-unissued-lower"
	store := &memoryJobStore{
		job: &job,
		managedResources: map[int]domain.ManagedAttemptResource{2: {
			JobID: job.ID, ClusterAttempt: 2, KubernetesNS: job.KubernetesNS,
			RayJobName: name, State: domain.ManagedAttemptResourceCreating,
			LeaseOwner: "replica-current", LeaseVersion: 3, ResourceFence: 3,
		}},
		issuedFences: map[int]map[int64]bool{2: {1: true, 3: true}},
	}
	dynamicClient := newFakeDynamicClient().(*dynamicfake.FakeDynamicClient)
	createManagedAttemptResourceWithFence(t, dynamicClient, job, 2, 2, name, uid)
	reconciler := NewReconciler(store, NewClientFromInterfaces(dynamicClient, nil), testRenderOptions())
	resource, err := reconciler.client.GetRayJob(context.Background(), job.KubernetesNS, name)
	if err != nil {
		t.Fatal(err)
	}
	if err := reconciler.removeSupersededManagedCreation(context.Background(), &job, resource, 2); err != nil {
		t.Fatal(err)
	}
	if _, err := reconciler.client.GetRayJob(context.Background(), job.KubernetesNS, name); err != nil {
		t.Fatalf("unissued lower fence was deleted instead of quarantined: %v", err)
	}
	ledger := store.managedResources[2]
	if ledger.State != domain.ManagedAttemptResourceQuarantined || ledger.RayJobUID != uid {
		t.Fatalf("unissued lower fence did not retain durable quarantine identity: %+v", ledger)
	}
	if len(store.quarantineEvents) != 1 || !store.quarantineEvents[0].Permanent || !strings.Contains(store.quarantineEvents[0].Message, "unissued") {
		t.Fatalf("unissued lower fence did not emit quarantine alert intent: %+v", store.quarantineEvents)
	}
}

func TestQuarantinedManagedAttemptReconciliationIsBoundedAndDoesNotBlockUnrelatedJob(t *testing.T) {
	job := managedEmptyAttempt(2, domain.StateRecovering)
	job.RayJobName = job.ID + "-a2"
	name, uid := job.RayJobName, "uid-quarantined"
	quarantine := domain.ManagedAttemptCleanupFailureRequest{
		JobID: job.ID, ClusterAttempt: 2, RayJobName: name, RayJobUID: uid,
		Message: "unissued creation fence 2", Permanent: true, ObservedAt: time.Now().UTC(),
	}
	store := &memoryJobStore{
		job: &job, reservationCallLimit: 4,
		managedResources: map[int]domain.ManagedAttemptResource{2: {
			JobID: job.ID, ClusterAttempt: 2, KubernetesNS: job.KubernetesNS,
			RayJobName: name, RayJobUID: uid, State: domain.ManagedAttemptResourceQuarantined,
			LeaseVersion: 3, ResourceFence: 3,
		}},
		issuedFences:     map[int]map[int64]bool{2: {1: true, 3: true}},
		quarantineEvents: []domain.ManagedAttemptCleanupFailureRequest{quarantine},
	}
	dynamicClient := newFakeDynamicClient().(*dynamicfake.FakeDynamicClient)
	createManagedAttemptResourceWithFence(t, dynamicClient, job, 2, 2, name, uid)
	actionsBefore := len(dynamicClient.Actions())
	reconciler := NewReconciler(store, NewClientFromInterfaces(dynamicClient, nil), testRenderOptions())
	if err := reconciler.ReconcileJob(context.Background(), job.ID); err != nil {
		t.Fatalf("quarantined reconciliation did not return boundedly: %v", err)
	}
	if store.reservationCalls != 1 {
		t.Fatalf("quarantined reconciliation re-entered reservation %d times", store.reservationCalls)
	}
	if ledger := store.managedResources[2]; ledger.State != domain.ManagedAttemptResourceQuarantined {
		t.Fatalf("quarantined ledger changed state: %+v", ledger)
	}
	if len(store.quarantineEvents) != 1 {
		t.Fatalf("durable quarantine alert was lost or duplicated: %+v", store.quarantineEvents)
	}
	if len(dynamicClient.Actions()) != actionsBefore {
		t.Fatalf("quarantined reconciliation touched Kubernetes: before=%d after=%d actions=%+v", actionsBefore, len(dynamicClient.Actions()), dynamicClient.Actions())
	}

	unrelated := managedEmptyAttempt(1, domain.StateSubmitted)
	store.job = &unrelated
	if err := reconciler.ReconcileJob(context.Background(), unrelated.ID); err != nil {
		t.Fatalf("quarantined job blocked unrelated managed reconciliation: %v", err)
	}
	if _, err := reconciler.client.GetRayJob(context.Background(), unrelated.KubernetesNS, unrelated.ID); err != nil {
		t.Fatalf("unrelated managed job did not progress: %v", err)
	}
}

func TestPendingForegroundDeletesBeyondFirstBatchProgressAcrossPasses(t *testing.T) {
	job := managedEmptyAttempt(30, domain.StateCanceled)
	store := &memoryJobStore{job: &job, managedResources: map[int]domain.ManagedAttemptResource{}, issuedFences: map[int]map[int64]bool{}}
	dynamicClient := newFakeDynamicClient().(*dynamicfake.FakeDynamicClient)
	for attempt := 1; attempt <= 25; attempt++ {
		name, uid := fmt.Sprintf("%s-a%d", job.ID, attempt), fmt.Sprintf("uid-%d", attempt)
		store.managedResources[attempt] = retiringAttemptResource(job, attempt, name, uid)
		store.issuedFences[attempt] = map[int64]bool{1: true}
		createManagedAttemptResourceWithFence(t, dynamicClient, job, attempt, 1, name, uid)
	}
	dynamicClient.PrependReactor("delete", "rayjobs", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, nil
	})
	reconciler := NewReconciler(store, NewClientFromInterfaces(dynamicClient, nil), testRenderOptions())
	reconciler.cleanupWait = time.Millisecond
	now := time.Now().UTC()
	reconciler.now = func() time.Time { return now }
	if err := reconciler.ProcessOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	failed := 0
	for _, resource := range store.managedResources {
		if resource.CleanupFailures > 0 {
			failed++
		}
	}
	if failed != 20 {
		t.Fatalf("first pass backed off %d resources, want 20", failed)
	}
	if err := reconciler.ProcessOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	failed = 0
	for _, resource := range store.managedResources {
		if resource.CleanupFailures > 0 {
			failed++
		}
	}
	if failed != 25 {
		t.Fatalf("later urgent rows did not progress on second pass: backed off=%d", failed)
	}
}

func TestManagedCleanupDeletionFailureRemainsDurableAndRetries(t *testing.T) {
	job := managedEmptyAttempt(2, domain.StateRecovering)
	job.RayJobName, job.RayJobUID = job.ID, "uid-attempt-1"
	store := &memoryJobStore{job: &job, managedResources: map[int]domain.ManagedAttemptResource{
		1: retiringAttemptResource(job, 1, job.ID, job.RayJobUID),
	}}
	dynamicClient := newFakeDynamicClient().(*dynamicfake.FakeDynamicClient)
	createManagedAttemptResource(t, dynamicClient, job, 1, job.ID, job.RayJobUID)
	failOnce := true
	dynamicClient.PrependReactor("delete", "rayjobs", func(action k8stesting.Action) (bool, runtime.Object, error) {
		if failOnce {
			failOnce = false
			return true, nil, errors.New("injected deletion failure")
		}
		return false, nil, nil
	})
	reconciler := NewReconciler(store, NewClientFromInterfaces(dynamicClient, nil), testRenderOptions())
	now := time.Now().UTC()
	reconciler.now = func() time.Time { return now }
	if err := reconciler.ProcessOnce(context.Background()); err != nil {
		t.Fatalf("durably recorded cleanup failure blocked the pass: %v", err)
	}
	if _, ok := store.managedResources[1]; !ok {
		t.Fatal("deletion failure lost durable retirement intent")
	}
	if resource := store.managedResources[1]; resource.CleanupFailures != 1 || resource.CleanupLastError == "" {
		t.Fatalf("deletion failure did not record bounded retry state: %+v", resource)
	}
	now = now.Add(6 * time.Second)
	if err := reconciler.ProcessOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if resource := store.managedResources[1]; resource.State != domain.ManagedAttemptResourceCleaned {
		t.Fatalf("successful retry did not retain a cleaned tombstone: %#v", resource)
	}
}

func TestTerminalManagedJobStillRunsGlobalLedgerCleanup(t *testing.T) {
	job := managedEmptyAttempt(1, domain.StateFailed)
	job.RayJobName, job.RayJobUID = job.ID, "uid-attempt-1"
	store := &memoryJobStore{job: &job, managedResources: map[int]domain.ManagedAttemptResource{
		1: retiringAttemptResource(job, 1, job.ID, job.RayJobUID),
	}}
	dynamicClient := newFakeDynamicClient().(*dynamicfake.FakeDynamicClient)
	createManagedAttemptResource(t, dynamicClient, job, 1, job.ID, job.RayJobUID)
	reconciler := NewReconciler(store, NewClientFromInterfaces(dynamicClient, nil), testRenderOptions())
	if err := reconciler.ProcessOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if resource := store.managedResources[1]; resource.State != domain.ManagedAttemptResourceCleaned {
		t.Fatalf("terminal job retirement did not retain a cleaned tombstone: %#v", resource)
	}
	if _, err := dynamicClient.Resource(rayJobGVR).Namespace(job.KubernetesNS).Get(context.Background(), job.ID, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("terminal managed resource was not cleaned: %v", err)
	}
}

func TestTerminalReservedIntentWithoutKubernetesResourceIsRetiredAndCleaned(t *testing.T) {
	job := managedEmptyAttempt(1, domain.StateCanceled)
	job.RayJobName = job.ID
	store := &memoryJobStore{job: &job, managedResources: map[int]domain.ManagedAttemptResource{
		1: {
			JobID: job.ID, ClusterAttempt: 1, KubernetesNS: job.KubernetesNS,
			RayJobName: job.ID, State: domain.ManagedAttemptResourceReserved,
		},
	}}
	reconciler := NewReconciler(store, NewClientFromInterfaces(newFakeDynamicClient(), nil), testRenderOptions())
	if err := reconciler.ProcessOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if resource := store.managedResources[1]; resource.State != domain.ManagedAttemptResourceCleaned {
		t.Fatalf("terminal RESERVED intent did not retain a cleaned tombstone: %#v", resource)
	}
}

func TestManagedCleanupRefusesForeignAttemptIdentity(t *testing.T) {
	job := managedEmptyAttempt(2, domain.StateRecovering)
	job.RayJobName, job.RayJobUID = job.ID, "uid-attempt-1"
	store := &memoryJobStore{job: &job, managedResources: map[int]domain.ManagedAttemptResource{
		1: retiringAttemptResource(job, 1, job.ID, job.RayJobUID),
	}}
	dynamicClient := newFakeDynamicClient().(*dynamicfake.FakeDynamicClient)
	createManagedAttemptResource(t, dynamicClient, job, 9, job.ID, job.RayJobUID)
	reconciler := NewReconciler(store, NewClientFromInterfaces(dynamicClient, nil), testRenderOptions())
	if err := reconciler.ProcessOnce(context.Background()); err != nil {
		t.Fatalf("quarantined foreign attempt blocked reconciliation: %v", err)
	}
	if resource := store.managedResources[1]; resource.State != domain.ManagedAttemptResourceQuarantined {
		t.Fatalf("foreign attempt metadata was not quarantined: %+v", resource)
	}
}

func TestExpiredCreatorResumingAfterTakeoverCleanupCannotLeaveLowerAttempt(t *testing.T) {
	job := managedEmptyAttempt(3, domain.StateCanceled)
	job.RayJobName, job.RayJobUID = job.ID+"-a3", "uid-attempt-3"
	retiring := retiringAttemptResource(job, 2, job.ID+"-a2", "uid-takeover")
	retiring.LeaseVersion, retiring.ResourceFence = 2, 2
	store := &memoryJobStore{
		job: &job, managedResources: map[int]domain.ManagedAttemptResource{2: retiring},
		issuedFences: map[int]map[int64]bool{2: {1: true, 2: true}},
	}
	dynamicClient := newFakeDynamicClient().(*dynamicfake.FakeDynamicClient)
	createManagedAttemptResourceWithFence(t, dynamicClient, job, 2, 2, retiring.RayJobName, retiring.RayJobUID)
	reconciler := NewReconciler(store, NewClientFromInterfaces(dynamicClient, nil), testRenderOptions())

	// Replica B has taken lease version 2, adopted and retired attempt 2. Its
	// foreground deletion reaches NotFound before stale replica A resumes.
	if err := reconciler.ProcessOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.managedResources[2]; !ok {
		t.Fatal("cleanup deleted the durable attempt-2 tombstone")
	}

	// Replica A still holds the manifest rendered under expired fence 1 and
	// resumes its already-authorized Ensure call after B's cleanup.
	staleJob := managedEmptyAttempt(2, domain.StateRecovering)
	staleJob.RayJobName = staleJob.ID + "-a2"
	staleOptions := testRenderOptions()
	staleOptions.managedCreationFence = 1
	staleManifest, err := RenderRayJob(staleJob, staleOptions)
	if err != nil {
		t.Fatal(err)
	}
	if staleManifest.GetLabels()["kueue.x-k8s.io/queue-name"] != "" {
		t.Fatal("expired creator produced an admissible RayJob")
	}
	if _, err := reconciler.client.EnsureRayJob(context.Background(), staleManifest); err != nil {
		t.Fatal(err)
	}
	if err := reconciler.ProcessOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := dynamicClient.Resource(rayJobGVR).Namespace(job.KubernetesNS).Get(context.Background(), staleJob.RayJobName, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("expired creator left a resurrected lower attempt: %v", err)
	}
	if _, ok := store.managedResources[2]; !ok {
		t.Fatal("lower-attempt tombstone did not survive repeated cleanup")
	}
}

func TestCancellationBetweenActivationAuthorizationAndKubernetesUpdateCannotExposeQueue(t *testing.T) {
	job := managedEmptyAttempt(2, domain.StateRecovering)
	store := &memoryJobStore{job: &job}
	dynamicClient := newFakeDynamicClient().(*dynamicfake.FakeDynamicClient)
	canceled := false
	deleteFailed := false
	dynamicClient.PrependReactor("update", "rayjobs", func(action k8stesting.Action) (bool, runtime.Object, error) {
		if !canceled {
			canceled = true
			store.job.DesiredState = domain.DesiredCanceled
			resource := store.managedResources[2]
			resource.State = domain.ManagedAttemptResourceRetiring
			store.managedResources[2] = resource
		}
		return false, nil, nil
	})
	dynamicClient.PrependReactor("delete", "rayjobs", func(action k8stesting.Action) (bool, runtime.Object, error) {
		if !deleteFailed {
			deleteFailed = true
			return true, nil, errors.New("simulated crash-window delete failure")
		}
		return false, nil, nil
	})
	reconciler := NewReconciler(store, NewClientFromInterfaces(dynamicClient, nil), testRenderOptions())
	if err := reconciler.ReconcileJob(context.Background(), job.ID); err == nil {
		t.Fatal("test did not exercise activation compensation crash window")
	}
	if !canceled || store.job.DesiredState != domain.DesiredCanceled {
		t.Fatal("test did not interleave cancellation after DB authorization")
	}
	resource, err := dynamicClient.Resource(rayJobGVR).Namespace(job.KubernetesNS).Get(context.Background(), job.ID+"-a2", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("simulated crash should leave the durably owned resource for retry: %v", err)
	}
	if resource.GetLabels()["kueue.x-k8s.io/queue-name"] != "" {
		t.Fatal("cancellation race left the crash-window resource queued")
	}
	if ledger := store.managedResources[2]; ledger.State != domain.ManagedAttemptResourceRetiring {
		t.Fatalf("cancellation did not persist retirement before compensation: %+v", ledger)
	}
	if err := reconciler.ProcessOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := dynamicClient.Resource(rayJobGVR).Namespace(job.KubernetesNS).Get(context.Background(), job.ID+"-a2", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("durable cancellation cleanup left an orphan after retry: %v", err)
	}
}

func TestMalformedCleanupIsQuarantinedWithoutBlockingUnrelatedReconciliation(t *testing.T) {
	active := validRenderJob()
	store := &memoryJobStore{job: &active, managedResources: map[int]domain.ManagedAttemptResource{
		9: {
			JobID: "job-bad", ClusterAttempt: 9, KubernetesNS: active.KubernetesNS,
			RayJobName: "job-bad-a9", RayJobUID: "uid-bad", State: domain.ManagedAttemptResourceRetiring,
			LeaseVersion: 1,
		},
	}}
	dynamicClient := newFakeDynamicClient().(*dynamicfake.FakeDynamicClient)
	foreign := managedManifest(t, managedEmptyAttempt(9, domain.StateCanceled))
	foreign.SetName("job-bad-a9")
	foreign.SetUID(types.UID("uid-bad"))
	labels := foreign.GetLabels()
	labels["ray.io/job-id"] = "foreign-owner"
	foreign.SetLabels(labels)
	if _, err := dynamicClient.Resource(rayJobGVR).Namespace(active.KubernetesNS).Create(context.Background(), foreign, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	reconciler := NewReconciler(store, NewClientFromInterfaces(dynamicClient, nil), testRenderOptions())
	if err := reconciler.ProcessOnce(context.Background()); err != nil {
		t.Fatalf("quarantined cleanup error escaped the pass: %v", err)
	}
	if resource := store.managedResources[9]; resource.State != domain.ManagedAttemptResourceQuarantined || resource.CleanupLastError == "" {
		t.Fatalf("permanent cleanup mismatch was not durable and alertable: %+v", resource)
	}
	if _, err := dynamicClient.Resource(rayJobGVR).Namespace(active.KubernetesNS).Get(context.Background(), active.ID, metav1.GetOptions{}); err != nil {
		t.Fatalf("bad tombstone blocked unrelated job reconciliation: %v", err)
	}
}

func managedAttemptString(attempt int) string {
	return fmt.Sprintf("%d", attempt)
}
