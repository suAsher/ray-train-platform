package api

import (
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"ray-train-platform-backend/domain"
	"ray-train-platform-backend/k8s"
)

func (h *Handler) RegisterWorkspaceRoutes(group *gin.RouterGroup) {
	group.GET("/dev-workspaces/me", h.getWorkspace)
	group.POST("/dev-workspaces", h.launchWorkspace)
	group.POST("/dev-workspaces/launch", h.launchWorkspace)
	group.DELETE("/dev-workspaces/me", h.stopWorkspace)
	group.POST("/dev-workspaces/:id/access", h.issueWorkspaceAccess)
}

type launchWorkspaceRequest struct {
	Name       string `json:"name"`
	Image      string `json:"image"`
	SnapshotID string `json:"snapshotId"`
}

func (h *Handler) launchWorkspace(c *gin.Context) {
	principal, ok := h.principal(c)
	if !ok {
		h.writeError(c, http.StatusUnauthorized, "AUTH_REQUIRED", "authentication is required")
		return
	}
	if !principal.Allowed("Engineer") {
		h.writeError(c, http.StatusForbidden, "FORBIDDEN", "engineer role is required")
		return
	}
	if h.workspaces == nil || h.kubernetes == nil {
		h.writeError(c, http.StatusServiceUnavailable, "WORKSPACE_UNAVAILABLE", "workspace Kubernetes integration is not configured")
		return
	}
	var request launchWorkspaceRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		h.writeError(c, http.StatusBadRequest, "INVALID_JSON", "request body is invalid")
		return
	}
	if existing, err := h.workspaces.GetWorkspace(c.Request.Context(), principal.TenantID, principal.Subject); err == nil && existing.State != domain.WorkspaceStopped {
		h.writeSuccess(c, http.StatusOK, existing)
		return
	}
	image := request.Image
	if image == "" {
		image = h.workspaceImage
	}
	if err := domain.ValidatePinnedImage(image); err != nil {
		h.writeError(c, http.StatusBadRequest, "WORKSPACE_IMAGE_REQUIRED", err.Error())
		return
	}
	name, err := h.newID()
	if err != nil {
		h.writeError(c, http.StatusInternalServerError, "ID_GENERATION_FAILED", "could not allocate workspace id")
		return
	}
	workspaceID := strings.TrimPrefix(name, "job-")
	workspaceName := "dev-" + sanitizeDNS(workspaceID)
	if len(workspaceName) > 63 {
		workspaceName = workspaceName[:63]
	}
	namespace := "tenant-" + sanitizeDNS(principal.TenantID)
	workspace := &domain.DevWorkspace{ID: "ws-" + workspaceID, TenantID: principal.TenantID, UserID: principal.Subject, Name: workspaceName, Namespace: namespace, RayClusterName: workspaceName, JupyterURL: "/api/v1/dev-workspaces/ws-" + workspaceID + "/proxy/", SnapshotID: request.SnapshotID, GPUCount: 1, State: domain.WorkspaceSubmitted}
	if err := h.repository.EnsureIdentity(c.Request.Context(), principal); err != nil {
		h.writeError(c, http.StatusInternalServerError, "IDENTITY_PERSIST_FAILED", "could not persist authenticated identity")
		return
	}
	if err := h.workspaces.CreateWorkspace(c.Request.Context(), workspace, 3600); err != nil {
		h.writeError(c, http.StatusConflict, "WORKSPACE_CREATE_FAILED", "could not persist workspace")
		return
	}
	manifest, err := k8s.RenderDevRayCluster(*workspace, k8s.WorkspaceRenderOptions{NodeSelector: h.trainingNodeSelector, Image: image, RayVersion: h.rayVersion, ServiceAccount: h.serviceAccount, ImagePullSecrets: h.imagePullSecrets, IDCExistingClaim: h.idcClaim, IDCMountPath: h.idcMountPath, JupyterBasePath: workspace.JupyterURL})
	if err != nil {
		_ = h.workspaces.UpdateWorkspaceState(c.Request.Context(), principal.TenantID, principal.Subject, domain.WorkspaceFailed)
		h.writeError(c, http.StatusBadRequest, "WORKSPACE_SPEC_INVALID", err.Error())
		return
	}
	if _, err := h.kubernetes.EnsureRayCluster(c.Request.Context(), manifest); err != nil {
		_ = h.workspaces.UpdateWorkspaceState(c.Request.Context(), principal.TenantID, principal.Subject, domain.WorkspaceFailed)
		h.writeError(c, http.StatusBadGateway, "WORKSPACE_CLUSTER_CREATE_FAILED", "could not create debug RayCluster")
		return
	}
	if err := h.kubernetes.EnsureWorkspaceService(c.Request.Context(), workspace.Namespace, workspace.RayClusterName, workspace.ID); err != nil {
		_ = h.kubernetes.DeleteRayCluster(c.Request.Context(), workspace.Namespace, workspace.RayClusterName, workspace.ID)
		_ = h.workspaces.UpdateWorkspaceState(c.Request.Context(), principal.TenantID, principal.Subject, domain.WorkspaceFailed)
		h.writeError(c, http.StatusBadGateway, "WORKSPACE_SERVICE_CREATE_FAILED", "could not create debug service")
		return
	}
	h.writeSuccess(c, http.StatusAccepted, workspace)
}

func (h *Handler) getWorkspace(c *gin.Context) {
	principal, ok := h.principal(c)
	if !ok {
		h.writeError(c, http.StatusUnauthorized, "AUTH_REQUIRED", "authentication is required")
		return
	}
	if h.workspaces == nil {
		h.writeError(c, http.StatusServiceUnavailable, "WORKSPACE_UNAVAILABLE", "workspace storage is not configured")
		return
	}
	workspace, err := h.workspaces.GetWorkspace(c.Request.Context(), principal.TenantID, principal.Subject)
	if err != nil {
		h.writeError(c, http.StatusNotFound, "WORKSPACE_NOT_FOUND", "no debug workspace exists")
		return
	}
	if h.kubernetes != nil && workspace.State != domain.WorkspaceStopped {
		if resource, queryErr := h.kubernetes.GetRayCluster(c.Request.Context(), workspace.Namespace, workspace.RayClusterName); queryErr == nil {
			state := domain.WorkspaceState(k8s.MapRayClusterState(resource))
			if state != workspace.State {
				_ = h.workspaces.UpdateWorkspaceState(c.Request.Context(), principal.TenantID, principal.Subject, state)
				workspace.State = state
			}
		}
	}
	h.writeSuccess(c, http.StatusOK, workspace)
}

func (h *Handler) stopWorkspace(c *gin.Context) {
	principal, ok := h.principal(c)
	if !ok {
		h.writeError(c, http.StatusUnauthorized, "AUTH_REQUIRED", "authentication is required")
		return
	}
	if h.workspaces == nil || h.kubernetes == nil {
		h.writeError(c, http.StatusServiceUnavailable, "WORKSPACE_UNAVAILABLE", "workspace Kubernetes integration is not configured")
		return
	}
	workspace, err := h.workspaces.GetWorkspace(c.Request.Context(), principal.TenantID, principal.Subject)
	if err != nil {
		h.writeError(c, http.StatusNotFound, "WORKSPACE_NOT_FOUND", "no debug workspace exists")
		return
	}
	if err := h.kubernetes.DeleteRayCluster(c.Request.Context(), workspace.Namespace, workspace.RayClusterName, workspace.ID); err != nil {
		h.writeError(c, http.StatusBadGateway, "WORKSPACE_STOP_FAILED", "could not stop debug RayCluster")
		return
	}
	if err := h.workspaces.UpdateWorkspaceState(c.Request.Context(), principal.TenantID, principal.Subject, domain.WorkspaceStopped); err != nil {
		h.writeError(c, http.StatusInternalServerError, "WORKSPACE_STATE_FAILED", "could not persist workspace state")
		return
	}
	h.writeSuccess(c, http.StatusAccepted, map[string]string{"state": string(domain.WorkspaceStopped)})
}

// RegisterWorkspaceProxyRoute mounts the JupyterLab reverse proxy. It is
// deliberately kept outside the interactive-session group: a browser tab
// cannot send an Authorization header, so the handler authorises with its own
// short-lived signed token instead and would never be reached if the generic
// session guard rejected the request first.
func (h *Handler) RegisterWorkspaceProxyRoute(group *gin.RouterGroup) {
	group.Any("/dev-workspaces/:id/proxy/*path", h.proxyWorkspace)
}

const workspaceAccessCookie = "ray_workspace_access"

// upstreamFor resolves the workspace's Jupyter service. It is a field so tests
// can point the proxy at a local server instead of cluster DNS.
func (h *Handler) upstreamFor(workspace *domain.DevWorkspace) string {
	if h.workspaceUpstream != nil {
		return h.workspaceUpstream(workspace)
	}
	return fmt.Sprintf("http://%s-head-svc.%s.svc.cluster.local:8888", workspace.RayClusterName, workspace.Namespace)
}

// issueWorkspaceAccess mints the short-lived token the Portal puts in the
// JupyterLab URL. A browser tab opened with target="_blank" cannot send an
// Authorization header, so the bearer token alone can never reach the proxy.
func (h *Handler) issueWorkspaceAccess(c *gin.Context) {
	principal, ok := h.principal(c)
	if !ok {
		h.writeError(c, http.StatusUnauthorized, "AUTH_REQUIRED", "authentication is required")
		return
	}
	if h.workspaces == nil {
		h.writeError(c, http.StatusServiceUnavailable, "WORKSPACE_UNAVAILABLE", "workspace storage is not configured")
		return
	}
	workspace, err := h.workspaces.GetWorkspace(c.Request.Context(), principal.TenantID, principal.Subject)
	if err != nil || workspace.ID != c.Param("id") {
		h.writeError(c, http.StatusNotFound, "WORKSPACE_NOT_FOUND", "debug workspace was not found")
		return
	}
	if len(h.workspacePepper) == 0 {
		h.writeError(c, http.StatusServiceUnavailable, "WORKSPACE_ACCESS_UNAVAILABLE", "workspace access signing is not configured")
		return
	}
	token, err := domain.IssueWorkspaceAccessToken(workspace.ID, principal.Subject, h.workspacePepper, time.Now(), domain.WorkspaceAccessTokenTTL)
	if err != nil {
		h.writeError(c, http.StatusInternalServerError, "WORKSPACE_ACCESS_FAILED", "could not issue workspace access token")
		return
	}
	c.Header("Cache-Control", "no-store")
	h.writeSuccess(c, http.StatusOK, map[string]string{
		"url": strings.TrimSuffix(workspace.JupyterURL, "/") + "/?access_token=" + url.QueryEscape(token),
	})
}

// workspacePrincipal authorises a proxy request. Browser navigation carries no
// Authorization header, so besides the normal bearer token it accepts a
// short-lived signed token — first from the query string, then from the cookie
// that the first request installs for all of JupyterLab's sub-resources.
func (h *Handler) workspacePrincipal(c *gin.Context) (string, bool) {
	if principal, ok := h.principal(c); ok {
		return principal.Subject, true
	}
	workspaceID := c.Param("id")
	if len(h.workspacePepper) == 0 || workspaceID == "" {
		return "", false
	}
	token := c.Query("access_token")
	fromQuery := token != ""
	if token == "" {
		if cookie, err := c.Request.Cookie(workspaceAccessCookie); err == nil {
			token = cookie.Value
		}
	}
	if token == "" {
		return "", false
	}
	subject := c.Query("subject")
	if subject == "" {
		if cookie, err := c.Request.Cookie(workspaceAccessCookie + "_subject"); err == nil {
			subject = cookie.Value
		}
	}
	if subject == "" || domain.VerifyWorkspaceAccessToken(token, workspaceID, subject, h.workspacePepper, time.Now()) != nil {
		return "", false
	}
	if fromQuery {
		// Scope the cookie to this workspace's proxy path so it cannot be
		// replayed against another workspace or the rest of the API.
		basePath := "/api/v1/dev-workspaces/" + workspaceID + "/proxy/"
		http.SetCookie(c.Writer, &http.Cookie{
			Name: workspaceAccessCookie, Value: token, Path: basePath,
			HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: int(domain.WorkspaceAccessTokenTTL.Seconds()),
		})
		http.SetCookie(c.Writer, &http.Cookie{
			Name: workspaceAccessCookie + "_subject", Value: subject, Path: basePath,
			HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: int(domain.WorkspaceAccessTokenTTL.Seconds()),
		})
	}
	return subject, true
}

func (h *Handler) proxyWorkspace(c *gin.Context) {
	subject, ok := h.workspacePrincipal(c)
	if !ok {
		h.writeError(c, http.StatusUnauthorized, "AUTH_REQUIRED", "authentication is required")
		return
	}
	if h.workspaces == nil {
		h.writeError(c, http.StatusServiceUnavailable, "WORKSPACE_UNAVAILABLE", "workspace storage is not configured")
		return
	}
	workspace, err := h.workspaces.GetWorkspaceByUser(c.Request.Context(), subject)
	if err != nil || workspace.ID != c.Param("id") {
		h.writeError(c, http.StatusNotFound, "WORKSPACE_NOT_FOUND", "debug workspace was not found")
		return
	}
	target, err := url.Parse(h.upstreamFor(workspace))
	if err != nil {
		h.writeError(c, http.StatusBadGateway, "WORKSPACE_PROXY_FAILED", "invalid workspace service address")
		return
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.ErrorHandler = func(writer http.ResponseWriter, _ *http.Request, _ error) {
		writer.WriteHeader(http.StatusBadGateway)
	}
	// JupyterLab is started with --ServerApp.base_url set to this proxy path,
	// so the prefix is forwarded rather than stripped. Rewriting it to "/"
	// makes Jupyter answer 404 and emit links that break outside the proxy.
	// The access credentials are removed so they never reach the upstream.
	query := c.Request.URL.Query()
	query.Del("access_token")
	query.Del("subject")
	c.Request.URL.RawQuery = query.Encode()
	proxy.ServeHTTP(c.Writer, c.Request)
}
