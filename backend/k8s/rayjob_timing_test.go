package k8s

import (
	"testing"
	"time"
)

// The platform used to stamp a finish time from its own clock at the moment
// the reconciler happened to poll, so the reported end was later than reality
// by up to a poll interval and no start time existed at all. KubeRay already
// publishes both; they are the authoritative values.
func TestMapRayJobStatusCarriesKubeRayExecutionTimes(t *testing.T) {
	observed := MapRayJobStatus("job-1", map[string]any{
		"jobStatus": "SUCCEEDED",
		"startTime": "2026-08-17T09:15:28Z",
		"endTime":   "2026-08-17T09:17:18Z",
	}, "42")

	if observed.StartedAt == nil || observed.FinishedAt == nil {
		t.Fatalf("expected both execution times, got %+v", observed)
	}
	if !observed.StartedAt.Equal(time.Date(2026, 8, 17, 9, 15, 28, 0, time.UTC)) {
		t.Fatalf("unexpected start time: %v", observed.StartedAt)
	}
	if !observed.FinishedAt.Equal(time.Date(2026, 8, 17, 9, 17, 18, 0, time.UTC)) {
		t.Fatalf("unexpected end time: %v", observed.FinishedAt)
	}
}

// A running job has a start but no end. Reporting a zero or fabricated end
// would make the Portal show a finished run.
func TestMapRayJobStatusLeavesEndTimeAbsentWhileRunning(t *testing.T) {
	observed := MapRayJobStatus("job-1", map[string]any{
		"jobStatus": "RUNNING",
		"startTime": "2026-08-17T09:15:28Z",
	}, "7")

	if observed.StartedAt == nil {
		t.Fatal("a running job must report when it started")
	}
	if observed.FinishedAt != nil {
		t.Fatalf("a running job must not report an end time, got %v", observed.FinishedAt)
	}
}

// A queued job has neither. The Portal distinguishes "not started" from
// "started at the epoch", so an unparseable or absent value stays nil.
func TestMapRayJobStatusIgnoresAbsentAndMalformedTimes(t *testing.T) {
	for _, status := range []map[string]any{
		{"jobStatus": "SUSPENDED"},
		{"jobStatus": "RUNNING", "startTime": ""},
		{"jobStatus": "RUNNING", "startTime": "not-a-timestamp"},
		{"jobStatus": "SUCCEEDED", "startTime": nil, "endTime": nil},
	} {
		observed := MapRayJobStatus("job-1", status, "1")
		if observed.StartedAt != nil || observed.FinishedAt != nil {
			t.Fatalf("expected no execution times for %v, got start=%v end=%v", status, observed.StartedAt, observed.FinishedAt)
		}
	}
}
