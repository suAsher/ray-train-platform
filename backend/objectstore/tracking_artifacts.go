package objectstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strconv"

	artifacts "ray-train-platform-backend/trackingartifacts"
)

type trackingArtifactObjects struct{ store *TOSStore }

func (s *TOSStore) TrackingArtifacts() artifacts.Objects { return &trackingArtifactObjects{store: s} }
func trackingArtifactKey(id string, index int) (string, error) {
	if !artifacts.ValidID(id) || index < 1 || index > artifacts.MaxParts {
		return "", artifacts.ErrInvalid
	}
	return "raytrain-mlflow-artifacts/" + id + "/parts/" + strconv.Itoa(index), nil
}
func (o *trackingArtifactObjects) Put(ctx context.Context, id string, index int, sha string, data []byte) error {
	key, err := trackingArtifactKey(id, index)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(data)
	if len(data) < 1 || int64(len(data)) > artifacts.PartSizeBytes || sha != hex.EncodeToString(sum[:]) {
		return artifacts.ErrInvalid
	}
	if o == nil || o.store == nil {
		return artifacts.ErrUnavailable
	}
	client, ok := o.store.client.(interface {
		Put(context.Context, tosPutRequest) error
	})
	if !ok {
		return artifacts.ErrUnavailable
	}
	err = client.Put(ctx, tosPutRequest{Bucket: o.store.bucket, Key: key, SHA256: sha, SizeBytes: int64(len(data)), ContentType: "application/octet-stream", Body: bytes.NewReader(data)})
	if err == nil {
		return nil
	}
	if !errors.Is(err, ErrAlreadyExists) {
		return artifacts.ErrUnavailable
	}
	// A previously committed object may have outlived a DB transaction. Verify
	// its immutable metadata before allowing the retry to register that part.
	head, err := o.store.client.Head(ctx, o.store.bucket, key)
	if err != nil {
		return artifacts.ErrUnavailable
	}
	if head.SizeBytes != int64(len(data)) || head.Metadata["sha256"] != sha {
		return artifacts.ErrConflict
	}
	return nil
}
func (o *trackingArtifactObjects) Get(ctx context.Context, id string, index int) (io.ReadCloser, int64, error) {
	key, err := trackingArtifactKey(id, index)
	if err != nil {
		return nil, 0, err
	}
	if o == nil || o.store == nil {
		return nil, 0, artifacts.ErrUnavailable
	}
	client, ok := o.store.client.(tosArtifactReadClient)
	if !ok {
		return nil, 0, artifacts.ErrUnavailable
	}
	result, err := client.ReadArtifact(ctx, tosArtifactReadRequest{Bucket: o.store.bucket, Key: key})
	if err != nil {
		return nil, 0, artifacts.ErrUnavailable
	}
	if result.Content == nil || result.SizeBytes < 1 || result.SizeBytes > artifacts.PartSizeBytes {
		if result.Content != nil {
			result.Content.Close()
		}
		return nil, 0, artifacts.ErrUnavailable
	}
	return result.Content, result.SizeBytes, nil
}
func (o *trackingArtifactObjects) Delete(ctx context.Context, id string, index int) error {
	key, err := trackingArtifactKey(id, index)
	if err != nil {
		return err
	}
	if o == nil || o.store == nil {
		return artifacts.ErrUnavailable
	}
	client, ok := o.store.client.(tosDeleteClient)
	if !ok {
		return artifacts.ErrUnavailable
	}
	err = client.DeleteObject(ctx, o.store.bucket, key)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return artifacts.ErrUnavailable
	}
	return nil
}
