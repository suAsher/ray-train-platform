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
	"ray-train-platform-backend/repositories"
)

type fakeDatasetCatalog struct {
	datasets            []domain.Dataset
	versions            []domain.DatasetVersion
	created             domain.Dataset
	createErr           error
	getErr              error
	listErr             error
	getVersionErr       error
	listVersionsErr     error
	resolveErr          error
	lastTenantID        string
	lastSuperAdmin      bool
	transitionDatasetID string
	transitionVersionID string
	transitionNext      domain.DatasetVersionState
	transitionedVersion domain.DatasetVersion
	transitionErr       error
	publicationRun      domain.DatasetPublicationRun
	publicationErr      error
}

func (store *fakeDatasetCatalog) CreateDataset(_ context.Context, dataset domain.Dataset) error {
	store.created = dataset
	return store.createErr
}

func (store *fakeDatasetCatalog) GetDataset(_ context.Context, tenantID string, superAdmin bool, datasetID string) (domain.Dataset, error) {
	store.lastTenantID, store.lastSuperAdmin = tenantID, superAdmin
	if store.getErr != nil {
		return domain.Dataset{}, store.getErr
	}
	for _, dataset := range store.datasets {
		if dataset.ID == datasetID && domain.CanViewDataset(dataset, tenantID, superAdmin) {
			return dataset, nil
		}
	}
	return domain.Dataset{}, repositories.ErrDatasetNotFound
}

func (store *fakeDatasetCatalog) ListDatasets(_ context.Context, tenantID string, superAdmin bool) ([]domain.Dataset, error) {
	store.lastTenantID, store.lastSuperAdmin = tenantID, superAdmin
	if store.listErr != nil {
		return nil, store.listErr
	}
	visible := make([]domain.Dataset, 0, len(store.datasets))
	for _, dataset := range store.datasets {
		if domain.CanViewDataset(dataset, tenantID, superAdmin) {
			visible = append(visible, dataset)
		}
	}
	return visible, nil
}

func (store *fakeDatasetCatalog) GetDatasetVersion(_ context.Context, tenantID string, superAdmin bool, datasetID, versionID string) (domain.DatasetVersion, error) {
	if store.getVersionErr != nil {
		return domain.DatasetVersion{}, store.getVersionErr
	}
	if _, err := store.GetDataset(context.Background(), tenantID, superAdmin, datasetID); err != nil {
		return domain.DatasetVersion{}, repositories.ErrDatasetVersionNotFound
	}
	for _, version := range store.versions {
		if version.DatasetID == datasetID && version.ID == versionID {
			return version, nil
		}
	}
	return domain.DatasetVersion{}, repositories.ErrDatasetVersionNotFound
}

func (store *fakeDatasetCatalog) ListDatasetVersions(_ context.Context, tenantID string, superAdmin bool, datasetID string) ([]domain.DatasetVersion, error) {
	if store.listVersionsErr != nil {
		return nil, store.listVersionsErr
	}
	if _, err := store.GetDataset(context.Background(), tenantID, superAdmin, datasetID); err != nil {
		return nil, err
	}
	versions := make([]domain.DatasetVersion, 0)
	for _, version := range store.versions {
		if version.DatasetID == datasetID {
			versions = append(versions, version)
		}
	}
	return versions, nil
}

func (store *fakeDatasetCatalog) GetDatasetPublicationRunForVersion(_ context.Context, tenantID string, superAdmin bool, datasetID, versionID string) (domain.DatasetPublicationRun, error) {
	if store.publicationErr != nil {
		return domain.DatasetPublicationRun{}, store.publicationErr
	}
	if _, err := store.GetDataset(context.Background(), tenantID, superAdmin, datasetID); err != nil || store.publicationRun.DatasetID != datasetID || store.publicationRun.DatasetVersionID != versionID {
		return domain.DatasetPublicationRun{}, repositories.ErrDatasetPublicationRunNotFound
	}
	return store.publicationRun, nil
}

func (store *fakeDatasetCatalog) ResolveReadyDatasetVersion(_ context.Context, tenantID string, superAdmin bool, datasetID string, selector domain.DatasetVersionSelector) (domain.DatasetVersion, error) {
	if store.resolveErr != nil {
		return domain.DatasetVersion{}, store.resolveErr
	}
	if _, err := store.GetDataset(context.Background(), tenantID, superAdmin, datasetID); err != nil {
		return domain.DatasetVersion{}, repositories.ErrDatasetVersionNotReady
	}
	for index := len(store.versions) - 1; index >= 0; index-- {
		version := store.versions[index]
		if version.DatasetID == datasetID && version.State == domain.DatasetVersionReady && (selector.Latest || selector.VersionID == version.ID) {
			return version, nil
		}
	}
	return domain.DatasetVersion{}, repositories.ErrDatasetVersionNotReady
}

func (store *fakeDatasetCatalog) TransitionDatasetVersion(_ context.Context, datasetID, versionID string, next domain.DatasetVersionState) (domain.DatasetVersion, error) {
	store.transitionDatasetID, store.transitionVersionID, store.transitionNext = datasetID, versionID, next
	if store.transitionErr != nil {
		return domain.DatasetVersion{}, store.transitionErr
	}
	return store.transitionedVersion, nil
}

type fakeDatasetPublicationManager struct {
	dataset     domain.Dataset
	requestedBy string
	run         domain.DatasetPublicationRun
	gcVersions  []domain.DatasetVersion
	err         error
	gcCalled    bool
}

func (manager *fakeDatasetPublicationManager) RequestDatasetPublication(_ context.Context, dataset domain.Dataset, requestedBy string) (domain.DatasetPublicationRun, error) {
	manager.dataset, manager.requestedBy = dataset, requestedBy
	return manager.run, manager.err
}

func (manager *fakeDatasetPublicationManager) DryRunDatasetVersionGC(_ context.Context) ([]domain.DatasetVersion, error) {
	manager.gcCalled = true
	return manager.gcVersions, manager.err
}

func datasetForAPI(id, visibility, tenantID string) domain.Dataset {
	dataset := domain.Dataset{
		ID: id, Slug: id, Name: "Dataset " + id, Description: "governed training data",
		SourceRelativePath: "labeled/" + id, Visibility: domain.DatasetVisibility(visibility), SchemaVersion: "s1h-v1",
	}
	if dataset.Visibility == domain.DatasetVisibilityPublic {
		dataset.SourceSpace = domain.DataSpacePublic
	} else {
		dataset.SourceSpace = domain.DataSpaceTeamShared
		dataset.OwnerTenantID = tenantID
	}
	return dataset
}

func readyDatasetVersionForAPI(datasetID, versionID string) domain.DatasetVersion {
	return domain.DatasetVersion{
		ID: versionID, DatasetID: datasetID, Version: "2026.08.30", State: domain.DatasetVersionReady,
		ManifestSHA256:    strings.Repeat("a", 64),
		ManifestObjectKey: "ray-train/platform/datasets/" + datasetID + "/manifests/" + versionID + ".parquet",
		SchemaVersion:     "s1h-v1", TrainSamples: 100, SourceObjectCount: 1_000, LogicalBytes: 2_000, PackedBytes: 1_500,
	}
}

func datasetAPIRouter(handler *Handler, principal *auth.Principal) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	if principal != nil {
		router.Use(func(c *gin.Context) {
			c.Set("ray-platform-principal", *principal)
			c.Next()
		})
	}
	handler.RegisterDatasetRoutes(router.Group("/api/v1"))
	return router
}

func datasetPrincipal(subject, tenant string, roles ...string) auth.Principal {
	return auth.Principal{Subject: subject, TenantID: tenant, Roles: roles, AuthType: auth.AuthTypeLocal}
}

func serveDatasetAPI(router *gin.Engine, method, path, body string) *httptest.ResponseRecorder {
	response := httptest.NewRecorder()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	router.ServeHTTP(response, request)
	return response
}

func TestDatasetAPIRequiresAuthenticationAndListsOnlyVisibleLogicalFields(t *testing.T) {
	store := &fakeDatasetCatalog{datasets: []domain.Dataset{
		datasetForAPI("public-data", string(domain.DatasetVisibilityPublic), ""),
		datasetForAPI("team-a-data", string(domain.DatasetVisibilityTeam), "team-a"),
		datasetForAPI("team-b-data", string(domain.DatasetVisibilityTeam), "team-b"),
	}}
	handler := NewHandler(nil, Options{Datasets: store})
	unauthenticated := serveDatasetAPI(datasetAPIRouter(handler, nil), http.MethodGet, "/api/v1/datasets", "")
	if unauthenticated.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated list status=%d body=%s", unauthenticated.Code, unauthenticated.Body.String())
	}

	principal := datasetPrincipal("engineer-a", "team-a", domain.RoleEngineer)
	response := serveDatasetAPI(datasetAPIRouter(handler, &principal), http.MethodGet, "/api/v1/datasets", "")
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "public-data") || !strings.Contains(response.Body.String(), "team-a-data") || strings.Contains(response.Body.String(), "team-b-data") {
		t.Fatalf("visible dataset response status=%d body=%s", response.Code, response.Body.String())
	}
	for _, secret := range []string{"manifestObjectKey", "ray-train/platform/datasets", "accessKey", "secretKey", "signedUrl", "serviceAccount"} {
		if strings.Contains(response.Body.String(), secret) {
			t.Fatalf("dataset list leaked internal field %q: %s", secret, response.Body.String())
		}
	}
	if store.lastTenantID != "team-a" || store.lastSuperAdmin {
		t.Fatalf("list scope tenant=%q super=%t", store.lastTenantID, store.lastSuperAdmin)
	}
}

func TestDatasetRoutesSeparatePATReadableCatalogFromInteractiveManagement(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := NewHandler(nil, Options{})

	readRouter := gin.New()
	handler.RegisterDatasetReadRoutes(readRouter.Group("/api/v1"))
	readMutation := serveDatasetAPI(readRouter, http.MethodPost, "/api/v1/datasets", "{}")
	if readMutation.Code != http.StatusNotFound {
		t.Fatalf("read-only catalog exposed mutation route: status=%d body=%s", readMutation.Code, readMutation.Body.String())
	}

	managementRouter := gin.New()
	handler.RegisterDatasetManagementRoutes(managementRouter.Group("/api/v1"))
	managementRead := serveDatasetAPI(managementRouter, http.MethodGet, "/api/v1/datasets", "")
	if managementRead.Code != http.StatusNotFound {
		t.Fatalf("management route set duplicated catalog read: status=%d body=%s", managementRead.Code, managementRead.Body.String())
	}
}

func TestDatasetAPIVersionsAndLatestHideInternalManifestLocation(t *testing.T) {
	dataset := datasetForAPI("public-data", string(domain.DatasetVisibilityPublic), "")
	version := readyDatasetVersionForAPI(dataset.ID, "version-ready")
	store := &fakeDatasetCatalog{datasets: []domain.Dataset{dataset}, versions: []domain.DatasetVersion{version}}
	handler := NewHandler(nil, Options{Datasets: store})
	principal := datasetPrincipal("engineer-a", "team-a", domain.RoleEngineer)
	router := datasetAPIRouter(handler, &principal)

	for _, path := range []string{
		"/api/v1/datasets/public-data/versions",
		"/api/v1/datasets/public-data/versions/version-ready",
		"/api/v1/datasets/public-data/versions/latest",
	} {
		response := serveDatasetAPI(router, http.MethodGet, path, "")
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "version-ready") {
			t.Fatalf("GET %s status=%d body=%s", path, response.Code, response.Body.String())
		}
		if strings.Contains(response.Body.String(), version.ManifestObjectKey) || strings.Contains(response.Body.String(), "manifestObjectKey") {
			t.Fatalf("GET %s leaked manifest object key: %s", path, response.Body.String())
		}
	}
}

func TestDatasetAuthorizationCreatesTeamAndPublicDefinitionsWithoutAcceptingInfrastructureFields(t *testing.T) {
	teamStore := &fakeDatasetCatalog{}
	teamHandler := NewHandler(nil, Options{Datasets: teamStore})
	teamAdmin := datasetPrincipal("team-admin", "team-a", domain.RoleTenantAdmin)
	teamRouter := datasetAPIRouter(teamHandler, &teamAdmin)
	teamResponse := serveDatasetAPI(teamRouter, http.MethodPost, "/api/v1/datasets", `{
		"slug":"s1h-team","name":"S1H team","description":"team data","visibility":"TEAM",
		"sourceRelativePath":"datasets/s1h","schemaVersion":"s1h-v1",
		"ownerTenantId":"team-b","manifestObjectKey":"ray-train/platform/datasets/escape"
	}`)
	if teamResponse.Code != http.StatusCreated {
		t.Fatalf("team create status=%d body=%s", teamResponse.Code, teamResponse.Body.String())
	}
	if teamStore.created.OwnerTenantID != "team-a" || teamStore.created.SourceSpace != domain.DataSpaceTeamShared || teamStore.created.Visibility != domain.DatasetVisibilityTeam {
		t.Fatalf("team definition trusted caller-owned infrastructure scope: %+v", teamStore.created)
	}
	if teamStore.created.ID == "" || strings.Contains(teamResponse.Body.String(), "team-b") {
		t.Fatalf("team response/ID invalid: created=%+v body=%s", teamStore.created, teamResponse.Body.String())
	}

	publicDenied := serveDatasetAPI(teamRouter, http.MethodPost, "/api/v1/datasets", `{
		"slug":"s1h-public","name":"S1H public","visibility":"PUBLIC",
		"sourceRelativePath":"labeled/s1h","schemaVersion":"s1h-v1"
	}`)
	if publicDenied.Code != http.StatusForbidden {
		t.Fatalf("tenant admin public create status=%d body=%s", publicDenied.Code, publicDenied.Body.String())
	}

	publicStore := &fakeDatasetCatalog{}
	publicHandler := NewHandler(nil, Options{Datasets: publicStore})
	superAdmin := datasetPrincipal("root-admin", "platform", domain.RoleSuperAdmin)
	publicResponse := serveDatasetAPI(datasetAPIRouter(publicHandler, &superAdmin), http.MethodPost, "/api/v1/datasets", `{
		"slug":"s1h-public","name":"S1H public","visibility":"PUBLIC",
		"sourceRelativePath":"labeled/s1h","schemaVersion":"s1h-v1"
	}`)
	if publicResponse.Code != http.StatusCreated || publicStore.created.SourceSpace != domain.DataSpacePublic || publicStore.created.OwnerTenantID != "" {
		t.Fatalf("superadmin public create status=%d created=%+v body=%s", publicResponse.Code, publicStore.created, publicResponse.Body.String())
	}
}

func TestDatasetAuthorizationRejectsInternalPrefixAndOrdinaryUserManagement(t *testing.T) {
	store := &fakeDatasetCatalog{}
	handler := NewHandler(nil, Options{Datasets: store})
	engineer := datasetPrincipal("engineer-a", "team-a", domain.RoleEngineer)
	engineerResponse := serveDatasetAPI(datasetAPIRouter(handler, &engineer), http.MethodPost, "/api/v1/datasets", `{
		"slug":"blocked","name":"Blocked","visibility":"TEAM","sourceRelativePath":"datasets/blocked","schemaVersion":"s1h-v1"
	}`)
	if engineerResponse.Code != http.StatusForbidden {
		t.Fatalf("engineer create status=%d body=%s", engineerResponse.Code, engineerResponse.Body.String())
	}

	teamAdmin := datasetPrincipal("team-admin", "team-a", domain.RoleTenantAdmin)
	for _, path := range []string{"ray-train/platform/datasets/private", "tos://bucket/private", "../private"} {
		body := `{"slug":"blocked","name":"Blocked","visibility":"TEAM","sourceRelativePath":"` + path + `","schemaVersion":"s1h-v1"}`
		response := serveDatasetAPI(datasetAPIRouter(handler, &teamAdmin), http.MethodPost, "/api/v1/datasets", body)
		if response.Code != http.StatusBadRequest || store.created.ID != "" {
			t.Fatalf("internal path %q status=%d created=%+v body=%s", path, response.Code, store.created, response.Body.String())
		}
	}

	customPrefixStore := &fakeDatasetCatalog{}
	customPrefixHandler := NewHandler(nil, Options{Datasets: customPrefixStore, DatasetInternalPrefix: "private/platform/datasets"})
	customPrefix := serveDatasetAPI(datasetAPIRouter(customPrefixHandler, &teamAdmin), http.MethodPost, "/api/v1/datasets", `{
		"slug":"blocked-custom","name":"Blocked custom","visibility":"TEAM",
		"sourceRelativePath":"private/platform/datasets/escape","schemaVersion":"s1h-v1"
	}`)
	if customPrefix.Code != http.StatusBadRequest || customPrefixStore.created.ID != "" {
		t.Fatalf("custom internal prefix status=%d created=%+v body=%s", customPrefix.Code, customPrefixStore.created, customPrefix.Body.String())
	}
}

func TestDatasetAuthorizationControlsPublicationDeprecationAndGlobalGCDryRun(t *testing.T) {
	teamDataset := datasetForAPI("team-a-data", string(domain.DatasetVisibilityTeam), "team-a")
	publicDataset := datasetForAPI("public-data", string(domain.DatasetVisibilityPublic), "")
	ready := readyDatasetVersionForAPI(teamDataset.ID, "version-ready")
	store := &fakeDatasetCatalog{datasets: []domain.Dataset{teamDataset, publicDataset}, versions: []domain.DatasetVersion{ready}, transitionedVersion: func() domain.DatasetVersion {
		result := ready
		result.State = domain.DatasetVersionDeprecated
		return result
	}()}
	publications := &fakeDatasetPublicationManager{run: domain.DatasetPublicationRun{
		ID: "publication-1", DatasetID: teamDataset.ID, DatasetVersionID: "version-discovering", State: domain.DatasetVersionDiscovering,
	}, gcVersions: []domain.DatasetVersion{ready}}
	handler := NewHandler(nil, Options{Datasets: store, DatasetPublications: publications})

	teamAdmin := datasetPrincipal("team-admin", "team-a", domain.RoleTenantAdmin)
	teamRouter := datasetAPIRouter(handler, &teamAdmin)
	publication := serveDatasetAPI(teamRouter, http.MethodPost, "/api/v1/datasets/team-a-data/publications", "{}")
	if publication.Code != http.StatusAccepted || publications.dataset.ID != teamDataset.ID || publications.requestedBy != "team-admin" {
		t.Fatalf("publication status=%d dataset=%+v requester=%q body=%s", publication.Code, publications.dataset, publications.requestedBy, publication.Body.String())
	}
	deprecate := serveDatasetAPI(teamRouter, http.MethodPost, "/api/v1/datasets/team-a-data/versions/version-ready/deprecate", "{}")
	if deprecate.Code != http.StatusOK || store.transitionNext != domain.DatasetVersionDeprecated || store.transitionDatasetID != teamDataset.ID || store.transitionVersionID != ready.ID {
		t.Fatalf("deprecate status=%d transition=%s/%s->%s body=%s", deprecate.Code, store.transitionDatasetID, store.transitionVersionID, store.transitionNext, deprecate.Body.String())
	}
	gcDenied := serveDatasetAPI(teamRouter, http.MethodPost, "/api/v1/datasets/gc/dry-run", "{}")
	if gcDenied.Code != http.StatusForbidden || publications.gcCalled {
		t.Fatalf("tenant GC status=%d called=%t body=%s", gcDenied.Code, publications.gcCalled, gcDenied.Body.String())
	}

	superAdmin := datasetPrincipal("root-admin", "platform", domain.RoleSuperAdmin)
	gc := serveDatasetAPI(datasetAPIRouter(handler, &superAdmin), http.MethodPost, "/api/v1/datasets/gc/dry-run", "{}")
	if gc.Code != http.StatusOK || !publications.gcCalled || strings.Contains(gc.Body.String(), ready.ManifestObjectKey) {
		t.Fatalf("superadmin GC status=%d called=%t body=%s", gc.Code, publications.gcCalled, gc.Body.String())
	}
}

func TestDatasetAPIMapsConflictsAndUnavailablePublisherWithoutLeakingErrors(t *testing.T) {
	teamDataset := datasetForAPI("team-a-data", string(domain.DatasetVisibilityTeam), "team-a")
	store := &fakeDatasetCatalog{datasets: []domain.Dataset{teamDataset}, createErr: repositories.ErrDatasetConflict}
	teamAdmin := datasetPrincipal("team-admin", "team-a", domain.RoleTenantAdmin)
	handler := NewHandler(nil, Options{Datasets: store})
	router := datasetAPIRouter(handler, &teamAdmin)

	conflict := serveDatasetAPI(router, http.MethodPost, "/api/v1/datasets", `{
		"slug":"duplicate","name":"Duplicate","visibility":"TEAM","sourceRelativePath":"datasets/duplicate","schemaVersion":"s1h-v1"
	}`)
	if conflict.Code != http.StatusConflict {
		t.Fatalf("conflict status=%d body=%s", conflict.Code, conflict.Body.String())
	}
	unavailable := serveDatasetAPI(router, http.MethodPost, "/api/v1/datasets/team-a-data/publications", "{}")
	if unavailable.Code != http.StatusServiceUnavailable || strings.Contains(unavailable.Body.String(), "nil") {
		t.Fatalf("publisher unavailable status=%d body=%s", unavailable.Code, unavailable.Body.String())
	}

	publications := &fakeDatasetPublicationManager{err: errors.New("TOS secret for internal-bucket was rejected")}
	failingHandler := NewHandler(nil, Options{Datasets: store, DatasetPublications: publications})
	failure := serveDatasetAPI(datasetAPIRouter(failingHandler, &teamAdmin), http.MethodPost, "/api/v1/datasets/team-a-data/publications", "{}")
	if failure.Code != http.StatusServiceUnavailable || strings.Contains(failure.Body.String(), "internal-bucket") || strings.Contains(failure.Body.String(), "TOS secret") {
		t.Fatalf("publisher failure leaked infrastructure detail: status=%d body=%s", failure.Code, failure.Body.String())
	}

	mismatched := &fakeDatasetPublicationManager{run: domain.DatasetPublicationRun{
		ID: "publication-mismatch", DatasetID: "different-dataset", DatasetVersionID: "version-discovering", State: domain.DatasetVersionDiscovering,
	}}
	mismatch := serveDatasetAPI(datasetAPIRouter(NewHandler(nil, Options{Datasets: store, DatasetPublications: mismatched}), &teamAdmin), http.MethodPost, "/api/v1/datasets/team-a-data/publications", "{}")
	if mismatch.Code != http.StatusServiceUnavailable || strings.Contains(mismatch.Body.String(), "different-dataset") {
		t.Fatalf("publisher mismatch status=%d body=%s", mismatch.Code, mismatch.Body.String())
	}
}

func TestDatasetAPILookupFailuresUseSafeStatusAndMessages(t *testing.T) {
	principal := datasetPrincipal("engineer-a", "team-a", domain.RoleEngineer)
	dataset := datasetForAPI("public-data", string(domain.DatasetVisibilityPublic), "")
	version := readyDatasetVersionForAPI(dataset.ID, "version-ready")

	for _, test := range []struct {
		name  string
		store *fakeDatasetCatalog
		path  string
		want  int
	}{
		{name: "list failure", store: &fakeDatasetCatalog{listErr: errors.New("database password leaked")}, path: "/api/v1/datasets", want: http.StatusInternalServerError},
		{name: "dataset found", store: &fakeDatasetCatalog{datasets: []domain.Dataset{dataset}}, path: "/api/v1/datasets/public-data", want: http.StatusOK},
		{name: "dataset missing", store: &fakeDatasetCatalog{}, path: "/api/v1/datasets/missing", want: http.StatusNotFound},
		{name: "dataset failure", store: &fakeDatasetCatalog{getErr: errors.New("database password leaked")}, path: "/api/v1/datasets/public-data", want: http.StatusInternalServerError},
		{name: "versions dataset missing", store: &fakeDatasetCatalog{listVersionsErr: repositories.ErrDatasetNotFound}, path: "/api/v1/datasets/missing/versions", want: http.StatusNotFound},
		{name: "versions failure", store: &fakeDatasetCatalog{listVersionsErr: errors.New("database password leaked")}, path: "/api/v1/datasets/public-data/versions", want: http.StatusInternalServerError},
		{name: "version missing", store: &fakeDatasetCatalog{getVersionErr: repositories.ErrDatasetVersionNotFound}, path: "/api/v1/datasets/public-data/versions/missing", want: http.StatusNotFound},
		{name: "version failure", store: &fakeDatasetCatalog{getVersionErr: errors.New("database password leaked")}, path: "/api/v1/datasets/public-data/versions/version-ready", want: http.StatusInternalServerError},
		{name: "latest missing", store: &fakeDatasetCatalog{resolveErr: repositories.ErrDatasetVersionNotReady}, path: "/api/v1/datasets/public-data/versions/latest", want: http.StatusNotFound},
		{name: "latest failure", store: &fakeDatasetCatalog{resolveErr: errors.New("database password leaked")}, path: "/api/v1/datasets/public-data/versions/latest", want: http.StatusInternalServerError},
		{name: "version found", store: &fakeDatasetCatalog{datasets: []domain.Dataset{dataset}, versions: []domain.DatasetVersion{version}}, path: "/api/v1/datasets/public-data/versions/version-ready", want: http.StatusOK},
	} {
		t.Run(test.name, func(t *testing.T) {
			handler := NewHandler(nil, Options{Datasets: test.store})
			response := serveDatasetAPI(datasetAPIRouter(handler, &principal), http.MethodGet, test.path, "")
			if response.Code != test.want || strings.Contains(response.Body.String(), "database password") {
				t.Fatalf("status=%d want=%d body=%s", response.Code, test.want, response.Body.String())
			}
		})
	}

	missingCatalog := NewHandler(nil, Options{})
	unavailable := serveDatasetAPI(datasetAPIRouter(missingCatalog, &principal), http.MethodGet, "/api/v1/datasets", "")
	if unavailable.Code != http.StatusServiceUnavailable {
		t.Fatalf("missing catalog status=%d body=%s", unavailable.Code, unavailable.Body.String())
	}
}

func TestDatasetAuthorizationMapsManagementFailuresWithoutSideEffects(t *testing.T) {
	dataset := datasetForAPI("team-a-data", string(domain.DatasetVisibilityTeam), "team-a")
	ready := readyDatasetVersionForAPI(dataset.ID, "version-ready")
	teamAdmin := datasetPrincipal("team-admin", "team-a", domain.RoleTenantAdmin)

	invalidVisibilityStore := &fakeDatasetCatalog{}
	invalidVisibility := serveDatasetAPI(datasetAPIRouter(NewHandler(nil, Options{Datasets: invalidVisibilityStore}), &teamAdmin), http.MethodPost, "/api/v1/datasets", `{
		"slug":"bad","name":"Bad","visibility":"PRIVATE","sourceRelativePath":"datasets/bad","schemaVersion":"s1h-v1"
	}`)
	if invalidVisibility.Code != http.StatusBadRequest || invalidVisibilityStore.created.ID != "" {
		t.Fatalf("invalid visibility status=%d created=%+v body=%s", invalidVisibility.Code, invalidVisibilityStore.created, invalidVisibility.Body.String())
	}

	invalidDatasetStore := &fakeDatasetCatalog{}
	invalidDataset := serveDatasetAPI(datasetAPIRouter(NewHandler(nil, Options{Datasets: invalidDatasetStore}), &teamAdmin), http.MethodPost, "/api/v1/datasets", `{
		"slug":"Invalid Slug","name":"Bad","visibility":"TEAM","sourceRelativePath":"datasets/bad","schemaVersion":"s1h-v1"
	}`)
	if invalidDataset.Code != http.StatusBadRequest || invalidDatasetStore.created.ID != "" {
		t.Fatalf("invalid dataset status=%d created=%+v body=%s", invalidDataset.Code, invalidDatasetStore.created, invalidDataset.Body.String())
	}

	failedCreateStore := &fakeDatasetCatalog{createErr: errors.New("database password leaked")}
	failedCreate := serveDatasetAPI(datasetAPIRouter(NewHandler(nil, Options{Datasets: failedCreateStore}), &teamAdmin), http.MethodPost, "/api/v1/datasets", `{
		"slug":"valid","name":"Valid","visibility":"TEAM","sourceRelativePath":"datasets/valid","schemaVersion":"s1h-v1"
	}`)
	if failedCreate.Code != http.StatusInternalServerError || strings.Contains(failedCreate.Body.String(), "database password") {
		t.Fatalf("failed create status=%d body=%s", failedCreate.Code, failedCreate.Body.String())
	}

	engineer := datasetPrincipal("engineer-a", "team-a", domain.RoleEngineer)
	engineerStore := &fakeDatasetCatalog{datasets: []domain.Dataset{dataset}}
	engineerPublication := serveDatasetAPI(datasetAPIRouter(NewHandler(nil, Options{Datasets: engineerStore}), &engineer), http.MethodPost, "/api/v1/datasets/team-a-data/publications", "{}")
	if engineerPublication.Code != http.StatusForbidden {
		t.Fatalf("engineer publication status=%d body=%s", engineerPublication.Code, engineerPublication.Body.String())
	}

	for _, test := range []struct {
		name string
		err  error
		want int
	}{
		{name: "missing", err: repositories.ErrDatasetVersionNotFound, want: http.StatusNotFound},
		{name: "state conflict", err: repositories.ErrDatasetVersionConflict, want: http.StatusConflict},
		{name: "storage failure", err: errors.New("database password leaked"), want: http.StatusInternalServerError},
	} {
		t.Run("deprecate "+test.name, func(t *testing.T) {
			store := &fakeDatasetCatalog{datasets: []domain.Dataset{dataset}, versions: []domain.DatasetVersion{ready}, transitionErr: test.err}
			response := serveDatasetAPI(datasetAPIRouter(NewHandler(nil, Options{Datasets: store}), &teamAdmin), http.MethodPost, "/api/v1/datasets/team-a-data/versions/version-ready/deprecate", "{}")
			if response.Code != test.want || strings.Contains(response.Body.String(), "database password") {
				t.Fatalf("status=%d want=%d body=%s", response.Code, test.want, response.Body.String())
			}
		})
	}

	superAdmin := datasetPrincipal("root-admin", "platform", domain.RoleSuperAdmin)
	gcUnavailable := serveDatasetAPI(datasetAPIRouter(NewHandler(nil, Options{Datasets: &fakeDatasetCatalog{}}), &superAdmin), http.MethodPost, "/api/v1/datasets/gc/dry-run", "{}")
	if gcUnavailable.Code != http.StatusServiceUnavailable {
		t.Fatalf("GC without manager status=%d body=%s", gcUnavailable.Code, gcUnavailable.Body.String())
	}
	gcManager := &fakeDatasetPublicationManager{err: errors.New("TOS secret leaked")}
	gcFailure := serveDatasetAPI(datasetAPIRouter(NewHandler(nil, Options{Datasets: &fakeDatasetCatalog{}, DatasetPublications: gcManager}), &superAdmin), http.MethodPost, "/api/v1/datasets/gc/dry-run", "{}")
	if gcFailure.Code != http.StatusServiceUnavailable || strings.Contains(gcFailure.Body.String(), "TOS secret") {
		t.Fatalf("GC failure status=%d body=%s", gcFailure.Code, gcFailure.Body.String())
	}
}

func TestDatasetPublicationReadRouteReturnsOnlyAggregateProgress(t *testing.T) {
	dataset := datasetForAPI("dataset-team-a", "TEAM", "team-a")
	store := &fakeDatasetCatalog{datasets: []domain.Dataset{dataset}, publicationRun: domain.DatasetPublicationRun{
		ID: "publication-1", DatasetID: dataset.ID, DatasetVersionID: "version-1", State: domain.DatasetVersionValidating,
		TotalPartitions: 256, CompletedPartitions: 16, SourceObjectCount: 1000, ProcessedObjectCount: 100,
	}}
	principal := datasetPrincipal("admin-a", "team-a", domain.RoleTenantAdmin)
	response := serveDatasetAPI(datasetAPIRouter(NewHandler(nil, Options{Datasets: store}), &principal), http.MethodGet, "/api/v1/datasets/dataset-team-a/versions/version-1/publication", "")
	if response.Code != http.StatusOK {
		t.Fatalf("publication response status=%d body=%s", response.Code, response.Body.String())
	}
	for _, forbidden := range []string{"sourceRoot", "objectKey", "credential", "jobName"} {
		if strings.Contains(response.Body.String(), forbidden) {
			t.Fatalf("publication API leaked %q: %s", forbidden, response.Body.String())
		}
	}
	if !strings.Contains(response.Body.String(), `"completedPartitions":16`) {
		t.Fatalf("publication response omitted progress: %s", response.Body.String())
	}
}
