package config

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"ray-train-platform-backend/environmentbuild"
	"ray-train-platform-backend/registryauth"
	"strings"
	validation "k8s.io/apimachinery/pkg/util/validation"
)

// EnvironmentBuildConfig controls only newly requested environment builds.
// It never replaces the existing training or workspace defaults.
type EnvironmentBuildConfig struct {
	WorkspaceProfiles                                       []environmentbuild.WorkspaceProfile
	Enabled                                                 bool
	BaseImage, WorkspaceImage, PrepareImage, PublisherImage string
	CompatibleWorkspaceImages                               []string
	StorageClass, WheelIndexURL                             string
	RegistryHosts                                           []string
	EncryptionKey                                           []byte
	NodeSelector                                            map[string]string
}

func loadEnvironmentBuildConfig() (EnvironmentBuildConfig, error) {
	enabled, err := parseBool("ENVIRONMENT_BUILDS_ENABLED", false)
	if err != nil {
		return EnvironmentBuildConfig{}, err
	}
	cfg := EnvironmentBuildConfig{Enabled: enabled}
	if !enabled {
		return cfg, nil
	}
	cfg.RegistryHosts, err = environmentRegistryHosts(os.Getenv("ENVIRONMENT_REGISTRY_HOSTS"))
	if err != nil {
		return EnvironmentBuildConfig{}, err
	}
	images := []struct {
		key    string
		target *string
	}{
		{"ENVIRONMENT_BASE_IMAGE", &cfg.BaseImage},
		{"ENVIRONMENT_WORKSPACE_IMAGE", &cfg.WorkspaceImage},
		{"ENVIRONMENT_PREPARE_IMAGE", &cfg.PrepareImage},
		{"ENVIRONMENT_PUBLISHER_IMAGE", &cfg.PublisherImage},
	}
	for _, item := range images {
		*item.target = strings.TrimSpace(os.Getenv(item.key))
		if !environmentImageAllowed(*item.target, cfg.RegistryHosts) {
			return EnvironmentBuildConfig{}, fmt.Errorf("%s must be a pinned Harbor image", item.key)
		}
	}
	if raw := strings.TrimSpace(os.Getenv("ENVIRONMENT_COMPATIBLE_WORKSPACE_IMAGES")); raw != "" {
		seen := map[string]bool{cfg.WorkspaceImage: true}
		for _, entry := range strings.Split(raw, ",") {
			image := strings.TrimSpace(entry)
			if !environmentImageAllowed(image, cfg.RegistryHosts) {
				return EnvironmentBuildConfig{}, fmt.Errorf("ENVIRONMENT_COMPATIBLE_WORKSPACE_IMAGES must contain only pinned Harbor images")
			}
			if !seen[image] {
				cfg.CompatibleWorkspaceImages = append(cfg.CompatibleWorkspaceImages, image)
				seen[image] = true
			}
		}
	}
	if raw := strings.TrimSpace(os.Getenv("ENVIRONMENT_WORKSPACE_PROFILES")); raw != "" {
		decoder := json.NewDecoder(strings.NewReader(raw))
		decoder.DisallowUnknownFields()
		if len(raw) > 65536 || decoder.Decode(&cfg.WorkspaceProfiles) != nil || cfg.WorkspaceProfiles == nil {
			return EnvironmentBuildConfig{}, fmt.Errorf("ENVIRONMENT_WORKSPACE_PROFILES must be a JSON array of trusted workspace profiles")
		}
		var extra any
		if decoder.Decode(&extra) != io.EOF {
			return EnvironmentBuildConfig{}, fmt.Errorf("ENVIRONMENT_WORKSPACE_PROFILES must contain one JSON array")
		}
	}
	cfg.WorkspaceProfiles, err = environmentbuild.NormalizeWorkspaceProfiles(cfg.WorkspaceImage, cfg.CompatibleWorkspaceImages, cfg.WorkspaceProfiles)
	if err != nil {
		return EnvironmentBuildConfig{}, fmt.Errorf("ENVIRONMENT_WORKSPACE_PROFILES must contain unique pinned Harbor image profiles and valid version metadata")
	}
	for _, profile := range cfg.WorkspaceProfiles {
		for _, image := range []string{profile.WorkspaceImage, profile.BaseImage, profile.PrepareImage} {
			if !environmentImageAllowed(image, cfg.RegistryHosts) {
				return EnvironmentBuildConfig{}, fmt.Errorf("ENVIRONMENT_WORKSPACE_PROFILES contains an image outside the registry allowlist")
			}
		}
	}
	cfg.EncryptionKey, err = base64.StdEncoding.DecodeString(os.Getenv("ENVIRONMENT_AUTH_KEY"))
	if err != nil || len(cfg.EncryptionKey) != 32 {
		return EnvironmentBuildConfig{}, fmt.Errorf("ENVIRONMENT_AUTH_KEY must encode a separate 32-byte key")
	}
	cfg.StorageClass = strings.TrimSpace(os.Getenv("ENVIRONMENT_STORAGE_CLASS"))
	cfg.WheelIndexURL = strings.TrimSpace(os.Getenv("ENVIRONMENT_WHEEL_INDEX_URL"))
	index, err := url.Parse(cfg.WheelIndexURL)
	if err != nil || index.Scheme != "https" || index.Hostname() == "" || index.User != nil || index.RawQuery != "" || index.Fragment != "" {
		return EnvironmentBuildConfig{}, fmt.Errorf("ENVIRONMENT_WHEEL_INDEX_URL must be a credential-free HTTPS package index")
	}
	if raw := strings.TrimSpace(os.Getenv("ENVIRONMENT_NODE_SELECTOR_JSON")); raw != "" {
		if json.Unmarshal([]byte(raw), &cfg.NodeSelector) != nil || cfg.NodeSelector == nil {
			return EnvironmentBuildConfig{}, fmt.Errorf("ENVIRONMENT_NODE_SELECTOR_JSON must be a label map")
		}
		for key, value := range cfg.NodeSelector {
			if len(validation.IsQualifiedName(key)) != 0 || len(validation.IsValidLabelValue(value)) != 0 {
				return EnvironmentBuildConfig{}, fmt.Errorf("ENVIRONMENT_NODE_SELECTOR_JSON contains an invalid label")
			}
		}
	}
	return cfg, nil
}

func environmentRegistryHosts(value string) ([]string, error) {
	if strings.TrimSpace(value) == "" {
		return nil, fmt.Errorf("ENVIRONMENT_REGISTRY_HOSTS is required when builds are enabled")
	}
	var hosts []string
	seen := make(map[string]bool)
	for _, raw := range strings.Split(value, ",") {
		host := strings.TrimSpace(raw)
		normalized, err := registryauth.NormalizeHost(host)
		if host == "" || err != nil {
			return nil, fmt.Errorf("ENVIRONMENT_REGISTRY_HOSTS must contain only supported Harbor hosts")
		}
		if !seen[normalized] {
			hosts = append(hosts, normalized)
			seen[normalized] = true
		}
	}
	return hosts, nil
}

func environmentImageAllowed(image string, hosts []string) bool {
	if !pinnedImagePattern.MatchString(image) {
		return false
	}
	host, _, ok := strings.Cut(image, "/")
	if !ok {
		return false
	}
	for _, allowed := range hosts {
		if host == allowed {
			return true
		}
	}
	return false
}
