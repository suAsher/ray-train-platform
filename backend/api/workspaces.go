package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
	"ray-train-platform-backend/k8s"
	"ray-train-platform-backend/repositories"
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
	GPUCount   int    `json:"gpuCount"`
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
	if request.GPUCount == 0 {
		request.GPUCount = 1
	}
	if request.GPUCount != 1 && request.GPUCount != 2 && request.GPUCount != 4 && request.GPUCount != 8 {
		h.writeError(c, http.StatusBadRequest, "WORKSPACE_GPU_COUNT_INVALID", "debug workspace GPU count must be one of 1, 2, 4, or 8")
		return
	}
	if existing, err := h.workspaces.GetWorkspace(c.Request.Context(), principal.TenantID, principal.Subject); err == nil && existing.State != domain.WorkspaceStopped {
		h.writeSuccess(c, http.StatusOK, existing)
		return
	}
	if h.dataSpacesEnabled || h.idcDataSpacesEnabled {
		if err := h.ensureDataSpacesForPrincipal(c.Request.Context(), principal); err != nil {
			h.writeError(c, http.StatusServiceUnavailable, "DATA_SPACE_INITIALIZATION_FAILED", "could not initialize the selected data spaces; inspect platform storage readiness and try again")
			return
		}
	}
	image, ok := h.resolveWorkspaceImage(c, principal.TenantID, domain.NormalizeImageReference(request.Image))
	if !ok {
		return
	}
	if err := domain.ValidatePinnedImage(image); err != nil {
		h.writeError(c, http.StatusBadRequest, "WORKSPACE_IMAGE_REQUIRED", err.Error())
		return
	}
	dataMounts, err := h.resolveWorkspaceDataMountPlan(c.Request.Context(), principal)
	if err != nil {
		switch {
		case errors.Is(err, ErrSubmissionDataSpacesUnavailable):
			h.writeError(c, http.StatusServiceUnavailable, "DATA_SPACES_UNAVAILABLE", "data spaces are not configured")
		default:
			h.writeError(c, http.StatusConflict, "DATA_SPACE_MOUNT_NOT_READY", "your personal data space is still being prepared; try again after storage setup is complete")
		}
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
	workspace := &domain.DevWorkspace{ID: "ws-" + workspaceID, TenantID: principal.TenantID, UserID: principal.Subject, Name: workspaceName, Namespace: namespace, RayClusterName: workspaceName, JupyterURL: "/api/v1/dev-workspaces/ws-" + workspaceID + "/proxy/", SnapshotID: request.SnapshotID, GPUCount: request.GPUCount, State: domain.WorkspaceSubmitted}
	if err := h.workspaces.CreateWorkspace(c.Request.Context(), workspace, 3600); err != nil {
		var quotaErr *repositories.GPUQuotaExceededError
		if errors.As(err, &quotaErr) {
			h.writeError(c, http.StatusConflict, "GPU_QUOTA_EXCEEDED", "the tenant GPU quota does not have enough capacity for this debug environment")
			return
		}
		h.writeError(c, http.StatusConflict, "WORKSPACE_CREATE_FAILED", "could not persist workspace")
		return
	}
	if err := h.ensureTenantNamespaceAndPullSecrets(c.Request.Context(), principal.TenantID, namespace); err != nil {
		_ = h.workspaces.UpdateWorkspaceState(c.Request.Context(), principal.TenantID, principal.Subject, domain.WorkspaceFailed)
		h.writeError(c, http.StatusBadGateway, "WORKSPACE_RUNTIME_PREPARE_FAILED", "could not prepare the tenant workspace runtime")
		return
	}
	manifest, err := k8s.RenderDevRayCluster(*workspace, k8s.WorkspaceRenderOptions{NodeSelector: h.trainingNodeSelector, Image: image, RayVersion: h.rayVersion, ServiceAccount: h.serviceAccount, ImagePullSecrets: h.imagePullSecrets, IDCExistingClaim: h.idcClaim, IDCMountPath: h.idcMountPath, JupyterBasePath: workspace.JupyterURL, DataMounts: dataMounts})
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

func (h *Handler) resolveWorkspaceDataMountPlan(ctx context.Context, principal auth.Principal) (k8s.DataMountPlan, error) {
	if !h.dataSpacesEnabled && !h.idcDataSpacesEnabled {
		return k8s.DataMountPlan{}, nil
	}
	if h.dataSpaces == nil {
		return k8s.DataMountPlan{}, ErrSubmissionDataSpacesUnavailable
	}
	bindings, err := h.dataSpaces.ListDataBindings(ctx, principal.TenantID, principal.Subject)
	if err != nil {
		return k8s.DataMountPlan{}, ErrSubmissionDataSpacesUnavailable
	}
	ready := make(map[domain.DataSpaceID]domain.DataMountBinding, len(bindings))
	for _, binding := range bindings {
		if binding.Status == domain.DataMountBindingReady && bindingVisibleToPrincipal(binding, principal) && dataSpaceMountingEnabled(binding.SpaceID, h.dataSpacesEnabled, h.idcDataSpacesEnabled) {
			ready[binding.SpaceID] = binding
		}
	}
	tenantRoot, hasTenantRoot := ready[domain.DataSpaceTenantStorageRoot]
	root := func(space domain.DataSpaceID, readOnly bool) *k8s.DataMountRoot {
		binding, ok := ready[space]
		if !ok || strings.TrimSpace(binding.ClaimName) == "" {
			return nil
		}
		if isTOSWorkloadSpace(space) {
			if !hasTenantRoot || strings.TrimSpace(tenantRoot.ClaimName) == "" {
				return nil
			}
			logicalRoot := binding.RootPrefix
			if space == domain.DataSpacePublic {
				var err error
				logicalRoot, err = h.publicDataRootForTenant(principal.TenantID)
				if err != nil {
					return nil
				}
			}
			subPath, err := dataRootSubPath(tenantRoot.RootPrefix, logicalRoot)
			if err != nil {
				return nil
			}
			return &k8s.DataMountRoot{ClaimName: tenantRoot.ClaimName, SubPath: subPath, ReadOnly: readOnly}
		}
		return &k8s.DataMountRoot{ClaimName: binding.ClaimName, ReadOnly: readOnly}
	}
	plan := k8s.DataMountPlan{
		Personal: root(domain.DataSpaceWorkspace, false), Team: root(domain.DataSpaceTeamShared, true), Public: root(domain.DataSpacePublic, true),
		IDCOriginal: root(domain.DataSpaceIDCOriginal, true), IDCWellspiking: root(domain.DataSpaceIDCWellspiking, true), IDCShared: root(domain.DataSpaceIDCShared, true),
	}
	if plan.Validate() != nil {
		return k8s.DataMountPlan{}, ErrSubmissionDataMountNotReady
	}
	if h.dataSpacesEnabled && plan.Personal == nil {
		return k8s.DataMountPlan{}, ErrSubmissionDataMountNotReady
	}
	if h.idcDataSpacesEnabled && (plan.IDCOriginal == nil || plan.IDCWellspiking == nil || plan.IDCShared == nil) {
		return k8s.DataMountPlan{}, ErrSubmissionDataMountNotReady
	}
	return plan, nil
}

func isTOSWorkloadSpace(space domain.DataSpaceID) bool {
	switch space {
	case domain.DataSpaceWorkspace, domain.DataSpaceTeamShared, domain.DataSpacePublic:
		return true
	default:
		return false
	}
}

// dataRootSubPath returns the canonical relative directory below the internal
// tenant TOS root. Both values originate in platform-owned bindings; still,
// the check keeps an unexpected stored root from widening a Pod mount.
func dataRootSubPath(tenantRoot, logicalRoot string) (string, error) {
	base := strings.TrimSuffix(strings.TrimSpace(tenantRoot), "/")
	child := strings.TrimSuffix(strings.TrimSpace(logicalRoot), "/")
	if base == "" || child == "" || !strings.HasPrefix(child, base+"/") {
		return "", fmt.Errorf("logical data root is outside the tenant storage root")
	}
	return domain.NormalizeStorageRelativePath(strings.TrimPrefix(child, base+"/"))
}

// resolveWorkspaceImage takes the image the user picked from the catalogue,
// falling back to the catalogue default and finally to the deployment-wide
// image so an environment without a catalogue still works.
func (h *Handler) resolveWorkspaceImage(c *gin.Context, tenantID, requested string) (string, bool) {
	if h.images != nil {
		if requested != "" {
			image, err := h.images.ImageByReference(c.Request.Context(), tenantID, domain.ImageKindWorkspace, requested)
			if err != nil {
				h.writeError(c, http.StatusBadRequest, "IMAGE_NOT_ALLOWED", "the requested workspace image is not in the catalog")
				return "", false
			}
			return image.Reference, true
		}
		if image, err := h.images.DefaultImage(c.Request.Context(), tenantID, domain.ImageKindWorkspace); err == nil {
			return image.Reference, true
		}
	}
	if requested != "" {
		return requested, true
	}
	return h.workspaceImage, true
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
	// The editor Service is created alongside the cluster and is not garbage
	// collected with it, so it has to be removed here or every stop leaks one.
	if err := h.kubernetes.DeleteWorkspaceService(c.Request.Context(), workspace.Namespace, workspace.RayClusterName, workspace.ID); err != nil {
		h.writeError(c, http.StatusBadGateway, "WORKSPACE_STOP_FAILED", "could not remove the debug workspace service")
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
	group.Any("/dev-workspaces/:id/vscode/*path", h.proxyWorkspaceVSCode)
}

// proxyWorkspaceVSCode forwards to code-server. It is a separate upstream port
// rather than a path on JupyterLab, so the editors stay independent.
func (h *Handler) proxyWorkspaceVSCode(c *gin.Context) {
	// code-server serves from the root, so the proxy prefix is stripped. Its
	// static assets and WebSocket endpoint can share the workspace-scoped
	// proxy; Nginx preserves the original Host for code-server's origin check.
	h.proxyWorkspacePort(c, 8443, true)
}

const workspaceAccessCookie = "ray_workspace_access"

// upstreamFor resolves the workspace's Jupyter service. It is a field so tests
// can point the proxy at a local server instead of cluster DNS.
func (h *Handler) upstreamForPort(workspace *domain.DevWorkspace, port int) string {
	if h.workspaceUpstream != nil {
		return h.workspaceUpstream(workspace)
	}
	return fmt.Sprintf("http://%s-dev-svc.%s.svc.cluster.local:%d", workspace.RayClusterName, workspace.Namespace, port)
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
	base := strings.TrimSuffix(workspace.JupyterURL, "/")
	vscodeBase := strings.TrimSuffix(base, "/proxy") + "/vscode"
	accessQuery := "?access_token=" + url.QueryEscape(token) + "&subject=" + url.QueryEscape(principal.Subject)
	h.writeSuccess(c, http.StatusOK, map[string]string{
		"url":       base + "/" + accessQuery,
		"vscodeUrl": vscodeBase + "/" + accessQuery,
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
		// Scope the cookie to this workspace so both editors (/proxy and
		// /vscode) can use it, while it stays unusable against another
		// workspace or the rest of the API.
		basePath := "/api/v1/dev-workspaces/" + workspaceID + "/"
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
	h.proxyWorkspacePort(c, 8888, false)
}

// proxyWorkspacePort is shared by both editors. stripPrefix is true for
// code-server, which serves from the root, and false for JupyterLab, which is
// configured with the proxy path as its base_url.
func (h *Handler) proxyWorkspacePort(c *gin.Context, port int, stripPrefix bool) {
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
	target, err := url.Parse(h.upstreamForPort(workspace, port))
	if err != nil {
		h.writeError(c, http.StatusBadGateway, "WORKSPACE_PROXY_FAILED", "invalid workspace service address")
		return
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	// code-server derives its base path and websocket origin from these.
	director := proxy.Director
	proxy.Director = func(request *http.Request) {
		director(request)
		request.Header.Set("X-Forwarded-Proto", "http")
		if request.Header.Get("X-Forwarded-Host") == "" {
			request.Header.Set("X-Forwarded-Host", c.Request.Host)
		}
	}
	proxy.ErrorHandler = func(writer http.ResponseWriter, _ *http.Request, _ error) {
		writer.WriteHeader(http.StatusBadGateway)
	}
	// JupyterLab is started with --ServerApp.base_url set to this proxy path,
	// so its prefix is forwarded rather than stripped. code-server serves from
	// the root and needs the prefix removed instead.
	if stripPrefix {
		path := c.Param("path")
		if path == "" {
			path = "/"
		}
		c.Request.URL.Path = path
	}
	// The access credentials are removed so they never reach the upstream.
	query := c.Request.URL.Query()
	query.Del("access_token")
	query.Del("subject")
	c.Request.URL.RawQuery = query.Encode()
	proxy.ServeHTTP(c.Writer, c.Request)
}
