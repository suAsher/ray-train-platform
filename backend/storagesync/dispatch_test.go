package storagesync

import (
	"context"
	"errors"
	"sort"
	"testing"
	"time"
)

type newestFirstRepo struct{*memoryRepo}
func(r newestFirstRepo)ListRuns(ctx context.Context,id string)([]Run,error){runs,err:=r.memoryRepo.ListRuns(ctx,id);sort.Slice(runs,func(i,j int)bool{return runs[i].CreatedAt.After(runs[j].CreatedAt)});return runs,err}

func TestGlobalDispatchPrefersOldestQueuedRun(t *testing.T){
	m,r,j,_,now:=fixture(t);m.repo=newestFirstRepo{r};ctx:=context.Background();var ids []string
	for index,destination:=range []string{"first","second"}{
		instant:=now.Add(time.Duration(index)*time.Minute);m.options.Now=func()time.Time{return instant};config:=testConfig();config.Mappings[0].Destination.RelativePath=destination
		plan,err:=m.CreatePlan(ctx,"admin",destination,config);if err!=nil{t.Fatal(err)};p,err:=m.CreatePreview(ctx,"admin",plan.ID,plan.Revision);if err!=nil{t.Fatal(err)};p.State="SUCCEEDED";p.ManifestDigest="manifest";p.SourceFingerprint="source";p.TargetFingerprint="target";r.previews[p.ID]=p
		run,err:=m.Start(ctx,"admin",plan.ID,StartRequest{IdempotencyKey:destination,PreviewID:p.ID,ConfigRevision:1,ManifestDigest:p.ManifestDigest});if err!=nil{t.Fatal(err)};ids=append(ids,run.ID)
	}
	if err:=m.Reconcile(ctx);err!=nil{t.Fatal(err)};if len(j.specs)!=1||j.specs[0].RunID!=ids[0]||r.runs[ids[1]].State!="QUEUED"{t.Fatal("new submission jumped an older queued run")}
}

type lostEnsureJobs struct{*fakeJobs;lost bool}
func(j *lostEnsureJobs)Ensure(ctx context.Context,s WorkSpec)(Observation,error){observation,err:=j.fakeJobs.Ensure(ctx,s);if !j.lost{j.lost=true;return Observation{},errors.New("create response lost")};return observation,err}

func TestLostJobCreateResponseNeverMakesRunUnstarted(t *testing.T){
	m,r,j,_,now:=fixture(t);jobs:=&lostEnsureJobs{fakeJobs:j};m.jobs=jobs;ctx:=context.Background();p,v:=readyPreview(t,m,r,now)
	run,err:=m.Start(ctx,"admin",p.ID,StartRequest{IdempotencyKey:"one",PreviewID:v.ID,ConfigRevision:1,ManifestDigest:v.ManifestDigest});if err!=nil{t.Fatal(err)};run.Phase="TRANSFER";r.runs[run.ID]=run
	if err=m.Reconcile(ctx);err==nil{t.Fatal("lost create response not reported")};run=r.runs[run.ID];if run.State!="RUNNING"||run.JobUID!=""{t.Fatal("dispatch intent did not survive response loss")}
	if err=m.Claim(ctx,run.ID,1,1,"pod-one");err!=nil{t.Fatal(err)};stopping,err:=m.Control(ctx,"admin",run.ID,"cancel");if err!=nil{t.Fatal(err)};if stopping.State!="CANCELLING"||stopping.StopVerified{t.Fatal("cancel assumed an unstarted writer")}
	if err=m.Reconcile(ctx);err!=nil{t.Fatal(err)};if len(r.locks[run.ID])==0||j.stops==0{t.Fatal("lost response bypassed stop gate")}
	final:=Report{WorkerID:"pod-one",RunID:run.ID,Attempt:1,Generation:1,Sequence:1,Phase:"TRANSFER",State:"CANCELLED",RequestsDrained:true}
	if err=m.Report(ctx,final);err!=nil{t.Fatal(err)};j.observation=Observation{Exists:true,Terminated:true,JobUID:"job-1"};if err=m.Reconcile(ctx);err!=nil{t.Fatal(err)};if r.runs[run.ID].State!="CANCELLED"||len(r.locks[run.ID])!=0{t.Fatal("verified cancellation did not complete")}
}

func TestResumeAndRetryRequireFreshCheckpointAndRetainAudit(t *testing.T){
	for _,action:=range []string{"resume","retry"}{t.Run(action,func(t *testing.T){
		m,r,j,_,run:=startedRun(t);ctx:=context.Background();run.StopVerified=true;run.State="PAUSED";if action=="retry"{run.State="FAILED";finished:=m.now();run.FinishedAt=&finished};r.runs[run.ID]=run
		old:=run;old.RecoverableUntil=m.now().Add(-time.Second);r.runs[run.ID]=old;if _,err:=m.Control(ctx,"admin",run.ID,action);!errors.Is(err,ErrConflict){t.Fatal("expired checkpoint resumed")};r.runs[run.ID]=run
		updated,err:=m.Control(ctx,"taking-admin",run.ID,action);if err!=nil{t.Fatal(err)};if updated.Attempt!=2||updated.Generation!=2||updated.WorkerID!=""||updated.RequestedBy!="admin"||updated.AuthorizedBy!="taking-admin"||updated.FinishedAt!=nil{t.Fatal("resume lost audit or reused executor identity")}
		j.observation=Observation{Exists:true,Running:true,JobUID:"job-2"};if err=m.Reconcile(ctx);err!=nil{t.Fatal(err)};if runActor(r.runs[run.ID])!="taking-admin"{t.Fatal("takeover authorization not used")}
	})}
}

func TestLongPreviewHeartbeatRenewsScanLeaseAndStartsFreshResultTTL(t *testing.T){
	m,r,j,_,now:=fixture(t);m.options.PreviewTTL=15*time.Minute;current:=now;m.options.Now=func()time.Time{return current};ctx:=context.Background()
	plan,err:=m.CreatePlan(ctx,"admin","long scan",testConfig());if err!=nil{t.Fatal(err)};p,err:=m.CreatePreview(ctx,"admin",plan.ID,1);if err!=nil{t.Fatal(err)};initialExpiry:=p.ExpiresAt
	if err=m.Reconcile(ctx);err!=nil{t.Fatal(err)};if err=m.Claim(ctx,p.ID,1,1,"pod-one");err!=nil{t.Fatal(err)}
	current=now.Add(10*time.Minute);report:=Report{WorkerID:"pod-one",RunID:p.ID,PreviewID:p.ID,Attempt:1,Generation:1,Sequence:1,Phase:"SCANNING",State:"RUNNING",Progress:Progress{DiscoveredFiles:2}}
	if err=m.Report(ctx,report);err!=nil{t.Fatal(err)};if !r.previews[p.ID].ExpiresAt.After(initialExpiry){t.Fatal("active scan heartbeat did not renew its lease")}
	current=now.Add(20*time.Minute);report.Sequence=2;report.State="SUCCEEDED";report.Phase="PLANNING";report.ManifestDigest="manifest";report.SourceFingerprint="source";report.TargetFingerprint="target";report.RequestsDrained=true;report.Progress=Progress{DiscoveredFiles:2,SourceFiles:2,TransferFiles:2,ScanComplete:true}
	if err=m.Report(ctx,report);err!=nil{t.Fatalf("long scan final report after initial TTL rejected: %v",err)};j.observation=Observation{Exists:true,Terminated:true,JobUID:"job-1"};if err=m.Reconcile(ctx);err!=nil{t.Fatal(err)};p=r.previews[p.ID]
	if !p.ExpiresAt.After(current.Add(14*time.Minute)){t.Fatal("completed preview did not receive a fresh acceptance TTL")}
	if _,err=m.Start(ctx,"admin",plan.ID,StartRequest{IdempotencyKey:"long",PreviewID:p.ID,ConfigRevision:1,ManifestDigest:p.ManifestDigest});err!=nil{t.Fatalf("fresh completed preview cannot start: %v",err)}
}

func TestCheckpointRetentionStartsAfterVerifiedStop(t *testing.T){
	m,r,j,_,run:=startedRun(t);ctx:=context.Background();run.Phase="TRANSFER";r.runs[run.ID]=run;later:=run.CreatedAt.Add(8*24*time.Hour);m.options.Now=func()time.Time{return later}
	if _,err:=m.Control(ctx,"admin",run.ID,"pause");err!=nil{t.Fatal(err)}
	if err:=m.Report(ctx,Report{WorkerID:"pod-one",RunID:run.ID,Attempt:1,Generation:1,Sequence:1,Phase:"TRANSFER",State:"PAUSED",RequestsDrained:true});err!=nil{t.Fatal(err)}
	j.observation=Observation{Exists:true,Terminated:true,JobUID:"job-1"};if err:=m.Reconcile(ctx);err!=nil{t.Fatal(err)}
	paused:=r.runs[run.ID];if !paused.RecoverableUntil.After(later){t.Fatal("long-running task's current checkpoint expired before it was paused")}
	if _,err:=m.Control(ctx,"admin",run.ID,"resume");err!=nil{t.Fatalf("fresh paused checkpoint could not resume: %v",err)}
}
