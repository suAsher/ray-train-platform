package api

import (
	"context"
	"time"

	"ray-train-platform-backend/domain"
	"ray-train-platform-backend/observability"
)

const (
	jobLogStartPadding = 10 * time.Minute
	jobLogEndPadding   = 5 * time.Minute
)

// LifecycleLogProvider supports a bounded Loki range query. The basic
// LogProvider contract remains supported for test doubles and other backends.
type LifecycleLogProvider interface {
	LogProvider
	QueryJobLogsInRange(context.Context, string, int, time.Time, time.Time) ([]observability.LogLine, error)
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
		return lifecycleProvider.QueryJobLogsInRange(ctx, job.ID, limit, start, end)
	}
	return provider.QueryJobLogs(ctx, job.ID, limit)
}
