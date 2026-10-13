package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"ray-train-platform-backend/auth"
)

func TestSchedulingCapacityRequiresAuthentication(t *testing.T) {
	h := NewHandler(&fakeJobRepository{}, Options{})
	r := httptest.NewRecorder()
	sessionRouter(h, nil).ServeHTTP(r, httptest.NewRequest(http.MethodGet, "/api/v1/capacity", nil))
	if r.Code != http.StatusUnauthorized {
		t.Fatalf("capacity requires authentication: status=%d body=%s", r.Code, r.Body)
	}
}

func TestSchedulingCapacityRejectsMalformedShape(t *testing.T) {
	h := NewHandler(&fakeJobRepository{}, Options{})
	p := auth.Principal{Subject: "member", TenantID: "team-a", AuthType: auth.AuthTypeOIDC}
	for _, query := range []string{"workerReplicas=-1", "gpusPerWorker=1.2", "cpuPerWorker=9223372036854775807", "memoryPerWorker=999999999999999999999999Ti", "tenantId=team-b", "acceleratorClass=a100"} {
		r := httptest.NewRecorder()
		sessionRouter(h, &p).ServeHTTP(r, httptest.NewRequest(http.MethodGet, "/api/v1/capacity?"+query, nil))
		if r.Code != http.StatusBadRequest {
			t.Errorf("query %q: expected 400, got %d body=%s", query, r.Code, r.Body)
		}
	}
}
