package api

import (
	"context"
	"errors"
	"io"
)

func (f *fakeArtifactStore) Put(context.Context, string, string, int64, io.Reader) error {
	return errors.New("not used")
}
