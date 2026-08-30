package spkrayjob

import (
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/api/resource"

	"ray-train-platform-backend/domain"
)

const (
	localValidationName       = "pending-job"
	localValidationImage      = "local.invalid/train@sha256:0000000000000000000000000000000000000000000000000000000000000000"
	localValidationArtifactID = "pending-artifact"
	localValidationQueue      = "platform-default"
	localValidationCacheSize  = "1Gi"
)

// validatePreflightJobSpec applies harmless sentinels only for values that the
// submit workflow intentionally derives later. Every supplied value is left
// untouched and is therefore checked by the same domain validator as the API.
func validatePreflightJobSpec(spec domain.JobSpec) error {
	return validateLocalJobSpec(spec, localJobSpecDefaults{
		name: true, image: true, source: true, cacheSize: true,
	})
}

// validateArchiveJobSpec is the last gate before artifact creation/upload.
// Only the source artifact identity is still intentionally unresolved.
func validateArchiveJobSpec(spec domain.JobSpec) error {
	return validateLocalJobSpec(spec, localJobSpecDefaults{source: true})
}

// validateFinalJobSpec is the last local gate before the create-job API.
// The tenant queue is the only value the server still derives at this point.
func validateFinalJobSpec(spec domain.JobSpec) error {
	return validateLocalJobSpec(spec, localJobSpecDefaults{})
}

type localJobSpecDefaults struct {
	name      bool
	image     bool
	source    bool
	cacheSize bool
}

func validateLocalJobSpec(spec domain.JobSpec, defaults localJobSpecDefaults) error {
	candidate := spec
	if defaults.source && candidate.Source == (domain.CodeSource{}) {
		candidate.Source = domain.CodeSource{Type: "workspace-archive", ArtifactID: localValidationArtifactID}
	}
	if candidate.Queue == "" {
		candidate.Queue = localValidationQueue
	}
	if defaults.name && strings.TrimSpace(candidate.Name) == "" {
		candidate.Name = localValidationName
	}
	if defaults.image && strings.TrimSpace(candidate.Image) == "" {
		candidate.Image = localValidationImage
	}
	if defaults.cacheSize && candidate.Cache.Mode == domain.CacheModeRuntime && strings.TrimSpace(candidate.Cache.Size) == "" {
		candidate.Cache.Size = localValidationCacheSize
	}
	// Ray version is an immutable server-side image-catalog decision. Managed
	// submissions need a version only so the shared domain validator can check
	// their shape locally; this sentinel is never copied back into the request.
	if candidate.TrainingEngine.Resolved() == domain.TrainingEngineRayTrain && strings.TrimSpace(candidate.RayVersion) == "" {
		candidate.RayVersion = domain.RayVersionProduction
		if candidate.DataMode == domain.DataModeStreaming {
			candidate.RayVersion = domain.RayVersionCanary
		}
	}
	if err := candidate.Validate(); err != nil {
		return err
	}
	if candidate.TimeoutSeconds < 0 {
		return fmt.Errorf("timeoutSeconds must not be negative")
	}
	return validateLocalComputeResources(candidate.Resources)
}

// JobSpec.Validate owns shared job-shape policy. These two values are rendered
// directly as Kubernetes resource requests but are not yet covered there, so
// the CLI checks their transport syntax without duplicating cluster limits.
func validateLocalComputeResources(resources domain.Resources) error {
	if resources.CPUPerWorker < 1 {
		return fmt.Errorf("cpuPerWorker must be positive")
	}
	memory, err := resource.ParseQuantity(strings.TrimSpace(resources.MemoryPerWorker))
	if err != nil || memory.Sign() <= 0 {
		return fmt.Errorf("memoryPerWorker must be a positive Kubernetes quantity")
	}
	return nil
}
