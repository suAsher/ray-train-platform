package modelregistry

import (
	"context"
	"time"
)

type Record struct {
	VersionID       string     `json:"versionId" gorm:"primaryKey"`
	State           string     `json:"state"`
	RegisteredName  string     `json:"registeredName"`
	RegistryVersion string     `json:"registryVersion"`
	RunID           string     `json:"runId"`
	SourceURI       string     `json:"sourceUri"`
	Error           string     `json:"error,omitempty"`
	LeaseID         string     `json:"-"`
	LeaseExpiresAt  *time.Time `json:"-"`
	Revision        int64      `json:"revision"`
	CreatedAt       time.Time  `json:"createdAt"`
	UpdatedAt       time.Time  `json:"updatedAt"`
}

func (Record) TableName() string { return "model_registry_links" }

// LeaseDuration exceeds the bounded worker lifetime, so a crashed process cannot
// overlap a retried worker while its last upstream HTTP operation is completing.
const LeaseDuration = 32 * time.Minute
const WorkerTimeout = 30 * time.Minute

type Store interface {
	Get(context.Context, string) (Record, error)
	Acquire(context.Context, string, string, string, bool) (Record, bool, error)
	Renew(context.Context, string, string) error
	Complete(context.Context, string, string, Link, string) error
}
