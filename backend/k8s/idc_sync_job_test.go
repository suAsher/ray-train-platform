package k8s

import (
	"strings"
	"testing"

	"ray-train-platform-backend/idcsync"
)

func TestRenderIDCSyncJobUsesReadOnlySourcePersistentWorkAndPreviousInventory(t *testing.T) {
	job, err := renderIDCSyncJob(idcsync.JobSpec{
		Namespace: "ray-train-platform", RunID: "run-1", SourceRelativePath: "QP_NuScene/labeled",
		MirrorPrefix: "ray-train/platform/idc-mirror/labeled", InternalPrefix: "ray-train/platform",
		Bucket: "training-data", Image: "harbor/idc@sha256:abc", TosutilConfigSecret: "tosutil",
		SourceNFSServer: "10.0.0.1", SourceNFSPath: "/original", CallbackURL: "http://backend/internal",
		CallbackToken: "secret", ServiceAccountName: "idc-sync", WorkClaimName: "idc-sync-work",
		PreviousInventoryKey: "ray-train/platform/idc-inventories/old/" + strings.Repeat("a", 64) + ".json",
	})
	if err != nil {
		t.Fatal(err)
	}
	container := job.Spec.Template.Spec.Containers[0]
	joined := strings.Join(container.Args, " ")
	if !strings.Contains(joined, "--previous-inventory-key") || !strings.Contains(joined, "--work-dir /work/run-1") || container.Command != nil {
		t.Fatalf("unexpected worker command: command=%v args=%v", container.Command, container.Args)
	}
	volumes := job.Spec.Template.Spec.Volumes
	if volumes[0].NFS == nil || !volumes[0].NFS.ReadOnly || volumes[1].PersistentVolumeClaim == nil || volumes[1].PersistentVolumeClaim.ClaimName != "idc-sync-work" {
		t.Fatalf("unsafe volumes: %#v", volumes)
	}
	if len(container.Resources.Requests) != 0 || len(container.Resources.Limits) != 0 {
		t.Fatalf("IDC sync must not request GPU resources: %#v", container.Resources)
	}
}
