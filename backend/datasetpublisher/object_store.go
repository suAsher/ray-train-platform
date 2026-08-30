package datasetpublisher

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"path"
	"strings"
	"unicode"

	"ray-train-platform-backend/objectstore"
)

const (
	maxPublicationPathBytes   = 4096
	maxPublicationCursorBytes = 4096
	maxPublicationListLimit   = 1000
)

var ErrInvalidPublicationPath = fmt.Errorf("invalid publication object path")

type PublicationStoreConfig struct {
	SourceRoot     string
	InternalPrefix string
	DatasetID      string
}

type PublicationObjectInfo = objectstore.PublicationObjectInfo
type PublicationListedObject = objectstore.PublicationListedObject
type PublicationObjectPage = objectstore.PublicationObjectPage
type PublicationSourceBackend = objectstore.PublicationSourceBackend
type PublicationDerivedBackend = objectstore.PublicationDerivedBackend

type DatasetPublicationObjectStore struct {
	sourceRoot     string
	internalPrefix string
	datasetID      string
	sourceBackend  PublicationSourceBackend
	derivedBackend PublicationDerivedBackend
}

func NewDatasetPublicationObjectStore(config PublicationStoreConfig, source PublicationSourceBackend, derived PublicationDerivedBackend) (*DatasetPublicationObjectStore, error) {
	if source == nil || derived == nil {
		return nil, fmt.Errorf("publication object store backends are required")
	}
	sourceRoot, err := cleanPublicationRelativePath(config.SourceRoot)
	if err != nil {
		return nil, err
	}
	internalPrefix, err := cleanPublicationRelativePath(config.InternalPrefix)
	if err != nil {
		return nil, err
	}
	if !validIdentifier(config.DatasetID) {
		return nil, fmt.Errorf("dataset id is invalid")
	}
	if publicationRootsOverlap(sourceRoot, internalPrefix) {
		return nil, fmt.Errorf("publication source and internal roots overlap")
	}
	return &DatasetPublicationObjectStore{
		sourceRoot:     sourceRoot,
		internalPrefix: internalPrefix,
		datasetID:      config.DatasetID,
		sourceBackend:  source,
		derivedBackend: derived,
	}, nil
}

func publicationRootsOverlap(left, right string) bool {
	return left == right || strings.HasPrefix(left, right+"/") || strings.HasPrefix(right, left+"/")
}

func (store *DatasetPublicationObjectStore) DatasetID() string {
	if store == nil {
		return ""
	}
	return store.datasetID
}

func (store *DatasetPublicationObjectStore) Source() PublicationSource {
	return PublicationSource{store: store}
}

func (store *DatasetPublicationObjectStore) Derived() PublicationDerived {
	return PublicationDerived{store: store}
}

type PublicationSource struct {
	store *DatasetPublicationObjectStore
}

func (source PublicationSource) List(ctx context.Context, relativePath, cursor string, limit int) (PublicationObjectPage, error) {
	prefix, err := source.store.sourceListPrefix(relativePath)
	if err != nil {
		return PublicationObjectPage{}, err
	}
	if err := validatePublicationListBounds(cursor, limit); err != nil {
		return PublicationObjectPage{}, err
	}
	page, err := source.store.sourceBackend.List(ctx, prefix, cursor, limit)
	if err != nil {
		return PublicationObjectPage{}, sanitizePublicationStoreError(err)
	}
	if page.NextCursor != "" && page.NextCursor == cursor {
		return PublicationObjectPage{}, objectstore.ErrUnavailable
	}
	objects := make([]PublicationListedObject, len(page.Objects))
	rootPrefix := source.store.sourceRoot + "/"
	for index, object := range page.Objects {
		if !strings.HasPrefix(object.Key, prefix) || !strings.HasPrefix(object.Key, rootPrefix) {
			return PublicationObjectPage{}, objectstore.ErrUnavailable
		}
		relative := strings.TrimPrefix(object.Key, rootPrefix)
		if _, err := cleanPublicationRelativePath(relative); err != nil {
			return PublicationObjectPage{}, objectstore.ErrUnavailable
		}
		object.Key = relative
		objects[index] = object
	}
	page.Objects = objects
	return page, nil
}

func (source PublicationSource) Head(ctx context.Context, relativePath string) (PublicationObjectInfo, error) {
	key, err := source.store.sourceKey(relativePath)
	if err != nil {
		return PublicationObjectInfo{}, err
	}
	info, err := source.store.sourceBackend.Head(ctx, key)
	if err != nil {
		return PublicationObjectInfo{}, sanitizePublicationStoreError(err)
	}
	return info.Clone(), nil
}

func (source PublicationSource) Get(ctx context.Context, relativePath string) (io.ReadCloser, PublicationObjectInfo, error) {
	key, err := source.store.sourceKey(relativePath)
	if err != nil {
		return nil, PublicationObjectInfo{}, err
	}
	body, info, err := source.store.sourceBackend.Get(ctx, key)
	if err != nil {
		return nil, PublicationObjectInfo{}, sanitizePublicationStoreError(err)
	}
	if body == nil {
		return nil, PublicationObjectInfo{}, objectstore.ErrUnavailable
	}
	return body, info.Clone(), nil
}

type PublicationDerived struct {
	store *DatasetPublicationObjectStore
}

func (derived PublicationDerived) Head(ctx context.Context, relativePath string) (PublicationObjectInfo, error) {
	key, err := derived.store.derivedKey(relativePath)
	if err != nil {
		return PublicationObjectInfo{}, err
	}
	info, err := derived.store.derivedBackend.Head(ctx, key)
	if err != nil {
		return PublicationObjectInfo{}, sanitizePublicationStoreError(err)
	}
	return info.Clone(), nil
}

func (derived PublicationDerived) PutImmutable(ctx context.Context, relativePath, digest string, sizeBytes int64, body io.Reader) error {
	key, err := derived.store.derivedKey(relativePath)
	if err != nil {
		return err
	}
	if err := validateDigestAndSize(digest, sizeBytes); err != nil {
		return err
	}
	if body == nil {
		return fmt.Errorf("%w: body is required", ErrInvalidPublicationPath)
	}
	return sanitizePublicationStoreError(derived.store.derivedBackend.PutImmutable(ctx, key, digest, sizeBytes, body))
}

func (derived PublicationDerived) CopyImmutable(ctx context.Context, sourceRelativePath, destinationRelativePath string) error {
	sourceKey, err := derived.store.derivedKey(sourceRelativePath)
	if err != nil {
		return err
	}
	destinationKey, err := derived.store.derivedKey(destinationRelativePath)
	if err != nil {
		return err
	}
	return sanitizePublicationStoreError(derived.store.derivedBackend.CopyImmutable(ctx, sourceKey, destinationKey))
}

func (derived PublicationDerived) Delete(ctx context.Context, relativePath string) error {
	key, err := derived.store.derivedKey(relativePath)
	if err != nil {
		return err
	}
	return sanitizePublicationStoreError(derived.store.derivedBackend.Delete(ctx, key))
}

func (store *DatasetPublicationObjectStore) sourceKey(relativePath string) (string, error) {
	relative, err := cleanPublicationRelativePath(relativePath)
	if err != nil {
		return "", err
	}
	return store.sourceRoot + "/" + relative, nil
}

func (store *DatasetPublicationObjectStore) sourceListPrefix(relativePath string) (string, error) {
	if relativePath == "" {
		return store.sourceRoot + "/", nil
	}
	relative, err := cleanPublicationRelativePath(relativePath)
	if err != nil {
		return "", err
	}
	return store.sourceRoot + "/" + relative + "/", nil
}

func (store *DatasetPublicationObjectStore) derivedKey(relativePath string) (string, error) {
	relative, err := cleanPublicationRelativePath(relativePath)
	if err != nil {
		return "", err
	}
	return store.internalPrefix + "/" + store.datasetID + "/" + relative, nil
}

func cleanPublicationRelativePath(value string) (string, error) {
	if value == "" || len(value) > maxPublicationPathBytes || strings.TrimSpace(value) != value || strings.Contains(value, "\\") || strings.Contains(value, "://") || strings.Contains(value, "%") {
		return "", ErrInvalidPublicationPath
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return "", ErrInvalidPublicationPath
		}
	}
	if decoded, err := url.PathUnescape(value); err != nil || decoded != value {
		return "", ErrInvalidPublicationPath
	}
	cleaned := path.Clean(value)
	if path.IsAbs(value) || cleaned != value {
		return "", ErrInvalidPublicationPath
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return "", ErrInvalidPublicationPath
		}
	}
	return value, nil
}

func validatePublicationListBounds(cursor string, limit int) error {
	if len(cursor) > maxPublicationCursorBytes || strings.ContainsRune(cursor, '\x00') {
		return ErrInvalidPublicationPath
	}
	if limit <= 0 || limit > maxPublicationListLimit {
		return ErrInvalidPublicationPath
	}
	return nil
}

func sanitizePublicationStoreError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, objectstore.ErrNotFound) {
		return objectstore.ErrNotFound
	}
	if errors.Is(err, objectstore.ErrAlreadyExists) {
		return objectstore.ErrAlreadyExists
	}
	if errors.Is(err, ErrInvalidPublicationBundle) {
		return ErrInvalidPublicationBundle
	}
	return objectstore.ErrUnavailable
}
