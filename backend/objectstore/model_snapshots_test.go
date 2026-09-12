package objectstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"testing"

	"ray-train-platform-backend/modellifecycle"
)

const modelSnapshotTestID = "12345678-1234-4234-8234-123456789abc"
const modelSnapshotTestETag = "\"model-source-etag-v1\""

func modelSnapshotDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func TestModelSnapshotsUseSeparateImmutableObjects(t *testing.T) {
	root := "ray-train/tenants/t/users/u/my-runs/job/output"
	sourceKey := root + "/model.pt"
	payload := []byte("model-weights")
	client := &modelSnapshotClient{objects: map[string][]byte{sourceKey: payload}}
	store := &TOSStore{client: client, bucket: "private"}
	ctx := context.Background()
	source, size, etag, err := store.ModelSnapshotSource().Read(ctx, root, "model.pt")
	if err != nil || size != int64(len(payload)) || etag != modelSnapshotTestETag {
		t.Fatalf("read source: size=%d etag=%q error=%v", size, etag, err)
	}
	data, err := io.ReadAll(source)
	_ = source.Close()
	if err != nil || !bytes.Equal(data, payload) || client.readKey != sourceKey {
		t.Fatalf("source scope: key=%q error=%v", client.readKey, err)
	}
	objects := store.ModelSnapshotObjects()
	for i := 0; i < 2; i++ {
		if err := objects.Put(ctx, modelSnapshotTestID, 0, modelSnapshotDigest(data), data); err != nil {
			t.Fatal(err)
		}
	}
	key := "raytrain-model-snapshots/" + modelSnapshotTestID + "/parts/0"
	if client.putKey != key || len(client.objects) != 2 || !bytes.Equal(client.objects[sourceKey], payload) {
		t.Fatalf("snapshot write escaped fixed prefix: key=%q objects=%d", client.putKey, len(client.objects))
	}
	changed := []byte("other-weights")
	if err := objects.Put(ctx, modelSnapshotTestID, 0, modelSnapshotDigest(changed), changed); !errors.Is(err, modellifecycle.ErrConflict) {
		t.Fatalf("overwriting immutable model returned %v", err)
	}
	if !bytes.Equal(client.objects[key], payload) {
		t.Fatal("duplicate different content replaced immutable part")
	}
	body, size, err := objects.Get(ctx, modelSnapshotTestID, 0)
	if err != nil || size != int64(len(payload)) {
		t.Fatalf("get size=%d error=%v", size, err)
	}
	got, err := io.ReadAll(body)
	_ = body.Close()
	if err != nil || !bytes.Equal(got, payload) || client.readKey != key {
		t.Fatalf("get key=%q error=%v", client.readKey, err)
	}
}

func TestModelSnapshotRejectsUnsafeIdentityPartAndPayload(t *testing.T) {
	client := &modelSnapshotClient{objects: map[string][]byte{}}
	objects := (&TOSStore{client: client}).ModelSnapshotObjects()
	ctx := context.Background()
	payload := []byte("weights")
	digest := modelSnapshotDigest(payload)
	for _, id := range []string{"", "../output", modelSnapshotTestID + "/../output", strings.ToUpper(modelSnapshotTestID), "/" + modelSnapshotTestID, "12345678%2foutput", "raytrain-mlflow-artifacts"} {
		if err := objects.Put(ctx, id, 0, digest, payload); !errors.Is(err, modellifecycle.ErrInvalid) {
			t.Errorf("unsafe id %q: %v", id, err)
		}
		if _, _, err := objects.Get(ctx, id, 0); !errors.Is(err, modellifecycle.ErrInvalid) {
			t.Errorf("unsafe get id %q: %v", id, err)
		}
	}
	for _, index := range []int{-1, int(modellifecycle.MaxFileSize / modellifecycle.PartSize), 1 << 30} {
		if err := objects.Put(ctx, modelSnapshotTestID, index, digest, payload); !errors.Is(err, modellifecycle.ErrInvalid) {
			t.Errorf("unsafe part %d: %v", index, err)
		}
		if _, _, err := objects.Get(ctx, modelSnapshotTestID, index); !errors.Is(err, modellifecycle.ErrInvalid) {
			t.Errorf("unsafe get part %d: %v", index, err)
		}
	}
	for _, data := range [][]byte{nil, make([]byte, modellifecycle.PartSize+1)} {
		if err := objects.Put(ctx, modelSnapshotTestID, 0, modelSnapshotDigest(data), data); !errors.Is(err, modellifecycle.ErrInvalid) {
			t.Errorf("invalid size %d: %v", len(data), err)
		}
	}
	for _, hash := range []string{"", "abc", strings.Repeat("g", 64), strings.ToUpper(digest), modelSnapshotDigest([]byte("different"))} {
		if err := objects.Put(ctx, modelSnapshotTestID, 0, hash, payload); !errors.Is(err, modellifecycle.ErrInvalid) {
			t.Errorf("invalid hash %q: %v", hash, err)
		}
	}
	if client.putKey != "" || client.readKey != "" {
		t.Fatal("invalid request contacted object storage")
	}
	if err := objects.Put(ctx, modelSnapshotTestID, int(modellifecycle.MaxFileSize/modellifecycle.PartSize)-1, digest, payload); err != nil {
		t.Fatalf("last valid part: %v", err)
	}
}

func TestModelSnapshotUnavailableAndInvalidResponses(t *testing.T) {
	ctx := context.Background()
	payload := []byte("weights")
	for _, store := range []*TOSStore{nil, {}, {client: &fakeTOSClient{}}} {
		if err := store.ModelSnapshotObjects().Put(ctx, modelSnapshotTestID, 0, modelSnapshotDigest(payload), payload); !errors.Is(err, modellifecycle.ErrUnavailable) {
			t.Errorf("unavailable put: %v", err)
		}
		if _, _, err := store.ModelSnapshotObjects().Get(ctx, modelSnapshotTestID, 0); !errors.Is(err, modellifecycle.ErrUnavailable) {
			t.Errorf("unavailable get: %v", err)
		}
		if _, _, _, err := store.ModelSnapshotSource().Read(ctx, "runs/job/output", "model.pt"); !errors.Is(err, modellifecycle.ErrUnavailable) {
			t.Errorf("unavailable source: %v", err)
		}
	}
	for _, size := range []int64{-1, 0, modellifecycle.PartSize + 1} {
		body := &modelSnapshotReadCloser{Reader: strings.NewReader("weights")}
		client := &modelSnapshotClient{response: &tosArtifactReadResponse{Content: body, SizeBytes: size}}
		if _, _, err := (&TOSStore{client: client}).ModelSnapshotObjects().Get(ctx, modelSnapshotTestID, 0); !errors.Is(err, modellifecycle.ErrUnavailable) || !body.closed {
			t.Errorf("invalid read size=%d error=%v closed=%v", size, err, body.closed)
		}
	}
	client := &modelSnapshotClient{response: &tosArtifactReadResponse{SizeBytes: 7}}
	if _, _, err := (&TOSStore{client: client}).ModelSnapshotObjects().Get(ctx, modelSnapshotTestID, 0); !errors.Is(err, modellifecycle.ErrUnavailable) {
		t.Fatalf("nil body: %v", err)
	}
	client = &modelSnapshotClient{putErr: errors.New("private SDK detail"), readErr: errors.New("private SDK detail")}
	store := &TOSStore{client: client}
	if err := store.ModelSnapshotObjects().Put(ctx, modelSnapshotTestID, 0, modelSnapshotDigest(payload), payload); err != modellifecycle.ErrUnavailable {
		t.Fatalf("put leaks storage error: %v", err)
	}
	if _, _, err := store.ModelSnapshotObjects().Get(ctx, modelSnapshotTestID, 0); err != modellifecycle.ErrUnavailable {
		t.Fatalf("get leaks storage error: %v", err)
	}
}

func TestModelSnapshotSourcePathAndSizeBoundary(t *testing.T) {
	client := &modelSnapshotClient{objects: map[string][]byte{}}
	source := (&TOSStore{client: client}).ModelSnapshotSource()
	for _, path := range []string{"", "../other/model.pt", "/model.pt", "a/../../other", "a\\model.pt", "%2e%2e/model.pt", "model.pt/", " model.pt", "model\x00.pt"} {
		if _, _, _, err := source.Read(context.Background(), "runs/job/output", path); !errors.Is(err, modellifecycle.ErrInvalid) {
			t.Errorf("unsafe path %q: %v", path, err)
		}
	}
	if client.readKey != "" {
		t.Fatal("unsafe source path contacted storage")
	}
	if _, _, _, err := source.Read(context.Background(), "runs/job/output", "missing.pt"); !errors.Is(err, modellifecycle.ErrNotFound) {
		t.Fatalf("missing source: %v", err)
	}
	for _, size := range []int64{0, modellifecycle.MaxFileSize + 1} {
		body := &modelSnapshotReadCloser{Reader: strings.NewReader("weights")}
		client.response = &tosArtifactReadResponse{Content: body, SizeBytes: size, ETag: modelSnapshotTestETag}
		if _, _, _, err := source.Read(context.Background(), "runs/job/output", "model.pt"); !errors.Is(err, modellifecycle.ErrInvalid) || !body.closed {
			t.Errorf("source size=%d error=%v closed=%v", size, err, body.closed)
		}
	}
}

func TestModelSnapshotSourceRequiresETagFromSameGet(t *testing.T) {
	for _, etag := range []string{"", " \t ", modelSnapshotTestETag, "\"model-source-etag-v2\""} {
		body := &modelSnapshotReadCloser{Reader: strings.NewReader("weights")}
		client := &modelSnapshotClient{response: &tosArtifactReadResponse{Content: body, SizeBytes: 7, ETag: etag}, fakeTOSClient: fakeTOSClient{headErr: errors.New("must not HEAD source")}}
		stream, size, gotETag, err := (&TOSStore{client: client}).ModelSnapshotSource().Read(context.Background(), "runs/job/output", "model.pt")
		if strings.TrimSpace(etag) == "" {
			if !errors.Is(err, modellifecycle.ErrUnavailable) || stream != nil || !body.closed {
				t.Fatalf("missing etag returned stream=%v error=%v closed=%v", stream, err, body.closed)
			}
			continue
		}
		if err != nil || size != 7 || gotETag != etag || body.closed {
			t.Fatalf("GET identity size=%d etag=%q error=%v closed=%v", size, gotETag, err, body.closed)
		}
		_ = stream.Close()
	}
}

func TestModelSnapshotVerifiesDeclaredStreamLength(t *testing.T) {
	for _, size := range []int64{3, 7, 9} {
		body := &modelSnapshotReadCloser{Reader: strings.NewReader("weights")}
		client := &modelSnapshotClient{response: &tosArtifactReadResponse{Content: body, SizeBytes: size}}
		stream, _, err := (&TOSStore{client: client}).ModelSnapshotObjects().Get(context.Background(), modelSnapshotTestID, 0)
		if err != nil {
			t.Fatal(err)
		}
		_, err = io.ReadAll(stream)
		_ = stream.Close()
		if size == 7 && err != nil {
			t.Fatalf("valid length: %v", err)
		}
		if size != 7 && !errors.Is(err, modellifecycle.ErrUnavailable) {
			t.Fatalf("size=%d error=%v", size, err)
		}
		if !body.closed {
			t.Fatal("close did not close source stream")
		}
	}
}

func TestModelSnapshotRetryRequiresMatchingMetadata(t *testing.T) {
	payload := []byte("weights")
	digest := modelSnapshotDigest(payload)
	for _, metadata := range []ObjectInfo{
		{SizeBytes: int64(len(payload)), Metadata: nil},
		{SizeBytes: int64(len(payload) + 1), Metadata: map[string]string{"sha256": digest}},
		{SizeBytes: int64(len(payload)), Metadata: map[string]string{"sha256": strings.Repeat("0", 64)}},
	} {
		client := &modelSnapshotClient{putErr: ErrAlreadyExists, headOverride: &metadata}
		err := (&TOSStore{client: client}).ModelSnapshotObjects().Put(context.Background(), modelSnapshotTestID, 0, digest, payload)
		if !errors.Is(err, modellifecycle.ErrConflict) {
			t.Fatalf("mismatched metadata: %v", err)
		}
	}
	client := &modelSnapshotClient{putErr: ErrAlreadyExists, fakeTOSClient: fakeTOSClient{headErr: ErrUnavailable}}
	if err := (&TOSStore{client: client}).ModelSnapshotObjects().Put(context.Background(), modelSnapshotTestID, 0, digest, payload); !errors.Is(err, modellifecycle.ErrUnavailable) {
		t.Fatalf("failed retry head: %v", err)
	}
}

func TestModelSnapshotCanceledCallsDoNotContactStorage(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	client := &modelSnapshotClient{objects: map[string][]byte{}}
	store := &TOSStore{client: client}
	payload := []byte("weights")
	if err := store.ModelSnapshotObjects().Put(ctx, modelSnapshotTestID, 0, modelSnapshotDigest(payload), payload); !errors.Is(err, context.Canceled) {
		t.Fatalf("put: %v", err)
	}
	if _, _, err := store.ModelSnapshotObjects().Get(ctx, modelSnapshotTestID, 0); !errors.Is(err, context.Canceled) {
		t.Fatalf("get: %v", err)
	}
	if _, _, _, err := store.ModelSnapshotSource().Read(ctx, "runs/job/output", "model.pt"); !errors.Is(err, context.Canceled) {
		t.Fatalf("source: %v", err)
	}
	if client.putKey != "" || client.readKey != "" {
		t.Fatal("canceled request contacted storage")
	}
}

type modelSnapshotReadCloser struct {
	io.Reader
	closed bool
}

func (r *modelSnapshotReadCloser) Close() error { r.closed = true; return nil }

type modelSnapshotClient struct {
	fakeTOSClient
	objects      map[string][]byte
	putKey       string
	readKey      string
	putErr       error
	readErr      error
	response     *tosArtifactReadResponse
	headOverride *ObjectInfo
}

func (c *modelSnapshotClient) Head(_ context.Context, _, key string) (ObjectInfo, error) {
	if c.headErr != nil {
		return ObjectInfo{}, c.headErr
	}
	if c.headOverride != nil {
		return *c.headOverride, nil
	}
	data, ok := c.objects[key]
	if !ok {
		return ObjectInfo{}, ErrNotFound
	}
	return ObjectInfo{SizeBytes: int64(len(data)), Metadata: map[string]string{"sha256": modelSnapshotDigest(data)}}, nil
}

func (c *modelSnapshotClient) Put(_ context.Context, request tosPutRequest) error {
	c.putKey = request.Key
	if c.putErr != nil {
		return c.putErr
	}
	if _, ok := c.objects[request.Key]; ok {
		return ErrAlreadyExists
	}
	data, err := io.ReadAll(request.Body)
	if err != nil {
		return err
	}
	c.objects[request.Key] = data
	return nil
}

func (c *modelSnapshotClient) ReadArtifact(_ context.Context, request tosArtifactReadRequest) (tosArtifactReadResponse, error) {
	c.readKey = request.Key
	if c.readErr != nil {
		return tosArtifactReadResponse{}, c.readErr
	}
	if c.response != nil {
		return *c.response, nil
	}
	data, ok := c.objects[request.Key]
	if !ok {
		return tosArtifactReadResponse{}, ErrNotFound
	}
	return tosArtifactReadResponse{Content: io.NopCloser(bytes.NewReader(data)), SizeBytes: int64(len(data)), ETag: modelSnapshotTestETag}, nil
}
