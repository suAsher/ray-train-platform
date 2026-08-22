package k8s

import (
	"fmt"
	"strings"
	"testing"

	"ray-train-platform-backend/domain"
)

func TestRenderRayJobMountsResolvedStorageWithoutWorkloadCredentials(t *testing.T) {
	job := validRenderJob()
	job.Spec.ResolvedStorage = domain.ResolvedStorageMounts{
		Dataset: &domain.ResolvedStorageMount{AssetID: "dataset-a", ClaimName: "tos-dataset-ro", RelativePath: "train", MountPath: domain.StorageMountDataset, ReadOnly: true},
		Output:  &domain.ResolvedStorageMount{AssetID: "output-a", ClaimName: "tos-output-rw", RelativePath: "runs/job-01", MountPath: domain.StorageMountOutput, ReadOnly: false},
	}
	options := testRenderOptions()
	manifest, err := RenderRayJob(job, options)
	if err != nil {
		t.Fatal(err)
	}

	headSpec, found, err := nestedMap(manifest.Object, "spec", "rayClusterSpec", "headGroupSpec", "template", "spec")
	if err != nil || !found {
		t.Fatalf("head spec: %v", err)
	}
	workers, found, err := nestedSlice(manifest.Object, "spec", "rayClusterSpec", "workerGroupSpecs")
	if err != nil || !found || len(workers) != 1 {
		t.Fatalf("worker groups: %v", err)
	}
	worker := workers[0].(map[string]any)
	workerSpec, found, err := nestedMap(worker, "template", "spec")
	if err != nil || !found {
		t.Fatalf("worker spec: %v", err)
	}
	submitterSpec, found, err := nestedMap(manifest.Object, "spec", "submitterPodTemplate", "spec")
	if err != nil || !found {
		t.Fatalf("submitter spec: %v", err)
	}

	for name, podSpec := range map[string]map[string]any{"head": headSpec, "worker": workerSpec} {
		assertStorageMount(t, podSpec, "platform-dataset", "tos-dataset-ro", domain.StorageMountDataset, true)
		assertStorageMount(t, podSpec, "platform-output", "tos-output-rw", domain.StorageMountOutput, false)
		env := podEnvironment(podSpec)
		if env["PLATFORM_DATASET_PATH"] != domain.StorageMountDataset+"/train" || env["PLATFORM_OUTPUT_PATH"] != domain.StorageMountOutput+"/runs/job-01" {
			t.Fatalf("%s local data contract mismatch: %#v", name, env)
		}
		for _, forbidden := range []string{"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "TOS_ENDPOINT", "TOS_BUCKET"} {
			if _, exists := env[forbidden]; exists {
				t.Fatalf("%s workload must not receive %s: %#v", name, forbidden, env)
			}
		}
	}
	if strings.Contains(fmt.Sprintf("%#v", submitterSpec), "tos-dataset-ro") || strings.Contains(fmt.Sprintf("%#v", submitterSpec), "tos-output-rw") {
		t.Fatalf("submitter must not mount business data: %#v", submitterSpec)
	}
}

func assertStorageMount(t *testing.T, podSpec map[string]any, volumeName, claimName, mountPath string, readOnly bool) {
	t.Helper()
	volumes, _, _ := nestedSlice(podSpec, "volumes")
	foundVolume := false
	for _, value := range volumes {
		volume, _ := value.(map[string]any)
		if volume["name"] != volumeName {
			continue
		}
		claim, _ := volume["persistentVolumeClaim"].(map[string]any)
		foundVolume = claim["claimName"] == claimName && claim["readOnly"] == readOnly
	}
	if !foundVolume {
		t.Fatalf("missing %s volume for %s: %#v", volumeName, claimName, volumes)
	}
	containers, _, _ := nestedSlice(podSpec, "containers")
	container := containers[0].(map[string]any)
	mounts, _ := container["volumeMounts"].([]any)
	for _, value := range mounts {
		mount, _ := value.(map[string]any)
		if mount["name"] == volumeName && mount["mountPath"] == mountPath && mount["readOnly"] == readOnly {
			return
		}
	}
	t.Fatalf("missing %s mount at %s: %#v", volumeName, mountPath, mounts)
}
