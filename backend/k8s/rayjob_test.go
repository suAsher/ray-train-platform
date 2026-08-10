package k8s

import (
	"strings"
	"testing"

	"ray-train-platform-backend/domain"
)

func validRenderJob() domain.TrainingJob {
	return domain.TrainingJob{
		ID:           "job-01",
		TenantID:     "tenant-a",
		UserID:       "user-01",
		KubernetesNS: "tenant-a",
		Spec: domain.JobSpec{
			Name:       "llm-train",
			Image:      "registry.example/ray-train@sha256:" + strings.Repeat("0", 64),
			Source:     domain.CodeSource{Type: "git", URL: "https://git.example/train.git", Commit: "0123456789abcdef"},
			Entrypoint: domain.Entrypoint{Command: []string{"python", "train.py"}, Args: []string{"--epochs", "3"}},
			Resources:  domain.Resources{WorkerReplicas: 2, GPUsPerWorker: 8, CPUPerWorker: 32, MemoryPerWorker: "128Gi"},
			Queue:      "tenant-a-gpu",
		},
	}
}

func testRenderOptions() RenderOptions {
	return RenderOptions{SourceMaterializerImage: "registry.example/source@sha256:" + strings.Repeat("b", 64)}
}

func TestRenderRayJobProducesKueueManagedRayJob(t *testing.T) {
	job := validRenderJob()
	options := testRenderOptions()
	options.ClusterSpecField = "rayClusterConfig"
	manifest, err := RenderRayJob(job, options)
	if err != nil {
		t.Fatalf("render ray job: %v", err)
	}
	if manifest.GetAPIVersion() != "ray.io/v1" || manifest.GetKind() != "RayJob" {
		t.Fatalf("unexpected GVK: %s/%s", manifest.GetAPIVersion(), manifest.GetKind())
	}
	if manifest.GetName() != "llm-train" || manifest.GetNamespace() != "tenant-a" {
		t.Fatalf("unexpected metadata: %s/%s", manifest.GetNamespace(), manifest.GetName())
	}
	labels := manifest.GetLabels()
	if labels["kueue.x-k8s.io/queue-name"] != "tenant-a-gpu" || labels["ray.io/job-id"] != "job-01" {
		t.Fatalf("unexpected labels: %#v", labels)
	}
	spec, ok, err := nestedMap(manifest.Object, "spec")
	if err != nil || !ok {
		t.Fatalf("missing spec: %v", err)
	}
	// No shell prefix: the working directory comes from the runtime env, and a
	// shell operator here would truncate the command Ray actually runs.
	if spec["entrypoint"] != "python train.py --epochs 3" {
		t.Fatalf("unexpected entrypoint: %#v", spec["entrypoint"])
	}
	cluster, ok, err := nestedMap(spec, "rayClusterConfig")
	if err != nil || !ok {
		t.Fatalf("missing ray cluster config: %v", err)
	}
	workers, ok, err := nestedSlice(cluster, "workerGroupSpecs")
	if err != nil || !ok || len(workers) != 1 {
		t.Fatalf("unexpected worker groups: %#v", workers)
	}
	worker, ok := workers[0].(map[string]any)
	if !ok || worker["replicas"] != int64(2) {
		t.Fatalf("unexpected worker spec: %#v", worker)
	}

	workerTemplate, ok, err := nestedMap(worker, "template", "spec")
	if err != nil || !ok {
		t.Fatalf("missing worker pod template: %v", err)
	}
	if workerTemplate["automountServiceAccountToken"] != false {
		t.Fatalf("worker pods must not mount Kubernetes API tokens by default: %#v", workerTemplate)
	}
	initContainers, ok, err := nestedSlice(workerTemplate, "initContainers")
	if err != nil || !ok || len(initContainers) != 1 {
		t.Fatalf("expected source materializer init container: %#v", workerTemplate)
	}
	volumes, ok, err := nestedSlice(workerTemplate, "volumes")
	if err != nil || !ok {
		t.Fatalf("missing volumes: %v", err)
	}
	for _, volume := range volumes {
		v, ok := volume.(map[string]any)
		if !ok {
			continue
		}
		if _, hasHostPath := v["hostPath"]; hasHostPath {
			t.Fatalf("renderer must not emit hostPath: %#v", v)
		}
	}
}

func TestRenderRayJobCanMountIDCClaim(t *testing.T) {
	job := validRenderJob()
	options := testRenderOptions()
	options.ClusterSpecField = "rayClusterSpec"
	options.IDCExistingClaim = "idc-rwx"
	options.IDCMountPath = "/mnt/idc"
	manifest, err := RenderRayJob(job, options)
	if err != nil {
		t.Fatalf("render ray job with IDC claim: %v", err)
	}
	spec, _, _ := nestedMap(manifest.Object, "spec")
	cluster, _, _ := nestedMap(spec, "rayClusterSpec")
	workerGroups, _, _ := nestedSlice(cluster, "workerGroupSpecs")
	worker := workerGroups[0].(map[string]any)
	workerPod, _, _ := nestedMap(worker, "template", "spec")
	volumes, _, _ := nestedSlice(workerPod, "volumes")
	foundPVC := false
	for _, volume := range volumes {
		v := volume.(map[string]any)
		claim, exists := v["persistentVolumeClaim"].(map[string]any)
		if exists && claim["claimName"] == "idc-rwx" {
			foundPVC = true
		}
	}
	if !foundPVC {
		t.Fatalf("expected IDC PVC volume: %#v", volumes)
	}
}

func TestRenderRayJobMaterializesArtifactUsingImmutableKeyAndDigest(t *testing.T) {
	job := validRenderJob()
	digest := strings.Repeat("a", 64)
	objectKey := "tenants/tenant-a/users/user-01/sha256/" + digest + ".zip"
	job.Spec.Source = domain.CodeSource{
		Type:              "artifact",
		ArtifactID:        "artifact-01",
		ArtifactObjectKey: objectKey,
		ArtifactSHA256:    digest,
	}
	options := testRenderOptions()
	options.TOSSecretName = "artifact-tos"
	options.TOSEndpoint = "https://tos.example.invalid"
	options.TOSBucket = "private-sources"

	manifest, err := RenderRayJob(job, options)
	if err != nil {
		t.Fatalf("render artifact ray job: %v", err)
	}
	spec, _, _ := nestedMap(manifest.Object, "spec")
	cluster, _, _ := nestedMap(spec, "rayClusterSpec")
	workers, _, _ := nestedSlice(cluster, "workerGroupSpecs")
	worker := workers[0].(map[string]any)
	pod, _, _ := nestedMap(worker, "template", "spec")
	initContainers, _, _ := nestedSlice(pod, "initContainers")
	init := initContainers[0].(map[string]any)

	command := init["command"].([]any)
	if len(command) != 1 || command[0] != "/usr/local/bin/safe-extract" {
		t.Fatalf("artifact materializer must execute safe-extract directly: %#v", command)
	}
	args := init["args"].([]any)
	joined := make([]string, 0, len(args))
	for _, arg := range args {
		joined = append(joined, arg.(string))
	}
	materializerArgs := strings.Join(joined, " ")
	for _, expected := range []string{"--object-key " + objectKey, "--sha256 " + digest, "--destination /workspace"} {
		if !strings.Contains(materializerArgs, expected) {
			t.Fatalf("artifact materializer missing %q: %q", expected, materializerArgs)
		}
	}
	if strings.Contains(materializerArgs, "unzip") {
		t.Fatalf("artifact materializer must not shell out to unzip: %q", materializerArgs)
	}
}

func TestMapRayJobStatus(t *testing.T) {
	cases := []struct {
		name   string
		status map[string]any
		want   domain.State
	}{
		{name: "pending", status: map[string]any{"jobStatus": "PENDING"}, want: domain.StateQueued},
		{name: "running", status: map[string]any{"jobStatus": "RUNNING", "rayClusterName": "llm-train-cluster"}, want: domain.StateRunning},
		{name: "success", status: map[string]any{"jobStatus": "SUCCEEDED"}, want: domain.StateSucceeded},
		{name: "failed", status: map[string]any{"jobStatus": "FAILED", "message": "exit code 1"}, want: domain.StateFailed},
		{name: "stopped", status: map[string]any{"jobStatus": "STOPPED"}, want: domain.StateCanceled},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			observed := MapRayJobStatus("job-01", tc.status, "rv-1")
			if observed.State != tc.want {
				t.Fatalf("expected %s, got %+v", tc.want, observed)
			}
		})
	}
}
