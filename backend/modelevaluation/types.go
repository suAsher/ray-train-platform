// Package modelevaluation defines immutable, independently reproducible model
// evaluations. Authorization and job submission belong to the API/orchestrator.
package modelevaluation

import (
	"encoding/json"
	"errors"
	"time"

	"ray-train-platform-backend/domain"
)

const (
	Protocol       = "model-evaluation-report/v1"
	MaxConfigBytes = 16 << 10
	MaxReportBytes = 256 << 10
	MaxMetrics     = 128
	MaxSlices      = 128
	MaxSites       = 128
	Creating       = "CREATING"
	Submitted      = "SUBMITTED"
	Running        = "RUNNING"
	Succeeded      = "SUCCEEDED"
	Failed         = "FAILED"
	Cancelled      = "CANCELLED"
	ReportPending  = "PENDING"
	ReportValid    = "VALID"
	ReportInvalid  = "INVALID"
	ReportMissing  = "MISSING"
	Public         = "PUBLIC"
	Team           = "TEAM"
	Higher         = "higher"
	Lower          = "lower"
	Neutral        = "neutral"
)

var (
	ErrInvalid      = errors.New("invalid model evaluation request or report")
	ErrConflict     = errors.New("model evaluation request conflicts")
	ErrNotFound     = errors.New("model evaluation not found")
	ErrUnauthorized = errors.New("model evaluation access denied")
	ErrQuota        = errors.New("model evaluation quota exceeded")
	ErrUnavailable  = errors.New("model evaluation service unavailable")
	ErrNotReady     = errors.New("model evaluation is not ready")
)

// Evaluator executable fields are immutable. Deactivation does not change
// historical Evaluation.Evaluator snapshots or invalidate their reports.
type Evaluator struct {
	ID             string    `json:"id"`
	Name           string    `json:"name"`
	Description    string    `json:"description"`
	OwnerID        string    `json:"ownerId"`
	OwnerName      string    `json:"ownerName"`
	TenantID       string    `json:"tenantId"`
	Revision       int64     `json:"revision"`
	Active         bool      `json:"active"`
	ImageReference string    `json:"imageReference"`
	ImageDigest    string    `json:"imageDigest"`
	GitURL         string    `json:"gitUrl"`
	GitCommit      string    `json:"gitCommit"`
	Code           *CodeSnapshot `json:"code,omitempty"`
	EntryPoint     []string  `json:"entryPoint"`
	SchemaVersion  string    `json:"schemaVersion"`
	Protocol       string    `json:"protocol"`
	CreatedAt      time.Time `json:"createdAt"`
}

type DatasetSnapshot struct {
	ID             string   `json:"id"`
	VersionID      string   `json:"versionId"`
	ManifestSHA256 string   `json:"manifestSha256"`
	SchemaVersion  string   `json:"schemaVersion"`
	Split          string   `json:"split"`
	Sites          []string `json:"sites"`
	SampleCount    int64    `json:"sampleCount"`
	Visibility     string   `json:"visibility"`
	TenantID       string   `json:"tenantId,omitempty"`
}

type Evaluation struct {
	ID             string           `json:"id"`
	ModelID        string           `json:"modelId"`
	VersionID      string           `json:"versionId"`
	ModelSHA256    string           `json:"modelSha256"`
	FileName       string           `json:"fileName"`
	Dataset        DatasetSnapshot  `json:"dataset"`
	Evaluator      Evaluator        `json:"evaluator"`
	Config         json.RawMessage  `json:"config"`
	ConfigSHA256   string           `json:"configSha256"`
	Resources      domain.Resources `json:"resources"`
	JobSpec        domain.JobSpec   `json:"-"`
	OwnerID        string           `json:"ownerId"`
	OwnerName      string           `json:"ownerName"`
	TenantID       string           `json:"tenantId"`
	JobID          string           `json:"jobId,omitempty"`
	State          string           `json:"state"`
	ReportState    string           `json:"reportState"`
	Report         *Report          `json:"report,omitempty"`
	ReportSHA256   string           `json:"reportSha256,omitempty"`
	Error          string           `json:"error,omitempty"`
	Revision       int64            `json:"revision"`
	CreatedAt      time.Time        `json:"createdAt"`
	UpdatedAt      time.Time        `json:"updatedAt"`
	FinishedAt     *time.Time       `json:"finishedAt,omitempty"`
	IdempotencyKey string           `json:"-"`
	RequestSHA256  string           `json:"-"`
}

// Request contains selectors only. The server resolves and freezes every
// provenance and ownership field before persisting an Evaluation.
type Request struct {
	ModelID          string           `json:"modelId"`
	VersionID        string           `json:"versionId"`
	DatasetID        string           `json:"datasetId"`
	DatasetVersionID string           `json:"datasetVersionId"`
	EvaluatorID      string           `json:"evaluatorId"`
	Split            string           `json:"split"`
	Sites            []string         `json:"sites"`
	Config           json.RawMessage  `json:"config"`
	Resources        domain.Resources `json:"resources"`
	IdempotencyKey   string           `json:"-"`
}

type Metric struct {
	Name      string  `json:"name"`
	Value     float64 `json:"value"`
	Unit      string  `json:"unit"`
	Direction string  `json:"direction"`
}

type Slice struct {
	Name        string   `json:"name"`
	SampleCount int64    `json:"sampleCount"`
	Metrics     []Metric `json:"metrics"`
}

type Report struct {
	Protocol              string   `json:"protocol"`
	EvaluationID          string   `json:"evaluationId"`
	ModelSHA256           string   `json:"modelSha256"`
	DatasetManifestSHA256 string   `json:"datasetManifestSha256"`
	EvaluatorID           string   `json:"evaluatorId"`
	ConfigSHA256          string   `json:"configSha256"`
	Metrics               []Metric `json:"metrics"`
	Slices                []Slice  `json:"slices,omitempty"`
}

type MetricComparison struct {
	Name      string  `json:"name"`
	Unit      string  `json:"unit"`
	Direction string  `json:"direction"`
	Left      float64 `json:"left"`
	Right     float64 `json:"right"`
	Delta     float64 `json:"delta"`
	Outcome   string  `json:"outcome"` // improved, regressed, equal, or neutral (right relative to left).
}

type Comparison struct {
	Comparable bool               `json:"comparable"`
	Reasons    []string           `json:"reasons"`
	LeftID     string             `json:"leftId"`
	RightID    string             `json:"rightId"`
	Metrics    []MetricComparison `json:"metrics"`
}

// Filter is passed only after authenticating the caller. Visibility is enforced
// by the repository before pagination, never by filtering already paged rows.
type Filter struct {
	TenantID   string
	SuperAdmin bool
	OwnerID    string
	ModelID    string
	VersionID  string
	State      string
	Cursor     string
	Limit      int
}
type EvaluatorPage struct {
	Items      []Evaluator `json:"items"`
	NextCursor string      `json:"nextCursor,omitempty"`
}
type EvaluationPage struct {
	Items      []Evaluation `json:"items"`
	NextCursor string       `json:"nextCursor,omitempty"`
}
