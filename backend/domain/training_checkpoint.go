package domain

import (
	"fmt"
	"math"
	"path"
	"regexp"
	"strings"
	"time"
)

type TrainingEventType string

const (
	TrainingEventWorkerGroupStarted TrainingEventType = "WORKER_GROUP_STARTED"
	TrainingEventCheckpointComplete TrainingEventType = "CHECKPOINT_COMPLETE"
	TrainingEventProgress           TrainingEventType = "TRAINING_PROGRESS"

	TrainingEventIDMaxBytes          = 128
	TrainingCheckpointIDMaxBytes     = 128
	TrainingCheckpointPathMaxBytes   = 4096
	TrainingCheckpointMetricMaxBytes = 128
	TrainingEventCounterMax          = int64(1_000_000_000_000)
)

var (
	trainingEventIDPattern      = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	trainingCheckpointIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	trainingManifestPattern     = regexp.MustCompile(`^[0-9a-f]{64}$`)
	trainingMetricPattern       = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,127}$`)
)

type TrainingCheckpoint struct {
	ID             string    `json:"id"`
	JobID          string    `json:"jobId"`
	TenantID       string    `json:"tenantId"`
	UserID         string    `json:"userId"`
	Epoch          int64     `json:"epoch"`
	Step           int64     `json:"step"`
	ObjectPath     string    `json:"objectPath"`
	MetricName     string    `json:"metricName,omitempty"`
	MetricValue    *float64  `json:"metricValue,omitempty"`
	Complete       bool      `json:"complete"`
	IsBest         bool      `json:"isBest"`
	ManifestSHA256 string    `json:"manifestSha256"`
	CreatedAt      time.Time `json:"createdAt"`
}

func (checkpoint TrainingCheckpoint) Validate() error {
	if !trainingCheckpointIDPattern.MatchString(checkpoint.ID) {
		return fmt.Errorf("checkpoint ID must be a safe identifier")
	}
	if checkpoint.Epoch < 0 || checkpoint.Epoch > TrainingEventCounterMax || checkpoint.Step < 0 || checkpoint.Step > TrainingEventCounterMax {
		return fmt.Errorf("checkpoint epoch and step are outside supported bounds")
	}
	if err := validateCheckpointPath(checkpoint.ObjectPath); err != nil {
		return err
	}
	if !checkpoint.Complete {
		return fmt.Errorf("checkpoint completion event must describe a complete checkpoint")
	}
	if !trainingManifestPattern.MatchString(checkpoint.ManifestSHA256) {
		return fmt.Errorf("checkpoint manifest SHA-256 must be 64 lowercase hexadecimal characters")
	}
	if checkpoint.MetricName != "" && !trainingMetricPattern.MatchString(checkpoint.MetricName) {
		return fmt.Errorf("checkpoint metric name is invalid")
	}
	if checkpoint.MetricValue != nil && (math.IsNaN(*checkpoint.MetricValue) || math.IsInf(*checkpoint.MetricValue, 0)) {
		return fmt.Errorf("checkpoint metric value must be finite")
	}
	return nil
}

func validateCheckpointPath(value string) error {
	if len(value) == 0 || len(value) > TrainingCheckpointPathMaxBytes || !strings.HasPrefix(value, "/") || path.Clean(value) != value || strings.ContainsAny(value, "\x00\r\n") {
		return fmt.Errorf("checkpoint object path must be a clean bounded absolute path")
	}
	return nil
}

type TrainingEvent struct {
	ID         string              `json:"eventId"`
	Type       TrainingEventType   `json:"type"`
	Generation int64               `json:"generation"`
	Epoch      int64               `json:"epoch,omitempty"`
	Step       int64               `json:"step,omitempty"`
	Checkpoint *TrainingCheckpoint `json:"checkpoint,omitempty"`
}

func (event TrainingEvent) Validate() error {
	if len(event.ID) > TrainingEventIDMaxBytes || !trainingEventIDPattern.MatchString(event.ID) {
		return fmt.Errorf("event ID must be a safe identifier")
	}
	if event.Generation < 1 || event.Generation > TrainingEventCounterMax {
		return fmt.Errorf("event generation must be between 1 and %d", TrainingEventCounterMax)
	}
	switch event.Type {
	case TrainingEventWorkerGroupStarted:
		if event.Checkpoint != nil {
			return fmt.Errorf("worker group event must not include a checkpoint")
		}
	case TrainingEventCheckpointComplete:
		if event.Checkpoint == nil {
			return fmt.Errorf("checkpoint completion event requires checkpoint metadata")
		}
		if err := event.Checkpoint.Validate(); err != nil {
			return err
		}
		if event.Epoch != event.Checkpoint.Epoch || event.Step != event.Checkpoint.Step {
			return fmt.Errorf("event and checkpoint epoch/step must match")
		}
	case TrainingEventProgress:
		if event.Checkpoint != nil {
			return fmt.Errorf("training progress event must not include a checkpoint")
		}
	default:
		return fmt.Errorf("unsupported training event type %q", event.Type)
	}
	if event.Epoch < 0 || event.Epoch > TrainingEventCounterMax || event.Step < 0 || event.Step > TrainingEventCounterMax {
		return fmt.Errorf("event epoch and step are outside supported bounds")
	}
	return nil
}

type TrainingEventResult struct {
	EventID            string `json:"eventId"`
	Replayed           bool   `json:"replayed"`
	CheckpointID       string `json:"checkpointId,omitempty"`
	WorkerRestartCount int    `json:"workerRestartCount"`
}
