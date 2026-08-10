package main

import (
	"ray-train-platform-backend/api"
	"ray-train-platform-backend/config"
	"ray-train-platform-backend/objectstore"
	"ray-train-platform-backend/repositories"
)

func newSourceArtifactComponents(repository *repositories.GormRepository, cfg config.Config) (*api.SourceArtifactHandler, error) {
	if !cfg.SourceArtifactsEnabled {
		return nil, nil
	}
	store, err := objectstore.NewTOSStore(objectstore.TOSConfig{
		Endpoint:      cfg.TOSEndpoint,
		Region:        cfg.TOSRegion,
		Bucket:        cfg.TOSBucket,
		AccessKey:     cfg.TOSAccessKey,
		SecretKey:     cfg.TOSSecretKey,
		SecurityToken: cfg.TOSSecurityToken,
	})
	if err != nil {
		return nil, err
	}
	return api.NewSourceArtifactHandler(repository, store, api.SourceArtifactOptions{AllowDemo: cfg.DemoMode, MaxPendingArtifacts: cfg.SourceArtifactMaxPending, QuotaBytes: cfg.SourceArtifactQuotaBytes})
}
