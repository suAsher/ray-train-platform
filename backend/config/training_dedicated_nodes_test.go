package config

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseTrainingDedicatedNodes(t *testing.T) {
	for _, raw := range []string{"", "  ", "{}"} {
		nodes, err := parseTrainingDedicatedNodes(raw)
		if err != nil || nodes == nil || len(nodes) != 0 {
			t.Fatalf("empty configuration should preserve shared placement: nodes=%v, err=%v", nodes, err)
		}
	}
	want := map[string][]string{
		"algorithm": {"172.28.3.32", "worker-a.example.internal"},
		"team-b":    {"worker-b"},
	}
	got, err := parseTrainingDedicatedNodes(`{"algorithm":["172.28.3.32","worker-a.example.internal"],"team-b":["worker-b"]}`)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("valid dedicated nodes: got=%v, err=%v", got, err)
	}
}

func TestParseTrainingDedicatedNodesRejectsInvalidConfig(t *testing.T) {
	cases := map[string]string{
		"malformed":             `{"algorithm":`,
		"array root":            `[]`,
		"null root":             `null`,
		"string root":           `"algorithm"`,
		"trailing JSON":         `{} {}`,
		"trailing text":         `{} hidden-secret`,
		"empty tenant":          `{"": ["node-a"]}`,
		"invalid tenant":        `{"Team/A": ["node-a"]}`,
		"spaced tenant":         `{" algorithm": ["node-a"]}`,
		"long tenant":           `{"` + strings.Repeat("a", 64) + `": ["node-a"]}`,
		"empty nodes":           `{"algorithm": []}`,
		"null nodes":            `{"algorithm": null}`,
		"scalar nodes":          `{"algorithm": "node-a"}`,
		"empty node":            `{"algorithm": [""]}`,
		"null node":             `{"algorithm": [null]}`,
		"number node":           `{"algorithm": [32]}`,
		"invalid node":          `{"algorithm": ["NODE_A"]}`,
		"spaced node":           `{"algorithm": ["node-a "]}`,
		"long node":             `{"algorithm": ["` + strings.Repeat("a", 254) + `"]}`,
		"duplicate node":        `{"algorithm": ["node-a", "node-a"]}`,
		"cross-team duplicate":  `{"algorithm": ["node-a"], "other": ["node-a"]}`,
		"duplicate tenant key":  `{"algorithm": ["node-a"], "algorithm": ["node-b"]}`,
		"sensitive invalid key": `{"hidden-secret/token": ["node-a"]}`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := parseTrainingDedicatedNodes(raw)
			if err == nil {
				t.Fatal("invalid dedicated-node configuration was accepted")
			}
			if !strings.Contains(err.Error(), "TRAINING_DEDICATED_NODES") || strings.Contains(err.Error(), "hidden-secret") {
				t.Fatalf("error should identify the setting without echoing its contents: %v", err)
			}
		})
	}
}

func TestLoadTrainingDedicatedNodes(t *testing.T) {
	t.Setenv("PAT_PEPPER", testPATPepper)
	t.Setenv("TRAINING_DEDICATED_NODES", `{"algorithm":["172.28.3.32"]}`)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cfg.TrainingDedicatedNodes, map[string][]string{"algorithm": {"172.28.3.32"}}) {
		t.Fatalf("dedicated nodes not loaded: %v", cfg.TrainingDedicatedNodes)
	}
	t.Setenv("TRAINING_DEDICATED_NODES", `{"algorithm":[]}`)
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "TRAINING_DEDICATED_NODES") {
		t.Fatalf("invalid deployment policy must prevent startup: %v", err)
	}
}
