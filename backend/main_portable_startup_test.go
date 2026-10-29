package main

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
	"ray-train-platform-backend/api"
	"ray-train-platform-backend/config"
)

// Use the real chart defaults, including optional endpoint/region values. A
// zero-valued Config cannot detect accidental cloud defaults in the Helm chart.
func TestHelmDefaultPVCOnlyOptionalStartupAssembly(t *testing.T) {
	raw, err := os.ReadFile("../helm/ray-train-platform/values.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var values map[string]any
	if err := yaml.Unmarshal(raw, &values); err != nil {
		t.Fatal(err)
	}
	chartValue := func(path string) string {
		t.Helper()
		var value any = values
		for _, key := range strings.Split(path, ".") {
			mapping, ok := value.(map[string]any)
			if !ok {
				t.Fatalf("chart value %s is not a mapping", path)
			}
			value, ok = mapping[key]
			if !ok {
				t.Fatalf("missing chart default %s", path)
			}
		}
		return fmt.Sprint(value)
	}
	for _, item := range [][2]string{
		{"APP_ENV", "backend.appEnv"},
		{"OIDC_REQUIRED", "backend.oidcRequired"},
		{"WEB_OIDC_ENABLED", "backend.webOIDCEnabled"},
		{"OAUTH2_PROXY_AUTH_ENABLED", "backend.oauth2ProxyAuthEnabled"},
		{"LOCAL_AUTH_ENABLED", "backend.localAuth.enabled"},
		{"DEMO_MODE", "backend.demoMode"},
		{"SOURCE_ARTIFACTS_ENABLED", "backend.sourceArtifactsEnabled"},
		{"DATA_SPACES_ENABLED", "dataSpaces.enabled"},
		{"WORKSPACE_FSX_COMPATIBLE_ENABLED", "dataSpaces.workspaceCompatibleEnabled"},
		{"DATASET_VERSIONING_ENABLED", "backend.datasetVersioningEnabled"},
		{"RAY_DATA_STREAMING_ENABLED", "backend.rayDataStreamingEnabled"},
		{"DATASET_PUBLISHER_ENABLED", "datasetPublisher.enabled"},
		{"MLFLOW_ENABLED", "backend.mlflow.enabled"},
		{"MLFLOW_DASHBOARD_ENABLED", "backend.mlflow.dashboardEnabled"},
		{"ENVIRONMENT_BUILDS_ENABLED", "backend.environmentBuilds.enabled"},
		{"TOS_ENDPOINT", "tos.endpoint"},
		{"TOS_BUCKET", "tos.bucket"},
		{"TOS_SECRET_NAME", "tos.secretName"},
	} {
		t.Setenv(item[0], chartValue(item[1]))
	}
	// The template injects region and credentials only when a Secret is selected.
	if chartValue("tos.secretName") != "" || chartValue("tos.region") != "" {
		t.Fatal("PVC-only defaults must not select a TOS region or credential Secret")
	}
	for _, key := range []string{"TOS_REGION", "TOS_ACCESS_KEY", "TOS_SECRET_KEY", "TOS_SECURITY_TOKEN", "TOS_OBJECT_SET_QUOTAS_ENABLED", "MIGRATIONS_ONLY"} {
		t.Setenv(key, "")
	}
	t.Setenv("PAT_ENABLED", "true")
	t.Setenv("PAT_PEPPER", strings.Repeat("p", 32))
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("load chart-equivalent configuration: %v", err)
	}
	if cfg.SourceArtifactsEnabled || cfg.DataSpacesEnabled || cfg.TOSObjectSetQuotasEnabled || cfg.DatasetPublisherEnabled || cfg.MLflowEnabled {
		t.Fatal("PVC-only chart silently enabled an object storage component")
	}
	lister, err := newStorageDirectoryLister(cfg)
	if err != nil || lister != nil {
		t.Fatalf("optional storage browser blocks startup: %v", err)
	}
	if api.NewPersonalDataSpaceInitializer(nil) != nil {
		t.Fatal("PVC-only account creation must not initialize object directories")
	}
	if handler, err := newSourceArtifactComponents(nil, cfg); err != nil || handler != nil {
		t.Fatalf("optional source upload blocks startup: %v", err)
	}
	if handler, err := newRayAPIHandler(nil, nil, nil, cfg); err != nil || handler != nil {
		t.Fatalf("optional Ray package API blocks startup: %v", err)
	}
	if manager, err := newDatasetPublicationManager(nil, nil, cfg); err != nil || manager != nil {
		t.Fatalf("optional dataset publisher blocks startup: %v", err)
	}
	if newDatasetPurgeObjects(cfg, lister, nil) != nil {
		t.Fatal("PVC-only startup installed object cleanup")
	}
	if _, err := newEnvironmentBuildService(nil, nil, cfg); err != nil {
		t.Fatalf("optional environment publisher blocks startup: %v", err)
	}
	if _, err := newOIDCValidator(cfg); err != nil {
		t.Fatalf("optional external identity blocks startup: %v", err)
	}
	if _, _, err := newPATComponents(nil, cfg); err != nil {
		t.Fatalf("PAT assembly requires an unrelated object store: %v", err)
	}
	if _, _, err := newLocalAuthComponents(nil, cfg, nil, nil); err != nil {
		t.Fatalf("local login assembly requires an unrelated object store: %v", err)
	}
}
