package registryauth

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"

	"github.com/google/go-containerregistry/pkg/registry"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/layout"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/static"
	"github.com/google/go-containerregistry/pkg/v1/types"
)

type secretTimeout struct{}

func (secretTimeout) Error() string   { return "https://private.invalid/upload?_state=secret-fixture" }
func (secretTimeout) Timeout() bool   { return true }
func (secretTimeout) Temporary() bool { return true }

func TestPublishTransportPreservesSafeRetryClassification(t *testing.T) {
	for _, original := range []error{secretTimeout{}, io.ErrUnexpectedEOF, syscall.ECONNRESET} {
		guard := &publishTransport{repository: "team/model", base: roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, original })}
		request, _ := http.NewRequest("PATCH", Origin+"/v2/team/model/blobs/uploads/123?_state=secret-fixture", nil)
		_, err := guard.RoundTrip(request)
		var temporary interface{ Temporary() bool }
		if !errors.Is(err, ErrUnavailable) || !errors.As(err, &temporary) || !temporary.Temporary() || strings.Contains(err.Error(), "secret-fixture") || errors.Is(err, original) {
			t.Fatalf("unsafe retry error: %v", err)
		}
		if source, ok := original.(net.Error); ok {
			var got net.Error
			if !errors.As(err, &got) || got.Timeout() != source.Timeout() {
				t.Fatal("timeout classification lost")
			}
		}
	}
}

func TestPublishTransportDiagnosticsExposeOnlySafeFields(t *testing.T) {
	var events []PublishDiagnostic
	ctx := WithPublishDiagnostics(context.Background(), func(event PublishDiagnostic) { events = append(events, event) })
	guard := &publishTransport{repository: "team/model", base: roundTripFunc(func(*http.Request) (*http.Response, error) {
		response := reply(413, "secret-fixture")
		return response, nil
	})}
	request, _ := http.NewRequestWithContext(ctx, "PATCH", Origin+"/v2/team/model/blobs/uploads/123?_state=secret-fixture", nil)
	response, err := guard.RoundTrip(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	request, _ = http.NewRequestWithContext(ctx, "GET", "https://evil.invalid/?token=secret-fixture", nil)
	if _, err = guard.RoundTrip(request); err == nil {
		t.Fatal("unsafe target accepted")
	}
	payload, _ := json.Marshal(events)
	if strings.Contains(string(payload), "secret-fixture") || strings.Contains(string(payload), "invalid") || len(events) != 3 || events[1].Status != 413 || events[1].Method != "PATCH" || events[2].Code != "TRANSPORT_TARGET_REJECTED" {
		t.Fatalf("unsafe or incomplete diagnostics: %s", payload)
	}
}

func TestPublishNonEmptyLayoutRetriesInterruptedLayerAndReportsStages(t *testing.T) {
	var archive bytes.Buffer
	writer := tar.NewWriter(&archive)
	contents := []byte("non-empty-layer-for-upload-contract")
	if err := writer.WriteHeader(&tar.Header{Name: "fixture.txt", Mode: 0644, Size: int64(len(contents))}); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write(contents); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	image, err := mutate.AppendLayers(empty.Image, static.NewLayer(archive.Bytes(), types.OCIUncompressedLayer))
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	output, err := layout.Write(directory, empty.Index)
	if err != nil {
		t.Fatal(err)
	}
	if err = output.AppendImage(image); err != nil {
		t.Fatal(err)
	}
	digest, _ := image.Digest()
	handler := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
	var patches atomic.Int64
	var mu sync.Mutex
	stages := []string{}
	ctx := WithPublishDiagnostics(context.Background(), func(event PublishDiagnostic) {
		if event.Stage != "" {
			mu.Lock()
			stages = append(stages, event.Stage)
			mu.Unlock()
		}
	})
	client := testClient(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path == "/service/token" {
			body, _ := json.Marshal(map[string]string{"token": jwt([]string{"pull", "push"}, "team/model")})
			return reply(200, string(body)), nil
		}
		if request.Method == "PATCH" && patches.Add(1) == 1 {
			if request.Body != nil {
				request.Body.Close()
			}
			return nil, secretTimeout{}
		}
		body := request.Body
		if body == nil {
			body = http.NoBody
		}
		incoming := httptest.NewRequestWithContext(request.Context(), request.Method, request.URL.String(), body)
		incoming.Header = request.Header.Clone()
		incoming.Host = request.Host
		incoming.ContentLength = request.ContentLength
		incoming.TransferEncoding = append([]string(nil), request.TransferEncoding...)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, incoming)
		response := recorder.Result()
		response.Request = request
		return response, nil
	})
	result, err := client.Publish(ctx, credentials, PublishRequest{LayoutPath: directory, Digest: digest.String(), Project: "team", Repository: "model", Tag: "nonempty"})
	if err != nil || result.ImageDigest != digest.String() || patches.Load() < 3 {
		t.Fatalf("retry publish failed result=%+v err=%v patches=%d", result, err, patches.Load())
	}
	if strings.Join(stages, ",") != "LAYOUT_VALIDATING,REGISTRY_AUTH,REGISTRY_WRITE,REGISTRY_VERIFY" {
		t.Fatalf("missing stage evidence: %v", stages)
	}
}
