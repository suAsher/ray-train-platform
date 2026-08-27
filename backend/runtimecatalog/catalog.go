package runtimecatalog

import (
	"fmt"
	"strings"

	"ray-train-platform-backend/domain"
)

// Policy controls which catalogued runtimes this deployment may admit.
// Both capabilities are disabled by default through the zero value.
type Policy struct {
	ManagedEnabled bool
	CanaryEnabled  bool
	canaryTenants  []string
	canaryScoped   bool
}

// NewPolicy constructs a deployment policy without retaining the caller's
// tenant slice. Tenant IDs are trimmed, empty entries are dropped and the
// original order is preserved while duplicates are removed.
func NewPolicy(managedEnabled, canaryEnabled bool, canaryTenants []string) Policy {
	seen := make(map[string]struct{}, len(canaryTenants))
	normalized := make([]string, 0, len(canaryTenants))
	for _, candidate := range canaryTenants {
		tenantID := strings.TrimSpace(candidate)
		if tenantID == "" {
			continue
		}
		if _, exists := seen[tenantID]; exists {
			continue
		}
		seen[tenantID] = struct{}{}
		normalized = append(normalized, tenantID)
	}
	return Policy{
		ManagedEnabled: managedEnabled,
		CanaryEnabled:  canaryEnabled,
		canaryTenants:  normalized,
	}
}

// Clone returns an independent policy value. The canary allowlist remains
// private so API responses and downstream callers cannot enumerate it.
func (policy Policy) Clone() Policy {
	clone := policy
	clone.canaryTenants = append([]string(nil), policy.canaryTenants...)
	return clone
}

// EffectiveForTenant applies the global canary master switch to one explicit
// tenant. An empty allowlist, empty tenant or non-member always disables
// canary. Resolve only trusts a policy that has passed through this method.
func (policy Policy) EffectiveForTenant(tenantID string) Policy {
	effective := policy.Clone()
	effective.CanaryEnabled = false
	effective.canaryScoped = true
	tenantID = strings.TrimSpace(tenantID)
	if !policy.CanaryEnabled || tenantID == "" {
		return effective
	}
	for _, allowed := range policy.canaryTenants {
		if allowed == tenantID {
			effective.CanaryEnabled = true
			break
		}
	}
	return effective
}

// Snapshot is the immutable runtime identity persisted with a submitted job.
// ImageDigest retains the historical contract name, but carries the exact
// administrator-catalogued reference whether it is a digest or an explicit tag.
type Snapshot struct {
	Engine      domain.TrainingEngine
	RayVersion  string
	ImageDigest string
}

// Resolve derives a job runtime exclusively from validated catalog metadata
// and deployment policy. Request-provided Ray versions are deliberately absent
// from this API and therefore cannot influence the result.
func Resolve(image domain.PlatformImage, requested domain.TrainingEngine, policy Policy) (Snapshot, error) {
	if err := image.Validate(); err != nil {
		return Snapshot{}, fmt.Errorf("invalid image metadata: %w", err)
	}

	engine := requested.Resolved()
	if engine == domain.TrainingEngineRayTrain && !policy.ManagedEnabled {
		return Snapshot{}, fmt.Errorf("Ray Train managed engine is not enabled")
	}
	if !image.Supports(engine) {
		return Snapshot{}, fmt.Errorf("image %q does not support %s", image.Name, engine)
	}
	if image.RayVersion == domain.RayVersionCanary && (!policy.canaryScoped || !policy.CanaryEnabled) {
		return Snapshot{}, fmt.Errorf("Ray %s is restricted to canary tenants", image.RayVersion)
	}

	return Snapshot{
		Engine:      engine,
		RayVersion:  image.RayVersion,
		ImageDigest: image.Reference,
	}, nil
}
