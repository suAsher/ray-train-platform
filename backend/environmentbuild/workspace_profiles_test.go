package environmentbuild

import (
	"context"
	"errors"
	"ray-train-platform-backend/domain"
	"strings"
	"testing"
)

func cudaWorkspaceProfile() WorkspaceProfile {
	return WorkspaceProfile{WorkspaceImage: RegistryHost + "/public/debug-cu126@sha256:" + strings.Repeat("5", 64), BaseImage: RegistryHost + "/public/base-cu126@sha256:" + strings.Repeat("6", 64), PrepareImage: RegistryHost + "/public/prepare-cu126@sha256:" + strings.Repeat("7", 64), Environment: domain.ImageEnvironment{Python: "3.10", CUDA: "12.6", PyTorch: "2.6.0+cu126"}}
}
func TestCreateUsesTrustedProfileFromActualWorkspaceImage(t *testing.T) {
	base, _, _, runner := multiRegistryFixture(t)
	profile := cudaWorkspaceProfile()
	cfg := base.config
	cfg.WorkspaceProfiles = []WorkspaceProfile{profile}
	runner.image = profile.WorkspaceImage
	service, err := NewService(base.store, runner, base.registry, base.vault, cfg)
	if err != nil {
		t.Fatal(err)
	}
	service.now = base.now
	owner := Owner{TenantID: "team", UserID: "owner"}
	auth, err := service.CreateAuthorization(context.Background(), owner, Credentials{Username: "fixture", Secret: "fixture-only"})
	if err != nil {
		t.Fatal(err)
	}
	build, err := service.Create(context.Background(), owner, "workspace", CreateRequest{AuthorizationID: auth.ID, Project: "project", Repository: "image", Name: "CUDA environment", IdempotencyKey: "cuda126-build"})
	if err != nil || build.BaseImage != profile.BaseImage || build.WorkspaceImage != profile.WorkspaceImage {
		t.Fatalf("actual image profile not bound: %+v %v", build, err)
	}
	if service.Capabilities()["workspaceImage"] != cfg.WorkspaceImage {
		t.Fatal("new profile changed default")
	}
	profiles := service.Capabilities()["workspaceProfiles"].([]WorkspaceProfile)
	if len(profiles) != 1 || profiles[0].Environment.CUDA != "12.6" {
		t.Fatal("missing version capability")
	}
	cfg.WorkspaceProfiles[0].BaseImage = "evil.invalid/base:latest"
	profiles[0].BaseImage = "evil.invalid/base:latest"
	got, ok := service.workspaceProfile(profile.WorkspaceImage)
	if !ok || got.BaseImage != profile.BaseImage {
		t.Fatal("caller changed trusted profile")
	}
}
func TestWorkspaceProfilesRejectAmbiguousOrUntrustedConfiguration(t *testing.T) {
	base, _, _, _ := multiRegistryFixture(t)
	for _, scenario := range []string{"workspace tag", "base registry", "prepare missing", "default conflict", "compatible conflict", "duplicate", "invalid metadata"} {
		t.Run(scenario, func(t *testing.T) {
			cfg := base.config
			p := cudaWorkspaceProfile()
			switch scenario {
			case "workspace tag":
				p.WorkspaceImage = RegistryHost + "/public/debug:latest"
			case "base registry":
				p.BaseImage = "evil.invalid/base@sha256:" + strings.Repeat("6", 64)
			case "prepare missing":
				p.PrepareImage = ""
			case "default conflict":
				p.WorkspaceImage = cfg.WorkspaceImage
			case "compatible conflict":
				cfg.CompatibleWorkspaceImages = []string{p.WorkspaceImage}
			case "invalid metadata":
				p.Environment.Python = "3.10\nforged"
			}
			cfg.WorkspaceProfiles = []WorkspaceProfile{p}
			if scenario == "duplicate" {
				cfg.WorkspaceProfiles = append(cfg.WorkspaceProfiles, p)
			}
			if _, err := NewService(base.store, base.runner, base.registry, base.vault, cfg); !errors.Is(err, ErrInvalid) {
				t.Fatalf("accepted %s: %v", scenario, err)
			}
		})
	}
}

func TestRevokedWorkspaceProfileFailsClosedButStillCleansResources(t *testing.T) {
	service, store, _, runner := multiRegistryFixture(t)
	profile := cudaWorkspaceProfile()
	store.build.WorkspaceImage = profile.WorkspaceImage
	store.build.BaseImage = profile.BaseImage
	if err := service.reconcileBuild(context.Background(), store.build); err != nil {
		t.Fatal(err)
	}
	if store.build.Status != Failed || runner.steps != 0 {
		t.Fatal("revoked profile was executed")
	}
	if err := service.reconcileBuild(context.Background(), store.build); err != nil {
		t.Fatal(err)
	}
	if runner.cleanups != 1 || store.build.CleanedAt == nil || !store.revoked {
		t.Fatal("revoked profile blocked cleanup")
	}
	if _, err := service.Retry(context.Background(), Owner{TenantID: "team", UserID: "owner"}, store.build.ID, ""); !errors.Is(err, ErrConflict) || store.retried {
		t.Fatal("revoked profile retried")
	}
}

type profileFinalizingStore struct {
	*multiRegistryStore
	finalized Build
}

func (s *profileFinalizingStore) FinalizeEnvironmentBuild(_ context.Context, b Build, _ string) error {
	s.finalized = b
	return nil
}
func TestFinalizeUsesTrustedProfileMetadataInsteadOfCaptureOutput(t *testing.T) {
	base, store, _, runner := multiRegistryFixture(t)
	profile := cudaWorkspaceProfile()
	cfg := base.config
	cfg.WorkspaceProfiles = []WorkspaceProfile{profile}
	finalizer := &profileFinalizingStore{multiRegistryStore: store}
	service, err := NewService(finalizer, runner, base.registry, base.vault, cfg)
	if err != nil {
		t.Fatal(err)
	}
	service.now = base.now
	b := store.build
	b.Status = VerifyingPull
	b.BaseImage, b.WorkspaceImage = profile.BaseImage, profile.WorkspaceImage
	b.ImageDigest = "sha256:" + strings.Repeat("8", 64)
	b.SnapshotJSON = `{"environment":{"cuda":"forged"}}`
	b.TrustedEnvironment = domain.ImageEnvironment{CUDA: "forged"}
	runner.result = StepResult{Done: true, ChecksJSON: `{"pullVerified":true}`}
	if err := service.reconcileBuild(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	if finalizer.finalized.Status != Ready || finalizer.finalized.TrustedEnvironment != profile.Environment {
		t.Fatal("capture/client metadata replaced trusted versions")
	}
}
