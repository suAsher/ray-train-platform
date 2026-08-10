package rayapi

import (
	"strings"
	"testing"
)

func TestParsePackageNameRejectsStaleRay40HexTransportName(t *testing.T) {
	if _, err := ParsePackageName("gcs", "_ray_pkg_"+strings.Repeat("a", 40)+".zip"); err == nil {
		t.Fatal("accepted stale 40-hex Ray transport package name")
	}
}
