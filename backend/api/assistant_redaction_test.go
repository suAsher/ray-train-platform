package api

import (
	"strings"
	"testing"
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
