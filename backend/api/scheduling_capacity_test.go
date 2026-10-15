package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
	"ray-train-platform-backend/k8s"
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
	for _, query := range []string{"workerReplicas=-1", "gpusPerWorker=1.2", "cpuPerWorker=9223372036854775807", "memoryPerWorker=999999999999999999999999Ti", "tenantId=team-b", "acceleratorClass=a100", "workerReplicas=", "memoryPerWorker=", "workerReplicas=1&workerReplicas=2"} {
		r := httptest.NewRecorder()
		sessionRouter(h, &p).ServeHTTP(r, httptest.NewRequest(http.MethodGet, "/api/v1/capacity?"+query, nil))
		if r.Code != http.StatusBadRequest {
			t.Errorf("query %q: expected 400, got %d body=%s", query, r.Code, r.Body)
		}
	}
}

func TestSchedulingCapacityPATUsesOwnPoolAndRedactsNodeDetails(t *testing.T) {
	for _, admin := range []bool{false, true} {
		node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-private", Labels: map[string]string{"accelerator": "nvidia-a100", "kubernetes.io/hostname": "node-private"}}, Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}, Allocatable: corev1.ResourceList{"nvidia.com/gpu": resource.MustParse("8"), corev1.ResourceCPU: resource.MustParse("64"), corev1.ResourceMemory: resource.MustParse("256Gi")}}}
		gvr := schema.GroupVersionResource{Group: "kueue.x-k8s.io", Version: "v1beta2", Resource: "workloads"}
		client := k8s.NewClientFromInterfaces(dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "WorkloadList"}), fake.NewSimpleClientset(node))
		quota := &fakePlatformQuotaStore{quota: domain.TenantQuota{TenantID: "team-a", GPULimit: 24, GPUAvailable: 24}}
		h := NewHandler(&fakeJobRepository{}, Options{Kubernetes: client, Quota: quota, TenantScheduling: fakePlatformSchedulingStore{accelerator: domain.AcceleratorA100}})
		p := auth.Principal{Subject: "member", TenantID: "team-a", AuthType: auth.AuthTypePAT, Roles: []string{domain.RoleEngineer}, Scopes: []string{domain.PATScopeJobsRead}}
		if admin {
			p.Roles = []string{domain.RoleSuperAdmin}
		}
		r := httptest.NewRecorder()
		started := time.Now().UTC()
		sessionRouter(h, &p).ServeHTTP(r, httptest.NewRequest(http.MethodGet, "/api/v1/capacity?workerReplicas=2&gpusPerWorker=8", nil))
		assertCapacityServerTime(t, started, r)
		var body struct {
			Data domain.SchedulingCapacity `json:"data"`
		}
		if err := json.Unmarshal(r.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if r.Code != http.StatusOK || r.Header().Get("Cache-Control") != "no-store" || !body.Data.Available || body.Data.AcceleratorClass != "a100" || body.Data.Pool.FreeGPUs != 8 || body.Data.Fit.Reason != "insufficient_gpus" || body.Data.Fit.CanFit {
			t.Fatalf("response=%d %s", r.Code, r.Body)
		}
		if (len(body.Data.Nodes) > 0) != admin {
			t.Fatalf("node visibility admin=%t: %s", admin, r.Body)
		}
		if len(quota.tenantIDs) != 1 || quota.tenantIDs[0] != "team-a" {
			t.Fatalf("wrong tenant quota lookup: %v", quota.tenantIDs)
		}
	}
}

func TestSchedulingCapacityUnavailableDoesNotInventQuota(t *testing.T) {
	h := NewHandler(&fakeJobRepository{}, Options{})
	p := auth.Principal{Subject: "member", TenantID: "team-a", AuthType: auth.AuthTypeOIDC}
	r := httptest.NewRecorder()
	started := time.Now().UTC()
	sessionRouter(h, &p).ServeHTTP(r, httptest.NewRequest(http.MethodGet, "/api/v1/capacity", nil))
	assertCapacityServerTime(t, started, r)
	var body struct {
		Data domain.SchedulingCapacity `json:"data"`
	}
	if err := json.Unmarshal(r.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if r.Code != http.StatusOK || body.Data.Available || body.Data.Quota != nil || body.Data.UnavailableReason != "quota_unavailable" {
		t.Fatalf("response=%d %s", r.Code, r.Body)
	}
}

// Decode the wire contract independently so a missing domain field fails as a
// behavioral assertion rather than a compilation error.
func assertCapacityServerTime(t *testing.T, started time.Time, response *httptest.ResponseRecorder) {
	t.Helper()
	finished := time.Now().UTC()
	var body struct {
		Data struct {
			ObservedAt time.Time  `json:"observedAt"`
			ServerTime *time.Time `json:"serverTime"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	serverTime := body.Data.ServerTime
	if serverTime == nil || serverTime.IsZero() {
		t.Fatalf("capacity response must include serverTime: %s", response.Body)
	}
	if serverTime.Before(started) || serverTime.After(finished) || serverTime.Before(body.Data.ObservedAt) {
		t.Fatalf("serverTime must identify this response after observation: start=%s observed=%s server=%s end=%s", started, body.Data.ObservedAt, serverTime, finished)
	}
	_, offset := serverTime.Zone()
	if offset != 0 {
		t.Fatalf("serverTime must be UTC: %s", serverTime)
	}
}

func TestSchedulingCapacityResponsePreservesCachedObservation(t *testing.T) {
	observedAt := time.Now().UTC().Add(-2 * time.Minute)
	previousResponseTime := observedAt.Add(time.Second)
	snapshot := domain.SchedulingCapacity{ObservedAt: observedAt, ServerTime: &previousResponseTime, Available: true}
	started := time.Now().UTC()
	response := schedulingCapacityResponse(snapshot)
	if response.ServerTime == nil || response.ServerTime.Before(started) || response.ServerTime.After(time.Now().UTC()) {
		t.Fatalf("response clock was not refreshed: %+v", response)
	}
	if !response.ObservedAt.Equal(observedAt) || !snapshot.ObservedAt.Equal(observedAt) || !snapshot.ServerTime.Equal(previousResponseTime) || response.ServerTime == snapshot.ServerTime {
		t.Fatal("response stamping must not freshen or mutate a cached observation")
	}
}
