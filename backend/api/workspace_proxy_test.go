package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
)

type fakeWorkspaceStore struct {
	workspace domain.DevWorkspace
}

func (s *fakeWorkspaceStore) CreateWorkspace(context.Context, *domain.DevWorkspace, int64) error {
	return nil
}
func (s *fakeWorkspaceStore) GetWorkspace(context.Context, string, string) (*domain.DevWorkspace, error) {
	copy := s.workspace
	return &copy, nil
}
func (s *fakeWorkspaceStore) GetWorkspaceByUser(context.Context, string) (*domain.DevWorkspace, error) {
	copy := s.workspace
	return &copy, nil
}
func (s *fakeWorkspaceStore) UpdateWorkspaceState(context.Context, string, string, domain.WorkspaceState) error {
	return nil
}

func workspaceProxyRouter(t *testing.T, authenticated bool) (*gin.Engine, *Handler) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	store := &fakeWorkspaceStore{workspace: domain.DevWorkspace{
		ID: "ws-1", TenantID: "team-a", UserID: "user-1",
		Name: "dev-1", Namespace: "tenant-team-a", RayClusterName: "dev-1",
		JupyterURL: "/api/v1/dev-workspaces/ws-1/proxy/",
	}}
	handler := NewHandler(&fakeJobRepository{}, Options{
		Workspaces: store, WorkspacePepper: []byte(strings.Repeat("p", 32)),
	})
	router := gin.New()
	router.Use(func(c *gin.Context) {
		if authenticated {
			c.Set("ray-platform-principal", auth.Principal{
				Subject: "user-1", TenantID: "team-a", Roles: []string{domain.RoleEngineer}, AuthType: auth.AuthTypeLocal,
			})
		}
		c.Next()
	})
	v1 := router.Group("/api/v1")
	handler.RegisterWorkspaceRoutes(v1)
	handler.RegisterWorkspaceProxyRoute(v1)
	return router, handler
}

// A browser tab opened with target="_blank" cannot send an Authorization
// header, so an unauthenticated navigation to the proxy must be rejected —
// this is the 401 users hit when the Portal links straight to the proxy URL.
func TestWorkspaceProxyRejectsPlainNavigation(t *testing.T) {
	router, _ := workspaceProxyRouter(t, false)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/dev-workspaces/ws-1/proxy/", nil))
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without any credential, got %d", response.Code)
	}
}

func TestWorkspaceAccessEndpointReturnsOpenableURL(t *testing.T) {
	router, _ := workspaceProxyRouter(t, true)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/dev-workspaces/ws-1/access", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), "access_token=") {
		t.Fatalf("the returned URL must carry a token: %s", response.Body.String())
	}
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("a URL containing a credential must not be cached")
	}
}

// The token in the query string is exchanged for a cookie so that JupyterLab's
// sub-resource requests, which carry no query parameters, stay authorised.
// The authorisation decision is exercised directly: driving the full route
// would dial a Jupyter service that does not exist in a unit test.
func TestWorkspaceProxySetsPathScopedCookieFromQueryToken(t *testing.T) {
	_, handler := workspaceProxyRouter(t, false)
	token, err := domain.IssueWorkspaceAccessToken("ws-1", "user-1", handler.workspacePepper, time.Now(), time.Minute)
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/dev-workspaces/ws-1/proxy/?access_token="+token+"&subject=user-1", nil)
	c.Params = gin.Params{{Key: "id", Value: "ws-1"}}

	subject, ok := handler.workspacePrincipal(c)
	if !ok || subject != "user-1" {
		t.Fatalf("a valid access token must authorise the proxy, got subject=%q ok=%v", subject, ok)
	}

	var cookie *http.Cookie
	for _, candidate := range recorder.Result().Cookies() {
		if candidate.Name == workspaceAccessCookie {
			cookie = candidate
		}
	}
	if cookie == nil {
		t.Fatalf("expected the access cookie to be installed")
	}
	if cookie.Path != "/api/v1/dev-workspaces/ws-1/proxy/" {
		t.Fatalf("cookie must be scoped to this workspace's proxy path, got %q", cookie.Path)
	}
	if !cookie.HttpOnly {
		t.Fatalf("the access cookie must be HttpOnly")
	}
}

func TestWorkspaceProxyRejectsTokenForAnotherWorkspace(t *testing.T) {
	_, handler := workspaceProxyRouter(t, false)
	token, _ := domain.IssueWorkspaceAccessToken("ws-other", "user-1", handler.workspacePepper, time.Now(), time.Minute)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/dev-workspaces/ws-1/proxy/?access_token="+token+"&subject=user-1", nil)
	c.Params = gin.Params{{Key: "id", Value: "ws-1"}}

	if _, ok := handler.workspacePrincipal(c); ok {
		t.Fatalf("a token minted for another workspace must be rejected")
	}
}

func TestWorkspaceProxyAcceptsCookieOnFollowUpRequests(t *testing.T) {
	_, handler := workspaceProxyRouter(t, false)
	token, _ := domain.IssueWorkspaceAccessToken("ws-1", "user-1", handler.workspacePepper, time.Now(), time.Minute)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/dev-workspaces/ws-1/proxy/static/main.js", nil)
	c.Request.AddCookie(&http.Cookie{Name: workspaceAccessCookie, Value: token})
	c.Request.AddCookie(&http.Cookie{Name: workspaceAccessCookie + "_subject", Value: "user-1"})
	c.Params = gin.Params{{Key: "id", Value: "ws-1"}}

	subject, ok := handler.workspacePrincipal(c)
	if !ok || subject != "user-1" {
		t.Fatalf("sub-resource requests must be authorised by the cookie alone")
	}
}

// JupyterLab serves under the proxy path, so the prefix must survive and the
// one-time credentials must not be forwarded upstream. This runs through real
// HTTP servers because the reverse proxy needs a CloseNotifier-capable writer.
func TestWorkspaceProxyForwardsBasePathAndStripsCredentials(t *testing.T) {
	seen := make(chan *http.Request, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.Clone(context.Background())
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	_, handler := workspaceProxyRouter(t, true)
	handler.workspaceUpstream = func(*domain.DevWorkspace) string { return upstream.URL }

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("ray-platform-principal", auth.Principal{Subject: "user-1", TenantID: "team-a", AuthType: auth.AuthTypeLocal})
		c.Next()
	})
	handler.RegisterWorkspaceProxyRoute(router.Group("/api/v1"))
	portal := httptest.NewServer(router)
	defer portal.Close()

	response, err := http.Get(portal.URL + "/api/v1/dev-workspaces/ws-1/proxy/lab?access_token=secret&subject=user-1&keep=1")
	if err != nil {
		t.Fatalf("request through proxy: %v", err)
	}
	defer response.Body.Close()

	forwarded := <-seen
	if forwarded.URL.Path != "/api/v1/dev-workspaces/ws-1/proxy/lab" {
		t.Fatalf("upstream must receive the full proxy path, got %q", forwarded.URL.Path)
	}
	query := forwarded.URL.Query()
	if query.Get("access_token") != "" || query.Get("subject") != "" {
		t.Fatalf("credentials must not be forwarded upstream, got %q", forwarded.URL.RawQuery)
	}
	if query.Get("keep") != "1" {
		t.Fatalf("ordinary query parameters must survive, got %q", forwarded.URL.RawQuery)
	}
}
