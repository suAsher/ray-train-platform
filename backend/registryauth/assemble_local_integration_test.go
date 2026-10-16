package registryauth

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/layout"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
)

// Verify real, locally built OCI material without publishing a candidate image.
// The same tar validation and assembly code runs in the production assembler.
func TestEnvironmentLayerOCIIntegration(t *testing.T) {
	basePath := os.Getenv("ENVIRONMENT_ASSEMBLY_BASE_LAYOUT")
	if basePath == "" {
		t.Skip("ENVIRONMENT_ASSEMBLY_BASE_LAYOUT is not set")
	}
	ctx := context.Background()
	baseDigest, err := v1.NewHash(os.Getenv("ENVIRONMENT_ASSEMBLY_BASE_DIGEST"))
	if err != nil {
		t.Fatal(err)
	}
	baseLayout, err := layout.FromPath(basePath)
	if err != nil {
		t.Fatal(err)
	}
	base, err := baseLayout.Image(baseDigest)
	if err != nil {
		t.Fatal(err)
	}
	layerPath := os.Getenv("ENVIRONMENT_ASSEMBLY_LAYER")
	if err := validateEnvironmentTar(ctx, layerPath); err != nil {
		t.Fatal(err)
	}
	material, err := os.ReadFile(filepath.Join(filepath.Dir(layerPath), "build-result.json"))
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		LayerDigest string `json:"layerSha256"`
	}
	if err := json.Unmarshal(material, &result); err != nil {
		t.Fatal(err)
	}
	if err := validateLayerMaterial(ctx, layerPath, result.LayerDigest); err != nil {
		t.Fatal(err)
	}
	image, err := appendEnvironment(base, layerPath)
	if err != nil {
		t.Fatal(err)
	}
	artifactDigest, err := writeEnvironmentLayout(ctx, image, os.Getenv("ENVIRONMENT_ASSEMBLY_RESULT_LAYOUT"))
	if err != nil {
		t.Fatal(err)
	}
	tag, err := name.NewTag("rtp-environment-assembled:integration")
	if err != nil {
		t.Fatal(err)
	}
	if err := tarball.WriteToFile(os.Getenv("ENVIRONMENT_ASSEMBLY_DOCKER_TAR"), tag, image); err != nil {
		t.Fatal(err)
	}
	t.Logf("assembled artifact digest: %s", artifactDigest)
}
