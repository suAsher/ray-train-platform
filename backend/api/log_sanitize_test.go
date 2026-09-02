package api

import (
	"testing"
	"time"

	"ray-train-platform-backend/observability"
)

func TestSanitizeJobLogLinesDropsTerminalProgressFramesAndKeepsTrainingText(t *testing.T) {
	now := time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)
	lines := []observability.LogLine{
		{Timestamp: now, Line: "\n"},
		{Timestamp: now.Add(time.Nanosecond), Line: "\x1b[A\x1b[A"},
		{Timestamp: now.Add(2 * time.Nanosecond), Line: "\x1b[36m(RayTrainWorker pid=308)\x1b[0m Epoch [1][10/952]\tloss: 19383.0702"},
		{Timestamp: now.Add(3 * time.Nanosecond), Line: "download 10%\rdownload 100%"},
	}

	cleaned := sanitizeJobLogLines(lines)

	if len(cleaned) != 2 {
		t.Fatalf("cleaned lines=%d want 2: %+v", len(cleaned), cleaned)
	}
	if cleaned[0].Line != "(RayTrainWorker pid=308) Epoch [1][10/952]\tloss: 19383.0702" {
		t.Fatalf("training line was not preserved: %q", cleaned[0].Line)
	}
	if cleaned[1].Line != "download 100%" {
		t.Fatalf("carriage-return redraw did not keep final frame: %q", cleaned[1].Line)
	}
	if cleaned[0].Timestamp != lines[2].Timestamp || cleaned[1].Timestamp != lines[3].Timestamp {
		t.Fatalf("sanitization changed timestamps: %+v", cleaned)
	}
}
