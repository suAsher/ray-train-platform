package assistantidle

import (
	"context"
	"errors"
	"time"
)

// Snapshot contains only observations, never credentials or user data.
// OwnedChildrenRemaining means orphaned children after the RayService disappeared.
type Snapshot struct {
	Observation Observation
	UID         string
	CreatedAt   time.Time
}

type Backend interface {
	Observe(context.Context) (Snapshot, error)
	Create(context.Context) error
	Delete(context.Context, string) error
	OwnService(context.Context) (string, time.Time, error)
}

// Controller is run by one elected leader. Step must not be called concurrently.
type Controller struct {
	config      Config
	backend     Backend
	gate        *Gate
	now         func() time.Time
	idleSince   time.Time
	drainSince  time.Time
	createSince time.Time
	knownUID    string
}

func NewController(config Config, backend Backend, gate *Gate, now func() time.Time) *Controller {
	return &Controller{config: config.WithDefaults(), backend: backend, gate: gate, now: now}
}

func (c *Controller) Step(ctx context.Context) (Decision, error) {
	snapshot, err := c.backend.Observe(ctx)
	now := c.now()
	if err != nil {
		c.gate.Close()
		c.idleSince = time.Time{}
		// Observation may have exhausted its deadline. Cleanup has an independent,
		// bounded budget and can only delete the current, ownership-checked UID.
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()
		uid, _, lookupErr := c.backend.OwnService(cleanup)
		err = errors.Join(err, lookupErr)
		if lookupErr == nil && uid != "" {
			err = errors.Join(err, c.backend.Delete(cleanup, uid))
		}
		return Decision{Action: ActionWait, State: StateUnknown, Reason: "observation unavailable"}, err
	}
	obs := snapshot.Observation
	if obs.ServiceExists && snapshot.UID == "" {
		c.gate.Close()
		return Decision{Action: ActionWait, State: StateUnknown}, errors.New("service observation has no UID")
	}
	if snapshot.UID != c.knownUID {
		c.drainSince = time.Time{}
		c.knownUID = snapshot.UID
	}
	if obs.ServiceExists {
		c.createSince = time.Time{}
		c.idleSince = time.Time{}
	}
	idle := obs.Fresh && obs.Enabled && !obs.ServiceExists && !obs.ServiceDeleting && !obs.OwnedChildrenRemaining && !obs.TrainingDemand && !obs.OtherPending && obs.EligibleIdleGPU == 1
	if !idle {
		c.idleSince = time.Time{}
	} else if c.idleSince.IsZero() {
		c.idleSince = now
	}
	if !c.idleSince.IsZero() {
		obs.IdleFor = now.Sub(c.idleSince)
	}
	if obs.ServiceExists {
		if snapshot.CreatedAt.IsZero() || snapshot.CreatedAt.After(now) {
			obs.Fresh = false
		} else {
			obs.Runtime = now.Sub(snapshot.CreatedAt)
			obs.StartingFor = obs.Runtime
		}
	}
	if !c.drainSince.IsZero() {
		obs.DrainingFor = now.Sub(c.drainSince) + time.Nanosecond
	}
	d := Decide(c.config, obs)
	if d.Action != ActionRouteReady {
		c.gate.Close()
	}
	switch d.Action {
	case ActionCreate:
		if !c.createSince.IsZero() {
			if now.Sub(c.createSince) < c.config.StartTimeout {
				return Decision{Action: ActionWait, State: StateStarting, Reason: "waiting for created object observation"}, nil
			}
			c.createSince = time.Time{}
			c.idleSince = now
			return Decision{Action: ActionWait, State: StateUnknown, Reason: "creation acknowledgement timed out"}, nil
		}
		err = c.backend.Create(ctx)
		if err == nil {
			c.createSince = now
		} else {
			c.idleSince = time.Time{}
		}
	case ActionRouteReady:
		c.gate.Open(now.Add(3 * time.Second))
	case ActionDrain:
		if c.drainSince.IsZero() {
			c.drainSince = now
		}
	case ActionDelete:
		if snapshot.UID != "" {
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
			err = c.backend.Delete(cleanup, snapshot.UID)
			cancel()
		}
	}
	return d, err
}

func (c *Controller) Run(ctx context.Context, report func(Decision, error)) {
	defer c.gate.Close()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		attempt, cancel := context.WithTimeout(ctx, 2*time.Second)
		d, err := c.Step(attempt)
		cancel()
		if report != nil {
			report(d, err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
