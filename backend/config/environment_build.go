package config

import (
	"encoding/base64"
	"fmt"
	"net/url"
	"os"
	"strings"
)

// EnvironmentBuildConfig controls only newly requested environment builds.
// It never replaces the existing training or workspace defaults.
type EnvironmentBuildConfig struct {
	Enabled bool
	BaseImage, WorkspaceImage, PrepareImage, PublisherImage string
	StorageClass, WheelIndexURL string
	EncryptionKey []byte
	NodeSelector map[string]string
}

func loadEnvironmentBuildConfig() (EnvironmentBuildConfig, error) {
	enabled, err := parseBool("ENVIRONMENT_BUILDS_ENABLED", false)
	if err != nil { return EnvironmentBuildConfig{}, err }
	cfg := EnvironmentBuildConfig{Enabled: enabled}
	if !enabled { return cfg, nil }
	images := []struct { key string; target *string }{
		{"ENVIRONMENT_BASE_IMAGE", &cfg.BaseImage},
		{"ENVIRONMENT_WORKSPACE_IMAGE", &cfg.WorkspaceImage},
		{"ENVIRONMENT_PREPARE_IMAGE", &cfg.PrepareImage},
		{"ENVIRONMENT_PUBLISHER_IMAGE", &cfg.PublisherImage},
	}
	for _, item := range images {
		*item.target = strings.TrimSpace(os.Getenv(item.key))
		if !pinnedImagePattern.MatchString(*item.target) || !strings.HasPrefix(*item.target, "harbor.wellspiking.ai/") {
			return EnvironmentBuildConfig{}, fmt.Errorf("%s must be a pinned Harbor image", item.key)
		}
	}
	cfg.EncryptionKey, err = base64.StdEncoding.DecodeString(os.Getenv("ENVIRONMENT_AUTH_KEY"))
	if err != nil || len(cfg.EncryptionKey) != 32 { return EnvironmentBuildConfig{}, fmt.Errorf("ENVIRONMENT_AUTH_KEY must encode a separate 32-byte key") }
	cfg.StorageClass = envOr("ENVIRONMENT_STORAGE_CLASS", "ebs-ssd")
	cfg.WheelIndexURL = strings.TrimSpace(os.Getenv("ENVIRONMENT_WHEEL_INDEX_URL"))
	index, err := url.Parse(cfg.WheelIndexURL)
	if err != nil || index.Scheme != "https" || index.Hostname() == "" || index.User != nil || index.RawQuery != "" || index.Fragment != "" {
		return EnvironmentBuildConfig{}, fmt.Errorf("ENVIRONMENT_WHEEL_INDEX_URL must be a credential-free HTTPS package index")
	}
	cfg.NodeSelector = map[string]string{"platform.wellspiking.ai/gpu-pool": "production"}
	return cfg, nil
}
