package rayapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
)

func TestRayListJobsReturnsAllTenantJobsAcrossRepositoryPages(t *testing.T) {
	jobs := make([]domain.TrainingJob, 0, 201)
	for index := 0; index < 201; index++ {
		jobs = append(jobs, domain.TrainingJob{ID: fmt.Sprintf("job-%03d", index), TenantID: "tenant-a", ExternalSubmissionID: fmt.Sprintf("submission-%03d", index), ObservedState: domain.StateSubmitted, Spec: domain.JobSpec{Entrypoint: domain.Entrypoint{Command: []string{"python", "train.py"}}}})
	}
	principal := auth.Principal{Subject: "user-a", TenantID: "tenant-a", Roles: []string{"Engineer"}, AuthType: auth.AuthTypeOIDC}
	response := rayRequest(rayRouter(t, &rayTestRepository{jobs: jobs}, &rayTestStore{}, principal), http.MethodGet, "/ray/api/jobs/", "")
	if response.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", response.Code, response.Body.String())
	}
	var listed []jobDetailsResponse
	if err := json.Unmarshal(response.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed) != len(jobs) {
		t.Fatalf("list returned %d jobs, want %d", len(listed), len(jobs))
	}
}
