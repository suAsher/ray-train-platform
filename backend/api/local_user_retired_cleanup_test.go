package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
	"ray-train-platform-backend/repositories"
)

type retiredAccountStore struct {
	*fakeLocalAuthStore
	cleaned    bool
	lookupErr  error
	cleanupErr error
	target     *domain.LocalUser
}

func (s *retiredAccountStore) FindLocalUserByID(context.Context, string) (domain.LocalUser, error) {
	return domain.LocalUser{}, repositories.ErrLocalUserNotFound
}
func (s *retiredAccountStore) FindRetiredLocalUserByID(context.Context, string) (domain.LocalUser, error) {
	if s.lookupErr != nil {
		return domain.LocalUser{}, s.lookupErr
	}
	if s.target != nil {
		return *s.target, nil
	}
	return domain.LocalUser{ID: "old-user", TenantID: "old-team", Roles: []string{domain.RoleEngineer}}, nil
}
func (s *retiredAccountStore) DecommissionRetiredLocalUser(context.Context, string, time.Time) error {
	s.cleaned = true
	return s.cleanupErr
}

func TestRetiredAccountCleanupFailureResponsesAndProtectedTargets(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		lookupErr, cleanupErr error
		target                *domain.LocalUser
		want                  int
		code                  string
		called                bool
	}{
		{name: "lookup failure", lookupErr: errors.New("private database detail"), want: 500, code: "LOCAL_USER_LOOKUP_FAILED"},
		{name: "not retired", lookupErr: repositories.ErrLocalUserNotFound, want: 404, code: "LOCAL_USER_NOT_FOUND"},
		{name: "active workload", cleanupErr: repositories.ErrLocalUserActiveWorkloads, want: 409, code: "USER_HAS_ACTIVE_WORKLOADS", called: true},
		{name: "concurrently removed", cleanupErr: repositories.ErrLocalUserNotFound, want: 404, code: "LOCAL_USER_NOT_FOUND", called: true},
		{name: "cleanup failure", cleanupErr: errors.New("private database detail"), want: 500, code: "USER_DECOMMISSION_FAILED", called: true},
		{name: "self", target: &domain.LocalUser{ID: superAdminPrincipal().Subject, Roles: []string{domain.RoleEngineer}}, want: 403, code: "USER_NOT_MANAGEABLE"},
		{name: "superadmin target", target: &domain.LocalUser{ID: "protected", Roles: []string{domain.RoleSuperAdmin}}, want: 403, code: "USER_NOT_MANAGEABLE"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &retiredAccountStore{fakeLocalAuthStore: newFakeLocalAuthStore(), lookupErr: tc.lookupErr, cleanupErr: tc.cleanupErr, target: tc.target}
			router := localUserAdminRouter(localAuthHandler(store), superAdminPrincipal())
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodDelete, "/api/v1/local-users/old-user", nil))
			if response.Code != tc.want || store.cleaned != tc.called || !strings.Contains(response.Body.String(), tc.code) {
				t.Fatalf("status=%d cleaned=%v body=%s", response.Code, store.cleaned, response.Body.String())
			}
			if strings.Contains(response.Body.String(), "private database detail") || len(store.auditActions) != 0 {
				t.Fatalf("error leaked or success audit on failure: %s", response.Body.String())
			}
		})
	}
}

func TestRetiredAccountCleanupRequiresInteractiveSuperAdmin(t *testing.T) {
	for _, tc := range []struct {
		name, role string
		authType   auth.AuthenticationType
		want       int
	}{
		{"local super", domain.RoleSuperAdmin, auth.AuthTypeLocal, http.StatusOK},
		{"oidc super", domain.RoleSuperAdmin, auth.AuthTypeOIDC, http.StatusOK},
		{"pat super", domain.RoleSuperAdmin, auth.AuthTypePAT, http.StatusNotFound},
		{"team admin", domain.RoleTenantAdmin, auth.AuthTypeLocal, http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &retiredAccountStore{fakeLocalAuthStore: newFakeLocalAuthStore()}
			principal := superAdminPrincipal()
			principal.Roles, principal.AuthType = []string{tc.role}, tc.authType
			router := localUserAdminRouter(localAuthHandler(store), principal)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodDelete, "/api/v1/local-users/old-user", nil))
			if response.Code != tc.want || store.cleaned != (tc.want == http.StatusOK) {
				t.Fatalf("status=%d cleaned=%v body=%s", response.Code, store.cleaned, response.Body.String())
			}
		})
	}
}

func TestRetiredAccountCleanupDoesNotOpenOtherMutations(t *testing.T) {
	for _, action := range []string{"enable", "reset-password", "roles", "storage-quota"} {
		store := &retiredAccountStore{fakeLocalAuthStore: newFakeLocalAuthStore()}
		router := localUserAdminRouter(localAuthHandler(store), superAdminPrincipal())
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/local-users/old-user/"+action, nil))
		if response.Code != http.StatusNotFound || store.cleaned {
			t.Fatalf("%s: status=%d cleaned=%v", action, response.Code, store.cleaned)
		}
	}
}
