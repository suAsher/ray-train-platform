package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"ray-train-platform-backend/auth"
)

func decodeSession(t *testing.T, body []byte) sessionResponse {
	t.Helper()
	var envelope struct {
		Success bool            `json:"success"`
		Data    sessionResponse `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	if !envelope.Success {
		t.Fatalf("expected successful envelope, got %s", string(body))
	}
	return envelope.Data
}

func sessionRouter(handler *Handler, principal *auth.Principal) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		if principal != nil {
			c.Set("ray-platform-principal", *principal)
		}
		c.Next()
	})
	handler.RegisterSessionRoutes(router.Group("/api/v1"))
	return router
}

func TestCurrentSessionReturnsServerResolvedTenantAndRoles(t *testing.T) {
	handler := NewHandler(&fakeJobRepository{}, Options{})
	principal := auth.Principal{
		Subject: "subject-1", Username: "alice", Email: "alice@example.com",
		TenantID: "team-a", Roles: []string{"Engineer"}, AuthType: auth.AuthTypeOIDC,
	}
	response := httptest.NewRecorder()
	sessionRouter(handler, &principal).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/me", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", response.Code)
	}
	session := decodeSession(t, response.Body.Bytes())
	if session.TenantID != "team-a" || session.Username != "alice" {
		t.Fatalf("unexpected session identity: %+v", session)
	}
	if session.AuthType != string(auth.AuthTypeOIDC) || session.Anonymous {
		t.Fatalf("expected authenticated oidc session, got %+v", session)
	}
	if session.Queue != "team-a-gpu" || session.Namespace != "tenant-team-a" {
		t.Fatalf("expected derived queue and namespace, got %+v", session)
	}
}

func TestCurrentSessionReportsDemoIdentityWhenAnonymousAllowed(t *testing.T) {
	handler := NewHandler(&fakeJobRepository{}, Options{AllowAnonymous: true})
	response := httptest.NewRecorder()
	sessionRouter(handler, nil).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/me", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", response.Code)
	}
	session := decodeSession(t, response.Body.Bytes())
	if session.TenantID != "local" || session.AuthType != string(auth.AuthTypeDemo) {
		t.Fatalf("expected demo principal, got %+v", session)
	}
	if !session.Anonymous {
		t.Fatalf("demo session must be flagged anonymous so the UI can label it")
	}
}

func TestCurrentSessionRequiresAuthenticationWhenAnonymousDisabled(t *testing.T) {
	handler := NewHandler(&fakeJobRepository{}, Options{AllowAnonymous: false})
	response := httptest.NewRecorder()
	sessionRouter(handler, nil).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/me", nil))

	if response.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", response.Code)
	}
}
