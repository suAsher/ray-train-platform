package storagesync

import (
    "context"
    "errors"
    "fmt"
)

// ExecutorIdentity comes from a bounded, operator-configured namespace scan.
// Terminated includes the Job's terminal condition and all owned containers.
// Runtime mutations must recheck that observation and the exact object UID.
type ExecutorIdentity struct {
    Namespace string
    Name string
    JobUID string
    RunID string
    SubjectKind string
    Phase string
    Attempt int
    Generation int64
    ParentJobUID string
    Terminated bool
}

type ExecutorGCRequest struct {
    Executor ExecutorIdentity
    TTLSeconds int32
    State string
}

// ExecutorGarbageCollector only schedules Kubernetes TTL collection. It must
// never remove checkpoints, audit records, or infer stop proof from deletion.
type ExecutorGarbageCollector interface {
    ListExecutors(context.Context) ([]ExecutorIdentity, error)
    ScheduleExecutorGC(context.Context, ExecutorGCRequest) error
}

// AttemptReader reads the already-persisted snapshot for one exact attempt,
// including an old stopped attempt after a later attempt has resumed.
type AttemptReader interface {
    GetAttempt(context.Context, string, int) (Run, error)
}

func (m *Manager) collectExecutors(ctx context.Context) error {
    collector, ok := m.jobs.(ExecutorGarbageCollector)
    if !ok { return nil }
    executors, err := collector.ListExecutors(ctx)
    if err != nil { return err }
    var failures []error
    for _, executor := range executors {
        state, err := m.executorGCState(ctx, executor)
        if err != nil {
            failures = append(failures, err)
            continue
        }
        if state == "" { continue }
        ttl := m.options.GCSucceededTTLSeconds
        if state == "FAILED" { ttl = m.options.GCFailedTTLSeconds }
        if err := collector.ScheduleExecutorGC(ctx, ExecutorGCRequest{Executor:executor, TTLSeconds:ttl, State:state}); err != nil {
            failures = append(failures, fmt.Errorf("schedule storage sync executor retention: %w", err))
        }
    }
    return errors.Join(failures...)
}

func (m *Manager) executorGCState(ctx context.Context, executor ExecutorIdentity) (string,error) {
    if !executor.Terminated || executor.JobUID == "" || executor.RunID == "" || executor.Attempt < 1 || executor.Generation < 1 { return "",nil }
    if executor.SubjectKind == "preview" { return m.previewGCState(ctx, executor) }
    if executor.SubjectKind != "run" { return "",nil }
    reader, ok := m.repo.(AttemptReader)
    if !ok { return "", errors.New("storage sync durable attempt reader unavailable") }
    run, err := reader.GetAttempt(ctx, executor.RunID, executor.Attempt)
    if errors.Is(err,ErrNotFound) { return "",nil }
    if err != nil { return "",err }
    if run.ID != executor.RunID || run.Attempt != executor.Attempt || run.Generation != executor.Generation || !run.StopVerified || !terminalReceipt(run.ReceiptState) { return "",nil }
    if !executorMatchesProof(executor, run.JobUID, run.Phase) { return "",nil }
    if run.Phase == "TRANSFER" && !run.RequestsDrained { return "",nil }
    if run.Phase != "TRANSFER" && run.Phase != "PREVIEW" { return "",nil }
    if run.State == "FAILED" || run.ReceiptState == "FAILED" { return "FAILED",nil }
    if run.State == "PAUSED" || run.State == "CANCELLED" { return run.State,nil }
    // PREVIEW success is persisted while the run still says RUNNING, before
    // the next TRANSFER attempt is created in that same database transaction.
    return run.ReceiptState,nil
}

func executorMatchesProof(executor ExecutorIdentity, jobUID, phase string) bool {
    if jobUID == "" { return false }
    if executor.Phase == "RECOVER" {
        return executor.ParentJobUID == jobUID && executor.JobUID != jobUID
    }
    return executor.JobUID == jobUID && executor.Phase == phase
}

func (m *Manager) previewGCState(ctx context.Context, executor ExecutorIdentity) (string,error) {
    var state string
    err := m.repo.Transact(ctx,func(tx Tx) error {
        preview, err := tx.GetPreview(executor.RunID)
        if errors.Is(err,ErrNotFound) { return nil }
        if err != nil { return err }
        if preview.ID != executor.RunID || preview.Attempt != executor.Attempt || preview.Generation != executor.Generation ||
            (preview.Kind != "BROWSE" && preview.Kind != "PREVIEW" && preview.Kind != "REVALIDATE") ||
            !executorMatchesProof(executor,preview.JobUID,preview.Kind) { return nil }
        if preview.State != "SUCCEEDED" && preview.State != "FAILED" { return nil }
        if preview.State == "SUCCEEDED" && preview.ReceiptState != "SUCCEEDED" { return nil }
        // Legacy preview snapshots lack StopVerified. A terminal read-only
        // executor can establish it independently; a recovery helper cannot
        // establish that the original executor stopped.
        if !preview.StopVerified {
            if executor.Phase == "RECOVER" { return nil }
            updated := preview
            updated.StopVerified = true
            if err := tx.PutPreview(updated); err != nil { return err }
        }
        state = preview.State
        return nil
    })
    return state,err
}
