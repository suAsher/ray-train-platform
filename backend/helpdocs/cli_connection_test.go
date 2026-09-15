package helpdocs

import (
	"strings"
	"testing"
)

func TestCLIHelpExplainsCanonicalServerAndLocalVersion(t *testing.T) {
	documents, err := Documents()
	if err != nil {
		t.Fatal(err)
	}
	bodies := map[string]string{}
	for _, document := range documents {
		bodies[document.ID] = document.Markdown
	}
	for _, marker := range []string{
		"https://raytrain.wellspiking.ai",
		"spiking.wellspiking.ai",
		"--help 来自本机",
		"type -a spk-rayjob",
		"hash -r",
		"Get-Command spk-rayjob -All",
		"spk-rayjob upgrade --server 'https://raytrain.wellspiking.ai'",
		"--token-stdin",
	} {
		if !strings.Contains(bodies["cli-onboarding-v2"], marker) {
			t.Errorf("CLI onboarding missing %q", marker)
		}
	}
	if strings.Contains(bodies["cli-onboarding-v2"], "spk-rayjob upgrade\n") {
		t.Error("upgrade must explicitly use the CLI service, even with an old saved server")
	}
	if strings.Contains(bodies["command-recipes"], "REPLACE_PLATFORM_HOST") {
		t.Error("command recipes must not ask Portal users to guess the CLI/Ray service host")
	}
	for _, guide := range PublicGuides() {
		if guide.ID == "quickstart" {
			for _, marker := range []string{"--help 来自本机", "type -a spk-rayjob", "hash -r", "spiking.wellspiking.ai"} {
				if !strings.Contains(guide.Markdown, marker) {
					t.Errorf("public CLI quickstart missing %q", marker)
				}
			}
		}
	}
}
