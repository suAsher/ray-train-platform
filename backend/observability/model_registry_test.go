package observability

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	ml "ray-train-platform-backend/modellifecycle"
	"ray-train-platform-backend/modelregistry"
)

type registryFixture struct {
	t                             *testing.T
	client                        *MLflowClient
	model                         ml.Model
	version                       ml.Version
	data                          []byte
	experiment                    bool
	run                           map[string]any
	registered                    map[string]any
	versions                      []map[string]any
	artifact                      []byte
	creates, uploads              int
	ambiguous, corrupt, duplicate bool
	unsafeURI                     string
}

func newRegistryFixture(t *testing.T) *registryFixture {
	t.Helper()
	data := []byte("checkpoint bytes for registry protocol")
	sum := sha256.Sum256(data)
	f := &registryFixture{t: t, data: data,
		model:   ml.Model{ID: "6849e5c1-b33b-438f-99f9-831bd568ce39", Name: "shared model"},
		version: ml.Version{ID: "2cb1bbef-48ee-48a0-88f4-a1ba6bd5a784", ModelID: "6849e5c1-b33b-438f-99f9-831bd568ce39", State: ml.Ready, FileName: "weights.safetensors", SizeBytes: int64(len(data)), SHA256: hex.EncodeToString(sum[:])},
	}
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	f.client = &MLflowClient{BaseURL: srv.URL + "/mlflow", ProvenanceKey: bytes.Repeat([]byte("k"), 32), HTTPClient: srv.Client()}
	return f
}

func (f *registryFixture) open(context.Context) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(f.data)), nil
}
func (f *registryFixture) serve(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if r.Method == http.MethodPost {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}
	path := strings.TrimPrefix(r.URL.Path, "/mlflow")
	var reply any = map[string]any{}
	switch path {
	case "/api/2.0/mlflow/experiments/get-by-name":
		if !f.experiment {
			w.WriteHeader(404)
			return
		}
		reply = map[string]any{"experiment": map[string]any{"experiment_id": "7"}}
	case "/api/2.0/mlflow/experiments/create":
		f.experiment = true
		reply = map[string]any{"experiment_id": "7"}
	case "/api/2.0/mlflow/runs/search":
		runs := []map[string]any{}
		if f.run != nil {
			runs = append(runs, f.run)
		}
		reply = map[string]any{"runs": runs}
	case "/api/2.0/mlflow/runs/create":
		uri := "mlflow-artifacts:/7/0123456789abcdef0123456789abcdef/artifacts"
		if f.unsafeURI != "" {
			uri = f.unsafeURI
		}
		f.run = map[string]any{"info": map[string]any{"run_id": "0123456789abcdef0123456789abcdef", "experiment_id": "7", "status": "RUNNING", "artifact_uri": uri}, "data": map[string]any{"tags": body["tags"]}}
		reply = map[string]any{"run": f.run}
	case "/api/2.0/mlflow/runs/get":
		reply = map[string]any{"run": f.run}
	case "/api/2.0/mlflow/runs/update":
		f.run["info"].(map[string]any)["status"] = body["status"]
	case "/api/2.0/mlflow/registered-models/get":
		if f.registered == nil {
			w.WriteHeader(404)
			return
		}
		reply = map[string]any{"registered_model": f.registered}
	case "/api/2.0/mlflow/registered-models/create":
		f.registered = body
		reply = map[string]any{"registered_model": body}
	case "/api/2.0/mlflow/model-versions/search":
		items := append([]map[string]any{}, f.versions...)
		if f.duplicate && len(items) > 0 {
			items = append(items, items[0])
		}
		reply = map[string]any{"model_versions": items}
	case "/api/2.0/mlflow/model-versions/create":
		f.creates++
		body["version"] = "1"
		body["status"] = "READY"
		f.versions = append(f.versions, body)
		if f.ambiguous {
			w.WriteHeader(503)
			return
		}
		reply = map[string]any{"model_version": body}
	default:
		if !strings.HasPrefix(path, "/api/2.0/mlflow-artifacts/artifacts/7/0123456789abcdef0123456789abcdef/artifacts/checkpoint/") {
			f.t.Errorf("unexpected %s %s", r.Method, path)
			w.WriteHeader(500)
			return
		}
		if r.Method == http.MethodPut {
			f.uploads++
			f.artifact, _ = io.ReadAll(r.Body)
			w.WriteHeader(200)
			return
		}
		if r.Method == http.MethodGet {
			if f.artifact == nil {
				w.WriteHeader(404)
				return
			}
			data := f.artifact
			if f.corrupt {
				data = []byte("corrupt")
			}
			_, _ = w.Write(data)
			return
		}
		f.t.Errorf("unexpected artifact method %s", r.Method)
		w.WriteHeader(500)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(reply)
}

func TestRegistryCopiesVerifiesAndReconcilesCheckpoint(t *testing.T) {
	f := newRegistryFixture(t)
	link, err := f.client.EnsureVersion(context.Background(), f.model, f.version, f.open)
	if err != nil {
		t.Fatal(err)
	}
	if link.RegisteredName != "raytrain-model-"+f.model.ID || link.Version != "1" || !strings.HasPrefix(link.SourceURI, "mlflow-artifacts:/7/") {
		t.Fatalf("bad link %#v", link)
	}
	if !bytes.Equal(f.data, f.artifact) || f.creates != 1 || f.uploads != 1 {
		t.Fatal("checkpoint was not copied exactly once")
	}
	if strings.Contains(string(f.artifact), "MLmodel") {
		t.Fatal("checkpoint incorrectly repackaged as model flavor")
	}
	again, err := f.client.EnsureVersion(context.Background(), f.model, f.version, f.open)
	if err != nil || again != link || f.creates != 1 || f.uploads != 1 {
		t.Fatalf("retry duplicated operation: %#v %v", again, err)
	}
}

func TestRegistryRecoversAmbiguousCreate(t *testing.T) {
	f := newRegistryFixture(t)
	f.ambiguous = true
	if _, err := f.client.EnsureVersion(context.Background(), f.model, f.version, f.open); err != nil {
		t.Fatal(err)
	}
	if f.creates != 1 {
		t.Fatal("duplicate create")
	}
}

func TestRegistryRejectsCorruptionBeforeRegistration(t *testing.T) {
	f := newRegistryFixture(t)
	f.corrupt = true
	_, err := f.client.EnsureVersion(context.Background(), f.model, f.version, f.open)
	if !errors.Is(err, modelregistry.ErrIntegrity) || f.creates != 0 {
		t.Fatalf("corruption accepted: %v", err)
	}
}

func TestRegistryDetectsDriftWithoutReplacingRegisteredArtifact(t *testing.T) {
	f := newRegistryFixture(t)
	if _, err := f.client.EnsureVersion(context.Background(), f.model, f.version, f.open); err != nil {
		t.Fatal(err)
	}
	f.corrupt = true
	if _, err := f.client.EnsureVersion(context.Background(), f.model, f.version, f.open); !errors.Is(err, modelregistry.ErrIntegrity) || f.uploads != 1 {
		t.Fatalf("registered object drift was ignored or overwritten: %v", err)
	}
}

func TestRegistryRejectsMissingTagsWithoutCreatingDuplicate(t *testing.T) {
	f := newRegistryFixture(t)
	if _, err := f.client.EnsureVersion(context.Background(), f.model, f.version, f.open); err != nil {
		t.Fatal(err)
	}
	f.versions[0]["tags"] = []MLflowKeyValue{}
	if _, err := f.client.EnsureVersion(context.Background(), f.model, f.version, f.open); !errors.Is(err, modelregistry.ErrConflict) || f.creates != 1 {
		t.Fatalf("stripped provenance was accepted: %v", err)
	}
}

func TestRegistryRejectsWrongSnapshotAndInvalidIdentity(t *testing.T) {
	for _, change := range []func(*registryFixture){
		func(f *registryFixture) { f.version.ModelID = "other" },
		func(f *registryFixture) { f.version.SHA256 = "../" },
		func(f *registryFixture) { f.version.State = ml.Copying },
		func(f *registryFixture) { f.model.ID = "name' OR 1=1" },
	} {
		f := newRegistryFixture(t)
		change(f)
		_, err := f.client.EnsureVersion(context.Background(), f.model, f.version, f.open)
		if !errors.Is(err, modelregistry.ErrInvalid) || f.experiment {
			t.Fatalf("invalid request made upstream call: %v", err)
		}
	}
	f := newRegistryFixture(t)
	f.data = []byte("wrong snapshot")
	if _, err := f.client.EnsureVersion(context.Background(), f.model, f.version, f.open); err == nil || f.creates != 0 {
		t.Fatal("wrong source registered")
	}
}

func TestRegistryRejectsUnsafeArtifactLocations(t *testing.T) {
	for _, uri := range []string{"https://example.invalid/steal", "mlflow-artifacts://example.invalid/path", "mlflow-artifacts:/7/0123456789abcdef0123456789abcdef/artifacts/../other", "mlflow-artifacts:/8/0123456789abcdef0123456789abcdef/artifacts"} {
		f := newRegistryFixture(t)
		f.unsafeURI = uri
		if _, err := f.client.EnsureVersion(context.Background(), f.model, f.version, f.open); err == nil || f.uploads != 0 {
			t.Fatalf("unsafe uri accepted %s", uri)
		}
	}
}

func TestRegistryRejectsDuplicateAndForgedVersionBinding(t *testing.T) {
	f := newRegistryFixture(t)
	if _, err := f.client.EnsureVersion(context.Background(), f.model, f.version, f.open); err != nil {
		t.Fatal(err)
	}
	f.duplicate = true
	if _, err := f.client.EnsureVersion(context.Background(), f.model, f.version, f.open); !errors.Is(err, modelregistry.ErrConflict) {
		t.Fatalf("duplicate accepted: %v", err)
	}
	f.duplicate = false
	f.versions[0]["source"] = "https://example.invalid/changed"
	if _, err := f.client.EnsureVersion(context.Background(), f.model, f.version, f.open); !errors.Is(err, modelregistry.ErrConflict) {
		t.Fatalf("forged association accepted: %v", err)
	}
}
