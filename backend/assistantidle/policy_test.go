package assistantidle

import (
	"testing"
	"time"
)

func TestDefaultConfigStartsDisabledWithBoundedDurations(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Enabled {
		t.Fatal("assistant idle policy must default disabled")
	}
	if cfg.IdleWindow != 10*time.Minute || cfg.DrainGrace != 15*time.Second || cfg.StartTimeout != 10*time.Minute || cfg.MaxRuntime != time.Hour || cfg.ReclaimTarget != time.Minute {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
}

func TestUnknownOrDisabledStateNeverCreatesAndDrainsExistingService(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Enabled = true

	for name, obs := range map[string]Observation{
		"stale":        {Enabled: true, Fresh: false, EligibleIdleGPU: 1, IdleFor: cfg.IdleWindow},
		"observer off": {Enabled: false, Fresh: true, EligibleIdleGPU: 1, IdleFor: cfg.IdleWindow},
	} {
		t.Run(name+" without service", func(t *testing.T) {
			got := Decide(cfg, obs)
			if got.Action != ActionWait {
				t.Fatalf("got %s, want wait", got.Action)
			}
		})
		t.Run(name+" with service", func(t *testing.T) {
			obs.ServiceExists = true
			got := Decide(cfg, obs)
			if got.Action != ActionDelete {
				t.Fatalf("got %s, want delete", got.Action)
			}
		})
	}
}

func TestDeletingResourcesWaitForConfirmationWithoutRecreate(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Enabled = true
	obs := Observation{
		Enabled:                true,
		Fresh:                  true,
		EligibleIdleGPU:        1,
		IdleFor:                cfg.IdleWindow,
		ServiceExists:          true,
		ServiceDeleting:        true,
		OwnedChildrenRemaining: true,
	}

	got := Decide(cfg, obs)
	if got.Action != ActionWait {
		t.Fatalf("got %s, want wait", got.Action)
	}
}

func TestTrainingDemandIsHighestPriorityAndDoesNotWaitForModelLoad(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Enabled = true
	obs := Observation{
		Enabled:         true,
		Fresh:           true,
		TrainingDemand:  true,
		EligibleIdleGPU: 1,
		IdleFor:         cfg.IdleWindow,
		ServiceExists:   true,
		ServiceReady:    false,
		Admitted:        true,
		StartingFor:     time.Second,
	}

	got := Decide(cfg, obs)
	if got.Action != ActionDrain {
		t.Fatalf("got %s, want drain", got.Action)
	}
	if got.Delay > cfg.ReclaimTarget {
		t.Fatalf("training reclaim delay %s exceeds target %s", got.Delay, cfg.ReclaimTarget)
	}
}

func TestPolicyDoesNotRecreateUntilDeletionIsFullyConfirmed(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Enabled = true
	obs := Observation{
		Enabled:                true,
		Fresh:                  true,
		EligibleIdleGPU:        1,
		IdleFor:                cfg.IdleWindow,
		ServiceDeleting:        true,
		OwnedChildrenRemaining: true,
	}

	got := Decide(cfg, obs)
	if got.Action != ActionWait {
		t.Fatalf("got %s, want wait", got.Action)
	}

	obs.ServiceDeleting = false
	obs.OwnedChildrenRemaining = false
	got = Decide(cfg, obs)
	if got.Action != ActionCreate {
		t.Fatalf("got %s, want create after deletion confirmation", got.Action)
	}
}

func TestCreateRequiresSingleIdleGPUAndFullIdleWindow(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Enabled = true
	base := Observation{Enabled: true, Fresh: true, IdleFor: cfg.IdleWindow}

	for name, obs := range map[string]Observation{
		"no gpu":         base,
		"idle too short": {Enabled: true, Fresh: true, EligibleIdleGPU: 1, IdleFor: cfg.IdleWindow - time.Second},
		"other pending":  {Enabled: true, Fresh: true, EligibleIdleGPU: 1, IdleFor: cfg.IdleWindow, OtherPending: true},
	} {
		t.Run(name, func(t *testing.T) {
			got := Decide(cfg, obs)
			if got.Action != ActionWait {
				t.Fatalf("got %s, want wait", got.Action)
			}
		})
	}

	base.EligibleIdleGPU = 1
	got := Decide(cfg, base)
	if got.Action != ActionCreate {
		t.Fatalf("got %s, want create", got.Action)
	}
}

func TestReadyServiceRoutesUntilRuntimeLimit(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Enabled = true
	obs := Observation{
		Enabled:       true,
		Fresh:         true,
		ServiceExists: true,
		ServiceReady:  true,
		Admitted:      true,
		Runtime:       cfg.MaxRuntime - time.Second,
	}

	got := Decide(cfg, obs)
	if got.Action != ActionRouteReady {
		t.Fatalf("got %s, want routeReady", got.Action)
	}

	obs.Runtime = cfg.MaxRuntime
	got = Decide(cfg, obs)
	if got.Action != ActionDrain {
		t.Fatalf("got %s, want drain at max runtime", got.Action)
	}
}

func TestStartingTimeoutAndDrainGraceConvergeToDelete(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Enabled = true

	starting := Observation{Enabled: true, Fresh: true, ServiceExists: true, Admitted: true, StartingFor: cfg.StartTimeout}
	got := Decide(cfg, starting)
	if got.Action != ActionDrain {
		t.Fatalf("starting timeout got %s, want drain", got.Action)
	}

	draining := Observation{Enabled: true, Fresh: true, ServiceExists: true, DrainingFor: cfg.DrainGrace}
	got = Decide(cfg, draining)
	if got.Action != ActionDelete {
		t.Fatalf("drain grace got %s, want delete", got.Action)
	}
}
