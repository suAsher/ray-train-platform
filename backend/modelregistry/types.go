// Package modelregistry links immutable platform checkpoints to native MLflow.
// A checkpoint link does not imply an MLflow flavor or a deployable model.
package modelregistry

import (
	"context"
	"errors"
	"io"

	ml "ray-train-platform-backend/modellifecycle"
)

var (
	ErrInvalid     = errors.New("invalid model registry request")
	ErrConflict    = errors.New("model registry association conflicts")
	ErrUnavailable = errors.New("model registry is unavailable")
	ErrIntegrity   = errors.New("model registry checkpoint integrity check failed")
)

type Link struct {
	RegisteredName string `json:"registeredName"`
	Version        string `json:"version"`
	RunID          string `json:"runId"`
	SourceURI      string `json:"sourceUri"`
}

// OpenSnapshot must return a fresh reader for this version's immutable snapshot.
// The caller authorizes access and holds a renewable database lease for the
// complete operation. MLflow's create-version API is not itself idempotent.
type OpenSnapshot func(context.Context) (io.ReadCloser, error)

type Provider interface {
	EnsureVersion(context.Context, ml.Model, ml.Version, OpenSnapshot) (Link, error)
}
