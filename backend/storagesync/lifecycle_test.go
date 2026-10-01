package storagesync

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func startedRun(t *testing.T)(*Manager,*memoryRepo,*fakeJobs,*fakeResolver,Run){t.Helper();m,r,j,resolver,now:=fixture(t);p,v:=readyPreview(t,m,r,now);run,err:=m.Start(context.Background(),"admin",p.ID,StartRequest{IdempotencyKey:"run",PreviewID:v.ID,ConfigRevision:p.Revision,ManifestDigest:v.ManifestDigest});if err!=nil{t.Fatal(err)};if err=m.Reconcile(context.Background());err!=nil{t.Fatal(err)};run=r.runs[run.ID];if err=m.Claim(context.Background(),run.ID,run.Attempt,run.Generation,"pod-one");err!=nil{t.Fatal(err)};return m,r,j,resolver,r.runs[run.ID]}
func preflightReport(run Run)Report{return Report{WorkerID:"pod-one",RunID:run.ID,Attempt:run.Attempt,Generation:run.Generation,Sequence:1,Phase:"PREVIEW",State:"SUCCEEDED",ManifestDigest:run.ManifestDigest,SourceFingerprint:run.SourceFingerprint,TargetFingerprint:run.TargetFingerprint,RequestsDrained:true,Progress:Progress{DiscoveredFiles:1,SourceFiles:1,SourceBytes:10,TransferFiles:1,TransferBytes:10,ScanComplete:true},Files:WorkerFileReference{Path:"/work/"+run.ID+"/manifest.json",Digest:run.ManifestDigest,Count:1}}}

func TestPlanStartsDisabledAndRevisionIsOptimistic(t *testing.T){
	m,r,_,_,_:=fixture(t);c:=testConfig();c.Schedule=Schedule{Kind:"DAILY",Timezone:"Asia/Shanghai",Time:"12:00"}
	p,err:=m.CreatePlan(context.Background(),"admin","new plan",c);if err!=nil{t.Fatal(err)};if p.Enabled||p.NextRunAt!=nil{t.Fatal("saving a plan activated a schedule")}
	c.Mappings[0].Source.RelativePath="mutated";if r.plans[p.ID].Config.Mappings[0].Source.RelativePath=="mutated"{t.Fatal("plan retained mutable input")}
	changed,err:=m.UpdatePlan(context.Background(),"other-admin",p.ID,p.Revision,"enabled",true,testConfig());if err!=nil{t.Fatal(err)};if changed.Revision!=2||changed.Owner!="other-admin"||changed.CreatedBy!="admin"{t.Fatal("revision/takeover audit incorrect")}
	if _,err=m.UpdatePlan(context.Background(),"admin",p.ID,p.Revision,"stale",false,testConfig());!errors.Is(err,ErrConflict){t.Fatalf("stale revision accepted: %v",err)}
}

func TestPlanNameLengthCountsUnicodeCharacters(t *testing.T){
	m,_,_,_,_:=fixture(t)
	plan,err:=m.CreatePlan(context.Background(),"admin",strings.Repeat("同",160),testConfig());if err!=nil{t.Fatalf("160 Chinese characters rejected: %v",err)}
	if _,err=m.UpdatePlan(context.Background(),"admin",plan.ID,plan.Revision,strings.Repeat("名",160),false,testConfig());err!=nil{t.Fatalf("unicode update rejected: %v",err)}
	if _,err=m.CreatePlan(context.Background(),"admin",strings.Repeat("同",161),testConfig());!errors.Is(err,ErrInvalid){t.Fatalf("161 characters accepted: %v",err)}
}

func TestPreflightBarrierAndVerifiedSuccess(t *testing.T){
	m,r,j,_,run:=startedRun(t);report:=preflightReport(run)
	if err:=m.Report(context.Background(),report);err!=nil{t.Fatal(err)}
	if err:=m.Report(context.Background(),report);err!=nil{t.Fatalf("identical callback replay: %v",err)}
	changed:=report;changed.TargetFingerprint="changed";if err:=m.Report(context.Background(),changed);!errors.Is(err,ErrConflict){t.Fatalf("same sequence different payload: %v",err)}
	if r.runs[run.ID].State!="RUNNING"||r.runs[run.ID].Attempt!=1{t.Fatal("receipt alone advanced job")}
	j.observation=Observation{Exists:true,Terminated:true,JobUID:"job-1"};if err:=m.Reconcile(context.Background());err!=nil{t.Fatal(err)}
	transfer:=r.runs[run.ID];if transfer.Attempt!=2||transfer.Generation!=2||transfer.State!="QUEUED"||transfer.Phase!="TRANSFER"||transfer.WorkerID!=""{t.Fatalf("preflight transition %#v",transfer)}
	j.observation=Observation{Exists:true,Running:true,JobUID:"job-2"};if err:=m.Reconcile(context.Background());err!=nil{t.Fatal(err)};transfer=r.runs[run.ID]
	if err:=m.Claim(context.Background(),run.ID,2,2,"pod-two");err!=nil{t.Fatal(err)}
	finished:=Report{WorkerID:"pod-two",RunID:run.ID,Attempt:2,Generation:2,Sequence:1,Phase:"VERIFYING",State:"SUCCEEDED",ManifestDigest:run.ManifestDigest,RequestsDrained:true,Progress:Progress{ScanComplete:true,SourceFiles:1,SourceBytes:10,TransferFiles:1,TransferBytes:10,CompletedFiles:1,CompletedBytes:10,VerifiedFiles:1,VerifiedBytes:10}}
	if err:=m.Report(context.Background(),finished);err!=nil{t.Fatal(err)};if r.runs[run.ID].State=="SUCCEEDED"{t.Fatal("write receipt before Pod termination became success")}
	j.observation=Observation{Exists:true,Terminated:true,JobUID:"job-2"};if err:=m.Reconcile(context.Background());err!=nil{t.Fatal(err)}
	end:=r.runs[run.ID];if end.State!="SUCCEEDED"||end.FinishedAt==nil||!end.StopVerified||len(r.locks[run.ID])!=0{t.Fatal("verified run did not finish/release locks")}
	if err:=m.Report(context.Background(),report);!errors.Is(err,ErrStaleAttempt){t.Fatalf("old attempt altered completed run: %v",err)}
}

func TestScheduledPreflightConflict(t *testing.T){
	m,r,j,_,now:=fixture(t);c:=testConfig();c.Schedule=Schedule{Kind:"INTERVAL",Timezone:"Asia/Shanghai",EveryHours:1};p,err:=m.CreatePlan(context.Background(),"admin","scheduled",c);if err!=nil{t.Fatal(err)};p.Enabled=true;past:=now.Add(-3*time.Hour);p.NextRunAt=&past;r.plans[p.ID]=p
	if err:=m.Reconcile(context.Background());err!=nil{t.Fatal(err)};if len(r.runs)!=1{t.Fatalf("missed slots created %d runs",len(r.runs))};var run Run;for _,v:=range r.runs{run=v}
	if run.Trigger!="SCHEDULED"||run.ScheduledAt==nil||run.Phase!="PREVIEW"{t.Fatal("schedule bypassed preflight")}
	if err:=m.Claim(context.Background(),run.ID,1,1,"pod-one");err!=nil{t.Fatal(err)}
	report:=Report{WorkerID:"pod-one",RunID:run.ID,Attempt:1,Generation:1,Sequence:1,Phase:"PREVIEW",State:"FAILED",FailureReason:"TARGET_CONFLICT",RequestsDrained:true}
	if err:=m.Report(context.Background(),report);err!=nil{t.Fatal(err)};j.observation=Observation{Exists:true,Terminated:true,JobUID:"job-1"};if err:=m.Reconcile(context.Background());err!=nil{t.Fatal(err)}
	if r.runs[run.ID].State!="FAILED"{t.Fatal("conflicted schedule not failed")};for _,spec:=range j.specs{if spec.Phase=="TRANSFER"{t.Fatal("scheduled_preflight_conflict created write executor")}}
	if !r.plans[p.ID].NextRunAt.After(now){t.Fatal("missed schedule not coalesced")}
}

func TestDuplicatePodClaimAndOldAttemptAreRejected(t *testing.T){
	m,r,_,_,run:=startedRun(t)
	if err:=m.Claim(context.Background(),run.ID,1,1,"pod-one");err!=nil{t.Fatal(err)}
	if err:=m.Claim(context.Background(),run.ID,1,1,"pod-two");!errors.Is(err,ErrStaleAttempt){t.Fatalf("same-attempt duplicate writer claimed: %v",err)}
	report:=preflightReport(run);report.WorkerID="pod-two";if err:=m.Report(context.Background(),report);!errors.Is(err,ErrStaleAttempt){t.Fatalf("other pod callback accepted: %v",err)}
	if r.runs[run.ID].Sequence!=0{t.Fatal("unclaimed worker changed state")}
}

func TestWriterTerminationWithoutDrainRetainsLocks(t *testing.T){
	m,r,j,_,run:=startedRun(t);run.Phase="TRANSFER";run.State="PAUSING";run.ReceiptState="PAUSED";run.RequestsDrained=false;r.runs[run.ID]=run
	j.observation=Observation{Exists:true,Terminated:true,RequestsDrained:true,JobUID:"job-1"};if err:=m.Reconcile(context.Background());err!=nil{t.Fatal(err)}
	if r.runs[run.ID].State!="PAUSING"||len(r.locks[run.ID])==0{t.Fatal("trusted runtime drain field without authenticated worker receipt")}
}

func TestQueuedCancelAndPauseNeverCreateExecutors(t *testing.T){
	for _,action:=range []string{"pause","cancel"}{t.Run(action,func(t *testing.T){m,r,j,_,now:=fixture(t);p,v:=readyPreview(t,m,r,now);run,err:=m.Start(context.Background(),"admin",p.ID,StartRequest{IdempotencyKey:"one",PreviewID:v.ID,ConfigRevision:p.Revision,ManifestDigest:v.ManifestDigest});if err!=nil{t.Fatal(err)};updated,err:=m.Control(context.Background(),"admin",run.ID,action);if err!=nil{t.Fatal(err)};if !updated.StopVerified||len(r.locks[run.ID])!=0{t.Fatal("unstarted executor control failed")};if err=m.Reconcile(context.Background());err!=nil{t.Fatal(err)};if len(j.specs)!=0{t.Fatal("controlled queued run started")};if _,err=m.Control(context.Background(),"admin",run.ID,action);err!=nil{t.Fatalf("repeated control not idempotent: %v",err)}})}
}

func TestScheduleOwnerRevocationFreezesFutureRuns(t *testing.T){
	m,r,_,resolver,now:=fixture(t);p,err:=m.CreatePlan(context.Background(),"admin","scheduled",testConfig());if err!=nil{t.Fatal(err)};p.Enabled=true;p.Config.Schedule=Schedule{Kind:"DAILY",Timezone:"Asia/Shanghai",Time:"12:00"};past:=now.Add(-time.Hour);p.NextRunAt=&past;r.plans[p.ID]=p;resolver.denied=true
	if err:=m.Reconcile(context.Background());err!=nil{t.Fatal(err)};if r.plans[p.ID].Enabled||r.plans[p.ID].NextRunAt!=nil||len(r.runs)!=0{t.Fatal("revoked owner schedule was not frozen")}
}

func TestBrowseAsyncBoundedAndSnapshotHidden(t *testing.T){
	m,r,j,_,_:=fixture(t);p,err:=m.CreateBrowse(context.Background(),"admin",Location{SpaceID:"idc",RelativePath:"images"},"",2);if err!=nil{t.Fatal(err)}
	if err=m.Reconcile(context.Background());err!=nil{t.Fatal(err)};if len(j.specs)!=1||j.specs[0].Phase!="BROWSE"{t.Fatal("browse not dispatched separately")}
	if err=m.Claim(context.Background(),p.ID,1,1,"pod-one");err!=nil{t.Fatal(err)}
	report:=Report{WorkerID:"pod-one",RunID:p.ID,PreviewID:p.ID,Attempt:1,Generation:1,Sequence:1,Phase:"BROWSE",State:"SUCCEEDED",RequestsDrained:true,BrowseEntries:[]BrowseEntry{{Name:"中文",RelativePath:"images/中文",Kind:"directory"}}}
	if err=m.Report(context.Background(),report);err!=nil{t.Fatal(err)};j.observation=Observation{Exists:true,Terminated:true,JobUID:"job-1"};if err=m.Reconcile(context.Background());err!=nil{t.Fatal(err)}
	if r.previews[p.ID].State!="SUCCEEDED"||r.previews[p.ID].BrowseEntries[0].Kind!="DIRECTORY"{t.Fatal("browse final result invalid")}
	if _,err=m.CreateBrowse(context.Background(),"admin",Location{SpaceID:"idc"},"",1001);!errors.Is(err,ErrInvalid){t.Fatal("unbounded browse accepted")}
}
