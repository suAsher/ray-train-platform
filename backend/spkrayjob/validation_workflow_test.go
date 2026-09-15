package spkrayjob

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"ray-train-platform-backend/domain"
)

func TestSubmitKnownInvalidSpecFailsBeforeClientConfiguration(t *testing.T) {
	tests := []struct {
		name      string
		project   string
		wantError string
	}{
		{
			name: "DNS name",
			project: `name: Not_A_DNS_Name
image: harbor.example/train@sha256:` + strings.Repeat("a", 64) + `
entrypoint: python train.py
`,
			wantError: "name must be a lowercase DNS label",
		},
		{
			name: "worker resources",
			project: `name: invalid-workers
image: harbor.example/train@sha256:` + strings.Repeat("a", 64) + `
entrypoint: python train.py
workers: -1
gpusPerWorker: 1
`,
			wantError: "ray_train requires at least 2 workers",
		},
		{
			name: "memory resource quantity",
			project: `name: invalid-memory
image: harbor.example/train@sha256:` + strings.Repeat("a", 64) + `
entrypoint: python train.py
memoryPerWorker: definitely-not-memory
`,
			wantError: "memoryPerWorker must be a positive Kubernetes quantity",
		},
		{
			name: "supplied image",
			project: `name: invalid-image
image: harbor.example/train
entrypoint: python train.py
`,
			wantError: "image must include an explicit tag or sha256 digest",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := seedProject(t, test.project)
			getenvRead := false
			getenv := func(string) string {
				getenvRead = true
				return ""
			}

			err := Run(context.Background(), []string{"submit", "--dir", root},
				&bytes.Buffer{}, &bytes.Buffer{}, getenv)
			if getenvRead {
				t.Fatal("known-invalid job spec must fail before reading credentials or connection settings")
			}
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("expected local validation error containing %q, got %v", test.wantError, err)
			}
		})
	}
}

func TestSubmitDefersClusterCapacityToServer(t *testing.T) {
	tests := []struct {
		name    string
		workers int
		gpus    int
		reject  bool
	}{
		{name: "four workers with eight GPUs", workers: 4, gpus: 8},
		{name: "larger GPU node", workers: 2, gpus: 16},
		{name: "server quota rejection", workers: 4, gpus: 8, reject: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := seedProject(t, "name: expanded-cluster\nimage: harbor.example/train@sha256:"+strings.Repeat("a", 64)+"\nentrypoint: python train.py\n")
			var submitted domain.JobSpec
			createReached := false
			stub := artifactStubHandler(t, func(spec domain.JobSpec) { submitted = spec })
			server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if request.Method == http.MethodPost && request.URL.Path == "/api/v1/jobs" {
					createReached = true
					if test.reject {
						writeClientFailure(t, writer, http.StatusConflict, "GPU_QUOTA_EXCEEDED")
						return
					}
				}
				stub(writer, request)
			}))
			defer server.Close()
			err := Run(context.Background(), []string{
				"submit", "--server", server.URL, "--ca-file", writeTestCA(t, server), "--dir", root,
				"--engine", "ray-ddp", "--execution-mode", "ray_train",
				"--workers", strconv.Itoa(test.workers), "--gpus-per-worker", strconv.Itoa(test.gpus),
			}, &bytes.Buffer{}, &bytes.Buffer{}, testEnvironment)
			if !createReached {
				t.Fatalf("valid expanded-cluster shape never reached create-job API: %v", err)
			}
			if test.reject {
				if err == nil || !strings.Contains(err.Error(), "GPU_QUOTA_EXCEEDED") {
					t.Fatalf("server quota rejection must reach caller unchanged: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("expanded-cluster submit failed: %v", err)
			}
			if submitted.Resources.WorkerReplicas != test.workers || submitted.Resources.GPUsPerWorker != test.gpus || submitted.Execution.Mode != domain.ExecutionModeRayTrain {
				t.Fatalf("submission flags were changed: %+v", submitted)
			}
		})
	}
}

func TestLocalValidationGatesPreserveShapeAndResourceChecks(t *testing.T) {
	valid := domain.JobSpec{
		Name: "expanded-cluster", Image: localValidationImage,
		Source:     domain.CodeSource{Type: "workspace-archive", ArtifactID: "test-artifact"},
		Entrypoint: domain.Entrypoint{Command: []string{"python", "train.py"}},
		Execution:  domain.ExecutionProfile{Mode: domain.ExecutionModeRayTrain},
		Resources:  domain.Resources{WorkerReplicas: 4, GPUsPerWorker: 8, CPUPerWorker: 4, MemoryPerWorker: "16Gi"},
	}
	for name, validate := range map[string]func(domain.JobSpec) error{
		"preflight": validatePreflightJobSpec,
		"archive":   validateArchiveJobSpec,
		"final":     validateFinalJobSpec,
	} {
		t.Run(name, func(t *testing.T) {
			if err := validate(valid); err != nil {
				t.Fatalf("valid four-worker request must pass every local gate: %v", err)
			}
			for _, resources := range []domain.Resources{
				{WorkerReplicas: 0, GPUsPerWorker: 8, CPUPerWorker: 4, MemoryPerWorker: "16Gi"},
				{WorkerReplicas: -1, GPUsPerWorker: 8, CPUPerWorker: 4, MemoryPerWorker: "16Gi"},
				{WorkerReplicas: 4, GPUsPerWorker: 0, CPUPerWorker: 4, MemoryPerWorker: "16Gi"},
				{WorkerReplicas: 4, GPUsPerWorker: -1, CPUPerWorker: 4, MemoryPerWorker: "16Gi"},
				{WorkerReplicas: int(^uint(0) >> 1), GPUsPerWorker: 2, CPUPerWorker: 4, MemoryPerWorker: "16Gi"},
				{WorkerReplicas: 4, GPUsPerWorker: 8, CPUPerWorker: 0, MemoryPerWorker: "16Gi"},
				{WorkerReplicas: 4, GPUsPerWorker: 8, CPUPerWorker: 4, MemoryPerWorker: "invalid"},
			} {
				invalid := valid
				invalid.Resources = resources
				if err := validate(invalid); err == nil {
					t.Fatalf("local gate accepted invalid resources: %+v", resources)
				}
			}
		})
	}
}

func TestSubmitWithDerivedNameAndPlatformDefaultImageStillNeedsNoJobOptions(t *testing.T) {
	root := seedProject(t, "entrypoint: python train.py\n")
	var submitted domain.JobSpec
	stub := artifactStubHandler(t, func(spec domain.JobSpec) { submitted = spec })
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/api/v1/images" {
			writeClientSuccess(t, writer, http.StatusOK, []map[string]any{{
				"name": "default", "reference": "harbor.example/train@sha256:" + strings.Repeat("b", 64), "isDefault": true,
				"rayVersion": domain.RayVersionLegacy, "supportedEngines": []string{"ray-ddp"},
			}})
			return
		}
		stub(writer, request)
	}))
	defer server.Close()

	err := Run(context.Background(), []string{
		"submit", "--server", server.URL, "--ca-file", writeTestCA(t, server), "--dir", root,
	}, &bytes.Buffer{}, &bytes.Buffer{}, testEnvironment)
	if err != nil {
		t.Fatalf("zero-option submit with derived defaults failed: %v", err)
	}
	if submitted.Name == "" || submitted.Image != "harbor.example/train@sha256:"+strings.Repeat("b", 64) {
		t.Fatalf("expected derived name and platform-default image, got %+v", submitted)
	}
}

func TestSubmitAcceptsExplicitTaggedPlatformImage(t *testing.T) {
	root := seedProject(t, "entrypoint: python train.py\n")
	var submitted domain.JobSpec
	stub := artifactStubHandler(t, func(spec domain.JobSpec) { submitted = spec })
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/api/v1/images" {
			writeClientSuccess(t, writer, http.StatusOK, []map[string]any{{
				"name": "tagged-default", "reference": "harbor.example/train:production", "isDefault": true,
				"rayVersion": domain.RayVersionLegacy, "supportedEngines": []string{"ray-ddp"},
			}})
			return
		}
		stub(writer, request)
	}))
	defer server.Close()

	err := Run(context.Background(), []string{
		"submit", "--server", server.URL, "--ca-file", writeTestCA(t, server), "--dir", root,
	}, &bytes.Buffer{}, &bytes.Buffer{}, testEnvironment)
	if err != nil {
		t.Fatalf("submit with catalogued tagged image failed: %v", err)
	}
	if submitted.Image != "harbor.example/train:production" {
		t.Fatalf("expected tagged platform image, got %q", submitted.Image)
	}
}

func TestSubmitValidatesResolvedPlatformImageBeforeArtifactCreation(t *testing.T) {
	root := seedProject(t, "entrypoint: python train.py\n")
	artifactCreated := false
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v1/images":
			writeClientSuccess(t, writer, http.StatusOK, []map[string]any{{
				"name": "invalid-default", "reference": "harbor.example/train", "isDefault": true,
			}})
		case "/api/v1/source-artifacts":
			artifactCreated = true
			t.Fatal("resolved job spec must be validated before artifact creation")
		default:
			t.Fatalf("unexpected request %s", request.URL.Path)
		}
	}))
	defer server.Close()

	err := Run(context.Background(), []string{
		"submit", "--server", server.URL, "--ca-file", writeTestCA(t, server), "--dir", root,
	}, &bytes.Buffer{}, &bytes.Buffer{}, testEnvironment)
	if artifactCreated {
		t.Fatal("invalid resolved image must fail before artifact creation")
	}
	if err == nil || !strings.Contains(err.Error(), "image must include an explicit tag or sha256 digest") {
		t.Fatalf("expected final local domain validation error, got %v", err)
	}
}
