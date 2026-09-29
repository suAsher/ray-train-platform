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
	limit = boundedDiagnosisLimit(limit, DiagnosisCompletionLimit)
	completionLimit := limit
	if completionLimit > diagnosisExplicitCompletionLimit {
		completionLimit = diagnosisExplicitCompletionLimit
	}
	// Sharded checkpoint writes can flood the tail after training completes.
	// Reserve an independent budget for explicit completion text so artifact
	// chatter cannot erase the evidence preceding a later launcher failure.
	completions, err := c.queryJobLogs(ctx, jobID, completionLimit, start, end, LogDirectionBackward, diagnosisCompletionFilter, nil, diagnosisLokiResponseBytes)
	checkpointLimit := limit - completionLimit
	if err != nil || checkpointLimit == 0 {
		return completions, err
	}
	checkpoints, err := c.queryJobLogs(ctx, jobID, checkpointLimit, start, end, LogDirectionBackward, diagnosisCheckpointFilter, nil, diagnosisLokiResponseBytes)
	if err != nil {
		return nil, err
	}
	return orderedDiagnosisLines(append(append([]LogLine(nil), completions...), checkpoints...)), nil
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
	if limit < 1 || limit > maximum {
		return maximum
	}
	return limit
}
