package api

import (
 "context"
 "crypto/sha256"
 "encoding/hex"
 "errors"
 "sort"
 "strings"

 "ray-train-platform-backend/auth"
 "ray-train-platform-backend/integrations"
 "ray-train-platform-backend/mlflowtracking"
)

type trackingOwnedRecords interface {
 GetRun(context.Context,mlflowtracking.Actor,string)(mlflowtracking.Run,error)
 GetExperiment(context.Context,mlflowtracking.Actor,string)(mlflowtracking.Experiment,error)
}

// GrantedMLflowTracking delegates only after resolving current grants. It never
// replaces the HTTP principal, so audit and rate limits retain the machine actor.
type GrantedMLflowTracking struct {
 base mlflowTrackingService
 records trackingOwnedRecords
 access integrations.AccessStore
 cursorKey []byte
}
func NewGrantedMLflowTracking(base mlflowTrackingService,records trackingOwnedRecords,access integrations.AccessStore,key []byte)*GrantedMLflowTracking{
 return &GrantedMLflowTracking{base:base,records:records,access:access,cursorKey:append([]byte(nil),key...)}
}
func integrationTrackingError(err error)error{
 switch {case errors.Is(err,integrations.ErrNotFound):return mlflowtracking.ErrNotFound
 case errors.Is(err,integrations.ErrInvalid):return mlflowtracking.ErrInvalid
 case errors.Is(err,integrations.ErrConflict),errors.Is(err,integrations.ErrLimit):return mlflowtracking.ErrConflict
 default:return err}
}
func trackingMachinePrincipal(ctx context.Context,a mlflowtracking.Actor)(auth.Principal,error){
 p,ok:=auth.PrincipalFromContext(ctx)
 if a.IntegrationID=="" {
  if ok&&p.IntegrationID!=""{return auth.Principal{},mlflowtracking.ErrNotFound}
  return auth.Principal{},nil
 }
 if !ok||p.AuthType!=auth.AuthTypePAT||p.IntegrationID!=a.IntegrationID||p.Subject!=a.UserID||p.TenantID!=a.TenantID||p.Subject!="integration:"+p.IntegrationID{return auth.Principal{},mlflowtracking.ErrNotFound}
 return p,nil
}
func (s *GrantedMLflowTracking) owner(ctx context.Context,a mlflowtracking.Actor)(mlflowtracking.Actor,auth.Principal,integrations.Identity,error){
 p,err:=trackingMachinePrincipal(ctx,a);if err!=nil{return mlflowtracking.Actor{},p,integrations.Identity{},err}
 if a.IntegrationID==""{return a,p,integrations.Identity{},nil}
 if s.access==nil{return mlflowtracking.Actor{},p,integrations.Identity{},mlflowtracking.ErrUnavailable}
 id,err:=s.access.Resolve(ctx,p);if err!=nil{return mlflowtracking.Actor{},p,id,integrationTrackingError(err)}
 if id.ID!=a.IntegrationID||id.TenantID!=a.TenantID||id.OwnerUserID==""{return mlflowtracking.Actor{},p,id,mlflowtracking.ErrNotFound}
 return mlflowtracking.Actor{TenantID:id.TenantID,UserID:id.OwnerUserID},p,id,nil
}
func (s *GrantedMLflowTracking) experimentOwner(ctx context.Context,a mlflowtracking.Actor,id,permission string)(mlflowtracking.Actor,error){
 owner,p,_,err:=s.owner(ctx,a);if err!=nil{return owner,err}
 if a.IntegrationID!=""{_,err=s.access.Authorize(ctx,p,id,permission);if err!=nil{return owner,integrationTrackingError(err)}}
 return owner,nil
}
func (s *GrantedMLflowTracking) runOwner(ctx context.Context,a mlflowtracking.Actor,id,permission string)(mlflowtracking.Actor,mlflowtracking.Run,error){
 owner,p,_,err:=s.owner(ctx,a);if err!=nil{return owner,mlflowtracking.Run{},err}
 if !isMLflowTrackingID(id)||s.records==nil{return owner,mlflowtracking.Run{},mlflowtracking.ErrInvalid}
 run,err:=s.records.GetRun(ctx,owner,id);if err!=nil{return owner,run,err}
 if a.IntegrationID!=""{_,err=s.access.Authorize(ctx,p,run.ExperimentID,permission);err=integrationTrackingError(err)}
 return owner,run,err
}
func integrationCreateKey(a mlflowtracking.Actor,key string)(string,error){
 if key==""||len(key)>128||!isSafeASCIIHeaderValue(key){return "",mlflowtracking.ErrInvalid}
 if a.IntegrationID==""{return key,nil}
 sum:=sha256.Sum256([]byte("integration:"+a.IntegrationID+"\x00"+key));return hex.EncodeToString(sum[:]),nil
}
func (s *GrantedMLflowTracking) CreateExperiment(ctx context.Context,a mlflowtracking.Actor,key,name string)(mlflowtracking.Experiment,error){
 owner,p,id,err:=s.owner(ctx,a);if err!=nil{return mlflowtracking.Experiment{},err}
 if a.IntegrationID!=""&&!id.AllowCreateExperiments{return mlflowtracking.Experiment{},mlflowtracking.ErrNotFound}
 key,err=integrationCreateKey(a,key);if err!=nil{return mlflowtracking.Experiment{},err}
 exp,createErr:=s.base.CreateExperiment(ctx,owner,key,name)
 if a.IntegrationID!=""&&exp.ID!=""&&(createErr==nil||errors.Is(createErr,mlflowtracking.ErrPending)) {
  if err=s.access.GrantCreated(ctx,p,exp.ID);err!=nil{return mlflowtracking.Experiment{},integrationTrackingError(err)}
 }
 return exp,createErr
}
func (s *GrantedMLflowTracking) CreateRun(ctx context.Context,a mlflowtracking.Actor,id,key,name string)(mlflowtracking.Run,error){
 owner,err:=s.experimentOwner(ctx,a,id,"write");if err!=nil{return mlflowtracking.Run{},err}
 key,err=integrationCreateKey(a,key);if err!=nil{return mlflowtracking.Run{},err}
 return s.base.CreateRun(ctx,owner,id,key,name)
}
func (s *GrantedMLflowTracking) GetRun(ctx context.Context,a mlflowtracking.Actor,id string)(mlflowtracking.RunDetail,error){
 if a.IntegrationID=="" {if _,err:=trackingMachinePrincipal(ctx,a);err!=nil{return mlflowtracking.RunDetail{},err};return s.base.GetRun(ctx,a,id)}
 owner,_,err:=s.runOwner(ctx,a,id,"read");if err!=nil{return mlflowtracking.RunDetail{},err};return s.base.GetRun(ctx,owner,id)
}
func (s *GrantedMLflowTracking) LogRun(ctx context.Context,a mlflowtracking.Actor,id string,b mlflowtracking.Batch)error{
 if a.IntegrationID==""{if _,err:=trackingMachinePrincipal(ctx,a);err!=nil{return err};return s.base.LogRun(ctx,a,id,b)}
 owner,_,err:=s.runOwner(ctx,a,id,"write");if err!=nil{return err};return s.base.LogRun(ctx,owner,id,b)
}
func (s *GrantedMLflowTracking) FinishRun(ctx context.Context,a mlflowtracking.Actor,id,status string)(mlflowtracking.Run,error){
 if a.IntegrationID==""{if _,err:=trackingMachinePrincipal(ctx,a);err!=nil{return mlflowtracking.Run{},err};return s.base.FinishRun(ctx,a,id,status)}
 owner,_,err:=s.runOwner(ctx,a,id,"write");if err!=nil{return mlflowtracking.Run{},err};return s.base.FinishRun(ctx,owner,id,status)
}
func (s *GrantedMLflowTracking) ListExperiments(ctx context.Context,a mlflowtracking.Actor,limit int,cursor string)(mlflowtracking.ExperimentPage,error){
 owner,p,_,err:=s.owner(ctx,a);if err!=nil{return mlflowtracking.ExperimentPage{},err}
 if a.IntegrationID==""{return s.base.ListExperiments(ctx,a,limit,cursor)}
 if limit<1||limit>100{return mlflowtracking.ExperimentPage{},mlflowtracking.ErrInvalid}
 after,err:=s.unwrapCursor(a,"experiments","",cursor);if err!=nil{return mlflowtracking.ExperimentPage{},err}
 if after!=""&&!isMLflowTrackingID(after){return mlflowtracking.ExperimentPage{},mlflowtracking.ErrInvalid}
 ids,err:=s.access.ListGrantedExperimentIDs(ctx,p,"read");if err!=nil{return mlflowtracking.ExperimentPage{},integrationTrackingError(err)}
 ids=append([]string(nil),ids...);sort.Strings(ids)
 page:=mlflowtracking.ExperimentPage{Items:[]mlflowtracking.Experiment{}}
 for _,id:=range ids{if id<=after{continue};if _,err=s.access.Authorize(ctx,p,id,"read");err!=nil{if errors.Is(err,integrations.ErrNotFound){continue};return page,integrationTrackingError(err)}
  exp,readErr:=s.records.GetExperiment(ctx,owner,id);if readErr!=nil{return page,readErr};page.Items=append(page.Items,exp)
  if len(page.Items)>limit{page.Items=page.Items[:limit];page.NextCursor=s.wrapCursor(a,"experiments","",page.Items[limit-1].ID);break}
 }
 return page,nil
}
func (s *GrantedMLflowTracking) ListRuns(ctx context.Context,a mlflowtracking.Actor,id string,limit int,cursor string)(mlflowtracking.RunPage,error){
 owner,err:=s.experimentOwner(ctx,a,id,"read");if err!=nil{return mlflowtracking.RunPage{},err}
 if a.IntegrationID==""{return s.base.ListRuns(ctx,a,id,limit,cursor)}
 inner,err:=s.unwrapCursor(a,"runs",id,cursor);if err!=nil{return mlflowtracking.RunPage{},err}
 page,err:=s.base.ListRuns(ctx,owner,id,limit,inner);if err!=nil{return page,err}
 if page.NextCursor!=""{page.NextCursor=s.wrapCursor(a,"runs",id,page.NextCursor)};return page,nil
}
func (s *GrantedMLflowTracking) AuthorizeArtifact(ctx context.Context,a mlflowtracking.Actor,id string,write bool)(mlflowtracking.Actor,mlflowtracking.Run,error){
 permission:="artifacts:read";if write{permission="artifacts:write"}
 owner,run,err:=s.runOwner(ctx,a,id,permission);if err!=nil{return owner,run,err}
 if strings.TrimSpace(run.ID)==""||run.ID!=id{return owner,run,mlflowtracking.ErrNotFound}
 return owner,run,nil
}
