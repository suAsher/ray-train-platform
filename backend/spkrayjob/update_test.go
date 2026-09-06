package spkrayjob

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestUpgradeExecutableEndToEnd(t *testing.T) {
	if runtime.GOOS == "windows" || artifactFilename(runtime.GOOS, runtime.GOARCH) == "" {
		t.Skip("native replacement integration test runs on supported Unix hosts")
	}
	dir := t.TempDir()
	build := func(path, version string) {
		command := exec.Command("go", "build", "-ldflags", "-X ray-train-platform-backend/spkrayjob.Version="+version, "-o", path, "../cmd/spk-rayjob")
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("build release fixture: %v %s", err, output)
		}
	}
	target := filepath.Join(dir, "spk-rayjob")
	next := filepath.Join(dir, "next")
	build(target, "release-20260906-01")
	build(next, "release-20260906-02")
	payload, err := os.ReadFile(next)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(payload)
	manifest := releaseFixture()
	manifest.Artifacts = []ReleaseArtifact{{OS: runtime.GOOS, Arch: runtime.GOARCH, Filename: artifactFilename(runtime.GOOS, runtime.GOARCH), SHA256: hex.EncodeToString(digest[:]), Size: int64(len(payload))}}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("upgrade sent credentials")
		}
		if r.URL.Path == releasePath+"release.json" {
			json.NewEncoder(w).Encode(manifest)
			return
		}
		w.Write(payload)
	}))
	defer server.Close()
	config := filepath.Join(dir, "config.json")
	if err := writeConfig(config, configFile{Server: server.URL, Token: "secret"}); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(target, "upgrade", "--config", config, "--ca-file", writeTestCA(t, server))
	if output, err := command.CombinedOutput(); err != nil || !bytes.Contains(output, []byte("升级成功")) {
		t.Fatalf("upgrade: %v %s", err, output)
	}
	if output, err := exec.Command(target, "version").CombinedOutput(); err != nil || !bytes.Contains(output, []byte(manifest.LatestVersion)) {
		t.Fatalf("upgraded executable: %v %s", err, output)
	}
}

func TestReleaseCompatibilityPolicy(t *testing.T) {
	old := Version
	defer func() { Version = old }()
	m := releaseFixture()
	unavailable := false
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if unavailable {
			http.NotFound(w, r)
			return
		}
		json.NewEncoder(w).Encode(m)
	}))
	defer server.Close()
	client, _ := NewClient(ClientOptions{ServerURL: server.URL, Token: "secret", HTTPClient: server.Client()})
	for _, tt := range []struct {
		version                 string
		submit, blocked, notice bool
	}{
		{"release-20260905-01", true, true, false},
		{"release-20260905-01", false, false, true},
		{"release-20260906-01", true, false, true},
		{"release-20260906-02", true, false, false},
		{"release-20260907-01", true, false, false},
		{"dev", true, false, false},
	} {
		Version = tt.version
		var stderr bytes.Buffer
		err := client.checkRelease(context.Background(), &stderr, tt.submit)
		if (err != nil) != tt.blocked || strings.Contains(stderr.String(), "spk-rayjob upgrade") != tt.notice {
			t.Errorf("%+v err=%v stderr=%s", tt, err, stderr.String())
		}
	}
	Version = "release-20260905-01"
	m.MinimumVersion = ""
	if err := client.checkRelease(context.Background(), &bytes.Buffer{}, true); err != nil {
		t.Fatal(err)
	}
	unavailable = true
	if err := client.checkRelease(context.Background(), &bytes.Buffer{}, true); err != nil {
		t.Fatal("unavailable manifest blocks submission", err)
	}
}

func TestUpgradeCommandValidationAndCurrentVersion(t *testing.T) {
	old := Version
	defer func() { Version = old }()
	Version = "dev"
	if err := runUpgrade(context.Background(), nil, &bytes.Buffer{}, &bytes.Buffer{}); err == nil {
		t.Fatal("development executable accepted upgrade")
	}
	Version = "release-20260906-02"
	if err := runUpgrade(context.Background(), []string{"--server", "https://other.example"}, &bytes.Buffer{}, &bytes.Buffer{}); err == nil {
		t.Fatal("server override accepted")
	}
	config := filepath.Join(t.TempDir(), "config.json")
	if err := runUpgrade(context.Background(), []string{"--config", config}, &bytes.Buffer{}, &bytes.Buffer{}); err == nil {
		t.Fatal("missing config accepted")
	}
	writeConfig(config, configFile{Server: "http://insecure.example", Token: "secret"})
	if err := runUpgrade(context.Background(), []string{"--config", config}, &bytes.Buffer{}, &bytes.Buffer{}); err == nil {
		t.Fatal("insecure origin accepted")
	}
	m := releaseFixture()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { json.NewEncoder(w).Encode(m) }))
	defer server.Close()
	writeConfig(config, configFile{Server: server.URL, Token: "secret"})
	arguments := []string{"--config", config, "--ca-file", writeTestCA(t, server)}
	var stdout bytes.Buffer
	if err := runUpgrade(context.Background(), arguments, &stdout, &bytes.Buffer{}); err != nil || !strings.Contains(stdout.String(), "已是最新版本") {
		t.Fatalf("%v %s", err, stdout.String())
	}
	m.SchemaVersion = 2
	if err := runUpgrade(context.Background(), arguments, &bytes.Buffer{}, &bytes.Buffer{}); err == nil {
		t.Fatal("invalid schema accepted")
	}
	for _, target := range []struct{ os, arch, filename string }{{"linux", "amd64", "spk-rayjob-linux-amd64"}, {"darwin", "arm64", "spk-rayjob-darwin-arm64"}, {"windows", "amd64", "spk-rayjob-windows-amd64.exe"}, {"darwin", "amd64", ""}} {
		if got := artifactFilename(target.os, target.arch); got != target.filename {
			t.Errorf("%+v: %s", target, got)
		}
	}
}

func TestRunSubmitRejectsOldReleaseBeforeUpload(t *testing.T) {
	old := Version
	Version = "release-20260905-01"
	defer func() { Version = old }()
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "train.py"), []byte("print('train')"), 0600)
	requests := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != releasePath+"release.json" {
			t.Errorf("submission started before minimum check: %s", r.URL.Path)
		}
		json.NewEncoder(w).Encode(releaseFixture())
	}))
	defer server.Close()
	err := Run(context.Background(), []string{"submit", "--server", server.URL, "--ca-file", writeTestCA(t, server), "--dir", root, "--name", "upgrade-test", "--image", "harbor.example/train@sha256:" + strings.Repeat("a", 64), "--entrypoint", "python train.py"}, &bytes.Buffer{}, &bytes.Buffer{}, func(key string) string {
		if key == "SPK_RAYJOB_TOKEN" {
			return "secret"
		}
		return ""
	})
	if err == nil || !strings.Contains(err.Error(), "spk-rayjob upgrade") || requests != 1 {
		t.Fatalf("%v requests=%d", err, requests)
	}
}

func TestReleaseBoundsAndCredentialIsolation(t *testing.T) {
	for _, body := range []string{strings.Repeat("x", (64<<10)+1), "{}", "not JSON"} {
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(body)) }))
		client, _ := NewClient(ClientOptions{ServerURL: server.URL, Token: "secret", HTTPClient: server.Client()})
		if _, err := client.releaseManifest(context.Background()); err == nil {
			t.Error("invalid manifest accepted")
		}
		server.Close()
	}
	for _, payload := range []string{"short", "new executable plus", "bad executable"} {
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(payload)) }))
		client, _ := NewClient(ClientOptions{ServerURL: server.URL, Token: "secret", HTTPClient: server.Client()})
		target := filepath.Join(t.TempDir(), "spk-rayjob")
		os.WriteFile(target, []byte("old"), 0700)
		if client.installRelease(context.Background(), releaseFixture().Artifacts[0], target) == nil {
			t.Error("bad payload accepted")
		}
		b, _ := os.ReadFile(target)
		if string(b) != "old" {
			t.Error("old executable lost")
		}
		if _, err := os.Stat(target + ".upgrade-lock"); !os.IsNotExist(err) {
			t.Error("lock leaked")
		}
		server.Close()
	}
}

func releaseFixture() ReleaseManifest {
	payload := linuxAMD64Fixture()
	sum := sha256.Sum256(payload)
	return ReleaseManifest{SchemaVersion: 1, LatestVersion: "release-20260906-02", MinimumVersion: "release-20260906-01", Artifacts: []ReleaseArtifact{{OS: "linux", Arch: "amd64", Filename: "spk-rayjob-linux-amd64", SHA256: hex.EncodeToString(sum[:]), Size: int64(len(payload))}}}
}

func linuxAMD64Fixture() []byte {
	payload := make([]byte, 120)
	copy(payload, []byte("\x7fELF"))
	payload[4] = 2
	payload[5] = 1
	payload[6] = 1
	binary.LittleEndian.PutUint16(payload[16:18], 2)
	binary.LittleEndian.PutUint16(payload[18:20], 0x3e)
	binary.LittleEndian.PutUint32(payload[20:24], 1)
	binary.LittleEndian.PutUint64(payload[24:32], 0x400000)
	binary.LittleEndian.PutUint64(payload[32:40], 64)
	binary.LittleEndian.PutUint16(payload[52:54], 64)
	binary.LittleEndian.PutUint16(payload[54:56], 56)
	binary.LittleEndian.PutUint16(payload[56:58], 1)
	binary.LittleEndian.PutUint32(payload[64:68], 1)
	binary.LittleEndian.PutUint32(payload[68:72], 5)
	binary.LittleEndian.PutUint64(payload[80:88], 0x400000)
	binary.LittleEndian.PutUint64(payload[96:104], uint64(len(payload)))
	binary.LittleEndian.PutUint64(payload[104:112], uint64(len(payload)))
	return payload
}

func TestReleaseRejectsInvalidExecutableDespiteMatchingChecksum(t *testing.T) {
	for _, mutate := range []func([]byte) []byte{
		func(b []byte) []byte { return b[:20] },
		func(b []byte) []byte { binary.LittleEndian.PutUint16(b[16:18], 1); return b },
		func(b []byte) []byte { binary.LittleEndian.PutUint16(b[18:20], 183); return b },
		func(b []byte) []byte { b[5] = 2; return b },
		func(b []byte) []byte { return []byte("<html>release temporarily unavailable</html>") },
	} {
		payload := mutate(linuxAMD64Fixture())
		sum := sha256.Sum256(payload)
		artifact := releaseFixture().Artifacts[0]
		artifact.Size = int64(len(payload))
		artifact.SHA256 = hex.EncodeToString(sum[:])
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(payload) }))
		client, _ := NewClient(ClientOptions{ServerURL: server.URL, Token: "secret", HTTPClient: server.Client()})
		target := filepath.Join(t.TempDir(), "spk-rayjob")
		os.WriteFile(target, []byte("old"), 0700)
		if err := client.installRelease(context.Background(), artifact, target); err == nil {
			t.Error("invalid executable replaced target despite valid checksum")
		}
		b, _ := os.ReadFile(target)
		if string(b) != "old" {
			t.Error("old executable lost")
		}
		server.Close()
	}
}

func TestReleaseManifestValidation(t *testing.T) {
	m := releaseFixture()
	if err := m.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*ReleaseManifest){func(m *ReleaseManifest) { m.SchemaVersion = 2 }, func(m *ReleaseManifest) { m.MinimumVersion = "dev" }, func(m *ReleaseManifest) { m.MinimumVersion = "release-20260907-01" }, func(m *ReleaseManifest) { m.ReleaseNotes = strings.Repeat("x", 8001) }, func(m *ReleaseManifest) { m.Artifacts[0].Filename = "../evil" }, func(m *ReleaseManifest) { m.Artifacts[0].Size = 0 }, func(m *ReleaseManifest) { m.Artifacts[0].Size = 1 << 40 }, func(m *ReleaseManifest) { m.Artifacts[0].SHA256 = "bad" }} {
		m := releaseFixture()
		change(&m)
		if m.Validate() == nil {
			t.Fatalf("accepted invalid manifest: %+v", m)
		}
	}
}

func TestReleaseDownloadAndAtomicInstall(t *testing.T) {
	m := releaseFixture()
	payload := linuxAMD64Fixture()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("credential leaked")
		}
		if r.URL.Path == releasePath+"release.json" {
			json.NewEncoder(w).Encode(m)
			return
		}
		w.Write(payload)
	}))
	defer server.Close()
	client, err := NewClient(ClientOptions{ServerURL: server.URL, Token: "secret", HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	got, err := client.releaseManifest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "spk-rayjob")
	os.WriteFile(target, []byte("old"), 0700)
	if err := client.installRelease(context.Background(), got.Artifacts[0], target); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(target)
	if !bytes.Equal(b, payload) {
		t.Fatalf("%s", b)
	}
	got.Artifacts[0].SHA256 = string(make([]byte, 64))
	if client.installRelease(context.Background(), got.Artifacts[0], target) == nil {
		t.Fatal("accepted corrupt artifact")
	}
	b, _ = os.ReadFile(target)
	if !bytes.Equal(b, payload) {
		t.Fatal("old executable lost")
	}
	if runtime.GOOS != "windows" {
		backups, err := filepath.Glob(target + ".previous.*")
		if err != nil || len(backups) != 1 {
			t.Fatalf("old executable backup missing: %v %v", backups, err)
		}
		backup, _ := os.ReadFile(backups[0])
		if string(backup) != "old" {
			t.Fatal("backup does not contain the old executable")
		}
	}
}

func TestReleaseRejectsWrongPlatformBinary(t *testing.T) {
	payload := []byte("MZ not a linux executable")
	sum := sha256.Sum256(payload)
	m := releaseFixture()
	m.Artifacts[0].SHA256 = hex.EncodeToString(sum[:])
	m.Artifacts[0].Size = int64(len(payload))
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(payload)
	}))
	defer server.Close()
	client, _ := NewClient(ClientOptions{ServerURL: server.URL, Token: "secret", HTTPClient: server.Client()})
	target := filepath.Join(t.TempDir(), "spk-rayjob")
	os.WriteFile(target, []byte("old"), 0700)
	if err := client.installRelease(context.Background(), m.Artifacts[0], target); err == nil || !strings.Contains(err.Error(), "linux/amd64") {
		t.Fatalf("wrong-platform binary accepted: %v", err)
	}
	b, _ := os.ReadFile(target)
	if string(b) != "old" {
		t.Fatal("old executable lost")
	}
}

func TestReleaseRejectsRedirectAndConcurrentUpgrade(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "http://example.org/payload", 302) }))
	defer server.Close()
	client, _ := NewClient(ClientOptions{ServerURL: server.URL, Token: "secret", HTTPClient: server.Client()})
	if _, err := client.releaseManifest(context.Background()); err == nil {
		t.Fatal("accepted redirect")
	}
	target := filepath.Join(t.TempDir(), "spk-rayjob")
	os.WriteFile(target, []byte("old"), 0700)
	os.Mkdir(target+".upgrade-lock", 0700)
	if err := client.installRelease(context.Background(), releaseFixture().Artifacts[0], target); err == nil {
		t.Fatal("ignored lock")
	}
}
