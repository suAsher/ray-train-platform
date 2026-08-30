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

func TestEffectivePolicyScopesManagedAndCanaryToExplicitTenants(t *testing.T) {
	managedTenants := []string{" tenant-a ", "tenant-a", ""}
	canaryTenants := []string{"tenant-a", "tenant-b"}
	policy := NewPolicy(false, true, managedTenants, canaryTenants)
	managedTenants[0] = "tenant-c"
	canaryTenants[0] = "tenant-c"

	if effective := policy.EffectiveForTenant(" tenant-a "); !effective.ManagedEnabled || !effective.CanaryEnabled {
		t.Fatalf("managed and canary allowlisted tenant did not receive both capabilities: %+v", effective)
	}
	if effective := policy.EffectiveForTenant("tenant-b"); effective.ManagedEnabled || effective.CanaryEnabled {
		t.Fatalf("canary allowlist bypassed managed permission: %+v", effective)
	}
	if effective := policy.EffectiveForTenant("tenant-c"); effective.ManagedEnabled || effective.CanaryEnabled {
		t.Fatalf("caller mutation changed tenant policy: %+v", effective)
	}
	if effective := NewPolicy(false, true, nil, nil).EffectiveForTenant("tenant-a"); effective.ManagedEnabled || effective.CanaryEnabled {
		t.Fatalf("empty allowlists enabled managed runtime: %+v", effective)
	}
	if effective := NewPolicy(true, true, nil, []string{"tenant-a"}).EffectiveForTenant(""); effective.ManagedEnabled || effective.CanaryEnabled {
		t.Fatalf("empty tenant inherited global managed permission: %+v", effective)
	}
	if effective := NewPolicy(true, false, nil, []string{"tenant-a"}).EffectiveForTenant("tenant-a"); !effective.ManagedEnabled || effective.CanaryEnabled {
		t.Fatalf("disabled master switch enabled canary: %+v", effective)
	}
}

func TestPolicyCloneDefensivelyCopiesManagedAndCanaryTenants(t *testing.T) {
	policy := NewPolicy(false, true, []string{"tenant-a"}, []string{"tenant-a"})
	clone := policy.Clone()
	policy.managedTenants[0] = "tenant-b"
	policy.canaryTenants[0] = "tenant-b"

	if !clone.EffectiveForTenant("tenant-a").ManagedEnabled || !clone.EffectiveForTenant("tenant-a").CanaryEnabled || clone.EffectiveForTenant("tenant-b").ManagedEnabled || clone.EffectiveForTenant("tenant-b").CanaryEnabled {
		t.Fatalf("clone retained mutable tenant storage")
	}
}

func TestResolveScopesManagedProductionWithoutAffectingRayDDP(t *testing.T) {
	managedImage := runtimeImage(domain.RayVersionProduction, domain.TrainingEngineRayTrain)
	ddpImage := runtimeImage(domain.RayVersionProduction, domain.TrainingEngineRayDDP)
	policy := NewPolicy(false, false, []string{"tenant-a"}, nil)

	if _, err := Resolve(managedImage, domain.TrainingEngineRayTrain, policy.EffectiveForTenant("tenant-a")); err != nil {
		t.Fatalf("allowlisted managed submission rejected: %v", err)
	}
	if _, err := Resolve(managedImage, domain.TrainingEngineRayTrain, policy.EffectiveForTenant("tenant-b")); err == nil || !strings.Contains(err.Error(), "managed engine is not enabled") {
		t.Fatalf("non-allowlisted managed submission error=%v", err)
	}
	if _, err := Resolve(ddpImage, domain.TrainingEngineRayDDP, policy.EffectiveForTenant("tenant-b")); err != nil {
		t.Fatalf("ray-ddp was affected by managed allowlist: %v", err)
	}
}

func TestResolveCanaryRequiresEffectiveTenantPolicy(t *testing.T) {
	image := runtimeImage(domain.RayVersionCanary, domain.TrainingEngineRayTrain)

	t.Run("allowlisted", func(t *testing.T) {
		policy := NewPolicy(true, true, nil, []string{"tenant-a"}).EffectiveForTenant("tenant-a")
		snapshot, err := Resolve(image, domain.TrainingEngineRayTrain, policy)
		if err != nil || snapshot.RayVersion != domain.RayVersionCanary {
			t.Fatalf("snapshot=%+v err=%v", snapshot, err)
		}
	})

	t.Run("empty allowlist enables every authenticated tenant", func(t *testing.T) {
		policy := NewPolicy(true, true, nil, nil).EffectiveForTenant("tenant-a")
		snapshot, err := Resolve(image, domain.TrainingEngineRayTrain, policy)
		if err != nil || snapshot.RayVersion != domain.RayVersionCanary {
			t.Fatalf("snapshot=%+v err=%v", snapshot, err)
		}
	})

	for _, test := range []struct {
		name   string
		policy Policy
	}{
		{name: "unscoped master policy", policy: NewPolicy(true, true, nil, []string{"tenant-a"})},
		{name: "non-allowlisted", policy: NewPolicy(true, true, nil, []string{"tenant-a"}).EffectiveForTenant("tenant-b")},
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
