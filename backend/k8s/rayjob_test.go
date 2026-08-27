package k8s

import (
	"fmt"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
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
	if manifest.GetName() != "job-01" || manifest.GetNamespace() != "tenant-a" {
		t.Fatalf("unexpected metadata: %s/%s", manifest.GetNamespace(), manifest.GetName())
	}
	labels := manifest.GetLabels()
	if labels["kueue.x-k8s.io/queue-name"] != "tenant-a-gpu" || labels["ray.io/job-id"] != "job-01" {
		t.Fatalf("unexpected labels: %#v", labels)
	}
	if labels["app.kubernetes.io/part-of"] != "ray-train-platform" {
		t.Fatalf("RayJob must carry the platform ownership label: %#v", labels)
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
	if initContainers, ok, err := nestedSlice(workerTemplate, "initContainers"); err != nil || (ok && len(initContainers) != 0) {
		t.Fatalf("worker source must arrive through the Ray runtime environment, got init containers: %#v", workerTemplate)
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

func TestRenderRayJobPullsTaggedTrainingImagesEveryStart(t *testing.T) {
	job := validRenderJob()
	job.Spec.Image = "registry.example/team/ray-train:cuda121"
	manifest, err := RenderRayJob(job, testRenderOptions())
	if err != nil {
		t.Fatalf("render tagged training image: %v", err)
	}
	spec, _, _ := nestedMap(manifest.Object, "spec")
	cluster, _, _ := nestedMap(spec, "rayClusterSpec")
	headPod, _, _ := nestedMap(cluster, "headGroupSpec", "template", "spec")
	workers, _, _ := nestedSlice(cluster, "workerGroupSpecs")
	worker := workers[0].(map[string]any)
	workerPod, _, _ := nestedMap(worker, "template", "spec")
	for name, pod := range map[string]map[string]any{"head": headPod, "worker": workerPod} {
		containers, _, _ := nestedSlice(pod, "containers")
		container := containers[0].(map[string]any)
		if container["imagePullPolicy"] != "Always" {
			t.Fatalf("%s must refresh a tagged training image, got %#v", name, container)
		}
	}
}

func TestRenderRayJobUsesJobIDSoDisplayNamesCanRepeat(t *testing.T) {
	first := validRenderJob()
	second := validRenderJob()
	second.ID = "job-02"

	firstManifest, err := RenderRayJob(first, testRenderOptions())
	if err != nil {
		t.Fatal(err)
	}
	secondManifest, err := RenderRayJob(second, testRenderOptions())
	if err != nil {
		t.Fatal(err)
	}
	if firstManifest.GetName() == secondManifest.GetName() {
		t.Fatalf("two runs named %q produced the same Kubernetes name %q", first.Spec.Name, firstManifest.GetName())
	}
}

func TestRenderRayJobPreservesPersistedLegacyResourceName(t *testing.T) {
	job := validRenderJob()
	job.RayJobName = "legacy-display-name"
	manifest, err := RenderRayJob(job, testRenderOptions())
	if err != nil {
		t.Fatalf("RenderRayJob() error = %v", err)
	}
	if manifest.GetName() != "legacy-display-name" {
		t.Fatalf("existing job must keep persisted Kubernetes name, got %q", manifest.GetName())
	}
}

func TestRenderRayJobRoutesSingleNodeDDPThroughLauncher(t *testing.T) {
	job := validRenderJob()
	job.Spec.Resources = domain.Resources{WorkerReplicas: 1, GPUsPerWorker: 2, CPUPerWorker: 16, MemoryPerWorker: "64Gi"}
	job.Spec.Execution = domain.ExecutionProfile{Mode: domain.ExecutionModeTorchrun}
	manifest, err := RenderRayJob(job, testRenderOptions())
	if err != nil {
		t.Fatalf("render DDP job: %v", err)
	}
	spec, _, _ := nestedMap(manifest.Object, "spec")
	if got := spec["entrypoint"]; got != "raytrain-launch --mode torchrun --workers 1 --gpus-per-worker 2 -- python train.py --epochs 3" {
		t.Fatalf("unexpected DDP launcher entrypoint: %#v", got)
	}
	cluster, _, _ := nestedMap(spec, "rayClusterSpec")
	workers, _, _ := nestedSlice(cluster, "workerGroupSpecs")
	worker := workers[0].(map[string]any)
	if worker["replicas"] != int64(1) {
		t.Fatalf("single-node DDP must render one worker pod: %#v", worker)
	}
}

func TestRenderRayJobRoutesRayTrainWorkersThroughLauncher(t *testing.T) {
	job := validRenderJob()
	job.Spec.Resources = domain.Resources{WorkerReplicas: 2, GPUsPerWorker: 1, CPUPerWorker: 8, MemoryPerWorker: "32Gi"}
	job.Spec.Execution = domain.ExecutionProfile{Mode: domain.ExecutionModeRayTrain}
	manifest, err := RenderRayJob(job, testRenderOptions())
	if err != nil {
		t.Fatalf("render Ray Train job: %v", err)
	}
	spec, _, _ := nestedMap(manifest.Object, "spec")
	if got := spec["entrypoint"]; got != "raytrain-launch --mode ray_train --workers 2 --gpus-per-worker 1 -- python train.py --epochs 3" {
		t.Fatalf("unexpected Ray Train launcher entrypoint: %#v", got)
	}
}

func TestLegacyRayTrainExecutionModeStillUsesActorLauncher(t *testing.T) {
	job := validRenderJob()
	job.Spec.TrainingEngine = domain.TrainingEngineRayDDP
	job.Spec.Execution = domain.ExecutionProfile{Mode: domain.ExecutionModeRayTrain}
	manifest, err := RenderRayJob(job, testRenderOptions())
	if err != nil {
		t.Fatalf("render legacy Ray Train profile: %v", err)
	}
	entrypoint, _, _ := unstructured.NestedString(manifest.Object, "spec", "entrypoint")
	if !strings.HasPrefix(entrypoint, "raytrain-launch --mode ray_train") {
		t.Fatalf("legacy serialized mode changed meaning: %s", entrypoint)
	}
}

func TestRayDDPUsesPerJobRayVersionWithOptionsOnlyAsLegacyFallback(t *testing.T) {
	for _, test := range []struct {
		name          string
		jobVersion    string
		optionVersion string
		want          string
	}{
		{name: "per-job snapshot wins", jobVersion: domain.RayVersionProduction, optionVersion: domain.RayVersionLegacy, want: domain.RayVersionProduction},
		{name: "option supports pre-migration rows", optionVersion: domain.RayVersionProduction, want: domain.RayVersionProduction},
		{name: "empty values use legacy default", want: domain.RayVersionLegacy},
	} {
		t.Run(test.name, func(t *testing.T) {
			job := validRenderJob()
			job.Spec.TrainingEngine = domain.TrainingEngineRayDDP
			job.Spec.RayVersion = test.jobVersion
			options := testRenderOptions()
			options.RayVersion = test.optionVersion
			manifest, err := RenderRayJob(job, options)
			if err != nil {
				t.Fatalf("render ray-ddp job: %v", err)
			}
			got, _, _ := unstructured.NestedString(manifest.Object, "spec", "rayClusterSpec", "rayVersion")
			if got != test.want {
				t.Fatalf("rayVersion=%q want %q", got, test.want)
			}
		})
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

func TestRenderRayJobRejectsLegacyArtifactSource(t *testing.T) {
	job := validRenderJob()
	digest := strings.Repeat("a", 64)
	objectKey := "tenants/tenant-a/users/user-01/sha256/" + digest + ".zip"
	job.Spec.Source = domain.CodeSource{
		Type:              "artifact",
		ArtifactID:        "artifact-01",
		ArtifactObjectKey: objectKey,
		ArtifactSHA256:    digest,
	}
	_, err := RenderRayJob(job, testRenderOptions())
	if err == nil || !strings.Contains(err.Error(), "unsupported Ray workload code source") {
		t.Fatalf("legacy artifact source must be rejected before a credential can reach a Ray pod: %v", err)
	}
}

func TestRenderRayJobMaterializesRaySDKArchiveFromPersonalPVC(t *testing.T) {
	job := validRenderJob()
	digest := strings.Repeat("a", 64)
	objectKey, err := domain.SourceArtifactObjectKey(job.TenantID, job.UserID, digest)
	if err != nil {
		t.Fatal(err)
	}
	job.Spec.Source = domain.CodeSource{Type: "workspace-archive", ArtifactID: "raypkg-a", ArtifactObjectKey: objectKey, ArtifactSHA256: digest}
	job.Spec.ResolvedDataRoots.Personal = &domain.ResolvedDataRoot{Space: domain.DataSpaceWorkspace, ClaimName: "data-user-01"}
	manifest, err := RenderRayJob(job, testRenderOptions())
	if err != nil {
		t.Fatalf("render Ray SDK archive job: %v", err)
	}
	encoded := fmt.Sprintf("%#v", manifest.Object)
	for _, expected := range []string{"platform-safe-extract.py", "workspace-snapshot-source", "data-user-01"} {
		if !strings.Contains(encoded, expected) {
			t.Fatalf("Ray SDK archive renderer missing %q: %s", expected, encoded)
		}
	}
	for _, forbidden := range []string{"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "TOS_ENDPOINT"} {
		if strings.Contains(encoded, forbidden) {
			t.Fatalf("Ray SDK archive renderer leaked %q: %s", forbidden, encoded)
		}
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
		{name: "kuberay suspended", status: map[string]any{"jobDeploymentStatus": "Suspended"}, want: domain.StateQueued},
		{name: "kuberay initializing", status: map[string]any{"jobDeploymentStatus": "Initializing"}, want: domain.StateProvisioning},
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

func TestMapRayJobStatusUsesDeploymentStatusAsMessage(t *testing.T) {
	observed := MapRayJobStatus("job-01", map[string]any{"jobDeploymentStatus": "Initializing"}, "rv-1")
	if observed.Message != "Initializing" {
		t.Fatalf("expected deployment status to explain an otherwise empty RayJob status, got %+v", observed)
	}
}
