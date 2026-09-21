package domain

import (
	"fmt"
	"strings"
	"time"
)

const (
	ImageKindTraining       = "training"
	ImageKindWorkspace      = "workspace"
	ImageVisibilityPersonal = "personal"
	ImageVisibilityTeam     = "team"
)

// PlatformImage is a catalogued runtime, either administrator-published or an
// owner-scoped environment build. Selection always checks its visibility.
type PlatformImage struct {
	ID                   string `json:"id"`
	OwnerUserID          string `json:"ownerUserId,omitempty"`
	Visibility           string `json:"visibility,omitempty"`
	EnvironmentVersionID string `json:"environmentVersionId,omitempty"`
	// Empty tenant means global access only for legacy administrator entries.
	TenantID         string           `json:"tenantId,omitempty"`
	Name             string           `json:"name"`
	Reference        string           `json:"reference"`
	Kind             string           `json:"kind"`
	Description      string           `json:"description,omitempty"`
	Framework        string           `json:"framework,omitempty"`
	Environment      ImageEnvironment `json:"environment"`
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

// VisibleTo deliberately has no role override: administrative membership is
// not permission to run another user's personal environment.
func (i PlatformImage) VisibleTo(tenantID, userID string) bool {
	if i.Visibility == ImageVisibilityPersonal {
		return tenantID != "" && tenantID == i.TenantID && userID != "" && userID == i.OwnerUserID
	}
	if i.Visibility != "" && i.Visibility != ImageVisibilityTeam {
		return false
	}
	return i.TenantID == "" || i.TenantID == tenantID
}

func (i PlatformImage) Validate() error {
	switch i.Visibility {
	case "":
		if i.OwnerUserID != "" || i.EnvironmentVersionID != "" {
			return fmt.Errorf("owned images require explicit visibility")
		}
	case ImageVisibilityPersonal, ImageVisibilityTeam:
		if strings.TrimSpace(i.OwnerUserID) == "" || strings.TrimSpace(i.TenantID) == "" {
			return fmt.Errorf("owned images require an owner and tenant")
		}
		if i.Visibility == ImageVisibilityPersonal && i.IsDefault {
			return fmt.Errorf("personal images cannot be defaults")
		}
	default:
		return fmt.Errorf("image visibility must be personal or team")
	}

	if err := i.Environment.Validate(); err != nil {
		return err
	}
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
