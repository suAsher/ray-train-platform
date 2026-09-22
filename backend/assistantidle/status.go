package assistantidle

import (
	"encoding/json"
	corev1 "k8s.io/api/core/v1"
	"net/http"
	"sync"
	"time"
)

type StatusPolicy struct {
	MaxGPU            int `json:"maxGPU"`
	IdleSeconds       int `json:"idleSeconds"`
	DrainSeconds      int `json:"drainSeconds"`
	MaxRuntimeSeconds int `json:"maxRuntimeSeconds"`
}

func DefaultStatusPolicy() StatusPolicy {
	return StatusPolicy{MaxGPU: 1, IdleSeconds: 600, DrainSeconds: 15, MaxRuntimeSeconds: 3600}
}

type ControllerStatus struct {
	Enabled    bool       `json:"enabled"`
	State      State      `json:"state"`
	Reason     string     `json:"reason"`
	ObservedAt *time.Time `json:"observedAt"`
	Gate       GateStatus `json:"gate"`
}

type StatusReporter struct {
	mu      sync.RWMutex
	enabled bool
	gate    *Gate
	now     func() time.Time
	last    ControllerStatus
}

func NewStatusReporter(enabled bool, gate *Gate, now func() time.Time) *StatusReporter {
	return &StatusReporter{enabled: enabled, gate: gate, now: now}
}
func (s *StatusReporter) Record(d Decision, err error) {
	now := s.now().UTC()
	status := ControllerStatus{Enabled: s.enabled, State: d.State, Reason: safeDecisionReason(d.Reason), ObservedAt: &now}
	if !ValidStatusState(status.State) {
		status.State = StateUnknown
	}
	if err != nil {
		status.State = StateUnknown
		status.Reason = "observation_failed"
	}
	s.mu.Lock()
	s.last = status
	s.mu.Unlock()
}
func (s *StatusReporter) Snapshot() ControllerStatus {
	s.mu.RLock()
	status := s.last
	s.mu.RUnlock()
	if status.ObservedAt != nil {
		copy := *status.ObservedAt
		status.ObservedAt = &copy
	}
	status.Enabled = s.enabled
	status.Gate = s.gate.Snapshot()
	if !s.enabled {
		status.State = StateDisabled
		status.Reason = "disabled"
		status.Gate.Allow = false
		return status
	}
	if status.ObservedAt == nil {
		status.State = StateUnknown
		status.Reason = "awaiting_observation"
		status.Gate.Allow = false
		return status
	}
	if s.now().Sub(*status.ObservedAt) > 5*time.Second || status.ObservedAt.After(s.now().Add(time.Second)) {
		status.State = StateUnknown
		status.Reason = "observation_stale"
		status.Gate.Allow = false
	}
	if status.State == StateReady && !status.Gate.Allow {
		status.State = StateUnknown
		status.Reason = "gate_closed"
	}
	if status.State != StateReady {
		status.Gate.Allow = false
	}
	return status
}
func (s *StatusReporter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(s.Snapshot())
}
func ValidStatusState(state State) bool {
	switch state {
	case StateDisabled, StateUnknown, StateDeleting, StateTrainingDemand, StateBlocked, StateIdle, StateStarting, StateReady, StateDraining:
		return true
	}
	return false
}
func ValidStatusReason(reason string) bool {
	switch reason {
	case "disabled", "awaiting_observation", "observation_stale", "observation_failed", "gate_closed", "unknown", "not_allowed", "observation_required", "deleting", "training_demand", "training_drain_elapsed", "idle_unavailable", "idle_available", "start_timeout", "starting", "max_runtime", "service_ready", "drain_elapsed", "draining", "creation_pending", "creation_timeout":
		return true
	}
	return false
}
func safeDecisionReason(reason string) string {
	switch reason {
	case "service is not allowed in this state":
		return "not_allowed"
	case "creation requires enabled fresh observation":
		return "observation_required"
	case "service deletion is still in progress":
		return "deleting"
	case "training demand owns the GPU budget", "training demand has priority":
		return "training_demand"
	case "training demand drain grace elapsed":
		return "training_drain_elapsed"
	case "idle capacity is not available":
		return "idle_unavailable"
	case "idle capacity is available":
		return "idle_available"
	case "service did not become ready before start timeout":
		return "start_timeout"
	case "service is starting":
		return "starting"
	case "service reached max runtime":
		return "max_runtime"
	case "service is ready":
		return "service_ready"
	case "drain grace elapsed":
		return "drain_elapsed"
	case "service is draining":
		return "draining"
	case "waiting for created object observation":
		return "creation_pending"
	case "creation acknowledgement timed out":
		return "creation_timeout"
	default:
		return "unknown"
	}
}

// PodGPURequested shares the controller's init/sidecar-aware resource accounting.
func PodGPURequested(pod corev1.Pod) int64 { return podGPURequests(pod) }
