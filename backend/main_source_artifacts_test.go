package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"ray-train-platform-backend/api"
	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/config"
	"ray-train-platform-backend/domain"
	"ray-train-platform-backend/objectstore"
	"ray-train-platform-backend/repositories"
)

type mainArtifactRepository struct{}

func (*mainArtifactRepository) EnsureIdentity(context.Context, auth.Principal) error { return nil }
func (*mainArtifactRepository) CreateOrReuseSourceArtifact(_ context.Context, artifact *domain.SourceArtifact) (*domain.SourceArtifact, error) {
	return artifact, nil
}
func (*mainArtifactRepository) CreateOrReuseSourceArtifactWithLimits(_ context.Context, artifact *domain.SourceArtifact, _ repositories.SourceArtifactLimits) (*domain.SourceArtifact, error) {
	return artifact, nil
}
func (*mainArtifactRepository) CreateSourceArtifactForRequestWithLimits(_ context.Context, artifact *domain.SourceArtifact, _ string, _ repositories.SourceArtifactLimits) (*domain.SourceArtifact, error) {
	return artifact, nil
}
func (*mainArtifactRepository) GetSourceArtifactByClientRequestID(context.Context, string, string, string) (*domain.SourceArtifact, error) {
	return nil, repositories.ErrSourceArtifactNotFound
}
func (*mainArtifactRepository) GetSourceArtifact(context.Context, string, string, string) (*domain.SourceArtifact, error) {
	return nil, context.Canceled
}
func (*mainArtifactRepository) ReopenSourceArtifactUploadWithLimits(context.Context, string, string, string, time.Time, repositories.SourceArtifactLimits) (*domain.SourceArtifact, error) {
	return nil, context.Canceled
}
func (*mainArtifactRepository) MarkSourceArtifactReady(context.Context, string, string, string, time.Time) (*domain.SourceArtifact, error) {
	return nil, context.Canceled
}

type mainArtifactStore struct{}

func (*mainArtifactStore) PresignPut(context.Context, string, string, int64, time.Duration) (objectstore.PresignedPut, error) {
	return objectstore.PresignedPut{}, context.Canceled
}
func (*mainArtifactStore) Head(context.Context, string) (objectstore.ObjectInfo, error) {
	return objectstore.ObjectInfo{}, context.Canceled
}

type mainJobRepository struct{}

func (*mainJobRepository) Create(context.Context, *domain.TrainingJob, string) error { return nil }
func (*mainJobRepository) Get(context.Context, string, string) (*domain.TrainingJob, error) {
	return nil, context.Canceled
}
func (*mainJobRepository) List(context.Context, domain.JobFilter) (domain.Page[domain.TrainingJob], error) {
	return domain.Page[domain.TrainingJob]{}, nil
}
func (*mainJobRepository) SetDesiredState(context.Context, string, string, domain.DesiredState) error {
	return nil
}
func (*mainJobRepository) EnsureIdentity(context.Context, auth.Principal) error { return nil }

func TestRegisterAPIRoutesExplicitlyRegistersSourceArtifactsOnlyWhenEnabled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	jobs := api.NewHandler(&mainJobRepository{}, api.Options{AllowAnonymous: true})
	artifactHandler, err := api.NewSourceArtifactHandler(&mainArtifactRepository{}, &mainArtifactStore{}, api.SourceArtifactOptions{})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		handler *api.SourceArtifactHandler
		want    int
	}{
		{name: "enabled", handler: artifactHandler, want: http.StatusBadRequest},
		{name: "disabled", handler: nil, want: http.StatusNotFound},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			router := gin.New()
			registerAPIRoutes(router, jobs, nil, test.handler, nil, nil, nil, config.Config{DemoMode: true})
			request := httptest.NewRequest(http.MethodPost, "/api/v1/source-artifacts", nil)
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != test.want {
				t.Fatalf("route status=%d, want %d", response.Code, test.want)
			}
		})
	}
}

func TestNewSourceArtifactComponentsHonorFeatureFlag(t *testing.T) {
	disabled, err := newSourceArtifactComponents(nil, config.Config{SourceArtifactsEnabled: false})
	if err != nil || disabled != nil {
		t.Fatalf("disabled source artifacts handler=%v err=%v", disabled, err)
	}
	enabled, err := newSourceArtifactComponents(repositories.NewGormRepository(nil), config.Config{
		SourceArtifactsEnabled: true, TOSEndpoint: "https://tos-cn-beijing.volces.com",
		TOSRegion: "cn-beijing", TOSBucket: "bucket", TOSAccessKey: "ak", TOSSecretKey: "sk",
	})
	if err != nil || enabled == nil {
		t.Fatalf("enabled source artifacts failed to construct: handler=%v err=%v", enabled, err)
	}
}
