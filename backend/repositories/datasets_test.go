package repositories

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"ray-train-platform-backend/domain"
)

func datasetRepository(t *testing.T) *GormRepository {
	t.Helper()
	repository := testRepository(t)
	if err := repository.db.AutoMigrate(
		&DatasetRecord{},
		&DatasetVersionRecord{},
		&DatasetPartitionRecord{},
		&DatasetPublicationPartitionAttemptRecord{},
		&DatasetPublicationRunRecord{},
		&DatasetVersionShardRecord{},
		&DatasetCacheObservationRecord{},
	); err != nil {
		t.Fatalf("migrate dataset records: %v", err)
	}
	return repository
}

func publicDataset(id, slug string) domain.Dataset {
	return domain.Dataset{
		ID: id, Slug: slug, Name: "Dataset " + slug,
		SourceSpace: domain.DataSpacePublic, SourceRelativePath: "labeled/" + slug,
		Visibility: domain.DatasetVisibilityPublic, SchemaVersion: "schema-v1",
	}
}

func teamDataset(id, slug, tenantID string) domain.Dataset {
	return domain.Dataset{
		ID: id, Slug: slug, Name: "Dataset " + slug,
		SourceSpace: domain.DataSpaceTeamShared, SourceRelativePath: "datasets/" + slug,
		OwnerTenantID: tenantID, Visibility: domain.DatasetVisibilityTeam, SchemaVersion: "schema-v1",
	}
}

func discoveringVersion(datasetID, id, version string) domain.DatasetVersion {
	return domain.DatasetVersion{
		ID: id, DatasetID: datasetID, Version: version,
		State: domain.DatasetVersionDiscovering, SchemaVersion: "schema-v1",
	}
}

func readyVersion(datasetID, id, version string) domain.DatasetVersion {
	result := discoveringVersion(datasetID, id, version)
	result.State = domain.DatasetVersionReady
	result.ManifestSHA256 = strings.Repeat("a", 64)
	result.ManifestObjectKey = "ray-train/platform/datasets/" + datasetID + "/manifests/" + id + ".parquet"
	result.TrainSamples = 100
	result.SourceObjectCount = 10
	result.LogicalBytes = 1_000
	result.PackedBytes = 900
	return result
}

func datasetVersionRecordForTest(version domain.DatasetVersion, createdAt time.Time) DatasetVersionRecord {
	return DatasetVersionRecord{
		ID: version.ID, DatasetID: version.DatasetID, Version: version.Version,
		State: string(version.State), ManifestSHA256: optionalID(version.ManifestSHA256),
		ManifestObjectKey: optionalID(version.ManifestObjectKey), SchemaVersion: version.SchemaVersion,
		TrainSamples: version.TrainSamples, ValSamples: version.ValSamples, TestSamples: version.TestSamples,
		SourceObjectCount: version.SourceObjectCount, LogicalBytes: version.LogicalBytes, PackedBytes: version.PackedBytes,
		CreatedAt: createdAt, UpdatedAt: createdAt,
	}
}

func mustCreateDataset(t *testing.T, repository *GormRepository, dataset domain.Dataset) {
	t.Helper()
	if err := repository.CreateDataset(context.Background(), dataset); err != nil {
		t.Fatalf("create dataset %s: %v", dataset.ID, err)
	}
}

func mustInsertDatasetVersion(t *testing.T, repository *GormRepository, version domain.DatasetVersion, createdAt time.Time) {
	t.Helper()
	record := datasetVersionRecordForTest(version, createdAt)
	if err := repository.db.Create(&record).Error; err != nil {
		t.Fatalf("insert dataset version %s: %v", version.ID, err)
	}
}

func datasetIDs(datasets []domain.Dataset) []string {
	ids := make([]string, 0, len(datasets))
	for _, dataset := range datasets {
		ids = append(ids, dataset.ID)
	}
	sort.Strings(ids)
	return ids
}

func versionIDs(versions []domain.DatasetVersion) []string {
	ids := make([]string, 0, len(versions))
	for _, version := range versions {
		ids = append(ids, version.ID)
	}
	sort.Strings(ids)
	return ids
}

func TestDatasetRecordModelsMatchMigrationContracts(t *testing.T) {
	if got := (DatasetRecord{}).TableName(); got != "datasets" {
		t.Fatalf("DatasetRecord table = %q", got)
	}
	if got := (DatasetVersionRecord{}).TableName(); got != "dataset_versions" {
		t.Fatalf("DatasetVersionRecord table = %q", got)
	}
	if got := (DatasetPartitionRecord{}).TableName(); got != "dataset_partitions" {
		t.Fatalf("DatasetPartitionRecord table = %q", got)
	}
	if got := (DatasetPublicationPartitionAttemptRecord{}).TableName(); got != "dataset_publication_partition_attempts" {
		t.Fatalf("DatasetPublicationPartitionAttemptRecord table = %q", got)
	}
	if got := (DatasetPublicationRunRecord{}).TableName(); got != "dataset_publication_runs" {
		t.Fatalf("DatasetPublicationRunRecord table = %q", got)
	}
	if got := (DatasetVersionShardRecord{}).TableName(); got != "dataset_version_shards" {
		t.Fatalf("DatasetVersionShardRecord table = %q", got)
	}
	if got := (DatasetCacheObservationRecord{}).TableName(); got != "dataset_cache_observations" {
		t.Fatalf("DatasetCacheObservationRecord table = %q", got)
	}

	datasetType := reflect.TypeOf(DatasetRecord{})
	versionType := reflect.TypeOf(DatasetVersionRecord{})
	for recordType, fields := range map[reflect.Type][]string{
		datasetType: {"OwnerTenantID"},
		versionType: {"ManifestSHA256", "ManifestObjectKey"},
	} {
		for _, name := range fields {
			field, ok := recordType.FieldByName(name)
			if !ok || field.Type != reflect.TypeOf((*string)(nil)) {
				t.Errorf("%s.%s must be *string, got %v", recordType.Name(), name, field.Type)
			}
		}
	}
	if _, ok := reflect.TypeOf(DatasetVersionShardRecord{}).FieldByName("DatasetID"); !ok {
		t.Fatal("DatasetVersionShardRecord must include DatasetID")
	}
}

func TestCreateDatasetValidatesPersistsAndDoesNotMutateInput(t *testing.T) {
	repository := datasetRepository(t)
	ctx := context.Background()

	invalid := publicDataset("dataset-invalid", "Invalid Slug")
	if err := repository.CreateDataset(ctx, invalid); err == nil {
		t.Fatal("invalid dataset must be rejected")
	}
	var count int64
	if err := repository.db.Model(&DatasetRecord{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("invalid dataset was persisted: count=%d err=%v", count, err)
	}

	dataset := teamDataset("dataset-team-a", "team-a-data", "team-a")
	dataset.Description = "immutable caller value"
	before := dataset
	if err := repository.CreateDataset(ctx, dataset); err != nil {
		t.Fatalf("create dataset: %v", err)
	}
	if !reflect.DeepEqual(dataset, before) {
		t.Fatalf("CreateDataset mutated its input: got=%+v want=%+v", dataset, before)
	}

	var stored DatasetRecord
	if err := repository.db.Where("id = ?", dataset.ID).First(&stored).Error; err != nil {
		t.Fatalf("load stored dataset: %v", err)
	}
	if stored.OwnerTenantID == nil || *stored.OwnerTenantID != "team-a" || stored.CreatedAt.IsZero() || stored.UpdatedAt.IsZero() {
		t.Fatalf("stored dataset does not match migration semantics: %+v", stored)
	}
}

func TestGetAndListDatasetsEnforceVisibility(t *testing.T) {
	repository := datasetRepository(t)
	ctx := context.Background()
	for _, dataset := range []domain.Dataset{
		publicDataset("dataset-public", "public-data"),
		teamDataset("dataset-team-a", "team-a-data", "team-a"),
		teamDataset("dataset-team-b", "team-b-data", "team-b"),
	} {
		mustCreateDataset(t, repository, dataset)
	}

	for _, id := range []string{"dataset-public", "dataset-team-a"} {
		if _, err := repository.GetDataset(ctx, "team-a", false, id); err != nil {
			t.Fatalf("team-a must see %s: %v", id, err)
		}
	}
	if _, err := repository.GetDataset(ctx, "team-a", false, "dataset-team-b"); !errors.Is(err, ErrDatasetNotFound) {
		t.Fatalf("cross-tenant TEAM lookup error = %v, want ErrDatasetNotFound", err)
	}
	if _, err := repository.GetDataset(ctx, "", false, "dataset-public"); !errors.Is(err, ErrDatasetNotFound) {
		t.Fatalf("anonymous public lookup error = %v, want ErrDatasetNotFound", err)
	}

	visible, err := repository.ListDatasets(ctx, "team-a", false)
	if err != nil {
		t.Fatalf("list team-a datasets: %v", err)
	}
	if got, want := datasetIDs(visible), []string{"dataset-public", "dataset-team-a"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("team-a datasets = %v, want %v", got, want)
	}
	anonymous, err := repository.ListDatasets(ctx, "", false)
	if err != nil || len(anonymous) != 0 {
		t.Fatalf("anonymous list = %+v err=%v, want empty", anonymous, err)
	}
	all, err := repository.ListDatasets(ctx, "", true)
	if err != nil {
		t.Fatalf("super-admin list: %v", err)
	}
	if got, want := datasetIDs(all), []string{"dataset-public", "dataset-team-a", "dataset-team-b"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("super-admin datasets = %v, want %v", got, want)
	}
	if _, err := repository.GetDataset(ctx, "", true, "dataset-team-b"); err != nil {
		t.Fatalf("super-admin must see all datasets: %v", err)
	}
}

func TestCreateDatasetVersionIsStrictlyIdempotentAndStartsDiscovering(t *testing.T) {
	repository := datasetRepository(t)
	ctx := context.Background()
	mustCreateDataset(t, repository, publicDataset("dataset-public", "public-data"))

	version := discoveringVersion("dataset-public", "version-1", "2026.08.30-1")
	version.TrainSamples = 10
	before := version
	if err := repository.CreateDatasetVersion(ctx, version); err != nil {
		t.Fatalf("create version: %v", err)
	}
	if !reflect.DeepEqual(version, before) {
		t.Fatalf("CreateDatasetVersion mutated input: got=%+v want=%+v", version, before)
	}
	if err := repository.CreateDatasetVersion(ctx, version); err != nil {
		t.Fatalf("identical retry must be idempotent: %v", err)
	}

	var stored DatasetVersionRecord
	if err := repository.db.Where("id = ?", version.ID).First(&stored).Error; err != nil {
		t.Fatalf("load version: %v", err)
	}
	if stored.State != string(domain.DatasetVersionDiscovering) || stored.CreatedAt.IsZero() || stored.UpdatedAt.IsZero() {
		t.Fatalf("new version must start in DISCOVERING with timestamps: %+v", stored)
	}

	differentPayload := version
	differentPayload.TrainSamples++
	if err := repository.CreateDatasetVersion(ctx, differentPayload); !errors.Is(err, ErrDatasetVersionConflict) {
		t.Fatalf("mismatched payload error = %v, want ErrDatasetVersionConflict", err)
	}
	differentID := version
	differentID.ID = "version-2"
	if err := repository.CreateDatasetVersion(ctx, differentID); !errors.Is(err, ErrDatasetVersionConflict) {
		t.Fatalf("same dataset/version with different ID error = %v, want conflict", err)
	}
	notDiscovering := readyVersion("dataset-public", "version-ready", "2026.08.30-ready")
	if err := repository.CreateDatasetVersion(ctx, notDiscovering); !errors.Is(err, ErrDatasetVersionConflict) {
		t.Fatalf("non-DISCOVERING create error = %v, want conflict", err)
	}
}

func TestGetAndListDatasetVersionsDoNotLeakHiddenDatasets(t *testing.T) {
	repository := datasetRepository(t)
	ctx := context.Background()
	mustCreateDataset(t, repository, publicDataset("dataset-public", "public-data"))
	mustCreateDataset(t, repository, teamDataset("dataset-team-a", "team-a-data", "team-a"))
	mustCreateDataset(t, repository, teamDataset("dataset-team-b", "team-b-data", "team-b"))
	for _, version := range []domain.DatasetVersion{
		discoveringVersion("dataset-public", "version-public", "v1"),
		discoveringVersion("dataset-team-a", "version-team-a", "v1"),
		discoveringVersion("dataset-team-b", "version-team-b", "v1"),
	} {
		if err := repository.CreateDatasetVersion(ctx, version); err != nil {
			t.Fatalf("create version %s: %v", version.ID, err)
		}
	}

	if _, err := repository.GetDatasetVersion(ctx, "team-a", false, "dataset-team-b", "version-team-b"); !errors.Is(err, ErrDatasetVersionNotFound) {
		t.Fatalf("hidden version lookup error = %v, want ErrDatasetVersionNotFound", err)
	}
	if _, err := repository.ListDatasetVersions(ctx, "team-a", false, "dataset-team-b"); !errors.Is(err, ErrDatasetNotFound) {
		t.Fatalf("hidden dataset version list error = %v, want ErrDatasetNotFound", err)
	}
	versions, err := repository.ListDatasetVersions(ctx, "team-a", false, "dataset-team-a")
	if err != nil || !reflect.DeepEqual(versionIDs(versions), []string{"version-team-a"}) {
		t.Fatalf("team-a versions = %+v err=%v", versions, err)
	}
	versions, err = repository.ListDatasetVersions(ctx, "", true, "dataset-team-b")
	if err != nil || !reflect.DeepEqual(versionIDs(versions), []string{"version-team-b"}) {
		t.Fatalf("super-admin versions = %+v err=%v", versions, err)
	}
}

func TestResolveReadyDatasetVersionHandlesExplicitLatestAndVisibility(t *testing.T) {
	repository := datasetRepository(t)
	ctx := context.Background()
	mustCreateDataset(t, repository, teamDataset("dataset-team-a", "team-a-data", "team-a"))
	mustCreateDataset(t, repository, teamDataset("dataset-team-b", "team-b-data", "team-b"))

	baseTime := time.Date(2026, 8, 30, 10, 0, 0, 0, time.UTC)
	readyA := readyVersion("dataset-team-a", "ready-a", "v1")
	readyB := readyVersion("dataset-team-a", "ready-b", "v2")
	packing := discoveringVersion("dataset-team-a", "packing-newer", "v3")
	packing.State = domain.DatasetVersionPacking
	deprecated := readyVersion("dataset-team-a", "deprecated-newest", "v4")
	deprecated.State = domain.DatasetVersionDeprecated
	failed := discoveringVersion("dataset-team-a", "failed-newest", "v5")
	failed.State = domain.DatasetVersionFailed
	retired := readyVersion("dataset-team-a", "retired-newest", "v6")
	retired.State = domain.DatasetVersionRetired
	for _, item := range []struct {
		version   domain.DatasetVersion
		createdAt time.Time
	}{
		{readyA, baseTime}, {readyB, baseTime},
		{packing, baseTime.Add(time.Hour)}, {deprecated, baseTime.Add(2 * time.Hour)},
		{failed, baseTime.Add(3 * time.Hour)}, {retired, baseTime.Add(4 * time.Hour)},
	} {
		mustInsertDatasetVersion(t, repository, item.version, item.createdAt)
	}
	hiddenReady := readyVersion("dataset-team-b", "hidden-ready", "v1")
	mustInsertDatasetVersion(t, repository, hiddenReady, baseTime.Add(5*time.Hour))

	latest, err := repository.ResolveReadyDatasetVersion(ctx, "team-a", false, "dataset-team-a", domain.DatasetVersionSelector{Latest: true})
	if err != nil || latest.ID != "ready-b" {
		t.Fatalf("latest READY = %+v err=%v, want ready-b", latest, err)
	}
	explicit, err := repository.ResolveReadyDatasetVersion(ctx, "team-a", false, "dataset-team-a", domain.DatasetVersionSelector{VersionID: "ready-a"})
	if err != nil || explicit.ID != "ready-a" {
		t.Fatalf("explicit READY = %+v err=%v", explicit, err)
	}
	for _, versionID := range []string{"packing-newer", "deprecated-newest", "failed-newest", "retired-newest"} {
		if _, err := repository.ResolveReadyDatasetVersion(ctx, "team-a", false, "dataset-team-a", domain.DatasetVersionSelector{VersionID: versionID}); !errors.Is(err, ErrDatasetVersionNotReady) {
			t.Errorf("resolve %s error = %v, want ErrDatasetVersionNotReady", versionID, err)
		}
	}
	if _, err := repository.ResolveReadyDatasetVersion(ctx, "team-a", false, "dataset-team-b", domain.DatasetVersionSelector{VersionID: "hidden-ready"}); !errors.Is(err, ErrDatasetVersionNotReady) {
		t.Fatalf("hidden READY error = %v, want non-leaking ErrDatasetVersionNotReady", err)
	}
	if _, err := repository.ResolveReadyDatasetVersion(ctx, "", false, "dataset-team-a", domain.DatasetVersionSelector{Latest: true}); !errors.Is(err, ErrDatasetVersionNotReady) {
		t.Fatalf("anonymous latest error = %v, want ErrDatasetVersionNotReady", err)
	}
}

func TestUpdateDatasetVersionDraftOnlyChangesPayloadInPublicationStates(t *testing.T) {
	repository := datasetRepository(t)
	ctx := context.Background()
	mustCreateDataset(t, repository, publicDataset("dataset-public", "public-data"))
	version := discoveringVersion("dataset-public", "version-draft", "v1")
	if err := repository.CreateDatasetVersion(ctx, version); err != nil {
		t.Fatalf("create draft: %v", err)
	}

	draft := version
	draft.ManifestSHA256 = strings.Repeat("b", 64)
	draft.ManifestObjectKey = "ray-train/platform/datasets/dataset-public/manifests/version-draft.parquet"
	draft.SchemaVersion = "schema-v2"
	draft.TrainSamples = 120
	draft.ValSamples = 20
	draft.TestSamples = 10
	draft.SourceObjectCount = 18
	draft.LogicalBytes = 2_000
	draft.PackedBytes = 1_700
	before := draft
	updated, err := repository.UpdateDatasetVersionDraft(ctx, draft)
	if err != nil {
		t.Fatalf("update draft: %v", err)
	}
	if !reflect.DeepEqual(draft, before) {
		t.Fatalf("UpdateDatasetVersionDraft mutated input: got=%+v want=%+v", draft, before)
	}
	if !reflect.DeepEqual(updated, draft) {
		t.Fatalf("updated draft = %+v, want %+v", updated, draft)
	}
	stored, err := repository.GetDatasetVersion(ctx, "team-a", false, "dataset-public", "version-draft")
	if err != nil {
		t.Fatalf("read updated draft: %v", err)
	}
	if stored.ID != version.ID || stored.DatasetID != version.DatasetID || stored.Version != version.Version || stored.State != version.State {
		t.Fatalf("draft identity/state changed: %+v", stored)
	}

	stateMismatch := draft
	stateMismatch.State = domain.DatasetVersionStabilizing
	if _, err := repository.UpdateDatasetVersionDraft(ctx, stateMismatch); !errors.Is(err, ErrDatasetVersionConflict) {
		t.Fatalf("state mismatch error = %v, want conflict", err)
	}
	if _, err := repository.TransitionDatasetVersion(ctx, version.DatasetID, version.ID, domain.DatasetVersionFailed); err != nil {
		t.Fatalf("transition draft to FAILED: %v", err)
	}
	failedDraft := draft
	failedDraft.State = domain.DatasetVersionFailed
	failedDraft.TrainSamples++
	if _, err := repository.UpdateDatasetVersionDraft(ctx, failedDraft); !errors.Is(err, ErrDatasetVersionConflict) {
		t.Fatalf("FAILED payload update error = %v, want conflict", err)
	}
}

func TestTransitionDatasetVersionUsesDomainStateMachineAndPreservesPayload(t *testing.T) {
	repository := datasetRepository(t)
	ctx := context.Background()
	mustCreateDataset(t, repository, publicDataset("dataset-public", "public-data"))
	version := discoveringVersion("dataset-public", "version-transition", "v1")
	if err := repository.CreateDatasetVersion(ctx, version); err != nil {
		t.Fatalf("create version: %v", err)
	}
	version.ManifestSHA256 = strings.Repeat("c", 64)
	version.ManifestObjectKey = "ray-train/platform/datasets/dataset-public/manifests/version-transition.parquet"
	version.TrainSamples = 42
	version.LogicalBytes = 420
	version.PackedBytes = 400
	if _, err := repository.UpdateDatasetVersionDraft(ctx, version); err != nil {
		t.Fatalf("prepare transition payload: %v", err)
	}

	for _, state := range []domain.DatasetVersionState{
		domain.DatasetVersionStabilizing,
		domain.DatasetVersionValidating,
		domain.DatasetVersionPacking,
		domain.DatasetVersionReady,
	} {
		transitioned, err := repository.TransitionDatasetVersion(ctx, version.DatasetID, version.ID, state)
		if err != nil {
			t.Fatalf("transition to %s: %v", state, err)
		}
		if transitioned.State != state || transitioned.ManifestSHA256 != version.ManifestSHA256 || transitioned.TrainSamples != version.TrainSamples || transitioned.LogicalBytes != version.LogicalBytes {
			t.Fatalf("transition changed immutable payload: %+v", transitioned)
		}
	}
	ready, err := repository.GetDatasetVersion(ctx, "team-a", false, version.DatasetID, version.ID)
	if err != nil || ready.State != domain.DatasetVersionReady || ready.ManifestSHA256 != version.ManifestSHA256 {
		t.Fatalf("stored READY version = %+v err=%v", ready, err)
	}
	terminalUpdate := ready
	terminalUpdate.TrainSamples++
	if _, err := repository.UpdateDatasetVersionDraft(ctx, terminalUpdate); !errors.Is(err, ErrDatasetVersionConflict) {
		t.Fatalf("READY payload update error = %v, want conflict", err)
	}

	invalid := discoveringVersion("dataset-public", "version-invalid-transition", "v2")
	if err := repository.CreateDatasetVersion(ctx, invalid); err != nil {
		t.Fatalf("create invalid-transition version: %v", err)
	}
	if _, err := repository.TransitionDatasetVersion(ctx, invalid.DatasetID, invalid.ID, domain.DatasetVersionReady); !errors.Is(err, ErrDatasetVersionConflict) {
		t.Fatalf("invalid transition error = %v, want conflict", err)
	}
	after, err := repository.GetDatasetVersion(ctx, "team-a", false, invalid.DatasetID, invalid.ID)
	if err != nil || after.State != domain.DatasetVersionDiscovering {
		t.Fatalf("invalid transition changed state: %+v err=%v", after, err)
	}
}

func TestCountDatasetVersionReferencesIncludesArchivedJobs(t *testing.T) {
	repository := datasetRepository(t)
	ctx := context.Background()
	mustCreateDataset(t, repository, publicDataset("dataset-public", "public-data"))
	version := readyVersion("dataset-public", "version-pinned", "v1")
	mustInsertDatasetVersion(t, repository, version, time.Now().UTC())
	datasetID, versionID := version.DatasetID, version.ID
	archivedAt := time.Now().UTC()
	jobs := []JobRecord{
		{ID: "job-active", TenantID: "team-a", DatasetID: &datasetID, DatasetVersionID: &versionID},
		{ID: "job-archived", TenantID: "team-a", DatasetID: &datasetID, DatasetVersionID: &versionID, ArchivedAt: &archivedAt},
		{ID: "job-unpinned", TenantID: "team-a"},
	}
	if err := repository.db.Create(&jobs).Error; err != nil {
		t.Fatalf("seed jobs: %v", err)
	}

	count, err := repository.CountDatasetVersionReferences(ctx, version.ID)
	if err != nil || count != 2 {
		t.Fatalf("reference count = %d err=%v, want 2", count, err)
	}
}

func TestRecordDatasetCacheObservationValidatesPinnedJobVersion(t *testing.T) {
	repository := datasetRepository(t)
	ctx := context.Background()
	mustCreateDataset(t, repository, publicDataset("dataset-public", "public-data"))
	version := readyVersion("dataset-public", "version-cache", "v1")
	otherVersion := readyVersion("dataset-public", "version-other", "v2")
	now := time.Now().UTC()
	mustInsertDatasetVersion(t, repository, version, now)
	mustInsertDatasetVersion(t, repository, otherVersion, now.Add(time.Second))
	datasetID, versionID := version.DatasetID, version.ID
	if err := repository.db.Create(&JobRecord{ID: "job-pinned", TenantID: "team-a", DatasetID: &datasetID, DatasetVersionID: &versionID}).Error; err != nil {
		t.Fatalf("seed pinned job: %v", err)
	}

	observation := domain.DatasetCacheObservation{
		ID: "cache-observation-1", DatasetVersionID: version.ID, TrainingJobID: "job-pinned", NodeName: "gpu-node-1",
		CacheHitCount: 10, CacheMissCount: 2, CacheHitBytes: 1_000, CacheMissBytes: 200,
		CachedBytes: 800, EvictedBytes: 100, ChecksumFailureCount: 1, PrefetchWaitMilliseconds: 25,
	}
	invalid := observation
	invalid.ID = "cache-observation-invalid"
	invalid.CacheHitCount = -1
	if err := repository.RecordDatasetCacheObservation(ctx, invalid); err == nil {
		t.Fatal("invalid observation must be rejected")
	}
	mismatched := observation
	mismatched.ID = "cache-observation-mismatch"
	mismatched.DatasetVersionID = otherVersion.ID
	if err := repository.RecordDatasetCacheObservation(ctx, mismatched); !errors.Is(err, ErrDatasetCacheObservationConflict) {
		t.Fatalf("mismatched pin error = %v, want ErrDatasetCacheObservationConflict", err)
	}
	missingJob := observation
	missingJob.ID = "cache-observation-missing"
	missingJob.TrainingJobID = "job-missing"
	if err := repository.RecordDatasetCacheObservation(ctx, missingJob); !errors.Is(err, ErrDatasetCacheObservationConflict) {
		t.Fatalf("missing job error = %v, want ErrDatasetCacheObservationConflict", err)
	}
	if err := repository.RecordDatasetCacheObservation(ctx, observation); err != nil {
		t.Fatalf("record cache observation: %v", err)
	}

	var stored DatasetCacheObservationRecord
	if err := repository.db.Where("id = ?", observation.ID).First(&stored).Error; err != nil {
		t.Fatalf("load cache observation: %v", err)
	}
	decoded, err := stored.toDomain()
	if err != nil || !reflect.DeepEqual(decoded, observation) || stored.CreatedAt.IsZero() {
		t.Fatalf("stored observation = %+v decoded=%+v err=%v", stored, decoded, err)
	}
}

func TestDatasetRecordConversionsRejectInvalidStorageAndHideManifestObjectKey(t *testing.T) {
	now := time.Now().UTC()
	invalidDataset := DatasetRecord{
		ID: "dataset-invalid", Slug: "invalid", Name: "Invalid", SourceSpace: string(domain.DataSpacePublic),
		SourceRelativePath: "labeled/invalid", Visibility: "PRIVATE", SchemaVersion: "schema-v1",
		CreatedAt: now, UpdatedAt: now,
	}
	if _, err := invalidDataset.toDomain(); err == nil {
		t.Fatal("invalid stored dataset must be rejected")
	}

	invalidVersion := datasetVersionRecordForTest(readyVersion("dataset-public", "version-invalid", "v1"), now)
	invalidVersion.ManifestSHA256 = nil
	if _, err := invalidVersion.toDomain(); err == nil {
		t.Fatal("READY stored version without a manifest digest must be rejected")
	}

	validRecord := datasetVersionRecordForTest(readyVersion("dataset-public", "version-private-key", "v2"), now)
	version, err := validRecord.toDomain()
	if err != nil {
		t.Fatalf("convert valid version: %v", err)
	}
	payload, err := json.Marshal(version)
	if err != nil {
		t.Fatalf("marshal version: %v", err)
	}
	if strings.Contains(string(payload), "manifestObjectKey") || strings.Contains(string(payload), version.ManifestObjectKey) {
		t.Fatalf("private manifest object key leaked into JSON: %s", payload)
	}
}
