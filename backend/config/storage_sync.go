package config

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"k8s.io/apimachinery/pkg/api/resource"
	k8svalidation "k8s.io/apimachinery/pkg/util/validation"
)

// StorageSyncConfig is operator-controlled. API requests cannot choose worker
// images, credentials, storage claims, resource ceilings or placement.
type StorageSyncConfig struct {
	Enabled bool
	Namespace, Image, Bucket, Region, Endpoint string
	CredentialMode, CredentialSecret, ServiceAccountName, WorkClaimName string
	CallbackBaseURL string
	NodeSelector map[string]string
	CPURequest, CPULimit, MemoryRequest, MemoryLimit string
	EphemeralStorageRequest, EphemeralStorageLimit string
	MaxActiveRuns, MaxFileConcurrency, MaxPartConcurrency int
	MaxBandwidthBytesPerSecond int64
}

func loadStorageSyncConfig() (StorageSyncConfig, error) {
	enabled, err := parseBool("STORAGE_SYNC_ENABLED", false)
	if err != nil { return StorageSyncConfig{}, err }
	cfg := StorageSyncConfig{Enabled: enabled}
	if !enabled { return cfg, nil }
	for _, field := range []struct{ key, fallback string; target *string }{
		{"NAMESPACE", "", &cfg.Namespace}, {"IMAGE", "", &cfg.Image},
		{"BUCKET", "", &cfg.Bucket}, {"REGION", "", &cfg.Region}, {"ENDPOINT", "", &cfg.Endpoint},
		{"CREDENTIAL_MODE", "static-secret", &cfg.CredentialMode}, {"CREDENTIAL_SECRET", "", &cfg.CredentialSecret},
		{"SERVICE_ACCOUNT", "", &cfg.ServiceAccountName}, {"WORK_CLAIM_NAME", "", &cfg.WorkClaimName},
		{"CALLBACK_BASE_URL", "", &cfg.CallbackBaseURL},
		{"CPU_REQUEST", "500m", &cfg.CPURequest}, {"CPU_LIMIT", "2", &cfg.CPULimit},
		{"MEMORY_REQUEST", "512Mi", &cfg.MemoryRequest}, {"MEMORY_LIMIT", "2Gi", &cfg.MemoryLimit},
		{"EPHEMERAL_STORAGE_REQUEST", "256Mi", &cfg.EphemeralStorageRequest}, {"EPHEMERAL_STORAGE_LIMIT", "1Gi", &cfg.EphemeralStorageLimit},
	} { *field.target = strings.TrimSpace(envOr("STORAGE_SYNC_" + field.key, field.fallback)) }
	if err := json.Unmarshal([]byte(envOr("STORAGE_SYNC_NODE_SELECTOR_JSON", "{}")), &cfg.NodeSelector); err != nil {
		return StorageSyncConfig{}, fmt.Errorf("STORAGE_SYNC_NODE_SELECTOR_JSON must be a label map")
	}
	for _, field := range []struct{ key string; fallback, maximum int; target *int }{
		{"MAX_ACTIVE_RUNS", 1, 16, &cfg.MaxActiveRuns},
		{"MAX_FILE_CONCURRENCY", 4, 128, &cfg.MaxFileConcurrency},
		{"MAX_PART_CONCURRENCY", 2, 64, &cfg.MaxPartConcurrency},
	} {
		value, err := strconv.Atoi(envOr("STORAGE_SYNC_" + field.key, strconv.Itoa(field.fallback)))
		if err != nil || value < 1 || value > field.maximum { return StorageSyncConfig{}, fmt.Errorf("STORAGE_SYNC_%s must be between 1 and %d", field.key, field.maximum) }
		*field.target = value
	}
	cfg.MaxBandwidthBytesPerSecond, err = strconv.ParseInt(envOr("STORAGE_SYNC_MAX_BANDWIDTH_BYTES_PER_SECOND", "104857600"), 10, 64)
	if err != nil || cfg.MaxBandwidthBytesPerSecond < 1 || cfg.MaxBandwidthBytesPerSecond > 10737418240 {
		return StorageSyncConfig{}, fmt.Errorf("STORAGE_SYNC_MAX_BANDWIDTH_BYTES_PER_SECOND must be between 1 and 10737418240")
	}
	if err := ValidateStorageSyncConfig(cfg); err != nil { return StorageSyncConfig{}, err }
	return cfg, nil
}

func ValidateStorageSyncConfig(cfg StorageSyncConfig) error {
	if !cfg.Enabled { return nil }
	if !pinnedImagePattern.MatchString(cfg.Image) { return fmt.Errorf("STORAGE_SYNC_IMAGE must be pinned by sha256 digest") }
	if !validDatasetPublisherBucket(cfg.Bucket) || cfg.Region == "" { return fmt.Errorf("STORAGE_SYNC_BUCKET and STORAGE_SYNC_REGION are required") }
	for _, value := range []string{cfg.Namespace, cfg.CredentialSecret, cfg.ServiceAccountName, cfg.WorkClaimName} {
		if !isDNSSubdomain(value) { return fmt.Errorf("storage sync namespace, Secret, ServiceAccount and work PVC must be valid names") }
	}
	if cfg.CredentialMode != "static-secret" { return fmt.Errorf("STORAGE_SYNC_CREDENTIAL_MODE must be static-secret; automatic writer failover is unavailable") }
	if cfg.MaxActiveRuns < 1 || cfg.MaxActiveRuns > 16 || cfg.MaxFileConcurrency < 1 || cfg.MaxFileConcurrency > 128 || cfg.MaxPartConcurrency < 1 || cfg.MaxPartConcurrency > 64 || cfg.MaxBandwidthBytesPerSecond < 1 || cfg.MaxBandwidthBytesPerSecond > 10737418240 {
		return fmt.Errorf("storage sync concurrency and bandwidth ceilings are invalid")
	}
	if len(cfg.NodeSelector) == 0 { return fmt.Errorf("STORAGE_SYNC_NODE_SELECTOR_JSON must explicitly select verified CPU nodes") }
	for key, value := range cfg.NodeSelector {
		if len(k8svalidation.IsQualifiedName(key)) != 0 || len(k8svalidation.IsValidLabelValue(value)) != 0 { return fmt.Errorf("STORAGE_SYNC_NODE_SELECTOR_JSON contains an invalid label") }
	}
	for _, pair := range []struct{ name, request, limit string }{
		{"CPU", cfg.CPURequest, cfg.CPULimit}, {"MEMORY", cfg.MemoryRequest, cfg.MemoryLimit},
		{"EPHEMERAL_STORAGE", cfg.EphemeralStorageRequest, cfg.EphemeralStorageLimit},
	} {
		request, requestErr := resource.ParseQuantity(pair.request)
		limit, limitErr := resource.ParseQuantity(pair.limit)
		if requestErr != nil || limitErr != nil || request.Sign() <= 0 || limit.Sign() <= 0 || request.Cmp(limit) > 0 { return fmt.Errorf("STORAGE_SYNC_%s request and limit must be positive, with request no greater than limit", pair.name) }
	}
	endpoint, err := url.Parse(cfg.Endpoint)
	if err != nil || endpoint.Scheme != "https" || endpoint.Hostname() == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" { return fmt.Errorf("STORAGE_SYNC_ENDPOINT must be a credential-free HTTPS endpoint") }
	callback, err := url.Parse(cfg.CallbackBaseURL)
	if err != nil || (callback.Scheme != "http" && callback.Scheme != "https") || callback.Hostname() == "" || callback.User != nil || callback.RawQuery != "" || callback.Fragment != "" { return fmt.Errorf("STORAGE_SYNC_CALLBACK_BASE_URL must be a credential-free HTTP(S) URL") }
	return nil
}
