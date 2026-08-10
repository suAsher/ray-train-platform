package config

import (
	"strings"
	"testing"
)

func TestSourceArtifactAbuseLimitsDefaultsAndValidation(t *testing.T) {
	t.Run("defaults", func(t *testing.T) {
		t.Setenv("APP_ENV", "development")
		t.Setenv("PAT_ENABLED", "false")
		cfg, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		if cfg.SourceArtifactMaxPending != 10 || cfg.SourceArtifactQuotaBytes != 100*1024*1024*1024 {
			t.Fatalf("unexpected defaults: pending=%d quota=%d", cfg.SourceArtifactMaxPending, cfg.SourceArtifactQuotaBytes)
		}
	})
	for _, test := range []struct {
		name, pending, quota, want string
	}{
		{name: "zero pending", pending: "0", quota: "2147483648", want: "SOURCE_ARTIFACT_MAX_PENDING"},
		{name: "quota below one artifact", pending: "10", quota: "2147483647", want: "SOURCE_ARTIFACT_QUOTA_BYTES"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("APP_ENV", "development")
			t.Setenv("PAT_ENABLED", "false")
			t.Setenv("SOURCE_ARTIFACTS_ENABLED", "true")
			t.Setenv("TOS_ENDPOINT", "https://tos.example.com")
			t.Setenv("TOS_REGION", "cn")
			t.Setenv("TOS_BUCKET", "bucket")
			t.Setenv("TOS_ACCESS_KEY", "ak")
			t.Setenv("TOS_SECRET_KEY", "sk")
			t.Setenv("SOURCE_ARTIFACT_MAX_PENDING", test.pending)
			t.Setenv("SOURCE_ARTIFACT_QUOTA_BYTES", test.quota)
			_, err := Load()
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("expected %s validation, got %v", test.want, err)
			}
		})
	}
}
