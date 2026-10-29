package k8s

import (
	"context"
	"io"
	"strings"
	"testing"

	"ray-train-platform-backend/environmentbuild"
)

func TestEnvironmentRunnerCustomRegistryIsExplicitlyPassedToPublisher(t *testing.T) {
	runner := environmentTestRunner()
	runner.config.RegistryHosts = []string{"registry.example.com", "harbor.custom.example"}
	build := environmentTestBuild(runner)
	build.Status = environmentbuild.Pushing
	build.RegistryHost = "harbor.custom.example"
	job, err := runner.renderJob(build)
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Join(job.Spec.Template.Spec.Containers[0].Args, " ")
	if !strings.Contains(args, "--allowed-registry-hosts harbor.custom.example") {
		t.Fatal("publisher has no deployment-approved destination")
	}
	build.RegistryHost = "unapproved.example"
	if _, err := runner.renderJob(build); err == nil {
		t.Fatal("unknown registry rendered a publisher job")
	}
}

func TestCustomRegistryWorkspaceCaptureAndAllExecutionPhases(t *testing.T) {
	original := environmentTestRunner()
	cfg := original.config
	const host = "harbor.custom.example"
	cfg.RegistryHosts = []string{host}
	image := func(name, digest string) string { return host + "/platform/" + name + "@sha256:" + strings.Repeat(digest, 64) }
	cfg.BaseImage = image("base", "1")
	cfg.WorkspaceImage = image("workspace", "2")
	cfg.PrepareImage = image("prepare", "3")
	cfg.PublisherImage = image("publisher", "4")
	runner := NewEnvironmentRunner(original.client, cfg)
	workspace := environmentbuild.Workspace{ID: "workspace-a", TenantID: "tenant-a", OwnerID: "alice", Namespace: "tenant-a", ResourceName: "workspace-a"}
	cluster, pod := environmentWorkspaceFixture(runner, workspace, cfg.WorkspaceImage, cfg.WorkspaceImage)
	environmentInstallWorkspaceFixture(runner, workspace, pod, cluster)
	inspected, err := runner.InspectWorkspace(context.Background(), workspace)
	if err != nil || inspected.Image != cfg.WorkspaceImage {
		t.Fatalf("custom workspace inspection: %+v %v", inspected, err)
	}
	build := environmentTestBuild(runner)
	build.RegistryHost = host
	build.WorkspaceID, build.Namespace, build.WorkspaceResourceName, build.WorkspaceUID = workspace.ID, workspace.Namespace, workspace.ResourceName, inspected.UID
	build.Status = environmentbuild.Capturing
	runner.captureExec = func(_ context.Context, _ string, _ string, stdout, stderr io.Writer) error {
		_, err := stdout.Write([]byte(`{"schemaVersion":1,"baseImage":"` + cfg.BaseImage + `","checks":{"baseUnchanged":true,"managedOnly":true,"installedFilesVerified":true,"stableCapture":true}}`))
		return err
	}
	if result, err := runner.Step(context.Background(), build, nil); err != nil || !result.Done {
		t.Fatalf("custom workspace capture: %+v %v", result, err)
	}
	for _, phase := range []string{environmentbuild.Building, environmentbuild.Validating, environmentbuild.Pushing, environmentbuild.VerifyingPull} {
		build.Status = phase
		job, err := runner.renderJob(build)
		if err != nil || !strings.HasPrefix(job.Spec.Template.Spec.Containers[0].Image, host+"/") {
			t.Fatalf("custom runtime phase %s: %v", phase, err)
		}
		forged := build
		forged.BaseImage = image("other-base", "5")
		if _, err := runner.renderJob(forged); err == nil {
			t.Fatalf("user-controlled base accepted in %s", phase)
		}
	}
	if runner.workspaceImageAllowed(image("unregistered-workspace", "6")) {
		t.Fatal("registry allowlist bypassed exact workspace profile binding")
	}
}

func TestEnvironmentEmptyStorageClassUsesClusterDefault(t *testing.T) {
	if environmentStorageClass("") != nil {
		t.Fatal("empty class must leave default provisioning enabled")
	}
	if class := environmentStorageClass("shared-block"); class == nil || *class != "shared-block" {
		t.Fatal("explicit storage class lost")
	}
}
