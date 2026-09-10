package idcsync

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"ray-train-platform-backend/domain"
)

type managerRepository struct {
	connectors    []domain.IDCDataSyncConnector
	runs          []domain.IDCDataSyncRun
	previous      domain.IDCDataSyncRun
	previousFound bool
	failed        []string
}

func (r *managerRepository) CreateIDCDataSyncConnector(context.Context, domain.IDCDataSyncConnector) error {
	return nil
}
func (r *managerRepository) ListIDCDataSyncConnectors(context.Context) ([]domain.IDCDataSyncConnector, error) {
	return append([]domain.IDCDataSyncConnector(nil), r.connectors...), nil
}
func (r *managerRepository) ListIDCDataSyncRuns(context.Context, string) ([]domain.IDCDataSyncRun, error) {
	return append([]domain.IDCDataSyncRun(nil), r.runs...), nil
}
func (r *managerRepository) ListActiveIDCDataSyncRuns(context.Context) ([]domain.IDCDataSyncRun, error) {
	return append([]domain.IDCDataSyncRun(nil), r.runs...), nil
}
func (r *managerRepository) CreateIDCDataSyncRun(_ context.Context, run domain.IDCDataSyncRun) error {
	r.runs = append([]domain.IDCDataSyncRun{run}, r.runs...)
	return nil
}
func (r *managerRepository) ClaimIDCDataSyncRun(_ context.Context, id string, now time.Time) (domain.IDCDataSyncRun, bool, error) {
	run := r.runs[0]
	run.ID, run.State, run.StartedAt = id, domain.IDCDataSyncRunRunning, &now
	r.runs[0] = run
	return run, true, nil
}
func (r *managerRepository) LatestSuccessfulIDCDataSyncRun(context.Context, string) (domain.IDCDataSyncRun, bool, error) {
	return r.previous, r.previousFound, nil
}
func (r *managerRepository) FailIDCDataSyncRun(_ context.Context, id, reason string, _ time.Time) (domain.IDCDataSyncRun, error) {
	r.failed = append(r.failed, id+":"+reason)
	return domain.IDCDataSyncRun{ID: id, State: domain.IDCDataSyncRunFailed}, nil
}

type managerJobs struct {
	spec        JobSpec
	ensureErr   error
	observation JobObservation
}

func (j *managerJobs) EnsureIDCSyncJob(_ context.Context, spec JobSpec) error {
	j.spec = spec
	return j.ensureErr
}
func (j *managerJobs) ObserveIDCSyncJob(context.Context, string, string) (JobObservation, error) {
	return j.observation, nil
}

func managerForTest(t *testing.T, repository Repository, jobs JobClient, now time.Time) *Manager {
	t.Helper()
	manager, err := NewManager(repository, jobs, Options{
		Namespace: "ray-train-platform", Image: "harbor/idc@sha256:" + "a", Bucket: "training-data",
		InternalPrefix: "ray-train/platform", TosutilConfigSecret: "tosutil", SourceNFSServer: "10.0.0.1",
		SourceNFSPath: "/original", CallbackURL: "http://backend", ServiceAccountName: "idc-sync",
		WorkClaimName: "idc-sync-work", CallbackKey: []byte("0123456789abcdef"), Now: func() time.Time { return now },
		Random: func(value []byte) (int, error) {
			for index := range value {
				value[index] = byte(index + 1)
			}
			return len(value), nil
		},
		ReconcileInterval: time.Second, RunTimeout: time.Hour, CompletionGrace: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	return manager
}

func TestRequestUsesPreviousInventoryAndPersistentWorkClaim(t *testing.T) {
	now := time.Now().UTC()
	repository := &managerRepository{previousFound: true, previous: domain.IDCDataSyncRun{InventoryObjectKey: "ray-train/platform/idc-inventories/old/" + strings.Repeat("a", 64) + ".json"}}
	jobs := &managerJobs{}
	manager := managerForTest(t, repository, jobs, now)
	connector := domain.IDCDataSyncConnector{ID: "labeled", SourceRelativePath: "QP_NuScene/labeled", MirrorPrefix: "ray-train/platform/idc-mirror/labeled"}
	if _, err := manager.Request(context.Background(), connector, "admin"); err != nil {
		t.Fatal(err)
	}
	if jobs.spec.PreviousInventoryKey != repository.previous.InventoryObjectKey || jobs.spec.WorkClaimName != "idc-sync-work" {
		t.Fatalf("unexpected Job spec: %#v", jobs.spec)
	}
}

func TestRequestConvergesRunWhenJobCreationFails(t *testing.T) {
	now := time.Now().UTC()
	repository := &managerRepository{}
	jobs := &managerJobs{ensureErr: errors.New("admission rejected")}
	manager := managerForTest(t, repository, jobs, now)
	connector := domain.IDCDataSyncConnector{ID: "labeled", SourceRelativePath: "QP_NuScene/labeled", MirrorPrefix: "ray-train/platform/idc-mirror/labeled"}
	if _, err := manager.Request(context.Background(), connector, "admin"); err == nil {
		t.Fatal("expected request failure")
	}
	if len(repository.failed) != 1 {
		t.Fatalf("failed callbacks=%v", repository.failed)
	}
}

func TestReconcileConvergesFailedAndReceiptlessSucceededJobs(t *testing.T) {
	now := time.Now().UTC()
	started := now.Add(-2 * time.Minute)
	for _, test := range []struct {
		name        string
		observation JobObservation
	}{
		{name: "failed", observation: JobObservation{Exists: true, Failed: true, Reason: "backoff limit reached"}},
		{name: "receiptless", observation: JobObservation{Exists: true, Succeeded: true}},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository := &managerRepository{runs: []domain.IDCDataSyncRun{{ID: "run-1", State: domain.IDCDataSyncRunRunning, StartedAt: &started}}}
			manager := managerForTest(t, repository, &managerJobs{observation: test.observation}, now)
			if err := manager.reconcile(context.Background()); err != nil {
				t.Fatal(err)
			}
			if len(repository.failed) != 1 {
				t.Fatalf("run did not converge: %v", repository.failed)
			}
		})
	}
}
