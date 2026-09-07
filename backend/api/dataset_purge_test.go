package api

import (
	"context"
	"errors"
	"github.com/gin-gonic/gin"
	"net/http/httptest"
	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
	"ray-train-platform-backend/repositories"
	"strings"
	"testing"
)

type purgeCatalog struct {
	fakeDatasetCatalog
	err error
}

func (s *purgeCatalog) PurgeFailedDatasetRecord(ctx context.Context, tenant string, super bool, dataset, version, actor string, purge func(repositories.DatasetPurgePlan) (int, error)) (int, error) {
	if s.err != nil {
		return 0, s.err
	}
	return purge(repositories.DatasetPurgePlan{DatasetID: dataset, VersionID: version})
}

func TestDatasetPurgeReceiptsAndSanitizedFailures(t *testing.T) {
	for _, item := range []struct {
		err    error
		status int
	}{{nil, 200}, {repositories.ErrDatasetCleanupConflict, 409}, {repositories.ErrDatasetCleanupNotFound, 404}, {errors.New("secret bucket token"), 503}} {
		s := &purgeCatalog{err: item.err}
		h := NewHandler(nil, Options{Datasets: s})
		h.ConfigureDatasetPurge(func(context.Context, repositories.DatasetPurgePlan) (int, error) { return 2, nil })
		r := gin.New()
		r.Use(func(c *gin.Context) {
			c.Set("ray-platform-principal", auth.Principal{Subject: "admin", TenantID: "team", Roles: []string{domain.RoleSuperAdmin}, AuthType: auth.AuthTypeLocal})
		})
		h.RegisterDatasetManagementRoutes(r.Group("/api/v1"))
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("DELETE", "/api/v1/datasets/dataset/versions/version/purge", nil))
		if w.Code != item.status || strings.Contains(w.Body.String(), "secret") {
			t.Fatalf("%d %s", w.Code, w.Body.String())
		}
		if item.err == nil && (!strings.Contains(w.Body.String(), `"exclusiveObjectsDeleted":2`) || !strings.Contains(w.Body.String(), `"sharedObjectsRetained":true`)) {
			t.Fatal(w.Body.String())
		}
	}
}

func TestDatasetPurgeDoesNotFallBackToRecordOnlyDelete(t *testing.T) {
	p := auth.Principal{Subject: "admin", TenantID: "team", Roles: []string{domain.RoleSuperAdmin}, AuthType: auth.AuthTypeLocal}
	s := &cleanupCatalog{}
	w := httptest.NewRecorder()
	cleanupRouter(s, &p).ServeHTTP(w, httptest.NewRequest("DELETE", "/api/v1/datasets/dataset/versions/version/purge", nil))
	if w.Code != 503 || s.calls != 0 {
		t.Fatalf("status=%d calls=%d body=%s", w.Code, s.calls, w.Body.String())
	}
}

func TestDatasetPurgeRequiresInteractiveAdministrator(t *testing.T) {
	for _, role := range []string{domain.RoleEngineer, domain.RoleSuperAdmin} {
		p := auth.Principal{Subject: "user", TenantID: "team", Roles: []string{role}, AuthType: auth.AuthTypePAT}
		w := httptest.NewRecorder()
		cleanupRouter(&cleanupCatalog{}, &p).ServeHTTP(w, httptest.NewRequest("DELETE", "/api/v1/datasets/dataset/versions/version/purge", nil))
		if w.Code != 403 {
			t.Fatalf("role=%s status=%d", role, w.Code)
		}
	}
}
