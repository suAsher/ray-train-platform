package api

import (
	"context"
	"encoding/json"
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

type fakeLocalAuthStore struct {
	users        map[string]domain.LocalUser
	sessions     []domain.LocalSession
	revoked      []string
	revokedAll   []string
	disabled     map[string]bool
	auditActions []string
	identityCall int
	passwordSet  string
}

func newFakeLocalAuthStore() *fakeLocalAuthStore {
	return &fakeLocalAuthStore{users: make(map[string]domain.LocalUser), disabled: make(map[string]bool)}
}

func (s *fakeLocalAuthStore) withUser(t *testing.T, username, password string, disabled bool) *fakeLocalAuthStore {
	t.Helper()
	hash, err := domain.HashPassword(password)
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	s.users[username] = domain.LocalUser{
		ID: "user-" + username, Username: username, TenantID: "team-a",
		Roles: []string{domain.RoleEngineer}, PasswordHash: hash, Disabled: disabled,
	}
	return s
}

func (s *fakeLocalAuthStore) FindLocalUserByUsername(_ context.Context, username string) (domain.LocalUser, error) {
	user, ok := s.users[domain.NormalizeUsername(username)]
	if !ok {
		return domain.LocalUser{}, repositories.ErrLocalUserNotFound
	}
	return user, nil
}
func (s *fakeLocalAuthStore) CreateLocalUser(_ context.Context, user domain.LocalUser) error {
	if _, exists := s.users[user.Username]; exists {
		return repositories.ErrUsernameTaken
	}
	s.users[user.Username] = user
	return nil
}
func (s *fakeLocalAuthStore) CountLocalUsers(context.Context) (int64, error) {
	return int64(len(s.users)), nil
}
func (s *fakeLocalAuthStore) ListLocalUsers(context.Context) ([]domain.LocalUser, error) {
	items := make([]domain.LocalUser, 0, len(s.users))
	for _, user := range s.users {
		items = append(items, user)
	}
	return items, nil
}
func (s *fakeLocalAuthStore) SetLocalUserPassword(_ context.Context, userID, hash string) error {
	s.passwordSet = hash
	for username, user := range s.users {
		if user.ID == userID {
			user.PasswordHash = hash
			s.users[username] = user
			return nil
		}
	}
	return repositories.ErrLocalUserNotFound
}
func (s *fakeLocalAuthStore) CreateLocalSession(_ context.Context, session domain.LocalSession, _ string) error {
	s.sessions = append(s.sessions, session)
	return nil
}
func (s *fakeLocalAuthStore) RevokeLocalSession(_ context.Context, publicID string, _ time.Time) error {
	s.revoked = append(s.revoked, publicID)
	return nil
}
func (s *fakeLocalAuthStore) FindLocalUserByID(_ context.Context, userID string) (domain.LocalUser, error) {
	for _, user := range s.users {
		if user.ID == userID {
			return user, nil
		}
	}
	return domain.LocalUser{}, repositories.ErrLocalUserNotFound
}
func (s *fakeLocalAuthStore) RevokeAllLocalSessions(_ context.Context, userID string, _ time.Time) error {
	s.revokedAll = append(s.revokedAll, userID)
	return nil
}
func (s *fakeLocalAuthStore) SetLocalUserDisabled(_ context.Context, userID string, disabled bool) error {
	for username, user := range s.users {
		if user.ID == userID {
			user.Disabled = disabled
			s.users[username] = user
			s.disabled[userID] = disabled
			return nil
		}
	}
	return repositories.ErrLocalUserNotFound
}
func (s *fakeLocalAuthStore) CreateAuditLog(_ context.Context, action, resourceID string, _ auth.Principal, _ string) error {
	s.auditActions = append(s.auditActions, action+":"+resourceID)
	return nil
}
func (s *fakeLocalAuthStore) EnsureIdentity(context.Context, auth.Principal) error {
	s.identityCall++
	return nil
}

func (s *fakeLocalAuthStore) TenantExists(_ context.Context, tenantID string) (bool, error) {
	return tenantID == "team-a" || tenantID == "team-b" || tenantID == "platform", nil
}

func localAuthHandler(store LocalAuthStore) *LocalAuthHandler {
	return NewLocalAuthHandler(LocalAuthOptions{
		Store: store, Pepper: []byte(strings.Repeat("p", 32)),
		SessionLifetime: time.Hour, Enabled: true,
	})
}

func postLogin(handler *LocalAuthHandler, body string) *httptest.ResponseRecorder {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	handler.RegisterPublicRoutes(router.Group("/api/v1"))
	request := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

func errorCodeOf(t *testing.T, body []byte) string {
	t.Helper()
	var envelope struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("decode error envelope: %v", err)
	}
	return envelope.Error.Code + "|" + envelope.Error.Message
}

func TestLoginIssuesSessionTokenForValidCredentials(t *testing.T) {
	store := newFakeLocalAuthStore().withUser(t, "alice", "correct-horse", false)
	response := postLogin(localAuthHandler(store), `{"username":"alice","password":"correct-horse"}`)

	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", response.Code, response.Body.String())
	}
	var envelope struct {
		Data loginResponse `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !domain.IsLocalSessionToken(envelope.Data.Token) {
		t.Fatalf("expected a local session token, got %q", envelope.Data.Token)
	}
	if envelope.Data.TenantID != "team-a" || len(store.sessions) != 1 {
		t.Fatalf("unexpected login result: %+v sessions=%d", envelope.Data, len(store.sessions))
	}
	if store.identityCall != 1 {
		t.Fatalf("login must provision the tenant and user rows")
	}
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("credential responses must not be cacheable")
	}
}

func TestChangePasswordRevokesEveryLocalSession(t *testing.T) {
	store := newFakeLocalAuthStore().withUser(t, "alice", "correct-horse", false)
	handler := localAuthHandler(store)
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("ray-platform-principal", auth.Principal{Subject: "user-alice", Username: "alice", TenantID: "team-a", Roles: []string{domain.RoleEngineer}, AuthType: auth.AuthTypeLocal})
		c.Next()
	})
	handler.RegisterAuthenticatedRoutes(router.Group("/api/v1"))
	request := httptest.NewRequest(http.MethodPost, "/api/v1/auth/password", strings.NewReader(`{"currentPassword":"correct-horse","newPassword":"new-correct-horse"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("expected password change success, got %d body=%s", response.Code, response.Body.String())
	}
	if len(store.revokedAll) != 1 || store.revokedAll[0] != "user-alice" {
		t.Fatalf("password change must revoke every local session, got %+v", store.revokedAll)
	}
	if !domain.VerifyPassword(store.users["alice"].PasswordHash, "new-correct-horse") {
		t.Fatal("password change did not persist the new bcrypt hash")
	}
	if strings.Contains(response.Body.String(), "new-correct-horse") || strings.Contains(response.Body.String(), "correct-horse") {
		t.Fatalf("password response leaked a password: %s", response.Body.String())
	}
}

func TestChangePasswordRejectsTheCurrentPassword(t *testing.T) {
	store := newFakeLocalAuthStore().withUser(t, "alice", "correct-horse", false)
	handler := localAuthHandler(store)
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("ray-platform-principal", auth.Principal{Subject: "user-alice", Username: "alice", TenantID: "team-a", Roles: []string{domain.RoleEngineer}, AuthType: auth.AuthTypeLocal})
		c.Next()
	})
	handler.RegisterAuthenticatedRoutes(router.Group("/api/v1"))
	request := httptest.NewRequest(http.MethodPost, "/api/v1/auth/password", strings.NewReader(`{"currentPassword":"correct-horse","newPassword":"correct-horse"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("expected current password reuse to be rejected, got %d body=%s", response.Code, response.Body.String())
	}
	if len(store.revokedAll) != 0 {
		t.Fatalf("rejected password reuse must not revoke sessions: %+v", store.revokedAll)
	}
}

func TestLoginRejectsWrongPasswordWithoutRevealingAccountExistence(t *testing.T) {
	store := newFakeLocalAuthStore().withUser(t, "alice", "correct-horse", false)
	handler := localAuthHandler(store)

	wrongPassword := postLogin(handler, `{"username":"alice","password":"nope-nope-nope"}`)
	unknownUser := postLogin(handler, `{"username":"nobody","password":"nope-nope-nope"}`)

	if wrongPassword.Code != http.StatusUnauthorized || unknownUser.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for both, got %d and %d", wrongPassword.Code, unknownUser.Code)
	}
	// Only the per-request id may differ; the error itself must be identical.
	if errorCodeOf(t, wrongPassword.Body.Bytes()) != errorCodeOf(t, unknownUser.Body.Bytes()) {
		t.Fatalf("responses must be indistinguishable:\n%s\n%s", wrongPassword.Body.String(), unknownUser.Body.String())
	}
	if len(store.sessions) != 0 {
		t.Fatalf("a failed login must not create a session")
	}
}

func TestLoginRejectsDisabledAccount(t *testing.T) {
	store := newFakeLocalAuthStore().withUser(t, "alice", "correct-horse", true)
	if response := postLogin(localAuthHandler(store), `{"username":"alice","password":"correct-horse"}`); response.Code != http.StatusUnauthorized {
		t.Fatalf("expected disabled account to be rejected, got %d", response.Code)
	}
}

func TestLoginLocksOutAfterRepeatedFailures(t *testing.T) {
	store := newFakeLocalAuthStore().withUser(t, "alice", "correct-horse", false)
	handler := localAuthHandler(store)
	for attempt := 0; attempt < maxLoginAttempts; attempt++ {
		postLogin(handler, `{"username":"alice","password":"wrong-password"}`)
	}
	// Even the correct password must be refused while the lockout is active.
	response := postLogin(handler, `{"username":"alice","password":"correct-horse"}`)
	if response.Code != http.StatusTooManyRequests {
		t.Fatalf("expected lockout, got %d", response.Code)
	}
}

func TestLoginDisabledReturnsNotFound(t *testing.T) {
	handler := NewLocalAuthHandler(LocalAuthOptions{Store: newFakeLocalAuthStore(), Pepper: []byte(strings.Repeat("p", 32)), Enabled: false})
	if response := postLogin(handler, `{"username":"alice","password":"whatever1"}`); response.Code != http.StatusNotFound {
		t.Fatalf("expected 404 when local login is disabled, got %d", response.Code)
	}
}

func TestEnsureBootstrapAdminCreatesFirstAccountOnlyOnce(t *testing.T) {
	store := newFakeLocalAuthStore()
	created, err := EnsureBootstrapAdmin(context.Background(), store, "admin", "bootstrap-pass", "local")
	if err != nil || !created {
		t.Fatalf("expected bootstrap admin to be created: created=%v err=%v", created, err)
	}
	user := store.users["admin"]
	if user.TenantID != "local" || len(user.Roles) != 1 || user.Roles[0] != domain.RoleSuperAdmin {
		t.Fatalf("unexpected bootstrap admin: %+v", user)
	}
	if !domain.VerifyPassword(user.PasswordHash, "bootstrap-pass") {
		t.Fatalf("bootstrap password must be usable")
	}

	// A restart must not reset an operator-changed password.
	againCreated, err := EnsureBootstrapAdmin(context.Background(), store, "admin", "different-pass", "local")
	if err != nil || againCreated {
		t.Fatalf("expected no second bootstrap: created=%v err=%v", againCreated, err)
	}
	if !domain.VerifyPassword(store.users["admin"].PasswordHash, "bootstrap-pass") {
		t.Fatalf("existing password must be preserved")
	}
}

func TestEnsureBootstrapAdminSkippedWithoutPassword(t *testing.T) {
	store := newFakeLocalAuthStore()
	created, err := EnsureBootstrapAdmin(context.Background(), store, "admin", "", "local")
	if err != nil || created {
		t.Fatalf("expected no bootstrap without a configured password")
	}
}

func (s *fakeLocalAuthStore) SetLocalUserRoles(_ context.Context, userID string, roles []string) error {
	for username, user := range s.users {
		if user.ID == userID {
			user.Roles = append([]string(nil), roles...)
			s.users[username] = user
			return nil
		}
	}
	return repositories.ErrLocalUserNotFound
}
