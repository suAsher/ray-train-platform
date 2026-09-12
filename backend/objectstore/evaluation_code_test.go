package objectstore

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ray-train-platform-backend/domain"
	me "ray-train-platform-backend/modelevaluation"
)

const evaluationCodeTestID = "0123456789abcdef0123456789abcdef"

func evaluationCodeZip(t *testing.T, name, content string, mode os.FileMode) []byte {
	t.Helper()
	var buffer bytes.Buffer
	w := zip.NewWriter(&buffer)
	h := &zip.FileHeader{Name: name, Method: zip.Deflate}
	if mode != 0 {
		h.SetMode(mode)
	}
	f, err := w.CreateHeader(h)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(f, content); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func evaluationCodeArtifact(t *testing.T, data []byte) domain.SourceArtifact {
	t.Helper()
	sum := sha256.Sum256(data)
	now := time.Now().UTC()
	a, err := domain.NewRequestScopedSourceArtifact(domain.SourceArtifactInput{ID: "code-upload", TenantID: "team", UserID: "owner", SHA256: hex.EncodeToString(sum[:]), SizeBytes: int64(len(data))}, now.Add(time.Hour), now)
	if err != nil {
		t.Fatal(err)
	}
	a, err = a.MarkReady(now)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func evaluationCodeNoSpools(t *testing.T, dir string) {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "raytrain-evaluation-code-*"))
	if err != nil || len(files) != 0 {
		t.Fatalf("temporary code archive leak: files=%v error=%v", files, err)
	}
}

func TestEvaluationCodePublishesVerifiedSeparateImmutableArchive(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)
	data := evaluationCodeZip(t, "evaluate.py", "print('evaluate')", 0)
	artifact := evaluationCodeArtifact(t, data)
	client := &evaluationCodeClient{objects: map[string][]byte{artifact.ObjectKey: data}}
	store := (&TOSStore{client: client, bucket: "private"}).EvaluationCode()
	snapshot, err := store.Publish(context.Background(), evaluationCodeTestID, artifact)
	if err != nil || snapshot.ID != evaluationCodeTestID || snapshot.SHA256 != artifact.SHA256 || snapshot.SizeBytes != artifact.SizeBytes || snapshot.Format != "zip" {
		t.Fatalf("snapshot=%+v error=%v", snapshot, err)
	}
	key := "raytrain-evaluation-code/" + evaluationCodeTestID + "/source.zip"
	if client.put.Key != key || client.put.Bucket != "private" || client.put.ContentType != "application/zip" || !client.spooledPut || !bytes.Equal(client.objects[artifact.ObjectKey], data) {
		t.Fatalf("unsafe/nonspooled publish: %+v", client.put)
	}
	evaluationCodeNoSpools(t, dir)
	if _, err := store.Publish(context.Background(), evaluationCodeTestID, artifact); err != nil {
		t.Fatalf("same immutable retry: %v", err)
	}
	body, size, err := store.Open(context.Background(), snapshot)
	if err != nil || size != artifact.SizeBytes {
		t.Fatalf("open size=%d error=%v", size, err)
	}
	actual, err := io.ReadAll(body)
	if err != nil || !bytes.Equal(actual, data) {
		t.Fatalf("open content mismatch: %v", err)
	}
	if err := body.Close(); err != nil {
		t.Fatal(err)
	}
	evaluationCodeNoSpools(t, dir)
	changed := evaluationCodeZip(t, "evaluate.py", "print('changed')", 0)
	other := evaluationCodeArtifact(t, changed)
	client.objects[other.ObjectKey] = changed
	if _, err := store.Publish(context.Background(), evaluationCodeTestID, other); !errors.Is(err, me.ErrConflict) {
		t.Fatalf("immutable overwrite: %v", err)
	}
	if !bytes.Equal(client.objects[key], data) {
		t.Fatal("immutable code was replaced")
	}
	evaluationCodeNoSpools(t, dir)
}

func TestEvaluationCodeRejectsUntrustedIdentityAndSource(t *testing.T) {
	data := evaluationCodeZip(t, "evaluate.py", "pass", 0)
	artifact := evaluationCodeArtifact(t, data)
	client := &evaluationCodeClient{objects: map[string][]byte{artifact.ObjectKey: data}}
	store := (&TOSStore{client: client}).EvaluationCode()
	for _, id := range []string{"", "../output", strings.ToUpper(evaluationCodeTestID), evaluationCodeTestID + "/source.zip", strings.Repeat("a", 33), "%2e%2e"} {
		if _, err := store.Publish(context.Background(), id, artifact); !errors.Is(err, me.ErrInvalid) {
			t.Fatalf("unsafe id %q: %v", id, err)
		}
	}
	for _, change := range []func(*domain.SourceArtifact){
		func(a *domain.SourceArtifact) { a.ObjectKey = "ray-train/other/output/source.zip" },
		func(a *domain.SourceArtifact) { a.SHA256 = strings.Repeat("A", 64) },
		func(a *domain.SourceArtifact) { a.SizeBytes = 0 },
		func(a *domain.SourceArtifact) { a.SizeBytes = me.MaxEvaluationCodeSize + 1 },
	} {
		bad := artifact
		change(&bad)
		if _, err := store.Publish(context.Background(), evaluationCodeTestID, bad); !errors.Is(err, me.ErrInvalid) {
			t.Fatalf("bad source accepted: %+v %v", bad, err)
		}
	}
	pending := artifact
	pending.State = domain.SourceArtifactPending
	if _, err := store.Publish(context.Background(), evaluationCodeTestID, pending); !errors.Is(err, me.ErrNotReady) {
		t.Fatalf("pending source: %v", err)
	}
	if client.reads != 0 || client.put.Key != "" {
		t.Fatal("invalid input contacted storage")
	}
}

func TestEvaluationCodeRejectsChangedBytesAndUnsafeZipAndRemovesSpools(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)
	data := evaluationCodeZip(t, "evaluate.py", "pass", 0)
	artifact := evaluationCodeArtifact(t, data)
	for _, payload := range [][]byte{append(append([]byte{}, data...), 1), data[:len(data)-1], bytes.Repeat([]byte("x"), len(data))} {
		client := &evaluationCodeClient{objects: map[string][]byte{}, readResponse: &tosArtifactReadResponse{Content: io.NopCloser(bytes.NewReader(payload)), SizeBytes: artifact.SizeBytes}}
		if _, err := (&TOSStore{client: client}).EvaluationCode().Publish(context.Background(), evaluationCodeTestID, artifact); err == nil {
			t.Fatal("changed source bytes accepted")
		}
		if client.put.Key != "" {
			t.Fatal("corrupt bytes reached immutable destination")
		}
		evaluationCodeNoSpools(t, dir)
	}
	for _, payload := range [][]byte{
		[]byte("not a zip"), evaluationCodeZip(t, "../escape.py", "pass", 0), evaluationCodeZip(t, "/absolute.py", "pass", 0),
		evaluationCodeZip(t, "link.py", "../../outside", os.ModeSymlink|0777),
	} {
		a := evaluationCodeArtifact(t, payload)
		client := &evaluationCodeClient{objects: map[string][]byte{a.ObjectKey: payload}}
		if _, err := (&TOSStore{client: client}).EvaluationCode().Publish(context.Background(), evaluationCodeTestID, a); !errors.Is(err, me.ErrInvalid) {
			t.Fatalf("unsafe ZIP accepted: %v", err)
		}
		if client.put.Key != "" {
			t.Fatal("unsafe ZIP was published")
		}
		evaluationCodeNoSpools(t, dir)
	}
}

func TestEvaluationCodeOpenVerifiesStoredSnapshotAndAvailability(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)
	data := evaluationCodeZip(t, "evaluate.py", "pass", 0)
	artifact := evaluationCodeArtifact(t, data)
	snapshot := me.CodeSnapshot{ID: evaluationCodeTestID, SHA256: artifact.SHA256, SizeBytes: artifact.SizeBytes, Format: "zip"}
	key := "raytrain-evaluation-code/" + snapshot.ID + "/source.zip"
	for _, payload := range [][]byte{data[:len(data)-1], bytes.Repeat([]byte("x"), len(data))} {
		client := &evaluationCodeClient{objects: map[string][]byte{key: payload}}
		if body, _, err := (&TOSStore{client: client}).EvaluationCode().Open(context.Background(), snapshot); err == nil || body != nil {
			t.Fatalf("corrupt stored snapshot returned body=%v error=%v", body, err)
		}
		evaluationCodeNoSpools(t, dir)
	}
	for _, store := range []*TOSStore{nil, {}, {client: &fakeTOSClient{}}} {
		if _, err := store.EvaluationCode().Publish(context.Background(), evaluationCodeTestID, artifact); !errors.Is(err, me.ErrUnavailable) {
			t.Fatalf("unavailable publish: %v", err)
		}
		if _, _, err := store.EvaluationCode().Open(context.Background(), snapshot); !errors.Is(err, me.ErrUnavailable) {
			t.Fatalf("unavailable open: %v", err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (&TOSStore{}).EvaluationCode().Publish(ctx, evaluationCodeTestID, artifact); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel publish: %v", err)
	}
	evaluationCodeNoSpools(t, dir)
}

func TestEvaluationCodeRejectsInvalidResponsesAndCleansStorageFailures(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)
	data := evaluationCodeZip(t, "evaluate.py", "pass", 0)
	artifact := evaluationCodeArtifact(t, data)
	for _, size := range []int64{-1, 0, artifact.SizeBytes + 1} {
		body := &modelSnapshotReadCloser{Reader: bytes.NewReader(data)}
		client := &evaluationCodeClient{readResponse: &tosArtifactReadResponse{Content: body, SizeBytes: size}}
		if _, err := (&TOSStore{client: client}).EvaluationCode().Publish(context.Background(), evaluationCodeTestID, artifact); err == nil || !body.closed {
			t.Fatalf("bad GET size=%d error=%v closed=%v", size, err, body.closed)
		}
		evaluationCodeNoSpools(t, dir)
	}
	client := &evaluationCodeClient{readResponse: &tosArtifactReadResponse{SizeBytes: artifact.SizeBytes}}
	if _, err := (&TOSStore{client: client}).EvaluationCode().Publish(context.Background(), evaluationCodeTestID, artifact); !errors.Is(err, me.ErrUnavailable) {
		t.Fatalf("missing stream: %v", err)
	}
	for _, putErr := range []error{errors.New("private SDK failure"), ErrAlreadyExists} {
		client := &evaluationCodeClient{objects: map[string][]byte{artifact.ObjectKey: data}, putErr: putErr, fakeTOSClient: fakeTOSClient{headErr: ErrUnavailable}}
		if _, err := (&TOSStore{client: client}).EvaluationCode().Publish(context.Background(), evaluationCodeTestID, artifact); !errors.Is(err, me.ErrUnavailable) {
			t.Fatalf("storage failure: %v", err)
		}
		evaluationCodeNoSpools(t, dir)
	}
	client = &evaluationCodeClient{objects: map[string][]byte{artifact.ObjectKey: data}, putErr: ErrAlreadyExists, headOverride: &ObjectInfo{SizeBytes: artifact.SizeBytes, Metadata: map[string]string{"sha256": strings.Repeat("f", 64)}}}
	if _, err := (&TOSStore{client: client}).EvaluationCode().Publish(context.Background(), evaluationCodeTestID, artifact); !errors.Is(err, me.ErrConflict) {
		t.Fatalf("mismatched retry metadata: %v", err)
	}
	evaluationCodeNoSpools(t, dir)
	for _, bad := range []me.CodeSnapshot{
		{ID: "../source", SHA256: artifact.SHA256, SizeBytes: artifact.SizeBytes, Format: "zip"},
		{ID: evaluationCodeTestID, SHA256: artifact.SHA256, SizeBytes: me.MaxEvaluationCodeSize + 1, Format: "zip"},
	} {
		if _, _, err := (&TOSStore{client: client}).EvaluationCode().Open(context.Background(), bad); !errors.Is(err, me.ErrInvalid) {
			t.Fatalf("bad open snapshot: %v", err)
		}
	}
}

func TestEvaluationCodeValidatesCompressionCRCAndActualExpandedLength(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)
	valid := evaluationCodeZip(t, "evaluate.py", strings.Repeat("pass\n", 100), 0)
	central := bytes.Index(valid, []byte{'P', 'K', 1, 2})
	if central < 0 {
		t.Fatal("fixture has no central directory")
	}
	for _, change := range []func([]byte){
		func(data []byte) { data[central+16] ^= 1 },                       // CRC disagrees with real bytes.
		func(data []byte) { data[central+10] = 99; data[central+11] = 0 }, // Unsupported compression.
		func(data []byte) {
			data[central+24] = 1
			data[central+25] = 0
			data[central+26] = 0
			data[central+27] = 0
		}, // Forged expanded size.
	} {
		payload := append([]byte{}, valid...)
		change(payload)
		artifact := evaluationCodeArtifact(t, payload)
		client := &evaluationCodeClient{objects: map[string][]byte{artifact.ObjectKey: payload}}
		if _, err := (&TOSStore{client: client}).EvaluationCode().Publish(context.Background(), evaluationCodeTestID, artifact); !errors.Is(err, me.ErrInvalid) {
			t.Fatalf("corrupt ZIP registered: %v", err)
		}
		if client.put.Key != "" {
			t.Fatal("corrupt ZIP reached immutable prefix")
		}
		evaluationCodeNoSpools(t, dir)
	}
}

type evaluationCodeClient struct {
	fakeTOSClient
	objects      map[string][]byte
	put          tosPutRequest
	spooledPut   bool
	reads        int
	readResponse *tosArtifactReadResponse
	putErr       error
	headOverride *ObjectInfo
}

func (c *evaluationCodeClient) ReadArtifact(_ context.Context, request tosArtifactReadRequest) (tosArtifactReadResponse, error) {
	c.reads++
	if c.readResponse != nil {
		return *c.readResponse, nil
	}
	data, ok := c.objects[request.Key]
	if !ok {
		return tosArtifactReadResponse{}, ErrNotFound
	}
	return tosArtifactReadResponse{Content: io.NopCloser(bytes.NewReader(data)), SizeBytes: int64(len(data))}, nil
}
func (c *evaluationCodeClient) Put(_ context.Context, request tosPutRequest) error {
	c.put = request
	_, c.spooledPut = request.Body.(*os.File)
	if c.putErr != nil {
		return c.putErr
	}
	if _, exists := c.objects[request.Key]; exists {
		return ErrAlreadyExists
	}
	data, err := io.ReadAll(request.Body)
	if err != nil {
		return err
	}
	c.objects[request.Key] = data
	return nil
}
func (c *evaluationCodeClient) Head(_ context.Context, _, key string) (ObjectInfo, error) {
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
	sum := sha256.Sum256(data)
	return ObjectInfo{SizeBytes: int64(len(data)), Metadata: map[string]string{"sha256": hex.EncodeToString(sum[:])}}, nil
}
