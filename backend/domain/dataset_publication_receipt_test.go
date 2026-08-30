package domain

import (
	"strings"
	"testing"
)

func validDatasetPublicationReceipt() DatasetPublicationReceipt {
	return DatasetPublicationReceipt{
		DatasetID: "dataset-s1h", DatasetVersionID: "s1h-20260830.1+sha256-12ab34cd",
		Version:           "20260830.1+sha256-12ab34cd",
		ManifestSHA256:    strings.Repeat("a", 64),
		ManifestObjectKey: DefaultDatasetInternalPrefix + "/dataset-s1h/manifests/s1h-20260830.1+sha256-12ab34cd.parquet",
		SchemaVersion:     "parquet-v1", TrainSamples: 100, ValSamples: 10,
		SourceObjectCount: 120, LogicalBytes: 4096, PackedBytes: 3072,
	}
}

func TestDatasetPublicationReceiptBuildsReadyVersionInsideConfiguredPrefix(t *testing.T) {
	receipt := validDatasetPublicationReceipt()
	if err := receipt.ValidateWithInternalPrefix(DefaultDatasetInternalPrefix); err != nil {
		t.Fatalf("validate receipt: %v", err)
	}
	version := receipt.ReadyVersion()
	if version.State != DatasetVersionReady || version.ID != receipt.DatasetVersionID ||
		version.ManifestSHA256 != receipt.ManifestSHA256 || version.TrainSamples != receipt.TrainSamples {
		t.Fatalf("ready version=%+v", version)
	}
	if err := version.ValidateWithInternalPrefix(DefaultDatasetInternalPrefix); err != nil {
		t.Fatalf("validate ready version: %v", err)
	}
}

func TestDatasetPublicationReceiptRejectsEmptyMismatchedAndEscapedPayloads(t *testing.T) {
	base := validDatasetPublicationReceipt()
	tests := []struct {
		name   string
		mutate func(*DatasetPublicationReceipt)
	}{
		{name: "empty samples", mutate: func(value *DatasetPublicationReceipt) { value.TrainSamples, value.ValSamples = 0, 0 }},
		{name: "empty source", mutate: func(value *DatasetPublicationReceipt) { value.SourceObjectCount = 0 }},
		{name: "empty logical bytes", mutate: func(value *DatasetPublicationReceipt) { value.LogicalBytes = 0 }},
		{name: "empty packed bytes", mutate: func(value *DatasetPublicationReceipt) { value.PackedBytes = 0 }},
		{name: "empty manifest digest", mutate: func(value *DatasetPublicationReceipt) { value.ManifestSHA256 = "" }},
		{name: "empty manifest key", mutate: func(value *DatasetPublicationReceipt) { value.ManifestObjectKey = "" }},
		{name: "wrong dataset", mutate: func(value *DatasetPublicationReceipt) { value.DatasetID = "dataset-other" }},
		{name: "wrong prefix", mutate: func(value *DatasetPublicationReceipt) {
			value.ManifestObjectKey = "other/platform/datasets/dataset-s1h/manifests/" + value.DatasetVersionID + ".parquet"
		}},
		{name: "escaped key", mutate: func(value *DatasetPublicationReceipt) {
			value.ManifestObjectKey = DefaultDatasetInternalPrefix + "/dataset-s1h/manifests/%2e%2e.parquet"
		}},
		{name: "bad digest", mutate: func(value *DatasetPublicationReceipt) { value.ManifestSHA256 = "secret" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := base
			test.mutate(&value)
			if err := value.ValidateWithInternalPrefix(DefaultDatasetInternalPrefix); err == nil {
				t.Fatalf("invalid receipt accepted: %+v", value)
			}
		})
	}
}
