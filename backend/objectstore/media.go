package objectstore

import (
	"context"
	"errors"
	"time"
)

var ErrMediaChanged = errors.New("media object changed")

// MediaReader is optional: existing upload, preview and download stores need
// not implement it. Roots always come from platform authorization, never URLs.
type MediaReader interface {
	HeadMedia(context.Context, string, string) (MediaInfo, error)
	ReadMediaRange(context.Context, string, string, MediaInfo, int64, int64) (ArtifactRead, error)
}

type MediaInfo struct {
	SizeBytes    int64
	ETag         string
	LastModified time.Time
}
