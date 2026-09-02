package api

import (
	"context"
	"testing"
	"time"

	"ray-train-platform-backend/domain"
	"ray-train-platform-backend/observability"
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

type pagedLogProvider struct {
	direction observability.LogDirection
	limit     int
	start     time.Time
	end       time.Time
	lines     []observability.LogLine
}

func (provider *pagedLogProvider) QueryJobLogs(context.Context, string, int) ([]observability.LogLine, error) {
	return append([]observability.LogLine(nil), provider.lines...), nil
}

func (provider *pagedLogProvider) QueryJobLogsPage(_ context.Context, _ string, limit int, start, end time.Time, direction observability.LogDirection) ([]observability.LogLine, error) {
	provider.direction = direction
	provider.limit = limit
	provider.start = start
	provider.end = end
	return append([]observability.LogLine(nil), provider.lines...), nil
}

type rangePagedLogProvider struct {
	lines []observability.LogLine
	limit int
}

func (provider *rangePagedLogProvider) QueryJobLogs(context.Context, string, int) ([]observability.LogLine, error) {
	return nil, nil
}

func (provider *rangePagedLogProvider) QueryJobLogsPage(_ context.Context, _ string, limit int, start, end time.Time, direction observability.LogDirection) ([]observability.LogLine, error) {
	provider.limit = limit
	selected := make([]observability.LogLine, 0)
	for _, line := range provider.lines {
		if !line.Timestamp.Before(start) && !line.Timestamp.After(end) {
			selected = append(selected, line)
		}
	}
	if len(selected) > limit {
		if direction == observability.LogDirectionBackward {
			selected = selected[len(selected)-limit:]
		} else {
			selected = selected[:limit]
		}
	}
	return append([]observability.LogLine(nil), selected...), nil
}

func TestQueryJobLogPageReturnsLatestLinesAndOlderCursor(t *testing.T) {
	created := time.Date(2026, 8, 22, 16, 0, 0, 0, time.UTC)
	provider := &pagedLogProvider{lines: []observability.LogLine{
		{Timestamp: created.Add(time.Second), Line: "first"},
		{Timestamp: created.Add(2 * time.Second), Line: "second"},
		{Timestamp: created.Add(3 * time.Second), Line: "third"},
	}}
	page, err := QueryJobLogPage(context.Background(), provider, domain.TrainingJob{ID: "job-1", CreatedAt: created}, JobLogPageRequest{
		Limit: 2, Direction: observability.LogDirectionBackward,
	}, created.Add(time.Hour))
	if err != nil {
		t.Fatalf("query page: %v", err)
	}
	if provider.limit != 3 || provider.direction != observability.LogDirectionBackward {
		t.Fatalf("provider request limit=%d direction=%q", provider.limit, provider.direction)
	}
	if len(page.Lines) != 2 || page.Lines[0].Line != "second" || page.Lines[1].Line != "third" {
		t.Fatalf("unexpected page lines: %+v", page.Lines)
	}
	if !page.HasMore || page.NextCursor != created.Add(2*time.Second).Format(time.RFC3339Nano)+"~1" {
		t.Fatalf("unexpected page metadata: %+v", page)
	}
}

func TestQueryJobLogPageAppliesForwardCursorWithoutRepeatingBoundary(t *testing.T) {
	created := time.Date(2026, 8, 22, 16, 0, 0, 0, time.UTC)
	cursor := created.Add(2 * time.Second)
	provider := &pagedLogProvider{lines: []observability.LogLine{{Timestamp: created.Add(3 * time.Second), Line: "next"}}}
	page, err := QueryJobLogPage(context.Background(), provider, domain.TrainingJob{ID: "job-1", CreatedAt: created}, JobLogPageRequest{
		Limit: 100, Direction: observability.LogDirectionForward, Cursor: &cursor,
	}, created.Add(time.Hour))
	if err != nil {
		t.Fatalf("query page: %v", err)
	}
	if !provider.start.Equal(cursor) {
		t.Fatalf("start = %s, want inclusive cursor", provider.start)
	}
	if len(page.Lines) != 1 || page.HasMore || page.NextCursor != created.Add(3*time.Second).Format(time.RFC3339Nano)+"~1" {
		t.Fatalf("unexpected page: %+v", page)
	}
}

func TestQueryJobLogPageKeepsRawCursorProgressWhenNoiseIsFiltered(t *testing.T) {
	created := time.Date(2026, 8, 22, 16, 0, 0, 0, time.UTC)
	provider := &pagedLogProvider{lines: []observability.LogLine{
		{Timestamp: created.Add(time.Second), Line: "\n"},
		{Timestamp: created.Add(2 * time.Second), Line: "first visible"},
		{Timestamp: created.Add(3 * time.Second), Line: "second visible"},
	}}
	page, err := QueryJobLogPage(context.Background(), provider, domain.TrainingJob{ID: "job-1", CreatedAt: created}, JobLogPageRequest{
		Limit: 2, Direction: observability.LogDirectionForward,
	}, created.Add(time.Hour))
	if err != nil {
		t.Fatalf("query page: %v", err)
	}
	if len(page.Lines) != 1 || page.Lines[0].Line != "first visible" {
		t.Fatalf("visible page lines = %+v, want one sanitized line", page.Lines)
	}
	if !page.HasMore {
		t.Fatal("page must preserve raw has-more state after filtering terminal noise")
	}
	wantCursor := created.Add(2*time.Second).Format(time.RFC3339Nano) + "~1"
	if page.NextCursor != wantCursor {
		t.Fatalf("next cursor = %q, want raw boundary %q", page.NextCursor, wantCursor)
	}
}

func TestQueryJobLogPageDoesNotLoseEqualTimestampsAcrossForwardPages(t *testing.T) {
	created := time.Date(2026, 8, 22, 16, 0, 0, 0, time.UTC)
	stamp := created.Add(time.Second)
	provider := &rangePagedLogProvider{lines: []observability.LogLine{
		{Timestamp: stamp, Line: "c"}, {Timestamp: stamp, Line: "a"},
		{Timestamp: stamp, Line: "d"}, {Timestamp: stamp, Line: "b"},
	}}
	first, err := QueryJobLogPage(context.Background(), provider, domain.TrainingJob{ID: "job-1", CreatedAt: created}, JobLogPageRequest{
		Limit: 2, Direction: observability.LogDirectionForward,
	}, created.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	request, err := NormalizeJobLogPageRequest("2", "forward", "", first.NextCursor)
	if err != nil {
		t.Fatal(err)
	}
	second, err := QueryJobLogPage(context.Background(), provider, domain.TrainingJob{ID: "job-1", CreatedAt: created}, request, created.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if got := []string{first.Lines[0].Line, first.Lines[1].Line, second.Lines[0].Line, second.Lines[1].Line}; !equalStrings(got, []string{"c", "a", "d", "b"}) {
		t.Fatalf("equal-timestamp forward pages lost or repeated lines: %v", got)
	}
	if provider.limit != 5 {
		t.Fatalf("second query limit=%d, want page + consumed boundary + probe", provider.limit)
	}
}

func TestQueryJobLogPageDoesNotLoseEqualTimestampsAcrossBackwardPages(t *testing.T) {
	created := time.Date(2026, 8, 22, 16, 0, 0, 0, time.UTC)
	stamp := created.Add(time.Second)
	provider := &rangePagedLogProvider{lines: []observability.LogLine{
		{Timestamp: stamp, Line: "c"}, {Timestamp: stamp, Line: "a"},
		{Timestamp: stamp, Line: "d"}, {Timestamp: stamp, Line: "b"},
	}}
	first, err := QueryJobLogPage(context.Background(), provider, domain.TrainingJob{ID: "job-1", CreatedAt: created}, JobLogPageRequest{
		Limit: 2, Direction: observability.LogDirectionBackward,
	}, created.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	request, err := NormalizeJobLogPageRequest("2", "backward", first.NextCursor, "")
	if err != nil {
		t.Fatal(err)
	}
	second, err := QueryJobLogPage(context.Background(), provider, domain.TrainingJob{ID: "job-1", CreatedAt: created}, request, created.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if got := []string{second.Lines[0].Line, second.Lines[1].Line, first.Lines[0].Line, first.Lines[1].Line}; !equalStrings(got, []string{"c", "a", "d", "b"}) {
		t.Fatalf("equal-timestamp backward pages lost or repeated lines: %v", got)
	}
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func TestNormalizeJobLogPageRequestRejectsLokiLimitOverflow(t *testing.T) {
	if _, err := NormalizeJobLogPageRequest("10000", "backward", "", ""); err == nil {
		t.Fatal("expected oversized page rejection")
	}
	if _, err := NormalizeJobLogPageRequest("2000", "forward", "2026-08-22T16:00:00Z", "2026-08-22T16:00:00Z"); err == nil {
		t.Fatal("expected conflicting cursor rejection")
	}
	if _, err := NormalizeJobLogPageRequest("2000", "forward", "", "2026-08-22T16:00:00Z~3000"); err == nil {
		t.Fatal("expected an unserviceable equal-timestamp cursor rejection")
	}
}

func TestNormalizeJobLogPageRequestKeepsLegacyDirectionForward(t *testing.T) {
	request, err := NormalizeJobLogPageRequest("1000", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if request.Direction != observability.LogDirectionForward {
		t.Fatalf("legacy direction=%q, want forward", request.Direction)
	}
}
