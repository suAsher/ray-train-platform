package api

import (
	"context"
	"fmt"

	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
	"ray-train-platform-backend/objectstore"
)

// personalDataSpaceInitializer translates a newly-created, trusted platform
// identity into the only personal root it can own. The storage layer creates
// the fixed workspace/files/runs/snapshots markers below that root.
type personalDataSpaceInitializer struct {
	directories objectstore.PersonalDataDirectoryInitializer
}

func NewPersonalDataSpaceInitializer(directories objectstore.PersonalDataDirectoryInitializer) PersonalDataInitializer {
	return personalDataSpaceInitializer{directories: directories}
}

func (initializer personalDataSpaceInitializer) EnsurePersonalDataSpace(ctx context.Context, principal auth.Principal) error {
	if initializer.directories == nil {
		return objectstore.ErrUnavailable
	}
	root, err := domain.PersonalDataRootFor(principal.TenantID, StorageKeyForPrincipal(principal))
	if err != nil {
		return fmt.Errorf("derive personal data space: %w", err)
	}
	if err := initializer.directories.EnsurePersonalDataDirectories(ctx, root); err != nil {
		return fmt.Errorf("initialize personal data space: %w", err)
	}
	return nil
}
