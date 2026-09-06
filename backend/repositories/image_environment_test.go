package repositories

import (
	"context"
	"ray-train-platform-backend/domain"
	"strings"
	"testing"
)

func TestImageEnvironmentRoundTrip(t *testing.T) {
	repo := imageRepo(t)
	image := testImage("env-image", "custom", domain.ImageKindTraining, "team-a", false, 'a')
	image.Reference = "harbor.other.example/team/train:v1"
	image.Environment = domain.ImageEnvironment{Python: "3.11", CUDA: "12.4", PyTorch: "2.5.1", MLflow: "2.17.2", Dependencies: "numpy==1.26.4\npyarrow==17.0.0", UseCases: "训练", ValidationNotes: "用户声明，未做 GPU 验证"}
	if err := repo.CreateImage(context.Background(), image); err != nil {
		t.Fatal(err)
	}
	got, err := repo.ImageByReference(context.Background(), "team-a", image.Kind, image.Reference)
	if err != nil {
		t.Fatal(err)
	}
	if got.Environment != image.Environment {
		t.Fatalf("environment lost: %+v", got.Environment)
	}
}

func TestImageEnvironmentLegacyAndInvalidRecords(t *testing.T) {
	for _, tc := range []struct {
		name, environment string
		invalid           bool
	}{
		{"legacy", "", false}, {"empty", "{}", false},
		{"malformed", "{broken", true}, {"wrong shape", "[]", true},
		{"wrong field type", `{"python":42}`, true},
		{"control character", `{"python":"3.11\u0000"}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			image, err := platformImageFromRecord(PlatformImageRecord{
				ID: "legacy", Name: "legacy", Reference: pinnedRef('a'), Kind: domain.ImageKindTraining,
				RayVersion: domain.RayVersionLegacy, SupportedEnginesJSON: `["ray-ddp"]`, EnvironmentJSON: tc.environment,
			})
			if (err != nil) != tc.invalid {
				t.Fatalf("image=%+v err=%v", image, err)
			}
			if !tc.invalid && image.Environment != (domain.ImageEnvironment{}) {
				t.Fatal("legacy image gained metadata")
			}
		})
	}
}

func TestCreateImageRejectsOversizedEnvironmentBeforePersisting(t *testing.T) {
	repo := imageRepo(t)
	image := testImage("env-invalid", "invalid", domain.ImageKindTraining, "team-a", false, 'a')
	image.Environment.Dependencies = strings.Repeat("a", 12001)
	if err := repo.CreateImage(context.Background(), image); err == nil {
		t.Fatal("oversized declaration accepted")
	}
	images, err := repo.ListImages(context.Background(), "team-a", domain.ImageKindTraining)
	if err != nil || len(images) != 0 {
		t.Fatalf("invalid image persisted: %+v %v", images, err)
	}
}
