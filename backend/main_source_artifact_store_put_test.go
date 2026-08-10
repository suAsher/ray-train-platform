package main

import (
	"context"
	"io"
)

func (*mainArtifactStore) Put(context.Context, string, string, int64, io.Reader) error { return nil }
