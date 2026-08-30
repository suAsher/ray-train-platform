package repositories

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gorm.io/gorm"
	publisher "ray-train-platform-backend/datasetpublisher"
	"ray-train-platform-backend/domain"
)

var _ publisher.PublicationRunRepository = (*GormRepository)(nil)
var _ publisher.PublicationManagerRepository = (*GormRepository)(nil)

func publicationRunForTest(datasetID, versionID, runID string) domain.DatasetPublicationRun {
	return domain.DatasetPublicationRun{
		ID: runID, DatasetID: datasetID, DatasetVersionID: versionID,
		State: domain.DatasetVersionDiscovering,
	}
}

func publicationReceiptForTest(datasetID, versionID string) domain.DatasetPublicationReceipt {
	return domain.DatasetPublicationReceipt{
		DatasetID: datasetID, DatasetVersionID: versionID, Version: "v1",
		ManifestSHA256:    strings.Repeat("a", 64),
		ManifestObjectKey: "ray-train/platform/datasets/" + datasetID + "/manifests/" + versionID + ".parquet",
		SchemaVersion:     "schema-v1", TrainSamples: 32, ValSamples: 4,
		SourceObjectCount: 96, LogicalBytes: 4096, PackedBytes: 2048,
	}
}

func storedPublicationVersion(t *testing.T, repository *GormRepository, versionID string) DatasetVersionRecord {
	t.Helper()
	var stored DatasetVersionRecord
	if err := repository.db.Where("id = ?", versionID).First(&stored).Error; err != nil {
		t.Fatalf("load stored dataset version: %v", err)
	}
	return stored
}

func seedPublicationDatasetVersion(t *testing.T, repository *GormRepository, dataset domain.Dataset, versionID string) {
	t.Helper()
	mustCreateDataset(t, repository, dataset)
	version := discoveringVersion(dataset.ID, versionID, "v1")
	if err := repository.CreateDatasetVersion(context.Background(), version); err != nil {
		t.Fatalf("create dataset version %s: %v", versionID, err)
	}
}

func TestEnsureDatasetPublicationRunIsIdempotentBoundAndImmutable(t *testing.T) {
	repository := datasetRepository(t)
	seedPublicationDatasetVersion(t, repository, teamDataset("dataset-team-a", "team-a-data", "team-a"), "version-team-a")
	seedPublicationDatasetVersion(t, repository, teamDataset("dataset-team-b", "team-b-data", "team-b"), "version-team-b")

	run := publicationRunForTest("dataset-team-a", "version-team-a", "publication-1")
	before := run
	created, err := repository.EnsureDatasetPublicationRun(context.Background(), "team-a", false, run)
	if err != nil {
		t.Fatalf("ensure publication run: %v", err)
	}
	if !reflect.DeepEqual(run, before) {
		t.Fatalf("EnsureDatasetPublicationRun mutated input: got=%+v want=%+v", run, before)
	}
	if !reflect.DeepEqual(created, run) {
		t.Fatalf("created run = %+v, want %+v", created, run)
	}

	retried, err := repository.EnsureDatasetPublicationRun(context.Background(), "team-a", false, run)
	if err != nil || !reflect.DeepEqual(retried, created) {
		t.Fatalf("identical ensure must be idempotent: run=%+v err=%v", retried, err)
	}
	var count int64
	if err := repository.db.Model(&DatasetPublicationRunRecord{}).Where("id = ?", run.ID).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("idempotent ensure row count=%d err=%v", count, err)
	}

	loaded, err := repository.GetDatasetPublicationRun(context.Background(), "team-a", false, run.DatasetID, run.DatasetVersionID, run.ID)
	if err != nil || !reflect.DeepEqual(loaded, run) {
		t.Fatalf("get publication run=%+v err=%v", loaded, err)
	}
	if _, err := repository.GetDatasetPublicationRun(context.Background(), "team-b", false, run.DatasetID, run.DatasetVersionID, run.ID); !errors.Is(err, ErrDatasetPublicationRunNotFound) {
		t.Fatalf("cross-tenant get error=%v, want ErrDatasetPublicationRunNotFound", err)
	}
	if _, err := repository.GetDatasetPublicationRun(context.Background(), "team-a", false, run.DatasetID, "version-team-b", run.ID); !errors.Is(err, ErrDatasetPublicationRunNotFound) {
		t.Fatalf("cross-version get error=%v, want ErrDatasetPublicationRunNotFound", err)
	}

	collision := publicationRunForTest("dataset-team-b", "version-team-b", run.ID)
	if _, err := repository.EnsureDatasetPublicationRun(context.Background(), "team-b", false, collision); !errors.Is(err, ErrDatasetPublicationRunNotFound) {
		t.Fatalf("hidden run ID rebound error=%v, want ErrDatasetPublicationRunNotFound", err)
	}
	if _, err := repository.EnsureDatasetPublicationRun(context.Background(), "team-b", false, run); !errors.Is(err, ErrDatasetPublicationRunNotFound) {
		t.Fatalf("hidden identical ensure error=%v, want ErrDatasetPublicationRunNotFound", err)
	}
	mismatchedVersion := publicationRunForTest("dataset-team-a", "version-team-b", "publication-mismatch")
	if _, err := repository.EnsureDatasetPublicationRun(context.Background(), "team-a", false, mismatchedVersion); !errors.Is(err, ErrDatasetPublicationRunNotFound) {
		t.Fatalf("dataset/version mismatch error=%v, want ErrDatasetPublicationRunNotFound", err)
	}
	secondVersion := discoveringVersion("dataset-team-a", "version-team-a-2", "v2")
	if err := repository.CreateDatasetVersion(context.Background(), secondVersion); err != nil {
		t.Fatal(err)
	}
	visibleCollision := publicationRunForTest("dataset-team-a", secondVersion.ID, run.ID)
	if _, err := repository.EnsureDatasetPublicationRun(context.Background(), "team-a", false, visibleCollision); !errors.Is(err, ErrDatasetPublicationRunConflict) {
		t.Fatalf("visible run ID rebound error=%v, want ErrDatasetPublicationRunConflict", err)
	}
}

func TestCreateDatasetPublicationRequestAtomicallyEnforcesManagementScope(t *testing.T) {
	repository := datasetRepository(t)
	team := teamDataset("dataset-team-a", "team-a-data", "team-a")
	public := publicDataset("dataset-public", "public-data")
	mustCreateDataset(t, repository, team)
	mustCreateDataset(t, repository, public)

	version := discoveringVersion(team.ID, "version-team-a", "20260830.1")
	run := publicationRunForTest(team.ID, version.ID, "publication-team-a")
	created, err := repository.CreateDatasetPublicationRequest(context.Background(), "team-a", false, version, run)
	if err != nil || !reflect.DeepEqual(created, run) {
		t.Fatalf("create publication request=%+v err=%v", created, err)
	}
	var versionCount, runCount int64
	if err := repository.db.Model(&DatasetVersionRecord{}).Where("id = ?", version.ID).Count(&versionCount).Error; err != nil {
		t.Fatal(err)
	}
	if err := repository.db.Model(&DatasetPublicationRunRecord{}).Where("id = ?", run.ID).Count(&runCount).Error; err != nil {
		t.Fatal(err)
	}
	if versionCount != 1 || runCount != 1 {
		t.Fatalf("atomic create counts version=%d run=%d", versionCount, runCount)
	}

	unauthorizedVersion := discoveringVersion(public.ID, "version-public-denied", "20260830.2")
	unauthorizedRun := publicationRunForTest(public.ID, unauthorizedVersion.ID, "publication-public-denied")
	if _, err := repository.CreateDatasetPublicationRequest(context.Background(), "team-a", false, unauthorizedVersion, unauthorizedRun); !errors.Is(err, ErrDatasetPublicationRunNotFound) {
		t.Fatalf("ordinary tenant public publication error=%v", err)
	}
	if err := repository.db.Model(&DatasetVersionRecord{}).Where("id = ?", unauthorizedVersion.ID).Count(&versionCount).Error; err != nil || versionCount != 0 {
		t.Fatalf("unauthorized version persisted count=%d err=%v", versionCount, err)
	}

	rollbackVersion := discoveringVersion(team.ID, "version-rollback", "20260830.3")
	conflictingRun := publicationRunForTest(team.ID, rollbackVersion.ID, run.ID)
	if _, err := repository.CreateDatasetPublicationRequest(context.Background(), "team-a", false, rollbackVersion, conflictingRun); !errors.Is(err, ErrDatasetPublicationRunConflict) {
		t.Fatalf("run collision error=%v", err)
	}
	if err := repository.db.Model(&DatasetVersionRecord{}).Where("id = ?", rollbackVersion.ID).Count(&versionCount).Error; err != nil || versionCount != 0 {
		t.Fatalf("transaction did not roll back version count=%d err=%v", versionCount, err)
	}
}

func TestListActiveDatasetPublicationsIsGlobalBoundedAndStateConsistent(t *testing.T) {
	repository := datasetRepository(t)
	public := publicDataset("dataset-public", "public-data")
	team := teamDataset("dataset-team-a", "team-a-data", "team-a")
	mustCreateDataset(t, repository, public)
	mustCreateDataset(t, repository, team)

	publicVersion := discoveringVersion(public.ID, "version-public", "20260830.1")
	publicRun := publicationRunForTest(public.ID, publicVersion.ID, "publication-public")
	if _, err := repository.CreateDatasetPublicationRequest(context.Background(), "", true, publicVersion, publicRun); err != nil {
		t.Fatal(err)
	}
	teamVersion := discoveringVersion(team.ID, "version-team", "20260830.2")
	teamRun := publicationRunForTest(team.ID, teamVersion.ID, "publication-team")
	if _, err := repository.CreateDatasetPublicationRequest(context.Background(), "team-a", false, teamVersion, teamRun); err != nil {
		t.Fatal(err)
	}
	if _, claimed, err := repository.ClaimDatasetPublicationRun(context.Background(), "team-a", false, team.ID, teamVersion.ID, teamRun.ID, time.Now()); err != nil || !claimed {
		t.Fatalf("claim team publication claimed=%t err=%v", claimed, err)
	}

	failedVersion := discoveringVersion(public.ID, "version-failed", "20260830.3")
	failedRun := publicationRunForTest(public.ID, failedVersion.ID, "publication-failed")
	if _, err := repository.CreateDatasetPublicationRequest(context.Background(), "", true, failedVersion, failedRun); err != nil {
		t.Fatal(err)
	}
	claimed, won, err := repository.ClaimDatasetPublicationRun(context.Background(), "", true, public.ID, failedVersion.ID, failedRun.ID, time.Now())
	if err != nil || !won {
		t.Fatalf("claim failed fixture won=%t err=%v", won, err)
	}
	claimed.State = domain.DatasetVersionFailed
	if _, swapped, err := repository.CompareAndSwapDatasetPublicationRun(context.Background(), "", true, domain.DatasetVersionStabilizing, claimed, time.Now()); err != nil || !swapped {
		t.Fatalf("fail fixture swapped=%t err=%v", swapped, err)
	}

	work, err := repository.ListActiveDatasetPublications(context.Background(), 10)
	if err != nil {
		t.Fatalf("list active publications: %v", err)
	}
	if len(work) != 2 || work[0].Dataset.ID != public.ID || work[1].Dataset.ID != team.ID {
		t.Fatalf("active work=%+v", work)
	}
	for _, item := range work {
		if err := item.Validate(); err != nil {
			t.Fatalf("invalid work item: %v", err)
		}
	}
	bounded, err := repository.ListActiveDatasetPublications(context.Background(), 1)
	if err != nil || len(bounded) != 1 {
		t.Fatalf("bounded active work=%+v err=%v", bounded, err)
	}
	if _, err := repository.ListActiveDatasetPublications(context.Background(), 0); !errors.Is(err, ErrDatasetPublicationRunConflict) {
		t.Fatalf("invalid limit error=%v", err)
	}
}

func TestListDatasetVersionGCCandidatesExcludesReferencedAndNonDeprecatedVersions(t *testing.T) {
	repository := datasetRepository(t)
	if err := repository.db.AutoMigrate(&JobRecord{}); err != nil {
		t.Fatalf("migrate job records: %v", err)
	}
	dataset := publicDataset("dataset-public", "public-data")
	mustCreateDataset(t, repository, dataset)
	deprecated := readyVersion(dataset.ID, "version-deprecated", "20260801.1")
	deprecated.State = domain.DatasetVersionDeprecated
	referenced := readyVersion(dataset.ID, "version-referenced", "20260801.2")
	referenced.State = domain.DatasetVersionDeprecated
	ready := readyVersion(dataset.ID, "version-ready", "20260801.3")
	for _, version := range []domain.DatasetVersion{deprecated, referenced, ready} {
		mustInsertDatasetVersion(t, repository, version, time.Now())
	}
	job := JobRecord{ID: "job-referencing-version", TenantID: "tenant-a", UserID: "user-a", Name: "reference", ObservedState: string(domain.StateSucceeded), DatasetID: &dataset.ID, DatasetVersionID: &referenced.ID}
	if err := repository.db.Create(&job).Error; err != nil {
		t.Fatalf("create referenced job: %v", err)
	}

	candidates, err := repository.ListDatasetVersionGCCandidates(context.Background())
	if err != nil {
		t.Fatalf("list GC candidates: %v", err)
	}
	if got := versionIDs(candidates); !reflect.DeepEqual(got, []string{deprecated.ID}) {
		t.Fatalf("GC candidates=%v", got)
	}
}

func TestDatasetPublicationMutationsRequireDatasetManagementScope(t *testing.T) {
	repository := datasetRepository(t)
	public := publicDataset("dataset-public", "public-data")
	seedPublicationDatasetVersion(t, repository, public, "version-public")
	run := publicationRunForTest(public.ID, "version-public", "publication-public")

	if _, err := repository.EnsureDatasetPublicationRun(context.Background(), "team-a", false, run); !errors.Is(err, ErrDatasetPublicationRunNotFound) {
		t.Fatalf("ordinary tenant published public dataset: err=%v", err)
	}
	created, err := repository.EnsureDatasetPublicationRun(context.Background(), "", true, run)
	if err != nil || !reflect.DeepEqual(created, run) {
		t.Fatalf("superadmin public publication run=%+v err=%v", created, err)
	}
	if _, _, err := repository.ClaimDatasetPublicationRun(context.Background(), "team-a", false, run.DatasetID, run.DatasetVersionID, run.ID, time.Now()); !errors.Is(err, ErrDatasetPublicationRunNotFound) {
		t.Fatalf("ordinary tenant claimed public publication: err=%v", err)
	}
}

func TestClaimDatasetPublicationRunHasSingleConcurrentWinner(t *testing.T) {
	repository := datasetRepository(t)
	seedPublicationDatasetVersion(t, repository, teamDataset("dataset-team-a", "team-a-data", "team-a"), "version-team-a")
	run := publicationRunForTest("dataset-team-a", "version-team-a", "publication-claim")
	if _, err := repository.EnsureDatasetPublicationRun(context.Background(), "team-a", false, run); err != nil {
		t.Fatal(err)
	}
	sqlDatabase, err := repository.db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDatabase.SetMaxOpenConns(1)

	claimedAt := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	start := make(chan struct{})
	errorsByWorker := make(chan error, 8)
	var winners atomic.Int32
	var waitGroup sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			<-start
			claimed, won, claimErr := repository.ClaimDatasetPublicationRun(
				context.Background(), "team-a", false, run.DatasetID, run.DatasetVersionID, run.ID, claimedAt,
			)
			if claimErr != nil {
				errorsByWorker <- claimErr
				return
			}
			if claimed.State != domain.DatasetVersionStabilizing {
				errorsByWorker <- errors.New("claim did not return STABILIZING state")
				return
			}
			if won {
				winners.Add(1)
			}
		}()
	}
	close(start)
	waitGroup.Wait()
	close(errorsByWorker)
	for workerErr := range errorsByWorker {
		t.Errorf("concurrent claim: %v", workerErr)
	}
	if got := winners.Load(); got != 1 {
		t.Fatalf("claim winners=%d, want 1", got)
	}

	var stored DatasetPublicationRunRecord
	if err := repository.db.Where("id = ?", run.ID).First(&stored).Error; err != nil {
		t.Fatal(err)
	}
	if stored.StartedAt == nil || !stored.StartedAt.Equal(claimedAt) || stored.State != string(domain.DatasetVersionStabilizing) {
		t.Fatalf("stored claim metadata=%+v", stored)
	}
	if version := storedPublicationVersion(t, repository, run.DatasetVersionID); version.State != string(domain.DatasetVersionStabilizing) {
		t.Fatalf("claimed dataset version state=%q, want STABILIZING", version.State)
	}
}

func TestCompareAndSwapDatasetPublicationRunAdvancesAndCompletes(t *testing.T) {
	repository := datasetRepository(t)
	seedPublicationDatasetVersion(t, repository, teamDataset("dataset-team-a", "team-a-data", "team-a"), "version-team-a")
	run := publicationRunForTest("dataset-team-a", "version-team-a", "publication-progress")
	if _, err := repository.EnsureDatasetPublicationRun(context.Background(), "team-a", false, run); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	claimed, won, err := repository.ClaimDatasetPublicationRun(context.Background(), "team-a", false, run.DatasetID, run.DatasetVersionID, run.ID, now)
	if err != nil || !won {
		t.Fatalf("claim run=%+v won=%t err=%v", claimed, won, err)
	}

	validating := claimed
	validating.State = domain.DatasetVersionValidating
	validating.TotalPartitions = 2
	validating.SourceObjectCount = 4
	validatingBefore := validating
	updated, swapped, err := repository.CompareAndSwapDatasetPublicationRun(context.Background(), "team-a", false, domain.DatasetVersionStabilizing, validating, now.Add(time.Second))
	if err != nil || !swapped || updated.State != domain.DatasetVersionValidating {
		t.Fatalf("advance validating run=%+v swapped=%t err=%v", updated, swapped, err)
	}
	if !reflect.DeepEqual(validating, validatingBefore) {
		t.Fatalf("CompareAndSwapDatasetPublicationRun mutated input: got=%+v want=%+v", validating, validatingBefore)
	}
	if version := storedPublicationVersion(t, repository, run.DatasetVersionID); version.State != string(domain.DatasetVersionValidating) {
		t.Fatalf("validating dataset version state=%q", version.State)
	}

	stale := validating
	stale.ProcessedObjectCount = 1
	current, swapped, err := repository.CompareAndSwapDatasetPublicationRun(context.Background(), "team-a", false, domain.DatasetVersionStabilizing, stale, now.Add(2*time.Second))
	if err != nil || swapped || current.State != domain.DatasetVersionValidating {
		t.Fatalf("stale CAS run=%+v swapped=%t err=%v", current, swapped, err)
	}

	packing := validating
	packing.State = domain.DatasetVersionPacking
	packing.CompletedPartitions = 1
	packing.ProcessedObjectCount = 2
	packing, swapped, err = repository.CompareAndSwapDatasetPublicationRun(context.Background(), "team-a", false, domain.DatasetVersionValidating, packing, now.Add(3*time.Second))
	if err != nil || !swapped {
		t.Fatalf("advance packing run=%+v swapped=%t err=%v", packing, swapped, err)
	}
	if version := storedPublicationVersion(t, repository, run.DatasetVersionID); version.State != string(domain.DatasetVersionPacking) {
		t.Fatalf("packing dataset version state=%q", version.State)
	}
	readyProgress := packing
	readyProgress.State = domain.DatasetVersionReady
	readyProgress.CompletedPartitions = readyProgress.TotalPartitions
	readyProgress.ProcessedObjectCount = readyProgress.SourceObjectCount
	finishedAt := now.Add(4 * time.Second)
	receipt := publicationReceiptForTest(run.DatasetID, run.DatasetVersionID)
	receipt.SourceObjectCount = readyProgress.SourceObjectCount
	ready, swapped, err := repository.FinalizeDatasetPublicationRun(
		context.Background(), "team-a", false, domain.DatasetVersionPacking,
		readyProgress, receipt, domain.DefaultDatasetInternalPrefix, finishedAt,
	)
	if err != nil || !swapped || ready.State != domain.DatasetVersionReady {
		t.Fatalf("complete run=%+v swapped=%t err=%v", ready, swapped, err)
	}

	var stored DatasetPublicationRunRecord
	if err := repository.db.Where("id = ?", run.ID).First(&stored).Error; err != nil {
		t.Fatal(err)
	}
	if stored.FinishedAt == nil || !stored.FinishedAt.Equal(finishedAt) || stored.CompletedPartitions != stored.TotalPartitions || stored.ProcessedObjectCount != stored.SourceObjectCount {
		t.Fatalf("completion was not written back atomically: %+v", stored)
	}
	version := storedPublicationVersion(t, repository, run.DatasetVersionID)
	if version.State != string(domain.DatasetVersionReady) || valueOrEmpty(version.ManifestSHA256) != receipt.ManifestSHA256 || valueOrEmpty(version.ManifestObjectKey) != receipt.ManifestObjectKey || version.TrainSamples != receipt.TrainSamples || version.SourceObjectCount != receipt.SourceObjectCount {
		t.Fatalf("ready dataset version was not finalized with receipt: %+v", version)
	}
}

func TestFinalizeDatasetPublicationRunRejectsReceiptWithoutPartialReadyState(t *testing.T) {
	repository := datasetRepository(t)
	seedPublicationDatasetVersion(t, repository, teamDataset("dataset-team-a", "team-a-data", "team-a"), "version-team-a")
	run := publicationRunForTest("dataset-team-a", "version-team-a", "publication-invalid-receipt")
	if _, err := repository.EnsureDatasetPublicationRun(context.Background(), "team-a", false, run); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	current, won, err := repository.ClaimDatasetPublicationRun(context.Background(), "team-a", false, run.DatasetID, run.DatasetVersionID, run.ID, now)
	if err != nil || !won {
		t.Fatalf("claim run=%+v won=%t err=%v", current, won, err)
	}
	validating := current
	validating.State = domain.DatasetVersionValidating
	validating.TotalPartitions = 1
	validating.SourceObjectCount = 1
	validating, _, err = repository.CompareAndSwapDatasetPublicationRun(context.Background(), "team-a", false, domain.DatasetVersionStabilizing, validating, now)
	if err != nil {
		t.Fatal(err)
	}
	packing := validating
	packing.State = domain.DatasetVersionPacking
	packing, _, err = repository.CompareAndSwapDatasetPublicationRun(context.Background(), "team-a", false, domain.DatasetVersionValidating, packing, now)
	if err != nil {
		t.Fatal(err)
	}
	ready := packing
	ready.State = domain.DatasetVersionReady
	ready.CompletedPartitions = 1
	ready.ProcessedObjectCount = 1
	receipt := publicationReceiptForTest(run.DatasetID, run.DatasetVersionID)
	receipt.SourceObjectCount = ready.SourceObjectCount
	receipt.ManifestObjectKey = "ray-train/platform/datasets/another-dataset/manifests/" + run.DatasetVersionID + ".parquet"

	current, swapped, err := repository.FinalizeDatasetPublicationRun(
		context.Background(), "team-a", false, domain.DatasetVersionPacking,
		ready, receipt, domain.DefaultDatasetInternalPrefix, now,
	)
	if !errors.Is(err, ErrDatasetPublicationRunConflict) || swapped || current.State != domain.DatasetVersionPacking {
		t.Fatalf("invalid receipt finalization run=%+v swapped=%t err=%v", current, swapped, err)
	}
	if version := storedPublicationVersion(t, repository, run.DatasetVersionID); version.State != string(domain.DatasetVersionPacking) || version.ManifestSHA256 != nil {
		t.Fatalf("invalid receipt partially finalized dataset version: %+v", version)
	}
}

func TestFinalizeDatasetPublicationRunRejectsReceiptCountMismatch(t *testing.T) {
	repository := datasetRepository(t)
	seedPublicationDatasetVersion(t, repository, teamDataset("dataset-team-a", "team-a-data", "team-a"), "version-team-a")
	run := publicationRunForTest("dataset-team-a", "version-team-a", "publication-count-mismatch")
	if _, err := repository.EnsureDatasetPublicationRun(context.Background(), "team-a", false, run); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	current, won, err := repository.ClaimDatasetPublicationRun(context.Background(), "team-a", false, run.DatasetID, run.DatasetVersionID, run.ID, now)
	if err != nil || !won {
		t.Fatalf("claim run=%+v won=%t err=%v", current, won, err)
	}
	validating := current
	validating.State = domain.DatasetVersionValidating
	validating.TotalPartitions = 1
	validating.SourceObjectCount = 4
	validating, _, err = repository.CompareAndSwapDatasetPublicationRun(context.Background(), "team-a", false, domain.DatasetVersionStabilizing, validating, now)
	if err != nil {
		t.Fatal(err)
	}
	packing := validating
	packing.State = domain.DatasetVersionPacking
	packing, _, err = repository.CompareAndSwapDatasetPublicationRun(context.Background(), "team-a", false, domain.DatasetVersionValidating, packing, now)
	if err != nil {
		t.Fatal(err)
	}
	ready := packing
	ready.State = domain.DatasetVersionReady
	ready.CompletedPartitions = 1
	ready.ProcessedObjectCount = 4
	receipt := publicationReceiptForTest(run.DatasetID, run.DatasetVersionID)
	receipt.SourceObjectCount = 5

	current, swapped, err := repository.FinalizeDatasetPublicationRun(
		context.Background(), "team-a", false, domain.DatasetVersionPacking,
		ready, receipt, domain.DefaultDatasetInternalPrefix, now,
	)
	if !errors.Is(err, ErrDatasetPublicationRunConflict) || swapped || current.State != domain.DatasetVersionPacking {
		t.Fatalf("mismatched receipt run=%+v swapped=%t err=%v", current, swapped, err)
	}
	if version := storedPublicationVersion(t, repository, run.DatasetVersionID); version.State != string(domain.DatasetVersionPacking) || version.ManifestSHA256 != nil {
		t.Fatalf("mismatched receipt partially finalized version: %+v", version)
	}
}

func TestFailedDatasetPublicationRunRetriesOnlyAfterBackoff(t *testing.T) {
	repository := datasetRepository(t)
	seedPublicationDatasetVersion(t, repository, teamDataset("dataset-team-a", "team-a-data", "team-a"), "version-team-a")
	run := publicationRunForTest("dataset-team-a", "version-team-a", "publication-retry")
	if _, err := repository.EnsureDatasetPublicationRun(context.Background(), "team-a", false, run); err != nil {
		t.Fatal(err)
	}
	failedAt := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	claimed, won, err := repository.ClaimDatasetPublicationRun(context.Background(), "team-a", false, run.DatasetID, run.DatasetVersionID, run.ID, failedAt.Add(-time.Second))
	if err != nil || !won {
		t.Fatalf("claim run=%+v won=%t err=%v", claimed, won, err)
	}
	failed := claimed
	failed.State = domain.DatasetVersionFailed
	failed.TotalPartitions = 1
	failed.FailedPartitions = 1
	failed.SourceObjectCount = 1
	failed.FailedObjectCount = 1
	failed, swapped, err := repository.CompareAndSwapDatasetPublicationRun(context.Background(), "team-a", false, domain.DatasetVersionStabilizing, failed, failedAt)
	if err != nil || !swapped {
		t.Fatalf("mark failed run=%+v swapped=%t err=%v", failed, swapped, err)
	}
	if version := storedPublicationVersion(t, repository, run.DatasetVersionID); version.State != string(domain.DatasetVersionFailed) {
		t.Fatalf("failed dataset version state=%q", version.State)
	}

	tooEarly, retried, err := repository.RetryDatasetPublicationRun(context.Background(), "team-a", false, run.DatasetID, run.DatasetVersionID, run.ID, failedAt.Add(4*time.Second), 5*time.Second)
	if err != nil || retried || tooEarly.State != domain.DatasetVersionFailed {
		t.Fatalf("early retry run=%+v retried=%t err=%v", tooEarly, retried, err)
	}
	retriedRun, retried, err := repository.RetryDatasetPublicationRun(context.Background(), "team-a", false, run.DatasetID, run.DatasetVersionID, run.ID, failedAt.Add(5*time.Second), 5*time.Second)
	if err != nil || !retried || retriedRun.State != domain.DatasetVersionDiscovering {
		t.Fatalf("eligible retry run=%+v retried=%t err=%v", retriedRun, retried, err)
	}
	if retriedRun.TotalPartitions != 0 || retriedRun.CompletedPartitions != 0 || retriedRun.FailedPartitions != 0 || retriedRun.SourceObjectCount != 0 || retriedRun.ProcessedObjectCount != 0 || retriedRun.FailedObjectCount != 0 {
		t.Fatalf("retried run retained stale progress: %+v", retriedRun)
	}
	var stored DatasetPublicationRunRecord
	if err := repository.db.Where("id = ?", run.ID).First(&stored).Error; err != nil {
		t.Fatal(err)
	}
	if stored.StartedAt != nil || stored.FinishedAt != nil {
		t.Fatalf("retry retained attempt timestamps: %+v", stored)
	}
	if version := storedPublicationVersion(t, repository, run.DatasetVersionID); version.State != string(domain.DatasetVersionDiscovering) {
		t.Fatalf("retried dataset version state=%q, want DISCOVERING", version.State)
	}
}

func TestDatasetPublicationRunOperationsReturnCanceledContext(t *testing.T) {
	repository := datasetRepository(t)
	seedPublicationDatasetVersion(t, repository, teamDataset("dataset-team-a", "team-a-data", "team-a"), "version-team-a")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	run := publicationRunForTest("dataset-team-a", "version-team-a", "publication-canceled")
	if _, err := repository.EnsureDatasetPublicationRun(ctx, "team-a", false, run); !errors.Is(err, context.Canceled) {
		t.Fatalf("ensure canceled error=%v, want context.Canceled", err)
	}
	var count int64
	if err := repository.db.Model(&DatasetPublicationRunRecord{}).Where("id = ?", run.ID).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("canceled ensure persisted row: count=%d err=%v", count, err)
	}
}

func TestDatasetPublicationRunRepositorySanitizesDatabaseFailures(t *testing.T) {
	repository := datasetRepository(t)
	seedPublicationDatasetVersion(t, repository, teamDataset("dataset-team-a", "team-a-data", "team-a"), "version-team-a")
	run := publicationRunForTest("dataset-team-a", "version-team-a", "publication-sensitive-error")
	if _, err := repository.EnsureDatasetPublicationRun(context.Background(), "team-a", false, run); err != nil {
		t.Fatal(err)
	}

	callbackName := "test:sensitive-publication-query-error"
	sensitive := errors.New("postgres://admin:password@internal-db/runs?AK=test-ak&SK=test-sk")
	if err := repository.db.Callback().Query().Before("gorm:query").Register(callbackName, func(database *gorm.DB) {
		database.AddError(sensitive)
	}); err != nil {
		t.Fatalf("register query failure: %v", err)
	}
	_, err := repository.GetDatasetPublicationRun(context.Background(), "team-a", false, run.DatasetID, run.DatasetVersionID, run.ID)
	if !errors.Is(err, ErrDatasetPublicationRunUnavailable) {
		t.Fatalf("database failure error=%v, want ErrDatasetPublicationRunUnavailable", err)
	}
	message := strings.ToLower(err.Error())
	for _, forbidden := range []string{"password", "internal-db", "test-ak", "test-sk", "postgres://"} {
		if strings.Contains(message, forbidden) {
			t.Fatalf("repository error leaked %q: %v", forbidden, err)
		}
	}
}

func TestDatasetPublicationRunRejectsInvalidTransitionsAndRetryInputs(t *testing.T) {
	repository := datasetRepository(t)
	seedPublicationDatasetVersion(t, repository, teamDataset("dataset-team-a", "team-a-data", "team-a"), "version-team-a")

	invalid := publicationRunForTest("dataset-team-a", "version-team-a", "bad/run")
	if _, err := repository.EnsureDatasetPublicationRun(context.Background(), "team-a", false, invalid); !errors.Is(err, ErrDatasetPublicationRunConflict) {
		t.Fatalf("invalid run error=%v", err)
	}
	nonDiscovering := publicationRunForTest("dataset-team-a", "version-team-a", "publication-not-discovering")
	nonDiscovering.State = domain.DatasetVersionPacking
	if _, err := repository.EnsureDatasetPublicationRun(context.Background(), "team-a", false, nonDiscovering); !errors.Is(err, ErrDatasetPublicationRunConflict) {
		t.Fatalf("non-DISCOVERING run error=%v", err)
	}

	run := publicationRunForTest("dataset-team-a", "version-team-a", "publication-invalid-transition")
	if _, err := repository.EnsureDatasetPublicationRun(context.Background(), "team-a", false, run); err != nil {
		t.Fatal(err)
	}
	claimed, won, err := repository.ClaimDatasetPublicationRun(context.Background(), "team-a", false, run.DatasetID, run.DatasetVersionID, run.ID, time.Time{})
	if err != nil || !won {
		t.Fatalf("claim run=%+v won=%t err=%v", claimed, won, err)
	}
	var stored DatasetPublicationRunRecord
	if err := repository.db.Where("id = ?", run.ID).First(&stored).Error; err != nil || stored.StartedAt == nil || stored.StartedAt.IsZero() {
		t.Fatalf("zero claim time was not normalized: row=%+v err=%v", stored, err)
	}

	skipped := claimed
	skipped.State = domain.DatasetVersionPacking
	if _, _, err := repository.CompareAndSwapDatasetPublicationRun(context.Background(), "team-a", false, domain.DatasetVersionStabilizing, skipped, time.Now()); !errors.Is(err, ErrDatasetPublicationRunConflict) {
		t.Fatalf("skipped transition error=%v", err)
	}
	invalidProgress := claimed
	invalidProgress.State = domain.DatasetVersionValidating
	invalidProgress.CompletedPartitions = 1
	if _, _, err := repository.CompareAndSwapDatasetPublicationRun(context.Background(), "team-a", false, domain.DatasetVersionStabilizing, invalidProgress, time.Now()); !errors.Is(err, ErrDatasetPublicationRunConflict) {
		t.Fatalf("invalid progress error=%v", err)
	}
	if _, retried, err := repository.RetryDatasetPublicationRun(context.Background(), "team-a", false, run.DatasetID, run.DatasetVersionID, run.ID, time.Now(), time.Second); err != nil || retried {
		t.Fatalf("active retry retried=%t err=%v", retried, err)
	}
	if _, _, err := repository.RetryDatasetPublicationRun(context.Background(), "team-a", false, run.DatasetID, run.DatasetVersionID, run.ID, time.Now(), -time.Second); !errors.Is(err, ErrDatasetPublicationRunConflict) {
		t.Fatalf("negative backoff error=%v", err)
	}
	validating := claimed
	validating.State = domain.DatasetVersionValidating
	validating.TotalPartitions = 2
	validating.SourceObjectCount = 2
	validating, swapped, err := repository.CompareAndSwapDatasetPublicationRun(context.Background(), "team-a", false, domain.DatasetVersionStabilizing, validating, time.Now())
	if err != nil || !swapped {
		t.Fatalf("advance validating run=%+v swapped=%t err=%v", validating, swapped, err)
	}
	packing := validating
	packing.State = domain.DatasetVersionPacking
	packing, swapped, err = repository.CompareAndSwapDatasetPublicationRun(context.Background(), "team-a", false, domain.DatasetVersionValidating, packing, time.Now())
	if err != nil || !swapped {
		t.Fatalf("advance packing run=%+v swapped=%t err=%v", packing, swapped, err)
	}
	incomplete := packing
	incomplete.State = domain.DatasetVersionReady
	incomplete.CompletedPartitions = 1
	incomplete.ProcessedObjectCount = 1
	if _, _, err := repository.CompareAndSwapDatasetPublicationRun(context.Background(), "team-a", false, domain.DatasetVersionPacking, incomplete, time.Now()); !errors.Is(err, ErrDatasetPublicationRunConflict) {
		t.Fatalf("incomplete READY error=%v", err)
	}
	if _, err := repository.GetDatasetPublicationRun(nil, "team-a", false, run.DatasetID, run.DatasetVersionID, run.ID); !errors.Is(err, ErrDatasetPublicationRunConflict) {
		t.Fatalf("nil context error=%v", err)
	}
}

func TestRetryDatasetPublicationRunHasSingleConcurrentWinner(t *testing.T) {
	repository := datasetRepository(t)
	seedPublicationDatasetVersion(t, repository, teamDataset("dataset-team-a", "team-a-data", "team-a"), "version-team-a")
	run := publicationRunForTest("dataset-team-a", "version-team-a", "publication-concurrent-retry")
	if _, err := repository.EnsureDatasetPublicationRun(context.Background(), "team-a", false, run); err != nil {
		t.Fatal(err)
	}
	failedAt := time.Now().UTC().Add(-time.Minute)
	claimed, won, err := repository.ClaimDatasetPublicationRun(context.Background(), "team-a", false, run.DatasetID, run.DatasetVersionID, run.ID, failedAt.Add(-time.Second))
	if err != nil || !won {
		t.Fatalf("claim run=%+v won=%t err=%v", claimed, won, err)
	}
	failed := claimed
	failed.State = domain.DatasetVersionFailed
	if _, swapped, err := repository.CompareAndSwapDatasetPublicationRun(context.Background(), "team-a", false, domain.DatasetVersionStabilizing, failed, failedAt); err != nil || !swapped {
		t.Fatalf("mark failed swapped=%t err=%v", swapped, err)
	}
	sqlDatabase, err := repository.db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDatabase.SetMaxOpenConns(1)

	var winners atomic.Int32
	var waitGroup sync.WaitGroup
	errorsByWorker := make(chan error, 6)
	for worker := 0; worker < 6; worker++ {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			_, retried, retryErr := repository.RetryDatasetPublicationRun(context.Background(), "team-a", false, run.DatasetID, run.DatasetVersionID, run.ID, failedAt.Add(time.Minute), time.Second)
			if retryErr != nil {
				errorsByWorker <- retryErr
				return
			}
			if retried {
				winners.Add(1)
			}
		}()
	}
	waitGroup.Wait()
	close(errorsByWorker)
	for workerErr := range errorsByWorker {
		t.Errorf("concurrent retry: %v", workerErr)
	}
	if winners.Load() != 1 {
		t.Fatalf("retry winners=%d, want 1", winners.Load())
	}
}

func TestDatasetPublicationRunRepositoryPreservesDependencyCancellation(t *testing.T) {
	repository := datasetRepository(t)
	callbackName := "test:canceled-publication-query"
	if err := repository.db.Callback().Query().Before("gorm:query").Register(callbackName, func(database *gorm.DB) {
		database.AddError(context.Canceled)
	}); err != nil {
		t.Fatal(err)
	}
	_, err := repository.GetDatasetPublicationRun(context.Background(), "team-a", false, "dataset-team-a", "version-team-a", "publication-canceled-query")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("dependency cancellation error=%v, want context.Canceled", err)
	}
}

func TestDatasetPublicationRunCanceledMutationsAndCorruptRowsFailClosed(t *testing.T) {
	repository := datasetRepository(t)
	seedPublicationDatasetVersion(t, repository, teamDataset("dataset-team-a", "team-a-data", "team-a"), "version-team-a")
	run := publicationRunForTest("dataset-team-a", "version-team-a", "publication-boundaries")
	if _, err := repository.EnsureDatasetPublicationRun(context.Background(), "team-a", false, run); err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	next := run
	next.State = domain.DatasetVersionValidating
	if _, _, err := repository.ClaimDatasetPublicationRun(canceled, "team-a", false, run.DatasetID, run.DatasetVersionID, run.ID, time.Now()); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled claim error=%v", err)
	}
	if _, _, err := repository.CompareAndSwapDatasetPublicationRun(canceled, "team-a", false, domain.DatasetVersionStabilizing, next, time.Now()); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled CAS error=%v", err)
	}
	if _, _, err := repository.RetryDatasetPublicationRun(canceled, "team-a", false, run.DatasetID, run.DatasetVersionID, run.ID, time.Now(), time.Second); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled retry error=%v", err)
	}

	if err := repository.db.Model(&DatasetPublicationRunRecord{}).Where("id = ?", run.ID).Update("state", "tos://internal/private?SK=test-sk").Error; err != nil {
		t.Fatal(err)
	}
	_, err := repository.GetDatasetPublicationRun(context.Background(), "team-a", false, run.DatasetID, run.DatasetVersionID, run.ID)
	if !errors.Is(err, ErrDatasetPublicationRunUnavailable) || strings.Contains(strings.ToLower(err.Error()), "tos://") || strings.Contains(strings.ToLower(err.Error()), "test-sk") {
		t.Fatalf("corrupt stored state error=%v", err)
	}
}

func TestDatasetPublicationRunSanitizesMutationDatabaseFailure(t *testing.T) {
	repository := datasetRepository(t)
	seedPublicationDatasetVersion(t, repository, teamDataset("dataset-team-a", "team-a-data", "team-a"), "version-team-a")
	run := publicationRunForTest("dataset-team-a", "version-team-a", "publication-update-error")
	if _, err := repository.EnsureDatasetPublicationRun(context.Background(), "team-a", false, run); err != nil {
		t.Fatal(err)
	}
	sensitive := errors.New("postgres://admin:password@internal-db/runs?AK=test-ak")
	if err := repository.db.Callback().Update().Before("gorm:update").Register("test:sensitive-publication-update", func(database *gorm.DB) {
		database.AddError(sensitive)
	}); err != nil {
		t.Fatal(err)
	}
	_, _, err := repository.ClaimDatasetPublicationRun(context.Background(), "team-a", false, run.DatasetID, run.DatasetVersionID, run.ID, time.Now())
	if !errors.Is(err, ErrDatasetPublicationRunUnavailable) {
		t.Fatalf("mutation database error=%v", err)
	}
	message := strings.ToLower(err.Error())
	for _, forbidden := range []string{"password", "internal-db", "test-ak", "postgres://"} {
		if strings.Contains(message, forbidden) {
			t.Fatalf("mutation error leaked %q: %v", forbidden, err)
		}
	}
}
