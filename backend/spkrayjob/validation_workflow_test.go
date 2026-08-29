package spkrayjob

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
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
workers: 999
gpusPerWorker: 1
`,
			wantError: "workerReplicas must be between",
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
