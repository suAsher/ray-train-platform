package k8s

import (
	"context"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes/fake"
	"strings"
	"testing"
)

func TestTopologyOnboardingUsesCurrentNodeIdentity(t *testing.T) {
	for _, tc := range []struct {
		name, state string
		accepted    bool
	}{
		{"matching", `{"uid":"current","stage":"prepare","at":"2026-09-07T00:00:00Z"}`, true},
		{"replaced node", `{"uid":"old","stage":"ready"}`, false},
		{"malformed", `{broken`, false},
		{"disabled", ``, false},
		{"unknown stage", `{"uid":"current","stage":"arbitrary"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			node := gpuNode("gpu", map[string]string{"platform.wellspiking.ai/cache-ready": "true"}, "8")
			node.UID = "current"
			node.Spec.Unschedulable = true
			node.Status.Conditions = []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}
			node.Annotations = map[string]string{"platform.wellspiking.ai/onboarding-state": tc.state, "platform.wellspiking.ai/onboarding-reason": strings.Repeat("故", 450)}
			items, err := (&Client{kubernetes: fake.NewSimpleClientset(node)}).ListGPUNodeUsage(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			got := items[0]
			if got.NodeReady == nil || !*got.NodeReady || got.Cordoned == nil || !*got.Cordoned {
				t.Fatalf("missing node state: %+v", got)
			}
			if tc.accepted {
				if got.OnboardingStage != "prepare" || len([]rune(got.OnboardingReason)) != 400 || got.CacheReady == nil || *got.CacheReady {
					t.Fatalf("incorrect state: %+v", got)
				}
			} else if got.OnboardingStage != "" || got.OnboardingReason != "" || got.CacheReady != nil {
				t.Fatalf("untrusted state exposed: %+v", got)
			}
		})
	}
}

func TestTopologyReadyRequiresBothCurrentStateAndLabel(t *testing.T) {
	for _, label := range []string{"true", "false", ""} {
		node := gpuNode("gpu", map[string]string{"platform.wellspiking.ai/cache-ready": label}, "8")
		node.UID = "current"
		node.Annotations = map[string]string{"platform.wellspiking.ai/onboarding-state": `{"uid":"current","stage":"ready"}`}
		got := withNodeOnboarding(GPUNodeUsage{}, *node)
		if got.CacheReady == nil || *got.CacheReady != (label == "true") {
			t.Fatalf("label %q: %+v", label, got)
		}
	}
}
