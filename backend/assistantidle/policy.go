package assistantidle

import "time"

type Config struct {
	Enabled       bool
	IdleWindow    time.Duration
	DrainGrace    time.Duration
	StartTimeout  time.Duration
	MaxRuntime    time.Duration
	ReclaimTarget time.Duration
}

func DefaultConfig() Config {
	return Config{
		Enabled:       false,
		IdleWindow:    10 * time.Minute,
		DrainGrace:    15 * time.Second,
		StartTimeout:  10 * time.Minute,
		MaxRuntime:    time.Hour,
		ReclaimTarget: time.Minute,
	}
}

func (c Config) WithDefaults() Config {
	defaults := DefaultConfig()
	if c.IdleWindow <= 0 {
		c.IdleWindow = defaults.IdleWindow
	}
	if c.DrainGrace <= 0 {
		c.DrainGrace = defaults.DrainGrace
	}
	if c.StartTimeout <= 0 {
		c.StartTimeout = defaults.StartTimeout
	}
	if c.MaxRuntime <= 0 {
		c.MaxRuntime = defaults.MaxRuntime
	}
	if c.ReclaimTarget <= 0 {
		c.ReclaimTarget = defaults.ReclaimTarget
	}
	return c
}

type Observation struct {
	Fresh                  bool
	Enabled                bool
	TrainingDemand         bool
	OtherPending           bool
	EligibleIdleGPU        int
	ServiceExists          bool
	ServiceReady           bool
	ServiceDeleting        bool
	OwnedChildrenRemaining bool
	Admitted               bool
	IdleFor                time.Duration
	StartingFor            time.Duration
	DrainingFor            time.Duration
	Runtime                time.Duration
}

type Action string

const (
	ActionWait       Action = "wait"
	ActionCreate     Action = "create"
	ActionRouteReady Action = "routeReady"
	ActionDrain      Action = "drain"
	ActionDelete     Action = "delete"
)

type State string

const (
	StateDisabled       State = "disabled"
	StateUnknown        State = "unknown"
	StateDeleting       State = "deleting"
	StateTrainingDemand State = "trainingDemand"
	StateBlocked        State = "blocked"
	StateIdle           State = "idle"
	StateStarting       State = "starting"
	StateReady          State = "ready"
	StateDraining       State = "draining"
)

type Decision struct {
	Action Action
	State  State
	Delay  time.Duration
	Reason string
}

func Decide(config Config, observation Observation) Decision {
	cfg := config.WithDefaults()
	state := classify(cfg, observation)
	switch state {
	case StateDisabled, StateUnknown:
		if observation.ServiceExists {
			return decision(ActionDelete, state, 0, "service is not allowed in this state")
		}
		return decision(ActionWait, state, cfg.IdleWindow, "creation requires enabled fresh observation")
	case StateDeleting:
		return decision(ActionWait, state, cfg.ReclaimTarget, "service deletion is still in progress")
	case StateTrainingDemand:
		if !observation.ServiceExists {
			return decision(ActionWait, state, 0, "training demand owns the GPU budget")
		}
		if observation.DrainingFor >= cfg.DrainGrace {
			return decision(ActionDelete, state, 0, "training demand drain grace elapsed")
		}
		return decision(ActionDrain, state, bounded(cfg.ReclaimTarget, cfg.DrainGrace), "training demand has priority")
	case StateBlocked:
		return decision(ActionWait, state, cfg.IdleWindow, "idle capacity is not available")
	case StateIdle:
		return decision(ActionCreate, state, cfg.StartTimeout, "idle capacity is available")
	case StateStarting:
		if observation.StartingFor >= cfg.StartTimeout {
			return decision(ActionDrain, state, cfg.DrainGrace, "service did not become ready before start timeout")
		}
		return decision(ActionWait, state, cfg.StartTimeout-observation.StartingFor, "service is starting")
	case StateReady:
		if observation.Runtime >= cfg.MaxRuntime {
			return decision(ActionDrain, state, cfg.DrainGrace, "service reached max runtime")
		}
		return decision(ActionRouteReady, state, cfg.MaxRuntime-observation.Runtime, "service is ready")
	case StateDraining:
		if observation.DrainingFor >= cfg.DrainGrace {
			return decision(ActionDelete, state, 0, "drain grace elapsed")
		}
		return decision(ActionWait, state, cfg.DrainGrace-observation.DrainingFor, "service is draining")
	default:
		if observation.ServiceExists {
			return decision(ActionDelete, StateUnknown, 0, "unrecognized state")
		}
		return decision(ActionWait, StateUnknown, cfg.IdleWindow, "unrecognized state")
	}
}

func classify(cfg Config, obs Observation) State {
	if !cfg.Enabled || !obs.Enabled {
		return StateDisabled
	}
	if !obs.Fresh {
		return StateUnknown
	}
	if obs.ServiceDeleting || obs.OwnedChildrenRemaining {
		return StateDeleting
	}
	if obs.TrainingDemand {
		return StateTrainingDemand
	}
	if obs.ServiceExists {
		if obs.DrainingFor > 0 {
			return StateDraining
		}
		if obs.ServiceReady && obs.Admitted {
			return StateReady
		}
		return StateStarting
	}
	if obs.OtherPending || obs.EligibleIdleGPU != 1 || obs.IdleFor < cfg.IdleWindow {
		return StateBlocked
	}
	return StateIdle
}

func decision(action Action, state State, delay time.Duration, reason string) Decision {
	if delay < 0 {
		delay = 0
	}
	return Decision{Action: action, State: state, Delay: delay, Reason: reason}
}

func bounded(left, right time.Duration) time.Duration {
	if left < right {
		return left
	}
	return right
}
