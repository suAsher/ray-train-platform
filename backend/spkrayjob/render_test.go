package spkrayjob

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// Raw JSON forced every user to pipe the CLI through jq to answer "did it
// run?". The default output is now readable; --output json keeps scripts and
// the existing e2e contract working.
func TestRenderJobTableSummarisesTheFieldsAUserActsOn(t *testing.T) {
	payload := json.RawMessage(`{"items":[
	  {"id":"job-1","spec":{"name":"bevfusion-lidar","resources":{"workerReplicas":1,"gpusPerWorker":8}},
	   "observedState":"RUNNING","submissionOrigin":"ray-cli","createdAt":"2026-08-19T02:03:04Z"},
	  {"id":"job-2","spec":{"name":"smoke","resources":{"workerReplicas":2,"gpusPerWorker":1}},
	   "observedState":"FAILED","statusMessage":"exit code 1","submissionOrigin":"portal","createdAt":"2026-08-19T01:00:00Z"}
	],"total":2}`)

	var output bytes.Buffer
	if err := renderJobTable(&output, payload); err != nil {
		t.Fatalf("render job table: %v", err)
	}
	text := output.String()
	for _, expected := range []string{"job-1", "bevfusion-lidar", "RUNNING", "8", "job-2", "FAILED"} {
		if !strings.Contains(text, expected) {
			t.Fatalf("expected %q in:\n%s", expected, text)
		}
	}
	if strings.Contains(text, `"observedState"`) {
		t.Fatalf("the table must not fall back to raw JSON:\n%s", text)
	}
}

func TestRenderJobDetailShowsStateReasonAndGovernedOutput(t *testing.T) {
	payload := json.RawMessage(`{"id":"job-9","spec":{"name":"bevfusion","execution":{"mode":"torchrun"},
	  "resources":{"workerReplicas":1,"gpusPerWorker":8},"output":{"space":"my-runs","relativePath":"bevfusion"}},
	  "observedState":"FAILED","statusReason":"Error","statusMessage":"CUDA out of memory","rayJobName":"bevfusion"}`)

	var output bytes.Buffer
	if err := renderJobDetail(&output, payload); err != nil {
		t.Fatalf("render job detail: %v", err)
	}
	text := output.String()
	for _, expected := range []string{"job-9", "FAILED", "CUDA out of memory", "torchrun", "8"} {
		if !strings.Contains(text, expected) {
			t.Fatalf("expected %q in:\n%s", expected, text)
		}
	}
}

func TestRenderLogLinesPrintsPlainTextInOrder(t *testing.T) {
	payload := json.RawMessage(`{"jobId":"job-1","items":[
	  {"timestamp":"2026-08-19T02:03:04Z","line":"epoch 1 loss 2.31"},
	  {"timestamp":"2026-08-19T02:03:05Z","line":"epoch 2 loss 1.87"}
	]}`)

	var output bytes.Buffer
	printed, err := renderLogLines(&output, payload, "")
	if err != nil {
		t.Fatalf("render logs: %v", err)
	}
	if printed != "2026-08-19T02:03:05Z" {
		t.Fatalf("the last timestamp is the follow cursor, got %q", printed)
	}
	lines := strings.Split(strings.TrimRight(output.String(), "\n"), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], "epoch 1 loss 2.31") || !strings.Contains(lines[1], "epoch 2 loss 1.87") {
		t.Fatalf("unexpected log rendering:\n%s", output.String())
	}
}

// Follow polls the same bounded endpoint repeatedly. Without a cursor every
// poll would reprint the whole buffer, which is unreadable for a long run.
func TestRenderLogLinesSuppressesLinesAlreadyPrinted(t *testing.T) {
	payload := json.RawMessage(`{"items":[
	  {"timestamp":"2026-08-19T02:03:04Z","line":"old"},
	  {"timestamp":"2026-08-19T02:03:05Z","line":"old too"},
	  {"timestamp":"2026-08-19T02:03:06Z","line":"new"}
	]}`)

	var output bytes.Buffer
	cursor, err := renderLogLines(&output, payload, "2026-08-19T02:03:05Z")
	if err != nil {
		t.Fatalf("render logs: %v", err)
	}
	if cursor != "2026-08-19T02:03:06Z" {
		t.Fatalf("expected the cursor to advance, got %q", cursor)
	}
	if strings.Contains(output.String(), "old") || !strings.Contains(output.String(), "new") {
		t.Fatalf("only unseen lines may print:\n%s", output.String())
	}
}

func TestJobStateIsTerminalOnlyForFinishedRuns(t *testing.T) {
	for _, state := range []string{"SUCCEEDED", "FAILED", "CANCELED", "TIMED_OUT"} {
		if !isTerminalJobState(state) {
			t.Fatalf("%s must end a watch", state)
		}
	}
	for _, state := range []string{"QUEUED", "PROVISIONING", "RUNNING", "SUBMITTED", ""} {
		if isTerminalJobState(state) {
			t.Fatalf("%s must keep a watch running", state)
		}
	}
}

// Resuming means "read the previous run's own output directory". Computing it
// on the client keeps the platform contract unchanged: the checkpoint is just
// an ordinary read-only selection in My runs.
func TestCheckpointLocationForPreviousRunPointsAtItsOutputDirectory(t *testing.T) {
	previous := json.RawMessage(`{"id":"job-abc","spec":{"output":{"space":"my-runs","relativePath":"bevfusion-lidar"}}}`)
	location, err := checkpointLocationForPreviousRun(previous)
	if err != nil {
		t.Fatalf("resolve resume location: %v", err)
	}
	if location.Space != "my-runs" || location.Path != "bevfusion-lidar/job-abc" {
		t.Fatalf("expected the previous run directory, got %+v", location)
	}
}

func TestCheckpointLocationRejectsARunWithoutGovernedOutput(t *testing.T) {
	previous := json.RawMessage(`{"id":"job-abc","spec":{}}`)
	if _, err := checkpointLocationForPreviousRun(previous); err == nil {
		t.Fatal("a run with no managed output cannot be resumed from")
	}
}
