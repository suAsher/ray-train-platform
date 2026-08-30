package domain

import (
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"
)

const validDatasetDigest = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func validDataset(visibility DatasetVisibility) Dataset {
	dataset := Dataset{
		ID:                 "dataset-labeled-full",
		Slug:               "labeled-full",
		Name:               "Labeled full",
		Description:        "Validated labeled scenes",
		SourceSpace:        DataSpacePublic,
		SourceRelativePath: "labeled/full",
		Visibility:         visibility,
		SchemaVersion:      "bev-v1",
	}
	if visibility == DatasetVisibilityTeam {
		dataset.OwnerTenantID = "tenant-a"
		dataset.SourceSpace = DataSpaceTeamShared
	}
	return dataset
}

func validDatasetVersion(state DatasetVersionState) DatasetVersion {
	return DatasetVersion{
		ID:                "labeled-full-20260830.2+sha256-12ab34cd",
		DatasetID:         "dataset-labeled-full",
		Version:           "20260830.2+sha256-12ab34cd",
		State:             state,
		ManifestSHA256:    validDatasetDigest,
		ManifestObjectKey: "ray-train/platform/datasets/dataset-labeled-full/manifests/version.parquet",
		SchemaVersion:     "bev-v1",
		TrainSamples:      100,
		ValSamples:        20,
		TestSamples:       10,
		SourceObjectCount: 400,
		LogicalBytes:      1024,
		PackedBytes:       512,
	}
}

func TestDatasetValidate(t *testing.T) {
	longID := strings.Repeat("a", DatasetIdentifierMaxBytes+1)
	longPath := strings.Repeat("a", DatasetPathMaxBytes+1)
	tests := []struct {
		name   string
		mutate func(*Dataset)
	}{
		{name: "empty ID", mutate: func(dataset *Dataset) { dataset.ID = "" }},
		{name: "bounded ID", mutate: func(dataset *Dataset) { dataset.ID = longID }},
		{name: "ID whitespace", mutate: func(dataset *Dataset) { dataset.ID = "dataset bad" }},
		{name: "slug uppercase", mutate: func(dataset *Dataset) { dataset.Slug = "Labeled" }},
		{name: "slug traversal", mutate: func(dataset *Dataset) { dataset.Slug = ".." }},
		{name: "unknown source space", mutate: func(dataset *Dataset) { dataset.SourceSpace = DataSpaceID("unknown") }},
		{name: "absolute source path", mutate: func(dataset *Dataset) { dataset.SourceRelativePath = "/labeled" }},
		{name: "URI source path", mutate: func(dataset *Dataset) { dataset.SourceRelativePath = "tos://bucket/labeled" }},
		{name: "dot source path", mutate: func(dataset *Dataset) { dataset.SourceRelativePath = "." }},
		{name: "traversal source path", mutate: func(dataset *Dataset) { dataset.SourceRelativePath = "labeled/../private" }},
		{name: "non canonical source path", mutate: func(dataset *Dataset) { dataset.SourceRelativePath = "labeled//full" }},
		{name: "source path whitespace", mutate: func(dataset *Dataset) { dataset.SourceRelativePath = " labeled/full" }},
		{name: "source path control", mutate: func(dataset *Dataset) { dataset.SourceRelativePath = "labeled/\nfull" }},
		{name: "escaped slash", mutate: func(dataset *Dataset) { dataset.SourceRelativePath = "labeled%2f..%2fprivate" }},
		{name: "escaped traversal", mutate: func(dataset *Dataset) { dataset.SourceRelativePath = "%2e%2e/private" }},
		{name: "escaped control", mutate: func(dataset *Dataset) { dataset.SourceRelativePath = "labeled%00x" }},
		{name: "escaped query", mutate: func(dataset *Dataset) { dataset.SourceRelativePath = "labeled%3fsecret" }},
		{name: "escaped fragment", mutate: func(dataset *Dataset) { dataset.SourceRelativePath = "labeled%23secret" }},
		{name: "source path too long", mutate: func(dataset *Dataset) { dataset.SourceRelativePath = longPath }},
		{name: "unsafe schema version", mutate: func(dataset *Dataset) { dataset.SchemaVersion = "../v2" }},
		{name: "unsupported visibility", mutate: func(dataset *Dataset) { dataset.Visibility = DatasetVisibility("PRIVATE") }},
		{name: "public owner", mutate: func(dataset *Dataset) { dataset.OwnerTenantID = "tenant-a" }},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dataset := validDataset(DatasetVisibilityPublic)
			tc.mutate(&dataset)
			if err := dataset.Validate(); err == nil {
				t.Fatalf("invalid dataset was accepted: %#v", dataset)
			}
		})
	}

	for _, dataset := range []Dataset{validDataset(DatasetVisibilityPublic), validDataset(DatasetVisibilityTeam)} {
		if err := dataset.Validate(); err != nil {
			t.Fatalf("valid dataset rejected: %v", err)
		}
	}

	teamWithoutOwner := validDataset(DatasetVisibilityTeam)
	teamWithoutOwner.OwnerTenantID = ""
	if err := teamWithoutOwner.Validate(); err == nil {
		t.Fatal("TEAM dataset without owner tenant was accepted")
	}
}

func TestDatasetVersionValidateManifestAndCounts(t *testing.T) {
	longPath := strings.Repeat("a", DatasetPathMaxBytes+1)
	tests := []struct {
		name   string
		state  DatasetVersionState
		mutate func(*DatasetVersion)
	}{
		{name: "unknown state", state: DatasetVersionReady, mutate: func(version *DatasetVersion) { version.State = "UNKNOWN" }},
		{name: "latest persisted ID", state: DatasetVersionReady, mutate: func(version *DatasetVersion) { version.ID = "latest" }},
		{name: "unsafe dataset ID", state: DatasetVersionReady, mutate: func(version *DatasetVersion) { version.DatasetID = "../dataset" }},
		{name: "unsafe version", state: DatasetVersionReady, mutate: func(version *DatasetVersion) { version.Version = "version/one" }},
		{name: "unsafe schema", state: DatasetVersionReady, mutate: func(version *DatasetVersion) { version.SchemaVersion = "schema one" }},
		{name: "missing ready digest", state: DatasetVersionReady, mutate: func(version *DatasetVersion) { version.ManifestSHA256 = "" }},
		{name: "uppercase ready digest", state: DatasetVersionReady, mutate: func(version *DatasetVersion) { version.ManifestSHA256 = strings.ToUpper(validDatasetDigest) }},
		{name: "short ready digest", state: DatasetVersionReady, mutate: func(version *DatasetVersion) { version.ManifestSHA256 = "0123" }},
		{name: "missing ready object key", state: DatasetVersionReady, mutate: func(version *DatasetVersion) { version.ManifestObjectKey = "" }},
		{name: "absolute object key", state: DatasetVersionReady, mutate: func(version *DatasetVersion) { version.ManifestObjectKey = "/manifest.json" }},
		{name: "object key traversal", state: DatasetVersionReady, mutate: func(version *DatasetVersion) { version.ManifestObjectKey = "manifests/../private.json" }},
		{name: "escaped object key", state: DatasetVersionReady, mutate: func(version *DatasetVersion) { version.ManifestObjectKey = "manifests%2f..%2fprivate.json" }},
		{name: "object key too long", state: DatasetVersionReady, mutate: func(version *DatasetVersion) { version.ManifestObjectKey = longPath }},
		{name: "negative train samples", state: DatasetVersionReady, mutate: func(version *DatasetVersion) { version.TrainSamples = -1 }},
		{name: "negative val samples", state: DatasetVersionReady, mutate: func(version *DatasetVersion) { version.ValSamples = -1 }},
		{name: "negative test samples", state: DatasetVersionReady, mutate: func(version *DatasetVersion) { version.TestSamples = -1 }},
		{name: "negative source objects", state: DatasetVersionReady, mutate: func(version *DatasetVersion) { version.SourceObjectCount = -1 }},
		{name: "negative logical bytes", state: DatasetVersionReady, mutate: func(version *DatasetVersion) { version.LogicalBytes = -1 }},
		{name: "negative packed bytes", state: DatasetVersionReady, mutate: func(version *DatasetVersion) { version.PackedBytes = -1 }},
		{name: "malformed optional digest", state: DatasetVersionPacking, mutate: func(version *DatasetVersion) { version.ManifestSHA256 = "bad" }},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			version := validDatasetVersion(tc.state)
			tc.mutate(&version)
			if err := version.Validate(); err == nil {
				t.Fatalf("invalid version was accepted: %#v", version)
			}
		})
	}

	for _, state := range []DatasetVersionState{
		DatasetVersionDiscovering,
		DatasetVersionStabilizing,
		DatasetVersionValidating,
		DatasetVersionPacking,
		DatasetVersionReady,
		DatasetVersionFailed,
		DatasetVersionDeprecated,
		DatasetVersionRetired,
	} {
		version := validDatasetVersion(state)
		if err := version.Validate(); err != nil {
			t.Fatalf("valid %s version rejected: %v", state, err)
		}
	}

	discovering := validDatasetVersion(DatasetVersionDiscovering)
	discovering.ManifestSHA256 = ""
	discovering.ManifestObjectKey = ""
	if err := discovering.Validate(); err != nil {
		t.Fatalf("pre-ready version without manifest rejected: %v", err)
	}
}

func TestDatasetVersionManifestObjectKeyIsPrivate(t *testing.T) {
	encoded, err := json.Marshal(validDatasetVersion(DatasetVersionReady))
	if err != nil {
		t.Fatalf("marshal dataset version: %v", err)
	}
	if strings.Contains(string(encoded), "manifestObjectKey") || strings.Contains(string(encoded), "platform/datasets") {
		t.Fatalf("dataset version leaked its internal manifest object key: %s", encoded)
	}
}

func TestDatasetVersionTransitionTo(t *testing.T) {
	validTransitions := []struct {
		from DatasetVersionState
		to   DatasetVersionState
	}{
		{DatasetVersionDiscovering, DatasetVersionStabilizing},
		{DatasetVersionDiscovering, DatasetVersionFailed},
		{DatasetVersionStabilizing, DatasetVersionValidating},
		{DatasetVersionStabilizing, DatasetVersionFailed},
		{DatasetVersionValidating, DatasetVersionPacking},
		{DatasetVersionValidating, DatasetVersionFailed},
		{DatasetVersionPacking, DatasetVersionReady},
		{DatasetVersionPacking, DatasetVersionFailed},
		{DatasetVersionFailed, DatasetVersionDiscovering},
		{DatasetVersionReady, DatasetVersionDeprecated},
		{DatasetVersionDeprecated, DatasetVersionRetired},
	}

	for _, tc := range validTransitions {
		t.Run(string(tc.from)+" to "+string(tc.to), func(t *testing.T) {
			original := validDatasetVersion(tc.from)
			before := original
			transitioned, err := original.TransitionTo(tc.to)
			if err != nil {
				t.Fatalf("transition rejected: %v", err)
			}
			if original != before {
				t.Fatalf("transition mutated original: got %#v, want %#v", original, before)
			}
			want := before
			want.State = tc.to
			if !reflect.DeepEqual(transitioned, want) {
				t.Fatalf("transition changed immutable fields: got %#v, want %#v", transitioned, want)
			}
		})
	}

	invalidTransitions := []struct {
		from DatasetVersionState
		to   DatasetVersionState
	}{
		{DatasetVersionDiscovering, DatasetVersionReady},
		{DatasetVersionStabilizing, DatasetVersionReady},
		{DatasetVersionValidating, DatasetVersionReady},
		{DatasetVersionPacking, DatasetVersionDeprecated},
		{DatasetVersionFailed, DatasetVersionReady},
		{DatasetVersionReady, DatasetVersionFailed},
		{DatasetVersionDeprecated, DatasetVersionReady},
		{DatasetVersionRetired, DatasetVersionDiscovering},
		{DatasetVersionReady, DatasetVersionReady},
	}

	for _, tc := range invalidTransitions {
		t.Run("reject "+string(tc.from)+" to "+string(tc.to), func(t *testing.T) {
			version := validDatasetVersion(tc.from)
			if _, err := version.TransitionTo(tc.to); err == nil {
				t.Fatal("invalid transition was accepted")
			}
		})
	}
}

func TestDatasetVersionTransitionRejectsInvalidImmutableFields(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*DatasetVersion)
	}{
		{name: "manifest digest", mutate: func(version *DatasetVersion) { version.ManifestSHA256 = "bad" }},
		{name: "manifest object key", mutate: func(version *DatasetVersion) { version.ManifestObjectKey = "../manifest" }},
		{name: "schema version", mutate: func(version *DatasetVersion) { version.SchemaVersion = "schema/v2" }},
		{name: "sample counts", mutate: func(version *DatasetVersion) { version.TrainSamples = -1 }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			version := validDatasetVersion(DatasetVersionReady)
			tc.mutate(&version)
			if _, err := version.TransitionTo(DatasetVersionDeprecated); err == nil {
				t.Fatal("transition accepted a READY version with mutated immutable fields")
			}
		})
	}
}

func TestParseDatasetVersionSelector(t *testing.T) {
	tests := []struct {
		name          string
		raw           string
		wantLatest    bool
		wantVersionID string
		wantErr       bool
	}{
		{name: "latest", raw: "latest", wantLatest: true},
		{name: "concrete", raw: "labeled-full-20260830.2+sha256-12ab34cd", wantVersionID: "labeled-full-20260830.2+sha256-12ab34cd"},
		{name: "empty", raw: "", wantErr: true},
		{name: "whitespace", raw: " latest", wantErr: true},
		{name: "dot", raw: ".", wantErr: true},
		{name: "traversal", raw: "../version", wantErr: true},
		{name: "URI", raw: "https://example.test/version", wantErr: true},
		{name: "control", raw: "version\n1", wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			selector, err := ParseDatasetVersionSelector(tc.raw)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("invalid selector %q was accepted: %#v", tc.raw, selector)
				}
				return
			}
			if err != nil {
				t.Fatalf("parse selector: %v", err)
			}
			if selector.Latest != tc.wantLatest || selector.VersionID != tc.wantVersionID {
				t.Fatalf("selector = %#v, want latest=%t versionID=%q", selector, tc.wantLatest, tc.wantVersionID)
			}
		})
	}
}

func TestValidateResolvedDatasetVersionIDRejectsLatest(t *testing.T) {
	tests := []struct {
		raw     string
		wantErr bool
	}{
		{raw: "labeled-full-20260830.2+sha256-12ab34cd"},
		{raw: "latest", wantErr: true},
		{raw: "", wantErr: true},
		{raw: "../version", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.raw, func(t *testing.T) {
			err := ValidateResolvedDatasetVersionID(tc.raw)
			if (err != nil) != tc.wantErr {
				t.Fatalf("ValidateResolvedDatasetVersionID(%q) error = %v, wantErr %t", tc.raw, err, tc.wantErr)
			}
		})
	}
}

func TestDatasetAuthorization(t *testing.T) {
	public := validDataset(DatasetVisibilityPublic)
	team := validDataset(DatasetVisibilityTeam)
	tests := []struct {
		name       string
		dataset    Dataset
		tenantID   string
		superAdmin bool
		wantView   bool
		wantManage bool
	}{
		{name: "authenticated tenant views public", dataset: public, tenantID: "tenant-b", wantView: true},
		{name: "anonymous cannot view public", dataset: public},
		{name: "tenant cannot manage public", dataset: public, tenantID: "tenant-b", wantView: true},
		{name: "superadmin manages public", dataset: public, superAdmin: true, wantView: true, wantManage: true},
		{name: "owner views and manages team", dataset: team, tenantID: "tenant-a", wantView: true, wantManage: true},
		{name: "other tenant cannot access team", dataset: team, tenantID: "tenant-b"},
		{name: "superadmin manages team", dataset: team, superAdmin: true, wantView: true, wantManage: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := CanViewDataset(tc.dataset, tc.tenantID, tc.superAdmin); got != tc.wantView {
				t.Fatalf("CanViewDataset() = %t, want %t", got, tc.wantView)
			}
			if got := CanManageDataset(tc.dataset, tc.tenantID, tc.superAdmin); got != tc.wantManage {
				t.Fatalf("CanManageDataset() = %t, want %t", got, tc.wantManage)
			}
		})
	}
}

func TestDatasetProgressRecordsValidate(t *testing.T) {
	validPartition := DatasetPartition{
		ID: "partition-1", DatasetVersionID: "version-1", Name: "train-0001",
		SourceObjectCount: 10, ProcessedObjectCount: 8, FailedObjectCount: 1,
		LogicalBytes: 1000, PackedBytes: 500,
	}
	validRun := DatasetPublicationRun{
		ID: "publication-1", DatasetID: "dataset-1", DatasetVersionID: "version-1",
		State: DatasetVersionPacking, TotalPartitions: 10, CompletedPartitions: 7, FailedPartitions: 1,
		SourceObjectCount: 100, ProcessedObjectCount: 80, FailedObjectCount: 2,
	}
	validObservation := DatasetCacheObservation{
		ID: "cache-observation-1", DatasetVersionID: "version-1", TrainingJobID: "job-1", NodeName: "gpu-node-1",
		CacheHitCount: 10, CacheMissCount: 2, CacheHitBytes: 1000, CacheMissBytes: 200,
		CachedBytes: 800, EvictedBytes: 100, ChecksumFailureCount: 1, PrefetchWaitMilliseconds: 25,
	}

	tests := []struct {
		name    string
		valid   func() error
		invalid func() error
	}{
		{name: "partition", valid: validPartition.Validate, invalid: func() error { value := validPartition; value.ProcessedObjectCount = -1; return value.Validate() }},
		{name: "publication", valid: validRun.Validate, invalid: func() error { value := validRun; value.CompletedPartitions = 11; return value.Validate() }},
		{name: "cache observation", valid: validObservation.Validate, invalid: func() error { value := validObservation; value.CacheHitBytes = -1; return value.Validate() }},
		{name: "partition overflow", valid: validPartition.Validate, invalid: func() error {
			value := validPartition
			value.SourceObjectCount = math.MaxInt64
			value.ProcessedObjectCount = math.MaxInt64
			value.FailedObjectCount = 1
			return value.Validate()
		}},
		{name: "publication overflow", valid: validRun.Validate, invalid: func() error {
			value := validRun
			value.TotalPartitions = math.MaxInt64
			value.CompletedPartitions = math.MaxInt64
			value.FailedPartitions = 1
			return value.Validate()
		}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.valid(); err != nil {
				t.Fatalf("valid record rejected: %v", err)
			}
			if err := tc.invalid(); err == nil {
				t.Fatal("invalid record was accepted")
			}
		})
	}
}
