package main

import (
	"testing"

	"ray-train-platform-backend/config"
)

func TestRayAPIOptionsUseConfiguredUploadLimits(t *testing.T) {
	options := rayAPIOptions(config.Config{
		RayAPIUploadMaxConcurrent: 7,
		RayAPIUploadRateLimit:     31,
	}, nil)
	if options.UploadMaxConcurrent != 7 || options.UploadRateLimit != 31 {
		t.Fatalf("configured upload controls were not propagated: concurrency=%d rate=%d", options.UploadMaxConcurrent, options.UploadRateLimit)
	}
}
