package main

import (
	"encoding/json"
	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
	"strings"
	"testing"
)

func TestTopologyOnboardingOnlyVisibleToSuperAdmin(t *testing.T) {
	ready := true
	original := domain.ClusterTopologyOverview{TotalGPUs: 8, Nodes: []domain.GPUNodeUsage{{NodeName: "gpu", Capacity: 8, NodeReady: &ready, Cordoned: &ready, CacheReady: &ready, OnboardingStage: "ready", OnboardingReason: "internal reason"}}}
	for _, role := range []string{"", "Engineer", "TenantAdmin", "SuperAdmin"} {
		got := topologyForPrincipal(original, auth.Principal{Roles: []string{role}})
		data, err := json.Marshal(got)
		if err != nil {
			t.Fatal(err)
		}
		for _, field := range []string{"nodeReady", "cordoned", "cacheReady", "onboardingStage", "onboardingReason"} {
			if strings.Contains(string(data), field) != (role == "SuperAdmin") {
				t.Fatalf("role %s unexpected %s: %s", role, field, data)
			}
		}
		if got.TotalGPUs != 8 || got.Nodes[0].Capacity != 8 {
			t.Fatal("existing topology changed")
		}
	}
	if original.Nodes[0].OnboardingReason != "internal reason" {
		t.Fatal("redaction mutated original")
	}
}
