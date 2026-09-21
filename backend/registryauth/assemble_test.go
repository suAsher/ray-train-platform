package registryauth

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
)

func writeEnvironmentTar(t *testing.T, headers []*tar.Header) string {
	t.Helper()
	file, err := os.Create(filepath.Join(t.TempDir(), "layer.tar"))
	if err != nil {
		t.Fatal(err)
	}
	writer := tar.NewWriter(file)
	for _, header := range headers {
		if err := writer.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	return file.Name()
}

func TestEnvironmentTarRejectsEscapeAndPrivilegedEntries(t *testing.T) {
	for _, header := range []*tar.Header{
		{Name: "../../etc/shadow", Typeflag: tar.TypeReg, Mode: 0644},
		{Name: "/opt/raytrain/environment/config", Typeflag: tar.TypeReg, Mode: 0644},
		{Name: "opt/raytrain/environment/../escape", Typeflag: tar.TypeReg, Mode: 0644},
		{Name: "etc/ld.so.preload", Typeflag: tar.TypeReg, Mode: 0644},
		{Name: "opt/raytrain/environment/setuid", Typeflag: tar.TypeReg, Mode: 04755},
		{Name: "opt/raytrain/environment/device", Typeflag: tar.TypeChar, Mode: 0644},
		{Name: "opt/raytrain/environment/link", Typeflag: tar.TypeLink, Linkname: "etc/shadow", Mode: 0644},
		{Name: "opt/raytrain/environment/link", Typeflag: tar.TypeSymlink, Linkname: "../../etc", Mode: 0777},
		{Name: "opt/raytrain/environment/.wh.bin", Typeflag: tar.TypeReg, Mode: 0644},
	} {
		t.Run(header.Name, func(t *testing.T) {
			header.Uid = 0
			header.Gid = 0
			if validateEnvironmentTar(context.Background(), writeEnvironmentTar(t, []*tar.Header{header})) == nil {
				t.Fatal("unsafe layer accepted")
			}
		})
	}
}

func TestEnvironmentTarAcceptsInternalVenvSymlinkAndRejectsWritesThroughIt(t *testing.T) {
	valid := []*tar.Header{
		{Name: "opt/raytrain/environment", Typeflag: tar.TypeDir, Mode: 0755, Uid: 0, Gid: 0},
		{Name: "opt/raytrain/environment/lib", Typeflag: tar.TypeDir, Mode: 0755, Uid: 0, Gid: 0},
		{Name: "opt/raytrain/environment/lib64", Typeflag: tar.TypeSymlink, Linkname: "lib", Mode: 0777, Uid: 0, Gid: 0},
	}
	if err := validateEnvironmentTar(context.Background(), writeEnvironmentTar(t, valid)); err != nil {
		t.Fatal(err)
	}
	invalid := append(append([]*tar.Header{}, valid...), &tar.Header{Name: "opt/raytrain/environment/lib64/write", Typeflag: tar.TypeReg, Mode: 0644, Uid: 0, Gid: 0})
	if validateEnvironmentTar(context.Background(), writeEnvironmentTar(t, invalid)) == nil {
		t.Fatal("write through symlink accepted")
	}
}

func TestAppendEnvironmentPreservesUserEntrypointAndProducesReadableOCI(t *testing.T) {
	filename := writeEnvironmentTar(t, []*tar.Header{{Name: environmentRoot, Typeflag: tar.TypeDir, Mode: 0755}})
	config, err := empty.Image.ConfigFile()
	if err != nil {
		t.Fatal(err)
	}
	config.Config.User = "ray"
	config.Config.WorkingDir = "/workspace"
	config.Config.Entrypoint = []string{"raytrain-launch"}
	config.Config.Env = []string{"PATH=/base/bin", "EXISTING=kept"}
	base, err := mutate.ConfigFile(empty.Image, config)
	if err != nil {
		t.Fatal(err)
	}
	image, err := appendEnvironment(base, filename)
	if err != nil {
		t.Fatal(err)
	}
	result, err := image.ConfigFile()
	if err != nil {
		t.Fatal(err)
	}
	if result.Config.User != "ray" || result.Config.WorkingDir != "/workspace" || len(result.Config.Entrypoint) != 1 || result.Config.Entrypoint[0] != "raytrain-launch" {
		t.Fatalf("base identity/entrypoint changed: %+v", result.Config)
	}
	if !strings.Contains(strings.Join(result.Config.Env, "\n"), "PATH=/opt/raytrain/environment-wrappers:/opt/raytrain/environment/bin:/base/bin") {
		t.Fatal("environment path not applied")
	}
	directory := filepath.Join(t.TempDir(), "oci")
	digest, err := writeEnvironmentLayout(context.Background(), image, directory)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := loadPublishImage(context.Background(), directory, digest); err != nil {
		t.Fatal(err)
	}
	if _, err := writeEnvironmentLayout(context.Background(), image, directory); err == nil {
		t.Fatal("existing artifact overwritten")
	}
}

func TestAppendEnvironmentRejectsRootBase(t *testing.T) {
	filename := writeEnvironmentTar(t, []*tar.Header{{Name: environmentRoot, Typeflag: tar.TypeDir, Mode: 0755}})
	if _, err := appendEnvironment(empty.Image, filename); err == nil {
		t.Fatal("root base accepted")
	}
}

func TestValidateLayerMaterialDetectsChangedLayerOrFrozenDigest(t *testing.T) {
	filename := writeEnvironmentTar(t, []*tar.Header{{Name: environmentRoot, Typeflag: tar.TypeDir, Mode: 0755}})
	content, err := os.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(content)
	digest := "sha256:" + hex.EncodeToString(hash[:])
	metadata, _ := json.Marshal(map[string]any{"schemaVersion": 1, "layerSha256": digest, "layerSizeBytes": len(content)})
	if err := os.WriteFile(filepath.Join(filepath.Dir(filename), "build-result.json"), metadata, 0600); err != nil {
		t.Fatal(err)
	}
	if err := validateLayerMaterial(context.Background(), filename, digest); err != nil {
		t.Fatal(err)
	}
	if err := validateLayerMaterial(context.Background(), filename, "sha256:"+strings.Repeat("0", 64)); err == nil {
		t.Fatal("wrong frozen digest accepted")
	}
	if err := os.WriteFile(filename, append(content, 1), 0600); err != nil {
		t.Fatal(err)
	}
	if err := validateLayerMaterial(context.Background(), filename, digest); err == nil {
		t.Fatal("modified layer accepted")
	}
}
