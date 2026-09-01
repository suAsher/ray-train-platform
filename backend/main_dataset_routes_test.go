package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"ray-train-platform-backend/api"
	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/config"
	"ray-train-platform-backend/domain"
	"ray-train-platform-backend/repositories"
)

type mainDatasetCatalog struct {
	datasets []domain.Dataset
	created  []domain.Dataset
}

func (store *mainDatasetCatalog) CreateDataset(_ context.Context, dataset domain.Dataset) error {
	store.created = append(store.created, dataset)
	return nil
}

func (store *mainDatasetCatalog) GetDataset(_ context.Context, tenantID string, superAdmin bool, id string) (domain.Dataset, error) {
	for _, dataset := range store.datasets {
		if dataset.ID == id && domain.CanViewDataset(dataset, tenantID, superAdmin) {
			return dataset, nil
		}
	}
	return domain.Dataset{}, repositories.ErrDatasetNotFound
}

func (store *mainDatasetCatalog) ListDatasets(_ context.Context, tenantID string, superAdmin bool) ([]domain.Dataset, error) {
	visible := make([]domain.Dataset, 0, len(store.datasets))
	for _, dataset := range store.datasets {
		if domain.CanViewDataset(dataset, tenantID, superAdmin) {
			visible = append(visible, dataset)
		}
	}
	return visible, nil
}

func (*mainDatasetCatalog) GetDatasetVersion(context.Context, string, bool, string, string) (domain.DatasetVersion, error) {
	return domain.DatasetVersion{}, repositories.ErrDatasetVersionNotFound
}

func (*mainDatasetCatalog) ListDatasetVersions(context.Context, string, bool, string) ([]domain.DatasetVersion, error) {
	return nil, nil
}

func (*mainDatasetCatalog) GetDatasetPublicationRunForVersion(context.Context, string, bool, string, string) (domain.DatasetPublicationRun, error) {
	return domain.DatasetPublicationRun{}, repositories.ErrDatasetPublicationRunNotFound
}

func (*mainDatasetCatalog) ResolveReadyDatasetVersion(context.Context, string, bool, string, domain.DatasetVersionSelector) (domain.DatasetVersion, error) {
	return domain.DatasetVersion{}, repositories.ErrDatasetVersionNotReady
}

func (*mainDatasetCatalog) TransitionDatasetVersion(context.Context, string, string, domain.DatasetVersionState) (domain.DatasetVersion, error) {
	return domain.DatasetVersion{}, repositories.ErrDatasetVersionConflict
}

type mainDatasetPATVerifier struct {
	principal auth.Principal
	scopes    []string
}

func (verifier *mainDatasetPATVerifier) Authenticate(context.Context, string) (auth.PATIdentity, error) {
	return auth.PATIdentity{Principal: verifier.principal, Scopes: append([]string(nil), verifier.scopes...)}, nil
}

type mainDatasetLocalVerifier struct{ principal auth.Principal }

func (verifier *mainDatasetLocalVerifier) Authenticate(context.Context, string) (auth.Principal, error) {
	return verifier.principal, nil
}

func mainDatasetRouter(store *mainDatasetCatalog, pat auth.PATVerifier, local auth.LocalSessionVerifier) *gin.Engine {
	jobs := api.NewHandler(&mainJobRepository{}, api.Options{Datasets: store})
	router := gin.New()
	registerAPIRoutesWithLocalAuth(router, jobs, nil, nil, nil, nil, pat, local, nil, nil, config.Config{
		OIDCRequired: true, DatasetVersioningEnabled: true,
	})
	return router
}

func TestDatasetCatalogRoutesFollowVersioningFeatureFlag(t *testing.T) {
	gin.SetMode(gin.TestMode)
	jobs := api.NewHandler(&mainJobRepository{}, api.Options{AllowAnonymous: true})
	for _, test := range []struct {
		name    string
		enabled bool
		want    int
	}{
		{name: "disabled", enabled: false, want: http.StatusNotFound},
		{name: "enabled", enabled: true, want: http.StatusServiceUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			router := gin.New()
			registerAPIRoutes(router, jobs, nil, nil, nil, nil, nil, config.Config{
				DemoMode: true, DatasetVersioningEnabled: test.enabled,
			})
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/datasets", nil))
			if response.Code != test.want {
				t.Fatalf("dataset route status=%d, want %d: %s", response.Code, test.want, response.Body.String())
			}
		})
	}
}

func TestDatasetCatalogPATCanReadButCannotUseInteractiveManagementRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := &mainDatasetCatalog{datasets: []domain.Dataset{{
		ID: "public-data", Slug: "public-data", Name: "Public data", SourceSpace: domain.DataSpacePublic,
		SourceRelativePath: "labeled", Visibility: domain.DatasetVisibilityPublic, SchemaVersion: "s1h-v1",
	}}}
	pat := &mainDatasetPATVerifier{principal: auth.Principal{
		Subject: "team-admin", TenantID: "team-a", Roles: []string{domain.RoleTenantAdmin},
	}, scopes: []string{domain.PATScopeJobsRead}}
	router := mainDatasetRouter(store, pat, nil)

	readRequest := httptest.NewRequest(http.MethodGet, "/api/v1/datasets", nil)
	readRequest.Header.Set("Authorization", "Bearer rpt_test-token")
	readResponse := httptest.NewRecorder()
	router.ServeHTTP(readResponse, readRequest)
	if readResponse.Code != http.StatusOK || !strings.Contains(readResponse.Body.String(), "public-data") {
		t.Fatalf("PAT catalog read status=%d body=%s", readResponse.Code, readResponse.Body.String())
	}

	mutationRequest := httptest.NewRequest(http.MethodPost, "/api/v1/datasets", strings.NewReader(`{
		"slug":"team-data","name":"Team data","visibility":"TEAM","sourceRelativePath":"datasets/team-data","schemaVersion":"s1h-v1"
	}`))
	mutationRequest.Header.Set("Authorization", "Bearer rpt_test-token")
	mutationRequest.Header.Set("Content-Type", "application/json")
	mutationResponse := httptest.NewRecorder()
	router.ServeHTTP(mutationResponse, mutationRequest)
	if mutationResponse.Code != http.StatusForbidden || len(store.created) != 0 {
		t.Fatalf("PAT mutation status=%d created=%+v body=%s", mutationResponse.Code, store.created, mutationResponse.Body.String())
	}

	writeOnlyPAT := &mainDatasetPATVerifier{principal: pat.principal, scopes: []string{domain.PATScopeJobsWrite}}
	writeOnlyRouter := mainDatasetRouter(store, writeOnlyPAT, nil)
	writeOnlyRequest := httptest.NewRequest(http.MethodGet, "/api/v1/datasets", nil)
	writeOnlyRequest.Header.Set("Authorization", "Bearer rpt_write-only")
	writeOnlyResponse := httptest.NewRecorder()
	writeOnlyRouter.ServeHTTP(writeOnlyResponse, writeOnlyRequest)
	if writeOnlyResponse.Code != http.StatusForbidden {
		t.Fatalf("write-only PAT catalog status=%d body=%s", writeOnlyResponse.Code, writeOnlyResponse.Body.String())
	}
}

func TestDatasetCatalogInteractiveAdminsCanCreateGovernedDefinitions(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, test := range []struct {
		name       string
		principal  auth.Principal
		body       string
		visibility domain.DatasetVisibility
	}{
		{
			name:       "tenant admin creates own team definition",
			principal:  auth.Principal{Subject: "team-admin", TenantID: "team-a", Roles: []string{domain.RoleTenantAdmin}},
			body:       `{"slug":"team-data","name":"Team data","visibility":"TEAM","sourceRelativePath":"datasets/team-data","schemaVersion":"s1h-v1"}`,
			visibility: domain.DatasetVisibilityTeam,
		},
		{
			name:       "super admin creates public definition",
			principal:  auth.Principal{Subject: "root-admin", TenantID: "platform", Roles: []string{domain.RoleSuperAdmin}},
			body:       `{"slug":"public-data","name":"Public data","visibility":"PUBLIC","sourceRelativePath":"labeled","schemaVersion":"s1h-v1"}`,
			visibility: domain.DatasetVisibilityPublic,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &mainDatasetCatalog{}
			local := &mainDatasetLocalVerifier{principal: test.principal}
			router := mainDatasetRouter(store, nil, local)
			request := httptest.NewRequest(http.MethodPost, "/api/v1/datasets", strings.NewReader(test.body))
			request.Header.Set("Authorization", "Bearer rls_test-session")
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != http.StatusCreated || len(store.created) != 1 || store.created[0].Visibility != test.visibility {
				t.Fatalf("status=%d created=%+v body=%s", response.Code, store.created, response.Body.String())
			}
		})
	}
}
