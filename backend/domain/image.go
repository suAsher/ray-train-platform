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
	TenantID         string           `json:"tenantId,omitempty"`
	Name             string           `json:"name"`
	Reference        string           `json:"reference"`
	Kind             string           `json:"kind"`
	Description      string           `json:"description,omitempty"`
	Framework        string           `json:"framework,omitempty"`
	IsDefault        bool             `json:"isDefault"`
	RayVersion       string           `json:"rayVersion"`
	SupportedEngines []TrainingEngine `json:"supportedEngines"`
	CreatedBy        string           `json:"createdBy,omitempty"`
	CreatedAt        time.Time        `json:"createdAt"`
	UpdatedAt        time.Time        `json:"updatedAt"`
}

// Supports reports whether this image can run the requested engine. An empty
// engine resolves to the legacy ray-ddp default without rewriting either the
// request or the image's caller-owned slice.
func (i PlatformImage) Supports(engine TrainingEngine) bool {
	resolved := engine.Resolved()
	for _, supported := range i.SupportedEngines {
		if supported.Resolved() == resolved {
			return true
		}
	}
	return false
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
	switch i.RayVersion {
	case RayVersionLegacy, RayVersionProduction, RayVersionCanary:
	default:
		return fmt.Errorf("Ray version must be %q, %q, or %q", RayVersionLegacy, RayVersionProduction, RayVersionCanary)
	}
	if len(i.SupportedEngines) == 0 {
		return fmt.Errorf("at least one supported engine is required")
	}
	seen := make(map[TrainingEngine]struct{}, len(i.SupportedEngines))
	for _, engine := range i.SupportedEngines {
		switch engine {
		case TrainingEngineRayDDP, TrainingEngineRayTrain:
		default:
			return fmt.Errorf("supported engine must be %q or %q", TrainingEngineRayDDP, TrainingEngineRayTrain)
		}
		if _, exists := seen[engine]; exists {
			return fmt.Errorf("duplicate supported engine %q", engine)
		}
		seen[engine] = struct{}{}
	}
	if i.RayVersion == RayVersionLegacy {
		if _, supportsRayTrain := seen[TrainingEngineRayTrain]; supportsRayTrain {
			return fmt.Errorf("Ray %s cannot support %s", RayVersionLegacy, TrainingEngineRayTrain)
		}
	}
	return nil
}
