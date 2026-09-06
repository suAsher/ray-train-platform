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

type cleanupCatalog struct {
	fakeDatasetCatalog
	calls                           int
	tenant, dataset, version, actor string
	super, publication              bool
	err                             error
}

func TestDatasetCleanupErrorsAndBounds(t *testing.T) {
	p := auth.Principal{Subject: "admin", TenantID: "team", Roles: []string{domain.RoleSuperAdmin}, AuthType: auth.AuthTypeLocal}
	for _, item := range []struct {
		err    error
		status int
	}{{repositories.ErrDatasetCleanupNotFound, 404}, {repositories.ErrDatasetCleanupConflict, 409}, {errors.New("private database detail"), 500}} {
		s := &cleanupCatalog{err: item.err}
		w := httptest.NewRecorder()
		cleanupRouter(s, &p).ServeHTTP(w, httptest.NewRequest("DELETE", "/api/v1/datasets/dataset/versions/version", nil))
		if w.Code != item.status {
			t.Fatalf("got %d want %d", w.Code, item.status)
		}
		if strings.Contains(w.Body.String(), "private database") {
			t.Fatal("internal error leaked")
		}
	}
	for _, path := range []string{"/datasets/bad%20id/versions/version", "/datasets/dataset/versions/latest"} {
		s := &cleanupCatalog{}
		w := httptest.NewRecorder()
		cleanupRouter(s, &p).ServeHTTP(w, httptest.NewRequest("DELETE", "/api/v1"+path, nil))
		if w.Code != 400 || s.calls != 0 {
			t.Fatal(path, w.Code)
		}
	}
	s := &cleanupCatalog{}
	r := cleanupRouter(s, &p)
	for i := 0; i < 121; i++ {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("DELETE", "/api/v1/datasets/dataset/versions/version", nil))
		want := 200
		if i == 120 {
			want = 429
		}
		if w.Code != want {
			t.Fatal(i, w.Code)
		}
	}
}

func (s *cleanupCatalog) DeleteFailedDatasetRecord(_ context.Context, tenant string, super bool, dataset, version, actor string, publication bool) error {
	s.calls++
	s.tenant = tenant
	s.super = super
	s.dataset = dataset
	s.version = version
	s.actor = actor
	s.publication = publication
	return s.err
}
func cleanupRouter(store *cleanupCatalog, principal *auth.Principal) *gin.Engine {
	r := gin.New()
	if principal != nil {
		r.Use(func(c *gin.Context) { c.Set("ray-platform-principal", *principal) })
	}
	h := NewHandler(nil, Options{Datasets: store})
	h.RegisterDatasetManagementRoutes(r.Group("/api/v1"))
	return r
}
func TestDatasetCleanupRequiresInteractiveAdministrator(t *testing.T) {
	for _, suffix := range []string{"", "/publication"} {
		for _, role := range []string{domain.RoleEngineer, domain.RoleTenantAdmin, domain.RoleSuperAdmin} {
			for _, kind := range []auth.AuthenticationType{auth.AuthTypeLocal, auth.AuthTypeOIDC, auth.AuthTypePAT} {
				s := &cleanupCatalog{}
				p := auth.Principal{Subject: "admin", TenantID: "team", Roles: []string{role}, AuthType: kind}
				w := httptest.NewRecorder()
				cleanupRouter(s, &p).ServeHTTP(w, httptest.NewRequest("DELETE", "/api/v1/datasets/dataset/versions/version"+suffix, nil))
				want := 200
				if role == domain.RoleEngineer || kind == auth.AuthTypePAT {
					want = 403
				}
				if w.Code != want {
					t.Fatalf("%s %s %s: got%d want%d: %s", role, kind, suffix, w.Code, want, w.Body.String())
				}
				if want == 200 {
					if s.calls != 1 || s.tenant != "team" || s.actor != "admin" || s.super != (role == domain.RoleSuperAdmin) || s.publication != (suffix != "") {
						t.Fatalf("bad scope %+v", s)
					}
				} else if s.calls != 0 {
					t.Fatal("unauthorized store call")
				}
			}
		}
	}
	s := &cleanupCatalog{}
	w := httptest.NewRecorder()
	cleanupRouter(s, nil).ServeHTTP(w, httptest.NewRequest("DELETE", "/api/v1/datasets/dataset/versions/version", nil))
	if w.Code != 401 || s.calls != 0 {
		t.Fatal(w.Code)
	}
}
