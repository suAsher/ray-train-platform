package main

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/volcengine/ve-tos-golang-sdk/v2/tos"
)

func TestCreatePrefixesWritesEachScopedPrefix(t *testing.T) {
	var got []string
	err := createPrefixes(context.Background(), "shanghai-data-transfer", func(_ context.Context, bucket, key string) error {
		if bucket != "shanghai-data-transfer" {
			t.Fatalf("bucket = %q, want shanghai-data-transfer", bucket)
		}
		got = append(got, key)
		return nil
	})
	if err != nil {
		t.Fatalf("createPrefixes() error = %v", err)
	}
	want := []string{
		"ray-train/tenants/local/datasets/",
		"ray-train/tenants/local/checkpoints/",
		"ray-train/tenants/local/outputs/",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("created keys = %v, want %v", got, want)
	}
}

func TestCreatePrefixesStopsOnWriteFailure(t *testing.T) {
	writes := 0
	err := createPrefixes(context.Background(), "bucket", func(_ context.Context, _, _ string) error {
		writes++
		return errors.New("put failed")
	})
	if err == nil {
		t.Fatal("createPrefixes() error = nil, want error")
	}
	if writes != 1 {
		t.Fatalf("writes = %d, want 1", writes)
	}
}

func TestVerifyPrefixesListsEachScopedPrefix(t *testing.T) {
	var got []string
	err := verifyPrefixes(context.Background(), "shanghai-data-transfer", func(_ context.Context, bucket, prefix string) error {
		if bucket != "shanghai-data-transfer" {
			t.Fatalf("bucket = %q, want shanghai-data-transfer", bucket)
		}
		got = append(got, prefix)
		return nil
	})
	if err != nil {
		t.Fatalf("verifyPrefixes() error = %v", err)
	}
	want := []string{
		"ray-train/tenants/local/datasets/",
		"ray-train/tenants/local/checkpoints/",
		"ray-train/tenants/local/outputs/",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("listed prefixes = %v, want %v", got, want)
	}
}

func TestVerifyPrefixesStopsOnListFailure(t *testing.T) {
	lists := 0
	err := verifyPrefixes(context.Background(), "bucket", func(_ context.Context, _, _ string) error {
		lists++
		return errors.New("list denied")
	})
	if err == nil {
		t.Fatal("verifyPrefixes() error = nil, want error")
	}
	if lists != 1 {
		t.Fatalf("lists = %d, want 1", lists)
	}
}

func TestTOSErrorMessageReturnsOnlyServerMessage(t *testing.T) {
	err := &tos.TosServerError{TosError: tos.TosError{Message: "expected region cn-shanghai"}}
	if got, want := tosErrorMessage(err), "expected region cn-shanghai"; got != want {
		t.Fatalf("tosErrorMessage() = %q, want %q", got, want)
	}
	if got := tosErrorMessage(errors.New("client failure")); got != "" {
		t.Fatalf("tosErrorMessage(client error) = %q, want empty", got)
	}
}

func TestLoadConfigAcceptsScopedPrefixOverride(t *testing.T) {
	values := map[string]string{
		"TOS_ENDPOINT": "https://tos.internal", "TOS_REGION": "cn-shanghai",
		"TOS_BUCKET": "vke-cluster", "TOS_ACCESS_KEY": "ak", "TOS_SECRET_KEY": "sk",
		"TOS_PREFIXES": "ray-train/platform/mlflow-artifacts/",
	}
	settings, err := loadConfig(func(key string) string { return values[key] })
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(settings.Prefixes, []string{"ray-train/platform/mlflow-artifacts/"}) {
		t.Fatalf("unexpected prefixes: %#v", settings.Prefixes)
	}
}

func TestLoadConfigRejectsUnsafePrefixOverride(t *testing.T) {
	values := map[string]string{
		"TOS_ENDPOINT": "https://tos.internal", "TOS_REGION": "cn-shanghai",
		"TOS_BUCKET": "vke-cluster", "TOS_ACCESS_KEY": "ak", "TOS_SECRET_KEY": "sk",
		"TOS_PREFIXES": "ray-train/../other/",
	}
	if _, err := loadConfig(func(key string) string { return values[key] }); err == nil {
		t.Fatal("expected unsafe prefix rejection")
	}
}
