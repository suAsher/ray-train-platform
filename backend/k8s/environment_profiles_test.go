package k8s

import (
 "context"
 "errors"
 "strings"
 "testing"
 "ray-train-platform-backend/environmentbuild"
)
func TestEnvironmentProfileBindsEveryExecutionPhaseToWorkspaceBase(t *testing.T) {
 r:=environmentTestRunner()
 p:=environmentbuild.WorkspaceProfile{WorkspaceImage:"harbor.wellspiking.ai/public/debug-cu126@sha256:"+strings.Repeat("5",64),BaseImage:"harbor.wellspiking.ai/public/base-cu126@sha256:"+strings.Repeat("6",64),PrepareImage:"harbor.wellspiking.ai/public/prepare-cu126@sha256:"+strings.Repeat("7",64)}
 cfg:=r.config
 cfg.WorkspaceProfiles=[]environmentbuild.WorkspaceProfile{p}
 r=NewEnvironmentRunner(r.client,cfg)
 cfg.WorkspaceProfiles[0].BaseImage="evil.invalid/base:latest"
 if !r.workspaceImageAllowed(p.WorkspaceImage) { t.Fatal("trusted profile workspace not accepted") }
 b:=environmentTestBuild(r)
 b.WorkspaceImage,b.BaseImage=p.WorkspaceImage,p.BaseImage
 for _,phase:=range []string{environmentbuild.Building,environmentbuild.Validating,environmentbuild.Pushing,environmentbuild.VerifyingPull} {
  b.Status=phase
  job,err:=r.renderJob(b)
  if err!=nil { t.Fatalf("profile phase %s: %v",phase,err) }
  c:=job.Spec.Template.Spec.Containers[0]
  if phase==environmentbuild.Building && c.Image!=p.PrepareImage { t.Fatal("prepare used default CUDA base") }
  if phase==environmentbuild.Validating && !strings.Contains(strings.Join(c.Args," "),"--base "+p.BaseImage) { t.Fatal("assembler used default CUDA base") }
  forged:=b
  forged.BaseImage=r.config.BaseImage
  if _,err:=r.renderJob(forged);!errors.Is(err,environmentbuild.ErrInvalid) { t.Fatalf("render accepted mismatched base in %s: %v",phase,err) }
  if _,err:=r.Step(context.Background(),forged,nil);!errors.Is(err,environmentbuild.ErrInvalid) { t.Fatalf("step accepted mismatched base in %s: %v",phase,err) }
 }
}
