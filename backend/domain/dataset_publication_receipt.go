package domain

import "fmt"

// DatasetPublicationReceipt is the small, immutable completion contract
// emitted by a publisher Job. It intentionally contains no credentials or
// user-facing object-store URI. The manifest object key stays internal and is
// validated against the deployment-owned prefix before the version becomes
// READY.
type DatasetPublicationReceipt struct {
	DatasetID         string
	DatasetVersionID  string
	Version           string
	ManifestSHA256    string
	ManifestObjectKey string
	SchemaVersion     string
	TrainSamples      int64
	ValSamples        int64
	TestSamples       int64
	SourceObjectCount int64
	LogicalBytes      int64
	PackedBytes       int64
}

func (receipt DatasetPublicationReceipt) ValidateWithInternalPrefix(rawPrefix string) error {
	// A receipt is the commit contract for a READY version, not an in-flight
	// progress payload. Validate the READY shape so an empty digest or object
	// key can never cross the catalogue commit point.
	version := receipt.datasetVersion(DatasetVersionReady)
	if err := version.ValidateWithInternalPrefix(rawPrefix); err != nil {
		return fmt.Errorf("invalid dataset publication receipt: %w", err)
	}
	if receipt.TrainSamples+receipt.ValSamples+receipt.TestSamples <= 0 {
		return fmt.Errorf("invalid dataset publication receipt: at least one sample is required")
	}
	if receipt.SourceObjectCount <= 0 || receipt.LogicalBytes <= 0 || receipt.PackedBytes <= 0 {
		return fmt.Errorf("invalid dataset publication receipt: positive source and byte counts are required")
	}
	return nil
}

// ReadyVersion materializes the immutable catalogue payload after the receipt
// has been verified by the controller. Repositories validate it again before
// atomically committing the READY transition.
func (receipt DatasetPublicationReceipt) ReadyVersion() DatasetVersion {
	return receipt.datasetVersion(DatasetVersionReady)
}

func (receipt DatasetPublicationReceipt) datasetVersion(state DatasetVersionState) DatasetVersion {
	return DatasetVersion{
		ID: receipt.DatasetVersionID, DatasetID: receipt.DatasetID,
		Version: receipt.Version, State: state,
		ManifestSHA256: receipt.ManifestSHA256, ManifestObjectKey: receipt.ManifestObjectKey,
		SchemaVersion: receipt.SchemaVersion, TrainSamples: receipt.TrainSamples,
		ValSamples: receipt.ValSamples, TestSamples: receipt.TestSamples,
		SourceObjectCount: receipt.SourceObjectCount, LogicalBytes: receipt.LogicalBytes,
		PackedBytes: receipt.PackedBytes,
	}
}
