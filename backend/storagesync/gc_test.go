package storagesync

import (
    "context"
    "errors"
    "fmt"
    "testing"
)

type gcRepository struct {
    *memoryRepo
    attempts map[string]Run
    readError error
    commitError error
}
func (r *gcRepository) GetAttempt(_ context.Context, id string, attempt int) (Run, error) {
    if r.readError != nil { return Run{}, r.readError }
    run, ok := r.attempts[fmt.Sprintf("%s/%d", id, attempt)]
    if !ok { return Run{}, ErrNotFound }
    return run, nil
}
func (r *gcRepository) Transact(ctx context.Context, fn func(Tx) error) error {
    if r.commitError != nil { return r.commitError }
    return r.memoryRepo.Transact(ctx, fn)
}

type gcJobs struct {
    fakeJobs
    executors []ExecutorIdentity
    scheduled []ExecutorGCRequest
    gcError error
    beforeSchedule func(ExecutorGCRequest)
}
func (j *gcJobs) ListExecutors(context.Context) ([]ExecutorIdentity, error) {
    return j.executors, nil
}
func (j *gcJobs) ScheduleExecutorGC(_ context.Context, request ExecutorGCRequest) error {
    if j.beforeSchedule != nil { j.beforeSchedule(request) }
    if j.gcError != nil { return j.gcError }
    j.scheduled = append(j.scheduled, request)
    return nil
}
func gcFixture() (*Manager, *gcRepository, *gcJobs, Run, ExecutorIdentity) {
    old := Run{ID:"run-old", Attempt:1, Generation:2, Phase:"TRANSFER", State:"PAUSED", StopVerified:true, RequestsDrained:true, ReceiptState:"PAUSED", JobUID:"old-uid"}
    executor := ExecutorIdentity{Namespace:"old-system", Name:"storage-sync-run-old-1", SubjectKind:"run", RunID:old.ID, Attempt:1, Generation:2, Phase:"TRANSFER", JobUID:old.JobUID, Terminated:true}
    repo := &gcRepository{memoryRepo:newMemoryRepo(), attempts:map[string]Run{"run-old/1":old}}
    jobs := &gcJobs{executors:[]ExecutorIdentity{executor}}
    manager := NewManager(repo, jobs, nil, Options{})
    return manager, repo, jobs, old, executor
}

func TestExecutorGCUsesHistoricalAttemptAfterResume(t *testing.T) {
    m,r,j,old,_ := gcFixture()
    current:=old
    current.Attempt=2
    current.Generation=3
    current.JobUID="new-writer"
    current.StopVerified=false
    current.RequestsDrained=false
    current.State="RUNNING"
    current.ReceiptState=""
    r.runs[current.ID]=current
    if err:=m.collectExecutors(context.Background()); err!=nil { t.Fatal(err) }
    if len(j.scheduled)!=1 || j.scheduled[0].Executor.JobUID!="old-uid" || j.scheduled[0].TTLSeconds!=3600 { t.Fatalf("old paused attempt was not safely scheduled: %+v",j.scheduled) }
    if r.runs[current.ID].JobUID!="new-writer" || r.runs[current.ID].StopVerified { t.Fatal("GC changed current writer") }
}

func TestExecutorGCRefusesMissingOrAmbiguousStopProof(t *testing.T) {
    cases:=[]struct{name string; change func(*Run,*ExecutorIdentity)}{
        {"unverified",func(r *Run,e *ExecutorIdentity){r.StopVerified=false}},
        {"undrained",func(r *Run,e *ExecutorIdentity){r.RequestsDrained=false}},
        {"receipt recovery pending",func(r *Run,e *ExecutorIdentity){r.ReceiptState=""}},
        {"nonterminal receipt",func(r *Run,e *ExecutorIdentity){r.ReceiptState="RUNNING"}},
        {"replaced uid",func(r *Run,e *ExecutorIdentity){e.JobUID="replacement"}},
        {"wrong generation",func(r *Run,e *ExecutorIdentity){e.Generation++}},
        {"wrong phase",func(r *Run,e *ExecutorIdentity){e.Phase="PREVIEW"}},
        {"active containers",func(r *Run,e *ExecutorIdentity){e.Terminated=false}},
        {"unbound recovery helper",func(r *Run,e *ExecutorIdentity){e.Phase="RECOVER";e.JobUID="helper"}},
    }
    for _,tc:=range cases { t.Run(tc.name,func(t *testing.T){
        m,r,j,old,e:=gcFixture();tc.change(&old,&e);r.attempts["run-old/1"]=old;j.executors=[]ExecutorIdentity{e}
        if err:=m.collectExecutors(context.Background());err!=nil { t.Fatal(err) }
        if len(j.scheduled)!=0 { t.Fatal("unsafe executor received TTL") }
    })}
}

func TestExecutorGCRetentionUsesBusinessOutcome(t *testing.T) {
    for _,state:=range []string{"SUCCEEDED","PAUSED","CANCELLED","FAILED"} {t.Run(state,func(t *testing.T){
        m,r,j,old,_:=gcFixture();old.State=state;old.ReceiptState=state;r.attempts["run-old/1"]=old
        m.options.GCSucceededTTLSeconds=17;m.options.GCFailedTTLSeconds=31
        if err:=m.collectExecutors(context.Background());err!=nil {t.Fatal(err)}
        expected:=int32(17);if state=="FAILED" {expected=31}
        if len(j.scheduled)!=1||j.scheduled[0].TTLSeconds!=expected {t.Fatalf("retention=%+v",j.scheduled)}
    })}
}

func TestExecutorGCPreviewProofIsPersistedBeforeRuntimePatch(t *testing.T) {
    m,r,j,_,e:=gcFixture()
    e.SubjectKind="preview";e.Phase="BROWSE"
    j.executors=[]ExecutorIdentity{e}
    r.previews[e.RunID]=Preview{ID:e.RunID,Kind:"BROWSE",State:"SUCCEEDED",Attempt:e.Attempt,Generation:e.Generation,JobUID:e.JobUID,ReceiptState:"SUCCEEDED"}
    j.beforeSchedule=func(ExecutorGCRequest){if !r.previews[e.RunID].StopVerified {t.Fatal("TTL scheduled before stop proof was persisted")}}
    if err:=m.collectExecutors(context.Background());err!=nil {t.Fatal(err)}
    if len(j.scheduled)!=1 {t.Fatal("legacy preview was not collected")}
}

func TestExecutorGCDoesNotPatchIfPreviewProofCommitFails(t *testing.T) {
    m,r,j,_,e:=gcFixture();e.SubjectKind="preview";e.Phase="PREVIEW";j.executors=[]ExecutorIdentity{e}
    r.previews[e.RunID]=Preview{ID:e.RunID,Kind:"PREVIEW",State:"SUCCEEDED",Attempt:e.Attempt,Generation:e.Generation,JobUID:e.JobUID,ReceiptState:"SUCCEEDED"}
    r.commitError=errors.New("database unavailable")
    if err:=m.collectExecutors(context.Background());err==nil {t.Fatal("expected persistence error")}
    if len(j.scheduled)!=0 {t.Fatal("uncommitted preview proof permitted GC")}
}

func TestExecutorGCRetriesWithoutChangingBusinessState(t *testing.T) {
    m,r,j,old,_:=gcFixture();j.gcError=errors.New("API unavailable")
    if err:=m.collectExecutors(context.Background());err==nil {t.Fatal("expected error")}
    if r.attempts["run-old/1"].State!=old.State {t.Fatal("GC failure changed business state")}
    j.gcError=nil
    if err:=m.collectExecutors(context.Background());err!=nil {t.Fatal(err)}
    if len(j.scheduled)!=1 {t.Fatal("GC did not retry")}
}

func TestExecutorGCRecoveryHelperRequiresOriginalAttemptProof(t *testing.T) {
    m,_,j,old,e:=gcFixture();e.Phase="RECOVER";e.JobUID="helper";e.ParentJobUID=old.JobUID;j.executors=[]ExecutorIdentity{e}
    if err:=m.collectExecutors(context.Background());err!=nil {t.Fatal(err)}
    if len(j.scheduled)!=1 {t.Fatal("bound terminal helper was not collected")}
}
