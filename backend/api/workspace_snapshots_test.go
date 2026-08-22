package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
	"ray-train-platform-backend/objectstore"
	"ray-train-platform-backend/repositories"
)

type fakeWorkspaceSnapshotRepository struct {
	items   []domain.WorkspaceSnapshot
	created *domain.WorkspaceSnapshot
}

func (repository *fakeWorkspaceSnapshotRepository) CreateWorkspaceSnapshot(_ context.Context, snapshot domain.WorkspaceSnapshot) error {
	copy := snapshot
	repository.created = &copy
	repository.items = append(repository.items, copy)
	return nil
}

func (repository *fakeWorkspaceSnapshotRepository) ListWorkspaceSnapshots(_ context.Context, tenantID, userID string, _ int) ([]domain.WorkspaceSnapshot, error) {
	items := make([]domain.WorkspaceSnapshot, 0, len(repository.items))
	for _, item := range repository.items {
		if item.TenantID == tenantID && item.UserID == userID {
			items = append(items, item)
		}
	}
	return items, nil
}

func (repository *fakeWorkspaceSnapshotRepository) GetWorkspaceSnapshot(_ context.Context, tenantID, userID, id string) (*domain.WorkspaceSnapshot, error) {
	for _, item := range repository.items {
		if item.ID == id && item.TenantID == tenantID && item.UserID == userID {
			copy := item
			return &copy, nil
		}
	}
	return nil, repositories.ErrWorkspaceSnapshotNotFound
}

type fakeWorkspaceSnapshotStore struct {
	workspaceRoot string
	sourcePath    string
	snapshotRoot  string
}

func (store *fakeWorkspaceSnapshotStore) SnapshotWorkspace(_ context.Context, workspaceRoot, sourcePath, snapshotRoot string) (int, error) {
	store.workspaceRoot, store.sourcePath, store.snapshotRoot = workspaceRoot, sourcePath, snapshotRoot
	return 2, nil
}

func workspaceSnapshotRouter(handler *Handler, principal auth.Principal) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) { c.Set("ray-platform-principal", principal); c.Next() })
	handler.RegisterWorkspaceSnapshotRoutes(router.Group("/api/v1"))
	return router
}

func TestWorkspaceSnapshotsCreateOwnerScopedImmutableCopy(t *testing.T) {
	repository := &fakeWorkspaceSnapshotRepository{}
	store := &fakeWorkspaceSnapshotStore{}
	handler := NewHandler(&fakeJobRepository{}, Options{WorkspaceSnapshots: repository, WorkspaceSnapshotStore: store})
	handler.newID = func() (string, error) { return "job-fixed", nil }
	router := workspaceSnapshotRouter(handler, auth.Principal{Subject: "user-a", TenantID: "team-a", Roles: []string{domain.RoleEngineer}, AuthType: auth.AuthTypeLocal})
	request := httptest.NewRequest(http.MethodPost, "/api/v1/workspace-snapshots", strings.NewReader(`{"sourcePath":"project"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusCreated || repository.created == nil || repository.created.ID != "snapshot-fixed" {
		t.Fatalf("status=%d snapshot=%#v body=%s", response.Code, repository.created, response.Body.String())
	}
	if store.workspaceRoot != "ray-train/tenants/team-a/users/user-a/workspace/" || store.sourcePath != "project" || store.snapshotRoot != "ray-train/tenants/team-a/users/user-a/snapshots/snapshot-fixed/" {
		t.Fatalf("unsafe copy inputs: %#v", store)
	}
	for _, forbidden := range []string{"ray-train/", "snapshotRoot", "claimName"} {
		if strings.Contains(response.Body.String(), forbidden) {
			t.Fatalf("response leaked %q: %s", forbidden, response.Body.String())
		}
	}
}

func TestWorkspaceSnapshotsRejectPathTraversal(t *testing.T) {
	handler := NewHandler(&fakeJobRepository{}, Options{WorkspaceSnapshots: &fakeWorkspaceSnapshotRepository{}, WorkspaceSnapshotStore: &fakeWorkspaceSnapshotStore{}})
	router := workspaceSnapshotRouter(handler, auth.Principal{Subject: "user-a", TenantID: "team-a", Roles: []string{domain.RoleEngineer}, AuthType: auth.AuthTypeLocal})
	request := httptest.NewRequest(http.MethodPost, "/api/v1/workspace-snapshots", strings.NewReader(`{"sourcePath":"../other"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "INVALID_WORKSPACE_SOURCE_PATH") {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

var _ objectstore.WorkspaceSnapshotStore = (*fakeWorkspaceSnapshotStore)(nil)
