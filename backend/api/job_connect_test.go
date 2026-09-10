package api

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"golang.org/x/net/websocket"
	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
	platformk8s "ray-train-platform-backend/k8s"
)

type fakeJobWorkerConnector struct {
	target platformk8s.JobWorkerTarget
	calls  int
}

func (f *fakeJobWorkerConnector) ResolveJobWorker(context.Context, string, string, int) (platformk8s.JobWorkerTarget, error) {
	return f.target, nil
}

func (f *fakeJobWorkerConnector) ConnectJobWorker(_ context.Context, _ platformk8s.JobWorkerTarget, _ io.Reader, stdout io.Writer) error {
	f.calls++
	_, _ = io.WriteString(stdout, "worker-ready\n")
	return nil
}

func TestConnectJobWorkerRequiresOwnerAndRunningJob(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, test := range []struct {
		name       string
		owner      string
		state      domain.State
		wantStatus int
	}{
		{name: "other team member", owner: "other", state: domain.StateRunning, wantStatus: http.StatusForbidden},
		{name: "job not running", owner: "user-a", state: domain.StateQueued, wantStatus: http.StatusConflict},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository := &fakeJobRepository{jobs: []domain.TrainingJob{{ID: "job-a", TenantID: "team-a", UserID: test.owner, ObservedState: test.state, KubernetesNS: "tenant-a"}}}
			handler := NewHandler(repository, Options{})
			handler.jobWorkerConnector = &fakeJobWorkerConnector{}
			router := jobConnectRouter(handler)
			response := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, "/api/v1/jobs/job-a/connect?worker=0", nil)
			router.ServeHTTP(response, request)
			if response.Code != test.wantStatus {
				t.Fatalf("expected %d, got %d: %s", test.wantStatus, response.Code, response.Body.String())
			}
		})
	}
}

func TestConnectJobWorkerStreamsFixedServerSideTerminal(t *testing.T) {
	repository := &fakeJobRepository{jobs: []domain.TrainingJob{{ID: "job-a", TenantID: "team-a", UserID: "user-a", ObservedState: domain.StateRunning, KubernetesNS: "tenant-a"}}}
	connector := &fakeJobWorkerConnector{target: platformk8s.JobWorkerTarget{Namespace: "tenant-a", PodName: "worker-a", ContainerName: "ray-worker"}}
	handler := NewHandler(repository, Options{})
	handler.jobWorkerConnector = connector
	server := httptest.NewServer(jobConnectRouter(handler))
	defer server.Close()

	config, err := websocket.NewConfig("ws"+strings.TrimPrefix(server.URL, "http")+"/api/v1/jobs/job-a/connect?worker=0", server.URL)
	if err != nil {
		t.Fatal(err)
	}
	connection, err := websocket.DialConfig(config)
	if err != nil {
		t.Fatalf("dial worker connection: %v", err)
	}
	defer connection.Close()
	contents, err := io.ReadAll(connection)
	if err != nil {
		t.Fatalf("read worker connection: %v", err)
	}
	if string(contents) != "worker-ready\n" || connector.calls != 1 {
		t.Fatalf("unexpected stream %q calls=%d", contents, connector.calls)
	}
}

func jobConnectRouter(handler *Handler) *gin.Engine {
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("ray-platform-principal", auth.Principal{Subject: "user-a", TenantID: "team-a", Roles: []string{domain.RoleEngineer}, AuthType: auth.AuthTypeOIDC})
		c.Next()
	})
	handler.RegisterTrainingRoutes(router.Group("/api/v1"))
	return router
}
