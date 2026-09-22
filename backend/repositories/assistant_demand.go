package repositories

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"ray-train-platform-backend/domain"
)

const (
	assistantDemandRecordLimit  = 512
	assistantDemandMaxSpecBytes = 64 * 1024
)

var assistantDemandJobStates = []string{
	string(domain.StateSubmitted),
	string(domain.StateValidating),
	string(domain.StateQueued),
	string(domain.StateAdmitted),
	string(domain.StateProvisioning),
	string(domain.StateRecovering),
	// UNKNOWN can mean the DB still has an ACTIVE GPU job whose Kubernetes
	// object is missing or not yet observed. Count it conservatively as demand;
	// healthy RUNNING jobs remain excluded and are handled by Kubernetes state.
	string(domain.StateUnknown),
}

type assistantDemandJobRow struct {
	ID       string
	SpecJSON string `gorm:"column:spec_json"`
}

type assistantDemandWorkspaceRow struct {
	GPUCount int `gorm:"column:gpu_count"`
}

type AssistantDemandSnapshot struct {
	TrainingJobCount int       `json:"trainingJobCount"`
	WorkspaceCount   int       `json:"workspaceCount"`
	GPUCount         int       `json:"gpuCount"`
	HasDemand        bool      `json:"hasDemand"`
	ObservedAt       time.Time `json:"observedAt"`
}

// AggregateAssistantDemand reports only GPU work that has not reached a healthy
// Kubernetes-backed running state yet. Running training and workspaces remain
// covered by Kubernetes observation so idle inference can still use other free
// cards while a normal job is already running.
func (r *GormRepository) AggregateAssistantDemand(ctx context.Context) (AssistantDemandSnapshot, error) {
	observedAt := time.Now().UTC()
	var records []assistantDemandJobRow
	if err := r.db.WithContext(ctx).
		Model(&JobRecord{}).
		Select("id, spec_json").
		Where("desired_state = ? AND observed_state IN ? AND archived_at IS NULL", string(domain.DesiredActive), assistantDemandJobStates).
		Limit(assistantDemandRecordLimit + 1).
		Find(&records).Error; err != nil {
		return AssistantDemandSnapshot{}, fmt.Errorf("list pending assistant demand jobs: %w", err)
	}
	if len(records) > assistantDemandRecordLimit {
		return AssistantDemandSnapshot{}, fmt.Errorf("pending assistant demand jobs exceed limit %d", assistantDemandRecordLimit)
	}
	snapshot := AssistantDemandSnapshot{ObservedAt: observedAt}
	for _, record := range records {
		if len(record.SpecJSON) > assistantDemandMaxSpecBytes {
			return AssistantDemandSnapshot{}, fmt.Errorf("pending assistant demand job %q spec exceeds limit %d", record.ID, assistantDemandMaxSpecBytes)
		}
		var spec domain.JobSpec
		if err := json.Unmarshal([]byte(record.SpecJSON), &spec); err != nil {
			return AssistantDemandSnapshot{}, fmt.Errorf("decode pending assistant demand job %q spec: %w", record.ID, err)
		}
		gpus := spec.Resources.WorkerReplicas * spec.Resources.GPUsPerWorker
		if gpus <= 0 {
			continue
		}
		snapshot.TrainingJobCount++
		snapshot.GPUCount += gpus
	}

	var workspaces []assistantDemandWorkspaceRow
	if err := r.db.WithContext(ctx).
		Model(&WorkspaceRecord{}).
		Select("gpu_count").
		Where("observed_state = ? AND gpu_count > 0", string(domain.WorkspaceSubmitted)).
		Limit(assistantDemandRecordLimit + 1).
		Find(&workspaces).Error; err != nil {
		return AssistantDemandSnapshot{}, fmt.Errorf("list pending assistant demand workspaces: %w", err)
	}
	if len(workspaces) > assistantDemandRecordLimit {
		return AssistantDemandSnapshot{}, fmt.Errorf("pending assistant demand workspaces exceed limit %d", assistantDemandRecordLimit)
	}
	for _, workspace := range workspaces {
		snapshot.WorkspaceCount++
		snapshot.GPUCount += workspace.GPUCount
	}
	snapshot.HasDemand = snapshot.GPUCount > 0
	return snapshot, nil
}
