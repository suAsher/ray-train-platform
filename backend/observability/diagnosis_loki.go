package observability

import (
	"context"
	"time"
)

const diagnosisLokiResponseBytes = 4 << 20

func (c *LokiClient) QueryJobDiagnosisCandidates(ctx context.Context, jobID string, limit int, start, end time.Time) ([]LogLine, error) {
	return c.queryJobLogs(ctx, jobID, boundedDiagnosisLimit(limit, DiagnosisCandidateLimit), start, end, LogDirectionForward, diagnosisFailureFilter, nil, diagnosisLokiResponseBytes)
}

func (c *LokiClient) QueryJobDiagnosisCompletions(ctx context.Context, jobID string, limit int, start, end time.Time) ([]LogLine, error) {
	return c.queryJobLogs(ctx, jobID, boundedDiagnosisLimit(limit, DiagnosisCompletionLimit), start, end, LogDirectionBackward, diagnosisCompletionFilter, nil, diagnosisLokiResponseBytes)
}

func (c *LokiClient) QueryJobDiagnosisFollowups(ctx context.Context, jobID string, limit int, start, end time.Time) ([]LogLine, error) {
	return c.queryJobLogs(ctx, jobID, boundedDiagnosisLimit(limit, DiagnosisFollowupLimit), start, end, LogDirectionBackward, diagnosisFollowupFilter, nil, diagnosisLokiResponseBytes)
}

// Context uses exact labels from the first evidence stream. Label values are
// quoted by the shared Loki query builder, never inserted as LogQL syntax.
func (c *LokiClient) QueryJobDiagnosisContext(ctx context.Context, jobID string, limit int, start, end time.Time, direction LogDirection, stream map[string]string) ([]LogLine, error) {
	return c.queryJobLogs(ctx, jobID, boundedDiagnosisLimit(limit, DiagnosisContextLimit/2), start, end, direction, "", stream, diagnosisLokiResponseBytes)
}

func boundedDiagnosisLimit(limit, maximum int) int {
	if limit < 1 || limit > maximum { return maximum }
	return limit
}
