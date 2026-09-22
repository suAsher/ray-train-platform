package api

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/gin-gonic/gin"
	"ray-train-platform-backend/assistant"
	"ray-train-platform-backend/assistantidle"
	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/config"
	"ray-train-platform-backend/domain"
	"ray-train-platform-backend/k8s"
)

type AssistantStatusSource interface {
	ObserveAssistantStatus(context.Context, string) (k8s.AssistantIdleStatus, error)
}
type assistantAdminResponse struct {
	ObservedAt   time.Time                        `json:"observedAt"`
	Enabled      bool                             `json:"enabled"`
	ReadOnly     bool                             `json:"readOnly"`
	Capabilities assistant.Capabilities           `json:"capabilities"`
	Backends     []assistant.BackendRuntimeStatus `json:"backends"`
	Idle         k8s.AssistantIdleStatus          `json:"idle"`
}

var assistantStatusEpoch = regexp.MustCompile(`^[A-Za-z0-9-]{0,64}$`)

func newAssistantStatusHTTPClient(caFiles ...string) *http.Client {
	transport := &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 500 * time.Millisecond}).DialContext, ResponseHeaderTimeout: 500 * time.Millisecond, MaxResponseHeaderBytes: 4096, MaxIdleConns: 2, MaxConnsPerHost: 2, IdleConnTimeout: 30 * time.Second}
	if len(caFiles) > 0 && caFiles[0] != "" {
		path := caFiles[0]
		if !filepath.IsAbs(path) || filepath.Clean(path) != path {
			return nil
		}
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() || info.Size() > 1024*1024 {
			return nil
		}
		file, err := os.Open(caFiles[0])
		if err != nil {
			return nil
		}
		defer file.Close()
		info, err = file.Stat()
		if err != nil || !info.Mode().IsRegular() || info.Size() > 1024*1024 {
			return nil
		}
		pem, err := io.ReadAll(io.LimitReader(file, 1024*1024+1))
		if err != nil || len(pem) > 1024*1024 {
			return nil
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil
		}
		transport.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	}
	return &http.Client{Timeout: time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }, Transport: transport}
}
func (h *Handler) assistantAdminStatus(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	p, ok := auth.PrincipalFromGin(c)
	if !ok || p.Subject == "" || p.TenantID == "" {
		h.writeError(c, 401, "AUTH_REQUIRED", "请先登录有效的平台账户")
		return
	}
	if !p.HasRole(domain.RoleSuperAdmin) {
		h.writeError(c, 403, "FORBIDDEN", "仅平台超级管理员可查看共享推理资源")
		return
	}
	if c.Request.URL.RawQuery != "" {
		h.writeError(c, 400, "INVALID_STATUS_QUERY", "状态查询不接受地址或资源参数")
		return
	}
	select {
	case h.assistantStatusSlots <- struct{}{}:
		defer func() { <-h.assistantStatusSlots }()
	default:
		h.writeError(c, 429, "ASSISTANT_STATUS_BUSY", "状态查询繁忙，请稍后重试")
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
	defer cancel()
	response := assistantAdminResponse{ObservedAt: time.Now().UTC(), ReadOnly: true, Capabilities: assistant.Capabilities{ReadOnly: true, Modes: []string{}, Backends: []assistant.BackendStatus{}, Providers: map[string]assistant.ProviderStatus{"api": {}, "local": {}}}, Backends: []assistant.BackendRuntimeStatus{}, Idle: k8s.EmptyAssistantStatus(h.assistantIdleNamespace)}
	if h.assistant != nil {
		response.Capabilities = h.assistant.Capabilities()
		response.Enabled = response.Capabilities.Enabled
		if source, ok := h.assistant.(interface {
			AdminBackends() []assistant.BackendRuntimeStatus
		}); ok {
			response.Backends = source.AdminBackends()
		}
	}
	if h.assistantIdleNamespace != "" && config.ValidateAssistantIdleNamespace(h.assistantIdleNamespace) == nil && h.assistantStatusSource != nil {
		idle, err := h.assistantStatusSource.ObserveAssistantStatus(ctx, h.assistantIdleNamespace)
		if err == nil {
			response.Idle = idle
			status, reason := h.observeAssistantController(ctx, idle)
			response.Idle.Controller = status
			response.Idle.Reason = reason
			response.Idle.ObservationAvailable = reason == "observed" || reason == "disabled" || reason == "controller_stopped"
			workload := idle.RayService
			if idle.RuntimeType == "pod" {
				workload = idle.InferencePod
			}
			if status.State == assistantidle.StateReady && (idle.Enabled == nil || !*idle.Enabled || !workload.Ready || workload.Suspended || !assistantDeploymentsReady(idle.Deployments)) {
				response.Idle.Controller.State = assistantidle.StateUnknown
				response.Idle.Controller.Reason = "readiness_unconfirmed"
				response.Idle.Controller.Gate.Allow = false
				response.Idle.ObservationAvailable = false
				response.Idle.Reason = "observation_failed"
			}
			response.Idle.InferenceReady = response.Idle.ObservationAvailable && response.Idle.Controller.State == assistantidle.StateReady && response.Idle.Controller.Gate.Allow
		}
	}
	h.writeSuccess(c, 200, response)
}

// A zero desired controller count is an observed stop, not an HTTP failure.
func (h *Handler) observeAssistantController(ctx context.Context, idle k8s.AssistantIdleStatus) (assistantidle.ControllerStatus, string) {
	for _, deployment := range idle.Deployments {
		if deployment.Name == "assistant-idle-controller" && deployment.Desired == 0 {
			now := time.Now().UTC()
			return assistantidle.ControllerStatus{Enabled: idle.Enabled != nil && *idle.Enabled, State: assistantidle.StateDisabled, Reason: "controller_stopped", ObservedAt: &now}, "controller_stopped"
		}
	}
	return h.readAssistantControllerStatus(ctx, h.assistantIdleNamespace)
}
func assistantDeploymentsReady(deployments []k8s.AssistantDeploymentStatus) bool {
	if len(deployments) != 2 {
		return false
	}
	for _, d := range deployments {
		if d.Desired < 1 || d.Ready < d.Desired || d.Available < d.Desired {
			return false
		}
	}
	return true
}
func (h *Handler) readAssistantControllerStatus(ctx context.Context, namespace string) (assistantidle.ControllerStatus, string) {
	empty := assistantidle.ControllerStatus{State: assistantidle.StateUnknown, Reason: "awaiting_observation"}
	if h.assistantStatusHTTP == nil || h.assistantControllerCAFile == "" || namespace == "" || config.ValidateAssistantIdleNamespace(namespace) != nil {
		return empty, "controller_unavailable"
	}
	target := "https://assistant-idle-controller." + namespace + ".svc.cluster.local:8443/status"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return empty, "controller_unavailable"
	}
	response, err := h.assistantStatusHTTP.Do(request)
	if err != nil {
		return empty, "controller_unavailable"
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return empty, "controller_unavailable"
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, 4097))
	if err != nil || len(raw) > 4096 {
		return empty, "controller_unavailable"
	}
	status, err := decodeAssistantControllerStatus(raw)
	if err != nil {
		return empty, "controller_unavailable"
	}
	now := time.Now()
	if !status.Enabled {
		status.Gate.Allow = false
		status.State = assistantidle.StateDisabled
		status.Reason = "disabled"
		return status, "disabled"
	}
	if status.ObservedAt == nil || status.ObservedAt.After(now.Add(time.Second)) || now.Sub(*status.ObservedAt) > 5*time.Second {
		empty.Enabled = status.Enabled
		return empty, "controller_stale"
	}
	if status.State == assistantidle.StateUnknown || status.Reason == "observation_failed" || status.Reason == "observation_stale" {
		status.Gate.Allow = false
		return status, "observation_failed"
	}
	if status.State != assistantidle.StateReady {
		status.Gate.Allow = false
	}
	if status.State == assistantidle.StateReady && (!status.Gate.Allow || !now.Before(status.Gate.ValidUntil) || status.Gate.ValidUntil.After(now.Add(4*time.Second))) {
		status.State = assistantidle.StateUnknown
		status.Reason = "gate_closed"
		status.Gate.Allow = false
		return status, "controller_stale"
	}
	return status, "observed"
}
func decodeAssistantControllerStatus(raw []byte) (assistantidle.ControllerStatus, error) {
	var status assistantidle.ControllerStatus
	if json.Unmarshal(raw, &status) != nil || !assistantidle.ValidStatusState(status.State) || !assistantidle.ValidStatusReason(status.Reason) || !assistantStatusEpoch.MatchString(status.Gate.Epoch) {
		return assistantidle.ControllerStatus{}, errors.New("invalid controller status")
	}
	return status, nil
}
