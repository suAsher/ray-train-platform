package storagesync

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

type recoveringJobs struct {
	*fakeJobs
	recoverySpecs []WorkSpec
	recoveryErr error
}

func (j *recoveringJobs) RecoverReceipt(_ context.Context, spec WorkSpec) error {
	j.recoverySpecs = append(j.recoverySpecs, spec)
	return j.recoveryErr
}

func TestUnrecoverableReceiptRespectsOriginalAttemptPhase(t *testing.T) {
	for _, phase := range []string{"PREVIEW", "TRANSFER"} {
		t.Run(phase, func(t *testing.T) {
			m, repo, jobs, _, run := startedRun(t)
			run.Phase = phase
			repo.runs[run.ID] = run
			recovering := &recoveringJobs{fakeJobs: jobs, recoveryErr: fmt.Errorf("receipt helper failed: %w", ErrReceiptRecoveryFailed)}
			m.jobs = recovering
			jobs.observation = Observation{Exists: true, Terminated: true, JobUID: run.JobUID}
			if err := m.Reconcile(context.Background()); !errors.Is(err, ErrReceiptRecoveryFailed) {
				t.Fatalf("terminal recovery failure was not reported: %v", err)
			}
			updated := repo.runs[run.ID]
			if updated.Attempt != run.Attempt || updated.Generation != run.Generation || updated.RequestsDrained || updated.ReceiptState != "" {
				t.Fatal("recovery failure invented a new attempt or worker receipt")
			}
			if phase == "PREVIEW" {
				if updated.State != "FAILED" || !updated.StopVerified || updated.FinishedAt == nil || len(repo.locks[run.ID]) != 0 {
					t.Fatal("terminated read-only revalidation held its plan and locks after recovery failed")
				}
			} else {
				if !updated.Active() || updated.StopVerified || updated.FinishedAt != nil || len(repo.locks[run.ID]) == 0 {
					t.Fatal("unconfirmed writer drain released locks or became recoverable")
				}
				if updated.FailureReason != "RECEIPT_UNRECOVERABLE_DRAIN_UNCONFIRMED" {
					t.Fatalf("unrecoverable writer receipt was shown as pending: %s", updated.FailureReason)
				}
				if _, err := m.Control(context.Background(), "admin", run.ID, "retry"); !errors.Is(err, ErrConflict) {
					t.Fatalf("unconfirmed writer could be retried: %v", err)
				}
			}
		})
	}
}

func TestStandalonePreviewFailsWhenReceiptCannotBeRecovered(t *testing.T) {
	m, repo, jobs, _, _ := fixture(t)
	ctx := context.Background()
	plan, err := m.CreatePlan(ctx, "admin", "copy", testConfig())
	if err != nil { t.Fatal(err) }
	preview, err := m.CreatePreview(ctx, "admin", plan.ID, plan.Revision)
	if err != nil { t.Fatal(err) }
	if err = m.Reconcile(ctx); err != nil { t.Fatal(err) }
	m.jobs = &recoveringJobs{fakeJobs: jobs, recoveryErr: ErrReceiptRecoveryFailed}
	jobs.observation = Observation{Exists: true, Terminated: true, JobUID: "job-1"}
	if err = m.Reconcile(ctx); !errors.Is(err, ErrReceiptRecoveryFailed) {
		t.Fatalf("terminal recovery failure was not reported: %v", err)
	}
	updated := repo.previews[preview.ID]
	if updated.State != "FAILED" || updated.ReceiptState != "" || updated.RequestsDrained {
		t.Fatal("unrecoverable preview remained pending or fabricated its receipt")
	}
}

func TestFinalReceiptRecoveredOnlyAfterObservedTermination(t *testing.T) {
	m, r, j, _, run := startedRun(t)
	recovering := &recoveringJobs{fakeJobs: j}
	m.jobs = recovering
	run.Phase = "TRANSFER"
	r.runs[run.ID] = run
	j.observation = Observation{}
	if err := m.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(recovering.recoverySpecs) != 0 {
		t.Fatal("missing Job incorrectly triggered recovery")
	}
	j.observation = Observation{Exists: true, Running: true, JobUID: "job-1"}
	if err := m.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(recovering.recoverySpecs) != 0 {
		t.Fatal("live writer incorrectly triggered recovery")
	}
	j.observation = Observation{Exists: true, Terminated: true, JobUID: "job-1"}
	if err := m.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(recovering.recoverySpecs) != 1 || recovering.recoverySpecs[0].Attempt != run.Attempt || r.runs[run.ID].Attempt != run.Attempt || len(r.locks[run.ID]) == 0 {
		t.Fatal("receipt recovery restarted writer or released locks")
	}
	report := Report{WorkerID: "pod-one", RunID: run.ID, Attempt: run.Attempt, Generation: run.Generation, Sequence: 1, Phase: "VERIFYING", State: "SUCCEEDED", ManifestDigest: run.ManifestDigest, RequestsDrained: true, Progress: Progress{ScanComplete: true}}
	if err := m.Report(context.Background(), report); err != nil {
		t.Fatal(err)
	}
	if err := m.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if r.runs[run.ID].State != "SUCCEEDED" {
		t.Fatal("valid replay did not finish original attempt")
	}
}

func TestQueuedPreviewStorageRevisionChangePreventsDispatch(t *testing.T) {
	m, _, j, resolver, _ := fixture(t)
	plan, err := m.CreatePlan(context.Background(), "admin", "copy", testConfig())
	if err != nil {
		t.Fatal(err)
	}
	preview, err := m.CreatePreview(context.Background(), "admin", plan.ID, plan.Revision)
	if err != nil {
		t.Fatal(err)
	}
	resolver.revision = "revoked-binding"
	if err = m.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	updated, err := m.GetPreview(context.Background(), preview.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.State != "FAILED" || len(j.specs) != 0 {
		t.Fatal("queued preview inspected stale resolved root")
	}
}

func TestReadonlyPreviewRecoversFinalReceipt(t *testing.T) {
	m, _, j, _, _ := fixture(t)
	recovering := &recoveringJobs{fakeJobs: j}
	m.jobs = recovering
	plan, err := m.CreatePlan(context.Background(), "admin", "copy", testConfig())
	if err != nil {
		t.Fatal(err)
	}
	p, err := m.CreatePreview(context.Background(), "admin", plan.ID, plan.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if err = m.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	j.observation = Observation{Exists: true, Terminated: true, JobUID: "job-1"}
	if err = m.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	updated, err := m.GetPreview(context.Background(), p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.State != "RUNNING" || len(recovering.recoverySpecs) != 1 || recovering.recoverySpecs[0].SubjectKind != "preview" {
		t.Fatal("lost preview receipt discarded instead of read-only recovery")
	}
}
