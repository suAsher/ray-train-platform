package k8s

import (
	"fmt"
	"testing"

	"ray-train-platform-backend/domain"
)

func TestRenderRayJobMountsResolvedDataSpacesWithoutWorkloadCredentials(t *testing.T) {
	job := validRenderJob()
	job.Spec.ResolvedDataMounts = domain.ResolvedDataSpaceMounts{
		Input: &domain.ResolvedDataMount{
			Space: domain.DataSpaceTeamShared, BindingSpace: domain.DataSpaceTeamShared,
			ClaimName: "data-team-a", SubPath: "datasets/v1", MountPath: domain.DataMountInputPath, ReadOnly: true,
		},
		Checkpoint: &domain.ResolvedDataMount{
			Space: domain.DataSpaceIDCOriginal, BindingSpace: domain.DataSpaceIDCOriginal,
			ClaimName: "idc-original-ro", SubPath: "models/base", MountPath: domain.DataMountCheckpointPath, ReadOnly: true,
		},
		Output: &domain.ResolvedDataMount{
			Space: domain.DataSpaceMyRuns, BindingSpace: domain.DataSpaceWorkspace,
			ClaimName: "data-user-01", SubPath: "runs/job-01", MountPath: domain.DataMountOutputPath, ReadOnly: false,
		},
	}
	job.Spec.ResolvedDataRoots = domain.ResolvedDataSpaceRoots{
		Personal:    &domain.ResolvedDataRoot{Space: domain.DataSpaceWorkspace, ClaimName: "data-user-01"},
		Team:        &domain.ResolvedDataRoot{Space: domain.DataSpaceTeamShared, ClaimName: "data-team-a", ReadOnly: true},
		Public:      &domain.ResolvedDataRoot{Space: domain.DataSpacePublic, ClaimName: "data-public", ReadOnly: true},
		IDCOriginal: &domain.ResolvedDataRoot{Space: domain.DataSpaceIDCOriginal, ClaimName: "idc-original-ro", ReadOnly: true},
	}

	options := testRenderOptions()
	options.IDCExistingClaim = "legacy-idc-rwx"
	manifest, err := RenderRayJob(job, options)
	if err != nil {
		t.Fatal(err)
	}
	head, found, err := nestedMap(manifest.Object, "spec", "rayClusterSpec", "headGroupSpec", "template", "spec")
	if err != nil || !found {
		t.Fatalf("head spec: %v", err)
	}
	workers, found, err := nestedSlice(manifest.Object, "spec", "rayClusterSpec", "workerGroupSpecs")
	if err != nil || !found || len(workers) != 1 {
		t.Fatalf("worker groups: %v", err)
	}
	worker, found, err := nestedMap(workers[0].(map[string]any), "template", "spec")
	if err != nil || !found {
		t.Fatalf("worker spec: %v", err)
	}
	submitter, found, err := nestedMap(manifest.Object, "spec", "submitterPodTemplate", "spec")
	if err != nil || !found {
		t.Fatalf("submitter spec: %v", err)
	}

	for name, pod := range map[string]map[string]any{"head": head, "worker": worker} {
		assertStorageMount(t, pod, "platform-data-personal", "data-user-01", domain.MyStorageMountPath, false)
		assertStorageMount(t, pod, "platform-data-team", "data-team-a", domain.TeamStorageMountPath, true)
		assertStorageMount(t, pod, "platform-data-public", "data-public", domain.PublicStorageMountPath, true)
		assertStorageMount(t, pod, "platform-data-idc-original", "idc-original-ro", domain.IDCOriginalMountPath, true)
		assertGovernedDataMount(t, pod, "platform-data-input", "data-team-a", domain.DataMountInputPath, "datasets/v1", true)
		assertGovernedDataMount(t, pod, "platform-data-checkpoint", "idc-original-ro", domain.DataMountCheckpointPath, "models/base", true)
		assertGovernedDataMount(t, pod, "platform-data-output", "data-user-01", domain.DataMountOutputPath, "runs/job-01", false)
		env := podEnvironment(pod)
		for key, want := range map[string]string{
			"PLATFORM_DATASET_PATH":    domain.DataMountInputPath,
			"PLATFORM_CHECKPOINT_PATH": domain.DataMountCheckpointPath,
			"PLATFORM_OUTPUT_PATH":     domain.DataMountOutputPath,
		} {
			if env[key] != want {
				t.Fatalf("%s %s=%q, want %q", name, key, env[key], want)
			}
		}
		for _, forbidden := range []string{"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "TOS_ENDPOINT", "TOS_BUCKET", "AWS_SESSION_TOKEN"} {
			if _, exists := env[forbidden]; exists {
				t.Fatalf("%s workload must not receive %s: %#v", name, forbidden, env)
			}
		}
		assertNoWorkspaceDataRoot(t, pod)
		if hasVolumeOrMountNamed(pod, "idc-storage") {
			t.Fatalf("%s pod must not retain the legacy IDC mount when governed IDC roots are available: %#v", name, pod)
		}
	}
	if rendered := fmt.Sprintf("%#v", submitter); rendered == "" || containsAny(rendered, "data-team-a", "data-user-01", "idc-original-ro") {
		t.Fatalf("submitter must not mount business data: %s", rendered)
	}
}

func TestRenderRayJobStagesTenantRootOnceForRootsAndSelections(t *testing.T) {
	job := validRenderJob()
	job.Spec.ResolvedDataRoots = domain.ResolvedDataSpaceRoots{
		Personal: &domain.ResolvedDataRoot{Space: domain.DataSpaceWorkspace, ClaimName: "data-tenant-a", SubPath: "tenants/tenant-a/users/guofeng.su"},
		Team:     &domain.ResolvedDataRoot{Space: domain.DataSpaceTeamShared, ClaimName: "data-tenant-a", SubPath: "tenants/tenant-a/shared", ReadOnly: true},
		Public:   &domain.ResolvedDataRoot{Space: domain.DataSpacePublic, ClaimName: "data-tenant-a", SubPath: "public", ReadOnly: true},
	}
	job.Spec.ResolvedDataMounts = domain.ResolvedDataSpaceMounts{
		Input:  &domain.ResolvedDataMount{Space: domain.DataSpaceTeamShared, BindingSpace: domain.DataSpaceTeamShared, ClaimName: "data-tenant-a", SubPath: "tenants/tenant-a/shared/datasets/v1", MountPath: domain.DataMountInputPath, ReadOnly: true},
		Output: &domain.ResolvedDataMount{Space: domain.DataSpaceMyRuns, BindingSpace: domain.DataSpaceWorkspace, ClaimName: "data-tenant-a", SubPath: "tenants/tenant-a/users/guofeng.su/runs/job-01", MountPath: domain.DataMountOutputPath, ReadOnly: false},
	}
	manifest, err := RenderRayJob(job, testRenderOptions())
	if err != nil {
		t.Fatal(err)
	}
	head, _, _ := nestedMap(manifest.Object, "spec", "rayClusterSpec", "headGroupSpec", "template", "spec")
	workers, _, _ := nestedSlice(manifest.Object, "spec", "rayClusterSpec", "workerGroupSpecs")
	worker, _, _ := nestedMap(workers[0].(map[string]any), "template", "spec")
	for name, pod := range map[string]map[string]any{"head": head, "worker": worker} {
		if countPVCVolumes(pod, "data-tenant-a") != 1 {
			t.Fatalf("%s must stage the TOS root only once: %#v", name, pod["volumes"])
		}
		assertStorageMountWithSubPath(t, pod, "platform-data-personal", "data-tenant-a", domain.MyStorageMountPath, "tenants/tenant-a/users/guofeng.su", false)
		assertStorageMountWithSubPath(t, pod, "platform-data-personal", "data-tenant-a", domain.TeamStorageMountPath, "tenants/tenant-a/shared", true)
		assertStorageMountWithSubPath(t, pod, "platform-data-personal", "data-tenant-a", domain.DataMountInputPath, "tenants/tenant-a/shared/datasets/v1", true)
		assertStorageMountWithSubPath(t, pod, "platform-data-personal", "data-tenant-a", domain.DataMountOutputPath, "tenants/tenant-a/users/guofeng.su/runs/job-01", false)
	}
}

func TestRenderRayJobStagesTenantRootOnceWhenPublicInputIsReadOnly(t *testing.T) {
	job := validRenderJob()
	job.Spec.ResolvedDataRoots = domain.ResolvedDataSpaceRoots{
		Personal: &domain.ResolvedDataRoot{Space: domain.DataSpaceWorkspace, ClaimName: "data-tenant-a", SubPath: "tenants/tenant-a/users/guofeng.su"},
		Team:     &domain.ResolvedDataRoot{Space: domain.DataSpaceTeamShared, ClaimName: "data-tenant-a", SubPath: "tenants/tenant-a/shared", ReadOnly: true},
		Public:   &domain.ResolvedDataRoot{Space: domain.DataSpacePublic, ClaimName: "data-tenant-a", SubPath: "public", ReadOnly: true},
	}
	job.Spec.ResolvedDataMounts = domain.ResolvedDataSpaceMounts{
		Input:  &domain.ResolvedDataMount{Space: domain.DataSpacePublic, BindingSpace: domain.DataSpacePublic, ClaimName: "data-tenant-a", SubPath: "public/datasets/v1", MountPath: domain.DataMountInputPath, ReadOnly: true},
		Output: &domain.ResolvedDataMount{Space: domain.DataSpaceMyRuns, BindingSpace: domain.DataSpaceWorkspace, ClaimName: "data-tenant-a", SubPath: "tenants/tenant-a/users/guofeng.su/runs/job-01", MountPath: domain.DataMountOutputPath, ReadOnly: false},
	}

	manifest, err := RenderRayJob(job, testRenderOptions())
	if err != nil {
		t.Fatal(err)
	}
	head, _, _ := nestedMap(manifest.Object, "spec", "rayClusterSpec", "headGroupSpec", "template", "spec")
	if countPVCVolumes(head, "data-tenant-a") != 1 {
		t.Fatalf("a public read-only input must reuse the writable tenant root volume; got %#v", head["volumes"])
	}
	assertStorageMountWithSubPath(t, head, "platform-data-personal", "data-tenant-a", domain.DataMountInputPath, "public/datasets/v1", true)
	assertStorageMountWithSubPath(t, head, "platform-data-personal", "data-tenant-a", domain.DataMountOutputPath, "tenants/tenant-a/users/guofeng.su/runs/job-01", false)
}

func TestRenderRayJobMaterializesWorkspaceSnapshotFromOwnerScopedPersonalPVC(t *testing.T) {
	job := validRenderJob()
	job.Spec.Source = domain.CodeSource{Type: "workspace", Snapshot: "snapshot-a"}
	job.Spec.ResolvedDataRoots = domain.ResolvedDataSpaceRoots{
		Personal: &domain.ResolvedDataRoot{Space: domain.DataSpaceWorkspace, ClaimName: "data-user-a"},
	}
	options := testRenderOptions()
	options.IDCExistingClaim = "legacy-idc-rwx"
	manifest, err := RenderRayJob(job, options)
	if err != nil {
		t.Fatal(err)
	}
	submitter, _, _ := nestedMap(manifest.Object, "spec", "submitterPodTemplate", "spec")

	for name, pod := range map[string]map[string]any{"submitter": submitter} {
		initContainers, _, _ := nestedSlice(pod, "initContainers")
		init := initContainers[0].(map[string]any)
		args, _ := init["args"].([]any)
		if len(args) != 1 || !containsAny(args[0].(string), "/mnt/platform-workspace-snapshot/snapshots/snapshot-a/") {
			t.Fatalf("%s workspace materializer did not use the scoped snapshot mount: %#v", name, init)
		}
		initMounts, _ := init["volumeMounts"].([]any)
		foundSourceMount := false
		for _, value := range initMounts {
			mount, _ := value.(map[string]any)
			if mount["name"] == "workspace-snapshot-source" && mount["mountPath"] == "/mnt/platform-workspace-snapshot" && mount["readOnly"] == true {
				foundSourceMount = true
			}
		}
		if !foundSourceMount {
			t.Fatalf("%s init container is missing read-only snapshot source mount: %#v", name, initMounts)
		}
		if hasVolumeOrMountNamed(pod, "idc-storage") {
			t.Fatalf("%s must not use the legacy IDC snapshot path: %#v", name, pod)
		}
		if rendered := fmt.Sprintf("%#v", pod); containsAny(rendered, "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "TOS_ENDPOINT") {
			t.Fatalf("%s must not receive object-storage credentials: %s", name, rendered)
		}
	}
}

func TestWorkspaceSnapshotMaterializerUsesConfinedTenantRootSubPath(t *testing.T) {
	job := validRenderJob()
	job.Spec.Source = domain.CodeSource{Type: "workspace", Snapshot: "snapshot-a"}
	job.Spec.ResolvedDataRoots = domain.ResolvedDataSpaceRoots{
		Personal: &domain.ResolvedDataRoot{Space: domain.DataSpaceWorkspace, ClaimName: "data-tenant-a", SubPath: "tenants/tenant-a/users/guofeng.su"},
	}
	manifest, err := RenderRayJob(job, testRenderOptions())
	if err != nil {
		t.Fatal(err)
	}
	submitter, _, _ := nestedMap(manifest.Object, "spec", "submitterPodTemplate", "spec")
	initContainers, _, _ := nestedSlice(submitter, "initContainers")
	mounts, _ := initContainers[0].(map[string]any)["volumeMounts"].([]any)
	for _, value := range mounts {
		mount, _ := value.(map[string]any)
		if mount["name"] == "workspace-snapshot-source" && mount["subPath"] == "tenants/tenant-a/users/guofeng.su" && mount["readOnly"] == true {
			return
		}
	}
	t.Fatalf("snapshot materializer is not confined to the personal root: %#v", mounts)
}

func TestWorkspaceSnapshotMaterializerDoesNotPreserveRootOwnedMetadata(t *testing.T) {
	job := validRenderJob()
	job.Spec.Source = domain.CodeSource{Type: "workspace", Snapshot: "snapshot-a"}
	job.Spec.ResolvedDataRoots = domain.ResolvedDataSpaceRoots{
		Personal: &domain.ResolvedDataRoot{Space: domain.DataSpaceWorkspace, ClaimName: "data-user-a"},
	}
	manifest, err := RenderRayJob(job, testRenderOptions())
	if err != nil {
		t.Fatal(err)
	}
	submitter, _, _ := nestedMap(manifest.Object, "spec", "submitterPodTemplate", "spec")
	initContainers, _, _ := nestedSlice(submitter, "initContainers")
	init := initContainers[0].(map[string]any)
	args, _ := init["args"].([]any)
	if len(args) != 1 {
		t.Fatalf("workspace materializer command is missing: %#v", init)
	}
	command := args[0].(string)
	if !containsAny(command, "cp -R ") {
		t.Fatalf("workspace materializer must copy snapshot contents without preserving metadata: %q", command)
	}
	if containsAny(command, "cp -a ") {
		t.Fatalf("workspace materializer must not preserve root-owned metadata: %q", command)
	}
}

func TestWorkspaceSnapshotMaterializerUsesRayStorageIdentity(t *testing.T) {
	job := validRenderJob()
	job.Spec.Source = domain.CodeSource{Type: "workspace", Snapshot: "snapshot-a"}
	job.Spec.ResolvedDataRoots = domain.ResolvedDataSpaceRoots{
		Personal: &domain.ResolvedDataRoot{Space: domain.DataSpaceWorkspace, ClaimName: "data-user-a"},
	}
	manifest, err := RenderRayJob(job, testRenderOptions())
	if err != nil {
		t.Fatal(err)
	}
	submitter, _, _ := nestedMap(manifest.Object, "spec", "submitterPodTemplate", "spec")
	initContainers, _, _ := nestedSlice(submitter, "initContainers")
	init := initContainers[0].(map[string]any)
	security, _ := init["securityContext"].(map[string]any)
	for field, want := range map[string]any{
		"runAsNonRoot": true,
		"runAsUser":    int64(1000),
		"runAsGroup":   int64(1000),
	} {
		if security[field] != want {
			t.Fatalf("workspace snapshot materializer %s=%#v, want %#v: %#v", field, security[field], want, security)
		}
	}
}

func assertNoWorkspaceDataRoot(t *testing.T, podSpec map[string]any) {
	t.Helper()
	containers, _, _ := nestedSlice(podSpec, "containers")
	mounts, _ := containers[0].(map[string]any)["volumeMounts"].([]any)
	for _, value := range mounts {
		mount, _ := value.(map[string]any)
		if mount["name"] == "platform-data-personal-workspace" || (mount["mountPath"] == domain.WorkspaceMountPath && mount["name"] != "workspace") {
			t.Fatalf("training workspace must remain the materialized ephemeral volume: %#v", mounts)
		}
	}
}

func assertGovernedDataMount(t *testing.T, podSpec map[string]any, volumeName, claimName, mountPath, subPath string, readOnly bool) {
	t.Helper()
	assertStorageMount(t, podSpec, volumeName, claimName, mountPath, readOnly)
	containers, _, _ := nestedSlice(podSpec, "containers")
	mounts, _ := containers[0].(map[string]any)["volumeMounts"].([]any)
	for _, value := range mounts {
		mount, _ := value.(map[string]any)
		if mount["name"] == volumeName && mount["mountPath"] == mountPath && mount["subPath"] == subPath {
			return
		}
	}
	t.Fatalf("missing %s subpath %s: %#v", volumeName, subPath, mounts)
}

func containsAny(value string, candidates ...string) bool {
	for _, candidate := range candidates {
		if candidate != "" && len(value) >= len(candidate) {
			for index := 0; index+len(candidate) <= len(value); index++ {
				if value[index:index+len(candidate)] == candidate {
					return true
				}
			}
		}
	}
	return false
}
