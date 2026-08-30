package spkrayjob

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ray-train-platform-backend/domain"
)

func TestDatasetsCommandListsOnlyLogicalCatalogFields(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != "/api/v1/datasets" {
			t.Fatalf("unexpected dataset request %s %s", request.Method, request.URL.Path)
		}
		writeClientSuccess(t, writer, http.StatusOK, []map[string]any{{
			"id": "dataset-labeled-full", "slug": "labeled-full", "name": "S1H labeled full",
			"visibility": "PUBLIC", "schemaVersion": "s1h-v1", "sourceSpace": "public",
			"sourceRelativePath": "labeled",
		}})
	}))
	defer server.Close()

	var output bytes.Buffer
	err := Run(context.Background(), []string{
		"datasets", "--server", server.URL, "--ca-file", writeTestCA(t, server),
	}, &output, &bytes.Buffer{}, testEnvironment)
	if err != nil {
		t.Fatalf("list datasets: %v", err)
	}
	text := output.String()
	for _, expected := range []string{"labeled-full", "S1H labeled full", "PUBLIC", "s1h-v1"} {
		if !strings.Contains(text, expected) {
			t.Fatalf("dataset table omitted %q:\n%s", expected, text)
		}
	}
	for _, internal := range []string{"tos://", domain.DefaultDatasetInternalPrefix, "manifestObjectKey"} {
		if strings.Contains(text, internal) {
			t.Fatalf("dataset table leaked internal value %q:\n%s", internal, text)
		}
	}
}

func TestDatasetVersionsCommandResolvesSlugAndRendersImmutableVersions(t *testing.T) {
	requests := make([]string, 0, 2)
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests = append(requests, request.URL.EscapedPath())
		switch request.URL.Path {
		case "/api/v1/datasets":
			writeClientSuccess(t, writer, http.StatusOK, []map[string]any{{
				"id": "dataset-labeled-full", "slug": "labeled-full", "name": "S1H labeled full",
				"visibility": "PUBLIC", "schemaVersion": "s1h-v1", "sourceSpace": "public",
				"sourceRelativePath": "labeled",
			}})
		case "/api/v1/datasets/dataset-labeled-full/versions":
			writeClientSuccess(t, writer, http.StatusOK, []map[string]any{{
				"id": "version-20260830", "datasetId": "dataset-labeled-full", "version": "2026.08.30",
				"state": "READY", "manifestSha256": strings.Repeat("a", 64), "schemaVersion": "s1h-v1",
				"trainSamples": 15228, "valSamples": 1620, "testSamples": 0,
				"sourceObjectCount": 91216, "logicalBytes": int64(10) << 40, "packedBytes": int64(8) << 40,
			}})
		default:
			t.Fatalf("unexpected dataset version request %s", request.URL.Path)
		}
	}))
	defer server.Close()

	var output bytes.Buffer
	err := Run(context.Background(), []string{
		"dataset", "versions", "--server", server.URL, "--ca-file", writeTestCA(t, server), "labeled-full",
	}, &output, &bytes.Buffer{}, testEnvironment)
	if err != nil {
		t.Fatalf("list dataset versions: %v", err)
	}
	text := output.String()
	for _, expected := range []string{"version-20260830", "2026.08.30", "READY", "15228", "8.0 TiB"} {
		if !strings.Contains(text, expected) {
			t.Fatalf("version table omitted %q:\n%s", expected, text)
		}
	}
	if strings.Contains(text, domain.DefaultDatasetInternalPrefix) || len(requests) != 2 {
		t.Fatalf("unsafe or incomplete version lookup: requests=%v output=%s", requests, text)
	}
}

func TestStreamingSubmitPreflightPinsLatestBeforeUploadingSource(t *testing.T) {
	root := seedProject(t, `name: s1h-streaming-preflight
entrypoint: python train.py
engine: ray-train
dataMode: streaming
datasetRef:
  dataset: labeled-full
  version: latest
cachePolicy: bounded
workers: 2
gpusPerWorker: 8
`)
	canaryImage := "harbor.example/streaming@sha256:" + strings.Repeat("c", 64)
	requestOrder := make([]string, 0, 6)
	var submitted domain.JobSpec
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requestOrder = append(requestOrder, request.URL.Path)
		switch request.URL.Path {
		case "/api/v1/limits":
			writeClientSuccess(t, writer, http.StatusOK, map[string]any{"runtime": map[string]any{
				"availableEngines": []string{"ray-ddp", "ray-train"}, "managedEnabled": true, "canaryEnabled": true,
				"productionRayVersion": domain.RayVersionProduction, "canaryRayVersion": domain.RayVersionCanary,
			}})
		case "/api/v1/images":
			writeClientSuccess(t, writer, http.StatusOK, []map[string]any{{
				"name": "Streaming", "reference": canaryImage, "rayVersion": domain.RayVersionCanary,
				"supportedEngines": []string{"ray-train"},
			}})
		case "/api/v1/jobs/preflight":
			var body struct {
				Spec domain.JobSpec `json:"spec"`
			}
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body.Spec.DatasetRef.Version != "latest" {
				t.Fatalf("preflight must receive the user's latest selector, got %+v", body.Spec.DatasetRef)
			}
			writeClientSuccess(t, writer, http.StatusOK, map[string]any{
				"image": canaryImage, "trainingEngine": "ray-train", "rayVersion": domain.RayVersionCanary, "requestedGpus": 16,
				"dataset": map[string]any{
					"datasetId": "dataset-labeled-full", "datasetSlug": "labeled-full", "versionId": "version-20260830",
					"version": "2026.08.30", "manifestSha256": strings.Repeat("a", 64), "schemaVersion": "s1h-v1",
					"trainSamples": 15228, "valSamples": 1620, "testSamples": 0,
					"logicalBytes": int64(10) << 40, "packedBytes": int64(8) << 40,
					"dataMode": "streaming", "cachePolicy": "bounded",
				},
			})
		case "/api/v1/source-artifacts":
			writeClientSuccess(t, writer, http.StatusOK, map[string]any{
				"artifactId": "artifact-streaming", "state": "READY", "uploadRequired": false,
			})
		case "/api/v1/jobs":
			var body struct {
				Spec domain.JobSpec `json:"spec"`
			}
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			submitted = body.Spec
			writeClientSuccess(t, writer, http.StatusAccepted, map[string]any{"id": "job-streaming"})
		default:
			t.Fatalf("unexpected request %s", request.URL.Path)
		}
	}))
	defer server.Close()

	var output bytes.Buffer
	err := Run(context.Background(), []string{
		"submit", "--server", server.URL, "--ca-file", writeTestCA(t, server), "--dir", root,
	}, &output, &bytes.Buffer{}, testEnvironment)
	if err != nil {
		t.Fatalf("submit streaming job: %v", err)
	}
	if submitted.DatasetRef != (domain.DatasetReference{Dataset: "dataset-labeled-full", Version: "version-20260830"}) {
		t.Fatalf("latest was not pinned before submission: %+v", submitted.DatasetRef)
	}
	if len(requestOrder) < 4 || requestOrder[2] != "/api/v1/jobs/preflight" || requestOrder[3] != "/api/v1/source-artifacts" {
		t.Fatalf("preflight must precede source upload: %v", requestOrder)
	}
	for _, expected := range []string{"预检通过", "version-20260830", "15228", "16 GPU", "bounded"} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("preflight output omitted %q:\n%s", expected, output.String())
		}
	}
}
