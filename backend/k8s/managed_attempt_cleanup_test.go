package k8s

import (
	"context"
	"errors"
	"fmt"
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
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
}

func createManagedAttemptResource(t *testing.T, client *dynamicfake.FakeDynamicClient, job domain.TrainingJob, attempt int, name, uid string) {
	t.Helper()
	resource := managedManifest(t, job)
	resource.SetName(name)
	resource.SetUID(types.UID(uid))
	labels := resource.GetLabels()
	labels[managedAttemptIdentityKey] = managedAttemptString(attempt)
	resource.SetLabels(labels)
	annotations := resource.GetAnnotations()
	annotations[managedAttemptIdentityKey] = managedAttemptString(attempt)
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

	if err := reconciler.ProcessOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := dynamicClient.Resource(rayJobGVR).Namespace(job.KubernetesNS).Get(context.Background(), job.ID+"-a2", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("attempt 2 created before attempt 1 reached NotFound: %v", err)
	}
	if _, ok := store.managedResources[1]; !ok {
		t.Fatal("retirement ledger was cleared while exact resource still existed")
	}

	holdDeletion = false
	if err := reconciler.ProcessOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := dynamicClient.Resource(rayJobGVR).Namespace(job.KubernetesNS).Get(context.Background(), job.ID+"-a2", metav1.GetOptions{}); err != nil {
		t.Fatalf("attempt 2 was not created after attempt 1 reached NotFound: %v", err)
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
	if err := reconciler.ProcessOnce(context.Background()); err == nil {
		t.Fatal("expected first cleanup pass to surface deletion failure")
	}
	if _, ok := store.managedResources[1]; !ok {
		t.Fatal("deletion failure lost durable retirement intent")
	}
	if err := reconciler.ProcessOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.managedResources[1]; ok {
		t.Fatal("successful retry did not complete ledger cleanup")
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
	if _, ok := store.managedResources[1]; ok {
		t.Fatal("terminal job retirement ledger was skipped")
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
	if _, ok := store.managedResources[1]; ok {
		t.Fatal("terminal RESERVED intent with no Kubernetes resource was not cleaned")
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
	if err := reconciler.ProcessOnce(context.Background()); err == nil {
		t.Fatal("foreign attempt metadata was accepted")
	}
	if _, ok := store.managedResources[1]; !ok {
		t.Fatal("foreign resource caused ledger completion")
	}
}

func managedAttemptString(attempt int) string {
	return fmt.Sprintf("%d", attempt)
}
