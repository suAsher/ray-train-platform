package api

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/gin-gonic/gin"
	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
	"ray-train-platform-backend/repositories"
)

const mlflowNativeBasePath = "/api/v1/mlflow-native"
const mlflowNativeRequestTimeout = 15 * time.Minute
const mlflowNativeAuditTimeout = 5 * time.Second

var mlflowNativeAPIPath = regexp.MustCompile(`^/api/[1-9][0-9]*\.[0-9]+/(mlflow|mlflow-artifacts)/`)

// RegisterMLflowNativeRoutes exposes the shared MLflow server's native API.
// Unlike the governed tracking subset, it does not filter experiment owners or
// translate IDs. Only an explicitly issued personal mlflow:full PAT can enter.
func (h *Handler) RegisterMLflowNativeRoutes(group *gin.RouterGroup) {
	target, err := parseMLflowNativeTarget(h.mlflowTrackingURL)
	if !h.mlflowDashboardEnabled || h.mlflowDashboardStore == nil || err != nil {
		return
	}
	h.mlflowNativeRegistered = true
	limiter := newFixedWindowSourceArtifactLimiter(1200, 1200, 10000, time.Now)
	group.Any("/mlflow-native/*path", h.mlflowNativeGuard(limiter), func(c *gin.Context) {
		h.proxyMLflowNative(c, target)
	})
}

func parseMLflowNativeTarget(raw string) (*url.URL, error) {
	target, err := url.Parse(raw)
	if err != nil || target.Host == "" || (target.Scheme != "http" && target.Scheme != "https") || target.User != nil || target.RawQuery != "" || target.Fragment != "" || target.RawPath != "" || (target.Path != "" && target.Path != "/mlflow" && target.Path != "/mlflow/") {
		return nil, fmt.Errorf("invalid MLflow native upstream")
	}
	return target, nil
}

func (h *Handler) mlflowNativeGuard(limiter SourceArtifactLimiter) gin.HandlerFunc {
	return func(c *gin.Context) {
		p, ok := auth.PrincipalFromGin(c)
		if !ok {
			h.writeError(c, 401, "AUTH_REQUIRED", "authentication is required")
			c.Abort()
			return
		}
		if p.AuthType != auth.AuthTypePAT || p.IntegrationID != "" || !p.HasScope(domain.PATScopeMLflowFull) {
			h.writeError(c, 403, "MLFLOW_NATIVE_SCOPE_REQUIRED", "native MLflow access requires a personal token with mlflow:full")
			c.Abort()
			return
		}
		if !isMLflowDashboardMethodAllowed(c.Request.Method) {
			c.Header("Allow", mlflowDashboardAllow)
			h.writeError(c, 405, "MLFLOW_NATIVE_METHOD_INVALID", "request method is not allowed")
			c.Abort()
			return
		}
		if !validMLflowNativePath(c.Param("path")) || hasMLflowNativeCredentialQuery(c.Request.URL.Query()) {
			h.writeError(c, 400, "MLFLOW_NATIVE_PATH_INVALID", "request must address a native MLflow API path")
			c.Abort()
			return
		}
		if allowed, _ := limiter.Allow(p.TenantID+"\x00"+p.Subject, sourceArtifactActionCreate); !allowed {
			c.Header("Retry-After", "60")
			h.writeError(c, 429, "RATE_LIMITED", "MLflow request rate limit exceeded")
			c.Abort()
			return
		}
		c.Header("Cache-Control", "no-store")
		ctx, cancel := context.WithTimeout(c.Request.Context(), mlflowNativeRequestTimeout)
		defer cancel()
		c.Request = c.Request.WithContext(ctx)
		controller := http.NewResponseController(c.Writer)
		deadline := time.Now().Add(mlflowNativeRequestTimeout)
		_ = controller.SetReadDeadline(deadline)
		_ = controller.SetWriteDeadline(deadline)
		defer controller.SetReadDeadline(time.Time{})
		defer controller.SetWriteDeadline(time.Time{})
		c.Next()
	}
}

func validMLflowNativePath(value string) bool {
	if !mlflowNativeAPIPath.MatchString(value) {
		return false
	}
	// Reject traversal at every decoding level; the upstream framework may
	// decode a path once more than Go. Ordinary percent signs remain valid.
	for depth := 0; depth < 5; depth++ {
		if strings.Contains(value, "\\") || strings.Contains(value, "//") || strings.IndexFunc(value, unicode.IsControl) >= 0 {
			return false
		}
		for _, part := range strings.Split(value, "/") {
			if part == "." || part == ".." {
				return false
			}
		}
		decoded, err := url.PathUnescape(value)
		if err != nil || decoded == value {
			return true
		}
		value = decoded
	}
	return false
}

func hasMLflowNativeCredentialQuery(query url.Values) bool {
	for key := range query {
		switch strings.ToLower(key) {
		case "access_token", "authorization", "api_key":
			return true
		}
	}
	return false
}

func (h *Handler) proxyMLflowNative(c *gin.Context, target *url.URL) {
	p, _ := auth.PrincipalFromGin(c)
	startedAt := time.Now()
	event := repositories.MLflowAuditEvent{Action: repositories.MLflowAuditNativeProxy, Principal: p, Method: c.Request.Method, Path: c.Request.URL.Path, RequestID: c.GetHeader("X-Request-ID")}
	if c.Request.Method != http.MethodGet && c.Request.Method != http.MethodHead && c.Request.Method != http.MethodOptions {
		event.Status = 102
		if err := h.mlflowDashboardStore.CreateMLflowAuditLog(c.Request.Context(), event); err != nil {
			h.writeError(c, 503, "MLFLOW_NATIVE_AUDIT_UNAVAILABLE", "could not record MLflow access")
			return
		}
	}
	defer func() {
		event.Status = c.Writer.Status()
		event.Duration = time.Since(startedAt)
		auditContext, cancel := context.WithTimeout(context.WithoutCancel(c.Request.Context()), mlflowNativeAuditTimeout)
		defer cancel()
		if err := h.mlflowDashboardStore.CreateMLflowAuditLog(auditContext, event); err != nil {
			log.Printf("could not record MLflow native request completion: %v", err)
		}
	}()
	proxy := &httputil.ReverseProxy{
		Transport: mlflowDashboardTransport,
		Rewrite: func(request *httputil.ProxyRequest) {
			request.SetURL(target)
			request.Out.URL.Path = strings.TrimSuffix(target.Path, "/") + c.Param("path")
			request.Out.URL.RawPath = ""
			request.Out.Host = target.Host
			request.Out.Header = mlflowNativeHeaders(request.In.Header)
		},
		ModifyResponse: func(response *http.Response) error {
			response.Header.Del("Set-Cookie")
			response.Header.Set("Cache-Control", "no-store")
			setMLflowDashboardSecurityHeaders(response.Header)
			return rewriteMLflowNativeLocation(response, target)
		},
		ErrorHandler: func(http.ResponseWriter, *http.Request, error) {
			h.writeError(c, 502, "MLFLOW_NATIVE_UPSTREAM_UNAVAILABLE", "MLflow is temporarily unavailable")
		},
	}
	// ReverseProxy copies bodies as streams. Never decode JSON or buffer model
	// artifacts here: native run IDs, errors, and artifact bytes are unchanged.
	proxy.ServeHTTP(c.Writer, c.Request)
}

func mlflowNativeHeaders(original http.Header) http.Header {
	clean := make(http.Header)
	for _, key := range []string{"Accept", "Accept-Encoding", "Content-Type", "Content-Encoding", "Content-Md5", "Range", "If-Range", "If-Match", "If-None-Match", "If-Modified-Since", "If-Unmodified-Since", "Cache-Control", "User-Agent", "X-Mlflow-Client-Version"} {
		if values := original.Values(key); len(values) > 0 {
			clean[key] = append([]string(nil), values...)
		}
	}
	return clean
}

func rewriteMLflowNativeLocation(response *http.Response, target *url.URL) error {
	raw := response.Header.Get("Location")
	if raw == "" {
		return nil
	}
	location, err := url.Parse(raw)
	if err != nil || location.User != nil {
		return fmt.Errorf("invalid MLflow redirect")
	}
	resolved := response.Request.URL.ResolveReference(location)
	if resolved.Scheme != target.Scheme || resolved.Host != target.Host {
		return fmt.Errorf("external MLflow redirect is forbidden")
	}
	base := strings.TrimSuffix(target.Path, "/")
	if !strings.HasPrefix(resolved.Path, base+"/") {
		return fmt.Errorf("MLflow redirect escapes upstream path")
	}
	path := strings.TrimPrefix(resolved.Path, base)
	if !validMLflowNativePath(path) || hasMLflowNativeCredentialQuery(resolved.Query()) {
		return fmt.Errorf("invalid MLflow redirect path")
	}
	response.Header.Set("Location", (&url.URL{Path: mlflowNativeBasePath+path, RawQuery: resolved.RawQuery}).String())
	return nil
}
