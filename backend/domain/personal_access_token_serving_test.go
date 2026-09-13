package domain

import "testing"

func TestServingInvocationScopeIsExplicit(t *testing.T) {
	scopes, err := NormalizePATScopes([]string{"models:invoke"})
	if err != nil || len(scopes) != 1 || scopes[0] != "models:invoke" {
		t.Fatalf("scope unavailable: %v %v", scopes, err)
	}
	old, err := NormalizePATScopes([]string{PATScopeJobsRead, PATScopeJobsWrite, PATScopeMLflowFull})
	if err != nil || len(old) != 3 {
		t.Fatal("old token changed")
	}
	for _, scope := range old {
		if scope == "models:invoke" {
			t.Fatal("old token gained inference scope")
		}
	}
}
