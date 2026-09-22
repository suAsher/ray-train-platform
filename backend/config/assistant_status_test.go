package config

import "testing"

func TestAssistantIdleNamespaceBoundary(t *testing.T) {
	for _, value := range []string{"", "raytrain-assistant-canary", "raytrain-assistant-idle"} {
		if err := ValidateAssistantIdleNamespace(value); err != nil {
			t.Fatalf("valid namespace %q: %v", value, err)
		}
	}
	for _, value := range []string{"default", "tenant-team", "raytrain-assistant-", "raytrain-assistant-../../x", "raytrain-assistant-X", "raytrain-assistant-a.svc"} {
		if ValidateAssistantIdleNamespace(value) == nil {
			t.Fatalf("accepted namespace %q", value)
		}
	}
}
