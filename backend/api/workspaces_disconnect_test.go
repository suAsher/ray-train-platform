package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
	"ray-train-platform-backend/k8s"
)

type workspaceRequestContextKey struct{}

type disconnectedWorkspaceLaunchStore struct {
	fakeWorkspaceStore
	disconnect                      context.CancelFunc
	operationErr, recoveryErr       error
	operationCtxErr, recoveryCtxErr error
	requestValuePreserved           bool
	operationBudget, recoveryBudget time.Duration
	stopCalls                       int
	replaceBeforeRecovery           bool
}

func (s *disconnectedWorkspaceLaunchStore) CreateWorkspace(ctx context.Context, workspace *domain.DevWorkspace, ttl int64) error {
	if err := s.fakeWorkspaceStore.CreateWorkspace(ctx, workspace, ttl); err != nil {
		return err
	}
	s.disconnect()
	return nil
}

func (s *disconnectedWorkspaceLaunchStore) WithWorkspaceOperation(ctx context.Context, id, tenant string, operation func(*domain.DevWorkspace, func(domain.WorkspaceState) error) error) error {
	s.operationCtxErr = ctx.Err()
	s.requestValuePreserved = ctx.Value(workspaceRequestContextKey{}) == "request-value"
	if deadline, ok := ctx.Deadline(); ok {
		s.operationBudget = time.Until(deadline)
	}
	if s.operationErr != nil {
		if s.replaceBeforeRecovery {
			s.workspace.ID = "ws-replacement"
			s.workspace.State = domain.WorkspaceSubmitted
		}
		return s.operationErr
	}
	return s.fakeWorkspaceStore.WithWorkspaceOperation(ctx, id, tenant, operation)
}

func (s *disconnectedWorkspaceLaunchStore) BeginWorkspaceStop(ctx context.Context, id, tenant string) (*domain.DevWorkspace, error) {
	s.stopCalls++
	s.recoveryCtxErr = ctx.Err()
	if deadline, ok := ctx.Deadline(); ok {
		s.recoveryBudget = time.Until(deadline)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.recoveryErr != nil {
		return nil, s.recoveryErr
	}
	return s.fakeWorkspaceStore.BeginWorkspaceStop(ctx, id, tenant)
}

func disconnectedWorkspaceLaunch(t *testing.T, store *disconnectedWorkspaceLaunchStore) *httptest.ResponseRecorder {
	t.Helper()
	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), workspaceRequestContextKey{}, "request-value"))
	t.Cleanup(cancel)
	store.disconnect = cancel
	store.getErr = context.Canceled
	dynamic := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
	core := k8sfake.NewSimpleClientset()
	handler := NewHandler(&fakeJobRepository{}, Options{Workspaces: store, Kubernetes: k8s.NewClientFromInterfaces(dynamic, core), WorkspaceImage: "registry.example/workspace@sha256:" + strings.Repeat("a", 64), RayVersion: "2.35.0"})
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("ray-platform-principal", auth.Principal{Subject: "owner", TenantID: "team-a", Roles: []string{domain.RoleEngineer}})
		c.Next()
	})
	handler.RegisterWorkspaceRoutes(router.Group("/api/v1"))
	request := httptest.NewRequest(http.MethodPost, "/api/v1/dev-workspaces", strings.NewReader(`{"gpuCount":0}`)).WithContext(ctx)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

func TestWorkspaceCommittedLaunchSurvivesClientDisconnect(t *testing.T) {
	store := &disconnectedWorkspaceLaunchStore{}
	response := disconnectedWorkspaceLaunch(t, store)
	if response.Code != http.StatusAccepted || store.operationCtxErr != nil || !store.requestValuePreserved || store.operationBudget <= 0 || store.operationBudget > 2*time.Minute || store.stopCalls != 0 {
		t.Fatalf("committed launch abandoned after disconnect: status=%d body=%s ctx=%v budget=%v recovery=%d", response.Code, response.Body.String(), store.operationCtxErr, store.operationBudget, store.stopCalls)
	}
}

func TestWorkspaceLaunchTransactionFailureRecordsRetriableStopIndependently(t *testing.T) {
	for _, failure := range []error{context.DeadlineExceeded, errors.New("transaction commit failed")} {
		store := &disconnectedWorkspaceLaunchStore{operationErr: failure}
		response := disconnectedWorkspaceLaunch(t, store)
		if response.Code != http.StatusInternalServerError || !strings.Contains(response.Body.String(), "WORKSPACE_STATE_FAILED") || store.stopCalls != 1 || store.recoveryCtxErr != nil || store.workspace.State != domain.WorkspaceStopping || store.recoveryBudget <= 0 || store.recoveryBudget > 5*time.Second {
			t.Fatalf("failed launch is stuck or lost quota: status=%d body=%s state=%s recovery=%d err=%v budget=%v", response.Code, response.Body.String(), store.workspace.State, store.stopCalls, store.recoveryCtxErr, store.recoveryBudget)
		}
	}
}

func TestWorkspaceLaunchRecoveryDoesNotChangeReplacementAndReportsFailure(t *testing.T) {
	for _, replace := range []bool{false, true} {
		store := &disconnectedWorkspaceLaunchStore{operationErr: errors.New("transaction failed"), replaceBeforeRecovery: replace}
		if !replace {
			store.recoveryErr = errors.New("database unavailable")
		}
		response := disconnectedWorkspaceLaunch(t, store)
		if response.Code != http.StatusInternalServerError || !strings.Contains(response.Body.String(), "WORKSPACE_RECOVERY_FAILED") || store.stopCalls != 1 {
			t.Fatalf("recovery error was swallowed: %d %s", response.Code, response.Body.String())
		}
		if replace && (store.workspace.ID != "ws-replacement" || store.workspace.State != domain.WorkspaceSubmitted) {
			t.Fatal("stale launch changed replacement workspace")
		}
	}
}
