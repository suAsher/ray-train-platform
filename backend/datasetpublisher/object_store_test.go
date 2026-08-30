package datasetpublisher

import (
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"ray-train-platform-backend/objectstore"
)

func TestDatasetPublicationStoreScopesSourceAndDerivedCapabilities(t *testing.T) {
	source := &recordingSourceStore{heads: map[string]PublicationObjectInfo{
		"ray-train/public/labeled/samples/token-1.pkl": {SizeBytes: 10, SHA256: testPublicationDigestA},
	}}
	derived := newRecordingDerivedStore()
	store, err := NewDatasetPublicationObjectStore(PublicationStoreConfig{
		SourceRoot:     "ray-train/public/labeled",
		InternalPrefix: "ray-train/platform/datasets",
		DatasetID:      "dataset-a",
	}, source, derived)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}

	if _, err := store.Source().Head(context.Background(), "samples/token-1.pkl"); err != nil {
		t.Fatalf("source head: %v", err)
	}
	if source.lastHead != "ray-train/public/labeled/samples/token-1.pkl" {
		t.Fatalf("source key=%q", source.lastHead)
	}
	if _, canWrite := any(store.Source()).(interface {
		PutImmutable(context.Context, string, string, int64, io.Reader) error
	}); canWrite {
		t.Fatal("source capability exposes write")
	}
	if _, canRead := any(store.Derived()).(interface {
		Get(context.Context, string) (io.ReadCloser, PublicationObjectInfo, error)
	}); canRead {
		t.Fatal("derived capability exposes read")
	}

	if err := store.Derived().PutImmutable(context.Background(), "objects/sha256/05/"+testPublicationDigestA+".parquet", testPublicationDigestA, 3, strings.NewReader("abc")); err != nil {
		t.Fatalf("derived put: %v", err)
	}
	if got := derived.ops[len(derived.ops)-1]; got != "put ray-train/platform/datasets/dataset-a/objects/sha256/05/"+testPublicationDigestA+".parquet" {
		t.Fatalf("derived op=%q", got)
	}
}

func TestDatasetPublicationStoreListsExactSourceRootAndReturnsRelativeKeys(t *testing.T) {
	observedAt := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	source := &recordingSourceStore{pages: map[string]PublicationObjectPage{
		"": {Objects: []PublicationListedObject{{
			Key: "ray-train/public/labeled/scene-a/token-1.pkl", SizeBytes: 10,
			ETag: "etag-a", ObservedAt: observedAt,
		}}},
	}}
	store, err := NewDatasetPublicationObjectStore(PublicationStoreConfig{
		SourceRoot:     "ray-train/public/labeled",
		InternalPrefix: "ray-train/platform/datasets",
		DatasetID:      "dataset-a",
	}, source, newRecordingDerivedStore())
	if err != nil {
		t.Fatalf("new store: %v", err)
	}

	page, err := store.Source().List(context.Background(), "", "", 100)
	if err != nil {
		t.Fatalf("list source root: %v", err)
	}
	if source.lastListPrefix != "ray-train/public/labeled/" {
		t.Fatalf("list prefix=%q", source.lastListPrefix)
	}
	want := []PublicationListedObject{{Key: "scene-a/token-1.pkl", SizeBytes: 10, ETag: "etag-a", ObservedAt: observedAt}}
	if !reflect.DeepEqual(page.Objects, want) {
		t.Fatalf("objects=%+v want=%+v", page.Objects, want)
	}
}

func TestDatasetPublicationStoreRejectsListedObjectsOutsideSourceRoot(t *testing.T) {
	source := &recordingSourceStore{pages: map[string]PublicationObjectPage{
		"": {Objects: []PublicationListedObject{{Key: "ray-train/public/labeled-private/token.pkl"}}},
	}}
	store, err := NewDatasetPublicationObjectStore(PublicationStoreConfig{
		SourceRoot:     "ray-train/public/labeled",
		InternalPrefix: "ray-train/platform/datasets",
		DatasetID:      "dataset-a",
	}, source, newRecordingDerivedStore())
	if err != nil {
		t.Fatalf("new store: %v", err)
	}

	if _, err := store.Source().List(context.Background(), "", "", 100); !errors.Is(err, objectstore.ErrUnavailable) {
		t.Fatalf("outside-root list error=%v want unavailable", err)
	}
}

func TestDatasetPublicationStoreRejectsUnsafeAndEncodedPaths(t *testing.T) {
	store, err := NewDatasetPublicationObjectStore(PublicationStoreConfig{
		SourceRoot:     "raw/datasets",
		InternalPrefix: "ray-train/platform/datasets",
		DatasetID:      "dataset-a",
	}, &recordingSourceStore{}, newRecordingDerivedStore())
	if err != nil {
		t.Fatalf("new store: %v", err)
	}

	tests := []string{
		"../dataset-b/object",
		"/absolute",
		"https://bucket/key",
		"safe\\object",
		"safe/%2e%2e/object",
		"safe/%252e%252e/object",
		"safe/\x00/object",
	}
	for _, path := range tests {
		t.Run(path, func(t *testing.T) {
			if _, err := store.Source().Head(context.Background(), path); err == nil {
				t.Fatal("source accepted unsafe path")
			}
			if _, err := store.Derived().Head(context.Background(), path); err == nil {
				t.Fatal("derived accepted unsafe path")
			}
		})
	}
}

func TestDatasetPublicationStoreRejectsOverlappingSourceAndDerivedRoots(t *testing.T) {
	for _, internalPrefix := range []string{
		"ray-train/public/labeled",
		"ray-train/public",
		"ray-train/public/labeled/internal",
	} {
		_, err := NewDatasetPublicationObjectStore(PublicationStoreConfig{
			SourceRoot:     "ray-train/public/labeled",
			InternalPrefix: internalPrefix,
			DatasetID:      "dataset-a",
		}, &recordingSourceStore{}, newRecordingDerivedStore())
		if err == nil {
			t.Fatalf("accepted overlapping internal prefix %q", internalPrefix)
		}
	}
}

func TestDatasetPublicationStoreListRejectsCursorLoopAndBounds(t *testing.T) {
	source := &recordingSourceStore{
		pages: map[string]PublicationObjectPage{
			"cursor-a": {Objects: []PublicationListedObject{{Key: "a"}}, NextCursor: "cursor-a"},
		},
	}
	store, err := NewDatasetPublicationObjectStore(PublicationStoreConfig{
		SourceRoot:     "raw/datasets",
		InternalPrefix: "ray-train/platform/datasets",
		DatasetID:      "dataset-a",
	}, source, newRecordingDerivedStore())
	if err != nil {
		t.Fatalf("new store: %v", err)
	}

	_, err = store.Source().List(context.Background(), "samples", "cursor-a", 100)
	if err == nil {
		t.Fatal("accepted looping cursor")
	}
	_, err = store.Source().List(context.Background(), "samples", strings.Repeat("c", 4097), 100)
	if err == nil {
		t.Fatal("accepted oversized cursor")
	}
	_, err = store.Source().List(context.Background(), "samples", "", 10001)
	if err == nil {
		t.Fatal("accepted oversized limit")
	}
}

func TestDatasetPublicationStoreDoesNotLeakBackendErrors(t *testing.T) {
	source := &recordingSourceStore{headErr: errors.New("bucket=b accessKey=ak signedURL=https://secret request failed")}
	store, err := NewDatasetPublicationObjectStore(PublicationStoreConfig{
		SourceRoot:     "raw/datasets",
		InternalPrefix: "ray-train/platform/datasets",
		DatasetID:      "dataset-a",
	}, source, newRecordingDerivedStore())
	if err != nil {
		t.Fatalf("new store: %v", err)
	}

	_, err = store.Source().Head(context.Background(), "samples/token-1.pkl")
	if !errors.Is(err, objectstore.ErrUnavailable) {
		t.Fatalf("error=%v want sanitized unavailable", err)
	}
	if strings.Contains(err.Error(), "bucket=") || strings.Contains(err.Error(), "accessKey") || strings.Contains(err.Error(), "signedURL") {
		t.Fatalf("error leaked storage detail: %v", err)
	}
}

func TestPublicationObjectInfoDefensivelyCopiesMetadata(t *testing.T) {
	info := PublicationObjectInfo{SizeBytes: 1, SHA256: testPublicationDigestA, Metadata: map[string]string{"sha256": testPublicationDigestA}}
	clone := info.Clone()
	clone.Metadata["sha256"] = testPublicationDigestB
	if !reflect.DeepEqual(info.Metadata, map[string]string{"sha256": testPublicationDigestA}) {
		t.Fatalf("metadata leaked mutable state: %v", info.Metadata)
	}
}
