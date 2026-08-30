package datasetpublisher

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"ray-train-platform-backend/objectstore"
)

const (
	testPublicationDigestA = "05b3abf2579a5eb66403cd78be557fd860633a1fe2103c7642030defe32c657f"
	testPublicationDigestB = "5e5a8dcfb13cdcdc19e7f75b1421e040214ec7790477a742d342d438e4371e97"
	testPublicationDigestC = "0ee30b30b83597614c6e42541fd1bd5b59f368db97dd662a83538e3b3c974083"
)

func TestPublicationServicePublishesShardsBeforeManifestCommitPoint(t *testing.T) {
	derived := newRecordingDerivedStore()
	store := mustPublicationStore(t, derived)
	service := NewPublicationService(store)

	bundle := publicationBundle("pub-1", "dataset-a", "version-1",
		publicationBlob("manifest", testPublicationDigestA),
		publicationBlob("shard-one", testPublicationDigestB),
		publicationBlob("shard-two", testPublicationDigestC),
	)
	result, err := service.Publish(context.Background(), bundle)
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if result.AddedShards != 2 || result.ReusedShards != 0 || !result.ManifestCommitted {
		t.Fatalf("result=%+v", result)
	}

	want := []string{
		"head ray-train/platform/datasets/dataset-a/manifests/version-1.parquet",
		"head ray-train/platform/datasets/dataset-a/objects/sha256/5e/" + testPublicationDigestB + ".parquet",
		"put ray-train/platform/datasets/dataset-a/temp/pub-1/objects/sha256/5e/" + testPublicationDigestB + ".parquet",
		"head ray-train/platform/datasets/dataset-a/temp/pub-1/objects/sha256/5e/" + testPublicationDigestB + ".parquet",
		"copy ray-train/platform/datasets/dataset-a/temp/pub-1/objects/sha256/5e/" + testPublicationDigestB + ".parquet -> ray-train/platform/datasets/dataset-a/objects/sha256/5e/" + testPublicationDigestB + ".parquet",
		"head ray-train/platform/datasets/dataset-a/objects/sha256/5e/" + testPublicationDigestB + ".parquet",
		"head ray-train/platform/datasets/dataset-a/objects/sha256/0e/" + testPublicationDigestC + ".parquet",
		"put ray-train/platform/datasets/dataset-a/temp/pub-1/objects/sha256/0e/" + testPublicationDigestC + ".parquet",
		"head ray-train/platform/datasets/dataset-a/temp/pub-1/objects/sha256/0e/" + testPublicationDigestC + ".parquet",
		"copy ray-train/platform/datasets/dataset-a/temp/pub-1/objects/sha256/0e/" + testPublicationDigestC + ".parquet -> ray-train/platform/datasets/dataset-a/objects/sha256/0e/" + testPublicationDigestC + ".parquet",
		"head ray-train/platform/datasets/dataset-a/objects/sha256/0e/" + testPublicationDigestC + ".parquet",
		"put ray-train/platform/datasets/dataset-a/temp/pub-1/manifests/version-1.parquet",
		"head ray-train/platform/datasets/dataset-a/temp/pub-1/manifests/version-1.parquet",
		"copy ray-train/platform/datasets/dataset-a/temp/pub-1/manifests/version-1.parquet -> ray-train/platform/datasets/dataset-a/manifests/version-1.parquet",
		"head ray-train/platform/datasets/dataset-a/manifests/version-1.parquet",
		"delete ray-train/platform/datasets/dataset-a/temp/pub-1/objects/sha256/5e/" + testPublicationDigestB + ".parquet",
		"delete ray-train/platform/datasets/dataset-a/temp/pub-1/objects/sha256/0e/" + testPublicationDigestC + ".parquet",
		"delete ray-train/platform/datasets/dataset-a/temp/pub-1/manifests/version-1.parquet",
	}
	if !reflect.DeepEqual(derived.ops, want) {
		t.Fatalf("ops mismatch\n got: %v\nwant: %v", derived.ops, want)
	}
}

func TestPublicationServiceIsIdempotentAndRetriesOnlyMissingShards(t *testing.T) {
	derived := newRecordingDerivedStore()
	derived.objects["ray-train/platform/datasets/dataset-a/objects/sha256/5e/"+testPublicationDigestB+".parquet"] = storedPublicationObject("shard-one", testPublicationDigestB)
	store := mustPublicationStore(t, derived)
	service := NewPublicationService(store)

	bundle := publicationBundle("pub-1", "dataset-a", "version-1",
		publicationBlob("manifest", testPublicationDigestA),
		publicationBlob("shard-one", testPublicationDigestB),
		publicationBlob("shard-two", testPublicationDigestC),
	)
	result, err := service.Publish(context.Background(), bundle)
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if result.AddedShards != 1 || result.ReusedShards != 1 {
		t.Fatalf("result=%+v", result)
	}
	if hasOpPrefix(derived.ops, "put ray-train/platform/datasets/dataset-a/objects/sha256/5e/") {
		t.Fatalf("reused shard was uploaded: %v", derived.ops)
	}

	derived.ops = nil
	derived.objects["ray-train/platform/datasets/dataset-a/manifests/version-1.parquet"] = storedPublicationObject("manifest", testPublicationDigestA)
	result, err = service.Publish(context.Background(), bundle)
	if err != nil {
		t.Fatalf("idempotent publish: %v", err)
	}
	if result.AddedShards != 0 || result.ReusedShards != 2 || !result.ManifestCommitted {
		t.Fatalf("idempotent result=%+v", result)
	}
	wantRetryOps := []string{
		"head ray-train/platform/datasets/dataset-a/manifests/version-1.parquet",
		"delete ray-train/platform/datasets/dataset-a/temp/pub-1/objects/sha256/5e/" + testPublicationDigestB + ".parquet",
		"delete ray-train/platform/datasets/dataset-a/temp/pub-1/objects/sha256/0e/" + testPublicationDigestC + ".parquet",
		"delete ray-train/platform/datasets/dataset-a/temp/pub-1/manifests/version-1.parquet",
	}
	if !reflect.DeepEqual(derived.ops, wantRetryOps) {
		t.Fatalf("idempotent manifest should be commit point, ops=%v", derived.ops)
	}
}

func TestPublicationServiceReportsDeferredTemporaryCleanupWithoutFailingCommittedVersion(t *testing.T) {
	derived := newRecordingDerivedStore()
	derived.deleteErr = objectstore.ErrUnavailable
	service := NewPublicationService(mustPublicationStore(t, derived))

	result, err := service.Publish(context.Background(), publicationBundle(
		"pub-1", "dataset-a", "version-1",
		publicationBlob("manifest", testPublicationDigestA),
		publicationBlob("shard-one", testPublicationDigestB),
	))
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if !result.ManifestCommitted || result.DeferredTemporaryCleanup != 2 {
		t.Fatalf("result=%+v", result)
	}
}

func TestPublicationServiceFailsClosedOnExistingMismatchAndDoesNotCommitManifest(t *testing.T) {
	derived := newRecordingDerivedStore()
	derived.objects["ray-train/platform/datasets/dataset-a/objects/sha256/5e/"+testPublicationDigestB+".parquet"] = storedPublicationObject("wrong", testPublicationDigestA)
	store := mustPublicationStore(t, derived)
	service := NewPublicationService(store)

	bundle := publicationBundle("pub-1", "dataset-a", "version-1",
		publicationBlob("manifest", testPublicationDigestA),
		publicationBlob("shard-one", testPublicationDigestB),
	)
	_, err := service.Publish(context.Background(), bundle)
	if !errors.Is(err, ErrPublicationObjectMismatch) {
		t.Fatalf("error=%v want mismatch", err)
	}
	if hasOpPrefix(derived.ops, "put ray-train/platform/datasets/dataset-a/temp/pub-1/manifests/") {
		t.Fatalf("manifest was uploaded after mismatch: %v", derived.ops)
	}
}

func TestPublicationServiceRejectsDuplicateShardDigests(t *testing.T) {
	service := NewPublicationService(mustPublicationStore(t, newRecordingDerivedStore()))
	duplicate := publicationBlob("shard-one", testPublicationDigestB)
	_, err := service.Publish(context.Background(), publicationBundle(
		"pub-1", "dataset-a", "version-1", publicationBlob("manifest", testPublicationDigestA), duplicate, duplicate,
	))
	if !errors.Is(err, ErrInvalidPublicationBundle) {
		t.Fatalf("error=%v want invalid bundle", err)
	}
}

func TestPublicationServiceDoesNotTrustReaders(t *testing.T) {
	tests := []struct {
		name string
		blob PublicationBlob
	}{
		{name: "nil opener", blob: PublicationBlob{Digest: testPublicationDigestA, SizeBytes: 8}},
		{name: "open fails", blob: PublicationBlob{Digest: testPublicationDigestA, SizeBytes: 8, Open: func() (io.ReadCloser, error) {
			return nil, errors.New("boom")
		}}},
		{name: "short read", blob: PublicationBlob{Digest: digestFor("manifest"), SizeBytes: 9, Open: reopenable("manifest")}},
		{name: "over read", blob: PublicationBlob{Digest: digestFor("manifest"), SizeBytes: 7, Open: reopenable("manifest")}},
		{name: "digest mismatch", blob: PublicationBlob{Digest: testPublicationDigestB, SizeBytes: 8, Open: reopenable("manifest")}},
		{name: "second open fails", blob: PublicationBlob{Digest: digestFor("manifest"), SizeBytes: 8, Open: oneShotOpener("manifest")}},
		{name: "second body changes", blob: PublicationBlob{Digest: digestFor("manifest"), SizeBytes: 8, Open: changingOpener("manifest", "tampered")}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			derived := newRecordingDerivedStore()
			service := NewPublicationService(mustPublicationStore(t, derived))
			_, err := service.Publish(context.Background(), publicationBundle("pub-1", "dataset-a", "version-1", test.blob, publicationBlob("shard-one", testPublicationDigestB)))
			if !errors.Is(err, ErrInvalidPublicationBundle) {
				t.Fatalf("error=%v want invalid bundle", err)
			}
			if hasOpPrefix(derived.ops, "copy ray-train/platform/datasets/dataset-a/temp/pub-1/manifests/") {
				t.Fatalf("manifest commit attempted after invalid body: %v", derived.ops)
			}
		})
	}
}

func TestPublicationServiceAcceptsUploaderThatStopsAtDeclaredLength(t *testing.T) {
	derived := newRecordingDerivedStore()
	derived.readExactly = true
	service := NewPublicationService(mustPublicationStore(t, derived))

	_, err := service.Publish(context.Background(), publicationBundle(
		"pub-1", "dataset-a", "version-1",
		publicationBlob("manifest", testPublicationDigestA),
		publicationBlob("shard-one", testPublicationDigestB),
	))
	if err != nil {
		t.Fatalf("publish with content-length reader: %v", err)
	}
}

func TestPublicationGarbageCollectorValidatesBoundariesAndSeparatesDryRun(t *testing.T) {
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	derived := newRecordingDerivedStore()
	store := mustPublicationStore(t, derived)
	guard := &recordingGarbageCollectionGuard{references: map[string]int{}}
	service := NewPublicationServiceWithGarbageCollectionGuard(store, guard)
	retired := now.Add(-48 * time.Hour)
	key := "objects/sha256/05/" + testPublicationDigestA + ".parquet"

	plan, err := service.PlanGarbageCollection(context.Background(), GarbageCollectionRequest{
		Now:       now,
		Retention: 24 * time.Hour,
		Candidates: []GarbageCollectionCandidate{{
			DatasetID: "dataset-a", Key: key, RetiredAt: retired, ReferenceCount: 0,
		}},
	})
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if len(plan.DeletableKeys) != 1 || plan.DeletedCount != 0 || len(derived.ops) != 0 {
		t.Fatalf("dry run result=%+v ops=%v", plan, derived.ops)
	}

	result, err := service.DeleteGarbage(context.Background(), plan)
	if err != nil {
		t.Fatalf("delete garbage: %v", err)
	}
	if result.DeletedCount != 1 {
		t.Fatalf("delete result=%+v", result)
	}
	if !reflect.DeepEqual(derived.ops, []string{"delete ray-train/platform/datasets/dataset-a/" + key}) {
		t.Fatalf("delete ops=%v", derived.ops)
	}

	forged := plan
	forged.DeletableKeys = append(forged.DeletableKeys, "objects/sha256/5e/"+testPublicationDigestB+".parquet")
	derived.ops = nil
	if _, err := service.DeleteGarbage(context.Background(), forged); !errors.Is(err, ErrInvalidGarbageCollection) {
		t.Fatalf("forged plan error=%v want invalid gc", err)
	}
	if len(derived.ops) != 0 {
		t.Fatalf("forged plan deleted before complete validation: %v", derived.ops)
	}

	invalid := []GarbageCollectionCandidate{
		{DatasetID: "dataset-a", Key: "manifests/version-1.parquet", RetiredAt: retired, ReferenceCount: 0},
		{DatasetID: "dataset-a", Key: "temp/pub-1/object.parquet", RetiredAt: retired, ReferenceCount: 0},
		{DatasetID: "dataset-b", Key: key, RetiredAt: retired, ReferenceCount: 0},
		{DatasetID: "dataset-a", Key: key, RetiredAt: now.Add(time.Hour), ReferenceCount: 0},
		{DatasetID: "dataset-a", Key: key, RetiredAt: retired, ReferenceCount: -1},
	}
	for _, candidate := range invalid {
		t.Run(candidate.Key+candidate.DatasetID, func(t *testing.T) {
			_, err := service.PlanGarbageCollection(context.Background(), GarbageCollectionRequest{Now: now, Retention: 24 * time.Hour, Candidates: []GarbageCollectionCandidate{candidate}})
			if !errors.Is(err, ErrInvalidGarbageCollection) {
				t.Fatalf("error=%v want invalid gc", err)
			}
		})
	}
}

func TestPublicationGarbageCollectorRechecksReferencesUnderDeletionFence(t *testing.T) {
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	key := "objects/sha256/05/" + testPublicationDigestA + ".parquet"
	derived := newRecordingDerivedStore()
	guard := &recordingGarbageCollectionGuard{references: map[string]int{}}
	service := NewPublicationServiceWithGarbageCollectionGuard(mustPublicationStore(t, derived), guard)
	plan, err := service.PlanGarbageCollection(context.Background(), GarbageCollectionRequest{
		Now: now, Retention: time.Hour,
		Candidates: []GarbageCollectionCandidate{{DatasetID: "dataset-a", Key: key, RetiredAt: now.Add(-2 * time.Hour)}},
	})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}

	guard.references[key] = 1
	if _, err := service.DeleteGarbage(context.Background(), plan); !errors.Is(err, ErrInvalidGarbageCollection) {
		t.Fatalf("stale plan error=%v want invalid gc", err)
	}
	if len(derived.ops) != 0 {
		t.Fatalf("newly referenced shard was deleted: %v", derived.ops)
	}
}

func TestPublicationGarbageCollectorFailsClosedWithoutAuthorityGuard(t *testing.T) {
	service := NewPublicationService(mustPublicationStore(t, newRecordingDerivedStore()))
	if _, err := service.DeleteGarbage(context.Background(), GarbageCollectionPlan{}); !errors.Is(err, ErrInvalidGarbageCollection) {
		t.Fatalf("error=%v want invalid gc", err)
	}
}

func TestPublicationGarbageCollectorSanitizesAuthorityFailures(t *testing.T) {
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	key := "objects/sha256/05/" + testPublicationDigestA + ".parquet"
	derived := newRecordingDerivedStore()
	guard := &recordingGarbageCollectionGuard{
		references: map[string]int{},
		err:        errors.New("postgres password=secret is unavailable"),
	}
	service := NewPublicationServiceWithGarbageCollectionGuard(mustPublicationStore(t, derived), guard)
	plan, err := service.PlanGarbageCollection(context.Background(), GarbageCollectionRequest{
		Now: now, Retention: time.Hour,
		Candidates: []GarbageCollectionCandidate{{DatasetID: "dataset-a", Key: key, RetiredAt: now.Add(-2 * time.Hour)}},
	})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}

	_, err = service.DeleteGarbage(context.Background(), plan)
	if !errors.Is(err, objectstore.ErrUnavailable) || strings.Contains(err.Error(), "password") {
		t.Fatalf("error=%v want sanitized unavailable", err)
	}
	if len(derived.ops) != 0 {
		t.Fatalf("authority failure reached storage: %v", derived.ops)
	}
}

func mustPublicationStore(t *testing.T, derived *recordingDerivedStore) *DatasetPublicationObjectStore {
	t.Helper()
	store, err := NewDatasetPublicationObjectStore(PublicationStoreConfig{
		SourceRoot:     "raw/datasets",
		InternalPrefix: "ray-train/platform/datasets",
		DatasetID:      "dataset-a",
	}, &recordingSourceStore{}, derived)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	return store
}

func publicationBundle(publicationID, datasetID, versionID string, manifest PublicationBlob, shards ...PublicationBlob) PublicationBundle {
	return PublicationBundle{
		PublicationID: publicationID,
		DatasetID:     datasetID,
		VersionID:     versionID,
		Manifest:      manifest,
		Shards:        shards,
	}
}

func publicationBlob(content, digest string) PublicationBlob {
	return PublicationBlob{Digest: digest, SizeBytes: int64(len(content)), Open: reopenable(content)}
}

func storedPublicationObject(content, digest string) storedObject {
	return storedObject{content: []byte(content), info: PublicationObjectInfo{SizeBytes: int64(len(content)), SHA256: digest, Metadata: map[string]string{"sha256": digest}}}
}

func reopenable(content string) func() (io.ReadCloser, error) {
	return func() (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader(content)), nil
	}
}

func oneShotOpener(content string) func() (io.ReadCloser, error) {
	opened := false
	return func() (io.ReadCloser, error) {
		if opened {
			return nil, errors.New("already opened")
		}
		opened = true
		return io.NopCloser(strings.NewReader(content)), nil
	}
}

func changingOpener(first, second string) func() (io.ReadCloser, error) {
	opened := false
	return func() (io.ReadCloser, error) {
		content := first
		if opened {
			content = second
		}
		opened = true
		return io.NopCloser(strings.NewReader(content)), nil
	}
}

func digestFor(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

func hasOpPrefix(ops []string, prefix string) bool {
	for _, op := range ops {
		if strings.HasPrefix(op, prefix) {
			return true
		}
	}
	return false
}

type storedObject struct {
	content []byte
	info    PublicationObjectInfo
}

type recordingSourceStore struct {
	lastHead       string
	lastListPrefix string
	headErr        error
	heads          map[string]PublicationObjectInfo
	pages          map[string]PublicationObjectPage
}

func (store *recordingSourceStore) List(_ context.Context, prefix, cursor string, _ int) (PublicationObjectPage, error) {
	store.lastListPrefix = prefix
	if store.pages != nil {
		return store.pages[cursor], nil
	}
	return PublicationObjectPage{}, nil
}

func (store *recordingSourceStore) Head(_ context.Context, key string) (PublicationObjectInfo, error) {
	store.lastHead = key
	if store.headErr != nil {
		return PublicationObjectInfo{}, store.headErr
	}
	if info, ok := store.heads[key]; ok {
		return info.Clone(), nil
	}
	return PublicationObjectInfo{}, objectstore.ErrNotFound
}

func (store *recordingSourceStore) Get(_ context.Context, _ string) (io.ReadCloser, PublicationObjectInfo, error) {
	return io.NopCloser(bytes.NewReader(nil)), PublicationObjectInfo{}, nil
}

type recordingDerivedStore struct {
	ops         []string
	objects     map[string]storedObject
	readExactly bool
	deleteErr   error
}

func newRecordingDerivedStore() *recordingDerivedStore {
	return &recordingDerivedStore{objects: map[string]storedObject{}}
}

func (store *recordingDerivedStore) Head(_ context.Context, key string) (PublicationObjectInfo, error) {
	store.ops = append(store.ops, "head "+key)
	object, ok := store.objects[key]
	if !ok {
		return PublicationObjectInfo{}, objectstore.ErrNotFound
	}
	return object.info.Clone(), nil
}

func (store *recordingDerivedStore) PutImmutable(_ context.Context, key, digest string, sizeBytes int64, body io.Reader) error {
	store.ops = append(store.ops, "put "+key)
	var payload []byte
	var err error
	if store.readExactly {
		payload = make([]byte, sizeBytes)
		_, err = io.ReadFull(body, payload)
	} else {
		payload, err = io.ReadAll(body)
	}
	if err != nil {
		return err
	}
	if _, exists := store.objects[key]; exists {
		return objectstore.ErrAlreadyExists
	}
	store.objects[key] = storedObject{content: payload, info: PublicationObjectInfo{SizeBytes: sizeBytes, SHA256: digest, Metadata: map[string]string{"sha256": digest}}}
	return nil
}

func (store *recordingDerivedStore) CopyImmutable(_ context.Context, sourceKey, destinationKey string) error {
	store.ops = append(store.ops, "copy "+sourceKey+" -> "+destinationKey)
	if _, exists := store.objects[destinationKey]; exists {
		return objectstore.ErrAlreadyExists
	}
	source, ok := store.objects[sourceKey]
	if !ok {
		return objectstore.ErrNotFound
	}
	store.objects[destinationKey] = source
	return nil
}

func (store *recordingDerivedStore) Delete(_ context.Context, key string) error {
	store.ops = append(store.ops, "delete "+key)
	if store.deleteErr != nil {
		return store.deleteErr
	}
	delete(store.objects, key)
	return nil
}

type recordingGarbageCollectionGuard struct {
	references map[string]int
	calls      int
	err        error
}

func (guard *recordingGarbageCollectionGuard) WithUnreferencedShards(
	ctx context.Context,
	_ string,
	keys []string,
	deleteObjects func(context.Context) error,
) error {
	guard.calls++
	if guard.err != nil {
		return guard.err
	}
	for _, key := range keys {
		if guard.references[key] != 0 {
			return ErrInvalidGarbageCollection
		}
	}
	return deleteObjects(ctx)
}
