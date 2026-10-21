package api

import (
 "context"
 "testing"
 "ray-train-platform-backend/auth"
 "ray-train-platform-backend/domain"
)

func teamWriteBindings() []domain.DataMountBinding {
 return []domain.DataMountBinding{
  {ID:"root", TenantID:"team-a", Scope:domain.DataMountScopeTenant, SpaceID:domain.DataSpaceTenantStorageRoot, ClaimName:"data-tenant-a", RootPrefix:"ray-train/", Status:domain.DataMountBindingReady},
  {ID:"mine", TenantID:"team-a", UserID:"member-a", Scope:domain.DataMountScopePersonal, SpaceID:domain.DataSpaceWorkspace, ClaimName:"data-mine", RootPrefix:"ray-train/tenants/team-a/users/member-a/", Status:domain.DataMountBindingReady},
  {ID:"team", TenantID:"team-a", Scope:domain.DataMountScopeTenant, SpaceID:domain.DataSpaceTeamShared, ClaimName:"legacy-team-ro", RootPrefix:"ray-train/tenants/team-a/shared/", ReadOnly:true, Status:domain.DataMountBindingReady},
  {ID:"public", TenantID:"team-a", Scope:domain.DataMountScopeTenant, SpaceID:domain.DataSpacePublic, ClaimName:"public-ro", RootPrefix:"ray-train/public/", ReadOnly:true, Status:domain.DataMountBindingReady},
  {ID:"foreign", TenantID:"team-b", Scope:domain.DataMountScopeTenant, SpaceID:domain.DataSpaceTeamShared, ClaimName:"foreign-team", RootPrefix:"ray-train/tenants/team-b/shared/", ReadOnly:true, Status:domain.DataMountBindingReady},
 }
}

func TestTeamStorageNewWorkloadsUseWritableCurrentTeamForEveryRole(t *testing.T) {
 for _, role := range []string{domain.RoleEngineer, domain.RoleTenantAdmin, domain.RoleSuperAdmin} {
  t.Run(role, func(t *testing.T) {
   store := &fakeDataSpaceStore{bindings:teamWriteBindings()}
   principal := auth.Principal{Subject:"member-a", TenantID:"team-a", Roles:[]string{role}}
   handler := NewHandler(&fakeJobRepository{}, Options{DataSpacesEnabled:true, DataSpaces:store})
   plan, err := handler.resolveWorkspaceDataMountPlan(context.Background(), principal)
   if err != nil { t.Fatal(err) }
   if plan.Team == nil || plan.Team.ReadOnly || plan.Team.ClaimName != "data-tenant-a" || plan.Team.SubPath != "tenants/team-a/shared" { t.Fatalf("workspace team: %#v", plan.Team) }
   if plan.Public == nil || !plan.Public.ReadOnly { t.Fatal("workspace public must stay readonly") }
   service := NewSubmissionService(&submissionServiceRepository{}, SubmissionServiceOptions{DataSpaces:store, DataSpacesEnabled:true})
   roots, err := service.resolveDataSpaceRoots(context.Background(), principal)
   if err != nil { t.Fatal(err) }
   if roots.Team == nil || roots.Team.ReadOnly || roots.Team.ClaimName != "data-tenant-a" || roots.Team.SubPath != "tenants/team-a/shared" { t.Fatalf("training team: %#v", roots.Team) }
   if roots.Public == nil || !roots.Public.ReadOnly { t.Fatal("training public must stay readonly") }
   if store.bindings[2].ReadOnly != true { t.Fatal("legacy binding was mutated") }
  })
 }
}

func TestTeamStorageWritableResolutionRequiresWritableTenantRoot(t *testing.T) {
 for _, mode := range []string{"missing", "readonly", "foreign"} {
  t.Run(mode, func(t *testing.T) {
   bindings := teamWriteBindings()
   switch mode { case "missing": bindings = bindings[1:]; case "readonly": bindings[0].ReadOnly = true; case "foreign": bindings[0].TenantID = "team-b" }
   store := &fakeDataSpaceStore{bindings:bindings}
   principal := auth.Principal{Subject:"member-a", TenantID:"team-a", Roles:[]string{domain.RoleEngineer}}
   handler := NewHandler(&fakeJobRepository{}, Options{DataSpacesEnabled:true, DataSpaces:store})
   if _, err := handler.resolveWorkspaceDataMountPlan(context.Background(), principal); err == nil { t.Fatal("workspace accepted missing/readonly/foreign root") }
   service := NewSubmissionService(&submissionServiceRepository{}, SubmissionServiceOptions{DataSpaces:store, DataSpacesEnabled:true})
   if _, err := service.resolveDataSpaceRoots(context.Background(), principal); err == nil { t.Fatal("training accepted missing/readonly/foreign root") }
  })
 }
}
