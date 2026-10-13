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

func TestDiagnoseRendersSharedSchedulingCapacity(t *testing.T) {
	for _, unavailable := range []bool{false, true} {
		for _, output := range []string{"text", "json"} {
			calls := 0
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.URL.Path != "/api/v1/jobs/job-1/diagnosis" {
					t.Fatalf("unexpected request: %s", r.URL.Path)
				}
				writeClientSuccess(t, w, http.StatusOK, map[string]any{
					"jobId": "job-1", "observedState": "QUEUED", "summary": "日志不可用",
					"schedulingCapacity": map[string]any{
						"available": !unavailable, "observedAt": "2026-10-13T01:00:00Z", "unavailableReason": "capacity lookup temporarily unavailable",
						"quota": map[string]any{"gpuAvailable": 24},
						"pool":  map[string]any{"totalGpus": 72, "allocatedGpus": 64, "reservedGpus": 4, "freeGpus": 4},
						"fit":   map[string]any{"workerReplicas": 2, "gpusPerWorker": 4, "cpuPerWorker": 16, "memoryPerWorker": "64Gi", "fittingNodes": 1, "workerSlots": 1, "canFit": false, "reason": "INSUFFICIENT_MATCHING_NODES"},
					},
				})
			}))
			var stdout bytes.Buffer
			err := Run(context.Background(), []string{"diagnose", "--server", server.URL, "--ca-file", writeTestCA(t, server), "--output", output, "job-1"}, &stdout, &bytes.Buffer{}, testEnvironment)
			server.Close()
			if err != nil {
				t.Fatal(err)
			}
			if calls != 1 {
				t.Fatalf("capacity must come from shared diagnosis, calls=%d", calls)
			}
			if output == "json" {
				var payload map[string]json.RawMessage
				if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil {
					t.Fatal(err)
				}
				if len(payload["schedulingCapacity"]) == 0 {
					t.Fatal("capacity lost in JSON")
				}
				continue
			}
			markers := []string{"capacity lookup temporarily unavailable"}
			if !unavailable {
				markers = []string{"24", "72", "64", "64Gi", "INSUFFICIENT_MATCHING_NODES", "2026-10-13T01:00:00Z"}
			}
			for _, marker := range markers {
				if !strings.Contains(stdout.String(), marker) {
					t.Fatalf("text lacks %q: %s", marker, stdout.String())
				}
			}
		}
	}
}

func TestDiagnoseExplainsSchedulingReasonCodesInChinese(t *testing.T) {
	for _, test := range []struct{ reason, explanation string }{
		{"insufficient_gpus", "资源池当前空闲 GPU 不足"},
		{"gpu_fragmentation", "空闲 GPU 分散"},
		{"insufficient_cpu_or_memory", "符合 GPU 条件的节点 CPU 或内存不足"},
		{"team_quota_exceeded", "团队可用额度不足以满足本任务请求"},
		{"no_eligible_nodes", "没有符合团队资源池、节点约束和健康状态的可调度节点"},
		{"fits", "当前容量快照可容纳本任务规格"},
	} {
		t.Run(test.reason, func(t *testing.T) {
			capacity := &domain.SchedulingCapacity{Available: true, Fit: domain.SchedulingCapacityFit{Reason: test.reason}}
			var output bytes.Buffer
			if err := renderSchedulingCapacity(&output, capacity); err != nil {
				t.Fatal(err)
			}
			for _, marker := range []string{test.reason, test.explanation, "不保证排队顺序或准入"} {
				if !strings.Contains(output.String(), marker) {
					t.Fatalf("output lacks %q: %s", marker, output.String())
				}
			}
		})
	}
}
