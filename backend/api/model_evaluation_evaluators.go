package api

import (
 "strings"
 "time"

 "github.com/gin-gonic/gin"
 "ray-train-platform-backend/domain"
 me "ray-train-platform-backend/modelevaluation"
)

type createModelEvaluatorInput struct {
 Name string `json:"name"`
 Description string `json:"description"`
 ImageReference string `json:"imageReference"`
 ImageDigest string `json:"imageDigest"`
 GitURL string `json:"gitUrl"`
 GitCommit string `json:"gitCommit"`
 EntryPoint []string `json:"entryPoint"`
 SchemaVersion string `json:"schemaVersion"`
 Protocol string `json:"protocol"`
}
func(h *Handler)createModelEvaluator(c *gin.Context){
 p:=actorPrincipal(c);if !p.HasRole(domain.RoleSuperAdmin){h.writeError(c,403,"EVALUATOR_ADMIN_REQUIRED","仅平台管理员可以登记评估方案");return}
 var input createModelEvaluatorInput;if !h.decodeEvaluationJSON(c,&input,evaluatorRequestFields){return}
 if h.modelEvaluationSubmission==nil{h.modelEvaluationError(c,me.ErrNotReady);return}
 if input.Protocol==""{input.Protocol=me.Protocol}
 // Resolve the catalogue reference using the same allowlists and JobSpec
 // validation as submissions. No job, storage directory or runtime is created.
 spec:=domain.JobSpec{Name:"evaluator-validation",Image:input.ImageReference,Source:domain.CodeSource{Type:"git",URL:input.GitURL,Commit:input.GitCommit},Entrypoint:domain.Entrypoint{Command:append([]string{},input.EntryPoint...)},TrainingEngine:domain.TrainingEngineRayTrain,RayVersion:domain.RayVersionCanary,Resources:evaluationDefaultResources()}
 result,err:=h.modelEvaluationSubmission.Preflight(c.Request.Context(),SubmissionInput{Principal:p,Spec:spec,Origin:domain.SubmissionOriginAPI})
 if err!=nil{h.writeSubmissionError(c,p,err);return}
 digest,ok:=extractImageSHA256Digest(result.Image);if !ok{h.modelEvaluationError(c,me.ErrInvalid);return}
 digest="sha256:"+digest
 if input.ImageDigest!=""&&input.ImageDigest!=digest{h.writeError(c,409,"EVALUATOR_IMAGE_CHANGED","登记镜像的实际摘要与所填摘要不一致");return}
 id,err:=newEvaluationID();if h.modelEvaluationError(c,err){return}
 evaluator:=me.Evaluator{ID:id,Name:strings.TrimSpace(input.Name),Description:input.Description,OwnerID:p.Subject,OwnerName:p.Username,TenantID:p.TenantID,Revision:1,Active:true,ImageReference:result.Image,ImageDigest:digest,GitURL:input.GitURL,GitCommit:input.GitCommit,EntryPoint:append([]string{},input.EntryPoint...),SchemaVersion:input.SchemaVersion,Protocol:input.Protocol,CreatedAt:time.Now().UTC()}
 if h.modelEvaluationError(c,me.ValidateEvaluator(evaluator)){return}
 evaluator,err=h.modelEvaluations.CreateEvaluator(c.Request.Context(),evaluator);if h.modelEvaluationError(c,err){return};h.writeSuccess(c,201,evaluator)
}
func(h *Handler)updateModelEvaluator(c *gin.Context){
 if !actorPrincipal(c).HasRole(domain.RoleSuperAdmin){h.writeError(c,403,"EVALUATOR_ADMIN_REQUIRED","仅平台管理员可以启停评估方案");return}
 var input struct{Active *bool `json:"active"`;Revision int64 `json:"revision"`}
 if !h.decodeEvaluationJSON(c,&input,map[string]bool{"active":true,"revision":true}){return};if input.Active==nil||input.Revision<1{h.modelEvaluationError(c,me.ErrInvalid);return}
 evaluator,err:=h.modelEvaluations.SetEvaluatorActive(c.Request.Context(),c.Param("evaluatorId"),*input.Active,input.Revision);if h.modelEvaluationError(c,err){return};h.writeSuccess(c,200,evaluator)
}
