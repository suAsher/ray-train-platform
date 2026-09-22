package api

import (
	"strings"
	"testing"

	"ray-train-platform-backend/assistant"
)

func TestAssistantRedactionPreservesPaginationCode(t *testing.T) {
	for _, name := range []string{"page_token", "run_token", "experiment_token"} {
		t.Run(name, func(t *testing.T) {
			input := name + " = None\npage = client.search_runs(page_token=" + name + ")\n" + name + " = page.token\nif not " + name + ":\n    break\n"
			if got := assistantRedact(input); got != input {
				t.Fatalf("pagination code changed:\n%s", got)
			}
		})
	}
}

func TestAssistantSourceMarkupPreservesCodeOperatorsAndPlaceholders(t *testing.T) {
	code := "```python\nif rank < 0 or rank >= world_size:\n    raise ValueError(\"invalid rank\")\nhandlers[i](value)\n```\n\n~~~bash\nspk-rayjob submit --image '<REGISTERED_IMAGE>'\n~~~\n\nUse `left < right && upper > lower` and `<PROJECT>/<IMAGE>`."
	input := "<b>说明</b> [入口](/job)\n\n" + code
	want := "说明 入口\n\n" + code
	for name, transform := range map[string]func(string) string{
		"published": func(s string) string { return assistantPublishedText(s, 8000) },
		"model":     func(s string) string { return assistantGroundedText(s, 8000, []assistant.Evidence{}) },
	} {
		t.Run(name, func(t *testing.T) {
			if got := transform(input); got != want {
				t.Fatalf("markup sanitization changed executable code:\n%s", got)
			}
			secret := transform("```python\npassword='private-value'\n# https://untrusted.invalid/install.sh\n```")
			if strings.Contains(secret, "private-value") || strings.Contains(secret, "untrusted.invalid") {
				t.Fatal("code formatting bypassed credential or URL sanitization")
			}
		})
	}
}

func TestAssistantRedactionStillRemovesPaginationLiteralCredentials(t *testing.T) {
	for _, input := range []string{
		`page_token="private-value"`, `run_token='private-value'`,
		`experiment_token=private-value`, `{"page_token":"private-value"}`,
		`api_token=private-value`, `auth_token=private-value`,
		"password=\nprivate-value", "password='private-value\nremaining-value",
		`page_token="private-value`, `page_token='private-value`,
	} {
		got := assistantRedact(input)
		if strings.Contains(got, "private-value") || strings.Contains(got, "remaining-value") || !strings.Contains(got, "[已脱敏]") {
			t.Errorf("credential survived: %q", got)
		}
	}
}
