package domain

import (
	"fmt"
	"strings"
	"time"
)

const (
	ImageKindTraining  = "training"
	ImageKindWorkspace = "workspace"
)

// PlatformImage is a runtime environment an administrator has published for
// users to choose from, so a job's dependencies are a selection rather than a
// hand-typed digest.
type PlatformImage struct {
	ID string `json:"id"`
	// Empty means the image is shared with every tenant.
	TenantID    string    `json:"tenantId,omitempty"`
	Name        string    `json:"name"`
	Reference   string    `json:"reference"`
	Kind        string    `json:"kind"`
	Description string    `json:"description,omitempty"`
	Framework   string    `json:"framework,omitempty"`
	IsDefault   bool      `json:"isDefault"`
	CreatedBy   string    `json:"createdBy,omitempty"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

func ValidateImageKind(kind string) error {
	switch kind {
	case ImageKindTraining, ImageKindWorkspace:
		return nil
	default:
		return fmt.Errorf("image kind must be %q or %q", ImageKindTraining, ImageKindWorkspace)
	}
}

func (i PlatformImage) Validate() error {
	if strings.TrimSpace(i.Name) == "" {
		return fmt.Errorf("image name is required")
	}
	if err := ValidateImageKind(i.Kind); err != nil {
		return err
	}
	// Administrators may publish either an immutable digest or an explicit tag.
	// The workload renderer always refreshes tagged images from the registry.
	if err := ValidateRuntimeImage(i.Reference); err != nil {
		return fmt.Errorf("image reference: %w", err)
	}
	return nil
}
