package config

import (
	"os"
	"regexp"
	"testing"
)

// Code snapshot verification uses os.CreateTemp, which follows TMPDIR. Keep its
// writable, bounded spool separate from Ray package uploads and the image root.
func TestEvaluationCodeSpoolChartContract(t *testing.T) {
	chart, err := os.ReadFile("../../helm/ray-train-platform/templates/backend-deployment.yaml")
	if err != nil {
		t.Fatal(err)
	}
	checks := []struct {
		name    string
		pattern string
	}{
		{"temporary directory", `(?m)^            - name: TMPDIR\r?\n              value: ["']?/var/lib/ray-platform/code-snapshots["']?\s*$`},
		{"code snapshot mount", `(?m)^            - name: code-snapshot-spool\r?\n              mountPath: ["']?/var/lib/ray-platform/code-snapshots["']?\s*$`},
		{"bounded independent volume", `(?m)^        - name: code-snapshot-spool\r?\n          emptyDir:\r?\n            sizeLimit: ["']?1Gi["']?\s*$`},
		{"non-root writable volume group", `(?m)^        fsGroup: 65532\s*$`},
		{"non-root process", `(?m)^        runAsNonRoot: true\s*$`},
		{"read-only image root", `(?m)^            readOnlyRootFilesystem: true\s*$`},
		{"Ray upload volume retained", `(?m)^        - name: ray-api-spool\r?\n          emptyDir:\r?\n            sizeLimit: ["']?3Gi["']?\s*$`},
	}
	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			if !regexp.MustCompile(check.pattern).Match(chart) {
				t.Errorf("backend chart must preserve %s", check.name)
			}
		})
	}
}
