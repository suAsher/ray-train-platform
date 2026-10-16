package repositories

import (
 "context"
 "errors"
 "testing"
 "time"
 "ray-train-platform-backend/auth"
 "ray-train-platform-backend/domain"
 eb "ray-train-platform-backend/environmentbuild"
)
func TestEnvironmentProfileMetadataPersistence(t *testing.T) {
 repo,_,_,_:=environmentTestService(t)
 assertEnvironmentProfileMetadataPersistence(t,repo)
}
func TestEnvironmentProfileMetadataPersistencePostgres(t *testing.T) {
 store,_:=evaluationPostgresStores(t,false)
 repo:=NewGormRepository(store.db)
 owner:=environmentOwner()
 ensurePostgresArtifactPrincipal(t,repo,context.Background(),auth.Principal{Subject:owner.UserID,Username:owner.UserID,TenantID:owner.TenantID,Roles:[]string{"Engineer"}})
 assertEnvironmentProfileMetadataPersistence(t,repo)
}
func assertEnvironmentProfileMetadataPersistence(t *testing.T,repo *GormRepository) {
 t.Helper()
 ctx:=context.Background()
 b:=registryPersistenceBuild("")
 b.Status,b.ImageDigest=eb.VerifyingPull,"sha256:3333333333333333333333333333333333333333333333333333333333333333"
 b.ImageReference=b.Host()+"/public/environment@"+b.ImageDigest
 if _,err:=repo.CreateEnvironmentBuild(ctx,b);err!=nil { t.Fatal(err) }
 claimed,err:=repo.ClaimEnvironmentBuild(ctx,"profile-finalizer",time.Now().UTC(),time.Minute,1,1)
 if err!=nil || claimed==nil { t.Fatalf("claim: %v",err) }
 ready:=*claimed
 ready.Status,ready.ImageID=eb.Ready,"image-profile"
 ready.TrustedEnvironment=domain.ImageEnvironment{Python:"3.10",CUDA:"12.6",PyTorch:"2.6.0+cu126",ValidationNotes:"CPU import checked; GPU validation not run"}
 for _,field:=range []string{"base","workspace","owner"} {
  invalid:=ready
  switch field {case "base":invalid.BaseImage+="forged";case "workspace":invalid.WorkspaceImage+="forged";case "owner":invalid.OwnerID="other"}
  if err:=repo.FinalizeEnvironmentBuild(ctx,invalid,"profile-finalizer");!errors.Is(err,eb.ErrConflict) { t.Fatalf("finalize accepted changed %s: %v",field,err) }
 }
 if err:=repo.FinalizeEnvironmentBuild(ctx,ready,"profile-finalizer");err!=nil { t.Fatal(err) }
 images,err:=repo.ListImagesForUser(ctx,"team-a","user-a","training")
 if err!=nil || len(images)!=1 || images[0].Environment!=ready.TrustedEnvironment || images[0].IsDefault { t.Fatalf("catalog lost profile or changed default: %+v %v",images,err) }
 versions,err:=repo.ListEnvironmentVersions(ctx,environmentOwner())
 if err!=nil || len(versions)!=1 || versions[0].BaseImage!=b.BaseImage { t.Fatalf("version changed immutable base: %+v %v",versions,err) }
}
