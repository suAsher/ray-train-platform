package k8s

import (
 "testing"
 "ray-train-platform-backend/domain"
)

func TestTeamStorageRenderPreservesWritableAndLegacySnapshots(t *testing.T) {
 for _, readOnly := range []bool{false, true} {
  plan := DataMountPlan{
   Personal:&DataMountRoot{ClaimName:"data-tenant-a", SubPath:"tenants/team-a/users/user-a"},
   Team:&DataMountRoot{ClaimName:"data-tenant-a", SubPath:"tenants/team-a/shared", ReadOnly:readOnly},
   Public:&DataMountRoot{ClaimName:"data-tenant-a", SubPath:"public", ReadOnly:true},
  }
  workspace := domain.DevWorkspace{ID:"ws-team-write", TenantID:"team-a", UserID:"user-a", Name:"dev-team-write", Namespace:"tenant-team-a", GPUCount:1}
  manifest, err := RenderDevRayCluster(workspace, WorkspaceRenderOptions{Image:"registry.example/dev:fixed", DataMounts:plan})
  if err != nil { t.Fatalf("workspace readOnly=%v: %v", readOnly, err) }
  for _, group := range []string{"head", "worker"} {
   pod := dataMountDevPodSpec(t, manifest, group)
   assertStorageMountWithSubPath(t,pod,"platform-data-personal","data-tenant-a",domain.TeamStorageMountPath,"tenants/team-a/shared",readOnly)
   assertStorageMountWithSubPath(t,pod,"platform-data-personal","data-tenant-a",domain.PublicStorageMountPath,"public",true)
   if countPVCVolumes(pod,"data-tenant-a") != 1 { t.Fatal("workspace staged tenant root more than once") }
  }
  job := validRenderJob()
  job.Spec.ResolvedDataRoots = domain.ResolvedDataSpaceRoots{
   Personal:&domain.ResolvedDataRoot{Space:domain.DataSpaceWorkspace, ClaimName:"data-tenant-a", SubPath:"tenants/team-a/users/user-a"},
   Team:&domain.ResolvedDataRoot{Space:domain.DataSpaceTeamShared, ClaimName:"data-tenant-a", SubPath:"tenants/team-a/shared",ReadOnly:readOnly},
   Public:&domain.ResolvedDataRoot{Space:domain.DataSpacePublic, ClaimName:"data-tenant-a", SubPath:"public",ReadOnly:true},
  }
  job.Spec.ResolvedDataMounts = domain.ResolvedDataSpaceMounts{Input:&domain.ResolvedDataMount{Space:domain.DataSpaceTeamShared,BindingSpace:domain.DataSpaceTeamShared,ClaimName:"data-tenant-a",SubPath:"tenants/team-a/shared/data",MountPath:domain.DataMountInputPath,ReadOnly:true}}
  training, err := RenderRayJob(job,testRenderOptions())
  if err != nil { t.Fatalf("training readOnly=%v: %v",readOnly,err) }
  head,_,_ := nestedMap(training.Object,"spec","rayClusterSpec","headGroupSpec","template","spec")
  workers,_,_ := nestedSlice(training.Object,"spec","rayClusterSpec","workerGroupSpecs")
  worker,_,_ := nestedMap(workers[0].(map[string]any),"template","spec")
  for _, pod := range []map[string]any{head,worker} {
   assertStorageMountWithSubPath(t,pod,"platform-data-personal","data-tenant-a",domain.TeamStorageMountPath,"tenants/team-a/shared",readOnly)
   assertStorageMountWithSubPath(t,pod,"platform-data-personal","data-tenant-a",domain.PublicStorageMountPath,"public",true)
   assertStorageMountWithSubPath(t,pod,"platform-data-personal","data-tenant-a",domain.DataMountInputPath,"tenants/team-a/shared/data",true)
   if countPVCVolumes(pod,"data-tenant-a") != 1 { t.Fatal("training staged tenant root more than once") }
  }
 }
}

func TestTeamStoragePVCSourceAllowsReadOnlyAndWritableAliases(t *testing.T) {
 original := []any{pvcVolume("public-first", "data-tenant-a", true)}
 name, updated := ensurePVCVolume(original, "team", "data-tenant-a", false)
 if name != "public-first" || len(updated) != 1 { t.Fatalf("source duplicated: %s %#v", name, updated) }
 before := original[0].(map[string]any)["persistentVolumeClaim"].(map[string]any)
 after := updated[0].(map[string]any)["persistentVolumeClaim"].(map[string]any)
 if before["readOnly"] != true || after["readOnly"] != false { t.Fatalf("source permissions or immutability broken: before=%#v after=%#v", before, after) }
}
