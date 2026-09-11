package domain

import "testing"

func TestMLflowPATWriteScopeIsExplicitAndDoesNotExpandExistingScopes(t *testing.T) {
	scopes, err := NormalizePATScopes([]string{"mlflow:write"})
	if err != nil || len(scopes) != 1 || scopes[0] != "mlflow:write" {
		t.Fatalf("scope unavailable: %v %v", scopes, err)
	}
	old, err := NormalizePATScopes([]string{PATScopeJobsRead, PATScopeJobsWrite, PATScopeSourcesWrite})
	if err != nil || len(old) != 3 {
		t.Fatalf("old scopes changed: %v %v", old, err)
	}
	for _, scope := range old {
		if scope == "mlflow:write" {
			t.Fatal("existing token implicitly gained MLflow write")
		}
	}
}
