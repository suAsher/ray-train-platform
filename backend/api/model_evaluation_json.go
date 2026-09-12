package api

import (
 "bytes"
 "encoding/json"
 "errors"
 "io"
 "net/http"
 "unicode/utf8"

 "github.com/gin-gonic/gin"
 me "ray-train-platform-backend/modelevaluation"
)

var evaluationRequestFields=map[string]bool{"modelId":true,"versionId":true,"datasetId":true,"datasetVersionId":true,"evaluatorId":true,"split":true,"sites":true,"config":true,"resources":true}
var evaluatorRequestFields=map[string]bool{"name":true,"description":true,"imageReference":true,"imageDigest":true,"gitUrl":true,"gitCommit":true,"entryPoint":true,"schemaVersion":true,"protocol":true}
var evaluationResourceFields=map[string]bool{"workerReplicas":true,"gpusPerWorker":true,"cpuPerWorker":true,"memoryPerWorker":true}
func(h *Handler)decodeEvaluationJSON(c *gin.Context,target any,allowed map[string]bool)bool{
 raw,err:=io.ReadAll(http.MaxBytesReader(c.Writer,c.Request.Body,32<<10))
 if err!=nil{var large *http.MaxBytesError;if errors.As(err,&large){h.writeError(c,413,"EVALUATION_BODY_TOO_LARGE","评估请求不能超过32 KiB，配置不能超过16 KiB")}else{h.modelEvaluationError(c,me.ErrInvalid)};return false}
 if !utf8.Valid(raw){h.modelEvaluationError(c,me.ErrInvalid);return false}
 check:=json.NewDecoder(bytes.NewReader(raw));check.UseNumber()
 if err:=checkEvaluationJSON(check,0,allowed);err!=nil{h.modelEvaluationError(c,me.ErrInvalid);return false}
 if _,err:=check.Token();err!=io.EOF{h.modelEvaluationError(c,me.ErrInvalid);return false}
 decoder:=json.NewDecoder(bytes.NewReader(raw));decoder.DisallowUnknownFields()
 if err:=decoder.Decode(target);err!=nil{h.modelEvaluationError(c,me.ErrInvalid);return false}
 return true
}
func checkEvaluationJSON(decoder *json.Decoder,depth int,allowed map[string]bool)error{
 if depth>16{return me.ErrInvalid}
 token,err:=decoder.Token();if err!=nil{return err}
 delimiter,isDelimiter:=token.(json.Delim)
 if !isDelimiter{if depth==0||allowed!=nil{return me.ErrInvalid};return nil}
 if depth==0&&delimiter!='{'{return me.ErrInvalid}
 switch delimiter{
 case '{':
  seen:=map[string]bool{}
  for decoder.More(){token,err:=decoder.Token();if err!=nil{return err};key,ok:=token.(string);if !ok||seen[key]||(allowed!=nil&&!allowed[key]){return me.ErrInvalid};seen[key]=true
   var nested map[string]bool;if depth==0&&key=="resources"{nested=evaluationResourceFields}
   if err:=checkEvaluationJSON(decoder,depth+1,nested);err!=nil{return err}
  }
 case '[':for decoder.More(){if err:=checkEvaluationJSON(decoder,depth+1,nil);err!=nil{return err}}
 default:return me.ErrInvalid
 }
 _,err=decoder.Token();return err
}
