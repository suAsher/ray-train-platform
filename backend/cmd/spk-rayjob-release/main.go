// Command spk-rayjob-release writes the validated manifest for published binaries.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"ray-train-platform-backend/spkrayjob"
)

func buildManifest(directory, version, minimum, notes string) (spkrayjob.ReleaseManifest, error) {
	manifest := spkrayjob.ReleaseManifest{SchemaVersion: 1, LatestVersion: version, MinimumVersion: minimum, ReleaseNotes: notes}
	for _, target := range []struct{ os, arch, filename string }{{"linux", "amd64", "spk-rayjob-linux-amd64"}, {"darwin", "arm64", "spk-rayjob-darwin-arm64"}, {"windows", "amd64", "spk-rayjob-windows-amd64.exe"}} {
		data, err := os.ReadFile(filepath.Join(directory, target.filename))
		if err != nil {
			return manifest, err
		}
		digest := sha256.Sum256(data)
		manifest.Artifacts = append(manifest.Artifacts, spkrayjob.ReleaseArtifact{OS: target.os, Arch: target.arch, Filename: target.filename, SHA256: hex.EncodeToString(digest[:]), Size: int64(len(data))})
	}
	return manifest, manifest.Validate()
}

func main() {
	directory := flag.String("dir", "/out", "artifact directory")
	version := flag.String("version", "", "release version")
	minimum := flag.String("minimum-version", "", "minimum supported version; empty disables enforcement")
	notes := flag.String("release-notes", "", "release notes")
	flag.Parse()
	manifest, err := buildManifest(*directory, *version, *minimum, *notes)
	if err == nil {
		err = json.NewEncoder(os.Stdout).Encode(manifest)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
