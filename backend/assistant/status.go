package assistant

import "time"

// BackendRuntimeStatus is safe operational metadata, never provider credentials
// or endpoint URLs. Cooldown is process-local and does not report account quota.
type BackendRuntimeStatus struct {
	BackendStatus
	CooldownUntil *time.Time `json:"cooldownUntil,omitempty"`
	Reason        string     `json:"reason"`
}

func (r *Router) AdminBackends() []BackendRuntimeStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	result := make([]BackendRuntimeStatus, 0, len(r.backends))
	for _, b := range r.backends {
		configured := b.provider != nil
		status := BackendRuntimeStatus{BackendStatus: BackendStatus{ID: b.id, Kind: b.kind, Protocol: effectiveProtocol(b.protocol), Model: b.model, Configured: configured}, Reason: "not_configured"}
		if configured {
			status.Reason = "available"
		}
		if until := r.blockedUntil[b.id]; now.Before(until) {
			status.CooldownUntil = &until
			switch reason := r.blockedReason[b.id]; reason {
			case "budget_exhausted", "authentication_error", "rate_limited":
				status.Reason = reason
			default:
				status.Reason = "provider_unavailable"
			}
		}
		result = append(result, status)
	}
	return result
}
