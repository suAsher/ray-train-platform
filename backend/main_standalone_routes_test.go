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

func TestStandaloneDoesNotRegisterRetiredCompanyAPIs(t *testing.T) {
	router := gin.New()
	jobs := api.NewHandler(&mainJobRepository{}, api.Options{})
	registerAPIRoutes(router, jobs, nil, nil, nil, nil, nil, config.Config{LocalAuthEnabled: true})
	foundTraining := false
	for _, route := range router.Routes() {
		for _, retired := range []string{"function-warehouse", "storage-sync", "idc-sync", "personal-storage"} {
			if strings.Contains(route.Path, retired) {
				t.Errorf("retired API remains registered: %s %s", route.Method, route.Path)
			}
		}
		if route.Path == "/api/v1/jobs" {
			foundTraining = true
		}
	}
	if !foundTraining {
		t.Fatal("training API was removed with company integrations")
	}
}

func TestStandaloneAnonymousRequestsCannotAccessTrainingStorageOrAdministration(t *testing.T) {
	router := gin.New()
	jobs := api.NewHandler(&mainJobRepository{}, api.Options{})
	registerAPIRoutes(router, jobs, nil, nil, nil, nil, nil, config.Config{LocalAuthEnabled: true, PATEnabled: true})
	for _, path := range []string{"/api/v1/jobs", "/api/v1/storage-assets", "/api/v1/admin/tenants"} {
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			response := httptest.NewRecorder()
			request := httptest.NewRequest(method, path, strings.NewReader(`{}`))
			request.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(response, request)
			if response.Code != http.StatusUnauthorized {
				t.Errorf("anonymous %s %s returned %d, want 401", method, path, response.Code)
			}
		}
	}
}
