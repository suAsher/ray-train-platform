package objectstore

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"path"
	"strings"
	"unicode"
)

const (
	maxPublicationObjectKeyBytes = 4096
	maxPublicationCursorBytes    = 4096
	maxPublicationListKeys       = 1000
)

type TOSPublicationStore struct {
	store       *TOSStore
	sourceRoot  string
	derivedRoot string
}

type tosDeleteClient interface {
	DeleteObject(context.Context, string, string) error
}

func (store *TOSStore) PublicationObjects(sourceRoot, derivedRoot string) (*TOSPublicationStore, error) {
	if store == nil {
		return nil, fmt.Errorf("publication object store is unavailable")
	}
	cleanSource, err := cleanPublicationObjectKey(sourceRoot)
	if err != nil {
		return nil, err
	}
	cleanDerived, err := cleanPublicationObjectKey(derivedRoot)
	if err != nil {
		return nil, err
	}
	if publicationObjectRootsOverlap(cleanSource, cleanDerived) {
		return nil, fmt.Errorf("publication object roots overlap")
	}
	return &TOSPublicationStore{store: store, sourceRoot: cleanSource, derivedRoot: cleanDerived}, nil
}

func (store *TOSPublicationStore) List(ctx context.Context, prefix, cursor string, limit int) (PublicationObjectPage, error) {
	if err := ctx.Err(); err != nil {
		return PublicationObjectPage{}, err
	}
	prefix, err := cleanPublicationObjectPrefix(prefix)
	if err != nil {
		return PublicationObjectPage{}, err
	}
	if !strings.HasPrefix(prefix, store.sourceRoot+"/") {
		return PublicationObjectPage{}, fmt.Errorf("publication source prefix is outside its root")
	}
	if len(cursor) > maxPublicationCursorBytes || strings.ContainsRune(cursor, '\x00') || limit <= 0 || limit > maxPublicationListKeys {
		return PublicationObjectPage{}, fmt.Errorf("invalid publication list request")
	}
	client, ok := store.store.client.(tosArtifactClient)
	if !ok {
		return PublicationObjectPage{}, ErrUnavailable
	}
	response, err := client.ListArtifacts(ctx, tosArtifactListRequest{
		Bucket: store.store.bucket, Prefix: prefix, Delimiter: "", ContinuationToken: cursor, MaxKeys: limit,
	})
	if err != nil {
		return PublicationObjectPage{}, ErrUnavailable
	}
	if response.NextContinuationToken != "" && response.NextContinuationToken == cursor {
		return PublicationObjectPage{}, ErrUnavailable
	}
	objects := make([]PublicationListedObject, 0, len(response.Objects))
	for _, object := range response.Objects {
		if _, err := cleanPublicationObjectKey(object.Key); err != nil ||
			!strings.HasPrefix(object.Key, prefix) || object.SizeBytes < 0 || object.ETag == "" || object.LastModified.IsZero() {
			return PublicationObjectPage{}, ErrUnavailable
		}
		objects = append(objects, PublicationListedObject{
			Key: object.Key, SizeBytes: object.SizeBytes, ETag: object.ETag, ObservedAt: object.LastModified.UTC(),
		})
	}
	return PublicationObjectPage{Objects: objects, NextCursor: response.NextContinuationToken}, nil
}

func cleanPublicationObjectPrefix(value string) (string, error) {
	base := strings.TrimSuffix(value, "/")
	cleaned, err := cleanPublicationObjectKey(base)
	if err != nil {
		return "", err
	}
	return cleaned + "/", nil
}

func (store *TOSPublicationStore) Head(ctx context.Context, key string) (PublicationObjectInfo, error) {
	if err := ctx.Err(); err != nil {
		return PublicationObjectInfo{}, err
	}
	key, err := cleanPublicationObjectKey(key)
	if err != nil {
		return PublicationObjectInfo{}, err
	}
	if !publicationObjectKeyWithinRoot(key, store.sourceRoot) && !publicationObjectKeyWithinRoot(key, store.derivedRoot) {
		return PublicationObjectInfo{}, fmt.Errorf("publication head key is outside its roots")
	}
	info, err := store.store.client.Head(ctx, store.store.bucket, key)
	if err != nil {
		if err == ErrNotFound {
			return PublicationObjectInfo{}, ErrNotFound
		}
		return PublicationObjectInfo{}, ErrUnavailable
	}
	return publicationInfoFromObjectInfo(info), nil
}

func (store *TOSPublicationStore) Get(ctx context.Context, key string) (io.ReadCloser, PublicationObjectInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, PublicationObjectInfo{}, err
	}
	key, err := cleanPublicationObjectKey(key)
	if err != nil {
		return nil, PublicationObjectInfo{}, err
	}
	if !publicationObjectKeyWithinRoot(key, store.sourceRoot) {
		return nil, PublicationObjectInfo{}, fmt.Errorf("publication get key is outside its source root")
	}
	client, ok := store.store.client.(tosArtifactReadClient)
	if !ok {
		return nil, PublicationObjectInfo{}, ErrUnavailable
	}
	response, err := client.ReadArtifact(ctx, tosArtifactReadRequest{Bucket: store.store.bucket, Key: key})
	if err != nil {
		if err == ErrNotFound {
			return nil, PublicationObjectInfo{}, ErrNotFound
		}
		return nil, PublicationObjectInfo{}, ErrUnavailable
	}
	if response.Content == nil || response.SizeBytes < 0 {
		if response.Content != nil {
			_ = response.Content.Close()
		}
		return nil, PublicationObjectInfo{}, ErrUnavailable
	}
	return response.Content, PublicationObjectInfo{
		SizeBytes: response.SizeBytes, ETag: response.ETag, ObservedAt: response.LastModified.UTC(),
	}, nil
}

func (store *TOSPublicationStore) PutImmutable(ctx context.Context, key, digest string, sizeBytes int64, body io.Reader) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	key, err := cleanPublicationObjectKey(key)
	if err != nil || body == nil || sizeBytes <= 0 {
		return fmt.Errorf("invalid publication put request")
	}
	if !publicationObjectKeyWithinRoot(key, store.derivedRoot) {
		return fmt.Errorf("publication put key is outside its derived root")
	}
	if err := validatePublicationDigest(digest); err != nil {
		return fmt.Errorf("invalid publication put digest")
	}
	client, ok := store.store.client.(interface {
		Put(context.Context, tosPutRequest) error
	})
	if !ok {
		return ErrUnavailable
	}
	err = client.Put(ctx, tosPutRequest{
		Bucket: store.store.bucket, Key: key, SHA256: digest, SizeBytes: sizeBytes,
		ContentType: "application/octet-stream", Body: body,
	})
	if err != nil {
		if err == ErrAlreadyExists {
			return ErrAlreadyExists
		}
		return ErrUnavailable
	}
	return nil
}

func (store *TOSPublicationStore) CopyImmutable(ctx context.Context, sourceKey, destinationKey string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	sourceKey, err := cleanPublicationObjectKey(sourceKey)
	if err != nil {
		return err
	}
	destinationKey, err = cleanPublicationObjectKey(destinationKey)
	if err != nil {
		return err
	}
	if !publicationObjectKeyWithinRoot(sourceKey, store.derivedRoot) || !publicationObjectKeyWithinRoot(destinationKey, store.derivedRoot) {
		return fmt.Errorf("publication copy key is outside its derived root")
	}
	client, ok := store.store.client.(tosCopyClient)
	if !ok {
		return ErrUnavailable
	}
	err = client.CopyObject(ctx, tosCopyRequest{Bucket: store.store.bucket, SourceKey: sourceKey, DestinationKey: destinationKey})
	if err != nil {
		if err == ErrAlreadyExists {
			return ErrAlreadyExists
		}
		if err == ErrNotFound {
			return ErrNotFound
		}
		return ErrUnavailable
	}
	return nil
}

func (store *TOSPublicationStore) Delete(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	key, err := cleanPublicationObjectKey(key)
	if err != nil {
		return err
	}
	if !publicationObjectKeyWithinRoot(key, store.derivedRoot) {
		return fmt.Errorf("publication delete key is outside its derived root")
	}
	client, ok := store.store.client.(tosDeleteClient)
	if !ok {
		return ErrUnavailable
	}
	if err := client.DeleteObject(ctx, store.store.bucket, key); err != nil && err != ErrNotFound {
		return ErrUnavailable
	}
	return nil
}

func publicationObjectRootsOverlap(left, right string) bool {
	return left == right || publicationObjectKeyWithinRoot(left, right) || publicationObjectKeyWithinRoot(right, left)
}

func publicationObjectKeyWithinRoot(key, root string) bool {
	return key == root || strings.HasPrefix(key, root+"/")
}

func cleanPublicationObjectKey(value string) (string, error) {
	if value == "" || len(value) > maxPublicationObjectKeyBytes || strings.TrimSpace(value) != value || strings.Contains(value, "\\") || strings.Contains(value, "://") || strings.Contains(value, "%") {
		return "", fmt.Errorf("invalid publication object key")
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return "", fmt.Errorf("invalid publication object key")
		}
	}
	if decoded, err := url.PathUnescape(value); err != nil || decoded != value {
		return "", fmt.Errorf("invalid publication object key")
	}
	cleaned := path.Clean(value)
	if path.IsAbs(value) || cleaned != value {
		return "", fmt.Errorf("invalid publication object key")
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return "", fmt.Errorf("invalid publication object key")
		}
	}
	return value, nil
}

func publicationInfoFromObjectInfo(info ObjectInfo) PublicationObjectInfo {
	metadata := info.Metadata
	if metadata == nil {
		metadata = map[string]string{}
	}
	return PublicationObjectInfo{SizeBytes: info.SizeBytes, SHA256: metadata["sha256"], Metadata: cloneObjectMetadata(metadata)}
}

func validatePublicationDigest(value string) error {
	if len(value) != 64 {
		return fmt.Errorf("invalid digest")
	}
	for _, r := range value {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return fmt.Errorf("invalid digest")
		}
	}
	return nil
}
