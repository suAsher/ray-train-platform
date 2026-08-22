package api

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"ray-train-platform-backend/domain"
)

const (
	jobDashboardSessionCookie = "ray_job_dashboard"
	jobDashboardTenantCookie  = "ray_job_dashboard_tenant"
	jobDashboardSubjectCookie = "ray_job_dashboard_subject"
)

// RegisterJobDashboardProxyRoute mounts the browser navigation route outside
// the bearer-token middleware. The handler performs its own job-scoped token
// verification, so port 8265 remains a ClusterIP-only internal endpoint.
func (h *Handler) RegisterJobDashboardProxyRoute(group *gin.RouterGroup) {
	group.Any("/jobs/:id/dashboard/*path", h.proxyJobDashboard)
}

func (h *Handler) issueJobDashboardAccess(c *gin.Context) {
	principal, ok := h.principal(c)
	if !ok {
		h.writeError(c, http.StatusUnauthorized, "AUTH_REQUIRED", "authentication is required")
		return
	}
	job, err := h.repository.Get(c.Request.Context(), principal.TenantID, c.Param("id"))
	if err != nil {
		h.writeError(c, http.StatusNotFound, "JOB_NOT_FOUND", "training job was not found")
		return
	}
	if job.UserID != principal.Subject {
		h.writeError(c, http.StatusForbidden, "DASHBOARD_FORBIDDEN", "Ray Dashboard is available only to the job owner")
		return
	}
	if strings.TrimSpace(job.RayClusterName) == "" {
		h.writeError(c, http.StatusConflict, "DASHBOARD_NOT_READY", "RayCluster has not been created yet")
		return
	}
	if _, err := h.jobDashboardUpstream(c.Request.Context(), job); err != nil {
		h.writeError(c, http.StatusConflict, "DASHBOARD_UNAVAILABLE", "Ray Dashboard is available only while the RayCluster head service is running")
		return
	}
	if len(h.workspacePepper) == 0 {
		h.writeError(c, http.StatusServiceUnavailable, "DASHBOARD_ACCESS_UNAVAILABLE", "Dashboard access signing is not configured")
		return
	}
	token, err := domain.IssueJobDashboardAccessToken(job.TenantID, job.ID, principal.Subject, h.workspacePepper, time.Now(), domain.JobDashboardAccessTokenTTL)
	if err != nil {
		h.writeError(c, http.StatusInternalServerError, "DASHBOARD_ACCESS_FAILED", "could not issue Ray Dashboard access token")
		return
	}
	base := jobDashboardBasePath(job.ID)
	query := url.Values{"access_token": {token}, "tenant": {job.TenantID}, "subject": {principal.Subject}}
	c.Header("Cache-Control", "no-store")
	h.writeSuccess(c, http.StatusOK, map[string]string{"url": base + "?" + query.Encode()})
}

func (h *Handler) proxyJobDashboard(c *gin.Context) {
	if c.Query("access_token") != "" {
		h.exchangeJobDashboardAccess(c)
		return
	}
	tenantID, subject, ok := h.jobDashboardSession(c)
	if !ok {
		h.writeError(c, http.StatusUnauthorized, "DASHBOARD_AUTH_REQUIRED", "open Ray Dashboard from the authenticated training job page")
		return
	}
	job, err := h.repository.Get(c.Request.Context(), tenantID, c.Param("id"))
	if err != nil || job.UserID != subject {
		h.writeError(c, http.StatusForbidden, "DASHBOARD_FORBIDDEN", "Ray Dashboard access is not allowed for this job")
		return
	}
	// Ray Dashboard exposes mutation APIs in addition to diagnostics. The
	// platform grants users a read-only diagnostic view so they cannot submit
	// ungoverned work or kill actors outside the RayJob lifecycle.
	if c.Request.Method != http.MethodGet && c.Request.Method != http.MethodHead && c.Request.Method != http.MethodOptions {
		c.Header("Allow", "GET, HEAD, OPTIONS")
		h.writeError(c, http.StatusMethodNotAllowed, "DASHBOARD_READ_ONLY", "Ray Dashboard is read-only on this platform")
		return
	}
	upstream, err := h.jobDashboardUpstream(c.Request.Context(), job)
	if err != nil {
		h.writeError(c, http.StatusGone, "DASHBOARD_UNAVAILABLE", "the RayCluster has stopped; use the platform logs and metrics for historical diagnosis")
		return
	}
	target, err := url.Parse(upstream)
	if err != nil {
		h.writeError(c, http.StatusBadGateway, "DASHBOARD_UPSTREAM_INVALID", "Ray Dashboard upstream is invalid")
		return
	}
	basePath := jobDashboardBasePath(job.ID)
	proxy := httputil.NewSingleHostReverseProxy(target)
	originalDirector := proxy.Director
	proxy.Director = func(request *http.Request) {
		originalDirector(request)
		path := c.Param("path")
		if path == "" {
			path = "/"
		}
		request.URL.Path = path
		request.URL.RawPath = ""
		request.Host = target.Host
		request.Header.Del("Accept-Encoding")
	}
	proxy.ModifyResponse = func(response *http.Response) error {
		response.Header.Set("X-Frame-Options", "DENY")
		response.Header.Set("Referrer-Policy", "no-referrer")
		return rewriteRayDashboardResponse(response, basePath)
	}
	proxy.ErrorHandler = func(_ http.ResponseWriter, _ *http.Request, proxyErr error) {
		_ = c.Error(proxyErr)
		h.writeError(c, http.StatusBadGateway, "DASHBOARD_PROXY_FAILED", "Ray Dashboard is temporarily unavailable")
	}
	proxy.ServeHTTP(c.Writer, c.Request)
}

func (h *Handler) exchangeJobDashboardAccess(c *gin.Context) {
	tenantID := strings.TrimSpace(c.Query("tenant"))
	subject := strings.TrimSpace(c.Query("subject"))
	jobID := c.Param("id")
	token := c.Query("access_token")
	if len(h.workspacePepper) == 0 || domain.VerifyJobDashboardAccessToken(token, tenantID, jobID, subject, h.workspacePepper, time.Now()) != nil {
		h.writeError(c, http.StatusUnauthorized, "DASHBOARD_TOKEN_INVALID", "Ray Dashboard access link is invalid or expired")
		return
	}
	job, err := h.repository.Get(c.Request.Context(), tenantID, jobID)
	if err != nil || job.UserID != subject {
		h.writeError(c, http.StatusForbidden, "DASHBOARD_FORBIDDEN", "Ray Dashboard access is not allowed for this job")
		return
	}
	session, err := domain.IssueJobDashboardAccessToken(tenantID, jobID, subject, h.workspacePepper, time.Now(), domain.JobDashboardSessionTTL)
	if err != nil {
		h.writeError(c, http.StatusInternalServerError, "DASHBOARD_SESSION_FAILED", "could not create Ray Dashboard session")
		return
	}
	path := jobDashboardBasePath(jobID)
	secure := c.Request.TLS != nil || strings.EqualFold(c.GetHeader("X-Forwarded-Proto"), "https")
	for name, value := range map[string]string{
		jobDashboardSessionCookie: session,
		jobDashboardTenantCookie:  tenantID,
		jobDashboardSubjectCookie: subject,
	} {
		http.SetCookie(c.Writer, &http.Cookie{
			Name: name, Value: value, Path: path, MaxAge: int(domain.JobDashboardSessionTTL.Seconds()),
			HttpOnly: true, Secure: secure, SameSite: http.SameSiteStrictMode,
		})
	}
	cleanQuery := c.Request.URL.Query()
	cleanQuery.Del("access_token")
	cleanQuery.Del("tenant")
	cleanQuery.Del("subject")
	location := c.Request.URL.Path
	if encoded := cleanQuery.Encode(); encoded != "" {
		location += "?" + encoded
	}
	c.Redirect(http.StatusFound, location)
}

func (h *Handler) jobDashboardSession(c *gin.Context) (string, string, bool) {
	session, sessionErr := c.Request.Cookie(jobDashboardSessionCookie)
	tenant, tenantErr := c.Request.Cookie(jobDashboardTenantCookie)
	subject, subjectErr := c.Request.Cookie(jobDashboardSubjectCookie)
	if sessionErr != nil || tenantErr != nil || subjectErr != nil {
		return "", "", false
	}
	if domain.VerifyJobDashboardAccessToken(session.Value, tenant.Value, c.Param("id"), subject.Value, h.workspacePepper, time.Now()) != nil {
		return "", "", false
	}
	return tenant.Value, subject.Value, true
}

func (h *Handler) jobDashboardUpstream(ctx context.Context, job *domain.TrainingJob) (string, error) {
	if h.dashboardUpstream != nil {
		return h.dashboardUpstream(ctx, job)
	}
	if h.kubernetes == nil {
		return "", fmt.Errorf("Kubernetes runtime is not configured")
	}
	namespace := strings.TrimSpace(job.KubernetesNS)
	if namespace == "" {
		namespace = "tenant-" + sanitizeDNS(job.TenantID)
	}
	return h.kubernetes.ResolveRayDashboardService(ctx, namespace, job.RayClusterName)
}

func jobDashboardBasePath(jobID string) string {
	return "/api/v1/jobs/" + url.PathEscape(jobID) + "/dashboard/"
}

func rewriteRayDashboardResponse(response *http.Response, basePath string) error {
	contentType := strings.ToLower(response.Header.Get("Content-Type"))
	if !strings.Contains(contentType, "javascript") || response.Header.Get("Content-Encoding") != "" {
		return nil
	}
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return err
	}
	_ = response.Body.Close()
	replacements := []struct{ old, new []byte }{
		{[]byte(`"/api/`), []byte(`"` + basePath + `api/`)},
		{[]byte(`'/api/`), []byte(`'` + basePath + `api/`)},
		{[]byte("`/api/"), []byte("`" + basePath + "api/")},
	}
	for _, replacement := range replacements {
		body = bytes.ReplaceAll(body, replacement.old, replacement.new)
	}
	response.Body = io.NopCloser(bytes.NewReader(body))
	response.ContentLength = int64(len(body))
	response.Header.Set("Content-Length", strconv.Itoa(len(body)))
	return nil
}
