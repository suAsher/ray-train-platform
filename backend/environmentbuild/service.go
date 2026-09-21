package environmentbuild

import (
 "context"
 "crypto/sha256"
 "encoding/hex"
 "encoding/json"
 "errors"
 "regexp"
 "strings"
 "time"
)

type Service struct { store Store; runner Runner; registry Registry; vault Vault; config Config; now func()time.Time; controllerID string }
func NewService(store Store, runner Runner, registry Registry, vault Vault, config Config) (*Service,error) {
 if config.Enabled && (store==nil || runner==nil || registry==nil || vault==nil || len(config.EncryptionKey)!=32 || !strings.Contains(config.BaseImage,"@sha256:") || !strings.Contains(config.WorkspaceImage,"@sha256:")){return nil,ErrInvalid}
 if config.AuthorizationTTL<=0 || config.AuthorizationTTL>24*time.Hour{config.AuthorizationTTL=24*time.Hour}
 if config.GlobalConcurrency<=0{config.GlobalConcurrency=2};if config.UserConcurrency<=0{config.UserConcurrency=1}
 id,err:=randomID("controller-");if err!=nil{return nil,err}
 config.EncryptionKey=append([]byte(nil),config.EncryptionKey...)
 return &Service{store:store,runner:runner,registry:registry,vault:vault,config:config,now:func()time.Time{return time.Now().UTC()},controllerID:id},nil
}
func(s *Service)Enabled()bool{return s!=nil && s.config.Enabled}
func(s *Service)Capabilities()map[string]any{if s==nil{return map[string]any{"enabled":false}};return map[string]any{"enabled":s.Enabled(),"registryHost":RegistryHost,"baseImage":s.config.BaseImage,"workspaceImage":s.config.WorkspaceImage,"supportedCaptureVersion":1}}
type CreateRequest struct {
 AuthorizationID string `json:"authorizationId"`
 Project string `json:"project"`
 Repository string `json:"repository"`
 Name string `json:"name"`
 Description string `json:"description"`
 Visibility string `json:"visibility"`
 IdempotencyKey string `json:"idempotencyKey"`
}
var targetPart=regexp.MustCompile(`^[a-z0-9]+(?:[._-][a-z0-9]+)*$`)
var requestKey=regexp.MustCompile(`^[A-Za-z0-9_-]{8,128}$`)
var digestPattern=regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
func validTarget(project,repository string)bool{
 if len(project)>128 || len(repository)>200 || !targetPart.MatchString(project){return false}
 for _,part:=range strings.Split(repository,"/"){if !targetPart.MatchString(part){return false}}
 return true
}
func(s *Service)Create(ctx context.Context, owner Owner, workspaceID string, req CreateRequest)(Build,error){
 if !s.Enabled(){return Build{},ErrUnavailable}
 if owner.UserID==""||owner.TenantID==""||!validTarget(req.Project,req.Repository)||strings.TrimSpace(req.Name)==""||len(req.Name)>128||len(req.Description)>2000||!requestKey.MatchString(req.IdempotencyKey){return Build{},ErrInvalid}
 if req.Visibility==""{req.Visibility="personal"};if req.Visibility!="personal"&&req.Visibility!="team"{return Build{},ErrInvalid}
 // Return existing operation before requiring a still-live authorization.
 builds,err:=s.store.ListEnvironmentBuilds(ctx,owner);if err!=nil{return Build{},err}
 for _,b:=range builds{if b.IdempotencyKey==req.IdempotencyKey{if b.WorkspaceID!=workspaceID||b.Project!=req.Project||b.Repository!=req.Repository||b.Name!=req.Name||b.Description!=req.Description||b.Visibility!=req.Visibility{return Build{},ErrConflict};return b,nil}}
 ws,err:=s.store.EnvironmentWorkspace(ctx,owner,workspaceID);if err!=nil{return Build{},err}
 if ws.State!="RUNNING"&&ws.State!="READY"{return Build{},ErrConflict}
 snapshot,err:=s.runner.InspectWorkspace(ctx,ws);if err!=nil{return Build{},ErrConflict}
 if snapshot.UID==""||snapshot.Image!=s.config.WorkspaceImage{return Build{},ErrConflict}
 sum:=sha256.Sum256([]byte(owner.TenantID+"\x00"+owner.UserID+"\x00"+req.IdempotencyKey));id:="env-"+hex.EncodeToString(sum[:16])
 now:=s.now()
 b:=Build{ID:id,TenantID:owner.TenantID,OwnerID:owner.UserID,WorkspaceID:workspaceID,Namespace:ws.Namespace,WorkspaceResourceName:ws.ResourceName,WorkspaceUID:snapshot.UID,BaseImage:s.config.BaseImage,WorkspaceImage:s.config.WorkspaceImage,Name:req.Name,Description:req.Description,Visibility:req.Visibility,Project:req.Project,Repository:req.Repository,Tag:id,Status:Queued,AuthID:req.AuthorizationID,IdempotencyKey:req.IdempotencyKey,Attempt:1,ArtifactExpiresAt:now.Add(24*time.Hour),CreatedAt:now,UpdatedAt:now}
 // Reserve the authorization before queueing; no job can see unbound material.
 if _,err=s.bindAuthorization(ctx,owner,req.AuthorizationID,b);err!=nil{return Build{},err}
 created,err:=s.store.CreateEnvironmentBuild(ctx,b)
 if err!=nil || created.AuthID!=b.AuthID{_=s.cleanupAuthorization(ctx,b)}
 return created,err
}
func(s *Service)Get(ctx context.Context,o Owner,id string)(Build,error){return s.store.EnvironmentBuild(ctx,o,id)}
func(s *Service)List(ctx context.Context,o Owner)([]Build,error){return s.store.ListEnvironmentBuilds(ctx,o)}
func(s *Service)Versions(ctx context.Context,o Owner)([]Version,error){return s.store.ListEnvironmentVersions(ctx,o)}
func(s *Service)Retry(ctx context.Context,o Owner,id,authorizationID string)(Build,error){
 b,err:=s.store.EnvironmentBuild(ctx,o,id);if err!=nil{return Build{},err}
 if (b.Status!=Failed&&b.Status!=AwaitingAuth)||b.CleanedAt==nil{return Build{},ErrConflict}
 if b.Attempt>=5 || !s.now().Before(b.ArtifactExpiresAt){return Build{},ErrConflict}
 if authorizationID==""{authorizationID=b.AuthID}
 if b.ImageDigest=="" {if _,err=s.bindAuthorization(ctx,o,authorizationID,b);err!=nil{return Build{},err}}
 return s.store.RetryEnvironmentBuild(ctx,o,id,authorizationID,s.now())
}
func(s *Service)Cancel(ctx context.Context,o Owner,id string)(Build,error){return s.store.CancelEnvironmentBuild(ctx,o,id)}
func(s *Service)Run(ctx context.Context){
 ticker:=time.NewTicker(5*time.Second);defer ticker.Stop()
 for{_ = s.Reconcile(ctx);select{case<-ctx.Done():return;case<-ticker.C:}}
}
func(s *Service)Reconcile(ctx context.Context)error{
 if !s.Enabled(){return nil}
 if err:=s.purgeExpiredCredentials(ctx);err!=nil{return err}
 // One short lease per phase; idempotent runner jobs survive API restarts.
 b,err:=s.store.ClaimEnvironmentBuild(ctx,s.controllerID,s.now(),6*time.Minute,s.config.GlobalConcurrency,s.config.UserConcurrency)
 if err!=nil||b==nil{return err}
 stepCtx,cancel:=context.WithTimeout(ctx,5*time.Minute);defer cancel()
 return s.reconcileBuild(stepCtx,*b)
}
func(s *Service)reconcileBuild(ctx context.Context,b Build)error{
 if b.Status==CancelRequested || b.Terminal(){
  retain:=(b.Status==Failed||b.Status==AwaitingAuth)&&s.now().Before(b.ArtifactExpiresAt)
  if err:=s.runner.Cleanup(ctx,b,retain);err!=nil{return err}
  if err:=s.cleanupAuthorization(ctx,b);err!=nil{return err}
  now:=s.now();b.CleanedAt=&now
  if b.Status==CancelRequested{b.Status=Canceled;b.Message="已取消；已推送到 Harbor 的镜像仍保留"}
  return s.store.SaveEnvironmentBuild(ctx,b,s.controllerID)
 }
 if !s.now().Before(b.ArtifactExpiresAt){return s.fail(ctx,b,Failed,"构建已超时，请重新创建环境版本")}
 if b.Status==Queued{b.Status=Capturing;return s.store.SaveEnvironmentBuild(ctx,b,s.controllerID)}
 var credentials *Credentials
 if b.Status==Pushing{
  a,err:=s.store.EnvironmentAuthorization(ctx,Owner{TenantID:b.TenantID,UserID:b.OwnerID},b.AuthID)
  if err!=nil{return s.fail(ctx,b,AwaitingAuth,"发布授权已失效，请重新授权后重试")}
  if a.BuildID!=b.ID||a.Target!=b.Project+"/"+b.Repository{return s.fail(ctx,b,AwaitingAuth,"发布授权与目标不匹配")}
  c,err:=s.credentials(ctx,a);if err!=nil{return s.fail(ctx,b,AwaitingAuth,"发布授权已过期，请重新授权")}
  if err=s.registry.CheckPush(ctx,c,b.Project+"/"+b.Repository);err!=nil{return s.fail(ctx,b,AwaitingAuth,"没有目标仓库推送权限，请检查 Harbor 授权")}
  credentials=&c
 }
 result,err:=s.runner.Step(ctx,b,credentials)
 if err!=nil{if errors.Is(err,ErrAuthorization){return s.fail(ctx,b,AwaitingAuth,"Harbor 拒绝发布，请重新授权后重试")};return s.fail(ctx,b,Failed,"此阶段执行失败，请检查环境依赖和平台构建状态后重试")}
 // Runner messages are trusted safe summaries, never command output or logs.
 if len(result.Message)>1000{result.Message="阶段执行中"};b.Message=result.Message
 if !result.Done{return s.store.SaveEnvironmentBuild(ctx,b,s.controllerID)}
 switch b.Status{
 case Capturing:
  if len(result.SnapshotJSON)==0||len(result.SnapshotJSON)>1024*1024{return s.fail(ctx,b,Failed,"依赖捕获结果不完整")};b.SnapshotJSON=result.SnapshotJSON;b.Status=Building
 case Building:
  b.Status=Validating
 case Validating:
  if !digestPattern.MatchString(result.ArtifactDigest){return s.fail(ctx,b,Failed,"构建产物摘要无效")};b.ArtifactDigest=result.ArtifactDigest;b.ChecksJSON=result.ChecksJSON;b.Status=Pushing
 case Pushing:
  if !digestPattern.MatchString(result.ImageDigest){return s.fail(ctx,b,Failed,"推送结果摘要无效")};b.ImageDigest=result.ImageDigest;b.ImageReference=RegistryHost+"/"+b.Project+"/"+b.Repository+"@"+result.ImageDigest;b.Status=VerifyingPull
 case VerifyingPull:
  b.ChecksJSON=mergeChecks(b.ChecksJSON,result.ChecksJSON)
  if !digestPattern.MatchString(b.ImageDigest){return s.fail(ctx,b,Failed,"镜像摘要缺失")};b.Status=Ready;b.ImageID="image-"+b.ID;b.Message="训练环境已就绪";return s.store.FinalizeEnvironmentBuild(ctx,b,s.controllerID)
 default:return ErrConflict
 }
 return s.store.SaveEnvironmentBuild(ctx,b,s.controllerID)
}
func(s *Service)fail(ctx context.Context,b Build,status,message string)error{b.ResumeStatus=b.Status;b.Status=status;b.Message=message;b.CleanedAt=nil;return s.store.SaveEnvironmentBuild(ctx,b,s.controllerID)}

func mergeChecks(existing,latest string)string{
 values:=map[string]any{};_ = json.Unmarshal([]byte(existing),&values)
 added:=map[string]any{};if len(latest)<=65536{_ = json.Unmarshal([]byte(latest),&added)}
 for key,value:=range added{values[key]=value};raw,err:=json.Marshal(values);if err!=nil{return existing};return string(raw)
}
