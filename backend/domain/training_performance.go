package domain

import "time"

// TrainingWorkloadRef is server-derived persisted workload identity. HTTP
// clients must never construct it from query parameters.
type TrainingWorkloadRef struct {
	JobID          string `json:"jobId"`
	Namespace      string `json:"namespace"`
	RayClusterName string `json:"rayClusterName"`
	RayJobName     string `json:"rayJobName"`
}

type TrainingMetricPoint struct {
	Timestamp time.Time `json:"timestamp"`
	Value     float64   `json:"value"`
}

type TrainingMetricSeries struct {
	Labels map[string]string     `json:"labels,omitempty"`
	Points []TrainingMetricPoint `json:"points"`
}

type TrainingWorkerPerformance struct {
	Rank     *int                             `json:"rank"`
	Pod      string                           `json:"pod"`
	Node     string                           `json:"node,omitempty"`
	GPU      string                           `json:"gpu,omitempty"`
	State    string                           `json:"state,omitempty"`
	Restarts *int                             `json:"restarts"`
	Step     *int64                           `json:"step"`
	Series   map[string][]TrainingMetricPoint `json:"series"`
	Summary  map[string]*float64              `json:"summary"`
}

type TrainingRecoveryPoint struct {
	At                 time.Time `json:"at"`
	ClusterAttempt     int       `json:"clusterAttempt"`
	RestartCount       int       `json:"restartCount"`
	ResumeCheckpointID string    `json:"resumeCheckpointId,omitempty"`
	CheckpointStep     *int64    `json:"checkpointStep,omitempty"`
}

type TrainingPerformance struct {
	Workload    TrainingWorkloadRef               `json:"workload"`
	Window      string                            `json:"window"`
	StepSeconds int                               `json:"stepSeconds"`
	StartedAt   time.Time                         `json:"startedAt"`
	EndedAt     time.Time                         `json:"endedAt"`
	Workers     []TrainingWorkerPerformance       `json:"workers"`
	Series      map[string][]TrainingMetricSeries `json:"series"`
	Summary     map[string]*float64               `json:"summary"`
	Recovery    []TrainingRecoveryPoint           `json:"recovery"`
}
