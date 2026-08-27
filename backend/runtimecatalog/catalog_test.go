package runtimecatalog

import (
	"strings"
	"testing"

	"ray-train-platform-backend/domain"
)

func runtimeImage(version string, engines ...domain.TrainingEngine) domain.PlatformImage {
	return domain.PlatformImage{
		ID:               "image-1",
		Name:             "managed-runtime",
		Kind:             domain.ImageKindTraining,
		Reference:        "harbor.example/ray-runtime:stable",
		RayVersion:       version,
		SupportedEngines: append([]domain.TrainingEngine(nil), engines...),
	}
}

func TestResolveRejectsManagedEngineOnLegacyImage(t *testing.T) {
	image := runtimeImage(domain.RayVersionLegacy, domain.TrainingEngineRayDDP)

	_, err := Resolve(image, domain.TrainingEngineRayTrain, Policy{ManagedEnabled: true})

	if err == nil || !strings.Contains(err.Error(), "does not support ray-train") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestResolveRejectsDisabledManagedAndCanaryRuntimes(t *testing.T) {
	t.Run("managed disabled", func(t *testing.T) {
		image := runtimeImage(domain.RayVersionProduction, domain.TrainingEngineRayTrain)
		_, err := Resolve(image, domain.TrainingEngineRayTrain, Policy{})
		if err == nil || !strings.Contains(err.Error(), "managed engine is not enabled") {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("canary disabled", func(t *testing.T) {
		image := runtimeImage(domain.RayVersionCanary, domain.TrainingEngineRayDDP)
		_, err := Resolve(image, domain.TrainingEngineRayDDP, Policy{})
		if err == nil || !strings.Contains(err.Error(), "restricted to canary") {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}

func TestResolveReturnsAuthoritativeCatalogSnapshot(t *testing.T) {
	image := runtimeImage(domain.RayVersionProduction, domain.TrainingEngineRayDDP, domain.TrainingEngineRayTrain)

	snapshot, err := Resolve(image, domain.TrainingEngineRayTrain, Policy{ManagedEnabled: true})
	if err != nil {
		t.Fatalf("resolve runtime: %v", err)
	}
	if snapshot.Engine != domain.TrainingEngineRayTrain || snapshot.RayVersion != domain.RayVersionProduction || snapshot.ImageDigest != image.Reference {
		t.Fatalf("unexpected snapshot: %+v", snapshot)
	}

	image.Reference = "harbor.example/changed:latest"
	image.SupportedEngines[0] = "mutated"
	if snapshot.ImageDigest != "harbor.example/ray-runtime:stable" {
		t.Fatalf("snapshot retained caller-owned image state: %+v", snapshot)
	}
}

func TestResolveValidatesCatalogMetadataAndDefaultsOmittedEngine(t *testing.T) {
	t.Run("invalid metadata", func(t *testing.T) {
		image := runtimeImage(domain.RayVersionProduction, domain.TrainingEngineRayDDP, domain.TrainingEngineRayDDP)
		if _, err := Resolve(image, domain.TrainingEngineRayDDP, Policy{}); err == nil || !strings.Contains(err.Error(), "invalid image metadata") {
			t.Fatalf("expected invalid catalog metadata, got %v", err)
		}
	})

	t.Run("omitted engine", func(t *testing.T) {
		image := runtimeImage(domain.RayVersionProduction, domain.TrainingEngineRayDDP)
		snapshot, err := Resolve(image, "", Policy{})
		if err != nil || snapshot.Engine != domain.TrainingEngineRayDDP {
			t.Fatalf("snapshot=%+v err=%v", snapshot, err)
		}
	})
}
