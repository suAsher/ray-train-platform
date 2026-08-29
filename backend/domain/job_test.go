package domain

import "testing"

func TestStateTransitionAcceptsNormalLifecycle(t *testing.T) {
	steps := []struct {
		from State
		to   State
	}{
		{StateSubmitted, StateValidating},
		{StateValidating, StateQueued},
		{StateQueued, StateAdmitted},
		{StateAdmitted, StateProvisioning},
		{StateProvisioning, StateRunning},
		{StateRunning, StateSucceeded},
		{StateRunning, StateFailed},
		{StateRunning, StateCanceled},
	}

	for _, step := range steps {
		if !CanTransition(step.from, step.to) {
			t.Errorf("expected transition %s -> %s to be allowed", step.from, step.to)
		}
	}
}

func TestStateTransitionRejectsTerminalRegression(t *testing.T) {
	if CanTransition(StateSucceeded, StateRunning) {
		t.Fatal("terminal state must not transition back to running")
	}
	if CanTransition(StateFailed, StateQueued) {
		t.Fatal("failed state must not transition back to queued")
	}
}

func TestStateTransitionAllowsCancellationAndTimeout(t *testing.T) {
	if !CanTransition(StateRunning, StateCanceling) {
		t.Fatal("running job must be cancelable")
	}
	if !CanTransition(StateRunning, StateTimedOut) {
		t.Fatal("running job must be timeout-able")
	}
	if !CanTransition(StateCanceling, StateCanceled) {
		t.Fatal("canceling job must reach canceled")
	}
}

func TestRunningManagedJobCanEnterRecovering(t *testing.T) {
	if !CanTransition(StateRunning, StateRecovering) {
		t.Fatal("running managed job must be able to enter recovery")
	}
	for _, target := range []State{StateRunning, StateFailed, StateCanceled, StateTimedOut} {
		if !CanTransition(StateRecovering, target) {
			t.Fatalf("recovering job must be able to transition to %s", target)
		}
	}
}

func TestRecoveringIsNotTerminalOrAProvisioningRegression(t *testing.T) {
	if CanTransition(StateRecovering, StateSucceeded) {
		t.Fatal("a recovering attempt cannot succeed before it returns to running")
	}
	if CanTransition(StateProvisioning, StateRecovering) {
		t.Fatal("initial provisioning failures must not masquerade as checkpoint recovery")
	}
}
