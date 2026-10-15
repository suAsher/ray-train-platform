package config

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"

	"k8s.io/apimachinery/pkg/api/resource"
)

// PersonalStorageConfig is deployment-owned. HTTP callers never select a host,
// remote root, username, key, or CSI implementation.
type PersonalStorageConfig struct {
	Enabled                       bool
	PersonalRootIsolationVerified bool
	TestOwnerIDs                  []string
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
	if err != nil {
		return cfg, err
	}
	cfg.TestOwnerIDs, err = parsePersonalStorageTestOwnerIDs(os.Getenv("PERSONAL_STORAGE_TEST_OWNER_IDS"))
	if err != nil {
		return cfg, err
	}
	if !cfg.PersonalRootIsolationVerified && len(cfg.TestOwnerIDs) == 0 {
		return cfg, fmt.Errorf("personal storage requires upstream isolation attestation or explicit PERSONAL_STORAGE_TEST_OWNER_IDS")
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

var personalStorageStableOwnerID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9:._-]{0,255}$`)

// Test scope is deployment-owned and never inferred from usernames or teams.
// A blank list means global mode only when the separate attestation is true.
func parsePersonalStorageTestOwnerIDs(raw string) ([]string, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	parts := strings.Split(raw, ",")
	if len(parts) > 16 {
		return nil, fmt.Errorf("PERSONAL_STORAGE_TEST_OWNER_IDS accepts at most 16 stable owner IDs")
	}
	seen := make(map[string]bool, len(parts))
	ids := make([]string, 0, len(parts))
	for _, part := range parts {
		id := strings.TrimSpace(part)
		if !personalStorageStableOwnerID.MatchString(id) || seen[id] {
			return nil, fmt.Errorf("PERSONAL_STORAGE_TEST_OWNER_IDS requires distinct valid stable owner IDs")
		}
		seen[id] = true
		ids = append(ids, id)
	}
	return ids, nil
}
