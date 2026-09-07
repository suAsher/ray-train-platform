package config

import (
	"os"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestNodeOnboardingOptInGateAlignsCapacityAndKueue(t *testing.T) {
	raw, err := os.ReadFile("../../deploy/overlays/node-onboarding-ready-gate.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var overlay struct {
		Training struct {
			NodeSelector string `yaml:"nodeSelector"`
		} `yaml:"training"`
		Kueue struct {
			AutoQuota bool              `yaml:"autoQuota"`
			Labels    map[string]string `yaml:"resourceFlavorNodeLabels"`
		} `yaml:"kueue"`
	}
	if err := yaml.Unmarshal(raw, &overlay); err != nil {
		t.Fatal(err)
	}
	selector := map[string]string{}
	for _, item := range strings.Split(overlay.Training.NodeSelector, ",") {
		pair := strings.SplitN(item, "=", 2)
		if len(pair) != 2 {
			t.Fatalf("invalid selector %q", item)
		}
		selector[pair[0]] = pair[1]
	}
	if !overlay.Kueue.AutoQuota || selector["platform.wellspiking.ai/cache-ready"] != "true" || !reflect.DeepEqual(selector, overlay.Kueue.Labels) {
		t.Fatalf("capacity and scheduling gates must match: %#v", overlay)
	}
	if strings.Contains(string(raw), "digest:") || strings.Contains(string(raw), "image:") {
		t.Fatal("gate overlay must not pin or roll back images")
	}
}
