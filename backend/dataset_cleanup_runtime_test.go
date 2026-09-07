package main

import (
	"ray-train-platform-backend/config"
	"ray-train-platform-backend/k8s"
	"ray-train-platform-backend/objectstore"
	"testing"
)

func TestDatasetPurgeRuntimeRejectsWrongTarget(t *testing.T) {
	cfg := config.Config{DatasetVersioningEnabled: true, TOSBucket: "target", DatasetPublisherTargetBucket: "target", TOSEndpoint: "https://tos.example.com", DatasetPublisherTOSEndpoint: "tos.example.com", TOSRegion: "region", DatasetPublisherTOSRegion: "region"}
	if newDatasetPurgeObjects(cfg, &objectstore.TOSStore{}, &k8s.Client{}) == nil {
		t.Fatal("matching target rejected")
	}
	cfg.DatasetPublisherTargetBucket = "other"
	if newDatasetPurgeObjects(cfg, &objectstore.TOSStore{}, &k8s.Client{}) != nil {
		t.Fatal("wrong bucket accepted")
	}
	cfg.DatasetPublisherTargetBucket = "target"
	cfg.DatasetPublisherTOSEndpoint = "other.example.com"
	if newDatasetPurgeObjects(cfg, &objectstore.TOSStore{}, &k8s.Client{}) != nil {
		t.Fatal("wrong endpoint accepted")
	}
	if newDatasetPurgeObjects(cfg, nil, nil) != nil {
		t.Fatal("missing dependencies accepted")
	}
}
