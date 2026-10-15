package config

import (
	"bytes"
	"fmt"
	"gopkg.in/yaml.v3"
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"text/template"
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
	if start < 0 {
		t.Fatal("missing default build target expansion")
	}
	end := strings.Index(script[start:], "IFS=','")
	if end < 0 {
		t.Fatal("missing default build target expansion")
	}
	if strings.Contains(script[start:start+end], "personal-storage-csi") {
		t.Fatal("personal CSI must require an explicit build target")
	}
}

// Exercise the chart's actual personal-storage activation block. Full-chart
// rendering and Kubernetes server-side dry-run remain separate release gates.
func renderPersonalStorageActivation(t *testing.T, personal map[string]any) error {
	t.Helper()
	raw, err := os.ReadFile("../../helm/ray-train-platform/templates/backend-deployment.yaml")
	if err != nil {
		return err
	}
	contents := string(raw)
	start := strings.Index(contents, "{{- $personalStorage :=")
	end := strings.Index(contents, "{{- $idcDataSpaces :=")
	if start < 0 || end <= start {
		return fmt.Errorf("personal storage activation block missing")
	}
	funcs := template.FuncMap{
		"dict": func() map[string]any { return map[string]any{} }, "list": func(values ...any) []any { return values },
		"default": func(fallback, value any) any {
			if value == nil || reflect.ValueOf(value).IsZero() {
				return fallback
			}
			return value
		},
		"kindIs": func(kind string, value any) bool {
			return value != nil && reflect.TypeOf(value).Kind().String() == kind
		},
		"hasKey": func(values map[string]any, key string) bool { _, ok := values[key]; return ok },
		"uniq": func(values []any) []any {
			out := []any{}
			for _, value := range values {
				found := false
				for _, existing := range out {
					if reflect.DeepEqual(value, existing) {
						found = true
						break
					}
				}
				if !found {
					out = append(out, value)
				}
			}
			return out
		},
		"regexMatch": func(pattern, value string) bool { return regexp.MustCompile(pattern).MatchString(value) },
		"fail":       func(message string) (string, error) { return "", fmt.Errorf("%s", message) },
	}
	parsed, err := template.New("personal-activation").Funcs(funcs).Parse(contents[start:end])
	if err != nil {
		return err
	}
	return parsed.Execute(&bytes.Buffer{}, map[string]any{"Values": map[string]any{"personalStorage": personal}})
}
func TestPersonalStorageHelmAllowsOnlyAttestedGlobalOrNamedTestOwners(t *testing.T) {
	for _, tc := range []struct {
		name     string
		verified bool
		owners   any
		allowed  bool
	}{
		{name: "no attestation or owners", owners: nil},
		{name: "empty allowlist", owners: []any{}},
		{name: "blank owner", owners: []any{""}},
		{name: "string is not owner list", owners: "alice,bob"},
		{name: "nonstring owner", owners: []any{123}},
		{name: "invalid owner", owners: []any{"../bob"}},
		{name: "two explicit test accounts", owners: []any{"stable-alice", "stable-bob"}, allowed: true},
		{name: "opaque prefixed owner", owners: []any{"provider:stable-alice"}, allowed: true},
		{name: "overlong owner", owners: []any{strings.Repeat("a", 257)}},
		{name: "duplicate owner", owners: []any{"stable-alice", "stable-alice"}},
		{name: "global attestation", verified: true, owners: []any{}, allowed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			personal := map[string]any{"enabled": true, "personalRootIsolationVerified": tc.verified, "testOwnerIDs": tc.owners, "host": "mount.wellspiking.ai", "existingKnownHostsSecret": "personal-hostkeys", "csi": map[string]any{"enabled": true, "image": map[string]any{"digest": "sha256:" + strings.Repeat("a", 64)}}}
			err := renderPersonalStorageActivation(t, personal)
			if (err == nil) != tc.allowed {
				t.Fatalf("activation allowed=%v, want=%v: %v", err == nil, tc.allowed, err)
			}
		})
	}
}
func TestPersonalStorageHelmTestOwnersDefaultEmptyAndReachBackend(t *testing.T) {
	raw, err := os.ReadFile("../../helm/ray-train-platform/values.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var values map[string]any
	if err = yaml.Unmarshal(raw, &values); err != nil {
		t.Fatal(err)
	}
	personal := values["personalStorage"].(map[string]any)
	owners, ok := personal["testOwnerIDs"].([]any)
	if !ok || len(owners) != 0 || personal["enabled"] != false || personal["personalRootIsolationVerified"] != false {
		t.Fatalf("test mode must be disabled and unassigned by default: %#v", personal)
	}
	raw, err = os.ReadFile("../../helm/ray-train-platform/templates/backend-deployment.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "- name: PERSONAL_STORAGE_TEST_OWNER_IDS") || !strings.Contains(string(raw), `join "," $personalTestOwnerIDs`) {
		t.Fatal("explicit test owner IDs must be passed to backend unchanged")
	}
}
