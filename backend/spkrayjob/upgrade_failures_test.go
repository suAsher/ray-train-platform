package spkrayjob

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSelfUpgradePlatformReleaseGate(t *testing.T) {
	if selfUpgradeReleased("windows") {
		t.Fatal("unverified Windows replacement must not ship enabled")
	}
	if !selfUpgradeReleased("linux") || !selfUpgradeReleased("darwin") {
		t.Fatal("verified Unix upgrade disabled")
	}
}

func TestUpgradeUnavailablePlatformDoesNotReplaceRunningProgram(t *testing.T) {
	old := Version
	Version = "release-20260906-01"
	defer func() { Version = old }()
	m := releaseFixture()
	if runtime.GOOS == "linux" {
		m.Artifacts[0].OS = "darwin"
		m.Artifacts[0].Arch = "arm64"
		m.Artifacts[0].Filename = "spk-rayjob-darwin-arm64"
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { json.NewEncoder(w).Encode(m) }))
	defer server.Close()
	config := filepath.Join(t.TempDir(), "config.json")
	if err := writeConfig(config, configFile{Server: server.URL, Token: "fixture"}); err != nil {
		t.Fatal(err)
	}
	err := runUpgrade(context.Background(), []string{"--config", config, "--ca-file", writeTestCA(t, server)}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "没有可用") {
		t.Fatalf("expected unsupported platform: %v", err)
	}
	// Matching metadata with the wrong response body must fail before replacing
	// the running test executable; this covers command-to-installer wiring.
	m.Artifacts[0].OS = runtime.GOOS
	m.Artifacts[0].Arch = runtime.GOARCH
	m.Artifacts[0].Filename = artifactFilename(runtime.GOOS, runtime.GOARCH)
	err = runUpgrade(context.Background(), []string{"--config", config, "--ca-file", writeTestCA(t, server)}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "原程序已保留") {
		t.Fatalf("invalid update reached replacement: %v", err)
	}
}

func TestUpgradeFailurePreservesTargetAndRemovesTemporaryFiles(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		length string
	}{{"unavailable", 503, "", ""}, {"bad-size", 200, "bad", "3"}, {"bad-checksum", 200, string(make([]byte, len(linuxAMD64Fixture()))), ""}} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.length != "" {
					w.Header().Set("Content-Length", tc.length)
				}
				w.WriteHeader(tc.status)
				w.Write([]byte(tc.body))
			}))
			defer server.Close()
			client, err := NewClient(ClientOptions{ServerURL: server.URL, Token: "fixture", HTTPClient: server.Client()})
			if err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			target := filepath.Join(dir, "spk")
			if err := os.WriteFile(target, []byte("original"), 0700); err != nil {
				t.Fatal(err)
			}
			if err := client.installRelease(context.Background(), releaseFixture().Artifacts[0], target); err == nil {
				t.Fatal("invalid download accepted")
			}
			b, err := os.ReadFile(target)
			if err != nil || string(b) != "original" {
				t.Fatal("original lost")
			}
			entries, err := os.ReadDir(dir)
			if err != nil || len(entries) != 1 {
				t.Fatalf("temporary files leaked: %v %v", entries, err)
			}
		})
	}
}

func TestUpgradeInvalidLocalTargets(t *testing.T) {
	client := &Client{}
	a := releaseFixture().Artifacts[0]
	for _, target := range []string{filepath.Join(t.TempDir(), "missing"), t.TempDir()} {
		if err := client.installRelease(context.Background(), a, target); err == nil {
			t.Fatal("invalid target accepted")
		}
	}
	a.Size = 0
	if err := client.installRelease(context.Background(), a, "unused"); err == nil {
		t.Fatal("invalid artifact accepted")
	}
	if err := checkUpgradeBackup(filepath.Join(t.TempDir(), "unused"), "windows"); err != nil {
		t.Fatal(err)
	}
	if err := verifyReleaseArtifactBinary("missing", ReleaseArtifact{OS: "unsupported"}); err == nil {
		t.Fatal("unsupported platform accepted")
	}
	for _, system := range []string{"linux", "darwin", "windows"} {
		if err := verifyReleaseArtifactBinary("missing", ReleaseArtifact{OS: system, Arch: map[string]string{"linux": "amd64", "darwin": "arm64", "windows": "amd64"}[system]}); err == nil {
			t.Fatal("missing executable accepted")
		}
	}
	m := releaseFixture()
	m.ReleaseNotes = strings.Repeat("x", 8001)
	if m.Validate() == nil {
		t.Fatal("oversized notes accepted")
	}
}

func TestVersionedClientArtifactCompletionCompatibility(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		ok     bool
	}{
		{"ready", 200, `{"success":true,"data":{"id":"fixture","state":"READY"}}`, true},
		{"bad-response", 200, `{"success":true,"data":"not-an-artifact"}`, false},
		{"unavailable", 503, `{"success":false}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" || r.URL.Path != "/api/v1/source-artifacts/fixture/complete" {
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
				}
				if r.Header.Get("X-Spk-Rayjob-Version") != Version {
					t.Error("version identity missing")
				}
				w.WriteHeader(tc.status)
				w.Write([]byte(tc.body))
			}))
			defer server.Close()
			client, err := NewClient(ClientOptions{ServerURL: server.URL, Token: "fixture", HTTPClient: server.Client()})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := client.CompleteArtifact(context.Background(), ""); err == nil {
				t.Fatal("empty id accepted")
			}
			result, err := client.CompleteArtifact(context.Background(), "fixture")
			if (err == nil) != tc.ok {
				t.Fatalf("result=%+v error=%v", result, err)
			}
		})
	}
}
