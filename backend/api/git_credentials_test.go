package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
	"ray-train-platform-backend/k8s"
	"ray-train-platform-backend/repositories"
)

type fakeGitCredentialStore struct {
	credentials []domain.GitCredential
}

type fakeGitCredentialProbe struct {
	repositoryURL string
	username      string
	token         string
	err           error
}

func (probe *fakeGitCredentialProbe) Probe(_ context.Context, repositoryURL, username, token string) (GitCredentialTestResult, error) {
	probe.repositoryURL, probe.username, probe.token = repositoryURL, username, token
	if probe.err != nil {
		return GitCredentialTestResult{}, probe.err
	}
	return GitCredentialTestResult{Reachable: true, Authenticated: true, Message: "repository access verified"}, nil
}

func (store *fakeGitCredentialStore) UpsertGitCredential(_ context.Context, credential domain.GitCredential) error {
	store.credentials = append(store.credentials, credential)
	return nil
}

func (store *fakeGitCredentialStore) ListGitCredentials(_ context.Context, tenantID, userID string, scope domain.GitCredentialScope) ([]domain.GitCredential, error) {
	items := make([]domain.GitCredential, 0)
	for _, credential := range store.credentials {
		if credential.TenantID == tenantID && credential.Scope == scope && (scope == domain.GitCredentialScopeTeam || credential.OwnerUserID == userID) {
			items = append(items, credential)
		}
	}
	return items, nil
}

func (store *fakeGitCredentialStore) GetGitCredential(_ context.Context, tenantID, id string) (domain.GitCredential, error) {
	for _, credential := range store.credentials {
		if credential.TenantID == tenantID && credential.ID == id {
			return credential, nil
		}
	}
	return domain.GitCredential{}, repositories.ErrGitCredentialNotFound
}

func (store *fakeGitCredentialStore) DeleteGitCredential(_ context.Context, tenantID, id string) (domain.GitCredential, error) {
	for index, credential := range store.credentials {
		if credential.TenantID == tenantID && credential.ID == id {
			store.credentials = append(store.credentials[:index], store.credentials[index+1:]...)
			return credential, nil
		}
	}
	return domain.GitCredential{}, repositories.ErrGitCredentialNotFound
}

func gitCredentialRouter(handler *Handler, principal auth.Principal) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("ray-platform-principal", principal)
		c.Next()
	})
	handler.RegisterGitCredentialRoutes(router.Group("/api/v1"))
	return router
}

func TestEngineerCreatesPersonalGitCredentialWithoutLeakingSecretReference(t *testing.T) {
	store := &fakeGitCredentialStore{}
	handler := NewHandler(&fakeJobRepository{}, Options{GitCredentials: store, Kubernetes: k8s.NewClientFromInterfaces(nil, k8sfake.NewSimpleClientset())})
	handler.newID = func() (string, error) { return "credential-personal", nil }
	router := gitCredentialRouter(handler, auth.Principal{Subject: "user-a", TenantID: "team-a", Roles: []string{domain.RoleEngineer}})

	request := httptest.NewRequest(http.MethodPost, "/api/v1/git-credentials", strings.NewReader(`{"name":"我的 GitLab","host":"git.example.com","username":"alice","token":"write-only-token"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if len(store.credentials) != 1 || store.credentials[0].Scope != domain.GitCredentialScopePersonal || store.credentials[0].OwnerUserID != "user-a" {
		t.Fatalf("personal credential was not scoped to its owner: %#v", store.credentials)
	}
	for _, forbidden := range []string{"write-only-token", "secretName", store.credentials[0].SecretName} {
		if strings.Contains(response.Body.String(), forbidden) {
			t.Fatalf("credential response leaked %q: %s", forbidden, response.Body.String())
		}
	}
}

func TestGitCredentialRejectsAnUnapprovedHostBeforeWritingItsSecret(t *testing.T) {
	store := &fakeGitCredentialStore{}
	clientset := k8sfake.NewSimpleClientset()
	handler := NewHandler(&fakeJobRepository{}, Options{GitCredentials: store, Kubernetes: k8s.NewClientFromInterfaces(nil, clientset), GitAllowlist: []string{"github.com"}})
	router := gitCredentialRouter(handler, auth.Principal{Subject: "user-a", TenantID: "team-a", Roles: []string{domain.RoleEngineer}})

	request := httptest.NewRequest(http.MethodPost, "/api/v1/git-credentials", strings.NewReader(`{"name":"内网 Git","host":"git.example.com","token":"write-only-token"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "GIT_HOST_NOT_ALLOWED") || len(store.credentials) != 0 {
		t.Fatalf("unapproved Git host must be rejected before persistence: status=%d credentials=%#v body=%s", response.Code, store.credentials, response.Body.String())
	}
}

func TestEngineerCanVerifyApprovedCredentialAgainstAnExactRepositoryWithoutLeakingToken(t *testing.T) {
	store := &fakeGitCredentialStore{}
	clientset := k8sfake.NewSimpleClientset()
	probe := &fakeGitCredentialProbe{}
	handler := NewHandler(&fakeJobRepository{}, Options{
		GitCredentials: store, Kubernetes: k8s.NewClientFromInterfaces(nil, clientset), GitAllowlist: []string{"git.example.com"}, GitCredentialTester: probe,
	})
	handler.newID = func() (string, error) { return "credential-personal", nil }
	router := gitCredentialRouter(handler, auth.Principal{Subject: "user-a", TenantID: "team-a", Roles: []string{domain.RoleEngineer}})

	create := httptest.NewRequest(http.MethodPost, "/api/v1/git-credentials", strings.NewReader(`{"name":"我的 Git","host":"git.example.com","username":"alice","token":"write-only-token"}`))
	create.Header.Set("Content-Type", "application/json")
	createResponse := httptest.NewRecorder()
	router.ServeHTTP(createResponse, create)
	if createResponse.Code != http.StatusCreated {
		t.Fatalf("create credential status=%d body=%s", createResponse.Code, createResponse.Body.String())
	}

	verify := httptest.NewRequest(http.MethodPost, "/api/v1/git-credentials/credential-personal/test", strings.NewReader(`{"repositoryUrl":"https://git.example.com/team/private.git"}`))
	verify.Header.Set("Content-Type", "application/json")
	verifyResponse := httptest.NewRecorder()
	router.ServeHTTP(verifyResponse, verify)

	if verifyResponse.Code != http.StatusOK || probe.repositoryURL != "https://git.example.com/team/private.git" || probe.username != "alice" || probe.token != "write-only-token" {
		t.Fatalf("credential test did not use the private secret safely: status=%d probe=%#v body=%s", verifyResponse.Code, probe, verifyResponse.Body.String())
	}
	if strings.Contains(verifyResponse.Body.String(), "write-only-token") || strings.Contains(verifyResponse.Body.String(), "secretName") {
		t.Fatalf("credential test leaked a secret: %s", verifyResponse.Body.String())
	}
}

func TestEngineerCannotCreateTeamGitCredential(t *testing.T) {
	store := &fakeGitCredentialStore{}
	handler := NewHandler(&fakeJobRepository{}, Options{GitCredentials: store, Kubernetes: k8s.NewClientFromInterfaces(nil, k8sfake.NewSimpleClientset())})
	router := gitCredentialRouter(handler, auth.Principal{Subject: "user-a", TenantID: "team-a", Roles: []string{domain.RoleEngineer}})

	request := httptest.NewRequest(http.MethodPost, "/api/v1/git-credentials", strings.NewReader(`{"name":"团队 GitLab","host":"git.example.com","token":"write-only-token","scope":"team"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusForbidden || len(store.credentials) != 0 {
		t.Fatalf("engineer must not publish a team credential: status=%d credentials=%#v body=%s", response.Code, store.credentials, response.Body.String())
	}
}

func TestEngineerCannotDeleteAnotherUsersPersonalGitCredential(t *testing.T) {
	store := &fakeGitCredentialStore{credentials: []domain.GitCredential{{
		ID: "credential-other", TenantID: "team-a", Scope: domain.GitCredentialScopePersonal, OwnerUserID: "user-b",
		Name: "user-b GitLab", Host: "git.example.com", SecretName: "git-cred-private",
	}}}
	handler := NewHandler(&fakeJobRepository{}, Options{GitCredentials: store, Kubernetes: k8s.NewClientFromInterfaces(nil, k8sfake.NewSimpleClientset())})
	router := gitCredentialRouter(handler, auth.Principal{Subject: "user-a", TenantID: "team-a", Roles: []string{domain.RoleEngineer}})

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodDelete, "/api/v1/git-credentials/credential-other", nil))

	if response.Code != http.StatusForbidden || len(store.credentials) != 1 {
		t.Fatalf("foreign personal credential must remain intact: status=%d credentials=%#v body=%s", response.Code, store.credentials, response.Body.String())
	}
}
