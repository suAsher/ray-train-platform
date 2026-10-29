package environmentbuild

import (
	"errors"
	"strings"
	"testing"
)

func TestCustomRegistryConfigurationStillRestrictsUserDestinations(t *testing.T) {
	base, _, _, _, _ := lifecycleFixture(t)
	cfg := base.config
	cfg.RegistryHosts = []string{"harbor.custom.example"}
	cfg.BaseImage = "harbor.custom.example/project/base@sha256:" + strings.Repeat("1", 64)
	cfg.WorkspaceImage = "harbor.custom.example/project/workspace@sha256:" + strings.Repeat("2", 64)
	service, err := NewService(base.store, base.runner, base.registry, base.vault, cfg)
	if err != nil {
		t.Fatalf("administrator configured registry rejected: %v", err)
	}
	for _, request := range []string{"", "harbor.custom.example"} {
		if host, err := service.registryHost(request); err != nil || host != "harbor.custom.example" {
			t.Fatalf("configured host rejected: %q %v", host, err)
		}
	}
	for _, request := range []string{RegistryHost, "unapproved.example", "harbor.custom.example.evil.example", "https://harbor.custom.example"} {
		if _, err := service.registryHost(request); !errors.Is(err, ErrInvalid) {
			t.Fatalf("user escaped registry allowlist: %q %v", request, err)
		}
	}
}
