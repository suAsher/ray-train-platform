package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
	"ray-train-platform-backend/objectstore"
)

// WorkspaceSnapshotRepository is owner-scoped by contract. A repository must
// never offer an unscoped lookup to the request layer.
type WorkspaceSnapshotRepository interface {
	CreateWorkspaceSnapshot(context.Context, domain.WorkspaceSnapshot) error
	ListWorkspaceSnapshots(context.Context, string, string, int) ([]domain.WorkspaceSnapshot, error)
	GetWorkspaceSnapshot(context.Context, string, string, string) (*domain.WorkspaceSnapshot, error)
}

func (h *Handler) RegisterWorkspaceSnapshotRoutes(group *gin.RouterGroup) {
	group.GET("/workspace-snapshots", h.listWorkspaceSnapshots)
	group.POST("/workspace-snapshots", h.createWorkspaceSnapshot)
}

type createWorkspaceSnapshotRequest struct {
	SourcePath string `json:"sourcePath"`
}

func (h *Handler) listWorkspaceSnapshots(c *gin.Context) {
	principal, ok := h.workspaceSnapshotPrincipal(c)
	if !ok {
		return
	}
	if h.workspaceSnapshots == nil {
		h.writeError(c, http.StatusServiceUnavailable, "WORKSPACE_SNAPSHOTS_UNAVAILABLE", "workspace versions are not configured")
		return
	}
	limit, err := strconv.Atoi(c.DefaultQuery("limit", "50"))
	if err != nil || limit < 1 || limit > 100 {
		h.writeError(c, http.StatusBadRequest, "INVALID_WORKSPACE_SNAPSHOT_LIMIT", "workspace version page limit is invalid")
		return
	}
	snapshots, err := h.workspaceSnapshots.ListWorkspaceSnapshots(c.Request.Context(), principal.TenantID, principal.Subject, limit)
	if err != nil {
		h.writeError(c, http.StatusInternalServerError, "WORKSPACE_SNAPSHOTS_LIST_FAILED", "could not list workspace versions")
		return
	}
	h.writeSuccess(c, http.StatusOK, snapshots)
}

func (h *Handler) createWorkspaceSnapshot(c *gin.Context) {
	principal, ok := h.workspaceSnapshotPrincipal(c)
	if !ok {
		return
	}
	if h.workspaceSnapshots == nil || h.workspaceSnapshotStore == nil {
		h.writeError(c, http.StatusServiceUnavailable, "WORKSPACE_SNAPSHOTS_UNAVAILABLE", "workspace versions are not configured")
		return
	}
	var request createWorkspaceSnapshotRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		h.writeError(c, http.StatusBadRequest, "INVALID_JSON", "request body is invalid")
		return
	}
	sourcePath, err := domain.NormalizeStorageRelativePath(request.SourcePath)
	if err != nil {
		h.writeError(c, http.StatusBadRequest, "INVALID_WORKSPACE_SOURCE_PATH", "workspace path must stay inside your workspace")
		return
	}
	if err := h.ensureWorkspaceObjectSpace(c.Request.Context(), principal); err != nil {
		h.writeError(c, http.StatusServiceUnavailable, "WORKSPACE_STORAGE_UNAVAILABLE", "could not prepare your workspace storage")
		return
	}
	spaces, err := h.personalDataSpacesForPrincipal(c.Request.Context(), principal)
	if err != nil {
		h.writeError(c, http.StatusForbidden, "DATA_SPACE_IDENTITY_INVALID", "the authenticated identity cannot access workspace storage")
		return
	}
	workspace, found := domain.FindDataSpace(spaces, domain.DataSpaceWorkspace)
	if !found {
		h.writeError(c, http.StatusServiceUnavailable, "WORKSPACE_SNAPSHOTS_UNAVAILABLE", "workspace storage is not configured")
		return
	}
	id, err := h.newWorkspaceSnapshotID()
	if err != nil {
		h.writeError(c, http.StatusInternalServerError, "ID_GENERATION_FAILED", "could not allocate workspace version")
		return
	}
	root, err := h.personalDataRootForPrincipal(c.Request.Context(), principal)
	if err != nil {
		h.writeError(c, http.StatusServiceUnavailable, "WORKSPACE_SNAPSHOTS_UNAVAILABLE", "could not resolve your workspace storage")
		return
	}
	prefix, err := domain.WorkspaceSnapshotPrefixForRoot(principal.TenantID, root, id)
	if err != nil {
		h.writeError(c, http.StatusInternalServerError, "WORKSPACE_SNAPSHOTS_UNAVAILABLE", "could not prepare workspace version")
		return
	}
	fileCount, err := h.workspaceSnapshotStore.SnapshotWorkspace(c.Request.Context(), workspace.RootPrefix, sourcePath, prefix)
	if err != nil {
		message := "could not create workspace version; ensure the selected directory contains code files"
		if errors.Is(err, objectstore.ErrUnavailable) {
			message = "workspace storage is temporarily unavailable"
		}
		h.writeError(c, http.StatusBadGateway, "WORKSPACE_SNAPSHOT_CREATE_FAILED", message)
		return
	}
	snapshot := domain.WorkspaceSnapshot{ID: id, TenantID: principal.TenantID, UserID: principal.Subject, SourcePath: sourcePath, FileCount: fileCount, CreatedAt: time.Now().UTC()}
	if err := h.workspaceSnapshots.CreateWorkspaceSnapshot(c.Request.Context(), snapshot); err != nil {
		h.writeError(c, http.StatusInternalServerError, "WORKSPACE_SNAPSHOT_PERSIST_FAILED", "workspace version was copied but could not be recorded; contact the platform administrator")
		return
	}
	h.writeSuccess(c, http.StatusCreated, snapshot)
}

func (h *Handler) workspaceSnapshotPrincipal(c *gin.Context) (auth.Principal, bool) {
	principal, ok := h.principal(c)
	if !ok {
		h.writeError(c, http.StatusUnauthorized, "AUTH_REQUIRED", "authentication is required")
		return auth.Principal{}, false
	}
	if !principal.Allowed(domain.RoleEngineer) {
		h.writeError(c, http.StatusForbidden, "FORBIDDEN", "engineer role is required")
		return auth.Principal{}, false
	}
	return principal, true
}

func (h *Handler) ensureWorkspaceObjectSpace(ctx context.Context, principal auth.Principal) error {
	if err := h.repository.EnsureIdentity(ctx, principal); err != nil {
		return fmt.Errorf("persist identity: %w", err)
	}
	if h.directoryInitializer == nil {
		return nil
	}
	root, err := h.personalDataRootForPrincipal(ctx, principal)
	if err != nil {
		return err
	}
	if err := h.directoryInitializer.EnsurePersonalDataDirectories(ctx, root); err != nil {
		return fmt.Errorf("initialize personal workspace: %w", err)
	}
	return nil
}

func (h *Handler) newWorkspaceSnapshotID() (string, error) {
	id, err := h.newID()
	if err != nil {
		return "", err
	}
	id = strings.TrimPrefix(strings.TrimSpace(id), "job-")
	if id == "" {
		return "", fmt.Errorf("invalid generated id")
	}
	return "snapshot-" + id, nil
}
