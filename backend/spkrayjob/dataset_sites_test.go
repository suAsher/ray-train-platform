package spkrayjob

import (
	"strings"
	"testing"

	"ray-train-platform-backend/domain"
)

func TestSiteScopedPreflightCannotDropOrChangeSelection(t *testing.T) {
	sites, _ := domain.NewDatasetSites([]string{"site-a"})
	spec := domain.JobSpec{Image: "image", CachePolicy: domain.DatasetCachePolicyAuto,
		DatasetRef: domain.DatasetReference{Dataset: "dataset", Version: "latest", Sites: sites}}
	result := SubmissionPreflightResult{Image: spec.Image, TrainingEngine: domain.TrainingEngineRayTrain, RayVersion: domain.RayVersionCanary,
		Dataset: &DatasetPreflightSummary{DatasetID: "dataset", VersionID: "version-1", ManifestSHA256: strings.Repeat("a", 64),
			TrainSamples: 100, PackedBytes: 100, DataMode: domain.DataModeStreaming, CachePolicy: spec.CachePolicy, Sites: sites}}
	resolved, err := validateStreamingPreflight(spec, result)
	if err != nil || resolved.DatasetRef.Sites != sites || resolved.DatasetRef.Version != "version-1" {
		t.Fatalf("lost scope: %+v %v", resolved, err)
	}
	result.Dataset.Sites = ""
	if _, err := validateStreamingPreflight(spec, result); err == nil {
		t.Fatal("server silently dropped selected sites")
	}
}

func TestProjectSiteOverridesPreserveAndExplicitlyClearScope(t *testing.T) {
	sites, _ := domain.NewDatasetSites([]string{"site-a"})
	base := project{DatasetRef: domain.DatasetReference{Dataset: "dataset", Version: "version-1", Sites: sites}}
	if got := base.merge(submitOverrides{}); got.DatasetRef.Sites != sites {
		t.Fatal("default scope lost")
	}
	if got := base.merge(submitOverrides{providedDatasetSites: true}); got.DatasetRef.Sites != "" {
		t.Fatal("explicit all-site override ignored")
	}
	if got := base.merge(submitOverrides{DatasetRef: domain.DatasetReference{Dataset: "other"}, providedDataset: true}); got.DatasetRef.Sites != "" {
		t.Fatal("old dataset sites carried into a different dataset")
	}
}
