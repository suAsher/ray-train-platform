package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
	"ray-train-platform-backend/k8s"
)

func (s *administrativeWorkspaceStore) BeginWorkspaceStop(_ context.Context, id, tenantID string) (*domain.DevWorkspace, error) {
	if id != s.workspace.ID || (tenantID != "" && tenantID != s.workspace.TenantID) {
		return nil, errors.New("workspace not found")
	}
	if s.workspace.State != domain.WorkspaceStopped {
		s.workspace.State = domain.WorkspaceStopping
	}
	copy := s.workspace
	return &copy, nil
}

func (s *administrativeWorkspaceStore) WithWorkspaceOperation(ctx context.Context, id, tenantID string, fn func(*domain.DevWorkspace, func(domain.WorkspaceState) error) error) error {
	if id != s.workspace.ID || (tenantID != "" && tenantID != s.workspace.TenantID) {
		return errors.New("workspace not found")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	before := s.workspace
	err := fn(&before, func(state domain.WorkspaceState) error {
		if err := s.UpdateWorkspaceStateByID(ctx, id, state); err != nil {
			return err
		}
		s.workspace.State = state
		return nil
	})
	if err != nil {
		s.workspace = before
	}
	return err
}

func (s *administrativeWorkspaceStore) ApplyWorkspaceObservation(_ context.Context, id string, previous, next domain.WorkspaceState) (bool, error) {
	if id != s.workspace.ID || s.workspace.State != previous || (previous != domain.WorkspaceSubmitted && previous != domain.WorkspaceRunning) {
		return false, nil
	}
	s.workspace.State = next
	return true, nil
}

func TestWorkspaceStopPersistsIntentBeforeDeleteAndKeepsItOnFailure(t *testing.T) {
	for _, path := range []string{"/api/v1/admin/dev-workspaces/ws-target", "/api/v1/dev-workspaces/me"} {
		router, store, _, dynamic, _ := administrativeWorkspaceRouter(auth.Principal{Subject: "owner", TenantID: "team-a", Roles: []string{domain.RoleSuperAdmin}})
		store.workspace.State = domain.WorkspaceSubmitted
		dynamic.PrependReactor("delete", "rayclusters", func(k8stesting.Action) (bool, runtime.Object, error) {
			if store.workspace.State != domain.WorkspaceStopping {
				t.Errorf("delete before durable stop intent: %s", store.workspace.State)
			}
			return true, nil, errors.New("transient delete failure")
		})
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodDelete, path, nil))
		if response.Code != http.StatusBadGateway || store.workspace.State != domain.WorkspaceStopping {
			t.Fatalf("%s lost retriable intent: %d state=%s body=%s", path, response.Code, store.workspace.State, response.Body.String())
		}
		dynamic.ReactionChain = nil
		// Re-add the tracker through a fresh one-shot reactor, then retry the same API.
		dynamic.PrependReactor("*", "*", k8stesting.ObjectReaction(dynamic.Tracker()))
		retry := httptest.NewRecorder()
		router.ServeHTTP(retry, httptest.NewRequest(http.MethodDelete, path, nil))
		if retry.Code != http.StatusAccepted || store.workspace.State != domain.WorkspaceStopped {
			t.Fatalf("stop retry failed: %d %s", retry.Code, retry.Body.String())
		}
	}
}

func TestWorkspaceGetCannotReviveStopIntent(t *testing.T) {
	for _, state := range []domain.WorkspaceState{domain.WorkspaceStopping, domain.WorkspaceStopped} {
		router, store, _, dynamic, _ := administrativeWorkspaceRouter(auth.Principal{Subject: "owner", TenantID: "team-a", Roles: []string{domain.RoleEngineer}})
		store.workspace.State = state
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/dev-workspaces/me", nil))
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"state":"`+string(state)+`"`) || len(dynamic.Actions()) != 0 {
			t.Fatalf("GET revived stopping workspace: %d %s", response.Code, response.Body.String())
		}
	}
}

func TestWorkspaceGetObservationLosingToStopReturnsPersistedState(t *testing.T) {
	router, store, _, dynamic, _ := administrativeWorkspaceRouter(auth.Principal{Subject: "owner", TenantID: "team-a", Roles: []string{domain.RoleEngineer}})
	dynamic.PrependReactor("get", "rayclusters", func(k8stesting.Action) (bool, runtime.Object, error) {
		store.workspace.State = domain.WorkspaceStopping
		return false, nil, nil
	})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/dev-workspaces/me", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"state":"STOPPING"`) {
		t.Fatalf("stale GET observation won over stop: %d %s", response.Code, response.Body.String())
	}
}

type canceledWorkspaceLaunchStore struct{ administrativeWorkspaceStore }

func (s *canceledWorkspaceLaunchStore) CreateWorkspace(_ context.Context, workspace *domain.DevWorkspace, _ int64) error {
	s.workspace = *workspace
	s.workspace.State = domain.WorkspaceStopping
	return nil
}

func TestWorkspaceLaunchDoesNotCreateResourcesAfterStopIntent(t *testing.T) {
	store := &canceledWorkspaceLaunchStore{}
	dynamic := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
	core := k8sfake.NewSimpleClientset()
	handler := NewHandler(&fakeJobRepository{}, Options{Workspaces: store, Kubernetes: k8s.NewClientFromInterfaces(dynamic, core), WorkspaceImage: "registry.example/workspace@sha256:" + strings.Repeat("a", 64), RayVersion: "2.35.0"})
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("ray-platform-principal", auth.Principal{Subject: "owner", TenantID: "team-a", Roles: []string{domain.RoleEngineer}})
		c.Next()
	})
	handler.RegisterWorkspaceRoutes(router.Group("/api/v1"))
	request := httptest.NewRequest(http.MethodPost, "/api/v1/dev-workspaces", strings.NewReader(`{"gpuCount":0}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "WORKSPACE_LAUNCH_CANCELLED") {
		t.Fatalf("canceled launch accepted: %d %s", response.Code, response.Body.String())
	}
	if len(dynamic.Actions()) != 0 || len(core.Actions()) != 0 {
		t.Fatal("late creator reached Kubernetes after committed stop intent")
	}
}
