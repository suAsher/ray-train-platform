package main

import (
	"fmt"
	"strings"

	"ray-train-platform-backend/config"
	"ray-train-platform-backend/objectstore"
)

// newStorageDirectoryLister is intentionally independent from source-artifact
// upload. A deployment may allow users to browse pre-provisioned data assets
// even when code upload is disabled. An entirely absent TOS configuration keeps
// the catalogue usable but reports the browser as unavailable; a partial
// configuration fails fast rather than behaving inconsistently.
func newStorageDirectoryLister(cfg config.Config) (objectstore.DirectoryLister, error) {
	values := []string{cfg.TOSEndpoint, cfg.TOSRegion, cfg.TOSBucket, cfg.TOSAccessKey, cfg.TOSSecretKey}
	present := 0
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			present++
		}
	}
	if present == 0 {
		return nil, nil
	}
	if present != len(values) {
		return nil, fmt.Errorf("storage directory browser requires complete TOS endpoint, region, bucket, and credentials configuration")
	}
	return objectstore.NewTOSStore(objectstore.TOSConfig{
		Endpoint: cfg.TOSEndpoint, Region: cfg.TOSRegion, Bucket: cfg.TOSBucket,
		AccessKey: cfg.TOSAccessKey, SecretKey: cfg.TOSSecretKey, SecurityToken: cfg.TOSSecurityToken,
	})
}
