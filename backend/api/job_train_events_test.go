package api

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"ray-train-platform-backend/domain"
	"ray-train-platform-backend/repositories"
)

type fakeManagedTrainingStore struct {
	*fakeJobRepository
	wantJobID string
	wantToken []byte
	result    domain.TrainingEventResult
	err       error
	seen      domain.TrainingEvent
	items     []domain.TrainingCheckpoint
}

func (store *fakeManagedTrainingStore) RecordTrainingEvent(_ context.Context, jobID string, token []byte, event domain.TrainingEvent, _ time.Time) (domain.TrainingEventResult, error) {
	store.wantJobID = jobID
	store.wantToken = append([]byte(nil), token...)
	store.seen = event
	return store.result, store.err
}

func (store *fakeManagedTrainingStore) ListUsableCheckpoints(_ context.Context, tenantID, userID, jobID string) ([]domain.TrainingCheckpoint, error) {
	store.wantJobID = tenantID + "/" + userID + "/" + jobID
	return append([]domain.TrainingCheckpoint(nil), store.items...), store.err
}

func TestTrainEventRejectsAnotherJobsToken(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := &fakeManagedTrainingStore{fakeJobRepository: &fakeJobRepository{}, err: repositories.ErrTrainingEventUnauthorized}
	handler := NewHandler(store, Options{})
	router := gin.New()
	handler.RegisterTrainingEventRoutes(router.Group("/api/v1/internal"))
	token := make([]byte, 32)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/internal/jobs/job-a/train-events", strings.NewReader(`{"eventId":"event-1","type":"WORKER_GROUP_STARTED","generation":2}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+base64.RawURLEncoding.EncodeToString(token))
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", response.Code, response.Body.String())
	}
}

func TestTrainEventAcceptsBoundedPayloadAndReturnsReplayResult(t *testing.T) {
	gin.SetMode(gin.TestMode)
	token := make([]byte, 32)
	for index := range token {
		token[index] = byte(index + 1)
	}
	store := &fakeManagedTrainingStore{fakeJobRepository: &fakeJobRepository{}, result: domain.TrainingEventResult{EventID: "event-1", Replayed: true, WorkerRestartCount: 2}}
	handler := NewHandler(store, Options{})
	router := gin.New()
	handler.RegisterTrainingEventRoutes(router.Group("/api/v1/internal"))
	request := httptest.NewRequest(http.MethodPost, "/api/v1/internal/jobs/job-a/train-events", strings.NewReader(`{"eventId":"event-1","type":"WORKER_GROUP_STARTED","generation":2}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+base64.RawURLEncoding.EncodeToString(token))
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || store.wantJobID != "job-a" || string(store.wantToken) != string(token) || store.seen.Generation != 2 {
		t.Fatalf("unexpected event response=%d body=%s store=%+v", response.Code, response.Body.String(), store)
	}
}

func TestTrainEventRejectsOversizedBodyBeforeRepository(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := &fakeManagedTrainingStore{fakeJobRepository: &fakeJobRepository{}}
	handler := NewHandler(store, Options{})
	router := gin.New()
	handler.RegisterTrainingEventRoutes(router.Group("/api/v1/internal"))
	request := httptest.NewRequest(http.MethodPost, "/api/v1/internal/jobs/job-a/train-events", strings.NewReader(strings.Repeat("x", int(maxTrainingEventBodyBytes+1))))
	request.Header.Set("Authorization", "Bearer "+base64.RawURLEncoding.EncodeToString(make([]byte, 32)))
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusRequestEntityTooLarge || store.seen.ID != "" {
		t.Fatalf("oversized request reached repository: code=%d event=%+v", response.Code, store.seen)
	}
}

func TestTrainEventRejectsInvalidJobIDBeforeRepository(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := &fakeManagedTrainingStore{fakeJobRepository: &fakeJobRepository{}}
	handler := NewHandler(store, Options{})
	router := gin.New()
	handler.RegisterTrainingEventRoutes(router.Group("/api/v1/internal"))
	request := httptest.NewRequest(http.MethodPost, "/api/v1/internal/jobs/JOB_UNSAFE/train-events", strings.NewReader(`{"eventId":"event-1","type":"WORKER_GROUP_STARTED","generation":1}`))
	request.Header.Set("Authorization", "Bearer "+base64.RawURLEncoding.EncodeToString(make([]byte, 32)))
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || store.wantJobID != "" {
		t.Fatalf("invalid job ID reached repository: code=%d job=%q", response.Code, store.wantJobID)
	}
}
