package api

import (
	"testing"
	"time"

	"ray-train-platform-backend/domain"
)

func TestJobLogQueryWindowUsesCompletedJobLifecycle(t *testing.T) {
	created := time.Date(2026, 8, 14, 8, 0, 0, 0, time.UTC)
	finished := created.Add(2*time.Hour + 15*time.Minute)
	start, end := JobLogQueryWindow(domain.TrainingJob{CreatedAt: created, FinishedAt: &finished}, time.Date(2026, 8, 17, 0, 0, 0, 0, time.UTC))
	if want := created.Add(-jobLogStartPadding); !start.Equal(want) {
		t.Fatalf("start = %s, want %s", start, want)
	}
	if want := finished.Add(jobLogEndPadding); !end.Equal(want) {
		t.Fatalf("end = %s, want %s", end, want)
	}
}

func TestJobLogQueryWindowKeepsLegacyFallbackForMigratedJob(t *testing.T) {
	now := time.Date(2026, 8, 17, 0, 0, 0, 0, time.UTC)
	start, end := JobLogQueryWindow(domain.TrainingJob{}, now)
	if want := now.Add(-30 * 24 * time.Hour); !start.Equal(want) {
		t.Fatalf("start = %s, want %s", start, want)
	}
	if !end.Equal(now) {
		t.Fatalf("end = %s, want %s", end, now)
	}
}
