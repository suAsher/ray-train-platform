package api

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"ray-train-platform-backend/observability"
)

type diagnosisLogProvider interface {
	QueryJobDiagnosisCandidates(context.Context, string, int, time.Time, time.Time) ([]observability.LogLine, error)
}

type diagnosisCompletionProvider interface {
	QueryJobDiagnosisCompletions(context.Context, string, int, time.Time, time.Time) ([]observability.LogLine, error)
}

type diagnosisFollowupProvider interface {
	QueryJobDiagnosisFollowups(context.Context, string, int, time.Time, time.Time) ([]observability.LogLine, error)
}

type diagnosisContextProvider interface {
	QueryJobDiagnosisContext(context.Context, string, int, time.Time, time.Time, observability.LogDirection, map[string]string) ([]observability.LogLine, error)
}

func (h *Handler) getJobDiagnosis(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	principal, ok := h.principal(c)
	if !ok {
		h.writeError(c, http.StatusUnauthorized, "AUTH_REQUIRED", "authentication is required")
		return
	}
	job, err := h.jobForPrincipal(c.Request.Context(), principal, c.Param("id"))
	if err != nil {
		h.writeError(c, http.StatusNotFound, "JOB_NOT_FOUND", "training job was not found")
		return
	}
	start, end := JobLogQueryWindow(*job, time.Now())
	base := observability.JobDiagnosis{
		JobID: job.ID, ObservedState: string(job.ObservedState), StatusReason: job.StatusReason, StatusMessage: job.StatusMessage,
		Coverage: observability.DiagnosisCoverage{WindowStart: start, WindowEnd: end},
	}
	// No full-log fallback: unsupported providers must remain explicit and cheap.
	provider, available := h.logs.(diagnosisLogProvider)
	if !available { base.Coverage.LogUnavailable = true; h.writeSuccess(c, http.StatusOK, observability.AnalyzeDiagnosis(base, nil, nil, nil)); return }
	ctx, cancel := context.WithTimeout(c.Request.Context(), 8*time.Second)
	defer cancel()
	candidates, err := provider.QueryJobDiagnosisCandidates(ctx, job.ID, observability.DiagnosisCandidateLimit, start, end)
	if err != nil { base.Coverage.LogUnavailable = true; h.writeSuccess(c, http.StatusOK, observability.AnalyzeDiagnosis(base, nil, nil, nil)); return }
	diagnosis := observability.AnalyzeDiagnosis(base, candidates, nil, nil)
	diagnosis, completions, followups := h.queryDiagnosisSupplement(ctx, diagnosis)
	diagnosis = observability.AnalyzeDiagnosis(diagnosis, candidates, completions, followups)
	diagnosis = h.queryDiagnosisContext(ctx, diagnosis)
	h.writeSuccess(c, http.StatusOK, diagnosis)
}

func (h *Handler) queryDiagnosisSupplement(ctx context.Context, diagnosis observability.JobDiagnosis) (observability.JobDiagnosis, []observability.LogLine, []observability.LogLine) {
	var completions, followups []observability.LogLine
	start, end := diagnosis.Coverage.WindowStart, diagnosis.Coverage.WindowEnd
	completionEnd := end
	if diagnosis.FirstFailure != nil { completionEnd = diagnosis.FirstFailure.Timestamp }
	if provider, ok := h.logs.(diagnosisCompletionProvider); ok {
		var err error
		completions, err = provider.QueryJobDiagnosisCompletions(ctx, diagnosis.JobID, observability.DiagnosisCompletionLimit, start, completionEnd)
		diagnosis.Coverage.CompletionUnavailable = err != nil
		if err != nil { completions = nil }
	}
	if diagnosis.FirstFailure != nil {
		if provider, ok := h.logs.(diagnosisFollowupProvider); ok {
			var err error
			followups, err = provider.QueryJobDiagnosisFollowups(ctx, diagnosis.JobID, observability.DiagnosisFollowupLimit, diagnosis.FirstFailure.Timestamp, end)
			diagnosis.Coverage.FollowupsUnavailable = err != nil
			if err != nil { followups = nil }
			if len(followups) >= observability.DiagnosisFollowupLimit { diagnosis.Coverage.Truncated = true }
		}
	}
	return diagnosis, completions, followups
}

func (h *Handler) queryDiagnosisContext(ctx context.Context, diagnosis observability.JobDiagnosis) observability.JobDiagnosis {
	if diagnosis.FirstFailure == nil { return diagnosis }
	first := diagnosis.FirstFailure
	var lines []observability.LogLine
	for _, direction := range []observability.LogDirection{observability.LogDirectionBackward, observability.LogDirectionForward} {
		start, end := first.Timestamp.Add(-time.Minute), first.Timestamp
		if direction == observability.LogDirectionForward { start, end = first.Timestamp, first.Timestamp.Add(time.Minute) }
		if start.Before(diagnosis.Coverage.WindowStart) { start = diagnosis.Coverage.WindowStart }
		if end.After(diagnosis.Coverage.WindowEnd) { end = diagnosis.Coverage.WindowEnd }
		page, err := h.diagnosisContextPage(ctx, diagnosis.JobID, start, end, direction, first.Stream)
		if err != nil { diagnosis.Coverage.ContextUnavailable = true; continue }
		if len(page) >= observability.DiagnosisContextLimit/2 { diagnosis.Coverage.Truncated = true }
		if len(page) > observability.DiagnosisContextLimit/2 { page = page[:observability.DiagnosisContextLimit/2] }
		lines = append(lines, page...)
	}
	return observability.WithDiagnosisContext(diagnosis, lines)
}

func (h *Handler) diagnosisContextPage(ctx context.Context, jobID string, start, end time.Time, direction observability.LogDirection, stream map[string]string) ([]observability.LogLine, error) {
	if provider, ok := h.logs.(diagnosisContextProvider); ok {
		return provider.QueryJobDiagnosisContext(ctx, jobID, observability.DiagnosisContextLimit/2, start, end, direction, stream)
	}
	if provider, ok := h.logs.(paginatedLogProvider); ok {
		return provider.QueryJobLogsPage(ctx, jobID, observability.DiagnosisContextLimit/2, start, end, direction)
	}
	return nil, context.Canceled
}
