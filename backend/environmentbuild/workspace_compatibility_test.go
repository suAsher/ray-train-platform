package environmentbuild

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// JSON keeps the RED tests compilable before the new configuration field is
// introduced, exercising missing behavior instead of a missing symbol.
func compatibleWorkspaceConfig(t *testing.T, cfg Config, images []string) Config {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"CompatibleWorkspaceImages": images})
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestCreateAcceptsOnlyExplicitCompatibleWorkspaceImagesAndSnapshotsActualImage(t *testing.T) {
	legacy := RegistryHost + "/public/debug@sha256:" + strings.Repeat("3", 64)
	for _, name := range []string{"primary", "compatible", "implicit legacy", "unknown digest", "mutable tag", "foreign registry"} {
		t.Run(name, func(t *testing.T) {
			base, store, _, runner := multiRegistryFixture(t)
			cfg := compatibleWorkspaceConfig(t, base.config, []string{legacy})
			runner.image = cfg.WorkspaceImage
			allowed := name == "primary" || name == "compatible"
			switch name {
			case "compatible":
				runner.image = legacy
			case "implicit legacy":
				cfg = compatibleWorkspaceConfig(t, base.config, nil)
				runner.image = legacy
			case "unknown digest":
				runner.image = RegistryHost + "/public/debug@sha256:" + strings.Repeat("4", 64)
			case "mutable tag":
				runner.image = RegistryHost + "/public/debug:latest"
			case "foreign registry":
				runner.image = "harbor.qomolo.com/public/debug@sha256:" + strings.Repeat("3", 64)
			}
			s, err := NewService(base.store, runner, base.registry, base.vault, cfg)
			if err != nil {
				t.Fatal(err)
			}
			s.now = base.now
			owner := Owner{TenantID: "team", UserID: "owner"}
			a, err := s.CreateAuthorization(context.Background(), owner, Credentials{Username: "person", Secret: strings.Repeat("test", 4)})
			if err != nil {
				t.Fatal(err)
			}
			build, err := s.Create(context.Background(), owner, "workspace", CreateRequest{AuthorizationID: a.ID, Project: "project", Repository: "image", Name: "environment", IdempotencyKey: "compatibility-build"})
			if !allowed {
				if !errors.Is(err, ErrConflict) || store.build.ID != "build" {
					t.Fatalf("unlisted workspace image accepted: build=%+v error=%v", build, err)
				}
				return
			}
			if err != nil || build.WorkspaceImage != runner.image || store.build.WorkspaceImage != runner.image || build.WorkspaceUID != "workspace-uid" {
				t.Fatalf("actual workspace image was rejected or replaced by current default: build=%+v error=%v", build, err)
			}
			if s.Capabilities()["workspaceImage"] != cfg.WorkspaceImage {
				t.Fatal("compatibility changed the advertised default workspace image")
			}
		})
	}
}

func TestCompatibleWorkspaceServiceConfigRejectsUnpinnedOrForeignSources(t *testing.T) {
	base, _, _, _ := multiRegistryFixture(t)
	for _, image := range []string{"", " ", RegistryHost + "/public/debug:latest", RegistryHost + "/public/debug@sha256:short", "harbor.qomolo.com/public/debug@sha256:" + strings.Repeat("3", 64), "evil.invalid/debug@sha256:" + strings.Repeat("3", 64)} {
		cfg := compatibleWorkspaceConfig(t, base.config, []string{image})
		if _, err := NewService(base.store, base.runner, base.registry, base.vault, cfg); !errors.Is(err, ErrInvalid) {
			t.Fatalf("service accepted untrusted compatibility source %q: %v", image, err)
		}
	}
}

func TestCompatibleWorkspaceServiceConfigDeduplicatesAndCopiesCallerSlice(t *testing.T) {
	base, _, _, _ := multiRegistryFixture(t)
	legacy := RegistryHost + "/public/debug@sha256:" + strings.Repeat("3", 64)
	cfg := compatibleWorkspaceConfig(t, base.config, []string{legacy, base.config.WorkspaceImage, legacy})
	s, err := NewService(base.store, base.runner, base.registry, base.vault, cfg)
	if err != nil {
		t.Fatal(err)
	}
	callerField := reflect.ValueOf(cfg).FieldByName("CompatibleWorkspaceImages")
	serviceField := reflect.ValueOf(s.config).FieldByName("CompatibleWorkspaceImages")
	if !callerField.IsValid() || !serviceField.IsValid() || !reflect.DeepEqual(serviceField.Interface(), []string{legacy}) {
		t.Fatal("service must keep a normalized compatibility allowlist separate from the primary")
	}
	callerField.Index(0).SetString("evil.invalid/changed:latest")
	if !reflect.DeepEqual(serviceField.Interface(), []string{legacy}) {
		t.Fatal("caller mutation changed the service image allowlist")
	}
}
