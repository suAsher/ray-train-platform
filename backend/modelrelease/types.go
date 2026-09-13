// Package modelrelease freezes reviewed evaluation evidence and the production
// model pointer. Approval does not itself allocate serving resources.
package modelrelease

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	Pending  = "PENDING"
	Approved = "APPROVED"
	Rejected = "REJECTED"
)

var (
	ErrInvalid      = errors.New("invalid model release request")
	ErrNotFound     = errors.New("model release not found")
	ErrConflict     = errors.New("model release revision or idempotency conflict")
	ErrUnauthorized = errors.New("model release access denied")
	ErrNotReady     = errors.New("model release evidence is not ready")
)

type Actor struct {
	ID, Name, TenantID      string
	SuperAdmin, TenantAdmin bool
}
type Request struct {
	ModelID        string `json:"modelId"`
	VersionID      string `json:"versionId"`
	EvaluationID   string `json:"evaluationId"`
	Reason         string `json:"reason"`
	IdempotencyKey string `json:"-"`
}
type Decision struct {
	Revision int64  `json:"revision"`
	Approve  bool   `json:"approve"`
	Reason   string `json:"reason"`
}
type PublishRequest struct {
	ReleaseID      string `json:"releaseId"`
	Revision       int64  `json:"revision"`
	Reason         string `json:"reason"`
	IdempotencyKey string `json:"-"`
}
type Release struct {
	ID                string     `json:"id" gorm:"primaryKey"`
	ModelID           string     `json:"modelId"`
	VersionID         string     `json:"versionId"`
	ModelSHA256       string     `json:"modelSha256"`
	ModelOwnerID      string     `json:"modelOwnerId"`
	EvaluationID      string     `json:"evaluationId"`
	ReportSHA256      string     `json:"reportSha256"`
	DatasetVisibility string     `json:"datasetVisibility"`
	DatasetTenantID   string     `json:"-"`
	ApplicantID       string     `json:"applicantId"`
	ApplicantName     string     `json:"applicantName"`
	TenantID          string     `json:"tenantId"`
	Reason            string     `json:"reason"`
	State             string     `json:"state"`
	ReviewerID        string     `json:"reviewerId,omitempty"`
	ReviewerName      string     `json:"reviewerName,omitempty"`
	ReviewReason      string     `json:"reviewReason,omitempty"`
	Revision          int64      `json:"revision"`
	CreatedAt         time.Time  `json:"createdAt"`
	UpdatedAt         time.Time  `json:"updatedAt"`
	ReviewedAt        *time.Time `json:"reviewedAt,omitempty"`
	IdempotencyKey    string     `json:"-"`
	RequestSHA256     string     `json:"-"`
}

func (Release) TableName() string { return "model_releases" }

type Publication struct {
	ModelID   string    `json:"modelId" gorm:"primaryKey"`
	ReleaseID string    `json:"releaseId"`
	VersionID string    `json:"versionId"`
	Revision  int64     `json:"revision"`
	ActorID   string    `json:"actorId"`
	UpdatedAt time.Time `json:"updatedAt"`
}

func (Publication) TableName() string { return "model_publications" }

type History struct {
	ID                string    `json:"id" gorm:"primaryKey"`
	ModelID           string    `json:"modelId"`
	ReleaseID         string    `json:"releaseId"`
	VersionID         string    `json:"versionId"`
	PreviousReleaseID string    `json:"previousReleaseId,omitempty"`
	Revision          int64     `json:"revision"`
	ActorID           string    `json:"actorId"`
	ActorName         string    `json:"actorName"`
	Reason            string    `json:"reason"`
	CreatedAt         time.Time `json:"createdAt"`
	IdempotencyKey    string    `json:"-"`
	RequestSHA256     string    `json:"-"`
}

func (History) TableName() string { return "model_publication_history" }

type Filter struct {
	ModelID, VersionID, State, Cursor string
	Limit                             int
}
type Page struct {
	Items      []Release `json:"items"`
	NextCursor string    `json:"nextCursor,omitempty"`
}
type HistoryPage struct {
	Items      []History `json:"items"`
	NextCursor string    `json:"nextCursor,omitempty"`
}
type Repository interface {
	CreateRequest(context.Context, Request, Actor) (Release, error)
	GetRelease(context.Context, string, Actor) (Release, error)
	ListReleases(context.Context, Filter, Actor) (Page, error)
	Decide(context.Context, string, Decision, Actor) (Release, error)
	Publish(context.Context, string, PublishRequest, Actor) (Publication, error)
	GetPublication(context.Context, string, Actor) (Publication, error)
	ListHistory(context.Context, string, string, int, Actor) (HistoryPage, error)
}

func ValidReason(reason string) bool {
	return strings.TrimSpace(reason) != "" && utf8.RuneCountInString(reason) <= 4000
}
func ValidateRequest(r Request) error {
	if r.ModelID == "" || r.VersionID == "" || r.EvaluationID == "" || !ValidReason(r.Reason) || len(r.IdempotencyKey) < 1 || len(r.IdempotencyKey) > 128 {
		return ErrInvalid
	}
	return nil
}
func CanReview(r Release, a Actor) bool {
	return a.ID != "" && a.ID != r.ApplicantID && a.ID != r.ModelOwnerID && (a.SuperAdmin || (a.TenantAdmin && a.TenantID != "" && a.TenantID == r.TenantID))
}
