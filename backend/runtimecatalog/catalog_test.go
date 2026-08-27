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

func TestEffectivePolicyScopesCanaryToExplicitTenants(t *testing.T) {
	tenants := []string{" tenant-a ", "tenant-a", "", "tenant-b"}
	policy := NewPolicy(true, true, tenants)
	tenants[0] = "tenant-c"

	if effective := policy.EffectiveForTenant(" tenant-a "); !effective.CanaryEnabled {
		t.Fatalf("allowlisted tenant did not receive canary: %+v", effective)
	}
	if effective := policy.EffectiveForTenant("tenant-c"); effective.CanaryEnabled {
		t.Fatalf("caller mutation changed canary policy: %+v", effective)
	}
	if effective := NewPolicy(true, true, nil).EffectiveForTenant("tenant-a"); effective.CanaryEnabled {
		t.Fatalf("empty allowlist enabled canary: %+v", effective)
	}
	if effective := NewPolicy(true, false, []string{"tenant-a"}).EffectiveForTenant("tenant-a"); effective.CanaryEnabled {
		t.Fatalf("disabled master switch enabled canary: %+v", effective)
	}
}

func TestPolicyCloneDefensivelyCopiesCanaryTenants(t *testing.T) {
	policy := NewPolicy(true, true, []string{"tenant-a"})
	clone := policy.Clone()
	policy.canaryTenants[0] = "tenant-b"

	if !clone.EffectiveForTenant("tenant-a").CanaryEnabled || clone.EffectiveForTenant("tenant-b").CanaryEnabled {
		t.Fatalf("clone retained mutable tenant storage")
	}
}

func TestResolveCanaryRequiresEffectiveTenantPolicy(t *testing.T) {
	image := runtimeImage(domain.RayVersionCanary, domain.TrainingEngineRayTrain)

	t.Run("allowlisted", func(t *testing.T) {
		policy := NewPolicy(true, true, []string{"tenant-a"}).EffectiveForTenant("tenant-a")
		snapshot, err := Resolve(image, domain.TrainingEngineRayTrain, policy)
		if err != nil || snapshot.RayVersion != domain.RayVersionCanary {
			t.Fatalf("snapshot=%+v err=%v", snapshot, err)
		}
	})

	for _, test := range []struct {
		name   string
		policy Policy
	}{
		{name: "unscoped master policy", policy: NewPolicy(true, true, []string{"tenant-a"})},
		{name: "non-allowlisted", policy: NewPolicy(true, true, []string{"tenant-a"}).EffectiveForTenant("tenant-b")},
		{name: "empty allowlist", policy: NewPolicy(true, true, nil).EffectiveForTenant("tenant-a")},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := Resolve(image, domain.TrainingEngineRayTrain, test.policy); err == nil || !strings.Contains(err.Error(), "restricted to canary") {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
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
