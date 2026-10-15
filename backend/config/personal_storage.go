package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"k8s.io/apimachinery/pkg/api/resource"
)

// PersonalStorageConfig is deployment-owned. HTTP callers never select a host,
// remote root, username, key, or CSI implementation.
type PersonalStorageConfig struct {
	Enabled                       bool
	PersonalRootIsolationVerified bool
	Host                          string
	Port                          int
	KnownHostsFile                string
	CSIImage                      string
	MountCapacity                 string
	MaxUploadBytes                int64
}

func loadPersonalStorageConfig() (PersonalStorageConfig, error) {
	enabled, err := parseBool("PERSONAL_STORAGE_ENABLED", false)
	if err != nil {
		return PersonalStorageConfig{}, err
	}
	cfg := PersonalStorageConfig{Enabled: enabled, Port: 22, MountCapacity: "1Ti", MaxUploadBytes: 64 << 20}
	if !enabled {
		return cfg, nil
	}
	cfg.PersonalRootIsolationVerified, err = parseBool("PERSONAL_STORAGE_PERSONAL_ROOT_ISOLATION_VERIFIED", false)
	if err != nil || !cfg.PersonalRootIsolationVerified {
		return cfg, fmt.Errorf("PERSONAL_STORAGE_PERSONAL_ROOT_ISOLATION_VERIFIED must attest upstream per-account root confinement")
	}
	cfg.Host = strings.TrimSpace(os.Getenv("PERSONAL_STORAGE_HOST"))
	if cfg.Host != "mount.wellspiking.ai" {
		return cfg, fmt.Errorf("PERSONAL_STORAGE_HOST must be mount.wellspiking.ai")
	}
	cfg.Port, err = strconv.Atoi(envOr("PERSONAL_STORAGE_PORT", "22"))
	if err != nil || cfg.Port != 22 {
		return cfg, fmt.Errorf("PERSONAL_STORAGE_PORT must be 22")
	}
	cfg.KnownHostsFile = strings.TrimSpace(os.Getenv("PERSONAL_STORAGE_KNOWN_HOSTS_FILE"))
	if !strings.HasPrefix(cfg.KnownHostsFile, "/") {
		return cfg, fmt.Errorf("PERSONAL_STORAGE_KNOWN_HOSTS_FILE must reference pinned known_hosts")
	}
	cfg.CSIImage = strings.TrimSpace(os.Getenv("PERSONAL_STORAGE_CSI_IMAGE"))
	if !pinnedImagePattern.MatchString(cfg.CSIImage) {
		return cfg, fmt.Errorf("PERSONAL_STORAGE_CSI_IMAGE must use an immutable sha256 digest")
	}
	cfg.MountCapacity = strings.TrimSpace(envOr("PERSONAL_STORAGE_MOUNT_CAPACITY", "1Ti"))
	capacity, err := resource.ParseQuantity(cfg.MountCapacity)
	if err != nil || capacity.Sign() <= 0 {
		return cfg, fmt.Errorf("PERSONAL_STORAGE_MOUNT_CAPACITY is invalid")
	}
	cfg.MaxUploadBytes, err = strconv.ParseInt(envOr("PERSONAL_STORAGE_MAX_UPLOAD_BYTES", "67108864"), 10, 64)
	if err != nil || cfg.MaxUploadBytes < 1 || cfg.MaxUploadBytes > 64<<20 {
		return cfg, fmt.Errorf("PERSONAL_STORAGE_MAX_UPLOAD_BYTES must be between 1 and 67108864")
	}
	return cfg, nil
}
