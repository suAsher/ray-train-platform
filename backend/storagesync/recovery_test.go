package storagesync

import (
	"context"
	"testing"
)

type recoveringJobs struct {
	*fakeJobs
	recoverySpecs []WorkSpec
}

func (j *recoveringJobs) RecoverReceipt(_ context.Context, spec WorkSpec) error {
	j.recoverySpecs = append(j.recoverySpecs, spec)
	return nil
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
