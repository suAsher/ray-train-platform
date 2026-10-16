package environmentbuild

import (
	"context"
	"log/slog"
	"time"

	"ray-train-platform-backend/domain"
)

type currentWorkspaceReader interface {
	GetWorkspace(context.Context, string, string) (*domain.DevWorkspace, error)
}

// CapabilitiesForOwner adds a best-effort UI hint, never authorization to save.
// The existing Create path independently verifies ownership and runtime identity.
func (s *Service) CapabilitiesForOwner(ctx context.Context, owner Owner) map[string]any {
	caps := s.Capabilities()
	if !s.Enabled() || owner.UserID == "" || owner.TenantID == "" || s.store == nil || s.runner == nil {
		return caps
	}
	reader, ok := s.store.(currentWorkspaceReader)
	if !ok {
		return caps
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	current, err := reader.GetWorkspace(ctx, owner.TenantID, owner.UserID)
	if err != nil || !ownedRunningWorkspace(current, owner) {
		return omitWorkspaceHint(caps, "current-workspace")
	}
	workspace, err := s.store.EnvironmentWorkspace(ctx, owner, current.ID)
	if err != nil || workspace.ID != current.ID || workspace.TenantID != owner.TenantID || workspace.OwnerID != owner.UserID || workspace.State != "RUNNING" || workspace.Namespace != current.Namespace || workspace.ResourceName != current.RayClusterName {
		return omitWorkspaceHint(caps, "owned-workspace")
	}
	snapshot, err := s.runner.InspectWorkspace(ctx, workspace)
	if err != nil || ctx.Err() != nil || snapshot.UID == "" || !pinnedProfileImage(snapshot.Image) {
		return omitWorkspaceHint(caps, "runtime-identity")
	}
	profile, ok := s.workspaceProfile(snapshot.Image)
	if !ok || !pinnedProfileImage(profile.BaseImage) {
		return omitWorkspaceHint(caps, "trusted-profile")
	}
	caps["eligibleWorkspaceId"] = current.ID
	return caps
}

func ownedRunningWorkspace(workspace *domain.DevWorkspace, owner Owner) bool {
	return workspace != nil && workspace.ID != "" && workspace.TenantID == owner.TenantID && workspace.UserID == owner.UserID && workspace.State == domain.WorkspaceRunning && workspace.Namespace != "" && workspace.RayClusterName != ""
}

func omitWorkspaceHint(caps map[string]any, stage string) map[string]any {
	// Never log raw repository/Kubernetes errors, identifiers, or credentials here.
	slog.Debug("optional environment capability hint omitted", "stage", stage)
	return caps
}
