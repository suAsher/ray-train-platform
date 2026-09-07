package main

import (
	"context"
	"ray-train-platform-backend/api"
	"ray-train-platform-backend/config"
	"ray-train-platform-backend/k8s"
	"ray-train-platform-backend/objectstore"
	"ray-train-platform-backend/repositories"
	"strings"
)

func newDatasetPurgeObjects(cfg config.Config, lister objectstore.DirectoryLister, client *k8s.Client) api.DatasetPurgeObjects {
	store, ok := lister.(*objectstore.TOSStore)
	// Reuse credentials only when they describe the publisher's exact target.
	if !ok || store == nil || client == nil || !cfg.DatasetVersioningEnabled ||
		cfg.TOSBucket == "" || cfg.TOSBucket != cfg.DatasetPublisherTargetBucket ||
		cleanupEndpoint(cfg.TOSEndpoint) != cleanupEndpoint(cfg.DatasetPublisherTOSEndpoint) || cfg.TOSRegion != cfg.DatasetPublisherTOSRegion {
		return nil
	}
	return func(ctx context.Context, plan repositories.DatasetPurgePlan) (int, error) {
		if err := client.CheckDatasetCleanupQuiescent(ctx, plan.DatasetID, plan.VersionID); err != nil {
			return 0, repositories.ErrDatasetCleanupConflict
		}
		return store.PurgeFailedPublicationObjects(ctx, cfg.DatasetPublisherTargetBucket, cfg.DatasetInternalPrefix, plan.DatasetID, plan.VersionID, plan.RunIDs)
	}
}

func cleanupEndpoint(value string) string {
	return strings.TrimRight(strings.TrimPrefix(value, "https://"), "/")
}
