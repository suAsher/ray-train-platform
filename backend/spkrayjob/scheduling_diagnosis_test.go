package spkrayjob

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDiagnoseRendersSharedSchedulingCapacity(t *testing.T) {
	for _, unavailable := range []bool{false, true} {
		for _, output := range []string{"text", "json"} {
			calls := 0
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.URL.Path != "/api/v1/jobs/job-1/diagnosis" { t.Fatalf("unexpected request: %s", r.URL.Path) }
				writeClientSuccess(t, w, http.StatusOK, map[string]any{
					"jobId": "job-1", "observedState": "QUEUED", "summary": "日志不可用",
					"schedulingCapacity": map[string]any{
						"available": !unavailable, "observedAt": "2026-10-13T01:00:00Z", "unavailableReason": "capacity lookup temporarily unavailable",
						"quota": map[string]any{"gpuAvailable": 24},
						"pool": map[string]any{"totalGpus": 72, "allocatedGpus": 64, "reservedGpus": 4, "freeGpus": 4},
						"fit": map[string]any{"workerReplicas": 2, "gpusPerWorker": 4, "cpuPerWorker": 16, "memoryPerWorker": "64Gi", "fittingNodes": 1, "workerSlots": 1, "canFit": false, "reason": "INSUFFICIENT_MATCHING_NODES"},
					},
				})
			}))
			var stdout bytes.Buffer
			err := Run(context.Background(), []string{"diagnose", "--server", server.URL, "--ca-file", writeTestCA(t, server), "--output", output, "job-1"}, &stdout, &bytes.Buffer{}, testEnvironment)
			server.Close()
			if err != nil { t.Fatal(err) }
			if calls != 1 { t.Fatalf("capacity must come from shared diagnosis, calls=%d", calls) }
			if output == "json" {
				var payload map[string]json.RawMessage
				if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil { t.Fatal(err) }
				if len(payload["schedulingCapacity"]) == 0 { t.Fatal("capacity lost in JSON") }
				continue
			}
			markers := []string{"capacity lookup temporarily unavailable"}
			if !unavailable { markers = []string{"24", "72", "64", "64Gi", "INSUFFICIENT_MATCHING_NODES", "2026-10-13T01:00:00Z"} }
			for _, marker := range markers {
				if !strings.Contains(stdout.String(), marker) { t.Fatalf("text lacks %q: %s", marker, stdout.String()) }
			}
		}
	}
}
