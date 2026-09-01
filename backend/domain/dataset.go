package domain

import (
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strings"
	"time"
	"unicode"
)

const (
	DatasetIdentifierMaxBytes    = 128
	DatasetPathMaxBytes          = 4096
	DefaultDatasetInternalPrefix = "ray-train/platform/datasets"
)

type DatasetVisibility string

const (
	DatasetVisibilityPublic DatasetVisibility = "PUBLIC"
	DatasetVisibilityTeam   DatasetVisibility = "TEAM"
)

type DatasetVersionState string

const (
	DatasetVersionDiscovering DatasetVersionState = "DISCOVERING"
	DatasetVersionStabilizing DatasetVersionState = "STABILIZING"
	DatasetVersionValidating  DatasetVersionState = "VALIDATING"
	DatasetVersionPacking     DatasetVersionState = "PACKING"
	DatasetVersionReady       DatasetVersionState = "READY"
	DatasetVersionFailed      DatasetVersionState = "FAILED"
	DatasetVersionDeprecated  DatasetVersionState = "DEPRECATED"
	DatasetVersionRetired     DatasetVersionState = "RETIRED"
)

var (
	datasetIdentifierPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,127}$`)
	datasetSlugPattern       = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,62}[a-z0-9])?$`)
	datasetDigestPattern     = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

type Dataset struct {
	ID                 string            `json:"id"`
	Slug               string            `json:"slug"`
	Name               string            `json:"name"`
	Description        string            `json:"description,omitempty"`
	SourceSpace        DataSpaceID       `json:"sourceSpace"`
	SourceRelativePath string            `json:"sourceRelativePath"`
	OwnerTenantID      string            `json:"ownerTenantId,omitempty"`
	Visibility         DatasetVisibility `json:"visibility"`
	SchemaVersion      string            `json:"schemaVersion"`
}

func (dataset Dataset) Validate() error {
	if err := validateDatasetIdentifier("dataset ID", dataset.ID); err != nil {
		return err
	}
	if !datasetSlugPattern.MatchString(dataset.Slug) || dataset.Slug == "." || dataset.Slug == ".." {
		return fmt.Errorf("dataset slug must be a lowercase path-safe identifier")
	}
	if strings.TrimSpace(dataset.Name) == "" || strings.TrimSpace(dataset.Name) != dataset.Name || strings.IndexFunc(dataset.Name, unicode.IsControl) >= 0 {
		return fmt.Errorf("dataset name is invalid")
	}
	if strings.IndexFunc(dataset.Description, unicode.IsControl) >= 0 {
		return fmt.Errorf("dataset description is invalid")
	}
	if dataset.SourceSpace != DataSpacePublic && dataset.SourceSpace != DataSpaceTeamShared {
		return fmt.Errorf("dataset source space must be public or team-shared")
	}
	if err := validateDatasetRelativePath("dataset source path", dataset.SourceRelativePath); err != nil {
		return err
	}
	if err := validateDatasetIdentifier("schema version", dataset.SchemaVersion); err != nil {
		return err
	}
	switch dataset.Visibility {
	case DatasetVisibilityPublic:
		if dataset.OwnerTenantID != "" || dataset.SourceSpace != DataSpacePublic {
			return fmt.Errorf("public dataset must use public source without an owner tenant")
		}
	case DatasetVisibilityTeam:
		if err := validateDatasetIdentifier("owner tenant", dataset.OwnerTenantID); err != nil {
			return err
		}
		if dataset.SourceSpace != DataSpaceTeamShared {
			return fmt.Errorf("team dataset must use team-shared source")
		}
	default:
		return fmt.Errorf("unsupported dataset visibility %q", dataset.Visibility)
	}
	return nil
}

type DatasetVersion struct {
	ID                string              `json:"id"`
	DatasetID         string              `json:"datasetId"`
	Version           string              `json:"version"`
	State             DatasetVersionState `json:"state"`
	ManifestSHA256    string              `json:"manifestSha256,omitempty"`
	ManifestObjectKey string              `json:"-"`
	SchemaVersion     string              `json:"schemaVersion"`
	TrainSamples      int64               `json:"trainSamples"`
	ValSamples        int64               `json:"valSamples"`
	TestSamples       int64               `json:"testSamples"`
	SourceObjectCount int64               `json:"sourceObjectCount"`
	LogicalBytes      int64               `json:"logicalBytes"`
	PackedBytes       int64               `json:"packedBytes"`
}

func (version DatasetVersion) Validate() error {
	if err := ValidateResolvedDatasetVersionID(version.ID); err != nil {
		return err
	}
	if err := validateDatasetIdentifier("dataset ID", version.DatasetID); err != nil {
		return err
	}
	if err := validateDatasetIdentifier("version", version.Version); err != nil {
		return err
	}
	if err := validateDatasetIdentifier("schema version", version.SchemaVersion); err != nil {
		return err
	}
	if !knownDatasetVersionState(version.State) {
		return fmt.Errorf("unsupported dataset version state %q", version.State)
	}
	for name, value := range map[string]int64{
		"train samples": version.TrainSamples, "validation samples": version.ValSamples,
		"test samples": version.TestSamples, "source object count": version.SourceObjectCount,
		"logical bytes": version.LogicalBytes, "packed bytes": version.PackedBytes,
	} {
		if value < 0 {
			return fmt.Errorf("%s must be nonnegative", name)
		}
	}
	if version.ManifestSHA256 != "" && !datasetDigestPattern.MatchString(version.ManifestSHA256) {
		return fmt.Errorf("manifest SHA-256 must be 64 lowercase hexadecimal characters")
	}
	if version.ManifestObjectKey != "" {
		if err := validateDatasetRelativePath("manifest object key", version.ManifestObjectKey); err != nil {
			return err
		}
		suffix := path.Join(version.DatasetID, "manifests", version.ID+".parquet")
		marker := "/" + suffix
		if !strings.HasSuffix(version.ManifestObjectKey, marker) {
			return fmt.Errorf("manifest object key must belong to its dataset and version")
		}
		if _, err := NormalizeDatasetInternalPrefix(strings.TrimSuffix(version.ManifestObjectKey, marker)); err != nil {
			return fmt.Errorf("manifest object key has an invalid internal prefix: %w", err)
		}
	}
	if version.State == DatasetVersionReady || version.State == DatasetVersionDeprecated || version.State == DatasetVersionRetired {
		if version.ManifestSHA256 == "" || version.ManifestObjectKey == "" {
			return fmt.Errorf("ready dataset version requires a manifest")
		}
	}
	return nil
}

// ValidateWithInternalPrefix adds the deployment-specific storage boundary to
// the structural DatasetVersion validation. Persistence can validate the
// immutable key shape without configuration, while API and publisher paths
// must call this method with their configured private prefix.
func (version DatasetVersion) ValidateWithInternalPrefix(rawPrefix string) error {
	if err := version.Validate(); err != nil {
		return err
	}
	if version.ManifestObjectKey == "" {
		return nil
	}
	prefix, err := NormalizeDatasetInternalPrefix(rawPrefix)
	if err != nil {
		return err
	}
	expected := path.Join(prefix, version.DatasetID, "manifests", version.ID+".parquet")
	if version.ManifestObjectKey != expected {
		return fmt.Errorf("manifest object key must use the configured internal dataset prefix")
	}
	return nil
}

func NormalizeDatasetInternalPrefix(raw string) (string, error) {
	prefix := strings.TrimSuffix(strings.TrimSpace(raw), "/")
	if err := validateDatasetRelativePath("dataset internal prefix", prefix); err != nil {
		return "", err
	}
	return prefix, nil
}

func (version DatasetVersion) TransitionTo(next DatasetVersionState) (DatasetVersion, error) {
	if err := version.Validate(); err != nil {
		return DatasetVersion{}, err
	}
	allowed := map[DatasetVersionState]map[DatasetVersionState]bool{
		DatasetVersionDiscovering: {DatasetVersionStabilizing: true, DatasetVersionFailed: true},
		DatasetVersionStabilizing: {DatasetVersionValidating: true, DatasetVersionFailed: true},
		DatasetVersionValidating:  {DatasetVersionPacking: true, DatasetVersionFailed: true},
		DatasetVersionPacking:     {DatasetVersionReady: true, DatasetVersionFailed: true},
		DatasetVersionFailed:      {DatasetVersionDiscovering: true},
		DatasetVersionReady:       {DatasetVersionDeprecated: true},
		DatasetVersionDeprecated:  {DatasetVersionRetired: true},
	}
	if !allowed[version.State][next] {
		return DatasetVersion{}, fmt.Errorf("dataset version cannot transition from %q to %q", version.State, next)
	}
	result := version
	result.State = next
	if err := result.Validate(); err != nil {
		return DatasetVersion{}, err
	}
	return result, nil
}

type DatasetVersionSelector struct {
	Latest    bool   `json:"latest"`
	VersionID string `json:"versionId,omitempty"`
}

func ParseDatasetVersionSelector(raw string) (DatasetVersionSelector, error) {
	if raw == "latest" {
		return DatasetVersionSelector{Latest: true}, nil
	}
	if err := ValidateResolvedDatasetVersionID(raw); err != nil {
		return DatasetVersionSelector{}, err
	}
	return DatasetVersionSelector{VersionID: raw}, nil
}

func ValidateResolvedDatasetVersionID(raw string) error {
	if raw == "latest" {
		return fmt.Errorf("latest must be resolved before persistence")
	}
	return validateDatasetIdentifier("dataset version ID", raw)
}

type DatasetPartitionState string

const (
	DatasetPartitionPending   DatasetPartitionState = "PENDING"
	DatasetPartitionLeased    DatasetPartitionState = "LEASED"
	DatasetPartitionCompleted DatasetPartitionState = "COMPLETED"
	DatasetPartitionFailed    DatasetPartitionState = "FAILED"
)

type DatasetPartition struct {
	ID, DatasetVersionID, Name                   string
	SourceObjectCount, ProcessedObjectCount      int64
	FailedObjectCount, LogicalBytes, PackedBytes int64
}

func (partition DatasetPartition) Validate() error {
	for name, value := range map[string]string{"partition ID": partition.ID, "dataset version ID": partition.DatasetVersionID, "partition name": partition.Name} {
		if err := validateDatasetIdentifier(name, value); err != nil {
			return err
		}
	}
	return validateProgressCounts(partition.SourceObjectCount, partition.ProcessedObjectCount, partition.FailedObjectCount, partition.LogicalBytes, partition.PackedBytes)
}

// DatasetPublicationPartitionAttempt is mutable execution state kept separate
// from the immutable DatasetPartition plan row.
type DatasetPublicationPartitionAttempt struct {
	DatasetVersionID, PartitionID, InputFingerprint, PlanSHA256, ReceiptSHA256 string
	State                                                                      DatasetPartitionState
	Attempt                                                                    int64
	LeaseOwner                                                                 string
	LeaseExpiresAt                                                             *time.Time
}

func (attempt DatasetPublicationPartitionAttempt) Validate() error {
	for name, value := range map[string]string{
		"dataset version ID": attempt.DatasetVersionID,
		"partition ID":       attempt.PartitionID,
	} {
		if err := validateDatasetIdentifier(name, value); err != nil {
			return err
		}
	}
	for name, value := range map[string]string{
		"partition input fingerprint": attempt.InputFingerprint,
		"partition plan SHA-256":      attempt.PlanSHA256,
		"partition receipt SHA-256":   attempt.ReceiptSHA256,
	} {
		if value != "" && !datasetDigestPattern.MatchString(value) {
			return fmt.Errorf("%s is invalid", name)
		}
	}
	state := attempt.State
	if state == "" {
		state = DatasetPartitionPending
	}
	if !knownDatasetPartitionState(state) || attempt.Attempt < 0 {
		return fmt.Errorf("partition lifecycle is invalid")
	}
	if state == DatasetPartitionCompleted && attempt.ReceiptSHA256 == "" {
		return fmt.Errorf("completed partition requires a receipt")
	}
	if state == DatasetPartitionLeased {
		if err := validateDatasetIdentifier("partition lease owner", attempt.LeaseOwner); err != nil || attempt.LeaseExpiresAt == nil {
			return fmt.Errorf("partition lease is invalid")
		}
	} else if attempt.LeaseOwner != "" || attempt.LeaseExpiresAt != nil {
		return fmt.Errorf("partition lease is invalid")
	}
	return nil
}

func (attempt DatasetPublicationPartitionAttempt) Lease(owner string, expiresAt time.Time) (DatasetPublicationPartitionAttempt, error) {
	if err := attempt.Validate(); err != nil {
		return DatasetPublicationPartitionAttempt{}, err
	}
	if attempt.State != "" && attempt.State != DatasetPartitionPending && attempt.State != DatasetPartitionFailed {
		return DatasetPublicationPartitionAttempt{}, fmt.Errorf("dataset partition cannot be leased from %q", attempt.State)
	}
	result := attempt
	result.State = DatasetPartitionLeased
	result.Attempt++
	result.LeaseOwner = owner
	result.LeaseExpiresAt = &expiresAt
	if err := result.Validate(); err != nil {
		return DatasetPublicationPartitionAttempt{}, err
	}
	return result, nil
}

func (attempt DatasetPublicationPartitionAttempt) Complete(receiptSHA256 string) (DatasetPublicationPartitionAttempt, error) {
	if err := attempt.Validate(); err != nil {
		return DatasetPublicationPartitionAttempt{}, err
	}
	if attempt.State != DatasetPartitionLeased {
		return DatasetPublicationPartitionAttempt{}, fmt.Errorf("dataset partition cannot complete from %q", attempt.State)
	}
	result := attempt
	result.State = DatasetPartitionCompleted
	result.ReceiptSHA256 = receiptSHA256
	result.LeaseOwner = ""
	result.LeaseExpiresAt = nil
	if err := result.Validate(); err != nil {
		return DatasetPublicationPartitionAttempt{}, err
	}
	return result, nil
}

// DatasetPublicationExecutionMode is fixed when a version is requested. It
// prevents enabling a new publisher implementation from changing the meaning
// of an in-flight legacy version.
type DatasetPublicationExecutionMode string

const (
	DatasetPublicationExecutionLegacy      DatasetPublicationExecutionMode = "legacy"
	DatasetPublicationExecutionDistributed DatasetPublicationExecutionMode = "distributed"
)

func (mode DatasetPublicationExecutionMode) Valid() bool {
	return mode == "" || mode == DatasetPublicationExecutionLegacy || mode == DatasetPublicationExecutionDistributed
}

func (mode DatasetPublicationExecutionMode) Normalized() DatasetPublicationExecutionMode {
	if mode == "" {
		return DatasetPublicationExecutionLegacy
	}
	return mode
}

type DatasetPublicationRun struct {
	ID, DatasetID, DatasetVersionID                            string
	ExecutionMode                                              DatasetPublicationExecutionMode
	State                                                      DatasetVersionState
	TotalPartitions, CompletedPartitions, FailedPartitions     int64
	SourceObjectCount, ProcessedObjectCount, FailedObjectCount int64
}

// DatasetPublicationWork is the control-plane-only join used by the elected
// publication manager. It keeps the user-facing dataset, immutable version,
// and current publication attempt bound together across reconciler restarts.
type DatasetPublicationWork struct {
	Dataset Dataset
	Version DatasetVersion
	Run     DatasetPublicationRun
}

func (work DatasetPublicationWork) Validate() error {
	if err := work.Dataset.Validate(); err != nil {
		return err
	}
	if err := work.Version.Validate(); err != nil {
		return err
	}
	if err := work.Run.Validate(); err != nil {
		return err
	}
	if work.Version.DatasetID != work.Dataset.ID || work.Run.DatasetID != work.Dataset.ID || work.Run.DatasetVersionID != work.Version.ID || work.Run.State != work.Version.State {
		return fmt.Errorf("dataset publication work identity or state is inconsistent")
	}
	return nil
}

func (run DatasetPublicationRun) Validate() error {
	for name, value := range map[string]string{"publication ID": run.ID, "dataset ID": run.DatasetID, "dataset version ID": run.DatasetVersionID} {
		if err := validateDatasetIdentifier(name, value); err != nil {
			return err
		}
	}
	if !knownDatasetVersionState(run.State) {
		return fmt.Errorf("unsupported publication state %q", run.State)
	}
	if !run.ExecutionMode.Valid() {
		return fmt.Errorf("unsupported publication execution mode %q", run.ExecutionMode)
	}
	if run.TotalPartitions < 0 || run.CompletedPartitions < 0 || run.FailedPartitions < 0 || run.CompletedPartitions > run.TotalPartitions || run.FailedPartitions > run.TotalPartitions-run.CompletedPartitions {
		return fmt.Errorf("publication partition progress is invalid")
	}
	return validateProgressCounts(run.SourceObjectCount, run.ProcessedObjectCount, run.FailedObjectCount, 0, 0)
}

type DatasetCacheObservation struct {
	ID, DatasetVersionID, TrainingJobID, NodeName  string
	CacheHitCount, CacheMissCount                  int64
	CacheHitBytes, CacheMissBytes                  int64
	CachedBytes, EvictedBytes                      int64
	ChecksumFailureCount, PrefetchWaitMilliseconds int64
}

func (observation DatasetCacheObservation) Validate() error {
	for name, value := range map[string]string{"cache observation ID": observation.ID, "dataset version ID": observation.DatasetVersionID, "training job ID": observation.TrainingJobID, "node name": observation.NodeName} {
		if err := validateDatasetIdentifier(name, value); err != nil {
			return err
		}
	}
	for _, value := range []int64{observation.CacheHitCount, observation.CacheMissCount, observation.CacheHitBytes, observation.CacheMissBytes, observation.CachedBytes, observation.EvictedBytes, observation.ChecksumFailureCount, observation.PrefetchWaitMilliseconds} {
		if value < 0 {
			return fmt.Errorf("cache observation counters must be nonnegative")
		}
	}
	return nil
}

func CanViewDataset(dataset Dataset, tenantID string, superAdmin bool) bool {
	if superAdmin {
		return true
	}
	if strings.TrimSpace(tenantID) == "" {
		return false
	}
	return dataset.Visibility == DatasetVisibilityPublic || dataset.Visibility == DatasetVisibilityTeam && dataset.OwnerTenantID == tenantID
}

func CanManageDataset(dataset Dataset, tenantID string, superAdmin bool) bool {
	if superAdmin {
		return true
	}
	return dataset.Visibility == DatasetVisibilityTeam && strings.TrimSpace(tenantID) != "" && dataset.OwnerTenantID == tenantID
}

func validateDatasetIdentifier(name, value string) error {
	if len(value) == 0 || len(value) > DatasetIdentifierMaxBytes || value == "." || value == ".." || strings.TrimSpace(value) != value || !datasetIdentifierPattern.MatchString(value) {
		return fmt.Errorf("%s must be a safe bounded identifier", name)
	}
	return nil
}

func validateDatasetRelativePath(name, value string) error {
	if value == "" || len(value) > DatasetPathMaxBytes || strings.TrimSpace(value) != value || strings.IndexFunc(value, unicode.IsControl) >= 0 || strings.Contains(value, `\`) || path.IsAbs(value) || path.Clean(value) != value || value == "." {
		return fmt.Errorf("%s must be a clean relative path", name)
	}
	unescaped, err := url.PathUnescape(value)
	if err != nil || unescaped != value {
		return fmt.Errorf("%s must not contain URL escapes", name)
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "" || parsed.Host != "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("%s must not be a URI", name)
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return fmt.Errorf("%s contains an invalid segment", name)
		}
	}
	return nil
}

func knownDatasetVersionState(state DatasetVersionState) bool {
	switch state {
	case DatasetVersionDiscovering, DatasetVersionStabilizing, DatasetVersionValidating, DatasetVersionPacking, DatasetVersionReady, DatasetVersionFailed, DatasetVersionDeprecated, DatasetVersionRetired:
		return true
	default:
		return false
	}
}

func knownDatasetPartitionState(state DatasetPartitionState) bool {
	switch state {
	case DatasetPartitionPending, DatasetPartitionLeased, DatasetPartitionCompleted, DatasetPartitionFailed:
		return true
	default:
		return false
	}
}

func validateProgressCounts(total, processed, failed, logicalBytes, packedBytes int64) error {
	if total < 0 || processed < 0 || failed < 0 || logicalBytes < 0 || packedBytes < 0 || processed > total || failed > total-processed {
		return fmt.Errorf("progress counters are invalid")
	}
	return nil
}
