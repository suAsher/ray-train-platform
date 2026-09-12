package api

import (
 "crypto/sha256"
 "encoding/hex"
 "errors"
 "io"
 "mime"
 "net/http"
 "reflect"
 "strconv"
 "time"

 "github.com/gin-gonic/gin"
 "ray-train-platform-backend/auth"
 "ray-train-platform-backend/domain"
 me "ray-train-platform-backend/modelevaluation"
)

// Publish/Open verify a bounded archive on temporary disk. This shared gate
// limits their aggregate disk footprint per backend, not just requests/minute.
func(h *Handler)acquireEvaluationCodeOperation(c *gin.Context)(func(),bool){
 if h.evaluationCode==nil||h.evaluationCodeOperations==nil{h.writeError(c,503,"EVALUATION_CODE_UNAVAILABLE","评估代码快照服务暂不可用");return nil,false}
 select{case h.evaluationCodeOperations<-struct{}{}:return func(){<-h.evaluationCodeOperations},true
 default:c.Header("Retry-After","5");h.writeError(c,429,"EVALUATION_CODE_BUSY","评估代码快照读写繁忙，请稍后重试");return nil,false}
}
func(h *Handler)evaluationCodeSource(c *gin.Context,id string)(*domain.SourceArtifact,bool){
 if !evaluationIdentifier(id){h.modelEvaluationError(c,me.ErrInvalid);return nil,false}
 if h.evaluationSourceArtifacts==nil{h.writeError(c,503,"EVALUATION_CODE_UNAVAILABLE","代码上传记录服务暂不可用");return nil,false}
 p:=actorPrincipal(c);artifact,err:=h.evaluationSourceArtifacts.GetSourceArtifact(c.Request.Context(),p.TenantID,p.Subject,id)
 if err!=nil||artifact==nil||artifact.ID!=id||artifact.TenantID!=p.TenantID||artifact.UserID!=p.Subject{h.writeError(c,404,"SOURCE_ARTIFACT_NOT_FOUND","代码包不存在或不属于当前用户和团队");return nil,false}
 if artifact.State!=domain.SourceArtifactReady{h.writeError(c,409,"SOURCE_ARTIFACT_NOT_READY","请先完成代码包上传和校验");return nil,false}
 if artifact.SizeBytes<1||artifact.SizeBytes>me.MaxEvaluationCodeSize||!artifactSHAValid(artifact.SHA256){h.writeError(c,400,"EVALUATION_CODE_INVALID","评估代码包必须为有效 ZIP 且不超过64 MiB");return nil,false}
 copy:=*artifact;return &copy,true
}
func evaluationCodeRequestID(p auth.Principal,key string)string{
 sum:=sha256.Sum256([]byte("model-evaluator-code\x00"+p.TenantID+"\x00"+p.Subject+"\x00"+key));return hex.EncodeToString(sum[:16])
}
func sameEvaluatorCodeDefinition(a,b me.Evaluator)bool{
 return a.ID==b.ID&&a.OwnerID==b.OwnerID&&a.TenantID==b.TenantID&&a.Name==b.Name&&a.Description==b.Description&&a.ImageReference==b.ImageReference&&a.ImageDigest==b.ImageDigest&&a.GitURL==b.GitURL&&a.GitCommit==b.GitCommit&&a.SchemaVersion==b.SchemaVersion&&a.Protocol==b.Protocol&&reflect.DeepEqual(a.EntryPoint,b.EntryPoint)&&reflect.DeepEqual(a.Code,b.Code)
}
func(h *Handler)publishModelEvaluatorCode(c *gin.Context,evaluator me.Evaluator,artifact domain.SourceArtifact){
 existing,err:=h.modelEvaluations.GetEvaluator(c.Request.Context(),evaluator.ID)
 if err==nil{
  if !sameEvaluatorCodeDefinition(existing,evaluator){h.modelEvaluationError(c,me.ErrConflict);return}
  h.writeSuccess(c,200,existing);return
 }
 if !errors.Is(err,me.ErrNotFound){h.modelEvaluationError(c,err);return}
 snapshot,err:=h.evaluationCode.Publish(c.Request.Context(),evaluator.Code.ID,artifact)
 if h.modelEvaluationError(c,err){return}
 if snapshot!=*evaluator.Code{h.writeError(c,503,"EVALUATION_CODE_SNAPSHOT_MISMATCH","代码快照与上传校验记录不一致，请保留请求键重试");return}
 created,err:=h.modelEvaluations.CreateEvaluator(c.Request.Context(),evaluator)
 if err!=nil{
  // A concurrent identical registration or lost DB response must return the
  // same immutable record. Never delete a snapshot that might be referenced.
  existing,readErr:=h.modelEvaluations.GetEvaluator(c.Request.Context(),evaluator.ID)
  if readErr==nil&&sameEvaluatorCodeDefinition(existing,evaluator){h.writeSuccess(c,200,existing);return}
  h.modelEvaluationError(c,err);return
 }
 h.writeSuccess(c,201,created)
}
func evaluationCodeJobSource(evaluator me.Evaluator)domain.CodeSource{
 if evaluator.Code!=nil{return domain.CodeSource{Type:"evaluation-archive",ArtifactID:evaluator.Code.ID,ArtifactSHA256:evaluator.Code.SHA256}}
 return domain.CodeSource{Type:"git",URL:evaluator.GitURL,Commit:evaluator.GitCommit}
}
func evaluationCodeSourceMatches(source domain.CodeSource,evaluator me.Evaluator)bool{
 expected:=evaluationCodeJobSource(evaluator)
 if evaluator.Code!=nil{return source==expected}
 return source.Type=="git"&&source.URL==expected.URL&&source.Commit==expected.Commit&&source.ArtifactID==""&&source.ArtifactSHA256==""&&source.ArtifactObjectKey==""&&source.URI==""&&source.Snapshot==""
}
func(h *Handler)downloadEvaluationCode(c *gin.Context){
 evaluation,ok:=c.MustGet("model-evaluation-job").(me.Evaluation)
 if !ok||evaluation.Evaluator.Code==nil{h.modelEvaluationError(c,me.ErrNotFound);return}
 snapshot:=*evaluation.Evaluator.Code
 if h.modelEvaluationError(c,me.ValidateCodeSnapshot(snapshot)){return}
 if c.GetHeader("Range")!=""{h.writeError(c,416,"EVALUATION_RANGE_UNSUPPORTED","evaluation code downloads do not support ranges");return}
 release,ok:=h.acquireEvaluationCodeOperation(c);if !ok{return};defer release()
 reader,size,err:=h.evaluationCode.Open(c.Request.Context(),snapshot)
 if h.modelEvaluationError(c,err){return}
 if reader==nil{h.writeError(c,503,"EVALUATION_CODE_UNAVAILABLE","评估代码快照读取失败");return};defer reader.Close()
 if size!=snapshot.SizeBytes{h.writeError(c,503,"EVALUATION_CODE_SNAPSHOT_MISMATCH","评估代码快照大小校验失败");return}
 controller:=http.NewResponseController(c.Writer);deadline:=time.Now().Add(30*time.Minute)
 if requestDeadline,ok:=c.Request.Context().Deadline();ok&&requestDeadline.Before(deadline){deadline=requestDeadline}
 if err:=controller.SetWriteDeadline(deadline);err==nil{defer controller.SetWriteDeadline(time.Time{})}else if !errors.Is(err,http.ErrNotSupported){h.modelEvaluationError(c,err);return}
 c.Header("Content-Type","application/zip");c.Header("Content-Disposition",mime.FormatMediaType("attachment",map[string]string{"filename":"evaluation-code-"+snapshot.ID+".zip"}));c.Header("Content-Length",strconv.FormatInt(size,10));c.Header("X-Content-SHA256",snapshot.SHA256);c.Header("Accept-Ranges","none")
 // Open has already verified every byte and ZIP member before response
 // headers. Streaming remains bounded and never exposes a storage key or URL.
 n,copyErr:=io.CopyBuffer(c.Writer,io.LimitReader(reader,size),make([]byte,64<<10))
 if copyErr!=nil||n!=size{
  if c.Writer.Written(){_ = c.Error(me.ErrNotReady);return}
  for _,header:=range []string{"Content-Type","Content-Disposition","Content-Length","X-Content-SHA256"}{c.Header(header,"")};h.modelEvaluationError(c,me.ErrNotReady)
 }
}
