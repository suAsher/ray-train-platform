package rayctl

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"ray-train-platform-backend/domain"
)

func TestSubmitDirectoryCreatesUploadsCompletesThenSubmits(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "train.py"), []byte("print('train')\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	steps := make([]string, 0, 4)
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v1/source-artifacts":
			steps = append(steps, "create")
			if request.Header.Get("Authorization") != "Bearer test-token" {
				t.Fatal("API token missing")
			}
			var create map[string]any
			if err := json.NewDecoder(request.Body).Decode(&create); err != nil {
				t.Fatal(err)
			}
			if create["sha256"] == "" || create["sizeBytes"].(float64) < 1 {
				t.Fatalf("invalid create body=%v", create)
			}
			writeClientSuccess(t, writer, http.StatusCreated, map[string]any{
				"artifactId": "artifact-test", "state": "PENDING", "sha256": create["sha256"], "sizeBytes": create["sizeBytes"],
				"uploadUrl": serverURL(request) + "/upload?X-Tos-Signature=secret-signature", "requiredHeaders": map[string]string{"X-Required": "signed-value"},
				"contentLength": create["sizeBytes"], "uploadRequired": true,
			})
		case "/upload":
			steps = append(steps, "upload")
			if request.Header.Get("Authorization") != "" || request.Header.Get("X-Required") != "signed-value" {
				t.Fatal("direct upload headers were incorrect")
			}
			if _, err := io.ReadAll(request.Body); err != nil {
				t.Fatal(err)
			}
			writer.WriteHeader(http.StatusOK)
		case "/api/v1/source-artifacts/artifact-test/complete":
			steps = append(steps, "complete")
			writeClientSuccess(t, writer, http.StatusOK, map[string]any{"artifactId": "artifact-test", "state": "READY", "uploadRequired": false})
		case "/api/v1/jobs":
			steps = append(steps, "submit")
			var submit struct {
				Spec domain.JobSpec `json:"spec"`
			}
			if err := json.NewDecoder(request.Body).Decode(&submit); err != nil {
				t.Fatal(err)
			}
			if submit.Spec.Source.Type != "artifact" || submit.Spec.Source.ArtifactID != "artifact-test" {
				t.Fatalf("job source=%+v", submit.Spec.Source)
			}
			writeClientSuccess(t, writer, http.StatusAccepted, map[string]any{"id": "job-test"})
		default:
			t.Fatalf("unexpected request %s %s", request.Method, request.URL.Path)
		}
	}))
	defer server.Close()
	client, err := NewClient(ClientOptions{ServerURL: server.URL, Token: "test-token", HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	job, err := client.SubmitDirectory(context.Background(), root, testJobSpec())
	if err != nil {
		t.Fatal(err)
	}
	if job.ID != "job-test" || !reflect.DeepEqual(steps, []string{"create", "upload", "complete", "submit"}) {
		t.Fatalf("job=%+v steps=%v", job, steps)
	}
}

func TestDebugOutputRedactsTokenAndPresignedSignature(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Query().Get("X-Tos-Signature") != "secret-signature" {
			t.Fatalf("signature was not sent to the direct upload endpoint")
		}
		writer.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	archivePath := filepath.Join(t.TempDir(), "source.zip")
	if err := os.WriteFile(archivePath, []byte("zip"), 0o600); err != nil {
		t.Fatal(err)
	}
	var debug strings.Builder
	client, err := NewClient(ClientOptions{ServerURL: server.URL, Token: "secret-token", HTTPClient: server.Client(), DebugWriter: &debug})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Upload(context.Background(), Artifact{UploadURL: server.URL + "/upload?X-Tos-Signature=secret-signature", ContentLength: 3}, Archive{Path: archivePath, SizeBytes: 3}); err != nil {
		t.Fatal(err)
	}
	output := debug.String()
	if strings.Contains(output, "secret-token") || strings.Contains(output, "X-Tos-Signature=secret") || !strings.Contains(output, "X-Tos-Signature=REDACTED") {
		t.Fatalf("debug leaked credential: %q", output)
	}
}

func TestLoadTokenRejectsGroupReadableConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"token":"stored-token"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadToken("", path); err == nil {
		t.Fatal("group-readable token config was accepted")
	}
}

func TestStatusEscapesUserProvidedJobIDAsOnePathSegment(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.EscapedPath() != "/api/v1/jobs/job%2Fother" {
			t.Fatalf("escaped path=%q", request.URL.EscapedPath())
		}
		writeClientSuccess(t, writer, http.StatusOK, map[string]any{"id": "job/other"})
	}))
	defer server.Close()
	client, err := NewClient(ClientOptions{ServerURL: server.URL, Token: "test-token", HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Status(context.Background(), "job/other"); err != nil {
		t.Fatal(err)
	}
}

func TestNewClientRejectsInsecurePlatformURLBeforeRequest(t *testing.T) {
	if _, err := NewClient(ClientOptions{ServerURL: "http://platform.example.internal", Token: "test-token"}); err == nil {
		t.Fatal("HTTP platform URL was accepted")
	}
}

func TestUploadTransportFailureNeverLeaksPresignedQuery(t *testing.T) {
	archivePath := filepath.Join(t.TempDir(), "source.zip")
	if err := os.WriteFile(archivePath, []byte("zip"), 0o600); err != nil {
		t.Fatal(err)
	}
	client, err := NewClient(ClientOptions{
		ServerURL: "https://platform.example.internal", Token: "test-token",
		HTTPClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			return nil, errors.New("request failed for " + request.URL.String())
		})},
	})
	if err != nil {
		t.Fatal(err)
	}
	err = client.Upload(context.Background(), Artifact{UploadURL: "https://upload.example.internal/package?X-Tos-Signature=secret-signature", ContentLength: 3}, Archive{Path: archivePath, SizeBytes: 3})
	if err == nil {
		t.Fatal("upload transport failure was accepted")
	}
	if strings.Contains(err.Error(), "secret-signature") || strings.Contains(err.Error(), "X-Tos-Signature") {
		t.Fatalf("upload error leaked signed URL: %q", err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (roundTrip roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTrip(request)
}

func TestLogsAndCancelUseEstablishedJobEndpoints(t *testing.T) {
	requests := make([]string, 0, 2)
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests = append(requests, request.Method+" "+request.URL.EscapedPath()+"?"+request.URL.RawQuery)
		switch request.URL.Path {
		case "/api/v1/jobs/job-test/logs":
			writeClientSuccess(t, writer, http.StatusOK, map[string]any{"items": []any{}})
		case "/api/v1/jobs/job-test/cancel":
			writeClientSuccess(t, writer, http.StatusAccepted, map[string]any{"id": "job-test"})
		default:
			t.Fatalf("unexpected path=%s", request.URL.Path)
		}
	}))
	defer server.Close()
	client, err := NewClient(ClientOptions{ServerURL: server.URL, Token: "test-token", HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Logs(context.Background(), "job-test", 25); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Cancel(context.Background(), "job-test"); err != nil {
		t.Fatal(err)
	}
	want := []string{"GET /api/v1/jobs/job-test/logs?limit=25", "POST /api/v1/jobs/job-test/cancel?"}
	if !reflect.DeepEqual(requests, want) {
		t.Fatalf("requests=%v want=%v", requests, want)
	}
}

func testJobSpec() domain.JobSpec {
	return domain.JobSpec{
		Name: "rayctl-test", Image: "registry.example/ray@sha256:abcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcd",
		Entrypoint: domain.Entrypoint{Command: []string{"python", "train.py"}},
		Resources:  domain.Resources{WorkerReplicas: 1, GPUsPerWorker: 1, CPUPerWorker: 8, MemoryPerWorker: "32Gi"}, Queue: "tenant-a-gpu",
	}
}

func serverURL(request *http.Request) string {
	if request.TLS != nil {
		return "https://" + request.Host
	}
	return "http://" + request.Host
}

func writeTestCA(t *testing.T, server *httptest.Server) string {
	t.Helper()
	certificate := server.Certificate()
	if certificate == nil {
		t.Fatal("test TLS certificate is unavailable")
	}
	path := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate.Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeClientSuccess(t *testing.T, writer http.ResponseWriter, status int, data any) {
	t.Helper()
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	if err := json.NewEncoder(writer).Encode(map[string]any{"success": true, "data": data, "request_id": "test"}); err != nil {
		t.Fatal(err)
	}
}

func writeClientFailure(t *testing.T, writer http.ResponseWriter, status int, code string) {
	t.Helper()
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	if err := json.NewEncoder(writer).Encode(map[string]any{"success": false, "error": map[string]string{"code": code, "message": "request rejected"}, "request_id": "test"}); err != nil {
		t.Fatal(err)
	}
}
