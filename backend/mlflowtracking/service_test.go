package mlflowtracking_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	tracking "ray-train-platform-backend/mlflowtracking"
	"ray-train-platform-backend/repositories"
)

type testProvider struct {
	experimentCreates, runCreates, logs, finishes int
	experiments                                   map[string]string
	runs                                          map[string]string
	createErr, finishErr                          error
	snapshotStatus                                string
	readErr                                       error
	finishEndTimes                                []int64
	beforeLog                                     func()
}

func (p *testProvider) CreateExperiment(_ context.Context, op string) (string, error) {
	p.experimentCreates++
	id := fmt.Sprint(p.experimentCreates)
	p.experiments[op] = id
	return id, p.createErr
}
func (p *testProvider) FindExperiment(_ context.Context, op string) (string, bool, error) {
	id, ok := p.experiments[op]
	return id, ok, nil
}
func (p *testProvider) CreateRun(_ context.Context, exp, op, name string) (string, error) {
	p.runCreates++
	id := fmt.Sprintf("%032x", p.runCreates)
	p.runs[op] = id
	return id, p.createErr
}
func (p *testProvider) FindRun(_ context.Context, exp, op string) (string, bool, error) {
	id, ok := p.runs[op]
	return id, ok, nil
}
func (p *testProvider) ReadRun(context.Context, string, string, string) (tracking.Snapshot, error) {
	status := p.snapshotStatus
	if status == "" {
		status = "RUNNING"
	}
	return tracking.Snapshot{Status: status, Latest: map[string]float64{"loss": 0.5}, Params: map[string]string{"epochs": "5"}, LatestMetrics: map[string]tracking.MetricPoint{"loss": {Value: 0.5, TimestampMS: 2000, Step: 7}}, Tags: map[string]string{"review": "candidate"}}, p.readErr
}
func (p *testProvider) LogRun(context.Context, string, string, string, tracking.Batch) error {
	p.logs++
	if p.beforeLog != nil {
		p.beforeLog()
	}
	return nil
}
func (p *testProvider) FinishRun(_ context.Context, _, _, _, _ string, endTimeMS int64) error {
	p.finishes++
	p.finishEndTimes = append(p.finishEndTimes, endTimeMS)
	return p.finishErr
}

func fixture(t *testing.T) (*tracking.Service, *repositories.MLflowTrackingStore, *testProvider, *time.Time) {
	t.Helper()
	database, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "tracking.db")+"?_busy_timeout=5000&_journal_mode=WAL"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.AutoMigrate(&repositories.MLflowTrackingExperimentRecord{}, &repositories.MLflowTrackingRunRecord{}); err != nil {
		t.Fatal(err)
	}
	store := repositories.NewMLflowTrackingStore(database)
	provider := &testProvider{experiments: map[string]string{}, runs: map[string]string{}}
	now := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	service := tracking.New(store, provider, tracking.Options{CursorKey: []byte(strings.Repeat("k", 32)), Now: func() time.Time { return now }})
	return service, store, provider, &now
}

var owner = tracking.Actor{TenantID: "team-a", UserID: "owner-a"}

func createPair(t *testing.T, s *tracking.Service) (tracking.Experiment, tracking.Run) {
	t.Helper()
	exp, err := s.CreateExperiment(context.Background(), owner, "experiment-key-1", "Quality evaluation")
	if err != nil {
		t.Fatal(err)
	}
	run, err := s.CreateRun(context.Background(), owner, exp.ID, "run-key-1", "candidate-one")
	if err != nil {
		t.Fatal(err)
	}
	return exp, run
}

func TestCreationIdempotencyDurablyReconcilesAmbiguousResults(t *testing.T) {
	s, store, p, _ := fixture(t)
	p.createErr = errors.New("upstream timeout after accepting create")
	if _, err := s.CreateExperiment(context.Background(), owner, "experiment-key-1", "Quality evaluation"); !errors.Is(err, tracking.ErrPending) {
		t.Fatalf("ambiguous create=%v", err)
	}
	restarted := tracking.New(store, p, tracking.Options{CursorKey: []byte(strings.Repeat("k", 32))})
	exp, err := restarted.CreateExperiment(context.Background(), owner, "experiment-key-1", "Quality evaluation")
	if err != nil || exp.UpstreamID == "" || p.experimentCreates != 1 {
		t.Fatalf("replay duplicated experiment: %+v %v creates=%d", exp, err, p.experimentCreates)
	}
	if _, err := restarted.CreateExperiment(context.Background(), owner, "experiment-key-1", "Changed payload"); !errors.Is(err, tracking.ErrConflict) {
		t.Fatalf("changed replay accepted: %v", err)
	}
	if _, err := s.CreateRun(context.Background(), owner, exp.ID, "run-key-1", "candidate-one"); !errors.Is(err, tracking.ErrPending) {
		t.Fatalf("ambiguous run=%v", err)
	}
	run, err := restarted.CreateRun(context.Background(), owner, exp.ID, "run-key-1", "candidate-one")
	if err != nil || run.UpstreamID == "" || p.runCreates != 1 {
		t.Fatalf("replay duplicated run: %+v %v creates=%d", run, err, p.runCreates)
	}
}

func TestUnresolvedCreateNeverBlindlyCreatesAgain(t *testing.T) {
	s, _, p, _ := fixture(t)
	p.createErr = errors.New("unknown outcome")
	_, _ = s.CreateExperiment(context.Background(), owner, "unknown-key", "Uncertain experiment")
	p.experiments = map[string]string{}
	for i := 0; i < 3; i++ {
		if _, err := s.CreateExperiment(context.Background(), owner, "unknown-key", "Uncertain experiment"); !errors.Is(err, tracking.ErrPending) {
			t.Fatalf("missing reconcile=%v", err)
		}
	}
	if p.experimentCreates != 1 {
		t.Fatal("unknown create was replayed")
	}
}

func TestPrivateOwnershipAndExternalRecordsAreAuthoritative(t *testing.T) {
	s, _, p, _ := fixture(t)
	exp, run := createPair(t, s)
	for _, actor := range []tracking.Actor{{TenantID: "team-a", UserID: "another"}, {TenantID: "team-b", UserID: "owner-a"}} {
		if _, err := s.GetRun(context.Background(), actor, run.ID); !errors.Is(err, tracking.ErrNotFound) {
			t.Fatalf("cross-owner read=%v", err)
		}
		if _, err := s.CreateRun(context.Background(), actor, exp.ID, "forged-key", "forged"); !errors.Is(err, tracking.ErrNotFound) {
			t.Fatalf("cross-owner create=%v", err)
		}
		if err := s.LogRun(context.Background(), actor, run.ID, tracking.Batch{Tags: []tracking.Pair{{Key: "external.source", Value: "test"}}}); !errors.Is(err, tracking.ErrNotFound) {
			t.Fatalf("cross-owner write=%v", err)
		}
	}
	if _, err := s.GetRun(context.Background(), owner, "0123456789abcdef0123456789abcdef"); !errors.Is(err, tracking.ErrNotFound) {
		t.Fatalf("unregistered training run accepted: %v", err)
	}
	if p.logs != 0 {
		t.Fatal("unauthorized writes reached MLflow")
	}
}

func TestFinishIntentSurvivesFailureAndCannotReopen(t *testing.T) {
	s, _, p, _ := fixture(t)
	_, run := createPair(t, s)
	p.finishErr = errors.New("finish timeout")
	if _, err := s.FinishRun(context.Background(), owner, run.ID, "FINISHED"); err == nil {
		t.Fatal("ambiguous finish reported success")
	}
	if err := s.LogRun(context.Background(), owner, run.ID, tracking.Batch{Tags: []tracking.Pair{{Key: "external.source", Value: "test"}}}); !errors.Is(err, tracking.ErrConflict) {
		t.Fatalf("write after finish intent=%v", err)
	}
	if _, err := s.FinishRun(context.Background(), owner, run.ID, "FAILED"); !errors.Is(err, tracking.ErrConflict) {
		t.Fatalf("different finish accepted: %v", err)
	}
	p.finishErr = nil
	finished, err := s.FinishRun(context.Background(), owner, run.ID, "FINISHED")
	if err != nil || finished.State != "FINISHED" {
		t.Fatalf("reconcile finish=%+v %v", finished, err)
	}
	if _, err := s.FinishRun(context.Background(), owner, run.ID, "RUNNING"); !errors.Is(err, tracking.ErrInvalid) {
		t.Fatalf("reopen accepted: %v", err)
	}
	if _, err := s.FinishRun(context.Background(), owner, run.ID, "FINISHED"); err != nil || p.finishes != 2 {
		t.Fatalf("idempotent finish repeated upstream: %v %d", err, p.finishes)
	}
}

func TestMutationLeaseSerializesWithoutHoldingDatabaseTransaction(t *testing.T) {
	s, _, p, _ := fixture(t)
	_, run := createPair(t, s)
	p.beforeLog = func() {
		if _, err := s.FinishRun(context.Background(), owner, run.ID, "FINISHED"); !errors.Is(err, tracking.ErrBusy) {
			t.Errorf("concurrent finish=%v", err)
		}
	}
	if err := s.LogRun(context.Background(), owner, run.ID, tracking.Batch{Tags: []tracking.Pair{{Key: "external.source", Value: "test"}}}); err != nil {
		t.Fatal(err)
	}
	if p.finishes != 0 {
		t.Fatal("finish bypassed in-flight log lease")
	}
}

func TestSignedPaginationBindsOwnerResourceAndExpiry(t *testing.T) {
	s, _, _, now := fixture(t)
	for i := 0; i < 3; i++ {
		if _, err := s.CreateExperiment(context.Background(), owner, fmt.Sprintf("key-%d", i), fmt.Sprintf("experiment-%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	page, err := s.ListExperiments(context.Background(), owner, 1, "")
	if err != nil || len(page.Items) != 1 || page.NextCursor == "" {
		t.Fatalf("page=%+v %v", page, err)
	}
	next, err := s.ListExperiments(context.Background(), owner, 1, page.NextCursor)
	if err != nil || next.Items[0].ID == page.Items[0].ID {
		t.Fatalf("keyset repeat: %+v %v", next, err)
	}
	other := tracking.Actor{TenantID: owner.TenantID, UserID: "another"}
	if _, err := s.ListExperiments(context.Background(), other, 1, page.NextCursor); !errors.Is(err, tracking.ErrInvalid) {
		t.Fatalf("cursor not owner bound: %v", err)
	}
	if _, err := s.ListRuns(context.Background(), owner, page.Items[0].ID, 1, page.NextCursor); !errors.Is(err, tracking.ErrInvalid) {
		t.Fatalf("cursor not resource bound: %v", err)
	}
	*now = now.Add(time.Hour)
	if _, err := s.ListExperiments(context.Background(), owner, 1, page.NextCursor); !errors.Is(err, tracking.ErrInvalid) {
		t.Fatalf("expired cursor accepted: %v", err)
	}
}

func TestSuccessfulReadListsAndIdempotentCreation(t *testing.T) {
	s, _, p, _ := fixture(t)
	ctx := context.Background()
	exp, run := createPair(t, s)
	again, err := s.CreateExperiment(ctx, owner, "experiment-key-1", "Quality evaluation")
	if err != nil || again.ID != exp.ID || p.experimentCreates != 1 {
		t.Fatalf("repeat experiment=%+v %v", again, err)
	}
	againRun, err := s.CreateRun(ctx, owner, exp.ID, "run-key-1", "candidate-one")
	if err != nil || againRun.ID != run.ID || p.runCreates != 1 {
		t.Fatalf("repeat run=%+v %v", againRun, err)
	}
	if _, err := s.CreateExperiment(ctx, owner, "duplicate-name-key", "Quality evaluation"); !errors.Is(err, tracking.ErrConflict) {
		t.Fatalf("duplicate private name=%v", err)
	}
	if _, err := s.CreateRun(ctx, owner, exp.ID, "run-key-1", "different-name"); !errors.Is(err, tracking.ErrConflict) {
		t.Fatalf("changed run request=%v", err)
	}
	detail, err := s.GetRun(ctx, owner, run.ID)
	if err != nil || detail.Run.ID != run.ID || detail.Latest["loss"] != 0.5 || detail.Params["epochs"] != "5" {
		t.Fatalf("detail=%+v %v", detail, err)
	}
	encoded, err := json.Marshal(detail)
	if err != nil { t.Fatal(err) }
	var readback struct { LatestMetrics map[string]tracking.MetricPoint; Tags map[string]string }
	if err := json.Unmarshal(encoded, &readback); err != nil { t.Fatal(err) }
	if readback.LatestMetrics["loss"].TimestampMS != 2000 || readback.LatestMetrics["loss"].Step != 7 || readback.Tags["review"] != "candidate" { t.Fatalf("service dropped readback fields: %s", encoded) }
	second, err := s.CreateRun(ctx, owner, exp.ID, "run-key-2", "candidate-two")
	if err != nil {
		t.Fatal(err)
	}
	page, err := s.ListRuns(ctx, owner, exp.ID, 1, "")
	if err != nil || len(page.Items) != 1 || page.NextCursor == "" {
		t.Fatalf("run page=%+v %v", page, err)
	}
	next, err := s.ListRuns(ctx, owner, exp.ID, 1, page.NextCursor)
	if err != nil || len(next.Items) != 1 || next.Items[0].ID == page.Items[0].ID || next.NextCursor != "" {
		t.Fatalf("next run=%+v %v", next, err)
	}
	if second.ID == run.ID {
		t.Fatal("distinct requests reused public ID")
	}
	empty, err := s.ListExperiments(ctx, tracking.Actor{TenantID: "team-a", UserID: "empty-owner"}, 100, "")
	if err != nil || len(empty.Items) != 0 {
		t.Fatalf("empty private catalog=%+v %v", empty, err)
	}
}

func TestReadableUserTagPolicy(t *testing.T) {
	for _, key := range []string{"review", "purpose", "dataset.version", "metrics/summary", "train.batch_size"} {
		if !tracking.ReadableUserTag(tracking.Pair{Key: key, Value: "用户标签"}) { t.Fatalf("user key rejected: %s", key) }
	}
	for _, key := range []string{"", "invalid key", "PLATFORM.owner", "mlflow.runName", "owner_id", "provenance", "credential", "credentials.password", "internal.trace", "system.version", "systemtag", "access_token", "refresh-token", "id_token", "api.key", "access_key", "secret.key", "private_key", "custom.token", "secret", "password", "authorization", "nested/provenance"} {
		if tracking.ReadableUserTag(tracking.Pair{Key: key, Value: "hidden"}) { t.Fatalf("reserved key accepted: %s", key) }
	}
	for _, value := range []string{strings.Repeat("x", 5001), string([]byte{0xff})} {
		if tracking.ReadableUserTag(tracking.Pair{Key: "review", Value: value}) { t.Fatal("invalid user tag value accepted") }
	}
}

func TestInvalidRequestsFailBeforeUpstream(t *testing.T) {
	s, _, p, _ := fixture(t)
	ctx := context.Background()
	badID := "invalid/path"
	checks := []func() error{
		func() error { _, err := s.CreateExperiment(ctx, owner, "bad key", "name"); return err },
		func() error { _, err := s.CreateExperiment(ctx, owner, "key", " name "); return err },
		func() error { _, err := s.CreateExperiment(ctx, tracking.Actor{}, "key", "name"); return err },
		func() error { _, err := s.CreateRun(ctx, owner, badID, "key", "name"); return err },
		func() error { _, err := s.GetRun(ctx, owner, badID); return err },
		func() error { return s.LogRun(ctx, owner, badID, tracking.Batch{}) },
		func() error { return s.LogRun(ctx, owner, strings.Repeat("b", 32), tracking.Batch{}) },
		func() error { _, err := s.FinishRun(ctx, owner, badID, "FINISHED"); return err },
		func() error { _, err := s.ListExperiments(ctx, owner, 101, ""); return err },
		func() error { _, err := s.ListRuns(ctx, owner, badID, 1, ""); return err },
		func() error { _, err := s.ListExperiments(ctx, owner, 1, "tampered.cursor"); return err },
		func() error { _, err := s.ListExperiments(ctx, owner, 1, strings.Repeat("x", 2049)); return err },
	}
	for i, check := range checks {
		if err := check(); !errors.Is(err, tracking.ErrInvalid) {
			t.Fatalf("invalid case %d=%v", i, err)
		}
	}
	if p.experimentCreates+p.runCreates+p.logs+p.finishes != 0 {
		t.Fatal("invalid inputs caused remote writes")
	}
	disabled := tracking.New(nil, nil, tracking.Options{})
	if _, err := disabled.ListExperiments(ctx, owner, 1, ""); !errors.Is(err, tracking.ErrUnavailable) {
		t.Fatalf("disabled service=%v", err)
	}
}

func TestPendingRunsAndExperimentsRemainVisibleButNotWritable(t *testing.T) {
	s, store, p, now := fixture(t)
	ctx := context.Background()
	exp := tracking.Experiment{ID: strings.Repeat("a", 32), TenantID: owner.TenantID, UserID: owner.UserID, IdempotencyHash: strings.Repeat("a", 64), Name: "pending", State: "PENDING", CreatedAt: *now, UpdatedAt: *now}
	if _, _, err := store.ReserveExperiment(ctx, exp); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateRun(ctx, owner, exp.ID, "run-key", "name"); !errors.Is(err, tracking.ErrPending) {
		t.Fatalf("pending experiment accepted: %v", err)
	}
	if _, err := store.CompleteExperiment(ctx, owner, exp.ID, "1"); err != nil {
		t.Fatal(err)
	}
	run := tracking.Run{ID: strings.Repeat("b", 32), ExperimentID: exp.ID, TenantID: owner.TenantID, UserID: owner.UserID, IdempotencyHash: strings.Repeat("b", 64), Name: "pending-run", State: "PENDING", CreatedAt: *now, UpdatedAt: *now}
	if _, _, err := store.ReserveRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetRun(ctx, owner, run.ID); !errors.Is(err, tracking.ErrPending) {
		t.Fatalf("pending run read=%v", err)
	}
	if err := s.LogRun(ctx, owner, run.ID, tracking.Batch{Tags: []tracking.Pair{{Key: "external.source", Value: "test"}}}); !errors.Is(err, tracking.ErrPending) {
		t.Fatalf("pending run logged=%v", err)
	}
	if p.logs != 0 {
		t.Fatal("pending run reached MLflow")
	}
}

func TestFinishConflictReconcilesVerifiedTerminalWithoutClaimingRequestedSuccess(t *testing.T) {
	s, store, p, _ := fixture(t)
	_, run := createPair(t, s)
	ctx := context.Background()
	p.finishErr = tracking.ErrConflict
	p.snapshotStatus = "FAILED"
	actual, err := s.FinishRun(ctx, owner, run.ID, "FINISHED")
	if !errors.Is(err, tracking.ErrConflict) || actual.State != "FAILED" {
		t.Fatalf("conflict reconciliation=%+v %v", actual, err)
	}
	persisted, err := store.GetRun(ctx, owner, run.ID)
	if err != nil || persisted.State != "FAILED" || persisted.FinishStatus != "FAILED" {
		t.Fatalf("terminal reconciliation not durable: %+v %v", persisted, err)
	}
	if err := s.LogRun(ctx, owner, run.ID, tracking.Batch{Tags: []tracking.Pair{{Key: "external.source", Value: "test"}}}); !errors.Is(err, tracking.ErrConflict) {
		t.Fatalf("reconciled terminal reopened: %v", err)
	}
	if _, err := s.FinishRun(ctx, owner, run.ID, "FAILED"); err != nil || p.finishes != 1 {
		t.Fatalf("actual terminal replay=%v calls=%d", err, p.finishes)
	}
}

func TestFinishConflictDoesNotTrustUnverifiedOrRunningSnapshot(t *testing.T) {
	for _, readErr := range []error{errors.New("unverified response"), nil} {
		s, store, p, _ := fixture(t)
		_, run := createPair(t, s)
		ctx := context.Background()
		p.finishErr = tracking.ErrConflict
		p.readErr = readErr
		if readErr != nil {
			p.snapshotStatus = "FAILED"
		}
		if _, err := s.FinishRun(ctx, owner, run.ID, "FINISHED"); !errors.Is(err, tracking.ErrPending) {
			t.Fatalf("unresolved conflict=%v", err)
		}
		persisted, err := store.GetRun(ctx, owner, run.ID)
		if err != nil || persisted.State != "FINISHING" {
			t.Fatalf("unverified terminal adopted: %+v %v", persisted, err)
		}
	}
}

func TestExplicitFinishTimeIsPersistedAndRetainedAcrossRetries(t *testing.T) {
	s, store, p, now := fixture(t)
	_, run := createPair(t, s)
	ctx := context.Background()
	requested := run.StartTimeMS + 5000
	p.finishErr = errors.New("ambiguous finish")
	if _, err := s.FinishRunAt(ctx, owner, run.ID, "FINISHED", requested); !errors.Is(err, tracking.ErrPending) {
		t.Fatalf("first finish=%v", err)
	}
	intent, err := store.GetRun(ctx, owner, run.ID)
	if err != nil || intent.EndTimeMS != requested {
		t.Fatalf("end time intent=%+v %v", intent, err)
	}
	*now = now.Add(time.Hour)
	p.finishErr = nil
	finished, err := s.FinishRunAt(ctx, owner, run.ID, "FINISHED", requested+10000)
	if err != nil || finished.EndTimeMS != requested || finished.FinishedAt == nil || finished.FinishedAt.UnixMilli() != requested {
		t.Fatalf("end time changed on retry=%+v %v", finished, err)
	}
	if len(p.finishEndTimes) != 2 || p.finishEndTimes[0] != requested || p.finishEndTimes[1] != requested {
		t.Fatalf("upstream end times=%v", p.finishEndTimes)
	}
}

func TestExplicitFinishTimeRejectsInvalidRangeBeforeUpstream(t *testing.T) {
	s, _, p, _ := fixture(t)
	_, run := createPair(t, s)
	for _, end := range []int64{-1, 253402300800000, run.StartTimeMS - 1} {
		if _, err := s.FinishRunAt(context.Background(), owner, run.ID, "FINISHED", end); !errors.Is(err, tracking.ErrInvalid) {
			t.Fatalf("invalid end %d accepted: %v", end, err)
		}
	}
	if p.finishes != 0 {
		t.Fatal("invalid finish reached upstream")
	}
}
