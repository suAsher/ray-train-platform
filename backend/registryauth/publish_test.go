package registryauth

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/google/go-containerregistry/pkg/registry"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/layout"
)

func testLayout(t *testing.T) (string, string) {
	t.Helper()
	directory := t.TempDir()
	p, err := layout.Write(directory, empty.Index)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.AppendImage(empty.Image); err != nil {
		t.Fatal(err)
	}
	digest, err := empty.Image.Digest()
	if err != nil {
		t.Fatal(err)
	}
	return directory, digest.String()
}

func TestLoadPublishedImageChecksFrozenDigest(t *testing.T) {
	directory, digest := testLayout(t)
	if _, err := loadPublishImage(context.Background(), directory, digest); err != nil {
		t.Fatal(err)
	}
	if _, err := loadPublishImage(context.Background(), directory, "sha256:"+string(make([]byte, 64))); !errors.Is(err, ErrArtifact) {
		t.Fatalf("invalid digest accepted: %v", err)
	}
}

func TestLoadPublishedImageRejectsSymlinkAndCorruptBlob(t *testing.T) {
	directory, digest := testLayout(t)
	if err := os.Symlink("/etc/passwd", filepath.Join(directory, "unexpected")); err != nil {
		t.Fatal(err)
	}
	if _, err := loadPublishImage(context.Background(), directory, digest); !errors.Is(err, ErrArtifact) {
		t.Fatalf("symlink accepted: %v", err)
	}
	if err := os.Remove(filepath.Join(directory, "unexpected")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "blobs", "sha256", digest[len("sha256:"):]), []byte(`{"schemaVersion":2}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadPublishImage(context.Background(), directory, digest); !errors.Is(err, ErrArtifact) {
		t.Fatalf("corrupt blob accepted: %v", err)
	}
}

func TestPublishRejectsInvalidTagBeforeReadingCredentials(t *testing.T) {
	_, err := NewClient().Publish(context.Background(), Credentials{}, PublishRequest{Project: "team", Repository: "model", Tag: "bad@tag"})
	if !errors.Is(err, ErrInvalidTarget) {
		t.Fatalf("expected invalid target, got %v", err)
	}
}

func TestPublishSealedLayoutEndToEndWithInMemoryRegistry(t *testing.T) {
	directory, digest := testLayout(t)
	handler := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
	var requests atomic.Int64
	client := testClient(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path == "/service/token" {
			body, _ := json.Marshal(map[string]string{"token": jwt([]string{"pull", "push"}, "team/model")})
			return reply(200, string(body)), nil
		}
		if username, _, ok := request.BasicAuth(); ok || username != "" {
			t.Fatal("personal credentials reached registry upload")
		}
		requests.Add(1)
		body := request.Body
		if body == nil {
			body = http.NoBody
		}
		serverRequest := httptest.NewRequestWithContext(request.Context(), request.Method, request.URL.String(), body)
		serverRequest.Header = request.Header.Clone()
		serverRequest.Host = request.Host
		serverRequest.ContentLength = request.ContentLength
		serverRequest.TransferEncoding = append([]string(nil), request.TransferEncoding...)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, serverRequest)
		response := recorder.Result()
		response.Request = request
		return response, nil
	})
	result, err := client.Publish(context.Background(), credentials, PublishRequest{LayoutPath: directory, Digest: digest, Project: "team", Repository: "model", Tag: "v1"})
	if err != nil || result.ImageDigest != digest || requests.Load() < 3 {
		t.Fatalf("result=%+v err=%v requests=%d", result, err, requests.Load())
	}
}
