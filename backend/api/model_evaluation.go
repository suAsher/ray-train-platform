package api

import (
 "context"
 "errors"
 "strconv"
 "time"

 "github.com/gin-gonic/gin"
 "ray-train-platform-backend/auth"
 "ray-train-platform-backend/domain"
 me "ray-train-platform-backend/modelevaluation"
 "ray-train-platform-backend/repositories"
)

type ModelEvaluationStore interface {
 CreateEvaluator(context.Context,me.Evaluator)(me.Evaluator,error)
 GetEvaluator(context.Context,string)(me.Evaluator,error)
 ListEvaluators(context.Context,bool)([]me.Evaluator,error)
 SetEvaluatorActive(context.Context,string,bool,int64)(me.Evaluator,error)
 FindEvaluationRequest(context.Context,string,string,string)(me.Evaluation,error)
 ReserveEvaluation(context.Context,me.Evaluation)(me.Evaluation,bool,error)
 MarkEvaluationSubmitted(context.Context,string,string)error
 FailEvaluationSubmission(context.Context,string,string)error
 CancelEvaluationReservation(context.Context,string)(bool,error)
 GetEvaluation(context.Context,string,string,bool)(me.Evaluation,error)
 ListEvaluations(context.Context,me.Filter)(me.EvaluationPage,error)
 GetEvaluationByJobID(context.Context,string)(me.Evaluation,error)
 StoreEvaluationReport(context.Context,string,[]byte,[]byte,time.Time)(me.Evaluation,error)
 AuthorizeEvaluationJobToken(context.Context,string,[]byte,time.Time)(me.Evaluation,error)
}
type ModelEvaluationSubmission interface {
 Preflight(context.Context,SubmissionInput)(SubmissionPreflightResult,error)
 Submit(context.Context,SubmissionInput)(*domain.TrainingJob,error)
}
type modelEvaluationResponse struct {
 me.Evaluation
 CanRead bool `json:"canRead"`
 CanCancel bool `json:"canCancel"`
 CanRerun bool `json:"canRerun"`
}
func (h *Handler) RegisterModelEvaluationReadRoutes(group *gin.RouterGroup) {
 read:=group.Group("",auth.RequireScopes(domain.PATScopeJobsRead),h.modelEvaluationGuard(false))
 read.GET("/model-evaluators",h.listModelEvaluators)
 read.GET("/model-evaluations",h.listModelEvaluations)
 read.GET("/model-evaluations/compare",h.compareModelEvaluations)
 read.GET("/model-evaluations/:evaluationId",h.getModelEvaluation)
 read.GET("/model-evaluations/:evaluationId/report",h.getModelEvaluationReport)
}
func (h *Handler) RegisterModelEvaluationManagementRoutes(group *gin.RouterGroup) {
 write:=group.Group("",auth.RequireInteractiveSession(false),h.modelEvaluationGuard(true))
 write.POST("/model-evaluators",h.createModelEvaluator)
 write.PATCH("/model-evaluators/:evaluatorId",h.updateModelEvaluator)
 write.POST("/model-evaluations/preflight",h.preflightModelEvaluation)
 write.POST("/model-evaluations",h.createModelEvaluation)
 write.POST("/model-evaluations/:evaluationId/cancel",h.cancelModelEvaluation)
}
func (h *Handler) modelEvaluationGuard(write bool) gin.HandlerFunc {
 limiter:=newFixedWindowSourceArtifactLimiter(30,120,10000,time.Now)
 return func(c *gin.Context){
  p,ok:=auth.PrincipalFromGin(c)
  if !ok||p.Subject==""||p.TenantID==""{h.writeError(c,401,"AUTH_REQUIRED","请先登录平台");c.Abort();return}
  if p.IntegrationID!=""{h.writeError(c,403,"EVALUATION_MEMBER_REQUIRED","请使用平台成员身份访问模型评估");c.Abort();return}
  if h.modelEvaluations==nil{h.writeError(c,503,"EVALUATIONS_UNAVAILABLE","模型评估服务暂不可用");c.Abort();return}
  if len(c.Request.URL.RawQuery)>2048{h.modelEvaluationError(c,me.ErrInvalid);c.Abort();return}
  for _,name:=range []string{"evaluationId","evaluatorId"}{if id:=c.Param(name);id!=""&&!evaluationIdentifier(id){h.modelEvaluationError(c,me.ErrNotFound);c.Abort();return}}
  action:=sourceArtifactActionComplete;if write{action=sourceArtifactActionCreate}
  if allowed,_:=limiter.Allow(p.TenantID+"\x00"+p.Subject,action);!allowed{c.Header("Retry-After","60");h.writeError(c,429,"RATE_LIMITED","操作过于频繁，请稍后重试");c.Abort();return}
  c.Header("Cache-Control","no-store");c.Header("X-Content-Type-Options","nosniff")
  ctx,cancel:=context.WithTimeout(c.Request.Context(),2*time.Minute);defer cancel();c.Request=c.Request.WithContext(ctx);c.Next()
 }
}
func evaluationIdentifier(id string)bool{return len(id)<=128&&modelRequestKeyPattern.MatchString(id)}
func evaluationTerminal(state string)bool{return state==me.Succeeded||state==me.Failed||state==me.Cancelled}
func canCancelEvaluation(p auth.Principal,e me.Evaluation)bool{
 allowed:=p.Subject==e.OwnerID||p.HasRole(domain.RoleSuperAdmin)||(p.TenantID==e.TenantID&&p.HasRole(domain.RoleTenantAdmin))
 return allowed&&e.JobID!=""&&(e.State==me.Creating||e.State==me.Submitted||e.State==me.Running)
}
func evaluationResponse(p auth.Principal,e me.Evaluation)modelEvaluationResponse{return modelEvaluationResponse{Evaluation:e,CanRead:true,CanCancel:canCancelEvaluation(p,e),CanRerun:true}}
func (h *Handler) modelEvaluationError(c *gin.Context,err error)bool{
 if err==nil{return false}
 switch{case errors.Is(err,me.ErrUnauthorized):h.writeError(c,401,"EVALUATION_JOB_TOKEN_INVALID","评估任务凭据无效或已失效")
 case errors.Is(err,me.ErrQuota),errors.Is(err,repositories.ErrTrainingEventRateLimited):c.Header("Retry-After","60");h.writeError(c,429,"EVALUATION_LIMIT_EXCEEDED","评估待处理数量或报告提交频率已达上限，请稍后重试")
 case errors.Is(err,me.ErrInvalid):h.writeError(c,400,"INVALID_EVALUATION_REQUEST","评估参数、固定来源或报告格式无效")
 case errors.Is(err,me.ErrNotFound):h.writeError(c,404,"EVALUATION_NOT_FOUND","评估记录不存在或不可访问")
 case errors.Is(err,me.ErrConflict):h.writeError(c,409,"EVALUATION_CONFLICT","评估记录已变更或请求键冲突，请刷新后重试")
 case errors.Is(err,me.ErrNotReady):h.writeError(c,409,"EVALUATION_NOT_READY","模型、数据版本、评估方案或有效报告尚未就绪")
 default:h.writeError(c,503,"EVALUATION_UNAVAILABLE","评估服务暂不可用，请保留请求键后重试")}
 return true
}
func (h *Handler) evaluationForMember(c *gin.Context,id string)(me.Evaluation,bool){
 if !evaluationIdentifier(id){h.modelEvaluationError(c,me.ErrNotFound);return me.Evaluation{},false}
 p:=actorPrincipal(c);e,err:=h.modelEvaluations.GetEvaluation(c.Request.Context(),id,p.TenantID,p.HasRole(domain.RoleSuperAdmin))
 if h.modelEvaluationError(c,err){return e,false}
 if !me.CanRead(e,p.TenantID,p.HasRole(domain.RoleSuperAdmin)){h.modelEvaluationError(c,me.ErrNotFound);return me.Evaluation{},false}
 return e,true
}
func (h *Handler) listModelEvaluators(c *gin.Context){
 p:=actorPrincipal(c);includeInactive:=p.HasRole(domain.RoleSuperAdmin)&&c.Query("includeInactive")=="true"
 items,err:=h.modelEvaluations.ListEvaluators(c.Request.Context(),includeInactive);if h.modelEvaluationError(c,err){return};if items==nil{items=[]me.Evaluator{}}
 h.writeSuccess(c,200,gin.H{"items":items,"canManage":p.HasRole(domain.RoleSuperAdmin)})
}
func (h *Handler) listModelEvaluations(c *gin.Context){
 p:=actorPrincipal(c);limit:=50;if raw:=c.Query("limit");raw!=""{n,err:=strconv.Atoi(raw);if err!=nil||n<1{h.modelEvaluationError(c,me.ErrInvalid);return};limit=n};if limit>100{limit=100}
 f:=me.Filter{TenantID:p.TenantID,SuperAdmin:p.HasRole(domain.RoleSuperAdmin),ModelID:c.Query("modelId"),VersionID:c.Query("versionId"),State:c.Query("state"),Cursor:c.Query("cursor"),Limit:limit};if c.Query("mine")=="true"{f.OwnerID=p.Subject}
 page,err:=h.modelEvaluations.ListEvaluations(c.Request.Context(),f);if h.modelEvaluationError(c,err){return}
 items:=[]modelEvaluationResponse{};for _,e:=range page.Items{if me.CanRead(e,p.TenantID,p.HasRole(domain.RoleSuperAdmin)){items=append(items,evaluationResponse(p,e))}}
 h.writeSuccess(c,200,gin.H{"items":items,"nextCursor":page.NextCursor})
}
func(h *Handler)getModelEvaluation(c *gin.Context){e,ok:=h.evaluationForMember(c,c.Param("evaluationId"));if !ok{return};h.writeSuccess(c,200,evaluationResponse(actorPrincipal(c),e))}
func(h *Handler)compareModelEvaluations(c *gin.Context){
 left,ok:=h.evaluationForMember(c,c.Query("left"));if !ok{return};right,ok:=h.evaluationForMember(c,c.Query("right"));if !ok{return}
 result,err:=me.Compare(left,right);if h.modelEvaluationError(c,err){return};h.writeSuccess(c,200,result)
}
func(h *Handler)cancelModelEvaluation(c *gin.Context){
 e,ok:=h.evaluationForMember(c,c.Param("evaluationId"));if !ok{return};p:=actorPrincipal(c)
 if !canCancelEvaluation(p,e){h.writeError(c,403,"EVALUATION_CANCEL_FORBIDDEN","仅评估提交者或管理员可以停止运行中的评估");return}
 cancelled,err:=h.modelEvaluations.CancelEvaluationReservation(c.Request.Context(),e.ID)
 if h.modelEvaluationError(c,err){return}
 if cancelled{e.State=me.Cancelled;h.writeSuccess(c,202,evaluationResponse(p,e));return}
 job,err:=h.repository.Get(c.Request.Context(),e.TenantID,e.JobID)
 if err!=nil||job==nil||job.ID!=e.JobID||job.UserID!=e.OwnerID||job.SubmissionOrigin!=domain.SubmissionOriginEvaluation||job.ExternalSubmissionID!=e.ID{h.modelEvaluationError(c,me.ErrNotFound);return}
 if err:=h.repository.SetDesiredState(c.Request.Context(),e.TenantID,e.JobID,domain.DesiredCanceled);h.modelEvaluationError(c,err){return}
 h.writeSuccess(c,202,gin.H{"id":e.ID,"jobId":e.JobID,"desiredState":domain.DesiredCanceled})
}
