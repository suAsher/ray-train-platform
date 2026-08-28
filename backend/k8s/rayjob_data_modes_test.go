package k8s

import (
	"encoding/json"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"ray-train-platform-backend/domain"
)

func managedJobWithDataMode(t *testing.T, mode domain.DataMode) domain.TrainingJob {
	t.Helper()
	job := managedRenderJob(domain.RayVersionProduction)
	job.Spec.DataMode = mode
	job.Spec.Input = domain.DataLocation{Space: domain.DataSpaceTeamShared, RelativePath: "datasets/train"}
	job.Spec.Checkpoint = domain.DataLocation{Space: domain.DataSpaceMyRuns, RelativePath: "runs/previous"}
	job.Spec.Output = domain.DataLocation{Space: domain.DataSpaceMyRuns}
	job.Spec.ResolvedDataMounts = domain.ResolvedDataSpaceMounts{
		Input: &domain.ResolvedDataMount{
			Space: domain.DataSpaceTeamShared, BindingSpace: domain.DataSpaceTeamShared,
			ClaimName: "data-team-a", SubPath: "datasets/train", MountPath: domain.DataMountInputPath, ReadOnly: true,
		},
		Checkpoint: &domain.ResolvedDataMount{
			Space: domain.DataSpaceMyRuns, BindingSpace: domain.DataSpaceWorkspace,
			ClaimName: "data-user-01", SubPath: "runs/previous", MountPath: domain.DataMountCheckpointPath, ReadOnly: true,
		},
		Output: &domain.ResolvedDataMount{
			Space: domain.DataSpaceMyRuns, BindingSpace: domain.DataSpaceWorkspace,
			ClaimName: "data-user-01", SubPath: "runs/job-01", MountPath: domain.DataMountOutputPath,
		},
	}
	job.Spec.ResolvedDataRoots = domain.ResolvedDataSpaceRoots{
		Personal: &domain.ResolvedDataRoot{Space: domain.DataSpaceWorkspace, ClaimName: "data-user-01"},
		Team:     &domain.ResolvedDataRoot{Space: domain.DataSpaceTeamShared, ClaimName: "data-team-a", ReadOnly: true},
		Public:   &domain.ResolvedDataRoot{Space: domain.DataSpacePublic, ClaimName: "data-public", ReadOnly: true},
	}
	if mode == domain.DataModeCache {
		job.Spec.Cache = domain.CacheRequest{Mode: domain.CacheModeRuntime, Size: "1Ti", Preload: domain.CachePreloadInput}
	}
	if mode == domain.DataModeRayData {
		config, err := domain.NewRayDataDatasetConfig(domain.RayDataFormatImages, "images")
		if err != nil {
			t.Fatal(err)
		}
		job.Spec.Managed.RayData = config
	}
	return job
}

func TestEveryDataModeKeepsStableContainerPaths(t *testing.T) {
	for _, mode := range []domain.DataMode{domain.DataModeMount, domain.DataModeCache, domain.DataModeRayData} {
		t.Run(string(mode), func(t *testing.T) {
			job := managedJobWithDataMode(t, mode)
			manifest, err := RenderRayJob(job, runtimeCacheRenderOptions())
			if err != nil {
				t.Fatalf("render %s mode: %v", mode, err)
			}
			for name, pod := range map[string]map[string]any{
				"head":   cacheHeadPodSpec(t, manifest.Object),
				"worker": cacheWorkerPodSpec(t, manifest.Object),
			} {
				env := podEnvironment(pod)
				if env["PLATFORM_OUTPUT_PATH"] != domain.DataMountOutputPath {
					t.Fatalf("%s %s changed output path: %#v", mode, name, env)
				}
				if env["PLATFORM_CHECKPOINT_PATH"] != domain.DataMountCheckpointPath {
					t.Fatalf("%s %s changed checkpoint path: %#v", mode, name, env)
				}
				if mode != domain.DataModeCache && env["PLATFORM_DATASET_PATH"] != domain.DataMountInputPath {
					t.Fatalf("%s %s changed governed input path: %#v", mode, name, env)
				}
				for _, root := range []string{domain.PublicStorageMountPath, domain.MyStorageMountPath, domain.TeamStorageMountPath} {
					if !podHasMountPath(pod, root) {
						t.Fatalf("%s %s removed stable storage root %s", mode, name, root)
					}
				}
			}
		})
	}
}

func TestRayDataModeUsesNamedDatasetAndDualNVMeSpilling(t *testing.T) {
	job := managedJobWithDataMode(t, domain.DataModeRayData)
	manifest, err := RenderRayJob(job, runtimeCacheRenderOptions())
	if err != nil {
		t.Fatalf("render ray-data mode: %v", err)
	}
	entrypoint, _, _ := unstructured.NestedString(manifest.Object, "spec", "entrypoint")
	for _, expected := range []string{
		"--data-mode ray-data",
		"--dataset-format images",
		"--dataset-uri /mnt/data/input/images",
	} {
		if !strings.Contains(entrypoint, expected) {
			t.Fatalf("managed entrypoint missing %q: %q", expected, entrypoint)
		}
	}

	worker := cacheWorkerPodSpec(t, manifest.Object)
	spilling := podEnvironment(worker)["RAY_object_spilling_config"]
	var config struct {
		Params struct {
			DirectoryPath []string `json:"directory_path"`
		} `json:"params"`
	}
	if err := json.Unmarshal([]byte(spilling), &config); err != nil {
		t.Fatalf("decode spill config: %v (%q)", err, spilling)
	}
	want := []string{"/mnt/cache/ray-spill/objects", "/mnt/cache2/ray-spill/objects"}
	if len(config.Params.DirectoryPath) != len(want) || config.Params.DirectoryPath[0] != want[0] || config.Params.DirectoryPath[1] != want[1] {
		t.Fatalf("ray-data must spill across both NVMe disks: %#v", config.Params.DirectoryPath)
	}
	if init, ok := worker["initContainers"].([]any); ok && len(init) != 0 {
		t.Fatalf("ray-data must not run the cache preloader: %#v", init)
	}
	assertGovernedDataMount(t, worker, "platform-data-input", "data-team-a", domain.DataMountInputPath, "datasets/train", true)
}

func TestLegacyRayDDPRejectsRayDataMode(t *testing.T) {
	job := validRenderJob()
	job.Spec.DataMode = domain.DataModeRayData
	if manifest, err := RenderRayJob(job, runtimeCacheRenderOptions()); err == nil || manifest != nil {
		t.Fatalf("legacy ray-ddp accepted ray-data mode: manifest=%#v err=%v", manifest, err)
	}
}

func TestRayDataModeRejectsRenderingWithoutDualNVMeCapability(t *testing.T) {
	job := managedJobWithDataMode(t, domain.DataModeRayData)
	manifest, err := RenderRayJob(job, testRenderOptions())
	if err == nil || !strings.Contains(err.Error(), "runtime cache capability is disabled") {
		t.Fatalf("ray-data without dual-NVMe capability returned manifest=%#v err=%v", manifest, err)
	}
	if manifest != nil {
		t.Fatalf("ray-data without dual-NVMe capability rendered a workload: %#v", manifest)
	}
}

func podHasMountPath(pod map[string]any, wanted string) bool {
	containers, _ := pod["containers"].([]any)
	if len(containers) == 0 {
		return false
	}
	mounts, _ := containers[0].(map[string]any)["volumeMounts"].([]any)
	for _, item := range mounts {
		mount, _ := item.(map[string]any)
		if mount["mountPath"] == wanted {
			return true
		}
	}
	return false
}
