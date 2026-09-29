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

func TestDiagnoseUsesSharedAPIForJSONAndTextWithoutStatusOrLogsCalls(t *testing.T) {
	for _, output := range []string{"json", "text"} {
		t.Run(output, func(t *testing.T) {
			calls := 0
			server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				calls++
				if request.Method != http.MethodGet || request.URL.Path != "/api/v1/jobs/job-1/diagnosis" { t.Fatalf("unexpected request: %s %s", request.Method, request.URL.Path) }
				writeClientSuccess(t, writer, http.StatusOK, map[string]any{
					"jobId": "job-1", "observedState": "RUNNING", "classification": "application_failure", "summary": "First recognized error in retained logs; not a proven root cause.",
					"firstFailure": map[string]any{"timestamp": "2026-09-29T01:00:00Z", "line": "loss=nan", "kind": "numerical_error"},
					"followups": []any{map[string]any{"timestamp": "2026-09-29T02:00:00Z", "line": "NCCL watchdog timeout", "kind": "communication_error"}},
					"completionEvidence": []any{}, "coverage": map[string]any{"filtered": true, "partial": true, "truncated": false, "logUnavailable": false},
				})
			}))
			defer server.Close()
			var stdout bytes.Buffer
			err := Run(context.Background(), []string{"diagnose", "--server", server.URL, "--ca-file", writeTestCA(t, server), "--output", output, "job-1"}, &stdout, &bytes.Buffer{}, testEnvironment)
			if err != nil { t.Fatal(err) }
			if calls != 1 { t.Fatalf("diagnose used %d requests", calls) }
			if output == "json" {
				var data map[string]any
				if err := json.Unmarshal(stdout.Bytes(), &data); err != nil || data["observedState"] != "RUNNING" || data["classification"] != "application_failure" { t.Fatalf("invalid shared JSON: %s (%v)", stdout.String(), err) }
			} else {
				for _, marker := range []string{"RUNNING", "loss=nan", "NCCL watchdog timeout", "not a proven root cause", "partial=true", "truncated=false", "logUnavailable=false", "contextUnavailable=false"} { if !strings.Contains(stdout.String(), marker) { t.Fatalf("text lacks %q: %s", marker, stdout.String()) } }
			}
		})
	}
}

func TestDiagnoseHelpAndValidationDoNotNeedCredentials(t *testing.T) {
	var stdout bytes.Buffer
	if err := Run(context.Background(), []string{"diagnose", "--help"}, &stdout, &bytes.Buffer{}, func(string) string { return "" }); err != nil { t.Fatal(err) }
	if !strings.Contains(stdout.String(), "diagnose") || !strings.Contains(stdout.String(), "--output") { t.Fatalf("incomplete help: %s", stdout.String()) }
	if err := Run(context.Background(), []string{"diagnose"}, &stdout, &bytes.Buffer{}, func(string) string { return "" }); err == nil || !strings.Contains(err.Error(), "job ID") { t.Fatalf("missing ID: %v", err) }
}
