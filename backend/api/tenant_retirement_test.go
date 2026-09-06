package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/repositories"
)

type inactiveRetirementActor struct{}

func (inactiveRetirementActor) WithActiveTenantWrite(context.Context, string, func() error) error {
	return errors.New("inactive actor")
}

func TestRetirementAlsoFencesAdministratorTenant(t *testing.T) {
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Request = c.Request.WithContext(auth.SetPrincipalContext(c.Request.Context(), auth.Principal{Subject: "admin", TenantID: "retired-admin"}))
		c.Next()
	}, TenantWriteGuard(inactiveRetirementActor{}))
	router.POST("/api/v1/tenants/:id/retire", func(c *gin.Context) { c.Status(200) })
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("POST", "/api/v1/tenants/other/retire", nil))
	if w.Code != 403 {
		t.Fatalf("inactive administrator can retire another team: %d", w.Code)
	}
}

type inactiveProxyRepository struct{ JobRepository }

func (*inactiveProxyRepository) TenantExists(context.Context, string) (bool, error) {
	return false, nil
}
func TestRetiredTenantCannotUseProxyCredentials(t *testing.T) {
	h := &Handler{repository: &inactiveProxyRepository{}}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/proxy", nil)
	if h.activeProxyTenant(c, "retired") || w.Code != 403 {
		t.Fatal("retired proxy credential accepted")
	}
}

type recordingTenantFence struct {
	active      bool
	readChecks  int
	writeChecks int
}

func (f *recordingTenantFence) TenantExists(context.Context, string) (bool, error) {
	f.readChecks++
	return f.active, nil
}

func (f *recordingTenantFence) WithActiveTenantWrite(_ context.Context, _ string, fn func() error) error {
	f.writeChecks++
	if !f.active {
		return errors.New("inactive")
	}
	return fn()
}

func TestTenantWriteGuardChecksReadsWithoutHoldingWriteFence(t *testing.T) {
	for _, tc := range []struct {
		name, method, route, path string
		active                    bool
		want                      int
		readChecks                int
		writeChecks               int
	}{
		{"active read", http.MethodGet, "/api/v1/jobs", "/api/v1/jobs", true, 200, 0, 1},
		{"inactive read", http.MethodGet, "/api/v1/jobs", "/api/v1/jobs", false, 403, 0, 1},
		{"active write", http.MethodPost, "/api/v1/jobs", "/api/v1/jobs", true, 200, 0, 1},
		{"retirement fences actor", http.MethodPost, "/api/v1/tenants/:id/retire", "/api/v1/tenants/team/retire", true, 200, 0, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fence := &recordingTenantFence{active: tc.active}
			router := gin.New()
			router.Use(func(c *gin.Context) {
				c.Request = c.Request.WithContext(auth.SetPrincipalContext(c.Request.Context(), auth.Principal{Subject: "u", TenantID: "team"}))
				c.Next()
			})
			router.Use(TenantWriteGuard(fence))
			router.Handle(tc.method, tc.route, func(c *gin.Context) { c.Status(http.StatusOK) })
			w := httptest.NewRecorder()
			router.ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, nil))
			if w.Code != tc.want || fence.readChecks != tc.readChecks || fence.writeChecks != tc.writeChecks {
				t.Fatalf("code=%d reads=%d writes=%d", w.Code, fence.readChecks, fence.writeChecks)
			}
		})
	}
}

type fakeRetirementStore struct {
	fakeAdminStore
	called bool
}

func (s *fakeRetirementStore) TenantRetirementPreflight(_ context.Context, id string) (repositories.TenantRetirementPreflight, error) {
	return repositories.TenantRetirementPreflight{TenantID: id, CanRetire: true, Blockers: []string{}, Counts: map[string]int64{}}, nil
}
func (s *fakeRetirementStore) RetireTenant(context.Context, string, string, func(context.Context, string) ([]string, error)) (repositories.TenantRetirementPreflight, error) {
	s.called = true
	return repositories.TenantRetirementPreflight{}, nil
}

func TestTenantRetirementRequiresInteractiveSuperAdmin(t *testing.T) {
	for _, p := range []auth.Principal{
		{Subject: "u", TenantID: "admin", Roles: []string{"SuperAdmin"}, AuthType: auth.AuthTypePAT},
		{Subject: "u", TenantID: "admin", Roles: []string{"TenantAdmin"}, AuthType: auth.AuthTypeLocal},
	} {
		store := &fakeRetirementStore{}
		router := adminRouter(&Handler{admin: store}, p)
		for _, route := range []struct{ method, path string }{{"GET", "/api/v1/tenants/team/retirement-preflight"}, {"POST", "/api/v1/tenants/team/retire"}} {
			w := httptest.NewRecorder()
			router.ServeHTTP(w, httptest.NewRequest(route.method, route.path, strings.NewReader(`{"confirmTenantId":"team"}`)))
			if w.Code != 403 {
				t.Fatalf("%s status=%d %s", route.path, w.Code, w.Body.String())
			}
		}
		if store.called {
			t.Fatal("unauthorized mutation")
		}
	}
}

func TestTenantRetirementFailsClosedAndProtectsCurrentTeam(t *testing.T) {
	store := &fakeRetirementStore{}
	p := auth.Principal{Subject: "u", TenantID: "team", Roles: []string{"SuperAdmin"}, AuthType: auth.AuthTypeLocal}
	router := adminRouter(&Handler{admin: store, bootstrapTenant: "team"}, p)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/tenants/team/retirement-preflight", nil))
	for _, blocker := range []string{"current_tenant", "protected_tenant", "k8s_state_unknown"} {
		if !strings.Contains(w.Body.String(), blocker) {
			t.Fatalf("missing %s: %s", blocker, w.Body.String())
		}
	}
	w = httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("POST", "/api/v1/tenants/team/retire", strings.NewReader(`{"confirmTenantId":"team"}`)))
	if w.Code != 409 || store.called {
		t.Fatalf("blocked retire = %d %s", w.Code, w.Body.String())
	}
}

func TestTenantListHidesRetiredByDefault(t *testing.T) {
	now := time.Now()
	store := &fakeAdminStore{tenants: []repositories.TenantSummary{{ID: "active"}, {ID: "retired", RetiredAt: &now}}}
	router := adminRouter(&Handler{admin: store}, auth.Principal{Subject: "u", TenantID: "admin", Roles: []string{"SuperAdmin"}, AuthType: auth.AuthTypeLocal})
	for _, query := range []string{"", "?includeRetired=true"} {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/tenants"+query, nil))
		has := strings.Contains(w.Body.String(), `"id":"retired"`)
		if has != (query != "") {
			t.Fatalf("query=%s body=%s", query, w.Body.String())
		}
	}
}
