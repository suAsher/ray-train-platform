package config

import (
	"strings"
	"testing"
)

const testPATPepper = "0123456789abcdef0123456789abcdef"

func setValidDatasetPublisherConfig(t *testing.T) {
	t.Helper()
	t.Setenv("DATASET_PUBLISHER_ENABLED", "true")
	t.Setenv("TOS_BUCKET", "source-bucket")
	t.Setenv("TOS_ENDPOINT", "tos-cn-shanghai.ivolces.com")
	t.Setenv("TOS_REGION", "cn-shanghai")
	t.Setenv("DATASET_PUBLISHER_IMAGE", "registry.example/publisher@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	t.Setenv("DATASET_PUBLISHER_SERVICE_ACCOUNT", "release-dataset-publisher")
	t.Setenv("DATASET_PUBLISHER_QUEUE_NAME", "release-dataset-publisher")
	t.Setenv("DATASET_PUBLISHER_PRIORITY_CLASS_NAME", "release-dataset-publisher-low")
}

func TestLoadAcceptsDatasetPublisherProxySecretReference(t *testing.T) {
	setValidDatasetPublisherConfig(t)
	t.Setenv("DATASET_VERSIONING_ENABLED", "true")
	t.Setenv("DATASET_PUBLISHER_PROXY_SECRET", "dataset-publisher-egress")
	t.Setenv("PAT_PEPPER", testPATPepper)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load publisher proxy Secret: %v", err)
	}
	if cfg.DatasetPublisherProxySecret != "dataset-publisher-egress" {
		t.Fatalf("proxy Secret=%q", cfg.DatasetPublisherProxySecret)
	}
}

func setValidProductionConfig(t *testing.T) {
	t.Helper()
	t.Setenv("APP_ENV", "production")
	t.Setenv("DATABASE_URL", "postgres://platform@db/platform")
	t.Setenv("OIDC_ISSUER_URL", "https://sso.example.com/realms/platform")
	t.Setenv("OIDC_CLIENT_ID", "ray-platform")
	t.Setenv("OIDC_AUDIENCE", "ray-platform-api")
	t.Setenv("KUBECONFIG", "/var/run/secrets/kubernetes.io/serviceaccount")
	t.Setenv("SOURCE_MATERIALIZER_IMAGE", "registry.example/source@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	t.Setenv("WORKSPACE_IMAGE", "registry.example/workspace@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	t.Setenv("RAY_IMAGE_ALLOWLIST", "registry.example/ray")
	t.Setenv("GIT_ALLOWLIST", "git.example.com")
	t.Setenv("DEMO_MODE", "false")
	t.Setenv("PAT_ENABLED", "true")
	t.Setenv("PAT_PEPPER", testPATPepper)
	t.Setenv("TOS_ENDPOINT", "https://tos-cn-beijing.volces.com")
	t.Setenv("TOS_REGION", "cn-beijing")
	t.Setenv("TOS_BUCKET", "private-bucket")
	t.Setenv("TOS_ACCESS_KEY", "test-access-key")
	t.Setenv("TOS_SECRET_KEY", "test-secret-key")
	t.Setenv("RAY_API_SPOOL_DIR", "/var/lib/ray-platform/ray-packages")
	t.Setenv("RAY_API_DEFAULT_IMAGE", "registry.example/ray@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
}

func TestLoadRejectsProductionWithoutRequiredDependencies(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	t.Setenv("DATABASE_URL", "")
	t.Setenv("OIDC_ISSUER_URL", "")
	t.Setenv("OIDC_CLIENT_ID", "")
	t.Setenv("OIDC_AUDIENCE", "")
	t.Setenv("KUBECONFIG", "")
	t.Setenv("PAT_PEPPER", testPATPepper)

	_, err := Load()
	if err == nil {
		t.Fatal("expected production configuration validation error")
	}
	if err.Error() != "DATABASE_URL is required in production" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadRejectsDemoModeInProduction(t *testing.T) {
	setValidProductionConfig(t)
	t.Setenv("DEMO_MODE", "true")

	_, err := Load()
	if err == nil {
		t.Fatal("expected demo mode validation error")
	}
	if err.Error() != "DEMO_MODE cannot be enabled in production" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadRejectsDisabledOIDCInProduction(t *testing.T) {
	setValidProductionConfig(t)
	t.Setenv("OIDC_REQUIRED", "false")

	_, err := Load()
	if err == nil {
		t.Fatal("expected production OIDC validation error")
	}
	if err.Error() != "OIDC_REQUIRED cannot be disabled in production" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadAcceptsProductionConfiguration(t *testing.T) {
	setValidProductionConfig(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("expected valid production configuration: %v", err)
	}
	if cfg.AppEnv != "production" || !cfg.OIDCRequired || !cfg.PATEnabled || !cfg.SourceArtifactsEnabled {
		t.Fatal("unexpected production feature flags")
	}
}

func TestLoadUsesLokiGatewayByDefault(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("PAT_ENABLED", "false")
	t.Setenv("LOKI_URL", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load defaults: %v", err)
	}
	if cfg.LokiURL != "http://loki-gateway.loki.svc.cluster.local" {
		t.Fatalf("unexpected Loki default: %q", cfg.LokiURL)
	}
}

func TestLoadRayTrainRuntimeFlagsAreDisabledByDefaultAndExplicitlyConfigurable(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("PAT_ENABLED", "false")
	t.Setenv("RAY_TRAIN_MANAGED_ENABLED", "")
	t.Setenv("RAY_TRAIN_MANAGED_TENANTS", "")
	t.Setenv("RAY_TRAIN_CANARY_ENABLED", "")
	t.Setenv("RAY_TRAIN_CANARY_TENANTS", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load runtime flag defaults: %v", err)
	}
	if cfg.RayTrainManagedEnabled || cfg.RayTrainCanaryEnabled {
		t.Fatalf("runtime flags must default off: %+v", cfg)
	}
	if len(cfg.RayTrainManagedTenants) != 0 || len(cfg.RayTrainCanaryTenants) != 0 {
		t.Fatalf("tenant allowlists must default empty: managed=%v canary=%v", cfg.RayTrainManagedTenants, cfg.RayTrainCanaryTenants)
	}

	t.Setenv("RAY_TRAIN_MANAGED_ENABLED", "true")
	t.Setenv("RAY_TRAIN_CANARY_ENABLED", "true")
	cfg, err = Load()
	if err != nil {
		t.Fatalf("load enabled runtime flags: %v", err)
	}
	if !cfg.RayTrainManagedEnabled || !cfg.RayTrainCanaryEnabled {
		t.Fatalf("explicit runtime flags were not retained: %+v", cfg)
	}
}

func TestLoadNormalizesRayTrainManagedAndCanaryTenants(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("PAT_ENABLED", "false")
	t.Setenv("RAY_TRAIN_MANAGED_TENANTS", " tenant-a,tenant-b, tenant-a ,, tenant-b ")
	t.Setenv("RAY_TRAIN_CANARY_ENABLED", "true")
	t.Setenv("RAY_TRAIN_CANARY_TENANTS", " tenant-a,tenant-a ,, tenant-c ")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load canary tenants: %v", err)
	}
	if strings.Join(cfg.RayTrainManagedTenants, ",") != "tenant-a,tenant-b" {
		t.Fatalf("unexpected normalized managed tenants: %v", cfg.RayTrainManagedTenants)
	}
	if strings.Join(cfg.RayTrainCanaryTenants, ",") != "tenant-a,tenant-c" {
		t.Fatalf("unexpected normalized canary tenants: %v", cfg.RayTrainCanaryTenants)
	}
}

func TestLoadRejectsInvalidRayTrainRuntimeFlags(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("PAT_ENABLED", "false")
	t.Setenv("RAY_TRAIN_MANAGED_ENABLED", "sometimes")

	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "RAY_TRAIN_MANAGED_ENABLED") {
		t.Fatalf("expected invalid managed runtime flag error, got %v", err)
	}
}

func TestLoadDatasetVersioningAndRayDataStreamingDefaults(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("PAT_ENABLED", "false")
	t.Setenv("DATASET_VERSIONING_ENABLED", "")
	t.Setenv("RAY_DATA_STREAMING_ENABLED", "")
	t.Setenv("DATASET_INTERNAL_PREFIX", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load dataset feature defaults: %v", err)
	}
	if cfg.DatasetVersioningEnabled || cfg.RayDataStreamingEnabled || cfg.DatasetPublisherEnabled {
		t.Fatalf("dataset feature flags must default off: versioning=%t streaming=%t publisher=%t", cfg.DatasetVersioningEnabled, cfg.RayDataStreamingEnabled, cfg.DatasetPublisherEnabled)
	}
	if cfg.DatasetInternalPrefix != "ray-train/platform/datasets" {
		t.Fatalf("unexpected dataset internal prefix default: %q", cfg.DatasetInternalPrefix)
	}
}

func TestLoadDatasetVersioningAndRayDataStreamingOverrides(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("PAT_ENABLED", "false")
	t.Setenv("DATASET_VERSIONING_ENABLED", "true")
	t.Setenv("RAY_DATA_STREAMING_ENABLED", "true")
	t.Setenv("DATASET_INTERNAL_PREFIX", "ray-train/private/platform/datasets")
	setValidDatasetPublisherConfig(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load dataset feature overrides: %v", err)
	}
	if !cfg.DatasetVersioningEnabled || !cfg.RayDataStreamingEnabled || !cfg.DatasetPublisherEnabled {
		t.Fatalf("dataset feature flag overrides were not retained: versioning=%t streaming=%t publisher=%t", cfg.DatasetVersioningEnabled, cfg.RayDataStreamingEnabled, cfg.DatasetPublisherEnabled)
	}
	if cfg.DatasetInternalPrefix != "ray-train/private/platform/datasets" {
		t.Fatalf("dataset internal prefix override = %q", cfg.DatasetInternalPrefix)
	}
}

func TestLoadRejectsInvalidDatasetVersioningAndRayDataStreamingFlags(t *testing.T) {
	for _, envName := range []string{"DATASET_VERSIONING_ENABLED", "RAY_DATA_STREAMING_ENABLED", "DATASET_PUBLISHER_ENABLED"} {
		t.Run(envName, func(t *testing.T) {
			t.Setenv("APP_ENV", "development")
			t.Setenv("PAT_ENABLED", "false")
			t.Setenv("DATASET_VERSIONING_ENABLED", "")
			t.Setenv("RAY_DATA_STREAMING_ENABLED", "")
			t.Setenv(envName, "enabled")

			_, err := Load()
			if err == nil || !strings.Contains(err.Error(), envName) {
				t.Fatalf("expected invalid %s error, got %v", envName, err)
			}
		})
	}
}

func TestLoadRejectsUnsafeDatasetInternalPrefix(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("PAT_ENABLED", "false")
	t.Setenv("DATASET_INTERNAL_PREFIX", "tos://bucket/private")

	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "DATASET_INTERNAL_PREFIX") {
		t.Fatalf("expected unsafe dataset prefix error, got %v", err)
	}
}

func TestLoadNormalizesDatasetInternalPrefix(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("PAT_ENABLED", "false")
	t.Setenv("DATASET_INTERNAL_PREFIX", "private/platform/datasets/")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load normalized dataset prefix: %v", err)
	}
	if cfg.DatasetInternalPrefix != "private/platform/datasets" {
		t.Fatalf("dataset internal prefix was not normalized: %q", cfg.DatasetInternalPrefix)
	}
}

func TestLoadRequiresCompleteDatasetPublisherConfigurationOnlyWhenPublisherIsEnabled(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("PAT_ENABLED", "false")
	t.Setenv("DATASET_VERSIONING_ENABLED", "true")
	t.Setenv("DATASET_PUBLISHER_ENABLED", "true")
	t.Setenv("DATASET_PUBLISHER_IMAGE", "")

	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "DATASET_PUBLISHER_IMAGE") {
		t.Fatalf("expected missing publisher image error, got %v", err)
	}

	t.Setenv("DATASET_PUBLISHER_ENABLED", "false")
	if _, err := Load(); err != nil {
		t.Fatalf("disabled publisher rollout must not require deployment settings: %v", err)
	}
}

func TestLoadAllowsVersionCatalogWithoutPublisherRollout(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("PAT_ENABLED", "false")
	t.Setenv("DATASET_VERSIONING_ENABLED", "true")
	t.Setenv("DATASET_PUBLISHER_ENABLED", "false")
	t.Setenv("DATASET_PUBLISHER_IMAGE", "")

	cfg, err := Load()
	if err != nil || !cfg.DatasetVersioningEnabled || cfg.DatasetPublisherEnabled {
		t.Fatalf("independent publisher rollout failed: cfg=%+v err=%v", cfg, err)
	}
}

func TestLoadRejectsStreamingPrefixOutsideTenantStorageRoot(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("PAT_ENABLED", "false")
	t.Setenv("DATASET_VERSIONING_ENABLED", "true")
	t.Setenv("RAY_DATA_STREAMING_ENABLED", "true")
	t.Setenv("DATASET_INTERNAL_PREFIX", "private/platform/datasets")

	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "DATASET_INTERNAL_PREFIX") {
		t.Fatalf("streaming prefix outside ray-train was accepted: %v", err)
	}
}

func TestLoadDatasetPublisherConfigurationUsesPinnedIRSAJobSettings(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("PAT_ENABLED", "false")
	t.Setenv("DATASET_VERSIONING_ENABLED", "true")
	setValidDatasetPublisherConfig(t)
	t.Setenv("TOS_ENDPOINT", "https://tos-cn-shanghai.ivolces.com")
	t.Setenv("DATASET_PUBLISHER_TARGET_BUCKET", "derived-bucket")
	t.Setenv("DATASET_PUBLISHER_NODE_SELECTOR", "pool=cpu")
	t.Setenv("DATASET_PUBLISHER_PREFERRED_NODE_SELECTOR", "node-role=cpu")
	t.Setenv("DATASET_PUBLISHER_TOLERATIONS_JSON", `[{"key":"dedicated","operator":"Equal","value":"platform","effect":"NoSchedule"}]`)
	t.Setenv("DATASET_PUBLISHER_JOB_ACTIVE_DEADLINE_SECONDS", "7200")
	t.Setenv("DATASET_PUBLISHER_JOB_TTL_SECONDS", "1800")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load dataset publisher configuration: %v", err)
	}
	if cfg.DatasetPublisherImage == "" || cfg.DatasetPublisherSourceBucket != "source-bucket" || cfg.DatasetPublisherTargetBucket != "derived-bucket" {
		t.Fatalf("unexpected publisher object storage settings: %+v", cfg)
	}
	if cfg.DatasetPublisherTOSEndpoint != "tos-cn-shanghai.ivolces.com" || cfg.DatasetPublisherTOSRegion != "cn-shanghai" {
		t.Fatalf("unexpected publisher endpoint settings: endpoint=%q region=%q", cfg.DatasetPublisherTOSEndpoint, cfg.DatasetPublisherTOSRegion)
	}
	if cfg.DatasetPublisherSourceIndexName != ".raytrain/trusted-index-v2.pkl" {
		t.Fatalf("unexpected trusted index name: %q", cfg.DatasetPublisherSourceIndexName)
	}
	if cfg.DatasetPublisherJobActiveDeadlineSeconds != 7200 || cfg.DatasetPublisherJobTTLSeconds != 1800 {
		t.Fatalf("unexpected publisher lifecycle: active=%d ttl=%d", cfg.DatasetPublisherJobActiveDeadlineSeconds, cfg.DatasetPublisherJobTTLSeconds)
	}
	if cfg.DatasetPublisherNodeSelector["pool"] != "cpu" || cfg.DatasetPublisherPreferredNodeSelector["node-role"] != "cpu" {
		t.Fatalf("unexpected publisher placement: hard=%v preferred=%v", cfg.DatasetPublisherNodeSelector, cfg.DatasetPublisherPreferredNodeSelector)
	}
	if len(cfg.DatasetPublisherTolerations) != 1 || cfg.DatasetPublisherTolerations[0].Key != "dedicated" || cfg.DatasetPublisherTolerations[0].Effect != "NoSchedule" {
		t.Fatalf("unexpected publisher tolerations: %+v", cfg.DatasetPublisherTolerations)
	}
}

func TestLoadSanitizesOptionalDatasetPublisherIRSARoleTRN(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "empty", raw: "", want: ""},
		{
			name: "configured",
			raw:  "  trn:iam::2103446203:role/tos-rw  ",
			want: "trn:iam::2103446203:role/tos-rw",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("APP_ENV", "development")
			t.Setenv("PAT_ENABLED", "false")
			t.Setenv("DATASET_VERSIONING_ENABLED", "true")
			setValidDatasetPublisherConfig(t)
			t.Setenv("DATASET_PUBLISHER_IRSA_ROLE_TRN", test.raw)

			cfg, err := Load()
			if err != nil {
				t.Fatalf("load dataset publisher IRSA role: %v", err)
			}
			if cfg.DatasetPublisherIRSARoleTRN != test.want {
				t.Fatalf("IRSA role TRN=%q, want %q", cfg.DatasetPublisherIRSARoleTRN, test.want)
			}
		})
	}
}

func TestLoadRejectsUnsafeDatasetPublisherIRSARoleTRNWithoutEchoingIt(t *testing.T) {
	unsafeRole := "trn:iam::2103446203:role/tos-rw\nVOLCENGINE_OIDC_TOKEN_FILE=/tmp/attacker-token"
	t.Setenv("APP_ENV", "development")
	t.Setenv("PAT_ENABLED", "false")
	t.Setenv("DATASET_VERSIONING_ENABLED", "true")
	setValidDatasetPublisherConfig(t)
	t.Setenv("DATASET_PUBLISHER_IRSA_ROLE_TRN", unsafeRole)

	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "DATASET_PUBLISHER_IRSA_ROLE_TRN") {
		t.Fatalf("expected IRSA role validation error, got %v", err)
	}
	if strings.Contains(err.Error(), unsafeRole) || strings.Contains(err.Error(), "attacker-token") {
		t.Fatalf("IRSA role validation error echoed unsafe configuration: %v", err)
	}
}

func TestLoadRejectsUnsafeDatasetPublisherConfiguration(t *testing.T) {
	validImage := "registry.example/publisher@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	tests := []struct {
		name  string
		env   string
		value string
	}{
		{name: "mutable image", env: "DATASET_PUBLISHER_IMAGE", value: "registry.example/publisher:latest"},
		{name: "image pull policy", env: "DATASET_PUBLISHER_IMAGE_PULL_POLICY", value: "Sometimes"},
		{name: "source bucket URI", env: "DATASET_PUBLISHER_SOURCE_BUCKET", value: "tos://source-bucket"},
		{name: "endpoint URI", env: "DATASET_PUBLISHER_ENDPOINT", value: "https://tos-cn-shanghai.ivolces.com"},
		{name: "loopback endpoint", env: "DATASET_PUBLISHER_ENDPOINT", value: "127.0.0.1"},
		{name: "localhost endpoint", env: "DATASET_PUBLISHER_ENDPOINT", value: "localhost.localdomain"},
		{name: "unapproved endpoint", env: "DATASET_PUBLISHER_ENDPOINT", value: "objects.example.com"},
		{name: "forged TOS suffix", env: "DATASET_PUBLISHER_ENDPOINT", value: "tos-cn-shanghai.ivolces.com.attacker.example"},
		{name: "wrong region endpoint", env: "DATASET_PUBLISHER_ENDPOINT", value: "tos-cn-beijing.ivolces.com"},
		{name: "root workdir", env: "DATASET_PUBLISHER_WORKING_DIRECTORY", value: "/"},
		{name: "unsafe index", env: "DATASET_PUBLISHER_SOURCE_INDEX_NAME", value: "../index.pkl"},
		{name: "CPU limit below request", env: "DATASET_PUBLISHER_CPU_LIMIT", value: "500m"},
		{name: "memory limit below request", env: "DATASET_PUBLISHER_MEMORY_LIMIT", value: "1Gi"},
		{name: "invalid tolerations", env: "DATASET_PUBLISHER_TOLERATIONS_JSON", value: `[{"key":"dedicated","operator":"RunAnything"}]`},
		{name: "zero attempts", env: "DATASET_PUBLISHER_CLIENT_MAX_ATTEMPTS", value: "0"},
		{name: "excessive ttl", env: "DATASET_PUBLISHER_JOB_TTL_SECONDS", value: "2592001"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("APP_ENV", "development")
			t.Setenv("PAT_ENABLED", "false")
			t.Setenv("DATASET_VERSIONING_ENABLED", "true")
			setValidDatasetPublisherConfig(t)
			t.Setenv("DATASET_PUBLISHER_IMAGE", validImage)
			t.Setenv(test.env, test.value)

			_, err := Load()
			if err == nil || !strings.Contains(err.Error(), test.env) {
				t.Fatalf("expected %s validation error, got %v", test.env, err)
			}
		})
	}
}

func TestLoadAcceptsApprovedVolcengineDatasetPublisherEndpoints(t *testing.T) {
	for _, endpoint := range []string{
		"tos-cn-shanghai.ivolces.com",
		"tos-s3-cn-shanghai.ivolces.com",
		"shanghai-data-transfer.tos-cn-shanghai.ivolces.com",
		"tos-cn-shanghai.volces.com",
		"tos2-private.cn-shanghai.tos.ivolces.com",
	} {
		t.Run(endpoint, func(t *testing.T) {
			t.Setenv("APP_ENV", "development")
			t.Setenv("PAT_ENABLED", "false")
			t.Setenv("DATASET_VERSIONING_ENABLED", "true")
			setValidDatasetPublisherConfig(t)
			t.Setenv("DATASET_PUBLISHER_ENDPOINT", endpoint)

			if _, err := Load(); err != nil {
				t.Fatalf("expected approved TOS endpoint %q: %v", endpoint, err)
			}
		})
	}
}

func TestLoadKeepsDataSpaceMountsDisabledUntilExplicitlyEnabled(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("PAT_ENABLED", "false")
	t.Setenv("TOS_ENDPOINT", "https://tos-cn-shanghai.ivolces.com")
	t.Setenv("TOS_BUCKET", "shanghai-data-transfer")
	t.Setenv("DATA_SPACES_ENABLED", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load defaults: %v", err)
	}
	if cfg.DataSpacesEnabled {
		t.Fatal("data-space mounts must be disabled by default")
	}

	t.Setenv("DATA_SPACES_ENABLED", "true")
	t.Setenv("DATA_SPACES_FSX_VOLUME_ATTRIBUTES_JSON", `{"type":"TOS","bucket":"shanghai-data-transfer","server":"tos-cn-shanghai.ivolces.com","region":"cn-shanghai"}`)
	t.Setenv("DATA_SPACES_MOUNT_CAPACITY", "1Ti")
	cfg, err = Load()
	if err != nil || !cfg.DataSpacesEnabled || cfg.DataSpacesMountCapacity != "1Ti" {
		t.Fatalf("explicit data-space enablement was not retained: cfg=%#v err=%v", cfg, err)
	}
	if cfg.DataSpacesPublicRoot != "ray-train/public/" {
		t.Fatalf("default public root = %q", cfg.DataSpacesPublicRoot)
	}
	t.Setenv("DATA_SPACES_PUBLIC_ROOT", "ray-train/tenants/local/datasets/public")
	cfg, err = Load()
	if err != nil || cfg.DataSpacesPublicRoot != "ray-train/tenants/local/datasets/public" {
		t.Fatalf("temporary public root was not retained: cfg=%#v err=%v", cfg, err)
	}
}

func TestLoadEnablesFinitePersonalObjectSetQuotaOnlyWithCompleteTOSConfiguration(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("PAT_ENABLED", "false")
	t.Setenv("TOS_ENDPOINT", "https://tos-cn-shanghai.ivolces.com")
	t.Setenv("TOS_REGION", "cn-shanghai")
	t.Setenv("TOS_BUCKET", "shanghai-data-transfer")
	t.Setenv("TOS_ACCESS_KEY", "test-access-key")
	t.Setenv("TOS_SECRET_KEY", "test-secret-key")
	t.Setenv("TOS_OBJECT_SET_QUOTAS_ENABLED", "true")
	t.Setenv("PERSONAL_STORAGE_DEFAULT_QUOTA", "100Gi")
	t.Setenv("PERSONAL_STORAGE_MAX_QUOTA", "2Ti")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load ObjectSet quota configuration: %v", err)
	}
	if !cfg.TOSObjectSetQuotasEnabled || cfg.PersonalStorageDefaultQuotaBytes != 100*1024*1024*1024 || cfg.PersonalStorageMaxQuotaBytes != 2*1024*1024*1024*1024 {
		t.Fatalf("unexpected ObjectSet quota configuration: %#v", cfg)
	}

	t.Setenv("PERSONAL_STORAGE_DEFAULT_QUOTA", "0")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "PERSONAL_STORAGE_DEFAULT_QUOTA") {
		t.Fatalf("expected finite quota validation error, got %v", err)
	}
}

func TestLoadRejectsDataSpaceMountAgainstDifferentBackendTOS(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("PAT_ENABLED", "false")
	t.Setenv("TOS_ENDPOINT", "https://tos-cn-shanghai.ivolces.com")
	t.Setenv("TOS_BUCKET", "shanghai-data-transfer")
	t.Setenv("DATA_SPACES_ENABLED", "true")
	t.Setenv("DATA_SPACES_FSX_VOLUME_ATTRIBUTES_JSON", `{"type":"TOS","bucket":"other-bucket","server":"tos-cn-shanghai.ivolces.com","region":"cn-shanghai"}`)
	t.Setenv("DATA_SPACES_MOUNT_CAPACITY", "1Ti")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "same bucket and server") {
		t.Fatalf("expected data-space/backend TOS mismatch rejection, got %v", err)
	}
}

func TestLoadRejectsEnabledDataSpacesWithoutSecretlessFSXContract(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("PAT_ENABLED", "false")
	t.Setenv("TOS_ENDPOINT", "https://s")
	t.Setenv("TOS_BUCKET", "b")
	t.Setenv("DATA_SPACES_ENABLED", "true")
	t.Setenv("DATA_SPACES_FSX_VOLUME_ATTRIBUTES_JSON", `{"type":"TOS","bucket":"b","server":"s","region":"r","secretName":"legacy"}`)
	t.Setenv("DATA_SPACES_MOUNT_CAPACITY", "1Ti")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "DATA_SPACES_FSX_VOLUME_ATTRIBUTES_JSON") {
		t.Fatalf("expected a secretless FSX contract error, got %v", err)
	}
}

func TestLoadRejectsIDCWithoutExistingClaim(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("PAT_ENABLED", "false")
	t.Setenv("IDC_STORAGE_ENABLED", "true")
	t.Setenv("IDC_EXISTING_CLAIM", "")
	t.Setenv("IDC_STORAGE_CLASS", "fast")
	if _, err := Load(); err == nil {
		t.Fatal("expected IDC existing claim validation error")
	}
}

func TestLoadAcceptsGovernedIDCDataSpacesWithoutLegacyClaim(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("PAT_ENABLED", "false")
	t.Setenv("IDC_STORAGE_ENABLED", "false")
	t.Setenv("IDC_DATA_SPACES_ENABLED", "true")
	t.Setenv("IDC_DATA_SPACES_MOUNT_CAPACITY", "1Pi")
	t.Setenv("IDC_DATA_SPACES_SOURCES_JSON", `{
      "original":{"server":"192.0.2.10","path":"/exports/original"},
      "wellspiking":{"server":"192.0.2.11","path":"/exports/wellspiking"},
      "shared":{"server":"storage.example.internal","path":"/exports/shared"}
    }`)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load governed IDC data spaces: %v", err)
	}
	if !cfg.IDCDataSpacesEnabled || cfg.IDCDataSpaceSources["shared"].Path != "/exports/shared" {
		t.Fatalf("unexpected governed IDC configuration: %#v", cfg)
	}
}

func TestLoadRejectsIncompleteGovernedIDCDataSpaces(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("PAT_ENABLED", "false")
	t.Setenv("IDC_STORAGE_ENABLED", "false")
	t.Setenv("IDC_DATA_SPACES_ENABLED", "true")
	t.Setenv("IDC_DATA_SPACES_MOUNT_CAPACITY", "1Pi")
	t.Setenv("IDC_DATA_SPACES_SOURCES_JSON", `{"original":{"server":"192.0.2.10","path":"/exports/original"}}`)

	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "wellspiking") {
		t.Fatalf("expected complete IDC source validation error, got %v", err)
	}
}

func TestLoadRejectsEnabledLocalCacheWithoutCompleteConfiguration(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("PAT_ENABLED", "false")
	t.Setenv("LOCAL_CACHE_ENABLED", "true")
	t.Setenv("LOCAL_CACHE_STORAGE_CLASS_DATA1", "ray-cache-local-data1")
	t.Setenv("LOCAL_CACHE_STORAGE_CLASS_DATA2", "")
	t.Setenv("LOCAL_CACHE_SIZE", "200Gi")
	t.Setenv("LOCAL_CACHE_MOUNT_PATH_DATA1", "/mnt/cache")
	t.Setenv("LOCAL_CACHE_MOUNT_PATH_DATA2", "/mnt/cache2")

	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "LOCAL_CACHE_STORAGE_CLASS_DATA2") {
		t.Fatalf("expected local cache storage class validation error, got %v", err)
	}
}

func TestLoadAcceptsCompleteLocalCacheConfiguration(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("PAT_ENABLED", "false")
	t.Setenv("LOCAL_CACHE_ENABLED", "true")
	setCompleteDualLocalCacheEnv(t)
	t.Setenv("LOCAL_CACHE_SIZE", "200Gi")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load complete local cache configuration: %v", err)
	}
	if !cfg.LocalCacheEnabled || cfg.LocalCacheStorageClassData1 != "ray-cache-local-data1" || cfg.LocalCacheStorageClassData2 != "ray-cache-local-data2" || cfg.LocalCacheSize != "200Gi" || cfg.LocalCacheMountPathData1 != "/mnt/cache" || cfg.LocalCacheMountPathData2 != "/mnt/cache2" {
		t.Fatalf("unexpected local cache configuration: %#v", cfg)
	}
	if strings.Join(cfg.LocalCacheAllowedSizes, ",") != "200Gi,500Gi,1Ti,2Ti,4Ti,5Ti" || cfg.LocalCacheMaxSize != "5Ti" {
		t.Fatalf("unexpected local cache policy defaults: allowed=%v max=%q", cfg.LocalCacheAllowedSizes, cfg.LocalCacheMaxSize)
	}
}

func TestLoadParsesLocalCachePolicy(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("PAT_ENABLED", "false")
	t.Setenv("LOCAL_CACHE_ENABLED", "true")
	setCompleteDualLocalCacheEnv(t)
	t.Setenv("LOCAL_CACHE_SIZE", " 2Ti ")
	t.Setenv("LOCAL_CACHE_ALLOWED_SIZES", "200Gi, 2Ti,5Ti")
	t.Setenv("LOCAL_CACHE_MAX_SIZE", " 5Ti ")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load local cache policy: %v", err)
	}
	if got := strings.Join(cfg.LocalCacheAllowedSizes, ","); got != "200Gi,2Ti,5Ti" {
		t.Fatalf("allowed sizes=%q", got)
	}
	if cfg.LocalCacheSize != "2Ti" || cfg.LocalCacheMaxSize != "5Ti" {
		t.Fatalf("default=%q max=%q", cfg.LocalCacheSize, cfg.LocalCacheMaxSize)
	}
}

func TestLoadRejectsInvalidLocalCachePolicy(t *testing.T) {
	tests := []struct {
		name        string
		allowed     string
		defaultSize string
		maxSize     string
		want        string
	}{
		{name: "empty allowlist", allowed: " ", defaultSize: "200Gi", maxSize: "500Gi", want: "LOCAL_CACHE_ALLOWED_SIZES"},
		{name: "non-positive allowed", allowed: "100Gi,0Gi", defaultSize: "100Gi", maxSize: "500Gi", want: "positive"},
		{name: "duplicate equivalent allowed", allowed: "100Gi,102400Mi", defaultSize: "100Gi", maxSize: "500Gi", want: "unique"},
		{name: "default outside allowlist", allowed: "100Gi,500Gi", defaultSize: "200Gi", maxSize: "500Gi", want: "LOCAL_CACHE_SIZE"},
		{name: "allowed above max", allowed: "100Gi,500Gi", defaultSize: "100Gi", maxSize: "200Gi", want: "LOCAL_CACHE_MAX_SIZE"},
		{name: "invalid max", allowed: "100Gi", defaultSize: "100Gi", maxSize: "large", want: "LOCAL_CACHE_MAX_SIZE"},
		{name: "odd total cannot split equally", allowed: "200Gi,201Gi", defaultSize: "200Gi", maxSize: "5Ti", want: "even whole-GiB"},
		{name: "maximum exceeds physical policy", allowed: "200Gi", defaultSize: "200Gi", maxSize: "6Ti", want: "must not exceed 5Ti"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("APP_ENV", "development")
			t.Setenv("PAT_ENABLED", "false")
			t.Setenv("LOCAL_CACHE_ENABLED", "true")
			setCompleteDualLocalCacheEnv(t)
			t.Setenv("LOCAL_CACHE_SIZE", test.defaultSize)
			t.Setenv("LOCAL_CACHE_ALLOWED_SIZES", test.allowed)
			t.Setenv("LOCAL_CACHE_MAX_SIZE", test.maxSize)

			if _, err := Load(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("expected error containing %q, got %v", test.want, err)
			}
		})
	}
}

func TestLoadRejectsLocalCacheMountedInsideRayDefaultTempDirectory(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("PAT_ENABLED", "false")
	t.Setenv("LOCAL_CACHE_ENABLED", "true")
	setCompleteDualLocalCacheEnv(t)
	t.Setenv("LOCAL_CACHE_SIZE", "200Gi")
	t.Setenv("LOCAL_CACHE_MOUNT_PATH_DATA1", "/tmp/ray")

	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "LOCAL_CACHE_MOUNT_PATH_DATA1") {
		t.Fatalf("expected unsafe Ray temp-directory cache mount to be rejected, got %v", err)
	}
}

func TestLoadUsesLegacyLocalCacheFieldsOnlyAsData1Fallback(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("PAT_ENABLED", "false")
	t.Setenv("LOCAL_CACHE_ENABLED", "true")
	t.Setenv("LOCAL_CACHE_STORAGE_CLASS", "legacy-data1")
	t.Setenv("LOCAL_CACHE_STORAGE_CLASS_DATA2", "ray-cache-local-data2")
	t.Setenv("LOCAL_CACHE_MOUNT_PATH", "/mnt/legacy-cache")
	t.Setenv("LOCAL_CACHE_MOUNT_PATH_DATA2", "/mnt/cache2")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load legacy data1 fallback: %v", err)
	}
	if cfg.LocalCacheStorageClassData1 != "legacy-data1" || cfg.LocalCacheMountPathData1 != "/mnt/legacy-cache" {
		t.Fatalf("legacy data1 fallback not applied: %#v", cfg)
	}
}

func setCompleteDualLocalCacheEnv(t *testing.T) {
	t.Helper()
	t.Setenv("LOCAL_CACHE_STORAGE_CLASS_DATA1", "ray-cache-local-data1")
	t.Setenv("LOCAL_CACHE_STORAGE_CLASS_DATA2", "ray-cache-local-data2")
	t.Setenv("LOCAL_CACHE_MOUNT_PATH_DATA1", "/mnt/cache")
	t.Setenv("LOCAL_CACHE_MOUNT_PATH_DATA2", "/mnt/cache2")
}

func TestLoadPATDefaults(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("PAT_ENABLED", "true")
	t.Setenv("PAT_PEPPER", testPATPepper)
	t.Setenv("PAT_DEFAULT_EXPIRY_DAYS", "")
	t.Setenv("PAT_MAX_EXPIRY_DAYS", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load PAT defaults: %v", err)
	}
	if !cfg.PATEnabled || cfg.PATDefaultExpiryDays != 90 || cfg.PATMaxExpiryDays != 365 {
		t.Fatalf("unexpected PAT defaults: enabled=%t default=%d max=%d", cfg.PATEnabled, cfg.PATDefaultExpiryDays, cfg.PATMaxExpiryDays)
	}
}

func TestLoadRejectsMissingOrWeakPATPepperWhenEnabled(t *testing.T) {
	for _, pepper := range []string{"", "too-short"} {
		t.Run(pepper, func(t *testing.T) {
			setValidProductionConfig(t)
			t.Setenv("PAT_PEPPER", pepper)
			_, err := Load()
			if err == nil || !strings.Contains(err.Error(), "PAT_PEPPER") {
				t.Fatalf("expected PAT pepper validation error, got %v", err)
			}
		})
	}
}

func TestLoadAllowsDisabledPATWithoutPepperInProduction(t *testing.T) {
	setValidProductionConfig(t)
	t.Setenv("PAT_ENABLED", "false")
	t.Setenv("PAT_PEPPER", "")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("disabled PAT must not require pepper: %v", err)
	}
	if cfg.PATEnabled {
		t.Fatal("PAT unexpectedly enabled")
	}
}

func TestLoadValidatesPATExpiryBounds(t *testing.T) {
	tests := []struct {
		name       string
		defaultDay string
		maxDay     string
	}{
		{name: "zero default", defaultDay: "0", maxDay: "365"},
		{name: "default above max", defaultDay: "91", maxDay: "90"},
		{name: "max above hard limit", defaultDay: "90", maxDay: "366"},
		{name: "invalid integer", defaultDay: "ninety", maxDay: "365"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("APP_ENV", "development")
			t.Setenv("PAT_ENABLED", "true")
			t.Setenv("PAT_PEPPER", testPATPepper)
			t.Setenv("PAT_DEFAULT_EXPIRY_DAYS", test.defaultDay)
			t.Setenv("PAT_MAX_EXPIRY_DAYS", test.maxDay)
			if _, err := Load(); err == nil {
				t.Fatal("expected PAT expiry validation error")
			}
		})
	}
}
