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
	Enabled                                                             bool
	Namespace, Image, Bucket, Region, Endpoint                          string
	CredentialMode, CredentialSecret, ServiceAccountName, WorkClaimName string
	CallbackBaseURL                                                     string
	NodeSelector                                                        map[string]string
	CPURequest, CPULimit, MemoryRequest, MemoryLimit                    string
	EphemeralStorageRequest, EphemeralStorageLimit                      string
	MaxActiveRuns, MaxFileConcurrency, MaxPartConcurrency               int
	MaxBandwidthBytesPerSecond                                          int64
	GCNamespaces                                                        []string
	GCSucceededTTLSeconds, GCFailedTTLSeconds                           int32
}

func loadStorageSyncConfig() (StorageSyncConfig, error) {
	enabled, err := parseBool("STORAGE_SYNC_ENABLED", false)
	if err != nil {
		return StorageSyncConfig{}, err
	}
	cfg := StorageSyncConfig{Enabled: enabled}
	if !enabled {
		return cfg, nil
	}
	for _, field := range []struct {
		key, fallback string
		target        *string
	}{
		{"NAMESPACE", "", &cfg.Namespace}, {"IMAGE", "", &cfg.Image},
		{"BUCKET", "", &cfg.Bucket}, {"REGION", "", &cfg.Region}, {"ENDPOINT", "", &cfg.Endpoint},
		{"CREDENTIAL_MODE", "static-secret", &cfg.CredentialMode}, {"CREDENTIAL_SECRET", "", &cfg.CredentialSecret},
		{"SERVICE_ACCOUNT", "", &cfg.ServiceAccountName}, {"WORK_CLAIM_NAME", "", &cfg.WorkClaimName},
		{"CALLBACK_BASE_URL", "", &cfg.CallbackBaseURL},
		{"CPU_REQUEST", "500m", &cfg.CPURequest}, {"CPU_LIMIT", "2", &cfg.CPULimit},
		{"MEMORY_REQUEST", "512Mi", &cfg.MemoryRequest}, {"MEMORY_LIMIT", "2Gi", &cfg.MemoryLimit},
		{"EPHEMERAL_STORAGE_REQUEST", "256Mi", &cfg.EphemeralStorageRequest}, {"EPHEMERAL_STORAGE_LIMIT", "1Gi", &cfg.EphemeralStorageLimit},
	} {
		*field.target = strings.TrimSpace(envOr("STORAGE_SYNC_"+field.key, field.fallback))
	}
	if err := json.Unmarshal([]byte(envOr("STORAGE_SYNC_NODE_SELECTOR_JSON", "{}")), &cfg.NodeSelector); err != nil {
		return StorageSyncConfig{}, fmt.Errorf("STORAGE_SYNC_NODE_SELECTOR_JSON must be a label map")
	}
	for _, field := range []struct {
		key               string
		fallback, maximum int
		target            *int
	}{
		{"MAX_ACTIVE_RUNS", 1, 16, &cfg.MaxActiveRuns},
		{"MAX_FILE_CONCURRENCY", 4, 128, &cfg.MaxFileConcurrency},
		{"MAX_PART_CONCURRENCY", 2, 64, &cfg.MaxPartConcurrency},
	} {
		value, err := strconv.Atoi(envOr("STORAGE_SYNC_"+field.key, strconv.Itoa(field.fallback)))
		if err != nil || value < 1 || value > field.maximum {
			return StorageSyncConfig{}, fmt.Errorf("STORAGE_SYNC_%s must be between 1 and %d", field.key, field.maximum)
		}
		*field.target = value
	}
	cfg.MaxBandwidthBytesPerSecond, err = strconv.ParseInt(envOr("STORAGE_SYNC_MAX_BANDWIDTH_BYTES_PER_SECOND", "104857600"), 10, 64)
	if err != nil || cfg.MaxBandwidthBytesPerSecond < 102400 || cfg.MaxBandwidthBytesPerSecond > 10737418240 {
		return StorageSyncConfig{}, fmt.Errorf("STORAGE_SYNC_MAX_BANDWIDTH_BYTES_PER_SECOND must be between 102400 and 10737418240")
	}
	cfg.GCNamespaces, err = storageSyncGCNamespaces(cfg.Namespace, envOr("STORAGE_SYNC_GC_NAMESPACES_JSON", "[]"))
	if err != nil {
		return StorageSyncConfig{}, err
	}
	for _, field := range []struct {
		key      string
		fallback int32
		target   *int32
	}{
		{"GC_SUCCEEDED_TTL_SECONDS", 3600, &cfg.GCSucceededTTLSeconds},
		{"GC_FAILED_TTL_SECONDS", 86400, &cfg.GCFailedTTLSeconds},
	} {
		value, err := strconv.ParseInt(envOr("STORAGE_SYNC_"+field.key, strconv.FormatInt(int64(field.fallback), 10)), 10, 32)
		if err != nil || value < 1 || value > 604800 {
			return StorageSyncConfig{}, fmt.Errorf("STORAGE_SYNC_%s must be between 1 and 604800", field.key)
		}
		*field.target = int32(value)
	}
	if err := ValidateStorageSyncConfig(cfg); err != nil {
		return StorageSyncConfig{}, err
	}
	return cfg, nil
}

func storageSyncGCNamespaces(namespace, raw string) ([]string, error) {
	var additional []string
	if err := json.Unmarshal([]byte(raw), &additional); err != nil || additional == nil {
		return nil, fmt.Errorf("STORAGE_SYNC_GC_NAMESPACES_JSON must be an array of namespace names")
	}
	result := make([]string, 0, len(additional)+1)
	seen := make(map[string]bool, len(additional)+1)
	for _, value := range append([]string{namespace}, additional...) {
		if len(k8svalidation.IsDNS1123Label(value)) != 0 {
			return nil, fmt.Errorf("storage sync execution and garbage collection namespaces must be valid namespace names")
		}
		if !seen[value] {
			result = append(result, value)
			seen[value] = true
		}
	}
	return result, nil
}

func ValidateStorageSyncConfig(cfg StorageSyncConfig) error {
	if !cfg.Enabled {
		return nil
	}
	if !pinnedImagePattern.MatchString(cfg.Image) {
		return fmt.Errorf("STORAGE_SYNC_IMAGE must be pinned by sha256 digest")
	}
	if !validDatasetPublisherBucket(cfg.Bucket) || cfg.Region == "" {
		return fmt.Errorf("STORAGE_SYNC_BUCKET and STORAGE_SYNC_REGION are required")
	}
	if len(k8svalidation.IsDNS1123Label(cfg.Namespace)) != 0 {
		return fmt.Errorf("STORAGE_SYNC_NAMESPACE must be a valid namespace name")
	}
	for _, value := range cfg.GCNamespaces {
		if len(k8svalidation.IsDNS1123Label(value)) != 0 {
			return fmt.Errorf("STORAGE_SYNC_GC_NAMESPACES_JSON contains an invalid namespace name")
		}
	}
	if cfg.GCSucceededTTLSeconds < 1 || cfg.GCSucceededTTLSeconds > 604800 || cfg.GCFailedTTLSeconds < 1 || cfg.GCFailedTTLSeconds > 604800 {
		return fmt.Errorf("storage sync garbage collection retention must be between 1 and 604800 seconds")
	}
	for _, value := range []string{cfg.CredentialSecret, cfg.ServiceAccountName, cfg.WorkClaimName} {
		if !isDNSSubdomain(value) {
			return fmt.Errorf("storage sync Secret, ServiceAccount and work PVC must be valid names")
		}
	}
	if cfg.CredentialMode != "static-secret" {
		return fmt.Errorf("STORAGE_SYNC_CREDENTIAL_MODE must be static-secret; automatic writer failover is unavailable")
	}
	if cfg.MaxActiveRuns < 1 || cfg.MaxActiveRuns > 16 || cfg.MaxFileConcurrency < 1 || cfg.MaxFileConcurrency > 128 || cfg.MaxPartConcurrency < 1 || cfg.MaxPartConcurrency > 64 || cfg.MaxBandwidthBytesPerSecond < 102400 || cfg.MaxBandwidthBytesPerSecond > 10737418240 {
		return fmt.Errorf("storage sync concurrency and bandwidth ceilings are invalid")
	}
	if len(cfg.NodeSelector) == 0 {
		return fmt.Errorf("STORAGE_SYNC_NODE_SELECTOR_JSON must explicitly select verified CPU nodes")
	}
	for key, value := range cfg.NodeSelector {
		if len(k8svalidation.IsQualifiedName(key)) != 0 || len(k8svalidation.IsValidLabelValue(value)) != 0 {
			return fmt.Errorf("STORAGE_SYNC_NODE_SELECTOR_JSON contains an invalid label")
		}
	}
	for _, pair := range []struct{ name, request, limit string }{
		{"CPU", cfg.CPURequest, cfg.CPULimit}, {"MEMORY", cfg.MemoryRequest, cfg.MemoryLimit},
		{"EPHEMERAL_STORAGE", cfg.EphemeralStorageRequest, cfg.EphemeralStorageLimit},
	} {
		request, requestErr := resource.ParseQuantity(pair.request)
		limit, limitErr := resource.ParseQuantity(pair.limit)
		if requestErr != nil || limitErr != nil || request.Sign() <= 0 || limit.Sign() <= 0 || request.Cmp(limit) > 0 {
			return fmt.Errorf("STORAGE_SYNC_%s request and limit must be positive, with request no greater than limit", pair.name)
		}
	}
	endpoint, err := url.Parse(cfg.Endpoint)
	if err != nil || endpoint.Scheme != "https" || endpoint.Hostname() == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" {
		return fmt.Errorf("STORAGE_SYNC_ENDPOINT must be a credential-free HTTPS endpoint")
	}
	callback, err := url.Parse(cfg.CallbackBaseURL)
	if err != nil || (callback.Scheme != "http" && callback.Scheme != "https") || callback.Hostname() == "" || callback.User != nil || callback.RawQuery != "" || callback.Fragment != "" {
		return fmt.Errorf("STORAGE_SYNC_CALLBACK_BASE_URL must be a credential-free HTTP(S) URL")
	}
	return nil
}
