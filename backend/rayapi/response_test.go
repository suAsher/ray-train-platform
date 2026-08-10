package rayapi

import (
	"encoding/json"
	"testing"
)

func TestRay235JobSubmitResponseContainsSubmissionAndJobIDs(t *testing.T) {
	encoded, err := json.Marshal(jobSubmitResponse{SubmissionID: "raysubmit_sdk", JobID: "job-platform"})
	if err != nil {
		t.Fatal(err)
	}
	var response map[string]string
	if err := json.Unmarshal(encoded, &response); err != nil {
		t.Fatal(err)
	}
	if response["submission_id"] != "raysubmit_sdk" || response["job_id"] != "job-platform" {
		t.Fatalf("Ray 2.35 response fields=%v", response)
	}
}
