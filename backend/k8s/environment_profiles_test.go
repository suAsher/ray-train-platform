package k8s

import (
	"context"
	"errors"
	"io"
	"ray-train-platform-backend/environmentbuild"
	"strings"
	"testing"
)

func TestEnvironmentProfileBindsEveryExecutionPhaseToWorkspaceBase(t *testing.T) {
	r := environmentTestRunner()
	p := environmentbuild.WorkspaceProfile{WorkspaceImage: "harbor.wellspiking.ai/public/debug-cu126@sha256:" + strings.Repeat("5", 64), BaseImage: "harbor.wellspiking.ai/public/base-cu126@sha256:" + strings.Repeat("6", 64), PrepareImage: "harbor.wellspiking.ai/public/prepare-cu126@sha256:" + strings.Repeat("7", 64)}
	cfg := r.config
	cfg.WorkspaceProfiles = []environmentbuild.WorkspaceProfile{p}
	r = NewEnvironmentRunner(r.client, cfg)
	cfg.WorkspaceProfiles[0].BaseImage = "evil.invalid/base:latest"
	if !r.workspaceImageAllowed(p.WorkspaceImage) {
		t.Fatal("trusted profile workspace not accepted")
	}
	b := environmentTestBuild(r)
	b.WorkspaceImage, b.BaseImage = p.WorkspaceImage, p.BaseImage
	for _, phase := range []string{environmentbuild.Building, environmentbuild.Validating, environmentbuild.Pushing, environmentbuild.VerifyingPull} {
		b.Status = phase
		job, err := r.renderJob(b)
		if err != nil {
			t.Fatalf("profile phase %s: %v", phase, err)
		}
		c := job.Spec.Template.Spec.Containers[0]
		if phase == environmentbuild.Building && c.Image != p.PrepareImage {
			t.Fatal("prepare used default CUDA base")
		}
		if phase == environmentbuild.Validating && !strings.Contains(strings.Join(c.Args, " "), "--base "+p.BaseImage) {
			t.Fatal("assembler used default CUDA base")
		}
		forged := b
		forged.BaseImage = r.config.BaseImage
		if _, err := r.renderJob(forged); !errors.Is(err, environmentbuild.ErrInvalid) {
			t.Fatalf("render accepted mismatched base in %s: %v", phase, err)
		}
		if _, err := r.Step(context.Background(), forged, nil); !errors.Is(err, environmentbuild.ErrInvalid) {
			t.Fatalf("step accepted mismatched base in %s: %v", phase, err)
		}
	}
}

func TestEnvironmentProfileCaptureUsesPairedBaseAndVerifiedActualImage(t *testing.T) {
	r := environmentTestRunner()
	p := environmentbuild.WorkspaceProfile{WorkspaceImage: "harbor.wellspiking.ai/public/debug-cu126@sha256:" + strings.Repeat("5", 64), BaseImage: "harbor.wellspiking.ai/public/base-cu126@sha256:" + strings.Repeat("6", 64), PrepareImage: "harbor.wellspiking.ai/public/prepare-cu126@sha256:" + strings.Repeat("7", 64)}
	cfg := r.config
	cfg.WorkspaceProfiles = []environmentbuild.WorkspaceProfile{p}
	r = NewEnvironmentRunner(r.client, cfg)
	workspace := environmentbuild.Workspace{ID: "workspace-a", TenantID: "tenant-a", OwnerID: "alice", Namespace: "tenant-a", ResourceName: "workspace-a"}
	cluster, pod := environmentWorkspaceFixture(r, workspace, p.WorkspaceImage, p.WorkspaceImage)
	environmentInstallWorkspaceFixture(r, workspace, pod, cluster)
	inspected, err := r.InspectWorkspace(context.Background(), workspace)
	if err != nil || inspected.Image != p.WorkspaceImage {
		t.Fatalf("inspect profile: %+v %v", inspected, err)
	}
	b := environmentTestBuild(r)
	b.Status = environmentbuild.Capturing
	b.WorkspaceID, b.Namespace, b.WorkspaceResourceName, b.WorkspaceUID = workspace.ID, workspace.Namespace, workspace.ResourceName, inspected.UID
	b.BaseImage, b.WorkspaceImage = p.BaseImage, p.WorkspaceImage
	capturedBase := p.BaseImage
	r.captureExec = func(_ context.Context, _ string, _ string, stdout, stderr io.Writer) error {
		_, err := stdout.Write([]byte(`{"schemaVersion":1,"baseImage":"` + capturedBase + `","checks":{"baseUnchanged":true,"managedOnly":true,"installedFilesVerified":true,"stableCapture":true}}`))
		return err
	}
	if result, err := r.Step(context.Background(), b, nil); err != nil || !result.Done {
		t.Fatalf("profile capture: %+v %v", result, err)
	}
	capturedBase = r.config.BaseImage
	if _, err := r.Step(context.Background(), b, nil); err == nil {
		t.Fatal("capture accepted cu121 envelope for cu126 profile")
	}
}
