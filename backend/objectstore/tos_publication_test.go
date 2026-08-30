package objectstore

import (
	"bytes"
	"context"
	"errors"
	"io"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestTOSPublicationStoreUsesImmutableDerivedOperationsAndSanitizesErrors(t *testing.T) {
	client := newRecordingPublicationClient()
	store := &TOSStore{client: client, bucket: "secret-bucket"}
	publication := mustTOSPublicationStore(t, store)

	if err := publication.PutImmutable(context.Background(), "ray-train/platform/datasets/dataset-a/temp/pub/object.parquet", testDigest, 3, strings.NewReader("abc")); err != nil {
		t.Fatalf("put immutable: %v", err)
	}
	if err := publication.CopyImmutable(context.Background(), "ray-train/platform/datasets/dataset-a/temp/pub/object.parquet", "ray-train/platform/datasets/dataset-a/objects/sha256/01/"+testDigest+".parquet"); err != nil {
		t.Fatalf("copy immutable: %v", err)
	}
	if err := publication.Delete(context.Background(), "ray-train/platform/datasets/dataset-a/objects/sha256/01/"+testDigest+".parquet"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	want := []string{
		"put ray-train/platform/datasets/dataset-a/temp/pub/object.parquet sha256=" + testDigest + " size=3",
		"copy ray-train/platform/datasets/dataset-a/temp/pub/object.parquet -> ray-train/platform/datasets/dataset-a/objects/sha256/01/" + testDigest + ".parquet",
		"delete ray-train/platform/datasets/dataset-a/objects/sha256/01/" + testDigest + ".parquet",
	}
	if !reflect.DeepEqual(client.ops, want) {
		t.Fatalf("ops=%v", client.ops)
	}

	client.putErr = errors.New("bucket=secret-bucket ak=secret signedURL=https://secret")
	err := publication.PutImmutable(context.Background(), "ray-train/platform/datasets/dataset-a/temp/pub/other.parquet", testDigest, 3, strings.NewReader("abc"))
	if !errors.Is(err, ErrUnavailable) || strings.Contains(err.Error(), "secret") {
		t.Fatalf("put error leaked detail: %v", err)
	}
}

func TestTOSPublicationStoreHeadGetAndListValidateResponses(t *testing.T) {
	client := newRecordingPublicationClient()
	observedAt := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	client.objects["ray-train/platform/datasets/dataset-a/object.parquet"] = []byte("abc")
	client.objects["ray-train/public/labeled/scene-a/token.pkl"] = []byte("raw")
	client.readETag = "etag-a"
	client.readObservedAt = observedAt
	client.pages[""] = tosArtifactListResponse{
		Objects: []tosArtifactObject{{
			Key: "ray-train/public/labeled/scene-a/token.pkl", SizeBytes: 3,
			ETag: "etag-a", LastModified: observedAt,
		}},
	}
	client.pages["cursor-a"] = tosArtifactListResponse{
		Objects:               []tosArtifactObject{{Key: "ray-train/platform/datasets/dataset-a/object.parquet", SizeBytes: 3}},
		NextContinuationToken: "cursor-a",
	}
	store := &TOSStore{client: client, bucket: "secret-bucket"}
	publication := mustTOSPublicationStore(t, store)

	info, err := publication.Head(context.Background(), "ray-train/platform/datasets/dataset-a/object.parquet")
	if err != nil {
		t.Fatalf("head: %v", err)
	}
	if info.SizeBytes != 3 || info.SHA256 != testDigest || info.Metadata["sha256"] != testDigest {
		t.Fatalf("head info=%+v", info)
	}
	body, info, err := publication.Get(context.Background(), "ray-train/public/labeled/scene-a/token.pkl")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	payload, readErr := io.ReadAll(body)
	closeErr := body.Close()
	if readErr != nil || closeErr != nil || !bytes.Equal(payload, []byte("raw")) || info.SizeBytes != 3 || info.ETag != "etag-a" || !info.ObservedAt.Equal(observedAt) {
		t.Fatalf("get payload=%q info=%+v read=%v close=%v", payload, info, readErr, closeErr)
	}
	page, err := publication.List(context.Background(), "ray-train/public/labeled/", "", 100)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	wantObjects := []PublicationListedObject{{
		Key: "ray-train/public/labeled/scene-a/token.pkl", SizeBytes: 3,
		ETag: "etag-a", ObservedAt: observedAt,
	}}
	if !reflect.DeepEqual(page.Objects, wantObjects) {
		t.Fatalf("list objects=%+v want=%+v", page.Objects, wantObjects)
	}
	_, err = publication.List(context.Background(), "ray-train/public/labeled", "cursor-a", 100)
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("looping cursor error=%v", err)
	}
}

func TestTOSPublicationStoreRejectsObjectsOutsideRequestedPrefix(t *testing.T) {
	client := newRecordingPublicationClient()
	client.pages[""] = tosArtifactListResponse{Objects: []tosArtifactObject{{
		Key: "ray-train/public/labeled-private/token.pkl", SizeBytes: 3,
	}}}
	publication := mustTOSPublicationStore(t, &TOSStore{client: client, bucket: "secret-bucket"})

	if _, err := publication.List(context.Background(), "ray-train/public/labeled/", "", 100); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("outside-prefix list error=%v want unavailable", err)
	}
}

func TestTOSPublicationStoreRejectsUnsafeKeysAndDeleteNotFoundIsIdempotent(t *testing.T) {
	client := newRecordingPublicationClient()
	client.deleteErr = ErrNotFound
	store := &TOSStore{client: client, bucket: "secret-bucket"}
	publication := mustTOSPublicationStore(t, store)

	for _, key := range []string{"../escape", "/absolute", "https://bucket/key", "safe\\key", "safe/%252e%252e/key"} {
		if _, err := publication.Head(context.Background(), key); err == nil {
			t.Fatalf("accepted unsafe key %q", key)
		}
	}
	if err := publication.Delete(context.Background(), "ray-train/platform/datasets/dataset-a/objects/sha256/01/"+testDigest+".parquet"); err != nil {
		t.Fatalf("delete not found should be idempotent: %v", err)
	}
}

func TestTOSPublicationStoreEnforcesSourceAndDerivedRoots(t *testing.T) {
	client := newRecordingPublicationClient()
	publication := mustTOSPublicationStore(t, &TOSStore{client: client, bucket: "secret-bucket"})

	if err := publication.PutImmutable(context.Background(), "ray-train/public/labeled/forbidden.parquet", testDigest, 3, strings.NewReader("abc")); err == nil {
		t.Fatal("derived put escaped into source root")
	}
	if _, _, err := publication.Get(context.Background(), "ray-train/platform/datasets/dataset-a/private.parquet"); err == nil {
		t.Fatal("source get escaped into derived root")
	}
	if err := publication.CopyImmutable(context.Background(),
		"ray-train/platform/datasets/dataset-a/source.parquet",
		"ray-train/platform/datasets/dataset-b/destination.parquet",
	); err == nil {
		t.Fatal("derived copy escaped into sibling dataset")
	}
	if err := publication.Delete(context.Background(), "ray-train/tenants/local/users/user-a/file"); err == nil {
		t.Fatal("derived delete escaped into tenant storage")
	}
	if len(client.ops) != 0 {
		t.Fatalf("out-of-scope operations reached TOS: %v", client.ops)
	}
}

func mustTOSPublicationStore(t *testing.T, store *TOSStore) *TOSPublicationStore {
	t.Helper()
	publication, err := store.PublicationObjects(
		"ray-train/public/labeled",
		"ray-train/platform/datasets/dataset-a",
	)
	if err != nil {
		t.Fatalf("publication store: %v", err)
	}
	return publication
}

type recordingPublicationClient struct {
	ops            []string
	objects        map[string][]byte
	pages          map[string]tosArtifactListResponse
	putErr         error
	copyErr        error
	deleteErr      error
	readETag       string
	readObservedAt time.Time
}

func newRecordingPublicationClient() *recordingPublicationClient {
	return &recordingPublicationClient{objects: map[string][]byte{}, pages: map[string]tosArtifactListResponse{}}
}

func (client *recordingPublicationClient) Presign(context.Context, tosPresignRequest) (*tosPresignResponse, error) {
	return nil, ErrUnavailable
}

func (client *recordingPublicationClient) Head(_ context.Context, _, key string) (ObjectInfo, error) {
	payload, ok := client.objects[key]
	if !ok {
		return ObjectInfo{}, ErrNotFound
	}
	return ObjectInfo{SizeBytes: int64(len(payload)), Metadata: map[string]string{"sha256": testDigest}}, nil
}

func (client *recordingPublicationClient) Put(_ context.Context, request tosPutRequest) error {
	client.ops = append(client.ops, "put "+request.Key+" sha256="+request.SHA256+" size="+strconvFormat(request.SizeBytes))
	if client.putErr != nil {
		return client.putErr
	}
	if _, exists := client.objects[request.Key]; exists {
		return ErrAlreadyExists
	}
	payload, err := io.ReadAll(request.Body)
	if err != nil {
		return err
	}
	client.objects[request.Key] = payload
	return nil
}

func (client *recordingPublicationClient) CopyObject(_ context.Context, request tosCopyRequest) error {
	client.ops = append(client.ops, "copy "+request.SourceKey+" -> "+request.DestinationKey)
	if client.copyErr != nil {
		return client.copyErr
	}
	source, ok := client.objects[request.SourceKey]
	if !ok {
		return ErrNotFound
	}
	if _, exists := client.objects[request.DestinationKey]; exists {
		return ErrAlreadyExists
	}
	client.objects[request.DestinationKey] = append([]byte(nil), source...)
	return nil
}

func (client *recordingPublicationClient) DeleteObject(_ context.Context, _, key string) error {
	client.ops = append(client.ops, "delete "+key)
	if client.deleteErr != nil {
		return client.deleteErr
	}
	delete(client.objects, key)
	return nil
}

func (client *recordingPublicationClient) ReadArtifact(_ context.Context, request tosArtifactReadRequest) (tosArtifactReadResponse, error) {
	payload, ok := client.objects[request.Key]
	if !ok {
		return tosArtifactReadResponse{}, ErrNotFound
	}
	return tosArtifactReadResponse{
		Content: io.NopCloser(bytes.NewReader(payload)), SizeBytes: int64(len(payload)),
		ContentType: "application/octet-stream", ETag: client.readETag, LastModified: client.readObservedAt,
	}, nil
}

func (client *recordingPublicationClient) ListArtifacts(_ context.Context, request tosArtifactListRequest) (tosArtifactListResponse, error) {
	return client.pages[request.ContinuationToken], nil
}

func strconvFormat(value int64) string {
	return strconv.FormatInt(value, 10)
}
