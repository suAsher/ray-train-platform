package k8s

import (
	"os/exec"
	"strconv"
	"strings"
	"testing"

	"ray-train-platform-backend/domain"
)

func TestSiteGuardExecutesOnlyCompatibleRuntime(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 unavailable")
	}
	for _, test := range []struct {
		name     string
		protocol int
		fields   string
		ok       bool
	}{
		{"old-image", 0, "{}", false},
		{"partial-upgrade", 1, "{}", false},
		{"site-aware", 1, `{"sites": None}`, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			// Install controlled fixture modules directly in this isolated test
			// interpreter. Production has no prefix and loads installed modules.
			setup := "import sys, types\n" +
				"pkg=types.ModuleType('raytrain_runtime'); sys.modules['raytrain_runtime']=pkg\n" +
				"driver=types.ModuleType('raytrain_runtime.managed_driver'); driver.SITE_SELECTION_PROTOCOL=" + strconv.Itoa(test.protocol) + "; pkg.managed_driver=driver\n" +
				"data=types.ModuleType('raytrain_runtime.ray_data'); data.StreamingDatasetConfig=type('Config',(),{'__dataclass_fields__':" + test.fields + "}); sys.modules['raytrain_runtime.ray_data']=data\n"
			output, err := exec.Command(python, "-I", "-c", setup+streamingSiteRuntimeGuard, python, "-I", "-c", "print('TRAINING_STARTED')").CombinedOutput()
			if test.ok {
				if err != nil || !strings.Contains(string(output), "TRAINING_STARTED") {
					t.Fatalf("compatible runtime blocked: %s %v", output, err)
				}
			} else if err == nil || strings.Contains(string(output), "TRAINING_STARTED") || !strings.Contains(string(output), "upgrade the training image") {
				t.Fatalf("old runtime did not fail closed: %s %v", output, err)
			}
		})
	}
}

func TestSiteSelectionAddsTrustedGuardOnlyWhenNeeded(t *testing.T) {
	spec := streamingManifestJob().Spec
	legacy := trainingEntrypoint(spec)
	if legacy[0] != "raytrain-managed" {
		t.Fatal("legacy whole-version launch changed")
	}
	spec.DatasetRef.Sites, _ = domain.NewDatasetSites([]string{"site-a"})
	guarded := trainingEntrypoint(spec)
	if len(guarded) != len(legacy)+4 || guarded[0] != "python" || guarded[1] != "-I" || guarded[3] != streamingSiteRuntimeGuard || guarded[4] != "raytrain-managed" {
		t.Fatalf("site-scoped launch missing immutable guard: %q", guarded)
	}
}
