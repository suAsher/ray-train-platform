// Package modelserving defines immutable serving contracts and durable deployment
// reservations. The ordinary training submission path owns compute admission.
package modelserving

import (
	"context"
	"encoding/json"
	"errors"
	"ray-train-platform-backend/domain"
	me "ray-train-platform-backend/modelevaluation"
	"time"
)

const (
	Protocol             = "model-serving-http/v1"
	MaxInputExampleBytes = 16 << 10
	Creating             = "CREATING"
	Submitted            = "SUBMITTED"
	Ready                = "READY"
	Stopping             = "STOPPING"
	Stopped              = "STOPPED"
	Failed               = "FAILED"
	Expired              = "EXPIRED"
)

var (
	ErrInvalid      = errors.New("invalid model serving request")
	ErrNotFound     = errors.New("model serving resource not found")
	ErrConflict     = errors.New("model serving revision or request conflicts")
	ErrUnauthorized = errors.New("model serving access denied")
	ErrNotReady     = errors.New("model serving source is not ready")
	ErrQuota        = errors.New("model serving active deployment limit exceeded")
	ErrUnavailable  = errors.New("model serving unavailable")
)

type Contract struct {
	ID                string           `json:"id"`
	Name              string           `json:"name"`
	Description       string           `json:"description"`
	OwnerID           string           `json:"ownerId"`
	OwnerName         string           `json:"ownerName"`
	TenantID          string           `json:"tenantId"`
	Revision          int64            `json:"revision"`
	Active            bool             `json:"active"`
	ImageReference    string           `json:"imageReference"`
	ImageDigest       string           `json:"imageDigest"`
	Code              *me.CodeSnapshot `json:"code"`
	EntryPoint        []string         `json:"entryPoint"`
	InputExample      json.RawMessage  `json:"inputExample"`
	OutputDescription string           `json:"outputDescription"`
	CreatedAt         time.Time        `json:"createdAt"`
}
type Deployment struct {
	ID             string           `json:"id"`
	Name           string           `json:"name"`
	ReleaseID      string           `json:"releaseId"`
	ModelID        string           `json:"modelId"`
	VersionID      string           `json:"versionId"`
	ModelSHA256    string           `json:"modelSha256"`
	ModelSizeBytes int64            `json:"modelSizeBytes"`
	FileName       string           `json:"fileName"`
	OwnerID        string           `json:"ownerId"`
	OwnerName      string           `json:"ownerName"`
	TenantID       string           `json:"tenantId"`
	Contract       Contract         `json:"contract"`
	Resources      domain.Resources `json:"resources"`
	JobSpec        domain.JobSpec   `json:"-"`
	JobID          string           `json:"jobId"`
	State          string           `json:"state"`
	Error          string           `json:"error,omitempty"`
	Revision       int64            `json:"revision"`
	ExpiresAt      time.Time        `json:"expiresAt"`
	CreatedAt      time.Time        `json:"createdAt"`
	UpdatedAt      time.Time        `json:"updatedAt"`
	IdempotencyKey string           `json:"-"`
	RequestSHA256  string           `json:"-"`
}
type Filter struct {
	ModelID, OwnerID, State, Cursor string
	Limit                           int
}
type DeploymentPage struct {
	Items      []Deployment `json:"items"`
	NextCursor string       `json:"nextCursor,omitempty"`
}
type Store interface {
	CreateContract(context.Context, Contract) (Contract, error)
	GetContract(context.Context, string) (Contract, error)
	ListContracts(context.Context, bool) ([]Contract, error)
	SetContractActive(context.Context, string, bool, int64) (Contract, error)
	ReserveDeployment(context.Context, Deployment) (Deployment, bool, error)
	FindDeploymentRequest(context.Context, string, string, string) (Deployment, error)
	GetDeployment(context.Context, string) (Deployment, error)
	GetDeploymentByJobID(context.Context, string) (Deployment, error)
	ListDeployments(context.Context, Filter) (DeploymentPage, error)
	MarkDeploymentSubmitted(context.Context, string, string) error
	FailDeploymentSubmission(context.Context, string) error
	RequestStop(context.Context, string, string, int64) (Deployment, error)
	UpdateObserved(context.Context, string, string, int64) (Deployment, error)
	GetPendingDeployments(context.Context, int) ([]Deployment, error)
	AuthorizeServingJobToken(context.Context, string, []byte, time.Time) (Deployment, error)
}

func Terminal(state string) bool { return state == Stopped || state == Failed || state == Expired }
