package objectstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strconv"
	"strings"

	"ray-train-platform-backend/modellifecycle"
)

type modelSnapshotSource struct{ store *TOSStore }
type modelSnapshotObjects struct{ store *TOSStore }

func (s *TOSStore) ModelSnapshotSource() modellifecycle.Source {
	return &modelSnapshotSource{store: s}
}

func (s *TOSStore) ModelSnapshotObjects() modellifecycle.Objects {
	return &modelSnapshotObjects{store: s}
}

// Read preserves the task artifact reader's server-selected root and path
// boundary. This adapter exposes no operation that can modify a task output.
func (s *modelSnapshotSource) Read(ctx context.Context, taskRoot, relativePath string) (io.ReadCloser, int64, string, error) {
	if err := ctx.Err(); err != nil {
		return nil, 0, "", err
	}
	// A snapshot must identify exactly one file; reject normalization aliases
	// such as encoded separators or trailing directory slashes.
	if _, err := cleanPublicationObjectKey(relativePath); err != nil {
		return nil, 0, "", modellifecycle.ErrInvalid
	}
	if s == nil || s.store == nil {
		return nil, 0, "", modellifecycle.ErrUnavailable
	}
	result, err := s.store.ReadArtifact(ctx, taskRoot, relativePath)
	if err != nil {
		switch {
		case errors.Is(err, ErrNotFound):
			return nil, 0, "", modellifecycle.ErrNotFound
		case errors.Is(err, ErrUnavailable):
			return nil, 0, "", modellifecycle.ErrUnavailable
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			return nil, 0, "", err
		default:
			return nil, 0, "", modellifecycle.ErrInvalid
		}
	}
	if result.SizeBytes < 1 || result.SizeBytes > modellifecycle.MaxFileSize {
		_ = result.Content.Close()
		return nil, 0, "", modellifecycle.ErrInvalid
	}
	if strings.TrimSpace(result.ETag) == "" {
		_ = result.Content.Close()
		return nil, 0, "", modellifecycle.ErrUnavailable
	}
	// The identity token comes from the GET that opened this exact stream.
	// A separate HEAD would leave a race between metadata and content reads.
	return result.Content, result.SizeBytes, result.ETag, nil
}

func modelSnapshotKey(id string, index int) (string, error) {
	if !modellifecycle.ValidID(id) || index < 0 || int64(index) >= modellifecycle.MaxFileSize/modellifecycle.PartSize {
		return "", modellifecycle.ErrInvalid
	}
	return "raytrain-model-snapshots/" + id + "/parts/" + strconv.Itoa(index), nil
}

func (o *modelSnapshotObjects) Put(ctx context.Context, id string, index int, sha string, data []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	key, err := modelSnapshotKey(id, index)
	if err != nil {
		return err
	}
	if len(data) < 1 || int64(len(data)) > modellifecycle.PartSize || len(sha) != sha256.Size*2 {
		return modellifecycle.ErrInvalid
	}
	sum := sha256.Sum256(data)
	if sha != hex.EncodeToString(sum[:]) {
		return modellifecycle.ErrInvalid
	}
	if o == nil || o.store == nil {
		return modellifecycle.ErrUnavailable
	}
	client, ok := o.store.client.(interface {
		Put(context.Context, tosPutRequest) error
	})
	if !ok {
		return modellifecycle.ErrUnavailable
	}
	// sdkTOSClient.Put sets ForbidOverwrite and If-None-Match. Never use
	// PutData, CopyObject, or a user-provided output key for model snapshots.
	err = client.Put(ctx, tosPutRequest{
		Bucket: o.store.bucket, Key: key, SHA256: sha, SizeBytes: int64(len(data)),
		ContentType: "application/octet-stream", Body: bytes.NewReader(data),
	})
	if err == nil {
		return nil
	}
	if !errors.Is(err, ErrAlreadyExists) {
		return modellifecycle.ErrUnavailable
	}
	// A retry after a database failure may find a committed immutable object.
	// Only matching content metadata makes that retry idempotent.
	head, err := o.store.client.Head(ctx, o.store.bucket, key)
	if err != nil {
		return modellifecycle.ErrUnavailable
	}
	if head.SizeBytes != int64(len(data)) || head.Metadata["sha256"] != sha {
		return modellifecycle.ErrConflict
	}
	return nil
}

func (o *modelSnapshotObjects) Get(ctx context.Context, id string, index int) (io.ReadCloser, int64, error) {
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	key, err := modelSnapshotKey(id, index)
	if err != nil {
		return nil, 0, err
	}
	if o == nil || o.store == nil {
		return nil, 0, modellifecycle.ErrUnavailable
	}
	client, ok := o.store.client.(tosArtifactReadClient)
	if !ok {
		return nil, 0, modellifecycle.ErrUnavailable
	}
	result, err := client.ReadArtifact(ctx, tosArtifactReadRequest{Bucket: o.store.bucket, Key: key})
	if err != nil {
		return nil, 0, modellifecycle.ErrUnavailable
	}
	if result.Content == nil || result.SizeBytes < 1 || result.SizeBytes > modellifecycle.PartSize {
		if result.Content != nil {
			_ = result.Content.Close()
		}
		return nil, 0, modellifecycle.ErrUnavailable
	}
	return &modelSnapshotSizedReader{ReadCloser: result.Content, remaining: result.SizeBytes}, result.SizeBytes, nil
}

// The service verifies the actual SHA-256 against its manifest. The object
// adapter additionally rejects streams that disagree with TOS Content-Length.
type modelSnapshotSizedReader struct {
	io.ReadCloser
	remaining int64
	err error
}

func (r *modelSnapshotSizedReader) Read(p []byte) (int, error) {
	if r.err != nil {
		return 0, r.err
	}
	if int64(len(p)) > r.remaining+1 {
		p = p[:r.remaining+1]
	}
	n, err := r.ReadCloser.Read(p)
	r.remaining -= int64(n)
	if r.remaining < 0 || (errors.Is(err, io.EOF) && r.remaining != 0) {
		r.err = modellifecycle.ErrUnavailable
		return n, r.err
	}
	if err != nil {
		r.err = err
	}
	return n, err
}
