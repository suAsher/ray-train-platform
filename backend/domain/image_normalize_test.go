package domain

import (
	"reflect"
	"strings"
	"testing"
)

// Digests are pasted from build output, which commonly carries a trailing
// newline. Without trimming, submission fails with a misleading "image is not
// in the allowlist" even though the catalogue holds exactly that image.
func TestNormalizeImageReferenceTrimsPastedWhitespace(t *testing.T) {
	reference := "registry.example/repo@sha256:" + repeatChar('a', 64)
	for _, raw := range []string{reference, reference + "\n", " " + reference + " \t\n"} {
		if got := NormalizeImageReference(raw); got != reference {
			t.Fatalf("expected %q, got %q", reference, got)
		}
	}
}

func validCompatibilityImage() PlatformImage {
	return PlatformImage{
		Name:             "PyTorch runtime",
		Reference:        "registry.example/runtime@sha256:" + strings.Repeat("a", 64),
		Kind:             ImageKindTraining,
		RayVersion:       RayVersionProduction,
		SupportedEngines: []TrainingEngine{TrainingEngineRayDDP, TrainingEngineRayTrain},
	}
}

func TestPlatformImageValidatesCompatibilityMetadata(t *testing.T) {
	for _, version := range []string{RayVersionLegacy, RayVersionProduction, RayVersionCanary} {
		t.Run("accepts Ray "+version, func(t *testing.T) {
			image := validCompatibilityImage()
			image.RayVersion = version
			if version == RayVersionLegacy {
				image.SupportedEngines = []TrainingEngine{TrainingEngineRayDDP}
			}
			if err := image.Validate(); err != nil {
				t.Fatalf("valid compatibility metadata rejected: %v", err)
			}
		})
	}

	tests := []struct {
		name    string
		mutate  func(*PlatformImage)
		message string
	}{
		{name: "missing Ray version", mutate: func(image *PlatformImage) { image.RayVersion = "" }, message: "Ray version"},
		{name: "unknown Ray version", mutate: func(image *PlatformImage) { image.RayVersion = "2.99.0" }, message: "Ray version"},
		{name: "missing engines", mutate: func(image *PlatformImage) { image.SupportedEngines = nil }, message: "supported engine"},
		{name: "unknown engine", mutate: func(image *PlatformImage) { image.SupportedEngines = []TrainingEngine{"pytorch"} }, message: "supported engine"},
		{name: "empty engine is not normalized", mutate: func(image *PlatformImage) { image.SupportedEngines = []TrainingEngine{""} }, message: "supported engine"},
		{name: "duplicate engine", mutate: func(image *PlatformImage) {
			image.SupportedEngines = []TrainingEngine{TrainingEngineRayDDP, TrainingEngineRayDDP}
		}, message: "duplicate"},
		{name: "legacy Ray Train", mutate: func(image *PlatformImage) {
			image.RayVersion = RayVersionLegacy
			image.SupportedEngines = []TrainingEngine{TrainingEngineRayTrain}
		}, message: "2.35.0"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			image := validCompatibilityImage()
			test.mutate(&image)
			err := image.Validate()
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(test.message)) {
				t.Fatalf("Validate() error = %v, want message containing %q", err, test.message)
			}
		})
	}
}

func TestImageSupportsResolvedCompatibilityWithoutMutation(t *testing.T) {
	engines := []TrainingEngine{TrainingEngineRayDDP, TrainingEngineRayTrain}
	want := append([]TrainingEngine(nil), engines...)
	image := validCompatibilityImage()
	image.SupportedEngines = engines

	if !image.Supports("") {
		t.Fatal("an omitted engine must resolve to ray-ddp")
	}
	if !image.Supports(TrainingEngineRayTrain) {
		t.Fatal("ray-train should be supported")
	}
	if image.Supports("unknown") {
		t.Fatal("an unknown engine must not be supported")
	}
	if !reflect.DeepEqual(engines, want) {
		t.Fatalf("Supports mutated caller-owned engines: got %v want %v", engines, want)
	}
}
