package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
	mr "ray-train-platform-backend/modelregistry"
	"ray-train-platform-backend/observability"
)

type dashboardRegistryLinksFake struct {
	mr.Store
	record  mr.Record
	err     error
	queried string
}

func (s *dashboardRegistryLinksFake) GetReadyByRunID(_ context.Context, runID string) (mr.Record, error) {
	s.queried = runID
	return s.record, s.err
}
func dashboardRegistryRecord(runID string) mr.Record {
	return mr.Record{VersionID: "model-version", State: "READY", RegisteredName: "raytrain-model-example", RegistryVersion: "1", RunID: runID, SourceURI: "mlflow-artifacts:/42/" + runID + "/artifacts/checkpoint/" + strings.Repeat("a", 64) + ".safetensors"}
}
func TestMLflowDashboardRegistryRunSharedInteractiveTicketAndRedirect(t *testing.T) {
	now := time.Date(2026, 9, 13, 0, 0, 0, 0, time.UTC)
	store := newFakeMLflowDashboardStore()
	h := newMLflowDashboardTestHandler(store, now)
	runID := "1e0205b5055349029258b16c45f9c1f5"
	links := &dashboardRegistryLinksFake{record: dashboardRegistryRecord(runID)}
	h.modelRegistryLinks = links
	// No tenant experiment provider or training job exists for a Registry copy Run.
	principal := auth.Principal{Subject: "another-member", TenantID: "another-team", AuthType: auth.AuthTypeLocal, Roles: []string{domain.RoleEngineer}}
	response := httptest.NewRecorder()
	mlflowAccessRouter(h, &principal, true).ServeHTTP(response, httptest.NewRequest("POST", "/api/v1/mlflow-dashboard-access", strings.NewReader(`{"runId":"`+runID+`"}`)))
	if response.Code != 200 || links.queried != runID || len(store.created) != 1 || store.created[0].RedirectFragment != "#/experiments/42/runs/"+runID {
		t.Fatalf("registry ticket %d %s records=%+v", response.Code, response.Body.String(), store.created)
	}
	var envelope struct {
		Data struct {
			URL string `json:"url"`
		}
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	exchange := httptest.NewRecorder()
	mlflowProxyRouter(h).ServeHTTP(exchange, httptest.NewRequest("GET", envelope.Data.URL, nil))
	if exchange.Code != http.StatusFound || exchange.Header().Get("Location") != "/mlflow/#/experiments/42/runs/"+runID {
		t.Fatalf("registry exchange %d %q", exchange.Code, exchange.Header().Get("Location"))
	}
}
func TestMLflowDashboardRegistryRunRejectsUntrustedRecords(t *testing.T) {
	runID := "1e0205b5055349029258b16c45f9c1f5"
	valid := dashboardRegistryRecord(runID)
	for _, tc := range []struct {
		name   string
		change func(*mr.Record)
		err    error
	}{
		{name: "not ready", change: func(r *mr.Record) { r.State = "SYNCING" }},
		{name: "wrong stored run", change: func(r *mr.Record) { r.RunID = strings.Repeat("b", 32) }},
		{name: "wrong URI run", change: func(r *mr.Record) { r.SourceURI = strings.Replace(r.SourceURI, runID, strings.Repeat("b", 32), 1) }},
		{name: "external URL", change: func(r *mr.Record) { r.SourceURI = "https://example.invalid/steal" }},
		{name: "URI authority", change: func(r *mr.Record) {
			r.SourceURI = strings.Replace(r.SourceURI, "mlflow-artifacts:/", "mlflow-artifacts://example.invalid/", 1)
		}},
		{name: "non numeric experiment", change: func(r *mr.Record) { r.SourceURI = strings.Replace(r.SourceURI, "/42/", "/another/", 1) }},
		{name: "traversal", change: func(r *mr.Record) { r.SourceURI = strings.Replace(r.SourceURI, "/checkpoint/", "/checkpoint/../", 1) }},
		{name: "encoded traversal", change: func(r *mr.Record) { r.SourceURI = strings.Replace(r.SourceURI, "/42/", "/%2e%2e/", 1) }},
		{name: "query", change: func(r *mr.Record) { r.SourceURI += "?redirect=https://example.invalid" }},
		{name: "fragment", change: func(r *mr.Record) { r.SourceURI += "#/redirect" }},
		{name: "invalid hash", change: func(r *mr.Record) {
			r.SourceURI = strings.Replace(r.SourceURI, strings.Repeat("a", 64), "arbitrary", 1)
		}},
		{name: "invalid extension", change: func(r *mr.Record) { r.SourceURI = strings.Replace(r.SourceURI, ".safetensors", ".html", 1) }},
		{name: "duplicate records", err: mr.ErrConflict},
		{name: "lookup failed", err: errors.New("database unavailable")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := valid
			if tc.change != nil {
				tc.change(&r)
			}
			store := newFakeMLflowDashboardStore()
			h := newMLflowDashboardTestHandler(store, time.Now())
			h.modelRegistryLinks = &dashboardRegistryLinksFake{record: r, err: tc.err}
			p := streamingPrincipal()
			response := httptest.NewRecorder()
			mlflowAccessRouter(h, &p, true).ServeHTTP(response, httptest.NewRequest("POST", "/api/v1/mlflow-dashboard-access", strings.NewReader(`{"runId":"`+runID+`"}`)))
			if response.Code != 404 || len(store.created) != 0 {
				t.Fatalf("unsafe registry record accepted %d %s", response.Code, response.Body.String())
			}
		})
	}
}
func TestMLflowDashboardMissingRegistryLinkPreservesTrainingOwnership(t *testing.T) {
	runID := "1e0205b5055349029258b16c45f9c1f5"
	for _, own := range []bool{true, false} {
		t.Run(map[bool]string{true: "own", false: "other"}[own], func(t *testing.T) {
			h := newMLflowDashboardTestHandler(newFakeMLflowDashboardStore(), time.Now())
			h.modelRegistryLinks = &dashboardRegistryLinksFake{record: mr.Record{State: "NOT_LINKED"}}
			p := streamingPrincipal()
			owner := p.Subject
			if !own {
				owner = "another-user"
			}
			h.repository = &fakeJobRepository{jobs: []domain.TrainingJob{{ID: "training-job", TenantID: p.TenantID, UserID: owner}}}
			h.experiments = &fakeExperimentProvider{catalog: observability.ExperimentCatalog{ExperimentID: "7", Runs: []observability.ExperimentRunSummary{{ID: runID, JobID: "training-job"}}}}
			fragment, err := h.mlflowDashboardRunRedirect(context.Background(), p, runID)
			if own {
				if err != nil || fragment != "#/experiments/7/runs/"+runID {
					t.Fatalf("own training inaccessible %q %v", fragment, err)
				}
			} else if err == nil {
				t.Fatal("registry fallback expanded training ownership")
			}
		})
	}
}
func TestMLflowDashboardRegistryRunRejectsPATAndIntegration(t *testing.T) {
	runID := "1e0205b5055349029258b16c45f9c1f5"
	for _, kind := range []string{"pat", "integration"} {
		t.Run(kind, func(t *testing.T) {
			h := newMLflowDashboardTestHandler(newFakeMLflowDashboardStore(), time.Now())
			links := &dashboardRegistryLinksFake{record: dashboardRegistryRecord(runID)}
			h.modelRegistryLinks = links
			p := streamingPrincipal()
			if kind == "pat" {
				p.AuthType = auth.AuthTypePAT
				p.Scopes = []string{domain.PATScopeMLflowFull}
			} else {
				p.IntegrationID = "automation"
			}
			if _, err := h.mlflowDashboardRunRedirect(context.Background(), p, runID); err == nil || links.queried != "" {
				t.Fatal("noninteractive membership accepted")
			}
		})
	}
}
