package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"ray-train-platform-backend/assistant"
	"ray-train-platform-backend/auth"
)

const assistantBodyLimit = 16 * 1024

var assistantJobID = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,127}$`)

type assistantQueryRequest struct {
	Question string `json:"question"`
	Mode string `json:"mode"`
	JobID string `json:"jobId"`
	IncludeLogs bool `json:"includeLogs"`
}

type assistantJobContext struct {
	JobID string `json:"jobId"`
	Status string `json:"status"`
}

type assistantQueryResponse struct {
	Answer string `json:"answer"`
	Mode string `json:"mode"`
	Reason string `json:"reason"`
	ObservedAt time.Time `json:"observedAt"`
	Citations []assistant.Evidence `json:"citations"`
	Context *assistantJobContext `json:"context,omitempty"`
	Warnings []string `json:"warnings"`
}

// RegisterAssistantRoutes exposes read-only retrieval, never arbitrary tools.
// Limits are per process; each replica independently enforces this ceiling.
func (h *Handler) RegisterAssistantRoutes(group *gin.RouterGroup) {
	g := group.Group("/assistant", auth.RequireInteractiveSession(false), h.assistantGuard())
	g.GET("/capabilities", h.assistantCapabilities)
	g.POST("/query", h.assistantQuery)
}

func (h *Handler) assistantGuard() gin.HandlerFunc {
	limiter := newFixedWindowSourceArtifactLimiter(8, 60, 10000, time.Now)
	inflight := make(chan struct{}, 8)
	var mu sync.Mutex
	active := make(map[string]int)
	return func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		p, ok := auth.PrincipalFromGin(c)
		if !ok || strings.TrimSpace(p.Subject) == "" || strings.TrimSpace(p.TenantID) == "" {
			h.writeError(c, 401, "AUTH_REQUIRED", "请先登录有效的平台账户")
			c.Abort()
			return
		}
		action := sourceArtifactActionComplete
		if c.Request.Method == http.MethodPost { action = sourceArtifactActionCreate }
		if allowed, _ := limiter.Allow(p.Subject, action); !allowed {
			c.Header("Retry-After", "60")
			h.writeError(c, 429, "RATE_LIMITED", "提问过于频繁，请稍后重试")
			c.Abort()
			return
		}
		if c.Request.Method == http.MethodGet { c.Next(); return }
		mu.Lock()
		busy := active[p.Subject] >= 2
		if !busy { active[p.Subject]++ }
		mu.Unlock()
		if busy { h.assistantBusy(c); return }
		defer func() {
			mu.Lock()
			active[p.Subject]--
			if active[p.Subject] == 0 { delete(active, p.Subject) }
			mu.Unlock()
		}()
		select {
		case inflight <- struct{}{}:
			defer func() { <-inflight }()
			c.Next()
		default:
			h.assistantBusy(c)
		}
	}
}

func (h *Handler) assistantBusy(c *gin.Context) {
	c.Header("Retry-After", "5")
	h.writeError(c, 429, "ASSISTANT_BUSY", "助手当前繁忙，请稍后重试")
	c.Abort()
}

func (h *Handler) assistantCapabilities(c *gin.Context) {
	if h.assistant != nil {
		h.writeSuccess(c, 200, h.assistant.Capabilities())
		return
	}
	h.writeSuccess(c, 200, assistant.Capabilities{
		Enabled: true, ReadOnly: true, Modes: []string{"docs"}, DefaultMode: "docs",
		Providers: map[string]assistant.ProviderStatus{"api": {Configured: false}, "local": {Configured: false}},
		Limitations: []string{"仅依据已发布文档和显式选中的授权任务回答；不执行修改", "不保留服务端对话历史", "常见凭据模式脱敏并非完整的敏感信息检测"},
	})
}

func (h *Handler) bindAssistant(c *gin.Context) (assistantQueryRequest, bool) {
	var req assistantQueryRequest
	mediaType, _, err := mime.ParseMediaType(c.GetHeader("Content-Type"))
	if err != nil || mediaType != "application/json" {
		h.writeError(c, 415, "INVALID_CONTENT_TYPE", "请使用 application/json 请求")
		return req, false
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, assistantBodyLimit)
	req, err = decodeAssistantQuery(c.Request.Body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			h.writeError(c, 413, "REQUEST_TOO_LARGE", "提问请求不能超过 16 KiB")
		} else {
			h.writeError(c, 400, "INVALID_ASSISTANT_QUERY", "请求必须是有效且仅含支持字段的 JSON")
		}
		return req, false
	}
	req.Question = strings.TrimSpace(req.Question)
	if req.Mode == "" { req.Mode = "auto" }
	validMode := req.Mode == "auto" || req.Mode == "api" || req.Mode == "local" || req.Mode == "docs"
	if !validMode || req.Question == "" || !utf8.ValidString(req.Question) || utf8.RuneCountInString(req.Question) > 4000 || (req.JobID != "" && !assistantJobID.MatchString(req.JobID)) || (req.IncludeLogs && req.JobID == "") {
		h.writeError(c, 400, "INVALID_ASSISTANT_QUERY", "问题须为 1–4000 字，mode 和 jobId 必须有效")
		return req, false
	}
	return req, true
}

func (h *Handler) assistantQuery(c *gin.Context) {
	req, ok := h.bindAssistant(c)
	if !ok { return }
	audited := h.auditAssistantQuery(c, req.Mode, req.IncludeLogs)
	ctx, cancel := context.WithTimeout(c.Request.Context(), 35*time.Second)
	defer cancel()
	c.Request = c.Request.WithContext(ctx)
	response := assistantQueryResponse{
		Mode: "docs", Reason: "docs_requested", ObservedAt: time.Now().UTC(),
		Citations: []assistant.Evidence{}, Warnings: []string{},
	}
	if req.JobID != "" {
		if !h.assistantJobEvidence(c, req.JobID, req.IncludeLogs, &response) { return }
	}
	docs, available := h.assistantDocuments(ctx, assistantRedact(req.Question))
	response.Citations = append(response.Citations, docs...)
	if !available { response.Warnings = append(response.Warnings, "帮助文档暂不可用；当前回答只包含可获取的授权信息。") }
	response.Answer = assistantDocsAnswer(response.Citations)
	if len(response.Citations) == 0 {
		response.Reason = "no_evidence"
	} else if req.Mode != "docs" {
		response.Reason = "provider_unavailable"
		if h.assistant != nil && !audited {
			response.Reason = "audit_unavailable"
			response.Warnings = append(response.Warnings, "审计服务暂不可用，未调用模型；已展示只读检索结果。")
		} else if h.assistant != nil {
			result, err := h.assistant.Answer(ctx, req.Mode, assistant.Input{Question: assistantRedact(req.Question), Evidence: response.Citations})
			answer := assistantPlainText(result.Answer, 8000)
			if err == nil && answer != "" && (result.Mode == "api" || result.Mode == "local") {
				response.Answer, response.Mode = answer, result.Mode
				response.Reason = assistantResultReason(result.Reason, "grounded_response")
			} else {
				response.Reason = assistantResultReason(result.Reason, "provider_unavailable")
				response.Warnings = append(response.Warnings, "模型未能提供回答，已展示文档检索结果。")
			}
		} else {
			response.Warnings = append(response.Warnings, "模型尚未配置，已展示文档检索结果。")
		}
	}
	h.writeSuccess(c, 200, response)
}

// Reasons are operational enums, never raw model or upstream error text.
func assistantResultReason(reason, fallback string) string {
	base := strings.TrimPrefix(reason, "fallback_")
	switch base {
	case "selected", "not_configured", "budget_exhausted", "authentication_error", "rate_limited", "provider_unavailable":
		return reason
	default:
		return fallback
	}
}

// The request is a flat, exactly-cased JSON object. Reject duplicate keys rather
// than silently letting a later value override consent or resource selection.
func decodeAssistantQuery(reader io.Reader) (assistantQueryRequest, error) {
	var req assistantQueryRequest
	body, err := io.ReadAll(reader)
	if err != nil { return req, err }
	if !utf8.Valid(body) { return req, errors.New("invalid UTF-8") }
	decoder := json.NewDecoder(bytes.NewReader(body))
	start, err := decoder.Token()
	if err != nil || start != json.Delim('{') { return req, errors.New("expected object") }
	seen := make(map[string]bool)
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil { return req, err }
		name, ok := key.(string)
		if !ok || seen[name] { return req, errors.New("duplicate or invalid field") }
		seen[name] = true
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil { return req, err }
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) { return req, errors.New("null field") }
		switch name {
		case "question": err = json.Unmarshal(raw, &req.Question)
		case "mode": err = json.Unmarshal(raw, &req.Mode)
		case "jobId": err = json.Unmarshal(raw, &req.JobID)
		case "includeLogs": err = json.Unmarshal(raw, &req.IncludeLogs)
		default: return req, errors.New("unknown field")
		}
		if err != nil { return req, err }
	}
	end, err := decoder.Token()
	if err != nil || end != json.Delim('}') { return req, errors.New("invalid object") }
	if _, err := decoder.Token(); err != io.EOF { return req, errors.New("trailing JSON") }
	return req, nil
}

type assistantAuditStore interface {
	CreateAssistantAuditLog(context.Context, auth.Principal, string, string, bool) error
}

func (h *Handler) auditAssistantQuery(c *gin.Context, mode string, includesLogs bool) bool {
	store, ok := h.repository.(assistantAuditStore)
	if !ok { return false }
	id, err := uuid.NewRandom()
	if err != nil { return false }
	p, ok := auth.PrincipalFromGin(c)
	if !ok { return false }
	// Metadata only. Do not persist question text, evidence, job identifiers,
	// user-controlled request headers, usernames or email addresses here.
	actor := auth.Principal{Subject: p.Subject, TenantID: p.TenantID, AuthType: p.AuthType}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
	defer cancel()
	return store.CreateAssistantAuditLog(ctx, actor, id.String(), mode, includesLogs) == nil
}
