package assistantidle

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestStatusReporterNeverInventsReady(t *testing.T) {
	now := time.Date(2026, 9, 22, 8, 0, 0, 0, time.UTC)
	gate := NewGate("safe-epoch", func() time.Time { return now })
	reporter := NewStatusReporter(true, gate, func() time.Time { return now })
	if s := reporter.Snapshot(); s.State != StateUnknown || s.Gate.Allow || s.ObservedAt != nil {
		t.Fatalf("initial=%+v", s)
	}
	gate.Open(now.Add(3 * time.Second))
	reporter.Record(Decision{State: StateReady, Action: ActionRouteReady, Reason: "service is ready"}, nil)
	if s := reporter.Snapshot(); s.State != StateReady || !s.Gate.Allow || s.Reason != "service_ready" {
		t.Fatalf("ready=%+v", s)
	}
	now = now.Add(6 * time.Second)
	if s := reporter.Snapshot(); s.State != StateUnknown || s.Gate.Allow || s.Reason != "observation_stale" {
		t.Fatalf("stale=%+v", s)
	}
}
func TestStatusReporterRedactsReasonAndRequiresGate(t *testing.T) {
	now := time.Now()
	reporter := NewStatusReporter(true, NewGate("safe", func() time.Time { return now }), func() time.Time { return now })
	reporter.Record(Decision{State: StateReady, Reason: "secret=http://internal.invalid/?token=private"}, nil)
	status := reporter.Snapshot()
	if status.State == StateReady || status.Gate.Allow {
		t.Fatal("closed gate presented as ready")
	}
	raw, _ := json.Marshal(status)
	if strings.Contains(string(raw), "secret") || strings.Contains(string(raw), "internal") {
		t.Fatal("raw decision leaked")
	}
	w := httptest.NewRecorder()
	reporter.ServeHTTP(w, httptest.NewRequest("POST", "/status", nil))
	if w.Code != 405 {
		t.Fatal("status accepted mutation")
	}
	w = httptest.NewRecorder()
	reporter.ServeHTTP(w, httptest.NewRequest("GET", "/status", nil))
	if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" || w.Body.Len() > 4096 {
		t.Fatal("unbounded or cacheable status")
	}
}
func TestStatusReporterDisabledAndFailure(t *testing.T) {
	now := time.Now()
	reporter := NewStatusReporter(false, NewGate("safe", time.Now), func() time.Time { return now })
	if s := reporter.Snapshot(); s.Enabled || s.State != StateDisabled || s.Gate.Allow {
		t.Fatalf("disabled=%+v", s)
	}
}

func TestStatusReporterErrorCannotLeakOrRemainReady(t *testing.T) {
	now := time.Now()
	gate := NewGate("safe", func() time.Time { return now })
	gate.Open(now.Add(3 * time.Second))
	reporter := NewStatusReporter(true, gate, func() time.Time { return now })
	reporter.Record(Decision{State: StateReady, Reason: "service is ready"}, errors.New("credential-private-url"))
	got := reporter.Snapshot()
	raw, _ := json.Marshal(got)
	if got.State != StateUnknown || got.Reason != "observation_failed" || got.Gate.Allow || strings.Contains(string(raw), "credential") {
		t.Fatalf("unsafe failed status: %s", raw)
	}
}

func TestStatusControllerNetworkPolicyRequiresBackendNamespaceAndPodInSamePeer(t *testing.T) {
	raw, err := os.ReadFile("../../deploy/assistant-idle/networkpolicy.yaml")
	if err != nil {
		t.Fatal(err)
	}
	policy := networkPolicyNamed(t, string(raw), "assistant-idle-controller-gate")
	found := false
	for _, rule := range policy.Spec.Ingress {
		for _, peer := range rule.From {
			if peer.NamespaceSelector != nil && peer.NamespaceSelector.MatchLabels["kubernetes.io/metadata.name"] == "PLACEHOLDER_RAYTRAIN_BACKEND_NAMESPACE" {
				if peer.PodSelector == nil || peer.PodSelector.MatchLabels["app"] != "ray-train-backend" || peer.PodSelector.MatchLabels["app.kubernetes.io/component"] != "api" {
					t.Fatal("backend peer is not pinned to API pods")
				}
				found = true
			}
		}
	}
	if !found {
		t.Fatal("backend status peer absent")
	}
}

// Every decision reachable through the current policy must have a safe display
// reason. New policy prose must not silently turn into raw operational output.
func TestStatusReasonsCoverPolicyDecisions(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Enabled = true
	for flags := 0; flags < 256; flags++ {
		for _, duration := range []time.Duration{0, time.Second, time.Hour} {
			observation := Observation{Enabled: flags&1 != 0, Fresh: flags&2 != 0, ServiceExists: flags&4 != 0, ServiceDeleting: flags&8 != 0, TrainingDemand: flags&16 != 0, IdleFor: time.Hour, StartingFor: duration, Runtime: duration, ServiceReady: flags&64 != 0, Admitted: flags&64 != 0}
			if flags&32 != 0 {
				observation.EligibleIdleGPU = 1
			}
			if flags&128 != 0 {
				observation.DrainingFor = duration
			}
			decision := Decide(cfg, observation)
			reason := safeDecisionReason(decision.Reason)
			if reason == "unknown" || !ValidStatusReason(reason) || !ValidStatusState(decision.State) {
				t.Fatalf("unmapped policy decision: %+v", decision)
			}
		}
	}
	if ValidStatusReason("private-url") || ValidStatusState(State("private-state")) {
		t.Fatal("unknown operational text accepted")
	}
	policy := DefaultStatusPolicy()
	if policy.MaxGPU != 1 || policy.IdleSeconds != 600 || policy.DrainSeconds != 15 || policy.MaxRuntimeSeconds != 3600 {
		t.Fatalf("policy contract changed: %+v", policy)
	}
}
