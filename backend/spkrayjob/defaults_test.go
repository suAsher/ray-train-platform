package spkrayjob

import (
	"strings"
	"testing"

	"ray-train-platform-backend/domain"
)

func TestManagedDefaultImageSkipsAnIncompatibleLegacyDefault(t *testing.T) {
	legacy := "registry/legacy@sha256:" + strings.Repeat("a", 64)
	managed := "registry/managed@sha256:" + strings.Repeat("b", 64)
	selected, err := managedImage([]catalogImage{
		{Reference: legacy, Name: "Legacy", IsDefault: true, RayVersion: domain.RayVersionLegacy, SupportedEngines: []domain.TrainingEngine{domain.TrainingEngineRayDDP}},
		{Reference: managed, Name: "Managed", RayVersion: domain.RayVersionProduction, SupportedEngines: []domain.TrainingEngine{domain.TrainingEngineRayDDP, domain.TrainingEngineRayTrain}},
	}, "", PlatformRuntimeLimits{
		AvailableEngines: []string{"ray-ddp", "ray-train"}, ManagedEnabled: true,
		ProductionRayVersion: domain.RayVersionProduction, CanaryRayVersion: domain.RayVersionCanary,
	})
	if err != nil || selected.Reference != managed {
		t.Fatalf("managed default must select the compatible image, got %+v err=%v", selected, err)
	}
}

func TestManagedImageRequiresExactCatalogCompatibility(t *testing.T) {
	legacy := catalogImage{
		Reference: "registry/legacy@sha256:" + strings.Repeat("c", 64), Name: "Legacy", IsDefault: true,
		RayVersion: domain.RayVersionLegacy, SupportedEngines: []domain.TrainingEngine{domain.TrainingEngineRayDDP},
	}
	runtime := PlatformRuntimeLimits{
		AvailableEngines: []string{"ray-ddp", "ray-train"}, ManagedEnabled: true,
		ProductionRayVersion: domain.RayVersionProduction, CanaryRayVersion: domain.RayVersionCanary,
	}
	if _, err := managedImage([]catalogImage{legacy}, legacy.Reference, runtime); err == nil || !strings.Contains(err.Error(), "ray-train") {
		t.Fatalf("legacy image was accepted for managed training: %v", err)
	}
	if _, err := managedImage([]catalogImage{legacy}, "registry/missing:tag", runtime); err == nil || !strings.Contains(err.Error(), "镜像目录") {
		t.Fatalf("an image absent from the catalog was accepted: %v", err)
	}
}

func TestManagedImageAllowsCanaryOnlyWhenServerEnablesCanary(t *testing.T) {
	canary := catalogImage{
		Reference: "registry/canary:2.58", Name: "Canary", RayVersion: domain.RayVersionCanary,
		SupportedEngines: []domain.TrainingEngine{domain.TrainingEngineRayTrain},
	}
	runtime := PlatformRuntimeLimits{
		AvailableEngines: []string{"ray-ddp", "ray-train"}, ManagedEnabled: true,
		ProductionRayVersion: domain.RayVersionProduction, CanaryRayVersion: domain.RayVersionCanary,
	}
	if _, err := managedImage([]catalogImage{canary}, canary.Reference, runtime); err == nil {
		t.Fatal("canary image was accepted while canary capability was disabled")
	}
	runtime.CanaryEnabled = true
	selected, err := managedImage([]catalogImage{canary}, canary.Reference, runtime)
	if err != nil || selected.Reference != canary.Reference {
		t.Fatalf("enabled canary image was rejected: %+v err=%v", selected, err)
	}
}

// The daily loop should be "edit code, submit". Requiring --name on every run
// makes the user invent a name for something that already has one: the
// directory they are standing in.
func TestDefaultJobNameComesFromTheSourceDirectory(t *testing.T) {
	name, err := defaultJobName("/home/alice/bevfusion-bev_3dod", fixedClock)
	if err != nil {
		t.Fatalf("derive name: %v", err)
	}
	if !strings.HasPrefix(name, "bevfusion-bev-3dod-") {
		t.Fatalf("expected the directory to name the job, got %q", name)
	}
	// RayJob names must be unique in a namespace, so a run suffix is required;
	// resubmitting the same directory must not collide with the previous run.
	if name == "bevfusion-bev-3dod" {
		t.Fatal("expected a per-run suffix")
	}
}

// A job name is a Kubernetes DNS label. Directory names routinely contain
// characters that are not, and a rejected name would only surface as a server
// error after the whole archive had been uploaded.
func TestDefaultJobNameIsAlwaysAValidDNSLabel(t *testing.T) {
	for _, directory := range []string{
		"/tmp/My_Project", "/tmp/BEVFusion.v2", "/tmp/---weird---", "/tmp/训练代码",
		"/tmp/" + strings.Repeat("very-long-directory-name", 8),
	} {
		name, err := defaultJobName(directory, fixedClock)
		if err != nil {
			t.Fatalf("derive name for %q: %v", directory, err)
		}
		if !isDNSLabel(name) {
			t.Fatalf("directory %q produced an invalid job name %q", directory, name)
		}
		if len(name) > 63 {
			t.Fatalf("job name %q exceeds 63 characters", name)
		}
	}
}

// The image catalogue is the administrator's allowlist, and it marks one entry
// as the default. Making the user paste a sha256 digest for the common case is
// the single most error-prone part of a submission.
func TestDefaultImagePrefersTheCatalogueDefault(t *testing.T) {
	images := []catalogImage{
		{Reference: "registry/a@sha256:" + strings.Repeat("a", 64), Name: "A"},
		{Reference: "registry/b@sha256:" + strings.Repeat("b", 64), Name: "B", IsDefault: true},
	}
	image, err := defaultImage(images)
	if err != nil {
		t.Fatalf("choose image: %v", err)
	}
	if !strings.HasPrefix(image, "registry/b@") {
		t.Fatalf("expected the catalogue default, got %q", image)
	}
}

// With exactly one approved image there is no ambiguity to resolve.
func TestDefaultImageUsesTheOnlyCatalogueEntry(t *testing.T) {
	image, err := defaultImage([]catalogImage{{Reference: "registry/only@sha256:" + strings.Repeat("c", 64), Name: "Only"}})
	if err != nil || !strings.HasPrefix(image, "registry/only@") {
		t.Fatalf("expected the sole image, got %q (%v)", image, err)
	}
}

// Guessing between several equally valid images would silently train on the
// wrong environment, so the choice is surfaced instead.
func TestDefaultImageRefusesToGuessBetweenSeveralNonDefaultImages(t *testing.T) {
	_, err := defaultImage([]catalogImage{
		{Reference: "registry/a@sha256:" + strings.Repeat("a", 64), Name: "A"},
		{Reference: "registry/b@sha256:" + strings.Repeat("b", 64), Name: "B"},
	})
	if err == nil {
		t.Fatal("expected an error listing the candidates")
	}
	if !strings.Contains(err.Error(), "registry/a@") || !strings.Contains(err.Error(), "registry/b@") {
		t.Fatalf("the error must list the choices, got %v", err)
	}
}

func TestDefaultImageReportsAnEmptyCatalogue(t *testing.T) {
	if _, err := defaultImage(nil); err == nil {
		t.Fatal("expected an error for an empty catalogue")
	}
}
