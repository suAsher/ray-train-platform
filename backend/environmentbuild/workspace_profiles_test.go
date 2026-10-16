package environmentbuild

import (
 "context"
 "errors"
 "strings"
 "testing"
 "ray-train-platform-backend/domain"
)

func cudaWorkspaceProfile() WorkspaceProfile {
 return WorkspaceProfile{WorkspaceImage: RegistryHost+"/public/debug-cu126@sha256:"+strings.Repeat("5",64), BaseImage: RegistryHost+"/public/base-cu126@sha256:"+strings.Repeat("6",64), PrepareImage: RegistryHost+"/public/prepare-cu126@sha256:"+strings.Repeat("7",64), Environment: domain.ImageEnvironment{Python:"3.10",CUDA:"12.6",PyTorch:"2.6.0+cu126"}}
}
func TestCreateUsesTrustedProfileFromActualWorkspaceImage(t *testing.T) {
 base, _, _, runner := multiRegistryFixture(t)
 profile := cudaWorkspaceProfile()
 cfg := base.config
 cfg.WorkspaceProfiles = []WorkspaceProfile{profile}
 runner.image = profile.WorkspaceImage
 service, err := NewService(base.store,runner,base.registry,base.vault,cfg)
 if err != nil { t.Fatal(err) }
 service.now=base.now
 owner := Owner{TenantID:"team",UserID:"owner"}
 auth,err:=service.CreateAuthorization(context.Background(),owner,Credentials{Username:"fixture",Secret:"fixture-only"})
 if err != nil { t.Fatal(err) }
 build,err:=service.Create(context.Background(),owner,"workspace",CreateRequest{AuthorizationID:auth.ID,Project:"project",Repository:"image",Name:"CUDA environment",IdempotencyKey:"cuda126-build"})
 if err != nil || build.BaseImage != profile.BaseImage || build.WorkspaceImage != profile.WorkspaceImage { t.Fatalf("actual image profile not bound: %+v %v",build,err) }
 if service.Capabilities()["workspaceImage"] != cfg.WorkspaceImage { t.Fatal("new profile changed default") }
 profiles:=service.Capabilities()["workspaceProfiles"].([]WorkspaceProfile)
 if len(profiles)!=1 || profiles[0].Environment.CUDA!="12.6" { t.Fatal("missing version capability") }
 cfg.WorkspaceProfiles[0].BaseImage="evil.invalid/base:latest"
 profiles[0].BaseImage="evil.invalid/base:latest"
 got,ok:=service.workspaceProfile(profile.WorkspaceImage)
 if !ok || got.BaseImage!=profile.BaseImage { t.Fatal("caller changed trusted profile") }
}
func TestWorkspaceProfilesRejectAmbiguousOrUntrustedConfiguration(t *testing.T) {
 base,_,_,_:=multiRegistryFixture(t)
 for _, scenario:=range []string{"workspace tag","base registry","prepare missing","default conflict","compatible conflict","duplicate","invalid metadata"} {
  t.Run(scenario,func(t *testing.T){
   cfg:=base.config
   p:=cudaWorkspaceProfile()
   switch scenario {
   case "workspace tag": p.WorkspaceImage=RegistryHost+"/public/debug:latest"
   case "base registry": p.BaseImage="evil.invalid/base@sha256:"+strings.Repeat("6",64)
   case "prepare missing": p.PrepareImage=""
   case "default conflict": p.WorkspaceImage=cfg.WorkspaceImage
   case "compatible conflict": cfg.CompatibleWorkspaceImages=[]string{p.WorkspaceImage}
   case "invalid metadata": p.Environment.Python="3.10\nforged"
   }
   cfg.WorkspaceProfiles=[]WorkspaceProfile{p}
   if scenario=="duplicate" { cfg.WorkspaceProfiles=append(cfg.WorkspaceProfiles,p) }
   if _,err:=NewService(base.store,base.runner,base.registry,base.vault,cfg); !errors.Is(err,ErrInvalid) { t.Fatalf("accepted %s: %v",scenario,err) }
  })
 }
}
