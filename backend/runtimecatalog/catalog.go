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
	managedTenants []string
	canaryTenants  []string
	tenantScoped   bool
}

// NewPolicy constructs a deployment policy without retaining the caller's
// tenant slices. Tenant IDs are trimmed, empty entries are dropped and the
// original order is preserved while duplicates are removed.
func NewPolicy(managedEnabled, canaryEnabled bool, managedTenants, canaryTenants []string) Policy {
	return Policy{
		ManagedEnabled: managedEnabled,
		CanaryEnabled:  canaryEnabled,
		managedTenants: normalizeTenants(managedTenants),
		canaryTenants:  normalizeTenants(canaryTenants),
	}
}

func normalizeTenants(tenants []string) []string {
	seen := make(map[string]struct{}, len(tenants))
	normalized := make([]string, 0, len(tenants))
	for _, candidate := range tenants {
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
	return normalized
}

// Clone returns an independent policy value. The allowlists remain private so
// API responses and downstream callers cannot enumerate them.
func (policy Policy) Clone() Policy {
	clone := policy
	clone.managedTenants = append([]string(nil), policy.managedTenants...)
	clone.canaryTenants = append([]string(nil), policy.canaryTenants...)
	return clone
}

// EffectiveForTenant applies the deployment switches and private allowlists to
// one explicit tenant. Empty tenant identities always fail closed. Canary also
// requires that the tenant has managed runtime permission.
func (policy Policy) EffectiveForTenant(tenantID string) Policy {
	effective := policy.Clone()
	effective.ManagedEnabled = false
	effective.CanaryEnabled = false
	effective.tenantScoped = true
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return effective
	}
	effective.ManagedEnabled = policy.ManagedEnabled || tenantAllowed(policy.managedTenants, tenantID)
	// An empty canary allowlist deliberately means every authenticated tenant
	// once the deployment-wide switch is enabled. A non-empty list narrows the
	// rollout to those tenants. The master switch remains fail-closed by
	// default, so the zero-value policy still enables nothing.
	if effective.ManagedEnabled && policy.CanaryEnabled && (len(policy.canaryTenants) == 0 || tenantAllowed(policy.canaryTenants, tenantID)) {
		effective.CanaryEnabled = true
	}
	return effective
}

func tenantAllowed(tenants []string, tenantID string) bool {
	for _, allowed := range tenants {
		if allowed == tenantID {
			return true
		}
	}
	return false
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
	if image.RayVersion == domain.RayVersionCanary && (!policy.tenantScoped || !policy.CanaryEnabled) {
		return Snapshot{}, fmt.Errorf("Ray %s is restricted to canary tenants", image.RayVersion)
	}

	return Snapshot{
		Engine:      engine,
		RayVersion:  image.RayVersion,
		ImageDigest: image.Reference,
	}, nil
}
