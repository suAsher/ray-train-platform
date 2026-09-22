package api

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"ray-train-platform-backend/repositories"
)

type fakeAssistantDemandStore struct {
	snapshot repositories.AssistantDemandSnapshot
	err      error
	calls    int
	inspect  func(context.Context)
}

func (s *fakeAssistantDemandStore) AggregateAssistantDemand(ctx context.Context) (repositories.AssistantDemandSnapshot, error) {
	s.calls++
	if s.inspect != nil {
		s.inspect(ctx)
	}
	return s.snapshot, s.err
}

func assistantDemandToken(key []byte) string {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(assistantDemandAuthMessage))
	return hex.EncodeToString(mac.Sum(nil))
}

func assistantDemandRouter(h *Handler) *gin.Engine {
	r := gin.New()
	h.RegisterAssistantDemandInternalRoutes(r.Group("/api/v1/internal"))
	return r
}

func TestAssistantDemandInternalRouteRequiresFixedHMAC(t *testing.T) {
	gin.SetMode(gin.TestMode)
	key := []byte(strings.Repeat("k", 32))
	store := &fakeAssistantDemandStore{snapshot: repositories.AssistantDemandSnapshot{TrainingJobCount: 2, WorkspaceCount: 1, GPUCount: 5, HasDemand: true}}
	router := assistantDemandRouter(NewHandler(&fakeJobRepository{}, Options{AssistantDemand: store, AssistantDemandAuthKey: key}))

	for _, auth := range []string{"", assistantDemandToken(key), "Bearer wrong", "Basic " + assistantDemandToken(key), "Bearer " + assistantDemandToken([]byte(strings.Repeat("x", 32)))} {
		response := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "/api/v1/internal/assistant-idle/training-demand", nil)
		request.Header.Set("Authorization", auth)
		router.ServeHTTP(response, request)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("auth %q status=%d body=%s", auth, response.Code, response.Body.String())
		}
	}
	if store.calls != 0 {
		t.Fatalf("unauthorized requests reached store %d times", store.calls)
	}

	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/internal/assistant-idle/training-demand", nil)
	request.Header.Set("Authorization", "Bearer "+assistantDemandToken(key))
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"gpuCount":5`) || !strings.Contains(response.Body.String(), `"observedAt"`) || strings.Contains(response.Body.String(), "tenant") || strings.Contains(response.Body.String(), "user") {
		t.Fatalf("unexpected authorized response status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestAssistantDemandInternalRouteDoesNotRedirectNearMissPath(t *testing.T) {
	gin.SetMode(gin.TestMode)
	key := []byte(strings.Repeat("k", 32))
	store := &fakeAssistantDemandStore{}
	router := assistantDemandRouter(NewHandler(&fakeJobRepository{}, Options{AssistantDemand: store, AssistantDemandAuthKey: key}))

	request := httptest.NewRequest(http.MethodGet, "/api/v1/internal/assistant-idle/training-demand/", nil)
	request.Header.Set("Authorization", "Bearer "+assistantDemandToken(key))
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound || response.Header().Get("Location") != "" || store.calls != 0 {
		t.Fatalf("near-miss path redirected or reached store: status=%d location=%q calls=%d body=%s", response.Code, response.Header().Get("Location"), store.calls, response.Body.String())
	}
}

func TestAssistantDemandInternalRouteRejectsBodyAndChunkedBeforeStore(t *testing.T) {
	gin.SetMode(gin.TestMode)
	key := []byte(strings.Repeat("k", 32))
	store := &fakeAssistantDemandStore{}
	router := assistantDemandRouter(NewHandler(&fakeJobRepository{}, Options{AssistantDemand: store, AssistantDemandAuthKey: key}))

	for _, request := range []*http.Request{
		httptest.NewRequest(http.MethodGet, "/api/v1/internal/assistant-idle/training-demand", strings.NewReader("{}")),
		func() *http.Request {
			r := httptest.NewRequest(http.MethodGet, "/api/v1/internal/assistant-idle/training-demand", nil)
			r.Body = io.NopCloser(strings.NewReader("{}"))
			r.ContentLength = -1
			r.TransferEncoding = []string{"chunked"}
			return r
		}(),
	} {
		request.Header.Set("Authorization", "Bearer "+assistantDemandToken(key))
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest || store.calls != 0 {
			t.Fatalf("body was not rejected before store: status=%d calls=%d body=%s", response.Code, store.calls, response.Body.String())
		}
	}
}

func TestAssistantDemandInternalRouteUsesShortStoreContext(t *testing.T) {
	gin.SetMode(gin.TestMode)
	key := []byte(strings.Repeat("k", 32))
	store := &fakeAssistantDemandStore{inspect: func(ctx context.Context) {
		deadline, ok := ctx.Deadline()
		if !ok {
			t.Fatal("assistant demand store context has no deadline")
		}
		remaining := time.Until(deadline)
		if remaining <= 0 || remaining > 1500*time.Millisecond {
			t.Fatalf("assistant demand store deadline is not about one second: %s", remaining)
		}
	}}
	router := assistantDemandRouter(NewHandler(&fakeJobRepository{}, Options{AssistantDemand: store, AssistantDemandAuthKey: key}))

	request := httptest.NewRequest(http.MethodGet, "/api/v1/internal/assistant-idle/training-demand", nil)
	request.Header.Set("Authorization", "Bearer "+assistantDemandToken(key))
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || store.calls != 1 {
		t.Fatalf("short-context request failed: status=%d calls=%d body=%s", response.Code, store.calls, response.Body.String())
	}
}

func TestAssistantDemandInternalRouteRejectsQueryAndFailsClosed(t *testing.T) {
	gin.SetMode(gin.TestMode)
	key := []byte(strings.Repeat("k", 32))
	store := &fakeAssistantDemandStore{err: errors.New("private database failure")}
	router := assistantDemandRouter(NewHandler(&fakeJobRepository{}, Options{AssistantDemand: store, AssistantDemandAuthKey: key}))

	request := httptest.NewRequest(http.MethodGet, "/api/v1/internal/assistant-idle/training-demand?url=http://elsewhere", nil)
	request.Header.Set("Authorization", "Bearer "+assistantDemandToken(key))
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || store.calls != 0 {
		t.Fatalf("query was not rejected before store: status=%d calls=%d body=%s", response.Code, store.calls, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodGet, "/api/v1/internal/assistant-idle/training-demand", nil)
	request.Header.Set("Authorization", "Bearer "+assistantDemandToken(key))
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable || strings.Contains(response.Body.String(), "private database") {
		t.Fatalf("store error did not fail closed safely: status=%d body=%s", response.Code, response.Body.String())
	}
}
