package api

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"ray-train-platform-backend/domain"
	"ray-train-platform-backend/observability"
)

const (
	jobLogStartPadding    = 10 * time.Minute
	jobLogEndPadding      = 5 * time.Minute
	DefaultJobLogPageSize = 2000
	MaxJobLogPageSize     = 2000
	logCursorSeparator    = "~"
)

// LifecycleLogProvider supports a bounded Loki range query. The basic
// LogProvider contract remains supported for test doubles and other backends.
type LifecycleLogProvider interface {
	LogProvider
	QueryJobLogsInRange(context.Context, string, int, time.Time, time.Time) ([]observability.LogLine, error)
}

type paginatedLogProvider interface {
	QueryJobLogsPage(context.Context, string, int, time.Time, time.Time, observability.LogDirection) ([]observability.LogLine, error)
}

type JobLogPageRequest struct {
	Limit        int
	Direction    observability.LogDirection
	Cursor       *time.Time
	CursorOffset int
}

type JobLogPage struct {
	Lines      []observability.LogLine
	Direction  observability.LogDirection
	Limit      int
	HasMore    bool
	NextCursor string
}

// NormalizeJobLogPageRequest validates the public query contract before Loki
// is called. One slot is reserved for has-more detection, keeping every
// internal request at or below Loki's 5,000-entry ceiling.
func NormalizeJobLogPageRequest(limitRaw, directionRaw, beforeRaw, afterRaw string) (JobLogPageRequest, error) {
	limit := DefaultJobLogPageSize
	if raw := strings.TrimSpace(limitRaw); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > MaxJobLogPageSize {
			return JobLogPageRequest{}, fmt.Errorf("limit must be between 1 and %d", MaxJobLogPageSize)
		}
		limit = parsed
	}
	direction := observability.LogDirection(strings.ToLower(strings.TrimSpace(directionRaw)))
	if direction == "" {
		direction = observability.LogDirectionForward
	}
	if direction != observability.LogDirectionForward && direction != observability.LogDirectionBackward {
		return JobLogPageRequest{}, fmt.Errorf("direction must be forward or backward")
	}
	beforeRaw = strings.TrimSpace(beforeRaw)
	afterRaw = strings.TrimSpace(afterRaw)
	if beforeRaw != "" && afterRaw != "" {
		return JobLogPageRequest{}, fmt.Errorf("before and after cannot be combined")
	}
	if beforeRaw != "" && direction != observability.LogDirectionBackward {
		return JobLogPageRequest{}, fmt.Errorf("before requires backward direction")
	}
	if afterRaw != "" && direction != observability.LogDirectionForward {
		return JobLogPageRequest{}, fmt.Errorf("after requires forward direction")
	}
	cursorRaw := beforeRaw
	if afterRaw != "" {
		cursorRaw = afterRaw
	}
	var cursor *time.Time
	cursorOffset := 0
	if cursorRaw != "" {
		timestampRaw := cursorRaw
		if separator := strings.LastIndex(cursorRaw, logCursorSeparator); separator > 0 {
			parsedOffset, offsetErr := strconv.Atoi(cursorRaw[separator+len(logCursorSeparator):])
			if offsetErr != nil || parsedOffset < 1 {
				return JobLogPageRequest{}, fmt.Errorf("log cursor is invalid")
			}
			cursorOffset = parsedOffset
			timestampRaw = cursorRaw[:separator]
		}
		parsed, err := time.Parse(time.RFC3339Nano, timestampRaw)
		if err != nil {
			return JobLogPageRequest{}, fmt.Errorf("log cursor must be RFC3339Nano")
		}
		normalized := parsed.UTC()
		cursor = &normalized
	}
	if cursorOffset > observability.MaxLogQueryEntries-limit-1 {
		return JobLogPageRequest{}, fmt.Errorf("too many log entries share the cursor timestamp; retry with a smaller limit")
	}
	return JobLogPageRequest{Limit: limit, Direction: direction, Cursor: cursor, CursorOffset: cursorOffset}, nil
}

// JobLogQueryWindow covers the period in which a job can emit logs, including a
// small startup/teardown buffer. A zero CreatedAt keeps the legacy retention
// window so corrupt or migrated records do not become invisible.
func JobLogQueryWindow(job domain.TrainingJob, now time.Time) (time.Time, time.Time) {
	now = now.UTC()
	if job.CreatedAt.IsZero() {
		return now.Add(-30 * 24 * time.Hour), now
	}
	start := job.CreatedAt.UTC().Add(-jobLogStartPadding)
	end := now
	if job.FinishedAt != nil && !job.FinishedAt.IsZero() {
		end = job.FinishedAt.UTC().Add(jobLogEndPadding)
	}
	if end.Before(start) {
		end = start.Add(jobLogEndPadding)
	}
	return start, end
}

// QueryJobLogsForLifecycle uses the job lifecycle to keep read latency bounded
// for both running and completed Ray jobs.
func QueryJobLogsForLifecycle(ctx context.Context, provider LogProvider, job domain.TrainingJob, limit int) ([]observability.LogLine, error) {
	if provider == nil {
		return nil, nil
	}
	if lifecycleProvider, ok := provider.(LifecycleLogProvider); ok {
		start, end := JobLogQueryWindow(job, time.Now())
		lines, err := lifecycleProvider.QueryJobLogsInRange(ctx, job.ID, limit, start, end)
		return sanitizeJobLogLines(lines), err
	}
	lines, err := provider.QueryJobLogs(ctx, job.ID, limit)
	return sanitizeJobLogLines(lines), err
}

// QueryJobLogPage returns a deterministic chronological page while allowing
// callers to walk either from the newest logs backward (Portal) or from job
// start forward (CLI export/follow).
func QueryJobLogPage(ctx context.Context, provider LogProvider, job domain.TrainingJob, request JobLogPageRequest, now time.Time) (JobLogPage, error) {
	page := JobLogPage{Lines: make([]observability.LogLine, 0), Direction: request.Direction, Limit: request.Limit}
	if provider == nil {
		return page, nil
	}
	if request.Limit < 1 || request.Limit > MaxJobLogPageSize {
		return page, fmt.Errorf("log page limit is invalid")
	}
	if request.Direction != observability.LogDirectionForward && request.Direction != observability.LogDirectionBackward {
		return page, fmt.Errorf("log page direction is invalid")
	}
	start, end := JobLogQueryWindow(job, now)
	if request.Cursor != nil {
		if request.Direction == observability.LogDirectionBackward {
			candidate := request.Cursor.UTC()
			if candidate.Before(end) {
				end = candidate
			}
		} else {
			candidate := request.Cursor.UTC()
			if candidate.After(start) {
				start = candidate
			}
		}
	}
	if end.Before(start) {
		return page, nil
	}
	queryLimit := request.Limit + request.CursorOffset + 1
	if queryLimit > observability.MaxLogQueryEntries {
		return page, fmt.Errorf("too many log entries share the cursor timestamp")
	}
	var (
		lines []observability.LogLine
		err   error
	)
	if paginated, ok := provider.(paginatedLogProvider); ok {
		lines, err = paginated.QueryJobLogsPage(ctx, job.ID, queryLimit, start, end, request.Direction)
	} else if lifecycle, ok := provider.(LifecycleLogProvider); ok {
		lines, err = lifecycle.QueryJobLogsInRange(ctx, job.ID, queryLimit, start, end)
	} else {
		lines, err = provider.QueryJobLogs(ctx, job.ID, queryLimit)
	}
	if err != nil {
		return page, err
	}
	ordered := append([]observability.LogLine(nil), lines...)
	// Preserve Loki's tie order. Reordering equal timestamps after Loki already
	// applied its limit would make a larger overlap page disagree with the
	// smaller page that produced the cursor.
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Timestamp.Before(ordered[j].Timestamp) })
	ordered = removeConsumedCursorLines(ordered, request)
	page.HasMore = len(ordered) > request.Limit
	if page.HasMore {
		if request.Direction == observability.LogDirectionBackward {
			ordered = append([]observability.LogLine(nil), ordered[len(ordered)-request.Limit:]...)
		} else {
			ordered = append([]observability.LogLine(nil), ordered[:request.Limit]...)
		}
	}
	// Cursor and has-more semantics must follow Loki's raw entries. Terminal
	// redraw noise may disappear from the response, but it still consumed a
	// position in the source stream and the next request must advance past it.
	page.Lines = sanitizeJobLogLines(ordered)
	if len(ordered) > 0 {
		cursorLine := ordered[len(ordered)-1]
		if request.Direction == observability.LogDirectionBackward {
			cursorLine = ordered[0]
		}
		consumed := countBoundaryLines(ordered, cursorLine.Timestamp)
		if request.Cursor != nil && request.Cursor.Equal(cursorLine.Timestamp) {
			consumed += request.CursorOffset
		}
		page.NextCursor = encodeLogCursor(cursorLine.Timestamp, consumed)
	}
	return page, nil
}

func encodeLogCursor(timestamp time.Time, consumed int) string {
	return timestamp.UTC().Format(time.RFC3339Nano) + logCursorSeparator + strconv.Itoa(consumed)
}

func removeConsumedCursorLines(lines []observability.LogLine, request JobLogPageRequest) []observability.LogLine {
	if request.Cursor == nil || request.CursorOffset == 0 || len(lines) == 0 {
		return lines
	}
	remaining := request.CursorOffset
	if request.Direction == observability.LogDirectionForward {
		index := 0
		for index < len(lines) && remaining > 0 && lines[index].Timestamp.Equal(*request.Cursor) {
			index++
			remaining--
		}
		return lines[index:]
	}
	index := len(lines)
	for index > 0 && remaining > 0 && lines[index-1].Timestamp.Equal(*request.Cursor) {
		index--
		remaining--
	}
	return lines[:index]
}

func countBoundaryLines(lines []observability.LogLine, timestamp time.Time) int {
	count := 0
	for _, line := range lines {
		if line.Timestamp.Equal(timestamp) {
			count++
		}
	}
	return count
}
