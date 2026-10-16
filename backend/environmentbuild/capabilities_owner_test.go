package environmentbuild

import (
 "context"
 "errors"
 "strings"
 "testing"
 "time"
 "ray-train-platform-backend/domain"
)
type capabilitiesOwnerStore struct {
 *multiRegistryStore
 current *domain.DevWorkspace
 workspace Workspace
 currentError,workspaceError error
 currentCalls,workspaceCalls int
 requested Owner
}
func (s *capabilitiesOwnerStore) GetWorkspace(_ context.Context,tenantID,userID string)(*domain.DevWorkspace,error) {
 s.currentCalls++
 s.requested=Owner{TenantID:tenantID,UserID:userID}
 return s.current,s.currentError
}
func (s *capabilitiesOwnerStore) EnvironmentWorkspace(_ context.Context,owner Owner,id string)(Workspace,error) {
 s.workspaceCalls++
 if owner!=s.requested || s.current==nil || id!=s.current.ID { return Workspace{},ErrNotFound }
 return s.workspace,s.workspaceError
}
type capabilitiesOwnerRunner struct {
 *multiRegistryRunner
 snapshot WorkspaceSnapshot
 err error
 calls int
 waitForContext bool
}
func (r *capabilitiesOwnerRunner) InspectWorkspace(ctx context.Context,w Workspace)(WorkspaceSnapshot,error) {
 r.calls++
 if r.waitForContext { <-ctx.Done();return WorkspaceSnapshot{},ctx.Err() }
 return r.snapshot,r.err
}
func capabilitiesOwnerFixture(t *testing.T)(*Service,*capabilitiesOwnerStore,*capabilitiesOwnerRunner) {
 t.Helper()
 base,oldStore,_,oldRunner:=multiRegistryFixture(t)
 profile:=cudaWorkspaceProfile()
 cfg:=base.config
 cfg.WorkspaceProfiles=[]WorkspaceProfile{profile}
 cfg.CompatibleWorkspaceImages=[]string{RegistryHost+"/public/older-debug@sha256:"+strings.Repeat("3",64)}
 store:=&capabilitiesOwnerStore{multiRegistryStore:oldStore,current:&domain.DevWorkspace{ID:"own-workspace",TenantID:"team",UserID:"owner",Namespace:"tenant",RayClusterName:"own-cluster",State:domain.WorkspaceRunning},workspace:Workspace{ID:"own-workspace",TenantID:"team",OwnerID:"owner",Namespace:"tenant",ResourceName:"own-cluster",State:"RUNNING"}}
 runner:=&capabilitiesOwnerRunner{multiRegistryRunner:oldRunner,snapshot:WorkspaceSnapshot{UID:"actual-pod-uid",Image:profile.WorkspaceImage}}
 service,err:=NewService(store,runner,base.registry,base.vault,cfg)
 if err!=nil { t.Fatal(err) }
 return service,store,runner
}
func TestOwnerCapabilitiesAddsOnlyCurrentTrustedRunningWorkspaceHint(t *testing.T) {
 for _,kind:=range []string{"profile","primary","legacy-compatible"} {
  t.Run(kind,func(t *testing.T){
   service,store,runner:=capabilitiesOwnerFixture(t)
   if kind=="primary" { runner.snapshot.Image=service.config.WorkspaceImage }
   if kind=="legacy-compatible" { runner.snapshot.Image=service.config.CompatibleWorkspaceImages[0] }
   caps:=service.CapabilitiesForOwner(context.Background(),Owner{TenantID:"team",UserID:"owner"})
   if caps["eligibleWorkspaceId"]!="own-workspace" || store.requested!=(Owner{TenantID:"team",UserID:"owner"}) || runner.calls!=1 { t.Fatalf("missing owned hint: %+v",caps) }
   if caps["workspaceImage"]!=service.config.WorkspaceImage || caps["baseImage"]!=service.config.BaseImage || len(service.config.CompatibleWorkspaceImages)!=1 { t.Fatal("hint changed original runtime configuration") }
   if _,found:=service.Capabilities()["eligibleWorkspaceId"];found { t.Fatal("owner hint leaked into global capabilities") }
  })
 }
}
func TestOwnerCapabilitiesOmitsHintForUntrustedOrUnavailableWorkspace(t *testing.T) {
 cases:=[]string{"no-current","current-error","current-owner","current-tenant","current-empty-id","current-stopped","current-ready","owned-error","owned-owner","owned-tenant","owned-id","owned-namespace","owned-resource","owned-stopped","inspect-error","no-uid","unknown-image","mutable-image","foreign-image"}
 for _,name:=range cases {
  t.Run(name,func(t *testing.T){
   service,store,runner:=capabilitiesOwnerFixture(t)
   switch name {
   case "no-current":store.current=nil
   case "current-error":store.currentError=errors.New("private lookup detail")
   case "current-owner":store.current.UserID="victim"
   case "current-tenant":store.current.TenantID="other"
   case "current-empty-id":store.current.ID=""
   case "current-stopped":store.current.State=domain.WorkspaceStopped
   case "current-ready":store.current.State="READY"
   case "owned-error":store.workspaceError=errors.New("private ownership detail")
   case "owned-owner":store.workspace.OwnerID="victim"
   case "owned-tenant":store.workspace.TenantID="other"
   case "owned-id":store.workspace.ID="victim-workspace"
   case "owned-namespace":store.workspace.Namespace="other"
   case "owned-resource":store.workspace.ResourceName="other"
   case "owned-stopped":store.workspace.State="STOPPED"
   case "inspect-error":runner.err=errors.New("private kubernetes detail")
   case "no-uid":runner.snapshot.UID=""
   case "unknown-image":runner.snapshot.Image=RegistryHost+"/public/unknown@sha256:"+strings.Repeat("8",64)
   case "mutable-image":runner.snapshot.Image=RegistryHost+"/public/debug:latest"
   case "foreign-image":runner.snapshot.Image="evil.invalid/debug@sha256:"+strings.Repeat("5",64)
   }
   caps:=service.CapabilitiesForOwner(context.Background(),Owner{TenantID:"team",UserID:"owner"})
   if _,found:=caps["eligibleWorkspaceId"];found || caps["enabled"]!=true { t.Fatal("unsafe hint or unavailable base capabilities") }
  })
 }
}
func TestOwnerCapabilitiesDoesNotInspectDisabledOrMissingOwner(t *testing.T) {
 for _,name:=range []string{"disabled","missing-user","missing-tenant","nil-service","no-current-store"} {
  t.Run(name,func(t *testing.T){
   service,store,runner:=capabilitiesOwnerFixture(t)
   owner:=Owner{TenantID:"team",UserID:"owner"}
   switch name {
   case "disabled":service.config.Enabled=false
   case "missing-user":owner.UserID=""
   case "missing-tenant":owner.TenantID=""
   case "nil-service":service=nil
   case "no-current-store":service.store=store.multiRegistryStore
   }
   caps:=service.CapabilitiesForOwner(context.Background(),owner)
   if _,found:=caps["eligibleWorkspaceId"];found || store.currentCalls!=0 || runner.calls!=0 { t.Fatal("unavailable capability hint performed owner inspection") }
  })
 }
}
func TestOwnerCapabilitiesLookupDeadlineReturnsBaseCapabilities(t *testing.T) {
 service,_,runner:=capabilitiesOwnerFixture(t)
 runner.waitForContext=true
 ctx,cancel:=context.WithTimeout(context.Background(),10*time.Millisecond)
 defer cancel()
 caps:=service.CapabilitiesForOwner(ctx,Owner{TenantID:"team",UserID:"owner"})
 if _,found:=caps["eligibleWorkspaceId"];found || caps["enabled"]!=true { t.Fatal("timeout failed closed capability hint") }
}
