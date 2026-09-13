package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/gin-gonic/gin"
	"io"
	"net/http"
	"ray-train-platform-backend/domain"
	ms "ray-train-platform-backend/modelserving"
	"time"
)

const servingMaxPayload = 1 << 20

type modelServingKubernetes interface {
	EnsureModelServingService(context.Context, *domain.TrainingJob) (string, error)
	DeleteModelServingService(context.Context, string, string, string) error
}

var servingHTTPClient = &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{Proxy: nil, MaxIdleConns: 32, MaxConnsPerHost: 8, IdleConnTimeout: time.Minute}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

func servingJobTerminal(job *domain.TrainingJob) bool {
	if job == nil {
		return false
	}
	switch job.ObservedState {
	case domain.StateSucceeded, domain.StateFailed, domain.StateCanceled, domain.StateTimedOut:
		return true
	}
	return false
}
func (h *Handler) servingTarget(ctx context.Context, d ms.Deployment) (string, error) {
	if ms.Terminal(d.State) || d.State == ms.Stopping || !d.ExpiresAt.After(time.Now()) || h.modelServingKubernetes == nil {
		return "", ms.ErrNotReady
	}
	job, err := h.repository.Get(ctx, d.TenantID, d.JobID)
	if err != nil || !servingJobMatches(job, d) || servingJobTerminal(job) || job.DesiredState == domain.DesiredCanceled {
		return "", ms.ErrNotReady
	}
	return h.modelServingKubernetes.EnsureModelServingService(ctx, job)
}
func checkServingHealth(ctx context.Context, target string, d ms.Deployment) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target+"/healthz", nil)
	if err != nil {
		return err
	}
	response, err := servingHTTPClient.Do(req)
	if err != nil {
		return ms.ErrNotReady
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 4097))
	if err != nil || response.StatusCode != 200 || len(raw) > 4096 {
		return ms.ErrNotReady
	}
	var health struct {
		Ready        bool   `json:"ready"`
		Protocol     string `json:"protocol"`
		DeploymentID string `json:"deploymentId"`
		ModelSHA256  string `json:"modelSha256"`
	}
	if json.Unmarshal(raw, &health) != nil || !health.Ready || health.Protocol != ms.Protocol || health.DeploymentID != d.ID || health.ModelSHA256 != d.ModelSHA256 {
		return ms.ErrNotReady
	}
	return nil
}
func (h *Handler) getModelServiceHealth(c *gin.Context) {
	d, ok := h.serviceForMember(c, false)
	if !ok {
		return
	}
	target, err := h.servingTarget(c.Request.Context(), d)
	if err == nil {
		err = checkServingHealth(c.Request.Context(), target, d)
	}
	h.writeSuccess(c, 200, gin.H{"ready": err == nil, "status": d.State})
}
func (h *Handler) invokeModelService(c *gin.Context) {
	d, ok := h.serviceForMember(c, false)
	if !ok {
		return
	}
	select {
	case h.servingRequests <- struct{}{}:
		defer func() { <-h.servingRequests }()
	default:
		h.writeError(c, 429, "SERVING_BUSY", "推理请求繁忙，请稍后重试")
		return
	}
	if c.ContentType() != "application/json" {
		h.writeError(c, 415, "SERVING_JSON_REQUIRED", "推理请求必须使用 application/json")
		return
	}
	raw, err := io.ReadAll(io.LimitReader(c.Request.Body, servingMaxPayload+1))
	if err != nil || len(raw) > servingMaxPayload {
		h.writeError(c, 413, "SERVING_REQUEST_TOO_LARGE", "推理请求不能超过1 MiB")
		return
	}
	if !json.Valid(raw) {
		h.servingError(c, ms.ErrInvalid)
		return
	}
	target, err := h.servingTarget(c.Request.Context(), d)
	if h.servingError(c, err) {
		return
	}
	if h.servingError(c, checkServingHealth(c.Request.Context(), target, d)) {
		return
	}
	request, err := http.NewRequestWithContext(c.Request.Context(), http.MethodPost, target+"/invocations", bytes.NewReader(raw))
	if h.servingError(c, err) {
		return
	}
	// User model code receives only the inference body, never cookies, PATs,
	// Authorization, X-Forwarded headers or caller-controlled routing headers.
	request.Header.Set("Content-Type", "application/json")
	response, err := servingHTTPClient.Do(request)
	if err != nil {
		h.writeError(c, 502, "SERVING_REQUEST_FAILED", "推理服务无响应或已超时，请检查服务任务日志")
		return
	}
	defer response.Body.Close()
	output, err := io.ReadAll(io.LimitReader(response.Body, servingMaxPayload+1))
	if err != nil || len(output) > servingMaxPayload || !json.Valid(output) {
		h.writeError(c, 502, "SERVING_RESPONSE_INVALID", "推理响应必须为不超过1 MiB的JSON")
		return
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		h.writeError(c, 502, "SERVING_MODEL_ERROR", fmt.Sprintf("模型返回HTTP %d，请检查输入与服务任务日志", response.StatusCode))
		return
	}
	c.Data(response.StatusCode, "application/json", output)
}
