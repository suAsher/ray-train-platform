package api

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	eb "ray-train-platform-backend/environmentbuild"
	"ray-train-platform-backend/registryauth"
)

func TestEnvironmentRegistryProjectListingFailureOffersManualTarget(t *testing.T) {
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest("GET", "/", nil)
	environmentBuildHandler{}.result(ctx, nil, registryauth.ErrProjectsUnavailable)
	if recorder.Code != 503 || !strings.Contains(recorder.Body.String(), "REGISTRY_PROJECTS_UNAVAILABLE") || !strings.Contains(recorder.Body.String(), "手动输入") || strings.Contains(recorder.Body.String(), "凭据失效") {
		t.Fatalf("misleading project API response: %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestEnvironmentRegistryNetworkFailureIsAvailabilityResponse(t *testing.T) {
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest("GET", "/", nil)
	environmentBuildHandler{}.result(ctx, nil, eb.ErrUnavailable)
	if recorder.Code != 503 || strings.Contains(recorder.Body.String(), "REGISTRY_AUTHORIZATION_REQUIRED") {
		t.Fatalf("network outage reported as invalid password: %d %s", recorder.Code, recorder.Body.String())
	}
}
