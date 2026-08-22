package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
	platformk8s "ray-train-platform-backend/k8s"
)

func TestGetJobRuntimeReturnsOnlyTheAuthenticatedTenantsPods(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repository := &fakeJobRepository{jobs: []domain.TrainingJob{{
		ID: "job-01", TenantID: "team-a", KubernetesNS: "tenant-team-a",
	}}}
	kube := k8sfake.NewSimpleClientset(&corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "ray-head", Namespace: "tenant-team-a",
			Labels: map[string]string{"platform_job_id": "job-01", "ray.io/node-type": "head"},
		},
		Spec:   corev1.PodSpec{NodeName: "gpu-node-a"},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	})
	handler := NewHandler(repository, Options{
		Kubernetes: platformk8s.NewClientFromInterfaces(nil, kube),
	})
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("ray-platform-principal", auth.Principal{
			Subject: "subject-a", TenantID: "team-a", Roles: []string{"Engineer"}, AuthType: auth.AuthTypeOIDC,
		})
		c.Next()
	})
	handler.RegisterTrainingRoutes(router.Group("/api/v1"))

	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/jobs/job-01/runtime", nil)
	router.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("expected runtime response, got %d: %s", response.Code, response.Body.String())
	}
	if body := response.Body.String(); !strings.Contains(body, "ray-head") || strings.Contains(body, "team-b") {
		t.Fatalf("unexpected runtime response body: %s", body)
	}
}
