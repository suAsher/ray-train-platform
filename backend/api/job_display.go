package api

import (
	"context"
	"log"

	"ray-train-platform-backend/domain"
	"ray-train-platform-backend/observability"
)

type jobUsernameReader interface {
	JobUsernames(context.Context, []string) (map[string]string, error)
}

func (h *Handler) jobUsernames(ctx context.Context, ids []string) map[string]string {
	reader, ok := h.repository.(jobUsernameReader)
	if !ok || len(ids) == 0 {
		return nil
	}
	names, err := reader.JobUsernames(ctx, ids)
	if err != nil {
		log.Printf("job submitter display names unavailable; returning stable user IDs")
		return nil
	}
	return names
}

// Only IDs from an already-authorized response reach the optional display
// lookup. It never changes source ownership or exposes a user directory.
func (h *Handler) jobsWithUsernames(ctx context.Context, jobs []domain.TrainingJob) []domain.TrainingJob {
	ids := make([]string, 0, len(jobs))
	for _, job := range jobs {
		ids = append(ids, job.UserID)
	}
	names := h.jobUsernames(ctx, ids)
	result := append([]domain.TrainingJob{}, jobs...)
	for index, job := range result {
		result[index].Username = names[job.UserID]
	}
	return result
}

func (h *Handler) experimentsWithUsernames(ctx context.Context, catalog observability.ExperimentCatalog) observability.ExperimentCatalog {
	ids := make([]string, 0, len(catalog.Runs))
	for _, run := range catalog.Runs {
		ids = append(ids, run.SubmitterUserID)
	}
	names := h.jobUsernames(ctx, ids)
	result := catalog
	result.Runs = append([]observability.ExperimentRunSummary{}, catalog.Runs...)
	for index, run := range result.Runs {
		result.Runs[index].SubmitterUsername = names[run.SubmitterUserID]
	}
	return result
}
