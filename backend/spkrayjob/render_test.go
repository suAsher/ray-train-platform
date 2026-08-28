package spkrayjob

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"ray-train-platform-backend/domain"
)

func TestRenderSubmitCommandIncludesEngineOnlyWhenBothEnginesAreAdvertised(t *testing.T) {
	both := PlatformRuntimeLimits{AvailableEngines: []string{"ray-ddp", "ray-train"}, ManagedEnabled: true}
	if got := renderSubmitCommand(domain.TrainingEngineRayTrain, both); got != "spk-rayjob submit --engine ray-train --watch" {
		t.Fatalf("managed-capable command=%q", got)
	}
	legacy := PlatformRuntimeLimits{AvailableEngines: []string{"ray-ddp"}}
	if got := renderSubmitCommand(domain.TrainingEngineRayDDP, legacy); got != "spk-rayjob submit --watch" {
		t.Fatalf("legacy-only command must not advertise engine selection: %q", got)
	}
}

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
	payload := json.RawMessage(`{"id":"job-9","spec":{"name":"bevfusion","trainingEngine":"ray-train","execution":{"mode":"torchrun"},
	  "resources":{"workerReplicas":1,"gpusPerWorker":8},"output":{"space":"my-runs","relativePath":"bevfusion"}},
	  "observedState":"FAILED","statusReason":"Error","statusMessage":"CUDA out of memory","rayJobName":"bevfusion"}`)

	var output bytes.Buffer
	if err := renderJobDetail(&output, payload); err != nil {
		t.Fatalf("render job detail: %v", err)
	}
	text := output.String()
	for _, expected := range []string{"job-9", "FAILED", "CUDA out of memory", "ray-train", "torchrun", "8"} {
		if !strings.Contains(text, expected) {
			t.Fatalf("expected %q in:\n%s", expected, text)
		}
	}
}

func TestRenderLegacyJobDetailResolvesOmittedEngineToRayDDP(t *testing.T) {
	payload := json.RawMessage(`{"id":"job-old","spec":{"name":"old","execution":{"mode":"torchrun"},"resources":{"workerReplicas":1,"gpusPerWorker":8}}}`)
	var output bytes.Buffer
	if err := renderJobDetail(&output, payload); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "ray-ddp") {
		t.Fatalf("an omitted pre-upgrade engine must render as ray-ddp:\n%s", output.String())
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

func TestCheckpointLocationForPreviousRunSelectsFirstCompleteOwnerScopedCheckpoint(t *testing.T) {
	const parentID = "job-0123456789abcdef01234567"
	previous := json.RawMessage(`{"id":"` + parentID + `","tenantId":"tenant-a","userId":"user-a","spec":{"output":{"space":"my-runs","relativePath":"bevfusion-lidar"}}}`)
	page := JobCheckpointPage{JobID: parentID, Items: []domain.TrainingCheckpoint{
		{ID: "checkpoint-new", JobID: parentID, TenantID: "tenant-a", UserID: "user-a", Epoch: 2, Step: 20, ObjectPath: "/mnt/data/output/.platform/ray-train/" + parentID + "/checkpoints/checkpoint-new", Complete: true, ManifestSHA256: strings.Repeat("a", 64)},
		{ID: "checkpoint-old", JobID: parentID, TenantID: "tenant-a", UserID: "user-a", Epoch: 1, Step: 10, ObjectPath: "/mnt/data/output/.platform/ray-train/" + parentID + "/checkpoints/checkpoint-old", Complete: true, ManifestSHA256: strings.Repeat("b", 64)},
	}}
	selection, err := checkpointLocationForPreviousRun(previous, page)
	if err != nil {
		t.Fatalf("resolve resume location: %v", err)
	}
	wantPath := "bevfusion-lidar/" + parentID + "/.platform/ray-train/" + parentID + "/checkpoints/checkpoint-new"
	if selection.Location.Space != "my-runs" || selection.Location.Path != wantPath || selection.CheckpointID != "checkpoint-new" {
		t.Fatalf("expected the latest complete checkpoint, got %+v", selection)
	}
}

func TestCheckpointLocationRejectsMissingForeignOrForgedCheckpoint(t *testing.T) {
	const parentID = "job-0123456789abcdef01234567"
	previous := json.RawMessage(`{"id":"` + parentID + `","tenantId":"tenant-a","userId":"user-a","spec":{"output":{"space":"my-runs","relativePath":"run"}}}`)
	valid := domain.TrainingCheckpoint{ID: "checkpoint-1", JobID: parentID, TenantID: "tenant-a", UserID: "user-a", Epoch: 1, Step: 2, ObjectPath: "/mnt/data/output/.platform/ray-train/" + parentID + "/checkpoints/checkpoint-1", Complete: true, ManifestSHA256: strings.Repeat("a", 64)}
	for _, test := range []struct {
		name     string
		previous json.RawMessage
		page     JobCheckpointPage
	}{
		{name: "no complete checkpoint", previous: previous, page: JobCheckpointPage{JobID: parentID}},
		{name: "response job mismatch", previous: previous, page: JobCheckpointPage{JobID: "job-fedcba9876543210fedcba98", Items: []domain.TrainingCheckpoint{valid}}},
		{name: "checkpoint owner mismatch", previous: previous, page: JobCheckpointPage{JobID: parentID, Items: []domain.TrainingCheckpoint{func() domain.TrainingCheckpoint { item := valid; item.UserID = "user-b"; return item }()}}},
		{name: "forged object path", previous: previous, page: JobCheckpointPage{JobID: parentID, Items: []domain.TrainingCheckpoint{func() domain.TrainingCheckpoint {
			item := valid
			item.ObjectPath = "/mnt/data/output/other"
			return item
		}()}}},
		{name: "missing governed output", previous: json.RawMessage(`{"id":"` + parentID + `","tenantId":"tenant-a","userId":"user-a","spec":{}}`), page: JobCheckpointPage{JobID: parentID, Items: []domain.TrainingCheckpoint{valid}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := checkpointLocationForPreviousRun(test.previous, test.page); err == nil {
				t.Fatal("unsafe resume checkpoint was accepted")
			}
		})
	}
}
