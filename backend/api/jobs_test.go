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
)

type fakeJobRepository struct {
	created  *domain.TrainingJob
	jobs     []domain.TrainingJob
	identity int
	canceled string
}

func (f *fakeJobRepository) Create(_ context.Context, job *domain.TrainingJob, _ string) error {
	f.created = job
	f.jobs = append(f.jobs, *job)
	return nil
}
func (f *fakeJobRepository) Get(_ context.Context, tenant, id string) (*domain.TrainingJob, error) {
	for _, job := range f.jobs {
		if job.ID == id && job.TenantID == tenant {
			copy := job
			return &copy, nil
		}
	}
	return nil, context.Canceled
}
func (f *fakeJobRepository) List(_ context.Context, filter domain.JobFilter) (domain.Page[domain.TrainingJob], error) {
	items := make([]domain.TrainingJob, 0)
	for _, job := range f.jobs {
		if job.TenantID == filter.TenantID {
			items = append(items, job)
		}
	}
	return domain.Page[domain.TrainingJob]{Items: items, Total: int64(len(items))}, nil
}
func (f *fakeJobRepository) SetDesiredState(_ context.Context, tenant, id string, state domain.DesiredState) error {
	f.canceled = tenant + "/" + id + "/" + string(state)
	return nil
}
func (f *fakeJobRepository) EnsureIdentity(context.Context, auth.Principal) error {
	f.identity++
	return nil
}

func TestSubmitJobUsesAuthenticatedTenantAndQueuesOutboxViaRepository(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repository := &fakeJobRepository{}
	handler := NewHandler(repository, Options{})
	handler.newID = func() (string, error) { return "job-fixed", nil }
	router := gin.New()
	router.Use(func(c *gin.Context) {
		principal := auth.Principal{Subject: "subject-1", TenantID: "team-a", Roles: []string{"Engineer"}, AuthType: auth.AuthTypeOIDC}
		c.Set("ray-platform-principal", principal)
		c.Next()
	})
	handler.RegisterTrainingRoutes(router.Group("/api/v1"))
	body := `{"spec":{"name":"train-one","image":"registry.example/ray@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef","source":{"type":"git","url":"https://git.example/train","commit":"0123456789abcdef"},"entrypoint":{"command":["python","train.py"]},"resources":{"workerReplicas":1,"gpusPerWorker":1},"queue":""}}`
	request := httptest.NewRequest(http.MethodPost, "/api/v1/jobs", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("expected accepted, got %d", response.Code)
	}
	if repository.created == nil || repository.created.TenantID != "team-a" || repository.created.Spec.Queue != "team-a-gpu" {
		t.Fatalf("unexpected created job: %+v", repository.created)
	}
	if repository.identity != 1 {
		t.Fatalf("expected identity upsert")
	}
}

func TestSubmitJobRejectsAnotherTenantQueue(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repository := &fakeJobRepository{}
	handler := NewHandler(repository, Options{})
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("ray-platform-principal", auth.Principal{Subject: "subject-1", TenantID: "team-a", Roles: []string{"Engineer"}, AuthType: auth.AuthTypeOIDC})
		c.Next()
	})
	handler.RegisterTrainingRoutes(router.Group("/api/v1"))
	body := `{"spec":{"name":"train-one","image":"registry.example/ray-train@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef","source":{"type":"git","url":"https://git.example/train","commit":"0123456789abcdef"},"entrypoint":{"command":["python","train.py"]},"resources":{"workerReplicas":1,"gpusPerWorker":1},"queue":"team-b-gpu"}}`
	request := httptest.NewRequest(http.MethodPost, "/api/v1/jobs", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || repository.created != nil {
		t.Fatalf("expected queue rejection, got %d", response.Code)
	}
}

func TestSubmitJobRecordsPortalSubmissionOrigin(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repository := &fakeJobRepository{}
	handler := NewHandler(repository, Options{})
	handler.newID = func() (string, error) { return "job-portal", nil }
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("ray-platform-principal", auth.Principal{Subject: "subject-1", TenantID: "team-a", Roles: []string{"Engineer"}, AuthType: auth.AuthTypeOIDC})
		c.Next()
	})
	handler.RegisterTrainingRoutes(router.Group("/api/v1"))
	body := `{"spec":{"name":"portal-job","image":"registry.example/ray@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef","source":{"type":"git","url":"https://git.example/train","commit":"0123456789abcdef"},"entrypoint":{"command":["python","train.py"]},"resources":{"workerReplicas":1,"gpusPerWorker":1}}}`
	request := httptest.NewRequest(http.MethodPost, "/api/v1/jobs", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("expected accepted, got %d", response.Code)
	}
	if repository.created == nil || repository.created.SubmissionOrigin != domain.SubmissionOriginPortal {
		t.Fatalf("expected portal origin, got %+v", repository.created)
	}
}
