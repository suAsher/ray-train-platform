package api

import (
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"
	"ray-train-platform-backend/domain"
	"ray-train-platform-backend/k8s"
)

func (h *Handler) RegisterWorkspaceRoutes(group *gin.RouterGroup) {
	group.GET("/dev-workspaces/me", h.getWorkspace)
	group.POST("/dev-workspaces", h.launchWorkspace)
	group.POST("/dev-workspaces/launch", h.launchWorkspace)
	group.DELETE("/dev-workspaces/me", h.stopWorkspace)
	group.Any("/dev-workspaces/:id/proxy/*path", h.proxyWorkspace)
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
	manifest, err := k8s.RenderDevRayCluster(*workspace, k8s.WorkspaceRenderOptions{Image: image, RayVersion: h.rayVersion, ServiceAccount: h.serviceAccount, ImagePullSecrets: h.imagePullSecrets, IDCExistingClaim: h.idcClaim, IDCMountPath: h.idcMountPath, JupyterBasePath: workspace.JupyterURL})
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

func (h *Handler) proxyWorkspace(c *gin.Context) {
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
	target, err := url.Parse(fmt.Sprintf("http://%s-head-svc.%s.svc.cluster.local:8888", workspace.RayClusterName, workspace.Namespace))
	if err != nil {
		h.writeError(c, http.StatusBadGateway, "WORKSPACE_PROXY_FAILED", "invalid workspace service address")
		return
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.ErrorHandler = func(writer http.ResponseWriter, _ *http.Request, _ error) {
		writer.WriteHeader(http.StatusBadGateway)
	}
	originalPath := c.Param("path")
	if originalPath == "" {
		originalPath = "/"
	}
	c.Request.URL.Path = originalPath
	proxy.ServeHTTP(c.Writer, c.Request)
}
