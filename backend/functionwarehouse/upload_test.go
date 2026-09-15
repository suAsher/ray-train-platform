package functionwarehouse

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

const testUploadPrefix = "raytrain/550e8400-e29b-41d4-a716-446655440000"

func liveUploadedFixture() UploadedFile {
	return UploadedFile{
		URL:        "/system/api/file/preview/raytrain/acceptance-20260914//2026/09/14/1789371824297_raytrain-contract-20260914.safetensors",
		FilePath:   "raytrain/acceptance-20260914//2026/09/14/1789371824297_raytrain-contract-20260914.safetensors",
		Filename:   "raytrain-contract-20260914.safetensors",
		FileSHA256: "2e011ee9444b22c18b4b17b6385c68e43fa34fdfb827349f3134763a4c1495a4",
		FileSize:   100,
	}
}

func TestLiveRelativePreviewUploadResponse(t *testing.T) {
	expected := liveUploadedFixture()
	payload, err := json.Marshal(expected)
	if err != nil {
		t.Fatal(err)
	}
	actual, ok := completedFile(payload, expected.Filename, expected.FileSize, expected.FileSHA256)
	if !ok || actual != expected {
		t.Fatalf("live relative URL rejected or changed: %#v", actual)
	}
	if !strings.Contains(actual.URL, "20260914//2026") {
		t.Fatal("upstream path was normalized")
	}
}

func TestRelativePreviewURLRejectsUnsafePaths(t *testing.T) {
	for _, address := range []string{
		"//evil.example/system/api/file/preview/object",
		"/other/object", "/system/api/file/preview/",
		"/system/api/file/preview/object?token=secret",
		"/system/api/file/preview/object?",
		"/system/api/file/preview/object#fragment",
		"/system/api/file/preview/object#",
		"/system/api/file/preview/../outside",
		"/system/api/file/preview/%2e%2e/outside",
		"/system/api/file/preview/%252e%252e/outside",
		"/system/api/file/preview/%5cevil",
		"/system/api/file/preview/%0aevil",
		"https://user:password@storage.example/object",
	} {
		file := liveUploadedFixture()
		file.URL = address
		if validUploadedFile(file) {
			t.Errorf("unsafe upload URL accepted: %q", address)
		}
	}
}

func digestOf(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func uploadedFixture(data []byte) UploadedFile {
	return UploadedFile{URL: "https://storage.example/weights.pth", Filename: "weights.pth", FileSHA256: digestOf(data), FileSize: int64(len(data))}
}

func TestUploadFileStreamingMultipartAndVerifiedResult(t *testing.T) {
	for _, name := range []string{"weights.pth", "train.yaml", "infer.yml", "model.config", "config.py", "metadata.json", "README", "artifact.custom"} {
		t.Run(name, func(t *testing.T) {
			data := []byte("opaque artifact content")
			want := uploadedFixture(data)
			want.Filename = name
			want.URL = "https://storage.example/" + name
			client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/system/api/file/minio/upload" || r.URL.Query().Get("prefix") != testUploadPrefix || r.Header.Get("Authorization") != "Bearer token" {
					t.Error("bad upload contract")
				}
				reader, err := r.MultipartReader()
				if err != nil {
					t.Fatal(err)
				}
				part, err := reader.NextPart()
				if err != nil || part.FormName() != "file" || part.FileName() != name {
					t.Fatal("missing multipart file")
				}
				actual, err := io.ReadAll(part)
				if err != nil || !bytes.Equal(actual, data) {
					t.Fatal("incorrect uploaded bytes")
				}
				if _, err := reader.NextPart(); err != io.EOF {
					t.Fatal("multipart was not terminated")
				}
				writeEnvelope(w, want)
			})
			file, err := client.UploadFile(context.Background(), "token", name, testUploadPrefix, int64(len(data)), digestOf(data), bytes.NewReader(data))
			if err != nil || file != want {
				t.Fatalf("upload=%#v error=%v", file, err)
			}
		})
	}
}

func TestUploadProcessVerifiedSnakeCaseResult(t *testing.T) {
	data := []byte("weights")
	var posts atomic.Int32
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			posts.Add(1)
			_, _ = io.Copy(io.Discard, r.Body)
			writeEnvelope(w, map[string]any{"status": "uploading"})
			return
		}
		if r.URL.Path != "/system/api/file/minio/upload/process" || r.URL.Query().Get("sha256") != digestOf(data) {
			t.Error("bad process query")
		}
		writeEnvelope(w, map[string]any{"status": "done", "schedule": 100, "result": []any{map[string]any{
			"filename": "weights.pth", "url": "https://storage.example/weights.pth", "file_sha256": digestOf(data), "file_size": fmt.Sprint(len(data)),
		}}})
	})
	file, err := client.Upload(context.Background(), "token", "weights.pth", testUploadPrefix, int64(len(data)), digestOf(data), func(context.Context) (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(data)), nil })
	if err != nil || file != uploadedFixture(data) || posts.Load() != 1 {
		t.Fatalf("upload=%#v error=%v posts=%d", file, err, posts.Load())
	}
}

func TestUploadRejectsLocalIntegrityMismatch(t *testing.T) {
	data := []byte("weights")
	for _, mode := range []string{"sha", "short", "long"} {
		t.Run(mode, func(t *testing.T) {
			client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				writeEnvelope(w, uploadedFixture(data))
			})
			digest := digestOf(data)
			size := int64(len(data))
			if mode == "sha" {
				digest = strings.Repeat("0", 64)
			}
			if mode == "short" {
				size++
			}
			if mode == "long" {
				size--
			}
			if _, err := client.UploadFile(context.Background(), "token", "weights.pth", testUploadPrefix, size, digest, bytes.NewReader(data)); !errors.Is(err, ErrUnknownOutcome) {
				t.Fatalf("integrity mismatch accepted: %v", err)
			}
		})
	}
}

func TestUploadRejectsUnsafeInputWithoutOpeningSource(t *testing.T) {
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) { t.Error("unsafe upload request sent") })
	for _, input := range []struct {
		name, prefix string
		size         int64
	}{
		{"../weights", testUploadPrefix, 1}, {"weights.pth", "../../other", 1}, {"weights.pth", "raytrain/not-operation-id", 1},
		{"weights.pth", testUploadPrefix, MaxUploadSize + 1}, {"weights.pth", testUploadPrefix, 0},
	} {
		_, err := client.Upload(context.Background(), "token", input.name, input.prefix, input.size, strings.Repeat("a", 64), func(context.Context) (io.ReadCloser, error) { t.Error("unsafe input opened source"); return nil, nil })
		if !errors.Is(err, ErrInvalid) {
			t.Errorf("unsafe input accepted: %v", err)
		}
	}
}

func TestChunkUploadTwoPassIntegrityAndExactProtocol(t *testing.T) {
	data := []byte("abcdefghijklmnopq")
	var opens atomic.Int32
	var chunks atomic.Int32
	var merges atomic.Int32
	fileMD5 := md5.Sum(data)
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1024); err != nil {
			t.Fatal(err)
		}
		defer r.MultipartForm.RemoveAll()
		switch r.URL.Path {
		case "/system/api/file/minio/chunk/upload":
			index := int(chunks.Add(1)) - 1
			if r.FormValue("currentChunk") != fmt.Sprint(index) || r.FormValue("chunks") != "3" || r.FormValue("fileMd5") != hex.EncodeToString(fileMD5[:]) || r.FormValue("path") != testUploadPrefix || r.FormValue("size") != "17" {
				t.Error("bad chunk fields")
			}
			file, _, err := r.FormFile("file")
			if err != nil {
				t.Fatal(err)
			}
			actual, err := io.ReadAll(file)
			_ = file.Close()
			expected := data[index*6 : min((index+1)*6, len(data))]
			digest := md5.Sum(expected)
			if err != nil || !bytes.Equal(actual, expected) || r.FormValue("chunkSize") != fmt.Sprint(len(expected)) || r.FormValue("currentChunkMd5") != hex.EncodeToString(digest[:]) {
				t.Error("bad chunk bytes or digest")
			}
			writeEnvelope(w, nil)
		case "/system/api/file/minio/chunk/merge":
			merges.Add(1)
			if r.FormValue("chunkCount") != "3" || r.FormValue("fileName") != "weights.pth" || r.FormValue("sha256") != digestOf(data) || r.FormValue("prefix") != testUploadPrefix {
				t.Error("bad merge fields")
			}
			writeEnvelope(w, uploadedFixture(data))
		default:
			t.Errorf("unexpected endpoint %s", r.URL.Path)
		}
	})
	file, err := client.uploadChunks(context.Background(), "token", "weights.pth", testUploadPrefix, int64(len(data)), digestOf(data), 6, func(context.Context) (io.ReadCloser, error) {
		opens.Add(1)
		return io.NopCloser(bytes.NewReader(data)), nil
	})
	if err != nil || file != uploadedFixture(data) || opens.Load() != 2 || chunks.Load() != 3 || merges.Load() != 1 {
		t.Fatalf("file=%#v err=%v opens=%d chunks=%d merges=%d", file, err, opens.Load(), chunks.Load(), merges.Load())
	}
}

func TestChunkSourceChangedBetweenPassesNeverMerges(t *testing.T) {
	data := []byte("abcdefghijklmnopq")
	var opens atomic.Int32
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/system/api/file/minio/chunk/upload" {
			t.Error("merged or cleaned invalid shared chunks")
		}
		_, _ = io.Copy(io.Discard, r.Body)
		writeEnvelope(w, nil)
	})
	_, err := client.uploadChunks(context.Background(), "token", "weights.pth", testUploadPrefix, int64(len(data)), digestOf(data), 6, func(context.Context) (io.ReadCloser, error) {
		if opens.Add(1) == 1 {
			return io.NopCloser(bytes.NewReader(data)), nil
		}
		return io.NopCloser(strings.NewReader(strings.Repeat("z", len(data)))), nil
	})
	if !errors.Is(err, ErrUnknownOutcome) {
		t.Fatalf("changed source accepted: %v", err)
	}
}

func TestChunkFirstPassMismatchDoesNotMutate(t *testing.T) {
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) { t.Error("unverified bytes sent") })
	_, err := client.uploadChunks(context.Background(), "token", "weights.pth", testUploadPrefix, 3, strings.Repeat("0", 64), 2, func(context.Context) (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("abc")), nil })
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad first pass accepted: %v", err)
	}
}

func TestUploadServerIntegrityFailureRemainsUnknown(t *testing.T) {
	data := []byte("weights")
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			_, _ = io.Copy(io.Discard, r.Body)
			writeEnvelope(w, map[string]any{"status": "uploading"})
			return
		}
		writeEnvelope(w, map[string]any{"status": "error", "msg": "private error"})
	})
	_, err := client.UploadFile(context.Background(), "token", "weights.pth", testUploadPrefix, int64(len(data)), digestOf(data), bytes.NewReader(data))
	if !errors.Is(err, ErrUnknownOutcome) || strings.Contains(err.Error(), "private") {
		t.Fatalf("unverified result accepted: %v", err)
	}
}
