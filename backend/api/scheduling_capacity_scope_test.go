package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
)

func TestSchedulingCapacityRejectsUnrelatedPATScopes(t *testing.T) {
	for _, scopes := range [][]string{nil, {domain.PATScopeSourcesWrite}, {domain.PATScopeMLflowFull}, {domain.PATScopeJobsWrite}} {
		quota := &fakePlatformQuotaStore{}
		h := NewHandler(&fakeJobRepository{}, Options{Quota: quota})
		principal := auth.Principal{Subject: "member", TenantID: "team-a", AuthType: auth.AuthTypePAT, Scopes: scopes}
		response := httptest.NewRecorder()
		sessionRouter(h, &principal).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/capacity", nil))
		if response.Code != http.StatusForbidden || len(quota.tenantIDs) != 0 {
			t.Fatalf("unrelated PAT scope read capacity: scopes=%v status=%d quotaReads=%d", scopes, response.Code, len(quota.tenantIDs))
		}
	}
}
