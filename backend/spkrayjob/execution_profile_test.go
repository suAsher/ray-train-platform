package spkrayjob

import (
	"testing"

	"ray-train-platform-backend/domain"
)

func TestExecutionProfileForFlagsInfersExplicitDistributedModes(t *testing.T) {
	profile, err := executionProfileForFlags("auto", 2, 8)
	if err != nil || profile.Mode != domain.ExecutionModeRayTrain {
		t.Fatalf("profile = %#v, %v", profile, err)
	}
}

func TestExecutionProfileForFlagsRejectsInvalidTorchrun(t *testing.T) {
	if _, err := executionProfileForFlags("torchrun", 2, 1); err == nil {
		t.Fatal("expected invalid torchrun profile")
	}
}
