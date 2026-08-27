package domain

type State string

const (
	StateSubmitted    State = "SUBMITTED"
	StateValidating   State = "VALIDATING"
	StateQueued       State = "QUEUED"
	StateAdmitted     State = "ADMITTED"
	StateProvisioning State = "PROVISIONING"
	StateRunning      State = "RUNNING"
	StateRecovering   State = "RECOVERING"
	StateSucceeded    State = "SUCCEEDED"
	StateFailed       State = "FAILED"
	StateCanceling    State = "CANCELING"
	StateCanceled     State = "CANCELED"
	StateTimedOut     State = "TIMED_OUT"
	StateUnknown      State = "UNKNOWN"
	StateDeleting     State = "DELETING"
)

func CanTransition(from, to State) bool {
	if from == to {
		return true
	}
	transitions := map[State]map[State]bool{
		StateSubmitted:    {StateValidating: true, StateQueued: true, StateProvisioning: true, StateCanceling: true},
		StateValidating:   {StateQueued: true, StateFailed: true, StateCanceling: true},
		StateQueued:       {StateAdmitted: true, StateFailed: true, StateCanceling: true, StateTimedOut: true},
		StateAdmitted:     {StateProvisioning: true, StateFailed: true, StateCanceling: true, StateTimedOut: true},
		StateProvisioning: {StateRunning: true, StateFailed: true, StateCanceling: true, StateDeleting: true, StateTimedOut: true},
		StateRunning:      {StateRecovering: true, StateSucceeded: true, StateFailed: true, StateCanceling: true, StateCanceled: true, StateTimedOut: true, StateUnknown: true},
		StateRecovering:   {StateRunning: true, StateFailed: true, StateCanceled: true, StateTimedOut: true},
		StateCanceling:    {StateCanceled: true, StateFailed: true, StateDeleting: true},
		StateUnknown:      {StateQueued: true, StateProvisioning: true, StateRunning: true, StateSucceeded: true, StateFailed: true, StateCanceled: true},
		StateDeleting:     {StateCanceled: true, StateSucceeded: true, StateFailed: true},
	}
	return transitions[from][to]
}
