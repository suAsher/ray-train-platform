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

	"github.com/gin-gonic/gin"
	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
	"ray-train-platform-backend/repositories"
)

type fakePATManagementStore struct {
	ensured           []auth.Principal
	created           []domain.PersonalAccessToken
	createdDigest     string
	items             []domain.PersonalAccessToken
	listTenant        string
	listUser          string
	revokeTenant      string
	revokeUser        string
	revokeID          string
	createErr         error
	listErr           error
	revokeErr         error
	ensureIdentityErr error
}

func (s *fakePATManagementStore) EnsureIdentity(_ context.Context, principal auth.Principal) error {
	s.ensured = append(s.ensured, principal)
	return s.ensureIdentityErr
}

func (s *fakePATManagementStore) CreatePersonalAccessToken(_ context.Context, token domain.PersonalAccessToken, digest string) error {
	s.created = append(s.created, token)
	s.createdDigest = digest
	return s.createErr
}

func (s *fakePATManagementStore) ListPersonalAccessTokens(_ context.Context, tenantID, userID string) ([]domain.PersonalAccessToken, error) {
	s.listTenant, s.listUser = tenantID, userID
	return append([]domain.PersonalAccessToken(nil), s.items...), s.listErr
}

func (s *fakePATManagementStore) RevokePersonalAccessToken(_ context.Context, tenantID, userID, tokenID string, _ time.Time) error {
	s.revokeTenant, s.revokeUser, s.revokeID = tenantID, userID, tokenID
	return s.revokeErr
}

func newPATAPIRouter(t *testing.T, store *fakePATManagementStore, principal *auth.Principal, allowDemo bool, now time.Time) *gin.Engine {
	t.Helper()
	handler, err := NewPersonalAccessTokenHandler(store, PersonalAccessTokenOptions{
		Pepper:            []byte("0123456789abcdef0123456789abcdef"),
		DefaultExpiryDays: 90,
		MaxExpiryDays:     365,
		AllowDemo:         allowDemo,
		Now:               func() time.Time { return now },
		NewID:             func() (string, error) { return "pat-fixed", nil },
	})
	if err != nil {
		t.Fatalf("new PAT handler: %v", err)
	}
	gin.SetMode(gin.TestMode)
	router := gin.New()
	if principal != nil {
		identity := *principal
		router.Use(func(c *gin.Context) {
			c.Set("ray-platform-principal", identity)
			c.Next()
		})
	}
	handler.RegisterRoutes(router.Group("/api/v1"))
	return router
}

func performPATRequest(router http.Handler, method, path, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

func oidcPATPrincipal() auth.Principal {
	return auth.Principal{Subject: "user-a", Username: "engineer", TenantID: "tenant-a", Roles: []string{"Engineer"}, AuthType: auth.AuthTypeOIDC}
}

func TestCreatePersonalAccessTokenUsesDefaultsAndReturnsPlaintextOnce(t *testing.T) {
	now := time.Date(2026, 8, 10, 8, 0, 0, 0, time.UTC)
	store := &fakePATManagementStore{}
	principal := oidcPATPrincipal()
	router := newPATAPIRouter(t, store, &principal, false, now)

	response := performPATRequest(router, http.MethodPost, "/api/v1/personal-access-tokens", `{}`)
	if response.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d", response.Code)
	}
	if len(store.ensured) != 1 || len(store.created) != 1 {
		t.Fatalf("expected identity and token persistence, ensured=%d created=%d", len(store.ensured), len(store.created))
	}
	created := store.created[0]
	if !created.ExpiresAt.Equal(now.Add(90*24*time.Hour)) || strings.Join(created.Scopes, ",") != "jobs:read,jobs:write,sources:write" {
		t.Fatalf("unexpected PAT metadata: expiry=%s scopes=%v", created.ExpiresAt, created.Scopes)
	}
	var envelope struct {
		Success bool `json:"success"`
		Data    struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode POST response: %v", err)
	}
	if !envelope.Success || !strings.HasPrefix(envelope.Data.Token, "rpt_") {
		t.Fatal("POST response did not contain a successful one-time token payload")
	}
	if strings.Contains(response.Body.String(), store.createdDigest) {
		t.Fatal("POST response leaked PAT digest")
	}

	store.items = append(store.items, created)
	listResponse := performPATRequest(router, http.MethodGet, "/api/v1/personal-access-tokens", "")
	if listResponse.Code != http.StatusOK {
		t.Fatalf("expected list success, got %d", listResponse.Code)
	}
	if strings.Contains(listResponse.Body.String(), envelope.Data.Token) || strings.Contains(listResponse.Body.String(), store.createdDigest) || strings.Contains(listResponse.Body.String(), `"token"`) {
		t.Fatal("list response leaked plaintext token or digest")
	}
}

func TestPersonalAccessTokenRoutesRejectPATPrincipals(t *testing.T) {
	now := time.Date(2026, 8, 10, 8, 0, 0, 0, time.UTC)
	store := &fakePATManagementStore{}
	principal := oidcPATPrincipal()
	principal.AuthType = auth.AuthTypePAT
	principal.Scopes = []string{domain.PATScopeJobsRead, domain.PATScopeJobsWrite, domain.PATScopeSourcesWrite}
	router := newPATAPIRouter(t, store, &principal, false, now)

	requests := []struct{ method, path, body string }{
		{http.MethodPost, "/api/v1/personal-access-tokens", `{}`},
		{http.MethodGet, "/api/v1/personal-access-tokens", ""},
		{http.MethodDelete, "/api/v1/personal-access-tokens/pat-1", ""},
	}
	for _, request := range requests {
		response := performPATRequest(router, request.method, request.path, request.body)
		if response.Code != http.StatusForbidden {
			t.Fatalf("%s %s expected 403, got %d", request.method, request.path, response.Code)
		}
	}
	if len(store.created) != 0 || store.listTenant != "" || store.revokeID != "" {
		t.Fatal("PAT principal reached PAT management store")
	}
}

func TestPersonalAccessTokenRoutesUseCurrentOwnerAndHideCrossOwnerRevocation(t *testing.T) {
	now := time.Date(2026, 8, 10, 8, 0, 0, 0, time.UTC)
	store := &fakePATManagementStore{revokeErr: repositories.ErrPersonalAccessTokenNotFound}
	principal := oidcPATPrincipal()
	router := newPATAPIRouter(t, store, &principal, false, now)

	listResponse := performPATRequest(router, http.MethodGet, "/api/v1/personal-access-tokens", "")
	if listResponse.Code != http.StatusOK || store.listTenant != "tenant-a" || store.listUser != "user-a" {
		t.Fatalf("list did not use authenticated owner: %d %q/%q", listResponse.Code, store.listTenant, store.listUser)
	}
	deleteResponse := performPATRequest(router, http.MethodDelete, "/api/v1/personal-access-tokens/another-owner-token", "")
	if deleteResponse.Code != http.StatusNotFound || store.revokeTenant != "tenant-a" || store.revokeUser != "user-a" {
		t.Fatalf("revoke isolation failed: status=%d owner=%q/%q", deleteResponse.Code, store.revokeTenant, store.revokeUser)
	}
}

func TestCreatePersonalAccessTokenValidatesExpiryAndScopes(t *testing.T) {
	now := time.Date(2026, 8, 10, 8, 0, 0, 0, time.UTC)
	principal := oidcPATPrincipal()
	tests := []struct {
		name string
		body string
		want int
	}{
		{name: "minimum", body: `{"expiresInDays":1,"scopes":["jobs:read"]}`, want: http.StatusCreated},
		{name: "maximum", body: `{"expiresInDays":365,"scopes":["jobs:write"]}`, want: http.StatusCreated},
		{name: "zero", body: `{"expiresInDays":0}`, want: http.StatusBadRequest},
		{name: "above maximum", body: `{"expiresInDays":366}`, want: http.StatusBadRequest},
		{name: "unknown scope", body: `{"scopes":["admin:all"]}`, want: http.StatusBadRequest},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &fakePATManagementStore{}
			router := newPATAPIRouter(t, store, &principal, false, now)
			response := performPATRequest(router, http.MethodPost, "/api/v1/personal-access-tokens", test.body)
			if response.Code != test.want {
				t.Fatalf("expected %d, got %d", test.want, response.Code)
			}
		})
	}
}

func TestPersonalAccessTokenRoutesAllowDemoOnlyWhenConfigured(t *testing.T) {
	now := time.Date(2026, 8, 10, 8, 0, 0, 0, time.UTC)
	store := &fakePATManagementStore{}
	allowed := newPATAPIRouter(t, store, nil, true, now)
	if response := performPATRequest(allowed, http.MethodPost, "/api/v1/personal-access-tokens", `{}`); response.Code != http.StatusCreated {
		t.Fatalf("configured demo request expected 201, got %d", response.Code)
	}
	if len(store.created) != 1 || store.created[0].TenantID != "local" || store.created[0].UserID != "local-user" {
		t.Fatalf("unexpected demo token owner count=%d", len(store.created))
	}

	denied := newPATAPIRouter(t, &fakePATManagementStore{}, nil, false, now)
	if response := performPATRequest(denied, http.MethodPost, "/api/v1/personal-access-tokens", `{}`); response.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous non-demo request expected 401, got %d", response.Code)
	}
}

func TestPersonalAccessTokenAPIHidesInternalStoreErrors(t *testing.T) {
	now := time.Date(2026, 8, 10, 8, 0, 0, 0, time.UTC)
	store := &fakePATManagementStore{listErr: errors.New("database password secret-detail")}
	principal := oidcPATPrincipal()
	response := performPATRequest(newPATAPIRouter(t, store, &principal, false, now), http.MethodGet, "/api/v1/personal-access-tokens", "")
	if response.Code != http.StatusInternalServerError || strings.Contains(response.Body.String(), "secret-detail") {
		t.Fatalf("internal error leaked or wrong status: %d", response.Code)
	}
}
