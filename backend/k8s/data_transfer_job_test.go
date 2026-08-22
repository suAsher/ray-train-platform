package k8s

import (
	"strings"
	"testing"

	"ray-train-platform-backend/domain"
)

func TestRenderDataTransferJobUsesOnlyScopedVolumesAndPinnedSFTPHost(t *testing.T) {
	transfer, err := domain.NewDataTransfer("transfer-1", "tenant-a", "user-a", domain.DataTransferIDCToTOS, "projects/demo/raw", domain.DataLocation{Space: domain.DataSpaceMyFiles, RelativePath: "datasets/demo/raw"})
	if err != nil {
		t.Fatal(err)
	}
	connection, err := domain.NewPersonalIDCConnection("connection-1", "tenant-a", "user-a", "guofeng.su", "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAITest platform@ray", "idc-sftp-connection-1")
	if err != nil {
		t.Fatal(err)
	}
	connection.State = domain.IDCConnectionReady

	job, err := RenderDataTransferJob(transfer, connection, "data-user-a", DataTransferJobOptions{
		Namespace:           "tenant-tenant-a",
		Image:               pinnedDataMoverImage(),
		ServiceAccount:      "ray-data-mover",
		KnownHostsConfigMap: "ray-data-mover-known-hosts",
		SFTPHost:            "mount.wellspiking.ai",
		SFTPPort:            22,
		NodeSelector:        map[string]string{"platform.wellspiking.ai/node-pool": "control-plane"},
	})
	if err != nil {
		t.Fatalf("render data transfer job: %v", err)
	}
	if job.Namespace != "tenant-tenant-a" || job.Spec.TTLSecondsAfterFinished == nil || *job.Spec.TTLSecondsAfterFinished <= 0 || job.Spec.ActiveDeadlineSeconds == nil || *job.Spec.ActiveDeadlineSeconds <= 0 {
		t.Fatalf("job lacks durable namespace/TTL/deadline controls: %#v", job)
	}
	pod := job.Spec.Template.Spec
	if pod.ServiceAccountName != "ray-data-mover" || pod.AutomountServiceAccountToken == nil || *pod.AutomountServiceAccountToken {
		t.Fatalf("transfer Pod must use scoped service account without an API token: %#v", pod)
	}
	if pod.NodeSelector["platform.wellspiking.ai/node-pool"] != "control-plane" || pod.SecurityContext == nil || pod.SecurityContext.RunAsNonRoot == nil || !*pod.SecurityContext.RunAsNonRoot {
		t.Fatalf("transfer Pod lacks CPU placement/non-root contract: %#v", pod)
	}
	manifest := string(mustJSON(job))
	if strings.Contains(manifest, "hostPath") || strings.Contains(manifest, "TOS_ACCESS_KEY") || strings.Contains(manifest, "TOS_SECRET_KEY") || strings.Contains(manifest, "serviceAccountToken") {
		t.Fatalf("transfer Job leaked an infrastructure access path: %s", manifest)
	}
	if !strings.Contains(manifest, "idc-sftp-connection-1") || !strings.Contains(manifest, "ray-data-mover-known-hosts") || !strings.Contains(manifest, "data-user-a") {
		t.Fatalf("transfer Job is missing its scoped Secret, known-hosts ConfigMap, or user claim: %s", manifest)
	}
	for _, container := range pod.Containers {
		if len(container.Command) > 1 && container.Command[0] == "sh" && container.Command[1] == "-c" {
			t.Fatalf("user paths must not be interpolated by a shell: %#v", container)
		}
	}
}

func TestRenderDataTransferJobRequiresVerifiedConnectionAndFixedSFTPConfiguration(t *testing.T) {
	transfer, err := domain.NewDataTransfer("transfer-1", "tenant-a", "user-a", domain.DataTransferTOSToIDC, "results/demo", domain.DataLocation{Space: domain.DataSpaceMyFiles, RelativePath: "results/demo"})
	if err != nil {
		t.Fatal(err)
	}
	connection, err := domain.NewPersonalIDCConnection("connection-1", "tenant-a", "user-a", "guofeng.su", "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAITest platform@ray", "idc-sftp-connection-1")
	if err != nil {
		t.Fatal(err)
	}
	options := DataTransferJobOptions{Namespace: "tenant-tenant-a", Image: pinnedDataMoverImage(), ServiceAccount: "ray-data-mover", KnownHostsConfigMap: "ray-data-mover-known-hosts", SFTPHost: "mount.wellspiking.ai", SFTPPort: 22}
	if _, err := RenderDataTransferJob(transfer, connection, "data-user-a", options); err == nil {
		t.Fatal("pending IDC connection was accepted")
	}
	connection.State = domain.IDCConnectionReady
	options.KnownHostsConfigMap = ""
	if _, err := RenderDataTransferJob(transfer, connection, "data-user-a", options); err == nil {
		t.Fatal("unverified SFTP host configuration was accepted")
	}
}

func pinnedDataMoverImage() string {
	return "harbor.wellspiking.ai/guofeng.su/ray-data-mover@sha256:" + strings.Repeat("a", 64)
}
