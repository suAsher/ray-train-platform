package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
	"ray-train-platform-backend/observability"
)

type namedJobRepository struct {
	fakeJobRepository
	ids []string
	err error
}

func (r *namedJobRepository) JobUsernames(_ context.Context, ids []string) (map[string]string, error) {
	r.ids = append([]string(nil), ids...)
	return map[string]string{"peer": "teammate.name", "outside": "hidden.name"}, r.err
}

func TestTeamJobDisplayNamesUseOnlyAuthorizedResults(t *testing.T) {
	for _, endpoint := range []string{"/jobs?scope=team", "/jobs/peer-job", "/experiments"} {
		t.Run(endpoint, func(t *testing.T) {
			r := &namedJobRepository{fakeJobRepository: fakeJobRepository{jobs: []domain.TrainingJob{
				{ID: "peer-job", TenantID: "team-a", UserID: "peer"},
				{ID: "outside-job", TenantID: "team-b", UserID: "outside"},
			}}}
			h := NewHandler(r, Options{Experiments: &fakeExperimentProvider{catalog: observability.ExperimentCatalog{Runs: []observability.ExperimentRunSummary{
				{ID: "peer-run", JobID: "peer-job"}, {ID: "forged-run", JobID: "outside-job", SubmitterUserID: "outside"},
			}}}})
			p := auth.Principal{Subject: "reader", TenantID: "team-a", Roles: []string{domain.RoleEngineer}, AuthType: auth.AuthTypeLocal}
			response := httptest.NewRecorder()
			jobGPUHistoryRouter(h, &p).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1"+endpoint, nil))
			if response.Code != 200 || !strings.Contains(response.Body.String(), "teammate.name") || strings.Contains(response.Body.String(), "hidden.name") {
				t.Fatalf("display names: %d %s", response.Code, response.Body.String())
			}
			if len(r.ids) != 1 || r.ids[0] != "peer" {
				t.Fatalf("queried users outside visible result: %v", r.ids)
			}
		})
	}
}

func TestJobDisplayLookupFailurePreservesReadableJob(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := &namedJobRepository{fakeJobRepository: fakeJobRepository{jobs: []domain.TrainingJob{{ID: "peer-job", TenantID: "team-a", UserID: "peer"}}}, err: errors.New("private database diagnostic")}
	h := NewHandler(r, Options{})
	p := auth.Principal{Subject: "reader", TenantID: "team-a", AuthType: auth.AuthTypeLocal}
	response := httptest.NewRecorder()
	jobGPUHistoryRouter(h, &p).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/jobs/peer-job", nil))
	if response.Code != 200 || !strings.Contains(response.Body.String(), "peer-job") || strings.Contains(response.Body.String(), "private database") || strings.Contains(response.Body.String(), "teammate.name") {
		t.Fatalf("optional display lookup changed job access: %d %s", response.Code, response.Body.String())
	}
}
