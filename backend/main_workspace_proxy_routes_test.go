package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"ray-train-platform-backend/api"
	"ray-train-platform-backend/config"
)

func TestWorkspaceProxyTokenReachesResourceScopedVerifierBeforeOAuth(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	jobs := api.NewHandler(&mainJobRepository{}, api.Options{
		WorkspacePepper: []byte(strings.Repeat("p", 32)),
	})
	registerAPIRoutesWithLocalAuth(router, jobs, nil, nil, nil, nil, nil, nil, nil, nil, nil, config.Config{
		OAuth2ProxyAuthEnabled: true,
	})

	request := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/dev-workspaces/ws-1/proxy/?access_token=invalid&subject=user-1",
		nil,
	)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if !strings.Contains(response.Body.String(), "WORKSPACE_ACCESS_INVALID") {
		t.Fatalf("workspace token verifier was bypassed: status=%d body=%s", response.Code, response.Body.String())
	}
}
