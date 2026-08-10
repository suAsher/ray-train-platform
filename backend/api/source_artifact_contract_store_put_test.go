package api

import (
	"context"
	"io"
)

func (*ttlCapturingArtifactStore) Put(context.Context, string, string, int64, io.Reader) error {
	return nil
}
