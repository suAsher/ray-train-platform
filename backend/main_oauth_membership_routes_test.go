package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"ray-train-platform-backend/api"
	"ray-train-platform-backend/config"
)

func TestOAuthOnlyDeploymentKeepsMembershipGovernanceRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	jobs := api.NewHandler(&mainJobRepository{}, api.Options{AllowAnonymous: true})
	locals := api.NewLocalAuthHandler(api.LocalAuthOptions{Enabled: false})
	registerAPIRoutesWithLocalAuth(router, jobs, nil, nil, locals, nil, nil, nil, nil, nil, nil, config.Config{DemoMode: true, LocalAuthEnabled: false})

	for _, request := range []*http.Request{
		httptest.NewRequest(http.MethodGet, "/api/v1/local-users", nil),
		httptest.NewRequest(http.MethodPost, "/api/v1/local-users", nil),
	} {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code == http.StatusNotFound {
			t.Fatalf("membership governance route disappeared with local auth disabled: %s %s", request.Method, request.URL.Path)
		}
	}
}
