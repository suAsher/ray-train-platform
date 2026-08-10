package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"ray-train-platform-backend/api"
	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
)

type requestIDPATStore struct{}

func (requestIDPATStore) EnsureIdentity(context.Context, auth.Principal) error { return nil }
func (requestIDPATStore) CreatePersonalAccessToken(context.Context, domain.PersonalAccessToken, string) error {
	return nil
}
func (requestIDPATStore) ListPersonalAccessTokens(context.Context, string, string) ([]domain.PersonalAccessToken, error) {
	return nil, nil
}
func (requestIDPATStore) RevokePersonalAccessToken(context.Context, string, string, string, time.Time) error {
	return nil
}

type requestIDEnvelope struct {
	RequestID string `json:"request_id"`
}

func TestRequestIDMiddlewareKeepsAuthenticationErrorIDConsistent(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(requestIDMiddleware(), auth.HybridMiddleware(nil, nil, true))
	router.GET("/protected", func(c *gin.Context) { c.Status(http.StatusNoContent) })

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/protected", nil))
	assertResponseRequestID(t, response)
}

func TestRequestIDMiddlewareKeepsPATAPIResponseIDConsistent(t *testing.T) {
	handler, err := api.NewPersonalAccessTokenHandler(requestIDPATStore{}, api.PersonalAccessTokenOptions{
		Pepper: []byte("0123456789abcdef0123456789abcdef"), DefaultExpiryDays: 90,
		MaxExpiryDays: 365, AllowDemo: true,
	})
	if err != nil {
		t.Fatalf("new PAT handler: %v", err)
	}
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(requestIDMiddleware(), auth.DemoIdentityMiddleware(true))
	handler.RegisterRoutes(router.Group("/api/v1"))
	request := httptest.NewRequest(http.MethodPost, "/api/v1/personal-access-tokens", strings.NewReader(`{}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("expected PAT creation status 201, got %d", response.Code)
	}
	assertResponseRequestID(t, response)
}

func assertResponseRequestID(t *testing.T, response *httptest.ResponseRecorder) {
	t.Helper()
	var envelope requestIDEnvelope
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode response envelope: %v", err)
	}
	header := response.Header().Get("X-Request-ID")
	if header == "" || envelope.RequestID == "" || header != envelope.RequestID {
		t.Fatalf("request ID mismatch: header=%q envelope=%q", header, envelope.RequestID)
	}
}
