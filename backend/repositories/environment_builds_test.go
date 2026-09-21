package repositories

import (
 "context"
 "errors"
 "strings"
 "testing"
 "time"

 eb "ray-train-platform-backend/environmentbuild"
)

type environmentVaultFake struct{values map[string][]byte}
func(v *environmentVaultFake)Put(_ context.Context,key string,value []byte,_ time.Time)error{v.values[key]=append([]byte(nil),value...);return nil}
func(v *environmentVaultFake)Get(_ context.Context,key string)([]byte,error){b,ok:=v.values[key];if !ok{return nil,eb.ErrNotFound};return append([]byte(nil),b...),nil}
func(v *environmentVaultFake)Delete(_ context.Context,key string)error{delete(v.values,key);return nil}
type environmentRegistryFake struct{denied bool}
func(*environmentRegistryFake)Authenticate(context.Context,eb.Credentials)error{return nil}
func(*environmentRegistryFake)Projects(context.Context,eb.Credentials,int)([]eb.Project,error){return []eb.Project{{Name:"public",CanPush:true}},nil}
func(r *environmentRegistryFake)CheckPush(context.Context,eb.Credentials,string)error{if r.denied{return eb.ErrAuthorization};return nil}
type environmentRunnerFake struct{phases []string;fail string;cleanup int}
func(*environmentRunnerFake)InspectWorkspace(context.Context,eb.Workspace)(eb.WorkspaceSnapshot,error){return eb.WorkspaceSnapshot{UID:"workspace-uid",Image:"harbor.wellspiking.ai/public/debug@sha256:"+strings.Repeat("1",64)},nil}
func(r *environmentRunnerFake)Step(_ context.Context,b eb.Build,c *eb.Credentials)(eb.StepResult,error){
 r.phases=append(r.phases,b.Status)
 if (b.Status==eb.Pushing)!=(c!=nil){return eb.StepResult{},errors.New("credentials crossed execution boundary")}
 if b.Status==r.fail{return eb.StepResult{},errors.New("internal runner failure with sensitive detail")}
 return eb.StepResult{Done:true,SnapshotJSON:`{"schemaVersion":1}`,ArtifactDigest:"sha256:"+strings.Repeat("2",64),ImageDigest:"sha256:"+strings.Repeat("3",64),ChecksJSON:`{"cpu":true}`},nil
}
func(r *environmentRunnerFake)Cleanup(context.Context,eb.Build,bool)error{r.cleanup++;return nil}
func environmentTestService(t *testing.T)(*GormRepository,*eb.Service,*environmentRunnerFake,*environmentVaultFake){
 t.Helper();repo:=testRepository(t)
 if err:=repo.db.AutoMigrate(&WorkspaceRecord{},&eb.Authorization{},&eb.Build{},&eb.Version{},&PlatformImageRecord{});err!=nil{t.Fatal(err)}
 // AutoMigrate cannot infer the explicit composite idempotency constraint.
 if err:=repo.db.Exec("CREATE UNIQUE INDEX env_test_idempotency ON environment_builds(tenant_id,owner_id,idempotency_key)").Error;err!=nil{t.Fatal(err)}
 if err:=repo.db.Create(&WorkspaceRecord{ID:"workspace-a",TenantID:"team-a",UserID:"user-a",Namespace:"tenant-a",RayClusterName:"workspace-cluster",ObservedState:"RUNNING"}).Error;err!=nil{t.Fatal(err)}
 runner:=&environmentRunnerFake{};vault:=&environmentVaultFake{values:map[string][]byte{}}
 service,err:=eb.NewService(repo,runner,&environmentRegistryFake{},vault,eb.Config{Enabled:true,BaseImage:"harbor.wellspiking.ai/public/base@sha256:"+strings.Repeat("0",64),WorkspaceImage:"harbor.wellspiking.ai/public/debug@sha256:"+strings.Repeat("1",64),EncryptionKey:[]byte(strings.Repeat("k",32)),GlobalConcurrency:1,UserConcurrency:1})
 if err!=nil{t.Fatal(err)};return repo,service,runner,vault
}
func environmentOwner()eb.Owner{return eb.Owner{TenantID:"team-a",UserID:"user-a"}}
func environmentRequest(auth string)eb.CreateRequest{return eb.CreateRequest{AuthorizationID:auth,Project:"public",Repository:"test-env",Name:"Test environment",Visibility:"personal",IdempotencyKey:"request-123456"}}
func createEnvironmentAuthorization(t *testing.T,s *eb.Service)eb.Authorization{t.Helper();a,err:=s.CreateAuthorization(context.Background(),environmentOwner(),eb.Credentials{Username:"harbor-user",Secret:"test-only-secret"});if err!=nil{t.Fatal(err)};return a}
func TestEnvironmentBuildFullLifecycleOwnsCredentialsAndPublishesAtomicVersion(t *testing.T){
 repo,s,runner,vault:=environmentTestService(t);ctx:=context.Background();a:=createEnvironmentAuthorization(t,s)
 for _,material:=range vault.values{if strings.Contains(string(material),"test-only-secret"){t.Fatal("plaintext credential in vault")}}
 b,err:=s.Create(ctx,environmentOwner(),"workspace-a",environmentRequest(a.ID));if err!=nil{t.Fatal(err)}
 duplicate,err:=s.Create(ctx,environmentOwner(),"workspace-a",environmentRequest(a.ID));if err!=nil||duplicate.ID!=b.ID{t.Fatalf("idempotency: %+v %v",duplicate,err)}
 for i:=0;i<8;i++{if err=s.Reconcile(ctx);err!=nil{t.Fatal(err)}}
 result,err:=s.Get(ctx,environmentOwner(),b.ID);if err!=nil{t.Fatal(err)}
 if result.Status!=eb.Ready||result.ImageID==""||result.CleanedAt==nil{t.Fatalf("not ready/cleaned: %+v",result)}
 if len(vault.values)!=0||runner.cleanup!=1{t.Fatalf("credentials/resources not cleaned: %d %d",len(vault.values),runner.cleanup)}
 images,err:=repo.ListImagesForUser(ctx,"team-a","user-a","training");if err!=nil||len(images)!=1{t.Fatalf("catalog missing: %v %v",images,err)}
 if images[0].IsDefault||images[0].Reference!=result.ImageReference||images[0].OwnerUserID!="user-a"{t.Fatalf("invalid catalog: %+v",images[0])}
 others,err:=repo.ListImagesForUser(ctx,"team-a","other","training");if err!=nil||len(others)!=0{t.Fatalf("private image leaked: %+v %v",others,err)}
 versions,err:=s.Versions(ctx,environmentOwner());if err!=nil||len(versions)!=1{t.Fatal("version missing")}
}
func TestEnvironmentBuildOwnerBoundaryAndIdempotencyConflict(t *testing.T){
 _,s,_,_:=environmentTestService(t);ctx:=context.Background();a:=createEnvironmentAuthorization(t,s)
 other:=eb.Owner{TenantID:"team-a",UserID:"user-b"}
 if _,err:=s.Projects(ctx,other,a.ID,1);!errors.Is(err,eb.ErrNotFound){t.Fatalf("cross-owner auth: %v",err)}
 if _,err:=s.Create(ctx,other,"workspace-a",environmentRequest(a.ID));!errors.Is(err,eb.ErrNotFound){t.Fatalf("cross-owner workspace: %v",err)}
 b,err:=s.Create(ctx,environmentOwner(),"workspace-a",environmentRequest(a.ID));if err!=nil{t.Fatal(err)}
 if _,err=s.Get(ctx,other,b.ID);!errors.Is(err,eb.ErrNotFound){t.Fatal("cross-owner build readable")}
 req:=environmentRequest(a.ID);req.Repository="different"
 if _,err=s.Create(ctx,environmentOwner(),"workspace-a",req);!errors.Is(err,eb.ErrConflict){t.Fatalf("idempotency target changed: %v",err)}
}
func TestEnvironmentBuildRetryReusesArtifactAndDoesNotLeakRunnerFailure(t *testing.T){
 _,s,r,v:=environmentTestService(t);ctx:=context.Background();a:=createEnvironmentAuthorization(t,s)
 b,err:=s.Create(ctx,environmentOwner(),"workspace-a",environmentRequest(a.ID));if err!=nil{t.Fatal(err)}
 r.fail=eb.Pushing
 for i:=0;i<5;i++{if err=s.Reconcile(ctx);err!=nil{t.Fatal(err)}}
 b,err=s.Get(ctx,environmentOwner(),b.ID);if err!=nil{t.Fatal(err)}
 if b.Status!=eb.Failed||b.CleanedAt==nil||strings.Contains(b.Message,"sensitive")||len(v.values)!=0{t.Fatalf("invalid failed cleanup: %+v",b)}
 a=createEnvironmentAuthorization(t,s);r.fail="";r.phases=nil
 if _,err=s.Retry(ctx,environmentOwner(),b.ID,a.ID);err!=nil{t.Fatal(err)}
 for i:=0;i<4;i++{if err=s.Reconcile(ctx);err!=nil{t.Fatal(err)}}
 if len(r.phases)!=2||r.phases[0]!=eb.Pushing||r.phases[1]!=eb.VerifyingPull{t.Fatalf("retry rebuilt captured environment: %v",r.phases)}
}
func TestEnvironmentBuildCancellationWinsStaleSaveAndClaimRecoversLease(t *testing.T){
 repo,s,_,_:=environmentTestService(t);ctx:=context.Background();a:=createEnvironmentAuthorization(t,s)
 b,err:=s.Create(ctx,environmentOwner(),"workspace-a",environmentRequest(a.ID));if err!=nil{t.Fatal(err)}
 now:=time.Now().UTC();claimed,err:=repo.ClaimEnvironmentBuild(ctx,"one",now,time.Minute,1,1);if err!=nil||claimed==nil{t.Fatal(err)}
 if next,err:=repo.ClaimEnvironmentBuild(ctx,"two",now,time.Minute,1,1);err!=nil||next!=nil{t.Fatal("live lease stolen")}
 if _,err=s.Cancel(ctx,environmentOwner(),b.ID);err!=nil{t.Fatal(err)}
 claimed.Status=eb.Building
 if err=repo.SaveEnvironmentBuild(ctx,*claimed,"one");err!=nil{t.Fatal(err)}
 got,err:=s.Get(ctx,environmentOwner(),b.ID);if err!=nil||got.Status!=eb.CancelRequested{t.Fatal("stale save lost cancellation")}
 if err=s.Reconcile(ctx);err!=nil{t.Fatal(err)}
 got,_=s.Get(ctx,environmentOwner(),b.ID);if got.Status!=eb.Canceled{t.Fatal("cancel did not complete")}
}
