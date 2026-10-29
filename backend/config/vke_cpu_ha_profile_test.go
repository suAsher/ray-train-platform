package config

import (
	"os"
	"strings"
	"testing"
)

func TestPlatformChartWiresExistingLocalCacheEnvironmentContract(t *testing.T) {
	contents, err := os.ReadFile("../../helm/ray-train-platform/templates/backend-deployment.yaml")
	if err != nil {
		t.Fatalf("read backend deployment template: %v", err)
	}
	template := string(contents)
	for _, name := range []string{
		"LOCAL_CACHE_ENABLED",
		"LOCAL_CACHE_STORAGE_CLASS_DATA1",
		"LOCAL_CACHE_STORAGE_CLASS_DATA2",
		"LOCAL_CACHE_MOUNT_PATH_DATA1",
		"LOCAL_CACHE_MOUNT_PATH_DATA2",
		"LOCAL_CACHE_SIZE",
		"LOCAL_CACHE_ALLOWED_SIZES",
		"LOCAL_CACHE_MAX_SIZE",
	} {
		if !strings.Contains(template, "name: "+name) {
			t.Fatalf("backend deployment must wire %s", name)
		}
	}
}
