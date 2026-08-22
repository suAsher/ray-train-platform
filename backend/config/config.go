package config

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path"
	"regexp"
	"strconv"
	"strings"

	"k8s.io/apimachinery/pkg/api/resource"
	"ray-train-platform-backend/domain"
)

var pinnedImagePattern = regexp.MustCompile(`^[^@\s]+@sha256:[0-9a-fA-F]{64}$`)

type Config struct {
	AppEnv                           string
	HTTPAddr                         string
	DatabaseURL                      string
	OIDCIssuerURL                    string
	OIDCClientID                     string
	OIDCAudience                     string
	OIDCGroupPrefix                  string
	OIDCRequired                     bool
	PATEnabled                       bool
	PATPepper                        string
	TrainingNodeSelector             map[string]string
	KueueAutoQuota                   bool
	MaxWorkerReplicas                int
	MaxGPUsPerWorker                 int
	MaxTotalGPUs                     int
	LocalCacheEnabled                bool
	DataSpacesEnabled                bool
	DataSpacesFSXAttributes          string
	DataSpacesMountCapacity          string
	DataSpacesPublicRoot             string
	IDCDataSpacesEnabled             bool
	IDCDataSpacesMountCapacity       string
	IDCDataSpaceSources              map[string]IDCDataSpaceSource
	LocalCacheStorageClass           string
	LocalCacheSize                   string
	LocalCacheMountPath              string
	LocalAuthEnabled                 bool
	LocalSessionHours                int
	BootstrapAdminUsername           string
	BootstrapAdminPassword           string
	BootstrapAdminTenant             string
	PATDefaultExpiryDays             int
	PATMaxExpiryDays                 int
	SourceArtifactsEnabled           bool
	SourceArtifactMaxPending         int
	SourceArtifactQuotaBytes         int64
	RayAPISpoolDir                   string
	RayAPISpoolSizeBytes             int64
	RayAPIUploadMaxConcurrent        int
	RayAPIUploadRateLimit            int
	RayAPIDefaultImage               string
	KubeConfig                       string
	KubeContext                      string
	LokiURL                          string
	PrometheusURL                    string
	MLflowEnabled                    bool
	MLflowTrackingURL                string
	MLflowIngestURL                  string
	MLflowExperimentPrefix           string
	KueueClusterQueue                string
	IDCStorageEnabled                bool
	IDCExistingClaim                 string
	IDCStorageClass                  string
	IDCMountPath                     string
	RayVersion                       string
	RayJobClusterSpecField           string
	RayJobServiceAccount             string
	ImagePullSecrets                 []string
	SourceMaterializerImage          string
	WorkspaceImage                   string
	CORSOrigins                      []string
	RayImageAllowlist                []string
	GitAllowlist                     []string
	TOSBucket                        string
	TOSEndpoint                      string
	TOSRegion                        string
	TOSAccessKey                     string
	TOSSecretKey                     string
	TOSSecurityToken                 string
	TOSSecretName                    string
	TOSObjectSetQuotasEnabled        bool
	PersonalStorageDefaultQuotaBytes int64
	PersonalStorageMaxQuotaBytes     int64
	MigrationsOnly                   bool
	DemoMode                         bool
}

// IDCDataSpaceSource is a deployment-owned NFS export. It is deliberately
// configuration-only: callers can select a logical data space but cannot
// submit an arbitrary NFS server or path through the API.
type IDCDataSpaceSource struct {
	Server string `json:"server"`
	Path   string `json:"path"`
}

func Load() (Config, error) {
	cfg := Config{
		AppEnv:                     envOr("APP_ENV", "development"),
		HTTPAddr:                   envOr("HTTP_ADDR", ":8080"),
		DatabaseURL:                os.Getenv("DATABASE_URL"),
		OIDCIssuerURL:              os.Getenv("OIDC_ISSUER_URL"),
		OIDCClientID:               os.Getenv("OIDC_CLIENT_ID"),
		OIDCAudience:               os.Getenv("OIDC_AUDIENCE"),
		OIDCGroupPrefix:            envOr("OIDC_GROUP_PREFIX", "platform/tenants/"),
		PATPepper:                  os.Getenv("PAT_PEPPER"),
		KubeConfig:                 os.Getenv("KUBECONFIG"),
		KubeContext:                os.Getenv("KUBE_CONTEXT"),
		LokiURL:                    envOr("LOKI_URL", "http://loki-gateway.loki.svc.cluster.local"),
		PrometheusURL:              envOr("PROMETHEUS_URL", "http://prometheus.monitoring.svc.cluster.local:9090"),
		MLflowTrackingURL:          strings.TrimSpace(os.Getenv("MLFLOW_TRACKING_URL")),
		MLflowIngestURL:            strings.TrimSpace(os.Getenv("MLFLOW_INGEST_URL")),
		MLflowExperimentPrefix:     envOr("MLFLOW_EXPERIMENT_PREFIX", "raytrain"),
		KueueClusterQueue:          envOr("KUEUE_CLUSTER_QUEUE", "cluster-gpu-queue"),
		IDCExistingClaim:           os.Getenv("IDC_EXISTING_CLAIM"),
		IDCStorageClass:            os.Getenv("IDC_STORAGE_CLASS"),
		IDCMountPath:               envOr("IDC_MOUNT_PATH", "/mnt/idc"),
		LocalCacheStorageClass:     strings.TrimSpace(os.Getenv("LOCAL_CACHE_STORAGE_CLASS")),
		LocalCacheSize:             strings.TrimSpace(os.Getenv("LOCAL_CACHE_SIZE")),
		LocalCacheMountPath:        strings.TrimSpace(os.Getenv("LOCAL_CACHE_MOUNT_PATH")),
		DataSpacesFSXAttributes:    strings.TrimSpace(os.Getenv("DATA_SPACES_FSX_VOLUME_ATTRIBUTES_JSON")),
		RayAPIDefaultImage:         strings.TrimSpace(os.Getenv("RAY_API_DEFAULT_IMAGE")),
		DataSpacesMountCapacity:    strings.TrimSpace(os.Getenv("DATA_SPACES_MOUNT_CAPACITY")),
		DataSpacesPublicRoot:       envOr("DATA_SPACES_PUBLIC_ROOT", domain.DefaultPublicDataRoot),
		IDCDataSpacesMountCapacity: strings.TrimSpace(os.Getenv("IDC_DATA_SPACES_MOUNT_CAPACITY")),
		RayVersion:                 envOr("RAY_VERSION", "2.35.0"),
		RayJobClusterSpecField:     envOr("KUBERAY_RAYJOB_CLUSTER_SPEC_FIELD", "rayClusterSpec"),
		RayJobServiceAccount:       os.Getenv("RAY_JOB_SERVICE_ACCOUNT"),
		ImagePullSecrets:           splitList(os.Getenv("IMAGE_PULL_SECRETS")),
		SourceMaterializerImage:    os.Getenv("SOURCE_MATERIALIZER_IMAGE"),
		WorkspaceImage:             os.Getenv("WORKSPACE_IMAGE"),
		CORSOrigins:                splitList(os.Getenv("CORS_ORIGINS")),
		RayImageAllowlist:          splitList(os.Getenv("RAY_IMAGE_ALLOWLIST")),
		GitAllowlist:               splitList(os.Getenv("GIT_ALLOWLIST")),
		TOSBucket:                  os.Getenv("TOS_BUCKET"),
		TOSEndpoint:                os.Getenv("TOS_ENDPOINT"),
		TOSRegion:                  os.Getenv("TOS_REGION"),
		TOSAccessKey:               os.Getenv("TOS_ACCESS_KEY"),
		TOSSecretKey:               os.Getenv("TOS_SECRET_KEY"),
		TOSSecurityToken:           os.Getenv("TOS_SECURITY_TOKEN"),
		TOSSecretName:              os.Getenv("TOS_SECRET_NAME"),
		RayAPISpoolDir:             strings.TrimSpace(os.Getenv("RAY_API_SPOOL_DIR")),
	}
	if cfg.RayAPISpoolDir == "" && cfg.AppEnv != "production" {
		cfg.RayAPISpoolDir = os.TempDir()
	}

	var err error
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
	if cfg.MLflowEnabled, err = parseBool("MLFLOW_ENABLED", false); err != nil {
		return Config{}, err
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
	return cfg, nil
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

func validateInternalServiceURL(name, value string) error {
	endpoint, err := url.Parse(strings.TrimSpace(value))
	if err != nil || (endpoint.Scheme != "http" && endpoint.Scheme != "https") || endpoint.Hostname() == "" {
		return fmt.Errorf("%s must be an in-cluster HTTP(S) URL when MLFLOW_ENABLED is true", name)
	}
	host := strings.ToLower(endpoint.Hostname())
	if !(strings.HasSuffix(host, ".svc") || strings.HasSuffix(host, ".svc.cluster.local")) {
		return fmt.Errorf("%s must target an in-cluster Kubernetes Service", name)
	}
	if endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || (endpoint.Path != "" && endpoint.Path != "/") {
		return fmt.Errorf("%s must not contain credentials, a path, query, or fragment", name)
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
	for _, name := range []string{"original", "wellspiking", "shared"} {
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
	if cfg.LocalCacheStorageClass == "" {
		return fmt.Errorf("LOCAL_CACHE_STORAGE_CLASS is required when LOCAL_CACHE_ENABLED is true")
	}
	if cfg.LocalCacheSize == "" {
		return fmt.Errorf("LOCAL_CACHE_SIZE is required when LOCAL_CACHE_ENABLED is true")
	}
	if cfg.LocalCacheMountPath == "" {
		return fmt.Errorf("LOCAL_CACHE_MOUNT_PATH is required when LOCAL_CACHE_ENABLED is true")
	}
	if !isDNSSubdomain(cfg.LocalCacheStorageClass) {
		return fmt.Errorf("LOCAL_CACHE_STORAGE_CLASS must be a valid Kubernetes name")
	}
	quantity, err := resource.ParseQuantity(cfg.LocalCacheSize)
	if err != nil || quantity.Sign() <= 0 {
		return fmt.Errorf("LOCAL_CACHE_SIZE must be a positive Kubernetes storage quantity")
	}
	if !strings.HasPrefix(cfg.LocalCacheMountPath, "/") || path.Clean(cfg.LocalCacheMountPath) != cfg.LocalCacheMountPath || cfg.LocalCacheMountPath == "/" {
		return fmt.Errorf("LOCAL_CACHE_MOUNT_PATH must be a clean absolute directory")
	}
	if cfg.LocalCacheMountPath == "/tmp/ray" || strings.HasPrefix(cfg.LocalCacheMountPath, "/tmp/ray/") {
		return fmt.Errorf("LOCAL_CACHE_MOUNT_PATH must not be inside Ray's default temporary directory")
	}
	return nil
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
