package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"ray-train-platform-backend/observability"
)

func(h *Handler) decodeMLflowIntegrationBatch(c *gin.Context)(observability.MLflowLogBatch,bool) {
	var batch observability.MLflowLogBatch
	c.Request.Body=http.MaxBytesReader(c.Writer,c.Request.Body,256*1024)
	body,err:=io.ReadAll(c.Request.Body)
	if err!=nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err,&tooLarge) {h.writeError(c,413,"MLFLOW_BATCH_TOO_LARGE","MLflow batch exceeds 256 KiB")} else {h.writeError(c,400,"MLFLOW_BATCH_INVALID","could not read MLflow batch")}
		return batch,false
	}
	check:=json.NewDecoder(bytes.NewReader(body))
	if err:=checkMLflowJSONValue(check,0);err!=nil {h.writeError(c,400,"MLFLOW_BATCH_INVALID","MLflow batch contains invalid or duplicate fields");return batch,false}
	decoder:=json.NewDecoder(bytes.NewReader(body));decoder.DisallowUnknownFields()
	err=decoder.Decode(&batch)
	if err==nil {var extra any;if decoder.Decode(&extra)!=io.EOF {err=observability.ErrMLflowBatchInvalid}}
	if err==nil {err=batch.Validate()}
	if err!=nil {h.writeError(c,400,"MLFLOW_BATCH_INVALID","MLflow batch is invalid");return batch,false}
	return batch,true
}

// encoding/json normally accepts duplicate object fields. Reject ambiguous
// payloads before decoding them, including nested metric and tag objects.
func checkMLflowJSONValue(decoder *json.Decoder,depth int) error {
	if depth>8 {return observability.ErrMLflowBatchInvalid}
	token,err:=decoder.Token();if err!=nil {return err}
	delim,ok:=token.(json.Delim);if !ok {return nil}
	switch delim {
	case '{':
		seen:=map[string]struct{}{}
		for decoder.More() {
			token,err:=decoder.Token();if err!=nil {return err}
			key,ok:=token.(string);if !ok {return observability.ErrMLflowBatchInvalid}
			key=strings.ToLower(key)
			if _,exists:=seen[key];exists {return observability.ErrMLflowBatchInvalid};seen[key]=struct{}{}
			if err:=checkMLflowJSONValue(decoder,depth+1);err!=nil {return err}
		}
	case '[':
		for decoder.More() {if err:=checkMLflowJSONValue(decoder,depth+1);err!=nil {return err}}
	default:return observability.ErrMLflowBatchInvalid
	}
	_,err=decoder.Token();return err
}
