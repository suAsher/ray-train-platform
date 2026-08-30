package datasetpublisher

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"math"
	"regexp"
	"sync"
	"time"

	"ray-train-platform-backend/objectstore"
)

var (
	ErrInvalidPublicationBundle  = errors.New("invalid publication bundle")
	ErrPublicationObjectMismatch = errors.New("publication object mismatch")
	ErrInvalidGarbageCollection  = errors.New("invalid garbage collection request")
	publicationIdentifierPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,127}$`)
	contentAddressedShardPattern = regexp.MustCompile(`^objects/sha256/([0-9a-f]{2})/([0-9a-f]{64})\.parquet$`)
)

type ReopenableBody func() (io.ReadCloser, error)

type PublicationBlob struct {
	Digest    string
	SizeBytes int64
	Open      ReopenableBody
}

type PublicationBundle struct {
	PublicationID string
	DatasetID     string
	VersionID     string
	Manifest      PublicationBlob
	Shards        []PublicationBlob
}

type PublicationResult struct {
	AddedShards              int
	ReusedShards             int
	ManifestCommitted        bool
	DeferredTemporaryCleanup int
}

type PublicationService struct {
	store   *DatasetPublicationObjectStore
	gcGuard GarbageCollectionGuard
}

// GarbageCollectionGuard must synchronously hold an authoritative catalogue
// fence for datasetID while deleteObjects runs. New version references to any
// key must be blocked until the callback returns.
type GarbageCollectionGuard interface {
	WithUnreferencedShards(context.Context, string, []string, func(context.Context) error) error
}

func NewPublicationService(store *DatasetPublicationObjectStore) *PublicationService {
	return &PublicationService{store: store}
}

func NewPublicationServiceWithGarbageCollectionGuard(store *DatasetPublicationObjectStore, guard GarbageCollectionGuard) *PublicationService {
	return &PublicationService{store: store, gcGuard: guard}
}

func (service *PublicationService) Publish(ctx context.Context, bundle PublicationBundle) (PublicationResult, error) {
	if service == nil || service.store == nil {
		return PublicationResult{}, ErrInvalidPublicationBundle
	}
	if err := validateBundleHeader(service.store.DatasetID(), bundle); err != nil {
		return PublicationResult{}, err
	}
	manifestPath := finalManifestPath(bundle.VersionID)
	manifestHead, err := service.store.Derived().Head(ctx, manifestPath)
	if err == nil {
		if objectMatches(manifestHead, bundle.Manifest) {
			result := PublicationResult{ReusedShards: len(bundle.Shards), ManifestCommitted: true}
			result.DeferredTemporaryCleanup = service.cleanupTemporaryObjects(ctx, bundle)
			return result, nil
		}
		return PublicationResult{}, ErrPublicationObjectMismatch
	}
	if !errors.Is(err, objectstore.ErrNotFound) {
		return PublicationResult{}, err
	}

	result := PublicationResult{}
	for _, shard := range bundle.Shards {
		reused, err := service.publishObject(ctx, bundle.PublicationID, finalShardPath(shard.Digest), shard, false)
		if err != nil {
			return PublicationResult{}, err
		}
		if reused {
			result.ReusedShards++
		} else {
			result.AddedShards++
		}
	}
	if _, err := service.publishObject(ctx, bundle.PublicationID, manifestPath, bundle.Manifest, true); err != nil {
		return PublicationResult{}, err
	}
	result.ManifestCommitted = true
	result.DeferredTemporaryCleanup = service.cleanupTemporaryObjects(ctx, bundle)
	return result, nil
}

func (service *PublicationService) cleanupTemporaryObjects(ctx context.Context, bundle PublicationBundle) int {
	paths := make([]string, 0, len(bundle.Shards)+1)
	for _, shard := range bundle.Shards {
		paths = append(paths, tempPathFor(bundle.PublicationID, finalShardPath(shard.Digest)))
	}
	paths = append(paths, tempPathFor(bundle.PublicationID, finalManifestPath(bundle.VersionID)))
	deferred := 0
	for _, path := range paths {
		if err := service.store.Derived().Delete(ctx, path); err != nil && !errors.Is(err, objectstore.ErrNotFound) {
			deferred++
		}
	}
	return deferred
}

func (service *PublicationService) publishObject(ctx context.Context, publicationID, finalPath string, blob PublicationBlob, finalKnownMissing bool) (bool, error) {
	if !finalKnownMissing {
		head, err := service.store.Derived().Head(ctx, finalPath)
		if err == nil {
			if objectMatches(head, blob) {
				return true, nil
			}
			return false, ErrPublicationObjectMismatch
		}
		if !errors.Is(err, objectstore.ErrNotFound) {
			return false, err
		}
	}
	if err := verifyReopenableBlob(blob); err != nil {
		return false, err
	}
	tempPath := tempPathFor(publicationID, finalPath)
	body, err := blob.Open()
	if err != nil || body == nil {
		return false, ErrInvalidPublicationBundle
	}
	upload := newVerifyingUploadReader(body, blob)
	defer upload.Close()
	putErr := service.store.Derived().PutImmutable(ctx, tempPath, blob.Digest, blob.SizeBytes, upload)
	if putErr != nil {
		if !errors.Is(putErr, objectstore.ErrAlreadyExists) {
			return false, putErr
		}
	} else {
		if err := upload.Err(); err != nil {
			return false, err
		}
	}
	if err := service.verifyObjectHead(ctx, tempPath, blob); err != nil {
		return false, err
	}
	if err := service.store.Derived().CopyImmutable(ctx, tempPath, finalPath); err != nil && !errors.Is(err, objectstore.ErrAlreadyExists) {
		return false, err
	}
	if err := service.verifyObjectHead(ctx, finalPath, blob); err != nil {
		return false, err
	}
	return false, nil
}

func (service *PublicationService) verifyObjectHead(ctx context.Context, path string, blob PublicationBlob) error {
	info, err := service.store.Derived().Head(ctx, path)
	if err != nil {
		return err
	}
	if !objectMatches(info, blob) {
		return ErrPublicationObjectMismatch
	}
	return nil
}

type GarbageCollectionCandidate struct {
	DatasetID      string
	Key            string
	RetiredAt      time.Time
	ReferenceCount int
}

type GarbageCollectionRequest struct {
	Now        time.Time
	Retention  time.Duration
	Candidates []GarbageCollectionCandidate
}

type GarbageCollectionPlan struct {
	DeletableKeys  []string
	DeletedCount   int
	authorizedKeys map[string]struct{}
	datasetID      string
}

func (service *PublicationService) PlanGarbageCollection(ctx context.Context, request GarbageCollectionRequest) (GarbageCollectionPlan, error) {
	if service == nil || service.store == nil || request.Now.IsZero() || request.Retention <= 0 {
		return GarbageCollectionPlan{}, ErrInvalidGarbageCollection
	}
	if err := ctx.Err(); err != nil {
		return GarbageCollectionPlan{}, err
	}
	plan := GarbageCollectionPlan{datasetID: service.store.DatasetID(), authorizedKeys: map[string]struct{}{}}
	seenCandidates := make(map[string]struct{}, len(request.Candidates))
	for _, candidate := range request.Candidates {
		if candidate.DatasetID != service.store.DatasetID() || candidate.ReferenceCount < 0 || candidate.RetiredAt.IsZero() || candidate.RetiredAt.After(request.Now) {
			return GarbageCollectionPlan{}, ErrInvalidGarbageCollection
		}
		if !validGCObjectKey(candidate.Key) {
			return GarbageCollectionPlan{}, ErrInvalidGarbageCollection
		}
		if _, duplicate := seenCandidates[candidate.Key]; duplicate {
			return GarbageCollectionPlan{}, ErrInvalidGarbageCollection
		}
		seenCandidates[candidate.Key] = struct{}{}
		if candidate.ReferenceCount == 0 && !candidate.RetiredAt.After(request.Now.Add(-request.Retention)) {
			plan.DeletableKeys = append(plan.DeletableKeys, candidate.Key)
			plan.authorizedKeys[candidate.Key] = struct{}{}
		}
	}
	return plan, nil
}

func (service *PublicationService) DeleteGarbage(ctx context.Context, plan GarbageCollectionPlan) (GarbageCollectionPlan, error) {
	if service == nil || service.store == nil || service.gcGuard == nil || plan.datasetID != service.store.DatasetID() {
		return GarbageCollectionPlan{}, ErrInvalidGarbageCollection
	}
	if err := ctx.Err(); err != nil {
		return GarbageCollectionPlan{}, err
	}
	if len(plan.authorizedKeys) != len(plan.DeletableKeys) {
		return GarbageCollectionPlan{}, ErrInvalidGarbageCollection
	}
	seen := make(map[string]struct{}, len(plan.DeletableKeys))
	for _, key := range plan.DeletableKeys {
		if !validGCObjectKey(key) {
			return GarbageCollectionPlan{}, ErrInvalidGarbageCollection
		}
		if _, authorized := plan.authorizedKeys[key]; !authorized {
			return GarbageCollectionPlan{}, ErrInvalidGarbageCollection
		}
		if _, duplicate := seen[key]; duplicate {
			return GarbageCollectionPlan{}, ErrInvalidGarbageCollection
		}
		seen[key] = struct{}{}
	}
	keys := append([]string(nil), plan.DeletableKeys...)
	result := GarbageCollectionPlan{DeletableKeys: keys, datasetID: plan.datasetID}
	var callbackMutex sync.Mutex
	callbackCalled := false
	callbackInvalid := false
	var callbackErr error
	guardErr := service.gcGuard.WithUnreferencedShards(ctx, service.store.DatasetID(), keys, func(guardedContext context.Context) error {
		callbackMutex.Lock()
		if callbackCalled {
			callbackInvalid = true
			callbackMutex.Unlock()
			return ErrInvalidGarbageCollection
		}
		callbackCalled = true
		callbackMutex.Unlock()

		for _, key := range keys {
			if err := service.store.Derived().Delete(guardedContext, key); err != nil && !errors.Is(err, objectstore.ErrNotFound) {
				callbackMutex.Lock()
				callbackErr = err
				callbackMutex.Unlock()
				return err
			}
			result.DeletedCount++
		}
		return nil
	})
	callbackMutex.Lock()
	called, invalid, deletionErr := callbackCalled, callbackInvalid, callbackErr
	callbackMutex.Unlock()
	if invalid {
		return GarbageCollectionPlan{}, ErrInvalidGarbageCollection
	}
	if !called {
		if guardErr == nil || errors.Is(guardErr, ErrInvalidGarbageCollection) {
			return GarbageCollectionPlan{}, ErrInvalidGarbageCollection
		}
		return GarbageCollectionPlan{}, objectstore.ErrUnavailable
	}
	if guardErr != nil {
		if errors.Is(guardErr, ErrInvalidGarbageCollection) {
			return GarbageCollectionPlan{}, ErrInvalidGarbageCollection
		}
		if deletionErr != nil {
			return GarbageCollectionPlan{}, deletionErr
		}
		return GarbageCollectionPlan{}, objectstore.ErrUnavailable
	}
	if deletionErr != nil {
		return GarbageCollectionPlan{}, deletionErr
	}
	return result, nil
}

func validateBundleHeader(datasetID string, bundle PublicationBundle) error {
	if !publicationIdentifierPattern.MatchString(bundle.PublicationID) ||
		!publicationIdentifierPattern.MatchString(bundle.DatasetID) ||
		!publicationIdentifierPattern.MatchString(bundle.VersionID) ||
		bundle.DatasetID != datasetID ||
		len(bundle.Shards) == 0 {
		return ErrInvalidPublicationBundle
	}
	if err := validateBlob(bundle.Manifest); err != nil {
		return err
	}
	seenShards := make(map[string]struct{}, len(bundle.Shards))
	for _, shard := range bundle.Shards {
		if err := validateBlob(shard); err != nil {
			return err
		}
		if _, exists := seenShards[shard.Digest]; exists {
			return ErrInvalidPublicationBundle
		}
		seenShards[shard.Digest] = struct{}{}
	}
	return nil
}

func validateBlob(blob PublicationBlob) error {
	return validateDigestAndSize(blob.Digest, blob.SizeBytes)
}

func validateDigestAndSize(digest string, sizeBytes int64) error {
	if !validDigest(digest) || sizeBytes <= 0 || sizeBytes > math.MaxInt64-1 {
		return ErrInvalidPublicationBundle
	}
	return nil
}

func verifyReopenableBlob(blob PublicationBlob) error {
	if blob.Open == nil {
		return ErrInvalidPublicationBundle
	}
	body, err := blob.Open()
	if err != nil || body == nil {
		return ErrInvalidPublicationBundle
	}
	defer body.Close()
	hash := sha256.New()
	limited := io.LimitReader(body, blob.SizeBytes+1)
	readBytes, err := io.Copy(hash, limited)
	if err != nil {
		return ErrInvalidPublicationBundle
	}
	if readBytes != blob.SizeBytes {
		return ErrInvalidPublicationBundle
	}
	if hex.EncodeToString(hash.Sum(nil)) != blob.Digest {
		return ErrInvalidPublicationBundle
	}
	return nil
}

func objectMatches(info PublicationObjectInfo, blob PublicationBlob) bool {
	return info.SizeBytes == blob.SizeBytes && info.SHA256 == blob.Digest && info.Metadata["sha256"] == blob.Digest
}

type verifyingUploadReader struct {
	body           io.ReadCloser
	hash           hash.Hash
	expectedDigest string
	expectedSize   int64
	readBytes      int64
	done           bool
	err            error
}

func newVerifyingUploadReader(body io.ReadCloser, blob PublicationBlob) *verifyingUploadReader {
	return &verifyingUploadReader{body: body, hash: sha256.New(), expectedDigest: blob.Digest, expectedSize: blob.SizeBytes}
}

func (reader *verifyingUploadReader) Read(buffer []byte) (int, error) {
	if reader.err != nil {
		return 0, reader.err
	}
	n, err := reader.body.Read(buffer)
	if n > 0 {
		if int64(n) > reader.expectedSize-reader.readBytes {
			reader.err = ErrInvalidPublicationBundle
			return 0, reader.err
		}
		reader.readBytes += int64(n)
		_, _ = reader.hash.Write(buffer[:n])
		return n, nil
	}
	if errors.Is(err, io.EOF) {
		reader.done = true
		if reader.readBytes != reader.expectedSize || hex.EncodeToString(reader.hash.Sum(nil)) != reader.expectedDigest {
			reader.err = ErrInvalidPublicationBundle
			return 0, reader.err
		}
	}
	return n, err
}

func (reader *verifyingUploadReader) Close() error {
	return reader.body.Close()
}

func (reader *verifyingUploadReader) Err() error {
	if reader.err != nil {
		return reader.err
	}
	if reader.done {
		return nil
	}
	if reader.readBytes != reader.expectedSize {
		return ErrInvalidPublicationBundle
	}
	var probe [1]byte
	n, err := reader.Read(probe[:])
	if reader.err != nil {
		return reader.err
	}
	if n == 0 && errors.Is(err, io.EOF) && reader.done {
		return nil
	}
	return ErrInvalidPublicationBundle
}

func finalShardPath(digest string) string {
	return "objects/sha256/" + digest[:2] + "/" + digest + ".parquet"
}

func finalManifestPath(versionID string) string {
	return "manifests/" + versionID + ".parquet"
}

func tempPathFor(publicationID, finalPath string) string {
	return "temp/" + publicationID + "/" + finalPath
}

func validGCObjectKey(key string) bool {
	matches := contentAddressedShardPattern.FindStringSubmatch(key)
	return len(matches) == 3 && matches[1] == matches[2][:2]
}

func (request GarbageCollectionRequest) String() string {
	return fmt.Sprintf("gc candidates=%d", len(request.Candidates))
}
