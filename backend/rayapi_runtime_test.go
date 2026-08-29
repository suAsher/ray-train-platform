package main

import (
	"testing"

	"ray-train-platform-backend/config"
)

func TestRayAPIOptionsUseConfiguredUploadLimits(t *testing.T) {
	options := rayAPIOptions(config.Config{
		RayAPIUploadMaxConcurrent: 7,
		RayAPIUploadRateLimit:     31,
		RayVersion:                "2.56.1",
	}, nil)
	if options.UploadMaxConcurrent != 7 || options.UploadRateLimit != 31 {
		t.Fatalf("configured upload controls were not propagated: concurrency=%d rate=%d", options.UploadMaxConcurrent, options.UploadRateLimit)
	}
	if options.RayVersion != "2.56.1" {
		t.Fatalf("configured Ray version was not propagated: %q", options.RayVersion)
	}
}
