package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
	"ray-train-platform-backend/objectstore"
	"ray-train-platform-backend/repositories"
)

type fencedLocalAuthStore struct {
	*fakeLocalAuthStore
	active bool
	reject bool
	tenant string
	t      *testing.T
}

func (s *fencedLocalAuthStore) WithActiveTenantWrite(_ context.Context, tenant string, fn func() error) error {
	s.tenant = tenant
	if s.reject {
		return repositories.ErrTenantRetirementBlocked
	}
	s.active = true
	defer func() { s.active = false }()
	return fn()
}
func (s *fencedLocalAuthStore) EnsureIdentity(ctx context.Context, p auth.Principal) error {
	if !s.active {
		s.t.Error("identity write outside target fence")
	}
	return s.fakeLocalAuthStore.EnsureIdentity(ctx, p)
}
func (s *fencedLocalAuthStore) CreateLocalUser(ctx context.Context, u domain.LocalUser) error {
	if !s.active {
		s.t.Error("local user write outside target fence")
	}
	return s.fakeLocalAuthStore.CreateLocalUser(ctx, u)
}
func (s *fencedLocalAuthStore) SetLocalUserPassword(ctx context.Context, userID, hash string) error {
	if !s.active {
		s.t.Error("password write outside target fence")
	}
	return s.fakeLocalAuthStore.SetLocalUserPassword(ctx, userID, hash)
}
func (s *fencedLocalAuthStore) SetLocalUserRoles(ctx context.Context, userID string, roles []string) error {
	if !s.active {
		s.t.Error("role write outside target fence")
	}
	return s.fakeLocalAuthStore.SetLocalUserRoles(ctx, userID, roles)
}
func (s *fencedLocalAuthStore) SetLocalUserDisabled(ctx context.Context, userID string, disabled bool) error {
	if !s.active {
		s.t.Error("state write outside target fence")
	}
	return s.fakeLocalAuthStore.SetLocalUserDisabled(ctx, userID, disabled)
}
func (s *fencedLocalAuthStore) RevokeAllLocalSessions(ctx context.Context, userID string, revokedAt time.Time) error {
	if !s.active {
		s.t.Error("session revoke outside target fence")
	}
	return s.fakeLocalAuthStore.RevokeAllLocalSessions(ctx, userID, revokedAt)
}
func (s *fencedLocalAuthStore) DecommissionLocalUser(ctx context.Context, userID string, now time.Time) error {
	if !s.active {
		s.t.Error("decommission outside target fence")
	}
	return s.fakeLocalAuthStore.SetLocalUserDisabled(ctx, userID, true)
}

type fencedPersonalStorage struct {
	fakePersonalStorageQuotaManager
	store *fencedLocalAuthStore
}

func (s *fencedPersonalStorage) EnsurePersonalQuota(ctx context.Context, tenant, user string, bytes int64) (objectstore.PersonalStorageQuota, error) {
	if !s.store.active {
		s.store.t.Error("quota creation outside target fence")
	}
	return s.fakePersonalStorageQuotaManager.EnsurePersonalQuota(ctx, tenant, user, bytes)
}
func (s *fencedPersonalStorage) SetPersonalQuota(ctx context.Context, tenant, user string, bytes int64) (objectstore.PersonalStorageQuota, error) {
	if !s.store.active {
		s.store.t.Error("quota update outside target fence")
	}
	return s.fakePersonalStorageQuotaManager.SetPersonalQuota(ctx, tenant, user, bytes)
}

type fencedPersonalInitializer struct {
	store *fencedLocalAuthStore
	calls int
}

func (s *fencedPersonalInitializer) EnsurePersonalDataSpace(context.Context, auth.Principal) error {
	s.calls++
	if !s.store.active {
		s.store.t.Error("personal data creation outside target fence")
	}
	return nil
}

func TestCrossTenantLocalUserSideEffectsHoldTargetFence(t *testing.T) {
	for _, reject := range []bool{false, true} {
		name := "active"
		if reject {
			name = "retired"
		}
		t.Run(name, func(t *testing.T) {
			store := &fencedLocalAuthStore{fakeLocalAuthStore: newFakeLocalAuthStore(), reject: reject, t: t}
			quota := &fencedPersonalStorage{store: store}
			initializer := &fencedPersonalInitializer{store: store}
			handler := localAuthHandlerWithInitializer(store, initializer)
			handler.personalStorageQuotaEnabled = true
			handler.personalStorageQuota = quota
			response := postUser(localUserAdminRouter(handler, superAdminPrincipal()), `{"username":"alice","password":"StrongPass123!","roles":["Engineer"],"tenantId":"team-b","storageQuotaGiB":100}`)
			want := http.StatusCreated
			if reject {
				want = http.StatusForbidden
			}
			if response.Code != want {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			if store.tenant != "team-b" {
				t.Fatalf("fenced tenant=%q want target team-b", store.tenant)
			}
			if reject && (quota.ensuredTenant != "" || initializer.calls != 0 || store.identityCall != 0 || len(store.users) != 0) {
				t.Fatal("retired target triggered side effects")
			}
		})
	}
}

func TestCrossTenantQuotaUpdateHoldsTargetFence(t *testing.T) {
	for _, reject := range []bool{false, true} {
		name := "active"
		if reject {
			name = "retired"
		}
		t.Run(name, func(t *testing.T) {
			store := &fencedLocalAuthStore{fakeLocalAuthStore: newFakeLocalAuthStore(), reject: reject, t: t}
			store.users["alice"] = domain.LocalUser{ID: "user-alice", Username: "alice", TenantID: "team-b", Roles: []string{"Engineer"}}
			quota := &fencedPersonalStorage{store: store}
			handler := localAuthHandler(store)
			handler.personalStorageQuotaEnabled = true
			handler.personalStorageQuota = quota
			response := postUserAction(localUserAdminRouter(handler, superAdminPrincipal()), "/api/v1/local-users/user-alice/storage-quota", `{"storageQuotaGiB":100}`)
			want := http.StatusOK
			if reject {
				want = http.StatusForbidden
			}
			if response.Code != want {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			if store.tenant != "team-b" {
				t.Fatalf("fenced tenant=%q want target team-b", store.tenant)
			}
			if reject && quota.setTenant != "" {
				t.Fatal("retired target changed quota")
			}
		})
	}
}

func TestCrossTenantAccountMutationsHoldTargetFence(t *testing.T) {
	for _, tc := range []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{"password", http.MethodPost, "/api/v1/local-users/user-alice/reset-password", `{"newPassword":"StrongPass456!"}`},
		{"roles", http.MethodPost, "/api/v1/local-users/user-alice/roles", `{"roles":["TenantAdmin"]}`},
		{"disable", http.MethodPost, "/api/v1/local-users/user-alice/disable", `{}`},
		{"enable", http.MethodPost, "/api/v1/local-users/user-alice/enable", `{}`},
		{"decommission", http.MethodDelete, "/api/v1/local-users/user-alice", ``},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, reject := range []bool{false, true} {
				name := "active"
				if reject {
					name = "retired"
				}
				t.Run(name, func(t *testing.T) {
					store := &fencedLocalAuthStore{fakeLocalAuthStore: newFakeLocalAuthStore(), reject: reject, t: t}
					store.users["alice"] = domain.LocalUser{ID: "user-alice", Username: "alice", TenantID: "team-b", Roles: []string{"Engineer"}, PasswordHash: "old-hash"}
					router := localUserAdminRouter(localAuthHandler(store), superAdminPrincipal())
					request := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
					request.Header.Set("Content-Type", "application/json")
					response := httptest.NewRecorder()
					router.ServeHTTP(response, request)
					want := http.StatusOK
					if reject {
						want = http.StatusForbidden
					}
					if response.Code != want {
						t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
					}
					if store.tenant != "team-b" {
						t.Fatalf("fenced tenant=%q want target team-b", store.tenant)
					}
					if reject {
						user := store.users["alice"]
						if user.PasswordHash != "old-hash" || store.disabled["user-alice"] || len(store.revokedAll) != 0 || len(store.auditActions) != 0 {
							t.Fatalf("retired target mutated user=%+v disabled=%v revoked=%v audit=%v", user, store.disabled, store.revokedAll, store.auditActions)
						}
					}
				})
			}
		})
	}
}
