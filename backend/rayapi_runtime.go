package main

import (
	"ray-train-platform-backend/api"
	"ray-train-platform-backend/config"
	"ray-train-platform-backend/objectstore"
	"ray-train-platform-backend/rayapi"
	"ray-train-platform-backend/repositories"
)

func newRayAPIHandler(repository *repositories.GormRepository, submission *api.SubmissionService, logs api.LogProvider, cfg config.Config) (*rayapi.Handler, error) {
	if !cfg.SourceArtifactsEnabled {
		return nil, nil
	}
	store, err := objectstore.NewTOSStore(objectstore.TOSConfig{
		Endpoint: cfg.TOSEndpoint, Region: cfg.TOSRegion, Bucket: cfg.TOSBucket,
		AccessKey: cfg.TOSAccessKey, SecretKey: cfg.TOSSecretKey, SecurityToken: cfg.TOSSecurityToken,
	})
	if err != nil {
		return nil, err
	}
	return rayapi.NewHandler(repository, store, submission, rayAPIOptions(cfg, logs))
}

func rayAPIOptions(cfg config.Config, logs api.LogProvider) rayapi.Options {
	return rayapi.Options{
		Limits:              repositories.SourceArtifactLimits{MaxPending: cfg.SourceArtifactMaxPending, QuotaBytes: cfg.SourceArtifactQuotaBytes},
		SpoolDir:            cfg.RayAPISpoolDir,
		Logs:                logs,
		UploadMaxConcurrent: cfg.RayAPIUploadMaxConcurrent,
		UploadRateLimit:     cfg.RayAPIUploadRateLimit,
	}
}
