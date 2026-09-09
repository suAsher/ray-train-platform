package main

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"ray-train-platform-backend/api"
	"ray-train-platform-backend/config"
)

func TestCLICompatibilityIsWiredBeforeSubmission(t *testing.T) {
	router := gin.New()
	handler := api.NewHandler(&mainJobRepository{}, api.Options{AllowAnonymous: true})
	registerAPIRoutesWithLocalAuth(router, handler, nil, nil, nil, nil, nil, nil, nil, nil, nil, config.Config{DemoMode: true, SPKRayjobMinimumVersion: "release-20260906-02"})
	for _, version := range []string{"release-20260906-01", "release-20260906-10", ""} {
		request := httptest.NewRequest("POST", "/api/v1/jobs", strings.NewReader("invalid-json"))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-Spk-Rayjob-Version", version)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		want := 400 // Compatible and unidentified callers reach normal input validation.
		if version == "release-20260906-01" {
			want = 426
		}
		if response.Code != want {
			t.Fatalf("version=%q: %d %s", version, response.Code, response.Body.String())
		}
	}
}
