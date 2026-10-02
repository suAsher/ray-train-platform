package k8s

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
	"ray-train-platform-backend/storagesync"
)

func storageSyncGCFixture(t *testing.T) (*batchv1.Job, *corev1.Secret, *corev1.Pod, storagesync.ExecutorGCRequest) {
	t.Helper()
	spec := storageSyncRuntimeSpec("TRANSFER")
	spec.SubjectKind = "run"
	job, secret, err := renderStorageSyncJob(storageSyncRuntimeConfig(), spec)
	if err != nil {
		t.Fatal(err)
	}
	job.UID, job.ResourceVersion = "gc-job", "17"
	secret.UID, secret.ResourceVersion = "gc-request", "13"
	job.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Name: "gc-worker", Namespace: job.Namespace, Labels: job.Labels,
		OwnerReferences: []metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "Job", Name: job.Name, UID: job.UID, Controller: pointerTo(true)}},
	}, Spec: job.Spec.Template.Spec, Status: corev1.PodStatus{
		Phase: corev1.PodSucceeded,
		ContainerStatuses: []corev1.ContainerStatus{{Name: storageSyncContainer, State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0}}}},
	}}
	request := storagesync.ExecutorGCRequest{Executor: storagesync.ExecutorIdentity{
		Namespace: job.Namespace, Name: job.Name, JobUID: string(job.UID), RunID: spec.RunID,
		SubjectKind: spec.SubjectKind, Attempt: spec.Attempt, Generation: spec.Generation, Phase: spec.Phase, Terminated: true,
	}, TTLSeconds: 3600, State: "SUCCEEDED"}
	return job, secret, pod, request
}

func TestStorageSyncExecutorGCAttachesSecretBeforeJobTTLWithCAS(t *testing.T) {
	job, secret, pod, request := storageSyncGCFixture(t)
	kube := fake.NewSimpleClientset(job, secret, pod)
	client := NewStorageSyncClient(kube, storageSyncRuntimeConfig())
	if err := client.ScheduleExecutorGC(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	updated, _ := kube.BatchV1().Jobs(job.Namespace).Get(context.Background(), job.Name, metav1.GetOptions{})
	owned, _ := kube.CoreV1().Secrets(job.Namespace).Get(context.Background(), secret.Name, metav1.GetOptions{})
	if updated.Spec.TTLSecondsAfterFinished == nil || *updated.Spec.TTLSecondsAfterFinished != 3600 {
		t.Fatal("terminal executor was not assigned its retention TTL")
	}
	if len(owned.OwnerReferences) != 1 || owned.OwnerReferences[0].UID != job.UID || owned.OwnerReferences[0].Name != job.Name || owned.OwnerReferences[0].Kind != "Job" {
		t.Fatal("request Secret does not follow its exact Job lifecycle")
	}
	patched := []string{}
	for _, action := range kube.Actions() {
		if action.GetVerb() == "delete" || action.GetVerb() == "update" {
			t.Fatal("GC must use identity-preconditioned patches, never delete or blind update")
		}
		if action.GetVerb() != "patch" {
			continue
		}
		patched = append(patched, action.GetResource().Resource)
		var operations []struct { Op string `json:"op"`; Path string `json:"path"` }
		if err := json.Unmarshal(action.(ktesting.PatchAction).GetPatch(), &operations); err != nil {
			t.Fatal(err)
		}
		if len(operations) < 3 || operations[0].Op != "test" || operations[0].Path != "/metadata/uid" || operations[1].Op != "test" || operations[1].Path != "/metadata/resourceVersion" {
			t.Fatal("mutation lacks UID and resourceVersion compare-and-swap")
		}
	}
	if fmt.Sprint(patched) != "[secrets jobs]" {
		t.Fatalf("cleanup must attach the request before allowing Job deletion: %v", patched)
	}
	if err := client.ScheduleExecutorGC(context.Background(), request); err != nil {
		t.Fatalf("repeated scheduling is not idempotent: %v", err)
	}
}

func TestStorageSyncExecutorGCRejectsUnprovenIdentityOrTermination(t *testing.T) {
	for _, tc := range []struct {
		name string
		mutate func(*batchv1.Job, *corev1.Secret, *corev1.Pod, *storagesync.ExecutorGCRequest)
	}{
		{"active-job", func(j *batchv1.Job, _ *corev1.Secret, _ *corev1.Pod, _ *storagesync.ExecutorGCRequest) { j.Status.Conditions = nil }},
		{"running-pod", func(_ *batchv1.Job, _ *corev1.Secret, p *corev1.Pod, _ *storagesync.ExecutorGCRequest) { p.Status.Phase = corev1.PodRunning }},
		{"no-owned-pod", func(_ *batchv1.Job, _ *corev1.Secret, p *corev1.Pod, _ *storagesync.ExecutorGCRequest) { p.OwnerReferences = nil }},
		{"incomplete-container-state", func(_ *batchv1.Job, _ *corev1.Secret, p *corev1.Pod, _ *storagesync.ExecutorGCRequest) { p.Status.ContainerStatuses = nil }},
		{"init-container-without-exit", func(_ *batchv1.Job, _ *corev1.Secret, p *corev1.Pod, _ *storagesync.ExecutorGCRequest) { p.Spec.InitContainers = []corev1.Container{{Name: "init"}} }},
		{"ephemeral-container-without-exit", func(_ *batchv1.Job, _ *corev1.Secret, p *corev1.Pod, _ *storagesync.ExecutorGCRequest) { p.Spec.EphemeralContainers = []corev1.EphemeralContainer{{EphemeralContainerCommon: corev1.EphemeralContainerCommon{Name: "debug"}}} }},
		{"mutable-request", func(_ *batchv1.Job, s *corev1.Secret, _ *corev1.Pod, _ *storagesync.ExecutorGCRequest) { s.Immutable = pointerTo(false) }},
		{"request-without-uid", func(_ *batchv1.Job, s *corev1.Secret, _ *corev1.Pod, _ *storagesync.ExecutorGCRequest) { s.UID = "" }},
		{"request-digest-changed", func(_ *batchv1.Job, s *corev1.Secret, _ *corev1.Pod, _ *storagesync.ExecutorGCRequest) { s.Data["request.json"] = []byte(`{"subjectKind":"run","runId":"run-123","attempt":1,"generation":2,"phase":"TRANSFER"}`) }},
		{"wrong-owner", func(_ *batchv1.Job, s *corev1.Secret, _ *corev1.Pod, _ *storagesync.ExecutorGCRequest) { s.OwnerReferences = []metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "Job", Name: "other-job", UID: "other-job"}} }},
		{"wrong-request-label", func(_ *batchv1.Job, s *corev1.Secret, _ *corev1.Pod, _ *storagesync.ExecutorGCRequest) { s.Labels = map[string]string{storageSyncRunLabel: "someone-else"} }},
		{"wrong-job-uid", func(_ *batchv1.Job, _ *corev1.Secret, _ *corev1.Pod, r *storagesync.ExecutorGCRequest) { r.Executor.JobUID = "replacement-job" }},
		{"wrong-generation", func(_ *batchv1.Job, _ *corev1.Secret, _ *corev1.Pod, r *storagesync.ExecutorGCRequest) { r.Executor.Generation++ }},
		{"wrong-subject", func(_ *batchv1.Job, _ *corev1.Secret, _ *corev1.Pod, r *storagesync.ExecutorGCRequest) { r.Executor.SubjectKind = "preview" }},
		{"wrong-phase", func(_ *batchv1.Job, _ *corev1.Secret, _ *corev1.Pod, r *storagesync.ExecutorGCRequest) { r.Executor.Phase = "PREVIEW" }},
		{"outside-namespace-allowlist", func(_ *batchv1.Job, _ *corev1.Secret, _ *corev1.Pod, r *storagesync.ExecutorGCRequest) { r.Executor.Namespace = "tenant-user" }},
		{"job-without-resource-version", func(j *batchv1.Job, _ *corev1.Secret, _ *corev1.Pod, _ *storagesync.ExecutorGCRequest) { j.ResourceVersion = "" }},
		{"nonterminal-state", func(_ *batchv1.Job, _ *corev1.Secret, _ *corev1.Pod, r *storagesync.ExecutorGCRequest) { r.State = "RUNNING" }},
		{"zero-ttl", func(_ *batchv1.Job, _ *corev1.Secret, _ *corev1.Pod, r *storagesync.ExecutorGCRequest) { r.TTLSeconds = 0 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			job, secret, pod, request := storageSyncGCFixture(t)
			tc.mutate(job, secret, pod, &request)
			kube := fake.NewSimpleClientset(job, secret, pod)
			if err := NewStorageSyncClient(kube, storageSyncRuntimeConfig()).ScheduleExecutorGC(context.Background(), request); err == nil {
				t.Fatal("unsafe executor accepted for garbage collection")
			}
			for _, action := range kube.Actions() {
				if action.GetVerb() == "patch" || action.GetVerb() == "delete" || action.GetVerb() == "update" {
					t.Fatalf("unsafe executor was mutated via %s", action.GetVerb())
				}
			}
		})
	}
}

func TestStorageSyncExecutorGCLiveOwnedPodCannotHideBehindChangedLabels(t *testing.T) {
	job, secret, pod, request := storageSyncGCFixture(t)
	live := pod.DeepCopy()
	live.Name, live.Labels, live.Status.Phase = "still-running", nil, corev1.PodRunning
	kube := fake.NewSimpleClientset(job, secret, pod, live)
	if err := NewStorageSyncClient(kube, storageSyncRuntimeConfig()).ScheduleExecutorGC(context.Background(), request); err == nil {
		t.Fatal("owned live Pod escaped termination verification through its labels")
	}
	for _, action := range kube.Actions() {
		if action.GetVerb() == "patch" {
			t.Fatal("executor with a live owned Pod was mutated")
		}
	}
}

func TestStorageSyncExecutorGCSecretCASFailureNeverSchedulesJob(t *testing.T) {
	job, secret, pod, request := storageSyncGCFixture(t)
	kube := fake.NewSimpleClientset(job, secret, pod)
	kube.PrependReactor("patch", "secrets", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, fmt.Errorf("conflicting request version")
	})
	if err := NewStorageSyncClient(kube, storageSyncRuntimeConfig()).ScheduleExecutorGC(context.Background(), request); err == nil {
		t.Fatal("ignored request ownership conflict")
	}
	for _, action := range kube.Actions() {
		if action.GetResource().Resource == "jobs" && action.GetVerb() == "patch" {
			t.Fatal("Job TTL scheduled before request ownership succeeded")
		}
	}
}

func TestStorageSyncExecutorGCListUsesOnlyManagedNamespacesAndValidRequests(t *testing.T) {
	job, secret, pod, _ := storageSyncGCFixture(t)
	legacyJob, legacySecret, legacyPod := job.DeepCopy(), secret.DeepCopy(), pod.DeepCopy()
	legacyJob.Namespace, legacySecret.Namespace, legacyPod.Namespace = "legacy-sync", "legacy-sync", "legacy-sync"
	foreignJob, foreignSecret := job.DeepCopy(), secret.DeepCopy()
	foreignJob.Namespace, foreignSecret.Namespace = "tenant-user", "tenant-user"
	invalidJob := job.DeepCopy()
	invalidJob.Name = "unrelated-job"
	config := storageSyncRuntimeConfig()
	config.GCNamespaces = []string{"legacy-sync", "legacy-sync", config.Namespace}
	kube := fake.NewSimpleClientset(job, secret, pod, legacyJob, legacySecret, legacyPod, foreignJob, foreignSecret, invalidJob)
	executors, err := NewStorageSyncClient(kube, config).ListExecutors(context.Background())
	if err != nil || len(executors) != 2 {
		t.Fatalf("allowlist discovery = %v, %v", executors, err)
	}
	for _, executor := range executors {
		if executor.SubjectKind != "run" || executor.Generation != 1 || !executor.Terminated || executor.JobUID != string(job.UID) {
			t.Fatalf("identity or termination lost: %+v", executor)
		}
	}
	lists := 0
	for _, action := range kube.Actions() {
		if action.GetVerb() == "list" && action.GetResource().Resource == "jobs" {
			lists++
			if action.GetNamespace() != config.Namespace && action.GetNamespace() != "legacy-sync" {
				t.Fatal("discovery escaped configured namespaces")
			}
		}
	}
	if lists != 2 {
		t.Fatalf("namespaces were not deduplicated: %d", lists)
	}
}
