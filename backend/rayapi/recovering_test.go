package rayapi

import (
	"testing"

	"ray-train-platform-backend/domain"
)

func TestRecoveringJobRemainsActiveInRayJobsAPI(t *testing.T) {
	if got := rayStatus(domain.StateRecovering); got != "RUNNING" {
		t.Fatalf("recovering Ray job status=%q want RUNNING", got)
	}
}
