// Package mlflowtracking governs external tracking records independently of
// scheduled training jobs. The platform store, never MLflow tags, owns access.
package mlflowtracking

import (
	"context"
	"errors"
	"time"
)

var (
	ErrInvalid     = errors.New("invalid tracking request")
	ErrNotFound    = errors.New("tracking record not found")
	ErrConflict    = errors.New("tracking state conflicts with request")
	ErrBusy        = errors.New("tracking mutation is in progress")
	ErrPending     = errors.New("tracking operation awaits reconciliation")
	ErrUnavailable = errors.New("tracking service is unavailable")
)

type Actor struct {
	TenantID string
	UserID   string
}

type Experiment struct {
	ID              string    `json:"id"`
	TenantID        string    `json:"-"`
	UserID          string    `json:"-"`
	IdempotencyHash string    `json:"-"`
	Name            string    `json:"name"`
	State           string    `json:"state"`
	UpstreamID      string    `json:"mlflowExperimentId,omitempty"`
	CreatedAt       time.Time `json:"createdAt"`
	UpdatedAt       time.Time `json:"updatedAt"`
}

type Run struct {
	ID              string     `json:"id"`
	ExperimentID    string     `json:"experimentId"`
	TenantID        string     `json:"-"`
	UserID          string     `json:"-"`
	IdempotencyHash string     `json:"-"`
	Name            string     `json:"name"`
	State           string     `json:"state"`
	UpstreamID      string     `json:"mlflowRunId,omitempty"`
	StartTimeMS     int64      `json:"startTimeMs"`
	EndTimeMS       int64      `json:"endTimeMs,omitempty"`
	CreatedAt       time.Time  `json:"createdAt"`
	UpdatedAt       time.Time  `json:"updatedAt"`
	FinishedAt      *time.Time `json:"finishedAt,omitempty"`
	FinishStatus    string     `json:"-"`
	LeaseID         string     `json:"-"`
	LeaseExpiresAt  *time.Time `json:"-"`
}

type Pair struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}
type Metric struct {
	Key       string   `json:"key"`
	Value     *float64 `json:"value"`
	Timestamp *int64   `json:"timestamp"`
	Step      *int64   `json:"step"`
}
type Batch struct {
	Metrics []Metric `json:"metrics,omitempty"`
	Params  []Pair   `json:"params,omitempty"`
	Tags    []Pair   `json:"tags,omitempty"`
}
type MetricPoint struct {
	Value       float64 `json:"value"`
	TimestampMS int64   `json:"timestampMs"`
	Step        int64   `json:"step"`
}
type MetricSeries struct {
	Key    string        `json:"key"`
	Points []MetricPoint `json:"points"`
}
type Snapshot struct {
	Status                 string
	StartTimeMS, EndTimeMS int64
	Latest                 map[string]float64
	LatestMetrics          map[string]MetricPoint
	Params                 map[string]string
	Tags                   map[string]string
	Series                 []MetricSeries
}
type RunDetail struct {
	Run           Run                    `json:"run"`
	Latest        map[string]float64     `json:"latest"`
	LatestMetrics map[string]MetricPoint `json:"latestMetrics"`
	Params        map[string]string      `json:"params"`
	Tags          map[string]string      `json:"tags"`
	Series        []MetricSeries         `json:"series"`
}
type ExperimentPage struct {
	Items      []Experiment `json:"items"`
	NextCursor string       `json:"nextCursor,omitempty"`
}
type RunPage struct {
	Items      []Run  `json:"items"`
	NextCursor string `json:"nextCursor,omitempty"`
}

type Provider interface {
	CreateExperiment(context.Context, string) (string, error)
	FindExperiment(context.Context, string) (string, bool, error)
	CreateRun(context.Context, string, string, string) (string, error)
	FindRun(context.Context, string, string) (string, bool, error)
	ReadRun(context.Context, string, string, string) (Snapshot, error)
	LogRun(context.Context, string, string, string, Batch) error
	FinishRun(context.Context, string, string, string, string, int64) error
}

// Reserve methods commit the durable operation before returning claimed=true.
// Lease methods use short DB transactions; no transaction spans Provider I/O.
type Store interface {
	ReserveExperiment(context.Context, Experiment) (Experiment, bool, error)
	CompleteExperiment(context.Context, Actor, string, string) (Experiment, error)
	GetExperiment(context.Context, Actor, string) (Experiment, error)
	ListExperiments(context.Context, Actor, string, int) ([]Experiment, error)
	ReserveRun(context.Context, Run) (Run, bool, error)
	CompleteRun(context.Context, Actor, string, string) (Run, error)
	GetRun(context.Context, Actor, string) (Run, error)
	ListRuns(context.Context, Actor, string, string, int) ([]Run, error)
	ClaimRunLease(context.Context, Actor, string, string, string, time.Time, time.Time, int64) (Run, error)
	ReleaseRunLease(context.Context, Actor, string, string, string, time.Time) (Run, error)
}

type Options struct {
	CursorKey []byte
	NewID     func() (string, error)
	Now       func() time.Time
}
