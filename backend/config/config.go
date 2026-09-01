package config

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"regexp"
	"strconv"
	"strings"

	"k8s.io/apimachinery/pkg/api/resource"
	k8svalidation "k8s.io/apimachinery/pkg/util/validation"
	"ray-train-platform-backend/domain"
)

var (
	pinnedImagePattern                     = regexp.MustCompile(`^[^@\s]+@sha256:[0-9a-fA-F]{64}$`)
	datasetPublisherPrivateEndpointPattern = regexp.MustCompile(`^tos[0-9]*-private$`)
	datasetPublisherIRSARoleTRNPattern     = regexp.MustCompile(`^trn:iam::[0-9]+:role/[A-Za-z0-9+=,.@_/-]+$`)
)

type Config struct {
	AppEnv                                   string
	HTTPAddr                                 string
	DatabaseURL                              string
	OIDCIssuerURL                            string
	OIDCClientID                             string
	OIDCAudience                             string
	OIDCGroupPrefix                          string
	OIDCRequired                             bool
	PATEnabled                               bool
	PATPepper                                string
	TrainingNodeSelector                     map[string]string
	KueueAutoQuota                           bool
	MaxWorkerReplicas                        int
	MaxGPUsPerWorker                         int
	MaxTotalGPUs                             int
	LocalCacheEnabled                        bool
	DataSpacesEnabled                        bool
	DataSpacesFSXAttributes                  string
	DataSpacesMountCapacity                  string
	DataSpacesPublicRoot                     string
	IDCDataSpacesEnabled                     bool
	IDCDataSpacesMountCapacity               string
	IDCDataSpaceSources                      map[string]IDCDataSpaceSource
	DatasetVersioningEnabled                 bool
	RayDataStreamingEnabled                  bool
	DatasetPublisherEnabled                  bool
	DatasetPublisherDistributedEnabled       bool
	DatasetInternalPrefix                    string
	DatasetPublisherImage                    string
	DatasetPublisherImagePullPolicy          string
	DatasetPublisherSourceBucket             string
	DatasetPublisherTargetBucket             string
	DatasetPublisherTOSEndpoint              string
	DatasetPublisherTOSRegion                string
	DatasetPublisherServiceAccount           string
	DatasetPublisherIRSARoleTRN              string
	DatasetPublisherCredentialSecret         string
	DatasetPublisherQueueName                string
	DatasetPublisherPriorityClassName        string
	DatasetPublisherWorkingDirectory         string
	DatasetPublisherSourceIndexName          string
	DatasetPublisherCPURequest               string
	DatasetPublisherCPULimit                 string
	DatasetPublisherMemoryRequest            string
	DatasetPublisherMemoryLimit              string
	DatasetPublisherNodeSelector             map[string]string
	DatasetPublisherPreferredNodeSelector    map[string]string
	DatasetPublisherTolerations              []DatasetPublisherToleration
	DatasetPublisherClientMaxAttempts        int
	DatasetPublisherJobBackoffLimit          int
	DatasetPublisherJobActiveDeadlineSeconds int
	DatasetPublisherJobTTLSeconds            int
	DatasetPublisherInitialRetrySeconds      int
	DatasetPublisherMaximumRetrySeconds      int
	DatasetPublisherPollIntervalSeconds      int
	DatasetPublisherPartitionCount           int
	DatasetPublisherMaxParallelism           int
	DatasetPublisherPartitionLeaseSeconds    int
	DatasetPublisherMaxPartitionAttempts     int
	LocalCacheStorageClass                   string
	LocalCacheStorageClassData1              string
	LocalCacheStorageClassData2              string
	LocalCacheSize                           string
	LocalCacheAllowedSizes                   []string
	LocalCacheMaxSize                        string
	LocalCacheMountPath                      string
	LocalCacheMountPathData1                 string
	LocalCacheMountPathData2                 string
	LocalAuthEnabled                         bool
	LocalSessionHours                        int
	BootstrapAdminUsername                   string
	BootstrapAdminPassword                   string
	BootstrapAdminTenant                     string
	PATDefaultExpiryDays                     int
	PATMaxExpiryDays                         int
	SourceArtifactsEnabled                   bool
	SourceArtifactMaxPending                 int
	SourceArtifactQuotaBytes                 int64
	RayAPISpoolDir                           string
	RayAPISpoolSizeBytes                     int64
	RayAPIUploadMaxConcurrent                int
	RayAPIUploadRateLimit                    int
	RayAPIDefaultImage                       string
	KubeConfig                               string
	KubeContext                              string
	LokiURL                                  string
	PrometheusURL                            string
	MLflowEnabled                            bool
	MLflowTrackingURL                        string
	MLflowIngestURL                          string
	MLflowExperimentPrefix                   string
	MLflowDashboardEnabled                   bool
	MLflowPublicOrigin                       string
	MLflowDashboardSessionHours              int
	KueueClusterQueue                        string
	IDCStorageEnabled                        bool
	IDCExistingClaim                         string
	IDCStorageClass                          string
	IDCMountPath                             string
	RayVersion                               string
	RayTrainManagedEnabled                   bool
	RayTrainManagedTenants                   []string
	RayTrainCanaryEnabled                    bool
	RayTrainCanaryTenants                    []string
	RayJobClusterSpecField                   string
	RayJobServiceAccount                     string
	ImagePullSecrets                         []string
	SourceMaterializerImage                  string
	WorkspaceImage                           string
	CORSOrigins                              []string
	RayImageAllowlist                        []string
	GitAllowlist                             []string
	TOSBucket                                string
	TOSEndpoint                              string
	TOSRegion                                string
	TOSAccessKey                             string
	TOSSecretKey                             string
	TOSSecurityToken                         string
	TOSSecretName                            string
	TOSObjectSetQuotasEnabled                bool
	PersonalStorageDefaultQuotaBytes         int64
	PersonalStorageMaxQuotaBytes             int64
	MigrationsOnly                           bool
	DemoMode                                 bool
}

// IDCDataSpaceSource is a deployment-owned NFS export. It is deliberately
// configuration-only: callers can select a logical data space but cannot
// submit an arbitrary NFS server or path through the API.
type IDCDataSpaceSource struct {
	Server       string   `json:"server"`
	Path         string   `json:"path"`
	MountOptions []string `json:"mountOptions"`
}

type DatasetPublisherToleration struct {
	Key               string `json:"key"`
	Operator          string `json:"operator"`
	Value             string `json:"value,omitempty"`
	Effect            string `json:"effect,omitempty"`
	TolerationSeconds *int64 `json:"tolerationSeconds,omitempty"`
}

func Load() (Config, error) {
	cfg := Config{
		AppEnv:                            envOr("APP_ENV", "development"),
		HTTPAddr:                          envOr("HTTP_ADDR", ":8080"),
		DatabaseURL:                       os.Getenv("DATABASE_URL"),
		OIDCIssuerURL:                     os.Getenv("OIDC_ISSUER_URL"),
		OIDCClientID:                      os.Getenv("OIDC_CLIENT_ID"),
		OIDCAudience:                      os.Getenv("OIDC_AUDIENCE"),
		OIDCGroupPrefix:                   envOr("OIDC_GROUP_PREFIX", "platform/tenants/"),
		PATPepper:                         os.Getenv("PAT_PEPPER"),
		KubeConfig:                        os.Getenv("KUBECONFIG"),
		KubeContext:                       os.Getenv("KUBE_CONTEXT"),
		LokiURL:                           envOr("LOKI_URL", "http://loki-gateway.loki.svc.cluster.local"),
		PrometheusURL:                     envOr("PROMETHEUS_URL", "http://prometheus.monitoring.svc.cluster.local:9090"),
		MLflowTrackingURL:                 strings.TrimSpace(os.Getenv("MLFLOW_TRACKING_URL")),
		MLflowIngestURL:                   strings.TrimSpace(os.Getenv("MLFLOW_INGEST_URL")),
		MLflowExperimentPrefix:            envOr("MLFLOW_EXPERIMENT_PREFIX", "raytrain"),
		MLflowPublicOrigin:                strings.TrimSpace(os.Getenv("MLFLOW_PUBLIC_ORIGIN")),
		MLflowDashboardSessionHours:       8,
		KueueClusterQueue:                 envOr("KUEUE_CLUSTER_QUEUE", "cluster-gpu-queue"),
		IDCExistingClaim:                  os.Getenv("IDC_EXISTING_CLAIM"),
		IDCStorageClass:                   os.Getenv("IDC_STORAGE_CLASS"),
		IDCMountPath:                      envOr("IDC_MOUNT_PATH", "/mnt/idc"),
		LocalCacheStorageClass:            strings.TrimSpace(os.Getenv("LOCAL_CACHE_STORAGE_CLASS")),
		LocalCacheStorageClassData1:       strings.TrimSpace(envOr("LOCAL_CACHE_STORAGE_CLASS_DATA1", os.Getenv("LOCAL_CACHE_STORAGE_CLASS"))),
		LocalCacheStorageClassData2:       strings.TrimSpace(os.Getenv("LOCAL_CACHE_STORAGE_CLASS_DATA2")),
		LocalCacheSize:                    strings.TrimSpace(envOr("LOCAL_CACHE_SIZE", "200Gi")),
		LocalCacheAllowedSizes:            splitList(envOr("LOCAL_CACHE_ALLOWED_SIZES", "200Gi,500Gi,1Ti,2Ti,4Ti,5Ti")),
		LocalCacheMaxSize:                 strings.TrimSpace(envOr("LOCAL_CACHE_MAX_SIZE", "5Ti")),
		LocalCacheMountPath:               strings.TrimSpace(os.Getenv("LOCAL_CACHE_MOUNT_PATH")),
		LocalCacheMountPathData1:          strings.TrimSpace(envOr("LOCAL_CACHE_MOUNT_PATH_DATA1", os.Getenv("LOCAL_CACHE_MOUNT_PATH"))),
		LocalCacheMountPathData2:          strings.TrimSpace(os.Getenv("LOCAL_CACHE_MOUNT_PATH_DATA2")),
		DataSpacesFSXAttributes:           strings.TrimSpace(os.Getenv("DATA_SPACES_FSX_VOLUME_ATTRIBUTES_JSON")),
		RayAPIDefaultImage:                strings.TrimSpace(os.Getenv("RAY_API_DEFAULT_IMAGE")),
		DataSpacesMountCapacity:           strings.TrimSpace(os.Getenv("DATA_SPACES_MOUNT_CAPACITY")),
		DataSpacesPublicRoot:              envOr("DATA_SPACES_PUBLIC_ROOT", domain.DefaultPublicDataRoot),
		IDCDataSpacesMountCapacity:        strings.TrimSpace(os.Getenv("IDC_DATA_SPACES_MOUNT_CAPACITY")),
		DatasetInternalPrefix:             envOr("DATASET_INTERNAL_PREFIX", domain.DefaultDatasetInternalPrefix),
		DatasetPublisherImage:             strings.TrimSpace(os.Getenv("DATASET_PUBLISHER_IMAGE")),
		DatasetPublisherImagePullPolicy:   strings.TrimSpace(envOr("DATASET_PUBLISHER_IMAGE_PULL_POLICY", "IfNotPresent")),
		DatasetPublisherSourceBucket:      strings.TrimSpace(envOr("DATASET_PUBLISHER_SOURCE_BUCKET", os.Getenv("TOS_BUCKET"))),
		DatasetPublisherTargetBucket:      strings.TrimSpace(envOr("DATASET_PUBLISHER_TARGET_BUCKET", envOr("DATASET_PUBLISHER_SOURCE_BUCKET", os.Getenv("TOS_BUCKET")))),
		DatasetPublisherTOSEndpoint:       configuredDatasetPublisherEndpoint(),
		DatasetPublisherTOSRegion:         strings.TrimSpace(envOr("DATASET_PUBLISHER_REGION", os.Getenv("TOS_REGION"))),
		DatasetPublisherServiceAccount:    strings.TrimSpace(os.Getenv("DATASET_PUBLISHER_SERVICE_ACCOUNT")),
		DatasetPublisherIRSARoleTRN:       strings.TrimSpace(os.Getenv("DATASET_PUBLISHER_IRSA_ROLE_TRN")),
		DatasetPublisherCredentialSecret:  strings.TrimSpace(os.Getenv("DATASET_PUBLISHER_CREDENTIAL_SECRET")),
		DatasetPublisherQueueName:         strings.TrimSpace(os.Getenv("DATASET_PUBLISHER_QUEUE_NAME")),
		DatasetPublisherPriorityClassName: strings.TrimSpace(os.Getenv("DATASET_PUBLISHER_PRIORITY_CLASS_NAME")),
		DatasetPublisherWorkingDirectory:  strings.TrimSpace(envOr("DATASET_PUBLISHER_WORKING_DIRECTORY", "/tmp/raytrain-publisher")),
		DatasetPublisherSourceIndexName:   strings.TrimSpace(envOr("DATASET_PUBLISHER_SOURCE_INDEX_NAME", ".raytrain/trusted-index-v2.pkl")),
		DatasetPublisherCPURequest:        strings.TrimSpace(envOr("DATASET_PUBLISHER_CPU_REQUEST", "1000m")),
		DatasetPublisherCPULimit:          strings.TrimSpace(envOr("DATASET_PUBLISHER_CPU_LIMIT", "4000m")),
		DatasetPublisherMemoryRequest:     strings.TrimSpace(envOr("DATASET_PUBLISHER_MEMORY_REQUEST", "2Gi")),
		DatasetPublisherMemoryLimit:       strings.TrimSpace(envOr("DATASET_PUBLISHER_MEMORY_LIMIT", "8Gi")),
		RayVersion:                        envOr("RAY_VERSION", "2.35.0"),
		RayTrainManagedTenants:            splitUniqueList(os.Getenv("RAY_TRAIN_MANAGED_TENANTS")),
		RayTrainCanaryTenants:             splitUniqueList(os.Getenv("RAY_TRAIN_CANARY_TENANTS")),
		RayJobClusterSpecField:            envOr("KUBERAY_RAYJOB_CLUSTER_SPEC_FIELD", "rayClusterSpec"),
		RayJobServiceAccount:              os.Getenv("RAY_JOB_SERVICE_ACCOUNT"),
		ImagePullSecrets:                  splitList(os.Getenv("IMAGE_PULL_SECRETS")),
		SourceMaterializerImage:           os.Getenv("SOURCE_MATERIALIZER_IMAGE"),
		WorkspaceImage:                    os.Getenv("WORKSPACE_IMAGE"),
		CORSOrigins:                       splitList(os.Getenv("CORS_ORIGINS")),
		RayImageAllowlist:                 splitList(os.Getenv("RAY_IMAGE_ALLOWLIST")),
		GitAllowlist:                      splitList(os.Getenv("GIT_ALLOWLIST")),
		TOSBucket:                         os.Getenv("TOS_BUCKET"),
		TOSEndpoint:                       os.Getenv("TOS_ENDPOINT"),
		TOSRegion:                         os.Getenv("TOS_REGION"),
		TOSAccessKey:                      os.Getenv("TOS_ACCESS_KEY"),
		TOSSecretKey:                      os.Getenv("TOS_SECRET_KEY"),
		TOSSecurityToken:                  os.Getenv("TOS_SECURITY_TOKEN"),
		TOSSecretName:                     os.Getenv("TOS_SECRET_NAME"),
		RayAPISpoolDir:                    strings.TrimSpace(os.Getenv("RAY_API_SPOOL_DIR")),
	}
	if cfg.RayAPISpoolDir == "" && cfg.AppEnv != "production" {
		cfg.RayAPISpoolDir = os.TempDir()
	}
	// Keep the original single-disk fields as read-only data1 aliases during a
	// rolling upgrade. Rendering uses the explicit dual-disk fields below.
	cfg.LocalCacheStorageClass = cfg.LocalCacheStorageClassData1
	cfg.LocalCacheMountPath = cfg.LocalCacheMountPathData1

	var err error
	if cfg.DatasetInternalPrefix, err = domain.NormalizeDatasetInternalPrefix(cfg.DatasetInternalPrefix); err != nil {
		return Config{}, fmt.Errorf("DATASET_INTERNAL_PREFIX is invalid: %w", err)
	}
	if cfg.MigrationsOnly, err = parseBool("MIGRATIONS_ONLY", false); err != nil {
		return Config{}, err
	}
	if cfg.MigrationsOnly {
		if strings.TrimSpace(cfg.DatabaseURL) == "" {
			return Config{}, fmt.Errorf("DATABASE_URL is required when MIGRATIONS_ONLY is true")
		}
		return cfg, nil
	}
	if cfg.OIDCRequired, err = parseBool("OIDC_REQUIRED", cfg.AppEnv == "production"); err != nil {
		return Config{}, err
	}
	if cfg.PATEnabled, err = parseBool("PAT_ENABLED", true); err != nil {
		return Config{}, err
	}
	// Local accounts stay available by default so the platform can be operated
	// and tested without an external identity provider.
	if cfg.LocalAuthEnabled, err = parseBool("LOCAL_AUTH_ENABLED", true); err != nil {
		return Config{}, err
	}
	if cfg.LocalSessionHours, err = parseInt("LOCAL_SESSION_HOURS", 12); err != nil {
		return Config{}, err
	}
	if cfg.TrainingNodeSelector, err = parseLabelSelector("TRAINING_NODE_SELECTOR"); err != nil {
		return Config{}, err
	}
	// Kueue cannot discover capacity, so the platform keeps the admission
	// budget aligned with the labelled training nodes by default.
	if cfg.KueueAutoQuota, err = parseBool("KUEUE_AUTO_QUOTA", true); err != nil {
		return Config{}, err
	}
	// Job size ceilings track the fleet; raising them needs no rebuild.
	if cfg.MaxWorkerReplicas, err = parseInt("MAX_WORKER_REPLICAS", 3); err != nil {
		return Config{}, err
	}
	if cfg.MaxGPUsPerWorker, err = parseInt("MAX_GPUS_PER_WORKER", 8); err != nil {
		return Config{}, err
	}
	if cfg.MaxTotalGPUs, err = parseInt("MAX_TOTAL_GPUS", 24); err != nil {
		return Config{}, err
	}
	if cfg.LocalCacheEnabled, err = parseBool("LOCAL_CACHE_ENABLED", false); err != nil {
		return Config{}, err
	}
	if cfg.RayTrainManagedEnabled, err = parseBool("RAY_TRAIN_MANAGED_ENABLED", false); err != nil {
		return Config{}, err
	}
	if cfg.RayTrainCanaryEnabled, err = parseBool("RAY_TRAIN_CANARY_ENABLED", false); err != nil {
		return Config{}, err
	}
	if cfg.MLflowEnabled, err = parseBool("MLFLOW_ENABLED", false); err != nil {
		return Config{}, err
	}
	if cfg.MLflowDashboardEnabled, err = parseBool("MLFLOW_DASHBOARD_ENABLED", false); err != nil {
		return Config{}, err
	}
	if cfg.MLflowDashboardEnabled {
		if cfg.MLflowDashboardSessionHours, err = parseInt("MLFLOW_DASHBOARD_SESSION_HOURS", 8); err != nil {
			return Config{}, err
		}
	}
	if cfg.DataSpacesEnabled, err = parseBool("DATA_SPACES_ENABLED", false); err != nil {
		return Config{}, err
	}
	if cfg.TOSObjectSetQuotasEnabled, err = parseBool("TOS_OBJECT_SET_QUOTAS_ENABLED", false); err != nil {
		return Config{}, err
	}
	if cfg.IDCDataSpacesEnabled, err = parseBool("IDC_DATA_SPACES_ENABLED", false); err != nil {
		return Config{}, err
	}
	if cfg.DatasetVersioningEnabled, err = parseBool("DATASET_VERSIONING_ENABLED", false); err != nil {
		return Config{}, err
	}
	if cfg.RayDataStreamingEnabled, err = parseBool("RAY_DATA_STREAMING_ENABLED", false); err != nil {
		return Config{}, err
	}
	if cfg.DatasetPublisherEnabled, err = parseBool("DATASET_PUBLISHER_ENABLED", false); err != nil {
		return Config{}, err
	}
	if cfg.DatasetPublisherDistributedEnabled, err = parseBool("DATASET_PUBLISHER_DISTRIBUTED_ENABLED", false); err != nil {
		return Config{}, err
	}
	if cfg.DatasetPublisherNodeSelector, err = parseLabelSelector("DATASET_PUBLISHER_NODE_SELECTOR"); err != nil {
		return Config{}, err
	}
	if cfg.DatasetPublisherPreferredNodeSelector, err = parseLabelSelector("DATASET_PUBLISHER_PREFERRED_NODE_SELECTOR"); err != nil {
		return Config{}, err
	}
	if cfg.DatasetPublisherTolerations, err = parseDatasetPublisherTolerations(); err != nil {
		return Config{}, err
	}
	if cfg.DatasetPublisherClientMaxAttempts, err = parseInt("DATASET_PUBLISHER_CLIENT_MAX_ATTEMPTS", 3); err != nil {
		return Config{}, err
	}
	if cfg.DatasetPublisherJobBackoffLimit, err = parseInt("DATASET_PUBLISHER_JOB_BACKOFF_LIMIT", 3); err != nil {
		return Config{}, err
	}
	if cfg.DatasetPublisherJobActiveDeadlineSeconds, err = parseInt("DATASET_PUBLISHER_JOB_ACTIVE_DEADLINE_SECONDS", 7*24*60*60); err != nil {
		return Config{}, err
	}
	if cfg.DatasetPublisherJobTTLSeconds, err = parseInt("DATASET_PUBLISHER_JOB_TTL_SECONDS", 24*60*60); err != nil {
		return Config{}, err
	}
	if cfg.DatasetPublisherInitialRetrySeconds, err = parseInt("DATASET_PUBLISHER_INITIAL_RETRY_SECONDS", 1); err != nil {
		return Config{}, err
	}
	if cfg.DatasetPublisherMaximumRetrySeconds, err = parseInt("DATASET_PUBLISHER_MAXIMUM_RETRY_SECONDS", 30); err != nil {
		return Config{}, err
	}
	if cfg.DatasetPublisherPollIntervalSeconds, err = parseInt("DATASET_PUBLISHER_POLL_INTERVAL_SECONDS", 10); err != nil {
		return Config{}, err
	}
	if cfg.DatasetPublisherPartitionCount, err = parseInt("DATASET_PUBLISHER_PARTITION_COUNT", 256); err != nil {
		return Config{}, err
	}
	if cfg.DatasetPublisherMaxParallelism, err = parseInt("DATASET_PUBLISHER_MAX_PARALLELISM", 4); err != nil {
		return Config{}, err
	}
	if cfg.DatasetPublisherPartitionLeaseSeconds, err = parseInt("DATASET_PUBLISHER_PARTITION_LEASE_SECONDS", 900); err != nil {
		return Config{}, err
	}
	if cfg.DatasetPublisherMaxPartitionAttempts, err = parseInt("DATASET_PUBLISHER_MAX_PARTITION_ATTEMPTS", 3); err != nil {
		return Config{}, err
	}
	if raw := strings.TrimSpace(os.Getenv("IDC_DATA_SPACES_SOURCES_JSON")); raw != "" {
		if err := json.Unmarshal([]byte(raw), &cfg.IDCDataSpaceSources); err != nil {
			return Config{}, fmt.Errorf("IDC_DATA_SPACES_SOURCES_JSON must be a JSON object of NFS sources: %w", err)
		}
	}
	cfg.BootstrapAdminUsername = envOr("BOOTSTRAP_ADMIN_USERNAME", "admin")
	cfg.BootstrapAdminPassword = os.Getenv("BOOTSTRAP_ADMIN_PASSWORD")
	cfg.BootstrapAdminTenant = envOr("BOOTSTRAP_ADMIN_TENANT", "local")
	if cfg.SourceArtifactsEnabled, err = parseBool("SOURCE_ARTIFACTS_ENABLED", cfg.AppEnv == "production"); err != nil {
		return Config{}, err
	}
	if cfg.SourceArtifactMaxPending, err = parseInt("SOURCE_ARTIFACT_MAX_PENDING", 10); err != nil {
		return Config{}, err
	}
	if cfg.SourceArtifactQuotaBytes, err = parseInt64("SOURCE_ARTIFACT_QUOTA_BYTES", 100*1024*1024*1024); err != nil {
		return Config{}, err
	}
	if cfg.PersonalStorageDefaultQuotaBytes, err = parseStorageQuantity("PERSONAL_STORAGE_DEFAULT_QUOTA", 100*1024*1024*1024); err != nil {
		return Config{}, err
	}
	if cfg.PersonalStorageMaxQuotaBytes, err = parseStorageQuantity("PERSONAL_STORAGE_MAX_QUOTA", 10*1024*1024*1024*1024); err != nil {
		return Config{}, err
	}
	if cfg.RayAPISpoolSizeBytes, err = parseInt64("RAY_API_SPOOL_SIZE_BYTES", 3*1024*1024*1024); err != nil {
		return Config{}, err
	}
	if cfg.RayAPIUploadMaxConcurrent, err = parseInt("RAY_API_UPLOAD_MAX_CONCURRENT", 1); err != nil {
		return Config{}, err
	}
	if cfg.RayAPIUploadRateLimit, err = parseInt("RAY_API_UPLOAD_RATE_LIMIT", 20); err != nil {
		return Config{}, err
	}
	if cfg.PATDefaultExpiryDays, err = parseInt("PAT_DEFAULT_EXPIRY_DAYS", 90); err != nil {
		return Config{}, err
	}
	if cfg.PATMaxExpiryDays, err = parseInt("PAT_MAX_EXPIRY_DAYS", 365); err != nil {
		return Config{}, err
	}
	if cfg.IDCStorageEnabled, err = parseBool("IDC_STORAGE_ENABLED", false); err != nil {
		return Config{}, err
	}
	if cfg.DemoMode, err = parseBool("DEMO_MODE", false); err != nil {
		return Config{}, err
	}

	if err := validateProduction(cfg); err != nil {
		return Config{}, err
	}
	if cfg.IDCStorageEnabled && cfg.IDCExistingClaim == "" {
		return Config{}, fmt.Errorf("IDC storage requires IDC_EXISTING_CLAIM; dynamic PVC provisioning is disabled in V1")
	}
	if err := validateLocalCacheConfig(cfg); err != nil {
		return Config{}, err
	}
	if err := validateMLflowConfig(cfg); err != nil {
		return Config{}, err
	}
	if err := validateMLflowDashboardConfig(cfg); err != nil {
		return Config{}, err
	}
	if err := validateDataSpaceConfig(cfg); err != nil {
		return Config{}, err
	}
	if err := validateObjectSetQuotaConfig(cfg); err != nil {
		return Config{}, err
	}
	if err := validateIDCDataSpaceConfig(cfg); err != nil {
		return Config{}, err
	}
	if err := validatePATConfig(cfg); err != nil {
		return Config{}, err
	}
	if err := validateSourceArtifactConfig(cfg); err != nil {
		return Config{}, err
	}
	if cfg.RayDataStreamingEnabled {
		if !cfg.DatasetVersioningEnabled {
			return Config{}, fmt.Errorf("RAY_DATA_STREAMING_ENABLED requires DATASET_VERSIONING_ENABLED")
		}
		if !strings.HasPrefix(cfg.DatasetInternalPrefix, "ray-train/") {
			return Config{}, fmt.Errorf("DATASET_INTERNAL_PREFIX must remain below ray-train when RAY_DATA_STREAMING_ENABLED is true")
		}
	}
	if err := validateDatasetPublisherConfig(cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func validateDatasetPublisherConfig(cfg Config) error {
	if cfg.DatasetPublisherDistributedEnabled && !cfg.DatasetPublisherEnabled {
		return fmt.Errorf("DATASET_PUBLISHER_DISTRIBUTED_ENABLED requires DATASET_PUBLISHER_ENABLED")
	}
	if !cfg.DatasetPublisherEnabled {
		return nil
	}
	if !cfg.DatasetVersioningEnabled {
		return fmt.Errorf("DATASET_PUBLISHER_ENABLED requires DATASET_VERSIONING_ENABLED")
	}
	checks := []struct{ value, name string }{
		{cfg.DatasetPublisherImage, "DATASET_PUBLISHER_IMAGE"},
		{cfg.DatasetPublisherSourceBucket, "DATASET_PUBLISHER_SOURCE_BUCKET"},
		{cfg.DatasetPublisherTargetBucket, "DATASET_PUBLISHER_TARGET_BUCKET"},
		{cfg.DatasetPublisherTOSEndpoint, "DATASET_PUBLISHER_ENDPOINT"},
		{cfg.DatasetPublisherTOSRegion, "DATASET_PUBLISHER_REGION"},
		{cfg.DatasetPublisherServiceAccount, "DATASET_PUBLISHER_SERVICE_ACCOUNT"},
		{cfg.DatasetPublisherQueueName, "DATASET_PUBLISHER_QUEUE_NAME"},
		{cfg.DatasetPublisherPriorityClassName, "DATASET_PUBLISHER_PRIORITY_CLASS_NAME"},
	}
	for _, check := range checks {
		if strings.TrimSpace(check.value) == "" {
			return fmt.Errorf("%s is required when DATASET_VERSIONING_ENABLED is true", check.name)
		}
	}
	if !pinnedImagePattern.MatchString(cfg.DatasetPublisherImage) {
		return fmt.Errorf("DATASET_PUBLISHER_IMAGE must be pinned by sha256 digest")
	}
	if cfg.DatasetPublisherImagePullPolicy != "Always" && cfg.DatasetPublisherImagePullPolicy != "IfNotPresent" && cfg.DatasetPublisherImagePullPolicy != "Never" {
		return fmt.Errorf("DATASET_PUBLISHER_IMAGE_PULL_POLICY must be Always, IfNotPresent, or Never")
	}
	if !validDatasetPublisherBucket(cfg.DatasetPublisherSourceBucket) {
		return fmt.Errorf("DATASET_PUBLISHER_SOURCE_BUCKET must be a bare TOS bucket name")
	}
	if !validDatasetPublisherBucket(cfg.DatasetPublisherTargetBucket) {
		return fmt.Errorf("DATASET_PUBLISHER_TARGET_BUCKET must be a bare TOS bucket name")
	}
	if !validDatasetPublisherRegion(cfg.DatasetPublisherTOSRegion) {
		return fmt.Errorf("DATASET_PUBLISHER_REGION must be a lowercase cloud region")
	}
	if !validDatasetPublisherEndpoint(cfg.DatasetPublisherTOSEndpoint, cfg.DatasetPublisherTOSRegion) {
		return fmt.Errorf("DATASET_PUBLISHER_ENDPOINT must be an approved Volcengine TOS DNS hostname for DATASET_PUBLISHER_REGION")
	}
	if cfg.DatasetPublisherIRSARoleTRN != "" && !datasetPublisherIRSARoleTRNPattern.MatchString(cfg.DatasetPublisherIRSARoleTRN) {
		return fmt.Errorf("DATASET_PUBLISHER_IRSA_ROLE_TRN must be a valid Volcengine IAM role TRN")
	}
	if cfg.DatasetPublisherCredentialSecret != "" && !isDNSSubdomain(cfg.DatasetPublisherCredentialSecret) {
		return fmt.Errorf("DATASET_PUBLISHER_CREDENTIAL_SECRET must be a valid Kubernetes name")
	}
	if cfg.DatasetPublisherCredentialSecret != "" && cfg.DatasetPublisherIRSARoleTRN != "" {
		return fmt.Errorf("dataset publisher credentials must use either IRSA or a Secret, not both")
	}
	for _, named := range []struct{ value, name string }{
		{cfg.DatasetPublisherServiceAccount, "DATASET_PUBLISHER_SERVICE_ACCOUNT"},
		{cfg.DatasetPublisherQueueName, "DATASET_PUBLISHER_QUEUE_NAME"},
		{cfg.DatasetPublisherPriorityClassName, "DATASET_PUBLISHER_PRIORITY_CLASS_NAME"},
	} {
		if !isDNSSubdomain(named.value) {
			return fmt.Errorf("%s must be a valid Kubernetes name", named.name)
		}
	}
	if !cleanAbsoluteDatasetPublisherDirectory(cfg.DatasetPublisherWorkingDirectory) {
		return fmt.Errorf("DATASET_PUBLISHER_WORKING_DIRECTORY must be a clean non-root absolute path")
	}
	if !cleanDatasetPublisherRelativePath(cfg.DatasetPublisherSourceIndexName) {
		return fmt.Errorf("DATASET_PUBLISHER_SOURCE_INDEX_NAME must be a clean relative path")
	}
	parsedQuantities := make(map[string]resource.Quantity, 4)
	for _, quantity := range []struct{ value, name string }{
		{cfg.DatasetPublisherCPURequest, "DATASET_PUBLISHER_CPU_REQUEST"},
		{cfg.DatasetPublisherCPULimit, "DATASET_PUBLISHER_CPU_LIMIT"},
		{cfg.DatasetPublisherMemoryRequest, "DATASET_PUBLISHER_MEMORY_REQUEST"},
		{cfg.DatasetPublisherMemoryLimit, "DATASET_PUBLISHER_MEMORY_LIMIT"},
	} {
		parsed, err := resource.ParseQuantity(quantity.value)
		if err != nil || parsed.Sign() <= 0 {
			return fmt.Errorf("%s must be a positive Kubernetes quantity", quantity.name)
		}
		parsedQuantities[quantity.name] = parsed
	}
	if request, limit := parsedQuantities["DATASET_PUBLISHER_CPU_REQUEST"], parsedQuantities["DATASET_PUBLISHER_CPU_LIMIT"]; request.Cmp(limit) > 0 {
		return fmt.Errorf("DATASET_PUBLISHER_CPU_LIMIT must be greater than or equal to DATASET_PUBLISHER_CPU_REQUEST")
	}
	if request, limit := parsedQuantities["DATASET_PUBLISHER_MEMORY_REQUEST"], parsedQuantities["DATASET_PUBLISHER_MEMORY_LIMIT"]; request.Cmp(limit) > 0 {
		return fmt.Errorf("DATASET_PUBLISHER_MEMORY_LIMIT must be greater than or equal to DATASET_PUBLISHER_MEMORY_REQUEST")
	}
	if cfg.DatasetPublisherClientMaxAttempts < 1 || cfg.DatasetPublisherClientMaxAttempts > 10 {
		return fmt.Errorf("DATASET_PUBLISHER_CLIENT_MAX_ATTEMPTS must be between 1 and 10")
	}
	if cfg.DatasetPublisherJobBackoffLimit < 0 || cfg.DatasetPublisherJobBackoffLimit > 10 {
		return fmt.Errorf("DATASET_PUBLISHER_JOB_BACKOFF_LIMIT must be between 0 and 10")
	}
	const maximumLifecycleSeconds = 30 * 24 * 60 * 60
	if cfg.DatasetPublisherJobActiveDeadlineSeconds < 1 || cfg.DatasetPublisherJobActiveDeadlineSeconds > maximumLifecycleSeconds {
		return fmt.Errorf("DATASET_PUBLISHER_JOB_ACTIVE_DEADLINE_SECONDS must be between 1 and 2592000")
	}
	if cfg.DatasetPublisherJobTTLSeconds < 1 || cfg.DatasetPublisherJobTTLSeconds > maximumLifecycleSeconds {
		return fmt.Errorf("DATASET_PUBLISHER_JOB_TTL_SECONDS must be between 1 and 2592000")
	}
	if cfg.DatasetPublisherInitialRetrySeconds < 0 || cfg.DatasetPublisherInitialRetrySeconds > cfg.DatasetPublisherMaximumRetrySeconds {
		return fmt.Errorf("DATASET_PUBLISHER_INITIAL_RETRY_SECONDS must be nonnegative and no greater than DATASET_PUBLISHER_MAXIMUM_RETRY_SECONDS")
	}
	if cfg.DatasetPublisherMaximumRetrySeconds < 1 || cfg.DatasetPublisherMaximumRetrySeconds > 300 {
		return fmt.Errorf("DATASET_PUBLISHER_MAXIMUM_RETRY_SECONDS must be between 1 and 300")
	}
	if cfg.DatasetPublisherPollIntervalSeconds < 1 || cfg.DatasetPublisherPollIntervalSeconds > 300 {
		return fmt.Errorf("DATASET_PUBLISHER_POLL_INTERVAL_SECONDS must be between 1 and 300")
	}
	if cfg.DatasetPublisherPartitionCount < 1 || cfg.DatasetPublisherPartitionCount > 100000 {
		return fmt.Errorf("DATASET_PUBLISHER_PARTITION_COUNT must be between 1 and 100000")
	}
	if cfg.DatasetPublisherMaxParallelism < 1 || cfg.DatasetPublisherMaxParallelism > cfg.DatasetPublisherPartitionCount {
		return fmt.Errorf("DATASET_PUBLISHER_MAX_PARALLELISM must be between 1 and DATASET_PUBLISHER_PARTITION_COUNT")
	}
	if cfg.DatasetPublisherPartitionLeaseSeconds < 60 || cfg.DatasetPublisherPartitionLeaseSeconds > 24*60*60 {
		return fmt.Errorf("DATASET_PUBLISHER_PARTITION_LEASE_SECONDS must be between 60 and 86400")
	}
	if cfg.DatasetPublisherMaxPartitionAttempts < 1 || cfg.DatasetPublisherMaxPartitionAttempts > 10 {
		return fmt.Errorf("DATASET_PUBLISHER_MAX_PARTITION_ATTEMPTS must be between 1 and 10")
	}
	return nil
}

func normalizeDatasetPublisherEndpoint(raw string) string {
	value := strings.TrimSpace(raw)
	value = strings.TrimPrefix(value, "https://")
	value = strings.TrimPrefix(value, "http://")
	return strings.TrimSuffix(value, "/")
}

func configuredDatasetPublisherEndpoint() string {
	if configured := strings.TrimSpace(os.Getenv("DATASET_PUBLISHER_ENDPOINT")); configured != "" {
		return configured
	}
	return normalizeDatasetPublisherEndpoint(os.Getenv("TOS_ENDPOINT"))
}

func validDatasetPublisherBucket(value string) bool {
	if len(value) < 3 || len(value) > 63 || strings.TrimSpace(value) != value || value[0] == '-' || value[len(value)-1] == '-' {
		return false
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || character == '-' {
			continue
		}
		return false
	}
	return true
}

func validDatasetPublisherEndpoint(value, region string) bool {
	if value == "" || value != strings.ToLower(value) || strings.ContainsAny(value, "/\\:@?#") {
		return false
	}
	if !validDatasetPublisherRegion(region) {
		return false
	}

	officialSuffix := ""
	switch {
	case strings.HasSuffix(value, ".ivolces.com"):
		officialSuffix = ".ivolces.com"
	case strings.HasSuffix(value, ".volces.com"):
		officialSuffix = ".volces.com"
	default:
		return false
	}

	endpointLabels := strings.Split(strings.TrimSuffix(value, officialSuffix), ".")
	regionalService := "tos-" + region
	regionalS3Service := "tos-s3-" + region
	if len(endpointLabels) == 1 {
		return endpointLabels[0] == regionalService || endpointLabels[0] == regionalS3Service
	}
	if len(endpointLabels) == 2 {
		return validDatasetPublisherBucket(endpointLabels[0]) &&
			(endpointLabels[1] == regionalService || endpointLabels[1] == regionalS3Service)
	}
	if officialSuffix == ".ivolces.com" && len(endpointLabels) == 3 {
		return datasetPublisherPrivateEndpointPattern.MatchString(endpointLabels[0]) &&
			endpointLabels[1] == region && endpointLabels[2] == "tos"
	}
	return false
}

func validDatasetPublisherRegion(value string) bool {
	return strings.Contains(value, "-") && isDNSSubdomain(value)
}

func cleanAbsoluteDatasetPublisherDirectory(value string) bool {
	return strings.HasPrefix(value, "/") && value != "/" && path.Clean(value) == value && strings.TrimSpace(value) == value
}

func cleanDatasetPublisherRelativePath(value string) bool {
	if value == "" || path.IsAbs(value) || path.Clean(value) != value || value == "." || strings.TrimSpace(value) != value || strings.ContainsAny(value, `\\:@?#%`) {
		return false
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
	}
	return true
}

func validateMLflowConfig(cfg Config) error {
	if !cfg.MLflowEnabled {
		return nil
	}
	if err := validateInternalServiceURL("MLFLOW_TRACKING_URL", cfg.MLflowTrackingURL); err != nil {
		return err
	}
	if err := validateInternalServiceURL("MLFLOW_INGEST_URL", cfg.MLflowIngestURL); err != nil {
		return err
	}
	if strings.TrimRight(cfg.MLflowTrackingURL, "/") == strings.TrimRight(cfg.MLflowIngestURL, "/") {
		return fmt.Errorf("MLFLOW_INGEST_URL must use a separate write-only gateway")
	}
	if len(cfg.PATPepper) < 32 {
		return fmt.Errorf("PAT_PEPPER must contain at least 32 bytes when MLFLOW_ENABLED is true")
	}
	prefix := strings.Trim(strings.TrimSpace(cfg.MLflowExperimentPrefix), "-")
	if prefix == "" || prefix != cfg.MLflowExperimentPrefix || !isDNSSubdomain(prefix) {
		return fmt.Errorf("MLFLOW_EXPERIMENT_PREFIX must be a lowercase DNS label")
	}
	return nil
}

func validateMLflowDashboardConfig(cfg Config) error {
	if !cfg.MLflowDashboardEnabled {
		return nil
	}
	if !cfg.MLflowEnabled {
		return fmt.Errorf("MLFLOW_DASHBOARD_ENABLED requires MLFLOW_ENABLED")
	}
	if strings.TrimSpace(cfg.MLflowTrackingURL) == "" {
		return fmt.Errorf("MLFLOW_TRACKING_URL is required when MLFLOW_DASHBOARD_ENABLED is true")
	}
	if len(cfg.PATPepper) < 32 {
		return fmt.Errorf("PAT_PEPPER must contain at least 32 bytes when MLFLOW_DASHBOARD_ENABLED is true")
	}
	if cfg.MLflowDashboardSessionHours < 1 || cfg.MLflowDashboardSessionHours > 24 {
		return fmt.Errorf("MLFLOW_DASHBOARD_SESSION_HOURS must be between 1 and 24")
	}
	origin, err := url.Parse(cfg.MLflowPublicOrigin)
	if err != nil || origin.Scheme != "https" || origin.Host == "" || origin.Hostname() == "" || origin.User != nil || origin.Path != "" || origin.RawPath != "" || origin.RawQuery != "" || origin.Fragment != "" || origin.Opaque != "" || origin.ForceQuery {
		return fmt.Errorf("MLFLOW_PUBLIC_ORIGIN must be an HTTPS origin with scheme and host only")
	}
	return nil
}

func validateInternalServiceURL(name, value string) error {
	endpoint, err := url.Parse(strings.TrimSpace(value))
	if err != nil || (endpoint.Scheme != "http" && endpoint.Scheme != "https") || endpoint.Hostname() == "" {
		return fmt.Errorf("%s must be an in-cluster HTTP(S) URL when MLFLOW_ENABLED is true", name)
	}
	host := strings.ToLower(endpoint.Hostname())
	if !(strings.HasSuffix(host, ".svc") || strings.HasSuffix(host, ".svc.cluster.local")) {
		return fmt.Errorf("%s must target an in-cluster Kubernetes Service", name)
	}
	pathAllowed := endpoint.Path == "" || endpoint.Path == "/" || (name == "MLFLOW_TRACKING_URL" && strings.TrimRight(endpoint.Path, "/") == "/mlflow")
	if endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || !pathAllowed {
		return fmt.Errorf("%s must not contain credentials, query, fragment, or an unsupported path", name)
	}
	return nil
}

func validateDataSpaceConfig(cfg Config) error {
	if !cfg.DataSpacesEnabled {
		return nil
	}
	if strings.TrimSpace(cfg.DataSpacesMountCapacity) == "" {
		return fmt.Errorf("DATA_SPACES_MOUNT_CAPACITY is required when DATA_SPACES_ENABLED is true")
	}
	if _, err := domain.NormalizePublicDataRoot(cfg.DataSpacesPublicRoot); err != nil {
		return fmt.Errorf("DATA_SPACES_PUBLIC_ROOT: %w", err)
	}
	quantity, err := resource.ParseQuantity(cfg.DataSpacesMountCapacity)
	if err != nil || quantity.Sign() <= 0 {
		return fmt.Errorf("DATA_SPACES_MOUNT_CAPACITY must be a positive Kubernetes storage quantity")
	}
	attributes := map[string]string{}
	if err := json.Unmarshal([]byte(cfg.DataSpacesFSXAttributes), &attributes); err != nil {
		return fmt.Errorf("DATA_SPACES_FSX_VOLUME_ATTRIBUTES_JSON must be a JSON object with string values")
	}
	for _, required := range []string{"type", "bucket", "server", "region"} {
		if strings.TrimSpace(attributes[required]) == "" {
			return fmt.Errorf("DATA_SPACES_FSX_VOLUME_ATTRIBUTES_JSON requires %s", required)
		}
	}
	if attributes["type"] != "TOS" {
		return fmt.Errorf("DATA_SPACES_FSX_VOLUME_ATTRIBUTES_JSON requires type TOS")
	}
	endpoint, err := parseDataSpaceEndpoint(cfg.TOSEndpoint)
	if err != nil {
		return err
	}
	if attributes["bucket"] != cfg.TOSBucket || attributes["server"] != endpoint {
		return fmt.Errorf("DATA_SPACES_FSX_VOLUME_ATTRIBUTES_JSON must use the same bucket and server as the backend TOS configuration")
	}
	for key := range attributes {
		if key == "path" || strings.EqualFold(key, "secretName") || strings.EqualFold(key, "secretNamespace") {
			return fmt.Errorf("DATA_SPACES_FSX_VOLUME_ATTRIBUTES_JSON must not contain a path or secret reference")
		}
	}
	return nil
}

// validateObjectSetQuotaConfig keeps the hard storage quota capability
// explicitly opt-in. The same backend-only TOS credential used to create
// signed uploads performs ObjectSet governance; Ray Pods never receive it.
func validateObjectSetQuotaConfig(cfg Config) error {
	if !cfg.TOSObjectSetQuotasEnabled {
		return nil
	}
	checks := []struct{ value, name string }{
		{cfg.TOSEndpoint, "TOS_ENDPOINT"}, {cfg.TOSRegion, "TOS_REGION"},
		{cfg.TOSBucket, "TOS_BUCKET"}, {cfg.TOSAccessKey, "TOS_ACCESS_KEY"},
		{cfg.TOSSecretKey, "TOS_SECRET_KEY"},
	}
	for _, check := range checks {
		if strings.TrimSpace(check.value) == "" {
			return fmt.Errorf("%s is required when TOS_OBJECT_SET_QUOTAS_ENABLED is true", check.name)
		}
	}
	const gib = int64(1024 * 1024 * 1024)
	const hundredPiB = int64(100 * 1024 * 1024 * 1024 * 1024 * 1024)
	if cfg.PersonalStorageDefaultQuotaBytes < gib || cfg.PersonalStorageDefaultQuotaBytes > hundredPiB {
		return fmt.Errorf("PERSONAL_STORAGE_DEFAULT_QUOTA must be between 1Gi and 100Pi")
	}
	if cfg.PersonalStorageMaxQuotaBytes < cfg.PersonalStorageDefaultQuotaBytes || cfg.PersonalStorageMaxQuotaBytes > hundredPiB {
		return fmt.Errorf("PERSONAL_STORAGE_MAX_QUOTA must be between PERSONAL_STORAGE_DEFAULT_QUOTA and 100Pi")
	}
	return nil
}

func validateIDCDataSpaceConfig(cfg Config) error {
	if !cfg.IDCDataSpacesEnabled {
		return nil
	}
	if cfg.IDCStorageEnabled {
		return fmt.Errorf("IDC_DATA_SPACES_ENABLED cannot be combined with deprecated IDC_STORAGE_ENABLED")
	}
	if strings.TrimSpace(cfg.IDCDataSpacesMountCapacity) == "" {
		return fmt.Errorf("IDC_DATA_SPACES_MOUNT_CAPACITY is required when IDC_DATA_SPACES_ENABLED is true")
	}
	quantity, err := resource.ParseQuantity(cfg.IDCDataSpacesMountCapacity)
	if err != nil || quantity.Sign() <= 0 {
		return fmt.Errorf("IDC_DATA_SPACES_MOUNT_CAPACITY must be a positive Kubernetes storage quantity")
	}
	for _, name := range []string{"original", "wellspiking", "shared", "spk-hybrid", "spk-ssd"} {
		source, ok := cfg.IDCDataSpaceSources[name]
		if !ok {
			return fmt.Errorf("IDC_DATA_SPACES_SOURCES_JSON requires %s", name)
		}
		if strings.TrimSpace(source.Server) == "" || strings.TrimSpace(source.Server) != source.Server || strings.ContainsAny(source.Server, " \t/\\@") {
			return fmt.Errorf("IDC_DATA_SPACES_SOURCES_JSON.%s.server must be a bare hostname or IP address", name)
		}
		if !strings.HasPrefix(source.Path, "/") || source.Path == "/" || strings.TrimSpace(source.Path) != source.Path || path.Clean(source.Path) != source.Path {
			return fmt.Errorf("IDC_DATA_SPACES_SOURCES_JSON.%s.path must be a clean non-root absolute export path", name)
		}
		if err := validateIDCMountOptions(source.MountOptions); err != nil {
			return fmt.Errorf("IDC_DATA_SPACES_SOURCES_JSON.%s.mountOptions: %w", name, err)
		}
	}
	return nil
}

func validateIDCMountOptions(options []string) error {
	allowed := map[string]bool{
		"ro": true, "hard": true, "noatime": true, "_netdev": true, "nofail": true,
		"vers=3": true, "timeo=600": true, "retrans=2": true,
		"rsize=1048576": true, "wsize=1048576": true,
	}
	seenRO := false
	for _, raw := range options {
		option := strings.TrimSpace(raw)
		if option == "" || option != raw || !allowed[option] {
			return fmt.Errorf("option %q is not in the read-only allowlist", raw)
		}
		seenRO = seenRO || option == "ro"
	}
	if len(options) > 0 && !seenRO {
		return fmt.Errorf("must include ro")
	}
	return nil
}

func parseDataSpaceEndpoint(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	value = strings.TrimPrefix(value, "https://")
	value = strings.TrimPrefix(value, "http://")
	if value == "" || strings.ContainsAny(value, "/?#@") {
		return "", fmt.Errorf("TOS_ENDPOINT must be an HTTPS origin when data spaces are enabled")
	}
	return value, nil
}

func validateLocalCacheConfig(cfg Config) error {
	if !cfg.LocalCacheEnabled {
		return nil
	}
	if cfg.LocalCacheStorageClassData1 == "" {
		return fmt.Errorf("LOCAL_CACHE_STORAGE_CLASS_DATA1 is required when LOCAL_CACHE_ENABLED is true")
	}
	if cfg.LocalCacheStorageClassData2 == "" {
		return fmt.Errorf("LOCAL_CACHE_STORAGE_CLASS_DATA2 is required when LOCAL_CACHE_ENABLED is true")
	}
	if cfg.LocalCacheSize == "" {
		return fmt.Errorf("LOCAL_CACHE_SIZE is required when LOCAL_CACHE_ENABLED is true")
	}
	if cfg.LocalCacheMountPathData1 == "" {
		return fmt.Errorf("LOCAL_CACHE_MOUNT_PATH_DATA1 is required when LOCAL_CACHE_ENABLED is true")
	}
	if cfg.LocalCacheMountPathData2 == "" {
		return fmt.Errorf("LOCAL_CACHE_MOUNT_PATH_DATA2 is required when LOCAL_CACHE_ENABLED is true")
	}
	if !isDNSSubdomain(cfg.LocalCacheStorageClassData1) {
		return fmt.Errorf("LOCAL_CACHE_STORAGE_CLASS_DATA1 must be a valid Kubernetes name")
	}
	if !isDNSSubdomain(cfg.LocalCacheStorageClassData2) {
		return fmt.Errorf("LOCAL_CACHE_STORAGE_CLASS_DATA2 must be a valid Kubernetes name")
	}
	if cfg.LocalCacheStorageClassData1 == cfg.LocalCacheStorageClassData2 {
		return fmt.Errorf("LOCAL_CACHE_STORAGE_CLASS_DATA1 and LOCAL_CACHE_STORAGE_CLASS_DATA2 must be different")
	}
	defaultSize, err := positiveStorageQuantity(cfg.LocalCacheSize)
	if err != nil {
		return fmt.Errorf("LOCAL_CACHE_SIZE must be a positive Kubernetes storage quantity")
	}
	maxSize, err := positiveStorageQuantity(cfg.LocalCacheMaxSize)
	if err != nil {
		return fmt.Errorf("LOCAL_CACHE_MAX_SIZE must be a positive Kubernetes storage quantity")
	}
	physicalMaximum := resource.MustParse("5Ti")
	if maxSize.Cmp(physicalMaximum) > 0 {
		return fmt.Errorf("LOCAL_CACHE_MAX_SIZE must not exceed 5Ti")
	}
	if len(cfg.LocalCacheAllowedSizes) == 0 {
		return fmt.Errorf("LOCAL_CACHE_ALLOWED_SIZES must contain at least one positive Kubernetes storage quantity")
	}
	allowed := make([]resource.Quantity, 0, len(cfg.LocalCacheAllowedSizes))
	defaultAllowed := false
	for _, configured := range cfg.LocalCacheAllowedSizes {
		quantity, err := positiveStorageQuantity(configured)
		if err != nil {
			return fmt.Errorf("LOCAL_CACHE_ALLOWED_SIZES must contain positive Kubernetes storage quantities")
		}
		if quantity.Cmp(maxSize) > 0 {
			return fmt.Errorf("LOCAL_CACHE_ALLOWED_SIZES entries must not exceed LOCAL_CACHE_MAX_SIZE")
		}
		const gib = int64(1024 * 1024 * 1024)
		if quantity.Value()%gib != 0 || (quantity.Value()/gib)%2 != 0 {
			return fmt.Errorf("LOCAL_CACHE_ALLOWED_SIZES entries must be even whole-GiB totals")
		}
		for _, existing := range allowed {
			if quantity.Cmp(existing) == 0 {
				return fmt.Errorf("LOCAL_CACHE_ALLOWED_SIZES entries must be unique")
			}
		}
		allowed = append(allowed, quantity)
		defaultAllowed = defaultAllowed || quantity.Cmp(defaultSize) == 0
	}
	if !defaultAllowed {
		return fmt.Errorf("LOCAL_CACHE_SIZE must belong to LOCAL_CACHE_ALLOWED_SIZES")
	}
	for name, mountPath := range map[string]string{
		"LOCAL_CACHE_MOUNT_PATH_DATA1": cfg.LocalCacheMountPathData1,
		"LOCAL_CACHE_MOUNT_PATH_DATA2": cfg.LocalCacheMountPathData2,
	} {
		if !strings.HasPrefix(mountPath, "/") || path.Clean(mountPath) != mountPath || mountPath == "/" {
			return fmt.Errorf("%s must be a clean absolute directory", name)
		}
		if mountPath == "/tmp/ray" || strings.HasPrefix(mountPath, "/tmp/ray/") {
			return fmt.Errorf("%s must not be inside Ray's default temporary directory", name)
		}
	}
	if cfg.LocalCacheMountPathData1 == cfg.LocalCacheMountPathData2 {
		return fmt.Errorf("LOCAL_CACHE_MOUNT_PATH_DATA1 and LOCAL_CACHE_MOUNT_PATH_DATA2 must be different")
	}
	return nil
}

func positiveStorageQuantity(value string) (resource.Quantity, error) {
	quantity, err := resource.ParseQuantity(strings.TrimSpace(value))
	if err != nil || quantity.Sign() <= 0 {
		return resource.Quantity{}, fmt.Errorf("quantity must be positive")
	}
	return quantity, nil
}

func isDNSSubdomain(value string) bool {
	if len(value) == 0 || len(value) > 253 {
		return false
	}
	for _, label := range strings.Split(value, ".") {
		if len(label) == 0 || len(label) > 63 {
			return false
		}
		for index, char := range label {
			if (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') || (char == '-' && index > 0 && index < len(label)-1) {
				continue
			}
			return false
		}
	}
	return true
}

func validateProduction(cfg Config) error {
	if cfg.AppEnv != "production" {
		return nil
	}
	if !cfg.OIDCRequired {
		return fmt.Errorf("OIDC_REQUIRED cannot be disabled in production")
	}
	checks := []struct{ value, message string }{
		{cfg.DatabaseURL, "DATABASE_URL is required in production"},
		{cfg.OIDCIssuerURL, "OIDC_ISSUER_URL is required in production"},
		{cfg.OIDCClientID, "OIDC_CLIENT_ID is required in production"},
		{cfg.OIDCAudience, "OIDC_AUDIENCE is required in production"},
		{cfg.SourceMaterializerImage, "SOURCE_MATERIALIZER_IMAGE is required in production"},
		{cfg.WorkspaceImage, "WORKSPACE_IMAGE is required in production"},
	}
	for _, check := range checks {
		if check.value == "" {
			return fmt.Errorf("%s", check.message)
		}
	}
	if !pinnedImagePattern.MatchString(cfg.SourceMaterializerImage) {
		return fmt.Errorf("SOURCE_MATERIALIZER_IMAGE must be pinned by sha256 digest")
	}
	if !pinnedImagePattern.MatchString(cfg.WorkspaceImage) {
		return fmt.Errorf("WORKSPACE_IMAGE must be pinned by sha256 digest")
	}
	if len(cfg.RayImageAllowlist) == 0 {
		return fmt.Errorf("RAY_IMAGE_ALLOWLIST must contain at least one allowed image prefix")
	}
	if len(cfg.GitAllowlist) == 0 {
		return fmt.Errorf("GIT_ALLOWLIST must contain at least one allowed host")
	}
	if cfg.KubeConfig == "" && os.Getenv("KUBERNETES_SERVICE_HOST") == "" {
		return fmt.Errorf("Kubernetes configuration is required in production")
	}
	if cfg.DemoMode {
		return fmt.Errorf("DEMO_MODE cannot be enabled in production")
	}
	return nil
}

func validatePATConfig(cfg Config) error {
	if cfg.PATDefaultExpiryDays < 1 || cfg.PATDefaultExpiryDays > cfg.PATMaxExpiryDays {
		return fmt.Errorf("PAT_DEFAULT_EXPIRY_DAYS must be between 1 and PAT_MAX_EXPIRY_DAYS")
	}
	if cfg.PATMaxExpiryDays < 1 || cfg.PATMaxExpiryDays > 365 {
		return fmt.Errorf("PAT_MAX_EXPIRY_DAYS must be between 1 and 365")
	}
	if cfg.PATEnabled && len([]byte(cfg.PATPepper)) < 32 {
		return fmt.Errorf("PAT_PEPPER must contain at least 32 bytes when PAT is enabled")
	}
	return nil
}

func validateSourceArtifactConfig(cfg Config) error {
	if !cfg.SourceArtifactsEnabled {
		return nil
	}
	checks := []struct{ value, name string }{
		{cfg.TOSEndpoint, "TOS_ENDPOINT"}, {cfg.TOSRegion, "TOS_REGION"},
		{cfg.TOSBucket, "TOS_BUCKET"}, {cfg.TOSAccessKey, "TOS_ACCESS_KEY"},
		{cfg.TOSSecretKey, "TOS_SECRET_KEY"},
	}
	for _, check := range checks {
		if strings.TrimSpace(check.value) == "" {
			return fmt.Errorf("%s is required when SOURCE_ARTIFACTS_ENABLED is true", check.name)
		}
	}
	if cfg.SourceArtifactMaxPending < 1 {
		return fmt.Errorf("SOURCE_ARTIFACT_MAX_PENDING must be positive")
	}
	if cfg.AppEnv == "production" && strings.TrimSpace(cfg.RayAPISpoolDir) == "" {
		return fmt.Errorf("RAY_API_SPOOL_DIR is required when source artifacts are enabled in production")
	}
	const maxUpload = 2 * 1024 * 1024 * 1024
	const spoolHeadroom = 1024 * 1024 * 1024
	if cfg.RayAPISpoolSizeBytes < maxUpload+spoolHeadroom {
		return fmt.Errorf("RAY_API_SPOOL_SIZE_BYTES must reserve upload headroom")
	}
	if cfg.RayAPIUploadMaxConcurrent < 1 || int64(cfg.RayAPIUploadMaxConcurrent)*maxUpload > cfg.RayAPISpoolSizeBytes-spoolHeadroom {
		return fmt.Errorf("RAY_API_UPLOAD_MAX_CONCURRENT exceeds spool capacity")
	}
	if cfg.RayAPIUploadRateLimit < 1 {
		return fmt.Errorf("RAY_API_UPLOAD_RATE_LIMIT must be positive")
	}
	if cfg.SourceArtifactQuotaBytes < 2*1024*1024*1024 {
		return fmt.Errorf("SOURCE_ARTIFACT_QUOTA_BYTES must be at least 2147483648")
	}
	if err := domain.ValidatePinnedImage(cfg.RayAPIDefaultImage); err != nil {
		return fmt.Errorf("RAY_API_DEFAULT_IMAGE must be a pinned sha256 image when source artifacts are enabled")
	}
	return nil
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func parseBool(name string, fallback bool) (bool, error) {
	value := os.Getenv(name)
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("%s must be a boolean: %w", name, err)
	}
	return parsed, nil
}

func parseInt(name string, fallback int) (int, error) {
	value := os.Getenv(name)
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer: %w", name, err)
	}
	return parsed, nil
}

func parseInt64(name string, fallback int64) (int64, error) {
	value := os.Getenv(name)
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer: %w", name, err)
	}
	return parsed, nil
}

func parseStorageQuantity(name string, fallback int64) (int64, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback, nil
	}
	quantity, err := resource.ParseQuantity(value)
	if err != nil || quantity.Sign() < 0 {
		return 0, fmt.Errorf("%s must be a non-negative storage quantity", name)
	}
	bytes := quantity.Value()
	if bytes < 0 {
		return 0, fmt.Errorf("%s is too large", name)
	}
	return bytes, nil
}

func splitList(value string) []string {
	parts := strings.Split(value, ",")
	items := make([]string, 0, len(parts))
	for _, part := range parts {
		if item := strings.TrimSpace(part); item != "" {
			items = append(items, item)
		}
	}
	return items
}

func splitUniqueList(value string) []string {
	items := splitList(value)
	seen := make(map[string]struct{}, len(items))
	unique := make([]string, 0, len(items))
	for _, item := range items {
		if _, exists := seen[item]; exists {
			continue
		}
		seen[item] = struct{}{}
		unique = append(unique, item)
	}
	return unique
}

// parseLabelSelector reads a comma-separated key=value list, for example
// "accelerator=nvidia-rtx-4090,pool=training". An empty value leaves the
// renderer on its built-in default.
func parseLabelSelector(name string) (map[string]string, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return nil, nil
	}
	selector := make(map[string]string)
	for _, pair := range strings.Split(raw, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		key, value, found := strings.Cut(pair, "=")
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		if !found || key == "" || value == "" {
			return nil, fmt.Errorf("%s must be a comma-separated key=value list", name)
		}
		selector[key] = value
	}
	return selector, nil
}

func parseDatasetPublisherTolerations() ([]DatasetPublisherToleration, error) {
	raw := strings.TrimSpace(os.Getenv("DATASET_PUBLISHER_TOLERATIONS_JSON"))
	if raw == "" {
		return nil, nil
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	var tolerations []DatasetPublisherToleration
	if err := decoder.Decode(&tolerations); err != nil {
		return nil, fmt.Errorf("DATASET_PUBLISHER_TOLERATIONS_JSON must be a JSON array of explicit tolerations")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, fmt.Errorf("DATASET_PUBLISHER_TOLERATIONS_JSON must contain exactly one JSON array")
	}
	if len(tolerations) > 64 {
		return nil, fmt.Errorf("DATASET_PUBLISHER_TOLERATIONS_JSON must contain at most 64 tolerations")
	}
	result := make([]DatasetPublisherToleration, len(tolerations))
	for index, toleration := range tolerations {
		if toleration.Key == "" || len(k8svalidation.IsQualifiedName(toleration.Key)) != 0 {
			return nil, fmt.Errorf("DATASET_PUBLISHER_TOLERATIONS_JSON contains an invalid key")
		}
		if toleration.Operator != "Equal" && toleration.Operator != "Exists" {
			return nil, fmt.Errorf("DATASET_PUBLISHER_TOLERATIONS_JSON contains an invalid operator")
		}
		if toleration.Operator == "Equal" && (toleration.Value == "" || len(k8svalidation.IsValidLabelValue(toleration.Value)) != 0) {
			return nil, fmt.Errorf("DATASET_PUBLISHER_TOLERATIONS_JSON contains an invalid value")
		}
		if toleration.Operator == "Exists" && toleration.Value != "" {
			return nil, fmt.Errorf("DATASET_PUBLISHER_TOLERATIONS_JSON Exists tolerations must not set a value")
		}
		if toleration.Effect != "NoSchedule" && toleration.Effect != "PreferNoSchedule" && toleration.Effect != "NoExecute" {
			return nil, fmt.Errorf("DATASET_PUBLISHER_TOLERATIONS_JSON contains an invalid effect")
		}
		if toleration.TolerationSeconds != nil && (toleration.Effect != "NoExecute" || *toleration.TolerationSeconds < 0 || *toleration.TolerationSeconds > 30*24*60*60) {
			return nil, fmt.Errorf("DATASET_PUBLISHER_TOLERATIONS_JSON contains invalid tolerationSeconds")
		}
		result[index] = toleration
	}
	return result, nil
}
