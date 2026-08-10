package config

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
)

var pinnedImagePattern = regexp.MustCompile(`^[^@\s]+@sha256:[0-9a-fA-F]{64}$`)

type Config struct {
	AppEnv                    string
	HTTPAddr                  string
	DatabaseURL               string
	OIDCIssuerURL             string
	OIDCClientID              string
	OIDCAudience              string
	OIDCGroupPrefix           string
	OIDCRequired              bool
	PATEnabled                bool
	PATPepper                 string
	TrainingNodeSelector      map[string]string
	LocalAuthEnabled          bool
	LocalSessionHours         int
	BootstrapAdminUsername    string
	BootstrapAdminPassword    string
	BootstrapAdminTenant      string
	PATDefaultExpiryDays      int
	PATMaxExpiryDays          int
	SourceArtifactsEnabled    bool
	SourceArtifactMaxPending  int
	SourceArtifactQuotaBytes  int64
	RayAPISpoolDir            string
	RayAPISpoolSizeBytes      int64
	RayAPIUploadMaxConcurrent int
	RayAPIUploadRateLimit     int
	KubeConfig                string
	KubeContext               string
	LokiURL                   string
	PrometheusURL             string
	KueueClusterQueue         string
	IDCStorageEnabled         bool
	IDCExistingClaim          string
	IDCStorageClass           string
	IDCMountPath              string
	RayVersion                string
	RayJobClusterSpecField    string
	RayJobServiceAccount      string
	ImagePullSecrets          []string
	SourceMaterializerImage   string
	WorkspaceImage            string
	CORSOrigins               []string
	RayImageAllowlist         []string
	GitAllowlist              []string
	TOSBucket                 string
	TOSEndpoint               string
	TOSRegion                 string
	TOSAccessKey              string
	TOSSecretKey              string
	TOSSecurityToken          string
	TOSSecretName             string
	MigrationsOnly            bool
	DemoMode                  bool
}

func Load() (Config, error) {
	cfg := Config{
		AppEnv:                  envOr("APP_ENV", "development"),
		HTTPAddr:                envOr("HTTP_ADDR", ":8080"),
		DatabaseURL:             os.Getenv("DATABASE_URL"),
		OIDCIssuerURL:           os.Getenv("OIDC_ISSUER_URL"),
		OIDCClientID:            os.Getenv("OIDC_CLIENT_ID"),
		OIDCAudience:            os.Getenv("OIDC_AUDIENCE"),
		OIDCGroupPrefix:         envOr("OIDC_GROUP_PREFIX", "platform/tenants/"),
		PATPepper:               os.Getenv("PAT_PEPPER"),
		KubeConfig:              os.Getenv("KUBECONFIG"),
		KubeContext:             os.Getenv("KUBE_CONTEXT"),
		LokiURL:                 envOr("LOKI_URL", "http://loki.logging.svc.cluster.local:3100"),
		PrometheusURL:           envOr("PROMETHEUS_URL", "http://prometheus.monitoring.svc.cluster.local:9090"),
		KueueClusterQueue:       envOr("KUEUE_CLUSTER_QUEUE", "cluster-gpu-queue"),
		IDCExistingClaim:        os.Getenv("IDC_EXISTING_CLAIM"),
		IDCStorageClass:         os.Getenv("IDC_STORAGE_CLASS"),
		IDCMountPath:            envOr("IDC_MOUNT_PATH", "/mnt/idc"),
		RayVersion:              envOr("RAY_VERSION", "2.35.0"),
		RayJobClusterSpecField:  envOr("KUBERAY_RAYJOB_CLUSTER_SPEC_FIELD", "rayClusterSpec"),
		RayJobServiceAccount:    os.Getenv("RAY_JOB_SERVICE_ACCOUNT"),
		ImagePullSecrets:        splitList(os.Getenv("IMAGE_PULL_SECRETS")),
		SourceMaterializerImage: os.Getenv("SOURCE_MATERIALIZER_IMAGE"),
		WorkspaceImage:          os.Getenv("WORKSPACE_IMAGE"),
		CORSOrigins:             splitList(os.Getenv("CORS_ORIGINS")),
		RayImageAllowlist:       splitList(os.Getenv("RAY_IMAGE_ALLOWLIST")),
		GitAllowlist:            splitList(os.Getenv("GIT_ALLOWLIST")),
		TOSBucket:               os.Getenv("TOS_BUCKET"),
		TOSEndpoint:             os.Getenv("TOS_ENDPOINT"),
		TOSRegion:               os.Getenv("TOS_REGION"),
		TOSAccessKey:            os.Getenv("TOS_ACCESS_KEY"),
		TOSSecretKey:            os.Getenv("TOS_SECRET_KEY"),
		TOSSecurityToken:        os.Getenv("TOS_SECURITY_TOKEN"),
		TOSSecretName:           os.Getenv("TOS_SECRET_NAME"),
		RayAPISpoolDir:          strings.TrimSpace(os.Getenv("RAY_API_SPOOL_DIR")),
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
	if err := validatePATConfig(cfg); err != nil {
		return Config{}, err
	}
	if err := validateSourceArtifactConfig(cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
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
