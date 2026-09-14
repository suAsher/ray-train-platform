package functionwarehouse

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

func createFixture() CreateVersionRequest {
	return CreateVersionRequest{
		FunctionWarehouseID: "warehouse-1", ModelTypeID: "model-1", Version: "v1",
		JobID: "job-1", RunID: "run-1", ExperimentID: "0",
		Paths: []UploadedFile{{URL: "https://storage.example/object", Filename: "weights.pth", FileSHA256: strings.Repeat("a", 64), FileSize: 5}},
	}
}

func creationPreflight(w http.ResponseWriter, r *http.Request) bool {
	switch r.URL.Path {
	case "/model/functionWarehouse":
		writeEnvelope(w, warehouseFixture())
		return true
	case "/model/model/type/list":
		writeEnvelope(w, []ModelType{{ID: "model-1", Name: "Model"}})
		return true
	}
	return false
}

func versionWire(req CreateVersionRequest) map[string]any {
	paths, _ := json.Marshal(req.Paths)
	return map[string]any{
		"id": "version-1", "functionWarehouseId": req.FunctionWarehouseID,
		"groupId": warehouseFixture().GroupID, "modelTypeId": req.ModelTypeID,
		"version": req.Version, "jobId": req.JobID, "runId": req.RunID,
		"experimentId": req.ExperimentID, "paths": string(paths), "production": false,
	}
}

func TestCreateVersionUsesConfirmedSourceAndAllowsNewDuplicates(t *testing.T) {
	var writes atomic.Int32
	req := createFixture()
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if creationPreflight(w, r) {
			return
		}
		if r.URL.Path != "/model/model/version/create" || r.Method != "POST" || r.Header.Get("Authorization") != "Bearer token" {
			t.Error("unexpected mutation")
		}
		var body map[string]any
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			t.Fatal("invalid create body")
		}
		if body["groupId"] != warehouseFixture().GroupID || body["production"] != false || body["recommended"] != false || body["jobId"] != req.JobID || body["runId"] != req.RunID || body["experimentId"] != req.ExperimentID {
			t.Errorf("incorrect source or defaults: %v", body)
		}
		if _, ok := body["mlflowRunId"]; ok {
			t.Error("overwrote report source")
		}
		writes.Add(1)
		writeEnvelope(w, versionWire(req))
	})
	for i := 0; i < 2; i++ {
		result, err := client.CreateVersion(context.Background(), "token", req)
		if err != nil || result.ID != "version-1" || result.RunID != req.RunID {
			t.Fatalf("create=%#v error=%v", result, err)
		}
	}
	if writes.Load() != 2 {
		t.Fatal("separate intentional creates were deduplicated")
	}
}

func TestCreateVersionUncertainResultNeverRetries(t *testing.T) {
	for _, mode := range []string{"500", "generic-error", "redirect", "malformed", "source-mismatch", "file-mismatch"} {
		t.Run(mode, func(t *testing.T) {
			var writes atomic.Int32
			client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				if creationPreflight(w, r) {
					return
				}
				writes.Add(1)
				switch mode {
				case "500":
					w.WriteHeader(500)
				case "generic-error":
					_, _ = w.Write([]byte(`{"code":-1,"data":null,"msg":"internal post-create error"}`))
				case "redirect":
					http.Redirect(w, r, "/other", 302)
				case "malformed":
					_, _ = w.Write([]byte("secret-token"))
				default:
					body := versionWire(createFixture())
					if mode == "source-mismatch" {
						body["runId"] = "other"
					} else {
						body["paths"] = "[]"
					}
					writeEnvelope(w, body)
				}
			})
			_, err := client.CreateVersion(context.Background(), "secret-token", createFixture())
			if !errors.Is(err, ErrUnknownOutcome) || writes.Load() != 1 || strings.Contains(err.Error(), "secret-token") {
				t.Fatalf("unsafe outcome: %v writes=%d", err, writes.Load())
			}
		})
	}
}

func TestCreateVersionRejectsReadOnlyPermissionAndForeignModel(t *testing.T) {
	for _, mode := range []string{"read-only", "foreign-model"} {
		client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/model/functionWarehouse":
				row := warehouseFixture()
				if mode == "read-only" {
					row.PermissionCodes = []string{"view", "not-admin"}
				}
				writeEnvelope(w, row)
			case "/model/model/type/list":
				writeEnvelope(w, []ModelType{{ID: "other"}})
			default:
				t.Error("unauthorized mutation reached upstream")
			}
		})
		if _, err := client.CreateVersion(context.Background(), "token", createFixture()); !errors.Is(err, ErrForbidden) {
			t.Errorf("authorization accepted: %v", err)
		}
	}
}

func TestVerifyVersionFindsSourceAndFileOnBoundedPage(t *testing.T) {
	req := createFixture()
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if creationPreflight(w, r) {
			return
		}
		body := versionWire(req)
		body["paths"] = req.Paths
		writeEnvelope(w, map[string]any{"records": []any{body}, "total": 1, "current": 1, "size": 100})
	})
	result, err := client.VerifyVersion(context.Background(), "token", req.FunctionWarehouseID, "version-1", req)
	if err != nil || result.ID != "version-1" {
		t.Fatalf("verify=%#v error=%v", result, err)
	}
	wrong := req
	wrong.ExperimentID = "another"
	if _, err := client.VerifyVersion(context.Background(), "token", req.FunctionWarehouseID, "version-1", wrong); !errors.Is(err, ErrUnknownOutcome) {
		t.Fatalf("mismatch accepted: %v", err)
	}
}

func TestLiveCreateResponseRelativeFilePaths(t *testing.T) {
	req := CreateVersionRequest{
		FunctionWarehouseID: "35b9cdf6ebd2d8afd95093174ea16d5b", ModelTypeID: "a55649782265452e6396af87641819db", Version: "contract-20260914",
		JobID: "job-contract-20260914", RunID: "11111111111111111111111111111111", ExperimentID: "0", Paths: []UploadedFile{liveUploadedFixture()},
	}
	client, err := NewClient(Development)
	if err != nil {
		t.Fatal(err)
	}
	for _, arrayPaths := range []bool{false, true} {
		body := versionWire(req)
		body["id"] = "98532580dd0ef51beac959b3b8cfa34f"
		if arrayPaths {
			body["paths"] = req.Paths
		}
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		var actual Version
		if err := json.Unmarshal(encoded, &actual); err != nil {
			t.Fatal(err)
		}
		if !client.matchesVersion(actual, req) {
			t.Fatalf("live %t paths response mismatched: %#v", arrayPaths, actual)
		}
		if actual.Files[0] != liveUploadedFixture() {
			t.Fatal("live URL changed during version decoding")
		}
	}
}

func TestVersionVerificationAllowsWarehouseManagedFileRelocation(t *testing.T) {
	client, err := NewClient(Development)
	if err != nil {
		t.Fatal(err)
	}
	req := createFixture()
	file := liveUploadedFixture()
	req.Paths = []UploadedFile{file}
	for _, field := range []string{"filePath", "url", "filename", "size", "sha256"} {
		t.Run(field, func(t *testing.T) {
			moved := file
			moved.FilePath = "/.wellspiking/team/model-function-warehouse/model/DEFAULT/weights.safetensors"
			switch field {
			case "url":
				moved.URL += "-different"
			case "filename":
				moved.Filename = "different.safetensors"
			case "size":
				moved.FileSize++
			case "sha256":
				moved.FileSHA256 = strings.Repeat("0", 64)
			}
			body := versionWire(req)
			body["paths"] = []UploadedFile{moved}
			encoded, _ := json.Marshal(body)
			var actual Version
			if err := json.Unmarshal(encoded, &actual); err != nil {
				t.Fatal(err)
			}
			if matched := client.matchesVersion(actual, req); matched != (field == "filePath") {
				t.Fatalf("managed relocation / immutable %s boundary: matched=%t", field, matched)
			}
		})
	}
}
