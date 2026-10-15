package config

import (
	"os"
	"strings"
	"testing"
)

func TestPersonalStorageBuildIsExplicitOnly(t *testing.T) {
	content, err := os.ReadFile("../../build-image.sh")
	if err != nil {
		t.Fatal(err)
	}
	script := string(content)
	if !strings.Contains(script, "images/personal-storage-csi/Dockerfile|ray-personal-storage-csi|.|-") {
		t.Fatal("missing standalone personal CSI build target")
	}
	start := strings.Index(script, "normalize_targets() {")
	end := strings.Index(script[start:], "IFS=','")
	if start < 0 || end < 0 {
		t.Fatal("missing default build target expansion")
	}
	if strings.Contains(script[start:start+end], "personal-storage-csi") {
		t.Fatal("personal CSI must require an explicit build target")
	}
}
