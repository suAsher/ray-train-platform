package objectstore

import (
	"context"
	"errors"
	"io"
	"time"
)

var (
	ErrNotFound      = errors.New("object not found")
	ErrAlreadyExists = errors.New("object already exists")
	ErrUnavailable   = errors.New("object store unavailable")
)

type PresignedPut struct {
	URL             string
	RequiredHeaders map[string]string
	ContentLength   int64
	ExpiresAt       time.Time
}

type ObjectInfo struct {
	SizeBytes int64
	Metadata  map[string]string
}

type Store interface {
	PresignPut(context.Context, string, string, int64, time.Duration) (PresignedPut, error)
	Head(context.Context, string) (ObjectInfo, error)
	Put(context.Context, string, string, int64, io.Reader) error
}
