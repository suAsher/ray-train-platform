package runtimecatalog

import (
	"fmt"

	"ray-train-platform-backend/domain"
)

// Policy controls which catalogued runtimes this deployment may admit.
// Both capabilities are disabled by default through the zero value.
type Policy struct {
	ManagedEnabled bool
	CanaryEnabled  bool
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
	if image.RayVersion == domain.RayVersionCanary && !policy.CanaryEnabled {
		return Snapshot{}, fmt.Errorf("Ray %s is restricted to canary tenants", image.RayVersion)
	}

	return Snapshot{
		Engine:      engine,
		RayVersion:  image.RayVersion,
		ImageDigest: image.Reference,
	}, nil
}
