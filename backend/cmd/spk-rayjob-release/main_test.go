package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBuildManifestMatchesArtifacts(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"spk-rayjob-linux-amd64", "spk-rayjob-darwin-arm64", "spk-rayjob-windows-amd64.exe"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("payload"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	m, err := buildManifest(dir, "release-20260906-02", "", "Notes")
	if err != nil || m.MinimumVersion != "" || len(m.Artifacts) != 3 || m.Artifacts[0].Size != 7 || m.Artifacts[0].SHA256 != "239f59ed55e737c77147cf55ad0c1b030b6d7ee748a7426952f9b852d5a935e5" {
		t.Fatalf("%+v %v", m, err)
	}
	if _, err := buildManifest(dir, "dev", "", ""); err == nil {
		t.Fatal("accepted development release")
	}
	if _, err := buildManifest(t.TempDir(), "release-20260906-01", "", ""); err == nil {
		t.Fatal("accepted missing artifacts")
	}
}
