package k8s

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
	"ray-train-platform-backend/config"
	"ray-train-platform-backend/storagesync"
)

func storageSyncRuntimeConfig() config.StorageSyncConfig {
	return config.StorageSyncConfig{
		Enabled: true, Namespace: "sync-system", Image: "registry.example/sync@sha256:" + strings.Repeat("a", 64),
		Bucket: "sync-bucket", Region: "cn-shanghai", Endpoint: "https://tos-cn-shanghai.volces.com",
		CredentialMode: "static-secret", CredentialSecret: "sync-rclone", ServiceAccountName: "sync-worker", WorkClaimName: "sync-work",
		NodeSelector: map[string]string{"raytrain.wellspiking.ai/storage-sync": "true"},
		CPURequest: "500m", CPULimit: "2", MemoryRequest: "512Mi", MemoryLimit: "2Gi",
		EphemeralStorageRequest: "256Mi", EphemeralStorageLimit: "1Gi",
		MaxActiveRuns: 1, MaxFileConcurrency: 4, MaxPartConcurrency: 2,
		MaxBandwidthBytesPerSecond: 104857600, CallbackBaseURL: "http://ray-train-backend:8080",
	}
}

func storageSyncRuntimeSpec(phase string) storagesync.WorkSpec {
	return storagesync.WorkSpec{
		RunID: "run-123", Attempt: 1, Generation: 1, Phase: phase,
		CallbackURL: "http://ray-train-backend:8080/api/v1/internal/storage-sync/runs/run-123", CallbackToken: "attempt-token",
		Mappings: []storagesync.ResolvedMapping{{Source: storagesync.ResolvedLocation{
			Location: storagesync.Location{SpaceID: "idc-original", RelativePath: "dataset"}, Kind: "IDC", NFSServer: "10.0.0.5", NFSRoot: "/export/original",
		}}},
	}
}

func TestStorageSyncReadOnlyPhasesCannotMountWriteCredentials(t *testing.T) {
	for _, phase := range []string{"BROWSE", "PREVIEW", "REVALIDATE"} {
		t.Run(phase, func(t *testing.T) {
			job, request, err := renderStorageSyncJob(storageSyncRuntimeConfig(), storageSyncRuntimeSpec(phase))
			if err != nil { t.Fatal(err) }
			pod := job.Spec.Template.Spec
			if job.Spec.BackoffLimit == nil || *job.Spec.BackoffLimit != 0 || pod.RestartPolicy != corev1.RestartPolicyNever {
				t.Fatal("attempts must never restart implicitly")
			}
			if pod.AutomountServiceAccountToken == nil || *pod.AutomountServiceAccountToken || len(pod.NodeSelector) == 0 { t.Fatal("missing worker isolation") }
			for _, volume := range pod.Volumes {
				if volume.Secret != nil && volume.Secret.SecretName == "sync-rclone" { t.Fatal("read-only phase received write credentials") }
				if volume.NFS != nil && !volume.NFS.ReadOnly { t.Fatal("IDC source is writable") }
			}
			container := pod.Containers[0]
			if container.SecurityContext == nil || !*container.SecurityContext.ReadOnlyRootFilesystem || !*container.SecurityContext.RunAsNonRoot { t.Fatal("worker lacks security context") }
			if _, ok := container.Resources.Limits[corev1.ResourceName("nvidia.com/gpu")]; ok { t.Fatal("sync requested GPUs") }
			if container.Resources.Limits.Cpu().String() != "2" || container.Resources.Requests.Memory().String() != "512Mi" { t.Fatal("resource limits were lost") }
			var payload map[string]any
			if err := json.Unmarshal(request.Data["request.json"], &payload); err != nil { t.Fatal(err) }
			if payload["callbackToken"] != nil && payload["callbackToken"] != "" { t.Fatal("token must be delivered in separate file") }
			if string(request.Data["token"]) != "attempt-token" { t.Fatal("missing attempt token") }
		})
	}
}

func TestStorageSyncTransferCredentialsAndDeterministicAttempt(t *testing.T) {
	cfg, spec := storageSyncRuntimeConfig(), storageSyncRuntimeSpec("TRANSFER")
	first, _, err := renderStorageSyncJob(cfg, spec)
	if err != nil { t.Fatal(err) }
	second, _, err := renderStorageSyncJob(cfg, spec)
	if err != nil || first.Name != second.Name { t.Fatal("Job naming is not deterministic") }
	spec.Attempt++
	next, _, err := renderStorageSyncJob(cfg, spec)
	if err != nil || first.Name == next.Name { t.Fatal("different attempts share Job names") }
	found := false
	for _, volume := range first.Spec.Template.Spec.Volumes { if volume.Secret != nil && volume.Secret.SecretName == cfg.CredentialSecret { found = true } }
	if !found { t.Fatal("transfer lacks configured credential secret") }
	if first.Spec.TTLSecondsAfterFinished != nil { t.Fatal("automatic Job cleanup would erase stop evidence") }
}

func TestStorageSyncWorkerPinsProcessIdentityAndWaitsForTermination(t *testing.T) {
	job, _, err := renderStorageSyncJob(storageSyncRuntimeConfig(), storageSyncRuntimeSpec("TRANSFER"))
	if err != nil { t.Fatal(err) }
	if job.Spec.PodReplacementPolicy == nil || *job.Spec.PodReplacementPolicy != batchv1.Failed { t.Fatal("Job may replace a worker before termination") }
	found := false
	for _, variable := range job.Spec.Template.Spec.Containers[0].Env {
		if variable.Name == "STORAGE_SYNC_POD_UID" && variable.ValueFrom != nil && variable.ValueFrom.FieldRef != nil && variable.ValueFrom.FieldRef.FieldPath == "metadata.uid" { found = true }
	}
	if !found { t.Fatal("worker lacks immutable process identity for the attempt claim") }
}

func TestStorageSyncRendererRejectsUnsafeSpec(t *testing.T) {
	for _, change := range []func(*config.StorageSyncConfig, *storagesync.WorkSpec){
		func(c *config.StorageSyncConfig, s *storagesync.WorkSpec) { c.NodeSelector = nil },
		func(c *config.StorageSyncConfig, s *storagesync.WorkSpec) { s.Phase = "SHELL" },
		func(c *config.StorageSyncConfig, s *storagesync.WorkSpec) { s.Attempt = 0 },
		func(c *config.StorageSyncConfig, s *storagesync.WorkSpec) { s.Mappings[0].Source.NFSRoot = "/" },
		func(c *config.StorageSyncConfig, s *storagesync.WorkSpec) { s.Mappings[0].Source.NFSServer = "server/path" },
	} {
		cfg, spec := storageSyncRuntimeConfig(), storageSyncRuntimeSpec("PREVIEW")
		change(&cfg, &spec)
		if _, _, err := renderStorageSyncJob(cfg, spec); err == nil { t.Fatal("unsafe worker accepted") }
	}
}

func TestStorageSyncPendingPVCDoesNotCreateWorker(t *testing.T) {
	cfg := storageSyncRuntimeConfig()
	kube := fake.NewSimpleClientset(&corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: cfg.WorkClaimName, Namespace: cfg.Namespace}, Status: corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimPending}})
	client := NewStorageSyncClient(kube, cfg)
	if _, err := client.Ensure(context.Background(), storageSyncRuntimeSpec("PREVIEW")); err == nil { t.Fatal("Pending PVC accepted") }
	for _, action := range kube.Actions() { if action.GetVerb() == "create" { t.Fatal("worker resources created despite unavailable checkpoint storage") } }
}

func TestStorageSyncEnsureAdoptsExistingMatchingAttemptAndRejectsCollision(t *testing.T) {
	cfg, spec := storageSyncRuntimeConfig(), storageSyncRuntimeSpec("PREVIEW")
	job, _, err := renderStorageSyncJob(cfg, spec)
	if err != nil { t.Fatal(err) }
	job.UID = "created-before-response-was-lost"
	kube := fake.NewSimpleClientset(job)
	client := NewStorageSyncClient(kube, cfg)
	observation, err := client.Ensure(context.Background(), spec)
	if err != nil || observation.JobUID != string(job.UID) { t.Fatalf("lost creation response not reconciled: %#v %v", observation, err) }
	for _, action := range kube.Actions() { if action.GetVerb() == "create" { t.Fatal("created duplicate resources for existing attempt") } }
	job.Labels[storageSyncRunLabel] = "another-run"
	if _, err := kube.BatchV1().Jobs(cfg.Namespace).Update(context.Background(), job, metav1.UpdateOptions{}); err != nil { t.Fatal(err) }
	if _, err := client.Ensure(context.Background(), spec); err == nil { t.Fatal("foreign deterministic Job adopted") }
}

func TestStorageSyncEnsureCreatesExactlyOneWorkerAndProtectedRequest(t *testing.T) {
	cfg, spec := storageSyncRuntimeConfig(), storageSyncRuntimeSpec("PREVIEW")
	kube := fake.NewSimpleClientset(&corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: cfg.WorkClaimName, Namespace: cfg.Namespace}, Status: corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimBound}})
	client := NewStorageSyncClient(kube, cfg)
	if _, err := client.Ensure(context.Background(), spec); err != nil { t.Fatal(err) }
	if _, err := client.Ensure(context.Background(), spec); err != nil { t.Fatal(err) }
	jobs, err := kube.BatchV1().Jobs(cfg.Namespace).List(context.Background(), metav1.ListOptions{})
	if err != nil || len(jobs.Items) != 1 { t.Fatalf("unexpected jobs: %#v %v", jobs, err) }
	secrets, err := kube.CoreV1().Secrets(cfg.Namespace).List(context.Background(), metav1.ListOptions{})
	if err != nil || len(secrets.Items) != 1 || secrets.Items[0].Immutable == nil || !*secrets.Items[0].Immutable { t.Fatal("request must be immutable and unique") }
}

func TestStorageSyncMissingOrUnknownWorkerIsNotStopProof(t *testing.T) {
	cfg, spec := storageSyncRuntimeConfig(), storageSyncRuntimeSpec("TRANSFER")
	job, _, err := renderStorageSyncJob(cfg, spec)
	if err != nil { t.Fatal(err) }
	job.UID = "job-uid"
	job.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}}
	kube := fake.NewSimpleClientset(job)
	client := NewStorageSyncClient(kube, cfg)
	observation, err := client.Observe(context.Background(), spec.RunID, spec.Attempt)
	if err != nil || observation.Terminated || observation.RequestsDrained { t.Fatalf("missing Pod treated as stop proof: %#v %v", observation, err) }
	if err := kube.BatchV1().Jobs(cfg.Namespace).Delete(context.Background(), job.Name, metav1.DeleteOptions{}); err != nil { t.Fatal(err) }
	observation, err = client.Observe(context.Background(), spec.RunID, spec.Attempt)
	if err != nil || observation.Terminated || observation.RequestsDrained { t.Fatalf("missing Job treated as stop proof: %#v %v", observation, err) }
}

func TestStorageSyncTerminalPodDoesNotInventRequestDrainProof(t *testing.T) {
	cfg, spec := storageSyncRuntimeConfig(), storageSyncRuntimeSpec("TRANSFER")
	job, _, err := renderStorageSyncJob(cfg, spec)
	if err != nil { t.Fatal(err) }
	job.UID = "job-uid"
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "sync-pod", Namespace: cfg.Namespace, Labels: job.Labels, OwnerReferences: []metav1.OwnerReference{{Kind: "Job", UID: job.UID, Controller: pointerTo(true)}}}, Status: corev1.PodStatus{Phase: corev1.PodSucceeded, ContainerStatuses: []corev1.ContainerStatus{{Name: "storage-sync", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0, FinishedAt: metav1.NewTime(time.Now())}}}}}}
	client := NewStorageSyncClient(fake.NewSimpleClientset(job, pod), cfg)
	observation, err := client.Observe(context.Background(), spec.RunID, spec.Attempt)
	if err != nil || !observation.Terminated || observation.RequestsDrained { t.Fatalf("invalid stop evidence: %#v %v", observation, err) }
}

func TestStorageSyncStopPreservesWorkerEvidenceAndUsesUIDPrecondition(t *testing.T) {
	cfg, spec := storageSyncRuntimeConfig(), storageSyncRuntimeSpec("TRANSFER")
	job, _, err := renderStorageSyncJob(cfg, spec)
	if err != nil { t.Fatal(err) }
	job.UID = types.UID("job-uid")
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "sync-pod", UID: "pod-uid", Namespace: cfg.Namespace, Labels: job.Labels, OwnerReferences: []metav1.OwnerReference{{Kind: "Job", UID: job.UID, Controller: pointerTo(true)}}}}
	kube := fake.NewSimpleClientset(job, pod)
	client := NewStorageSyncClient(kube, cfg)
	if err := client.Stop(context.Background(), spec.RunID, spec.Attempt); err != nil { t.Fatal(err) }
	patched := false
	for _, action := range kube.Actions() {
		if action.GetVerb() == "delete" { t.Fatal("stop deleted worker termination evidence") }
		if action.GetVerb() != "patch" { continue }
		patchAction := action.(ktesting.PatchAction)
		if patchAction.GetPatchType() != types.JSONPatchType || !strings.Contains(string(patchAction.GetPatch()), `"/metadata/uid"`) || !strings.Contains(string(patchAction.GetPatch()), `"job-uid"`) { t.Fatal("stop lacks UID guard") }
		patched = true
	}
	if !patched { t.Fatal("stop request was not recorded") }
}
