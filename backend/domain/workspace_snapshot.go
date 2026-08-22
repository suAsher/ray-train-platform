package domain

import (
	"fmt"
	"strings"
	"time"
)

// WorkspaceSnapshot is an immutable, owner-scoped copy of code from a
// personal workspace. Object prefixes and claims are derived by the control
// plane and deliberately do not cross the API boundary.
type WorkspaceSnapshot struct {
	ID         string    `json:"id"`
	TenantID   string    `json:"tenantId"`
	UserID     string    `json:"userId"`
	SourcePath string    `json:"sourcePath,omitempty"`
	FileCount  int       `json:"fileCount"`
	CreatedAt  time.Time `json:"createdAt"`
}

func (snapshot WorkspaceSnapshot) Validate() error {
	if !snapshotID.MatchString(strings.TrimSpace(snapshot.ID)) {
		return fmt.Errorf("workspace snapshot id is invalid")
	}
	if err := validateDataSpaceIdentity("tenant", snapshot.TenantID); err != nil {
		return err
	}
	if err := validateDataSpaceIdentity("user", snapshot.UserID); err != nil {
		return err
	}
	if _, err := NormalizeStorageRelativePath(snapshot.SourcePath); err != nil {
		return fmt.Errorf("workspace snapshot source path: %w", err)
	}
	if snapshot.FileCount < 1 {
		return fmt.Errorf("workspace snapshot must contain at least one file")
	}
	return nil
}

// WorkspaceSnapshotPrefix returns the server-owned immutable location for a
// snapshot. It is intentionally not serialised or accepted from HTTP input.
func WorkspaceSnapshotPrefix(tenantID, userID, id string) (string, error) {
	root, err := PersonalDataRootFor(tenantID, userID)
	if err != nil {
		return "", err
	}
	return WorkspaceSnapshotPrefixForRoot(tenantID, root, id)
}

// WorkspaceSnapshotPrefixForRoot keeps snapshots alongside the exact
// persisted personal data root used by a workload. This lets account IDs stay
// opaque while a stable storage key controls the physical bucket layout.
func WorkspaceSnapshotPrefixForRoot(tenantID, personalRoot, id string) (string, error) {
	if _, err := PersonalDataSpacesForRoot(tenantID, personalRoot); err != nil {
		return "", err
	}
	if !snapshotID.MatchString(strings.TrimSpace(id)) {
		return "", fmt.Errorf("workspace snapshot id is invalid")
	}
	return personalRoot + "snapshots/" + id + "/", nil
}
