package config

import (
	"crypto/sha256"
	"fmt"
	"strconv"
	"strings"
	"testing"
)

func TestPersonalStorageDisabledByDefault(t *testing.T) {
	t.Setenv("PERSONAL_STORAGE_ENABLED", "false")
	cfg, err := loadPersonalStorageConfig()
	if err != nil || cfg.Enabled {
		t.Fatalf("default personal storage must be disabled: %#v %v", cfg, err)
	}
}
func TestPersonalStorageRequiresPinnedHostAndDriver(t *testing.T) {
	t.Setenv("PERSONAL_STORAGE_ENABLED", "true")
	if _, err := loadPersonalStorageConfig(); err == nil {
		t.Fatal("enabled personal storage requires pinned upstream identity and driver")
	}
}

func setValidPersonalStorageScopeConfig(t *testing.T) {
	t.Helper()
	t.Setenv("PERSONAL_STORAGE_ENABLED", "true")
	t.Setenv("PERSONAL_STORAGE_HOST", "mount.wellspiking.ai")
	t.Setenv("PERSONAL_STORAGE_PORT", "22")
	t.Setenv("PERSONAL_STORAGE_KNOWN_HOSTS_FILE", "/run/known-hosts")
	t.Setenv("PERSONAL_STORAGE_CSI_IMAGE", "registry.example/csi@sha256:"+strings.Repeat("a", 64))
	t.Setenv("PERSONAL_STORAGE_MOUNT_CAPACITY", "1Ti")
	t.Setenv("PERSONAL_STORAGE_MAX_UPLOAD_BYTES", "67108864")
}

func TestPersonalStorageScopeAllowsOnlyAttestedOrExplicitOwners(t *testing.T) {
	for _, tc := range []struct {
		name       string
		verified   bool
		owners     string
		wantError  bool
		wantOwners int
	}{
		{"unverified global rejected", false, "", true, 0},
		{"scoped without global attestation", false, "stable-alice,oauth2-proxy:stable-bob", false, 2},
		{"verified global mode", true, "", false, 0},
		{"verified restricted mode", true, "stable-alice", false, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setValidPersonalStorageScopeConfig(t)
			t.Setenv("PERSONAL_STORAGE_PERSONAL_ROOT_ISOLATION_VERIFIED", strconv.FormatBool(tc.verified))
			t.Setenv("PERSONAL_STORAGE_TEST_OWNER_IDS", tc.owners)
			cfg, err := loadPersonalStorageConfig()
			if (err != nil) != tc.wantError {
				t.Fatalf("scope validation error=%v", err)
			}
			if err == nil && (len(cfg.TestOwnerIDs) != tc.wantOwners || cfg.PersonalRootIsolationVerified != tc.verified) {
				t.Fatal("test scope changed global attestation or owner identity")
			}
		})
	}
}

func TestPersonalStorageScopeRejectsMalformedDeploymentOwners(t *testing.T) {
	for _, owners := range []string{"*", "alice,*", "alice,", ",alice", "alice,,bob", "alice,alice", "../alice", "alice/bob", "alice?owner=bob", "alice\nbob", strings.Repeat("a", 257), strings.Join([]string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "k", "l", "m", "n", "o", "p", "q"}, ",")} {
		t.Run(fmt.Sprintf("case-%x", sha256.Sum256([]byte(owners))), func(t *testing.T) {
			setValidPersonalStorageScopeConfig(t)
			t.Setenv("PERSONAL_STORAGE_PERSONAL_ROOT_ISOLATION_VERIFIED", "true")
			t.Setenv("PERSONAL_STORAGE_TEST_OWNER_IDS", owners)
			if _, err := loadPersonalStorageConfig(); err == nil {
				t.Fatal("malformed deployment scope accepted")
			}
		})
	}
}
