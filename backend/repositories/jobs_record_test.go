package repositories

import (
	"encoding/json"
	"testing"

	"ray-train-platform-backend/domain"
)

// The training_jobs table stores cleanup_json as jsonb. SQLite accepts any
// string there, so this asserts the mapping directly: an empty string is not
// valid JSON and PostgreSQL rejects the whole INSERT with SQLSTATE 22P02,
// which makes every job submission fail in a real deployment.
func TestNewJobRecordAlwaysWritesValidJSONColumns(t *testing.T) {
	job := testJob()
	job.Spec.CleanupPolicy = domain.CleanupPolicy{}

	record, err := newJobRecord(&job)
	if err != nil {
		t.Fatalf("build job record: %v", err)
	}

	for name, value := range map[string]string{"cleanup_json": record.CleanupJSON, "spec_json": record.SpecJSON} {
		if value == "" {
			t.Fatalf("%s must never be empty, jsonb rejects it", name)
		}
		var decoded any
		if err := json.Unmarshal([]byte(value), &decoded); err != nil {
			t.Fatalf("%s must be valid JSON, got %q: %v", name, value, err)
		}
	}
}

func TestNewJobRecordPreservesCleanupPolicy(t *testing.T) {
	job := testJob()
	job.Spec.CleanupPolicy = domain.CleanupPolicy{SuccessTTLSeconds: 300, FailureTTLSeconds: 900}

	record, err := newJobRecord(&job)
	if err != nil {
		t.Fatalf("build job record: %v", err)
	}

	var policy domain.CleanupPolicy
	if err := json.Unmarshal([]byte(record.CleanupJSON), &policy); err != nil {
		t.Fatalf("decode cleanup policy: %v", err)
	}
	if policy.SuccessTTLSeconds != 300 || policy.FailureTTLSeconds != 900 {
		t.Fatalf("cleanup policy not round-tripped: %+v", policy)
	}
}

func TestJobRecordRoundTripsManagedRuntimeMetadata(t *testing.T) {
	job := testJob()
	job.Spec.TrainingEngine = domain.TrainingEngineRayTrain
	job.Spec.RayVersion = domain.RayVersionProduction
	job.Spec.ParentJobID = "job-0123456789abcdef01234567"
	job.ClusterAttempt = 2
	job.WorkerRestartCount = 3
	job.ResumeCheckpointID = "checkpoint-7"

	record, err := newJobRecord(&job)
	if err != nil {
		t.Fatal(err)
	}
	got, err := record.toDomain()
	if err != nil {
		t.Fatal(err)
	}
	if got.Spec.TrainingEngine != domain.TrainingEngineRayTrain || got.Spec.RayVersion != domain.RayVersionProduction || got.Spec.ParentJobID != job.Spec.ParentJobID || got.ClusterAttempt != 2 || got.WorkerRestartCount != 3 || got.ResumeCheckpointID != "checkpoint-7" {
		t.Fatalf("runtime metadata lost: %+v", got)
	}
}

func TestJobRecordRoundTripsDatasetProvenanceColumns(t *testing.T) {
	job := testJob()
	job.Spec.TrainingEngine = domain.TrainingEngineRayTrain
	job.Spec.RayVersion = domain.RayVersionCanary
	job.Spec.DataMode = domain.DataModeStreaming
	job.Spec.DatasetRef = domain.DatasetReference{Dataset: "dataset-labeled-full", Version: "labeled-full-20260830.2+sha256-12ab34cd"}
	job.Spec.CachePolicy = domain.DatasetCachePolicyBounded
	job.DatasetProvenance = domain.DatasetProvenance{
		DatasetID:        "dataset-labeled-full",
		DatasetVersionID: "labeled-full-20260830.2+sha256-12ab34cd",
		ManifestSHA256:   "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		DataMode:         domain.DataModeStreaming,
		CachePolicy:      domain.DatasetCachePolicyBounded,
	}
	job.Spec.DatasetRef.Sites, _ = domain.NewDatasetSites([]string{"cnfzhjyg"})
	job.DatasetProvenance.Sites = job.Spec.DatasetRef.Sites

	record, err := newJobRecord(&job)
	if err != nil {
		t.Fatal(err)
	}
	if record.DatasetID == nil || *record.DatasetID != job.DatasetProvenance.DatasetID {
		t.Fatalf("dataset_id was not mapped: %+v", record)
	}
	if record.DatasetVersionID == nil || *record.DatasetVersionID != job.DatasetProvenance.DatasetVersionID {
		t.Fatalf("dataset_version_id was not mapped: %+v", record)
	}
	if record.DatasetManifestDigest == nil || *record.DatasetManifestDigest != job.DatasetProvenance.ManifestSHA256 {
		t.Fatalf("dataset_manifest_digest was not mapped: %+v", record)
	}
	if record.DatasetDataMode == nil || *record.DatasetDataMode != string(domain.DataModeStreaming) {
		t.Fatalf("dataset_data_mode was not mapped: %+v", record)
	}
	if record.DatasetCachePolicy == nil || *record.DatasetCachePolicy != string(domain.DatasetCachePolicyBounded) {
		t.Fatalf("dataset_cache_policy was not mapped: %+v", record)
	}

	got, err := record.toDomain()
	if err != nil {
		t.Fatal(err)
	}
	if got.DatasetProvenance != job.DatasetProvenance {
		t.Fatalf("dataset provenance did not round-trip: got %+v want %+v", got.DatasetProvenance, job.DatasetProvenance)
	}
}

func TestNewJobRecordRejectsMutableOrMismatchedDatasetProvenance(t *testing.T) {
	base := testJob()
	base.Spec.TrainingEngine = domain.TrainingEngineRayTrain
	base.Spec.RayVersion = domain.RayVersionCanary
	base.Spec.DataMode = domain.DataModeStreaming
	base.Spec.DatasetRef = domain.DatasetReference{Dataset: "dataset-labeled-full", Version: "version-pinned"}
	base.Spec.CachePolicy = domain.DatasetCachePolicyAuto
	base.DatasetProvenance = domain.DatasetProvenance{
		DatasetID: "dataset-labeled-full", DatasetVersionID: "version-pinned",
		ManifestSHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		DataMode:       domain.DataModeStreaming, CachePolicy: domain.DatasetCachePolicyAuto,
	}
	tests := []struct {
		name   string
		mutate func(*domain.TrainingJob)
	}{
		{name: "latest selector remained", mutate: func(job *domain.TrainingJob) { job.Spec.DatasetRef.Version = "latest" }},
		{name: "version mismatch", mutate: func(job *domain.TrainingJob) { job.Spec.DatasetRef.Version = "version-other" }},
		{name: "dataset mismatch", mutate: func(job *domain.TrainingJob) { job.Spec.DatasetRef.Dataset = "dataset-other" }},
		{name: "cache policy mismatch", mutate: func(job *domain.TrainingJob) { job.Spec.CachePolicy = domain.DatasetCachePolicyOff }},
		{name: "site mismatch", mutate: func(job *domain.TrainingJob) {
			job.Spec.DatasetRef.Sites, _ = domain.NewDatasetSites([]string{"site-a"})
		}},
		{name: "missing provenance", mutate: func(job *domain.TrainingJob) { job.DatasetProvenance = domain.DatasetProvenance{} }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			job := base
			test.mutate(&job)
			if _, err := newJobRecord(&job); err == nil {
				t.Fatal("expected immutable dataset snapshot validation failure")
			}
		})
	}
}

func TestJobRecordKeepsLegacyDatasetProvenanceNull(t *testing.T) {
	job := testJob()

	record, err := newJobRecord(&job)
	if err != nil {
		t.Fatal(err)
	}
	if record.DatasetID != nil || record.DatasetVersionID != nil || record.DatasetManifestDigest != nil || record.DatasetDataMode != nil || record.DatasetCachePolicy != nil {
		t.Fatalf("legacy job must keep dataset provenance columns NULL: %+v", record)
	}

	got, err := record.toDomain()
	if err != nil {
		t.Fatal(err)
	}
	if got.DatasetProvenance != (domain.DatasetProvenance{}) {
		t.Fatalf("legacy job must read as empty dataset provenance: %+v", got.DatasetProvenance)
	}
}

type datasetConstraintTestError struct {
	state   string
	message string
}

func (err datasetConstraintTestError) Error() string    { return err.message }
func (err datasetConstraintTestError) SQLState() string { return err.state }

func TestDatasetSnapshotConstraintErrorsAreClassifiedNarrowly(t *testing.T) {
	for _, test := range []struct {
		err  error
		want bool
	}{
		{err: datasetConstraintTestError{state: "P0001", message: "training jobs can only pin READY dataset versions"}, want: true},
		{err: datasetConstraintTestError{state: "P0001", message: "training job dataset version does not exist"}, want: true},
		{err: datasetConstraintTestError{state: "23514", message: "training jobs can only pin READY dataset versions"}, want: true},
		{err: datasetConstraintTestError{state: "23514", message: "unrelated check constraint"}, want: false},
		{err: datasetConstraintTestError{state: "22000", message: "training job dataset version does not exist"}, want: false},
	} {
		if got := isDatasetSnapshotConstraintError(test.err); got != test.want {
			t.Fatalf("classification=%t want=%t for %v", got, test.want, test.err)
		}
	}
}

func TestJobRecordNormalizedRuntimeMetadataOverridesSpecJSON(t *testing.T) {
	job := testJob()
	specJSON, err := json.Marshal(job.Spec)
	if err != nil {
		t.Fatal(err)
	}
	record := JobRecord{
		SpecJSON:           string(specJSON),
		TrainingEngine:     string(domain.TrainingEngineRayTrain),
		RayVersion:         domain.RayVersionCanary,
		ClusterAttempt:     4,
		WorkerRestartCount: 5,
		ResumeCheckpointID: "checkpoint-normalized",
		ParentJobID:        "job-89abcdef0123456701234567",
	}

	got, err := record.toDomain()
	if err != nil {
		t.Fatal(err)
	}
	if got.Spec.TrainingEngine != domain.TrainingEngineRayTrain || got.Spec.RayVersion != domain.RayVersionCanary || got.Spec.ParentJobID != record.ParentJobID {
		t.Fatalf("normalized spec metadata did not override spec_json: %+v", got.Spec)
	}
	if got.ClusterAttempt != 4 || got.WorkerRestartCount != 5 || got.ResumeCheckpointID != "checkpoint-normalized" {
		t.Fatalf("normalized runtime counters lost: %+v", got)
	}
}

func TestJobRecordResolvesLegacyRuntimeMetadataDefaults(t *testing.T) {
	job := testJob()
	specJSON, err := json.Marshal(job.Spec)
	if err != nil {
		t.Fatal(err)
	}

	got, err := (JobRecord{SpecJSON: string(specJSON)}).toDomain()
	if err != nil {
		t.Fatal(err)
	}
	if got.Spec.TrainingEngine != domain.TrainingEngineRayDDP || got.Spec.RayVersion != domain.RayVersionLegacy || got.ClusterAttempt != 1 {
		t.Fatalf("legacy runtime metadata defaults not resolved: %+v", got)
	}
}

func TestJobRecordDefensivelyNormalizesInvalidRuntimeCounters(t *testing.T) {
	job := testJob()
	specJSON, err := json.Marshal(job.Spec)
	if err != nil {
		t.Fatal(err)
	}

	got, err := (JobRecord{
		SpecJSON:           string(specJSON),
		ClusterAttempt:     -2,
		WorkerRestartCount: -3,
	}).toDomain()
	if err != nil {
		t.Fatal(err)
	}
	if got.ClusterAttempt != 1 || got.WorkerRestartCount != 0 {
		t.Fatalf("invalid runtime counters not normalized: %+v", got)
	}
}
