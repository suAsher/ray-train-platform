package storagesync

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestWorkReadGrantStopsButFinalReceiptGrantSurvives(t *testing.T){
	m,r,_,_,run:=startedRun(t);ctx:=context.Background()
	spec,err:=m.GetWorkSpec(ctx,run.ID,run.Attempt,run.Generation);if err!=nil||spec.SubjectKind!="run"||len(spec.Mappings)!=1{t.Fatalf("active metadata grant: %#v %v",spec,err)}
	if _,err=m.GetWorkSpec(ctx,run.ID,run.Attempt+1,run.Generation);!errors.Is(err,ErrStaleAttempt){t.Fatal("wrong attempt obtained read grant")}
	if _,err=m.Control(ctx,"admin",run.ID,"pause");err!=nil{t.Fatal(err)}
	if _,err=m.GetWorkSpec(ctx,run.ID,run.Attempt,run.Generation);!errors.Is(err,ErrStaleAttempt){t.Fatal("stopping worker obtained new metadata grant")}
	if _,err=m.GetReportSpec(ctx,run.ID,run.Attempt,run.Generation);err!=nil{t.Fatal("stopping worker cannot report drain")}
	if action,err:=m.ControlForRun(ctx,run.ID);err!=nil||action!="PAUSE"{t.Fatalf("pause command: %q %v",action,err)}
	updated:=r.runs[run.ID];updated.State="CANCELLING";r.runs[run.ID]=updated
	if action,err:=m.ControlForRun(ctx,run.ID);err!=nil||action!="CANCEL"{t.Fatalf("cancel command: %q %v",action,err)}
	updated.State="CANCELLED";finished:=m.now();updated.FinishedAt=&finished;r.runs[run.ID]=updated
	if _,err=m.GetReportSpec(ctx,run.ID,run.Attempt,run.Generation);err!=nil{t.Fatal("exact final callback cannot replay")}
	if _,err=m.GetReportSpec(ctx,run.ID,run.Attempt,run.Generation+1);!errors.Is(err,ErrStaleAttempt){t.Fatal("different generation final grant accepted")}
	if _,err=m.GetWorkSpec(ctx,"missing",1,1);!errors.Is(err,ErrNotFound){t.Fatalf("missing work: %v",err)}
	if action,err:=m.ControlForRun(ctx,run.ID);err!=nil||action!=""{t.Fatal("terminal run produced a control command")}
}

func TestPreviewReadGrantLifetimeAndFinalReplay(t *testing.T){
	m,r,_,_,now:=fixture(t);ctx:=context.Background();p,err:=m.CreateBrowse(ctx,"admin",Location{SpaceID:"idc"},"",0);if err!=nil{t.Fatal(err)}
	if _,err=m.GetWorkSpec(ctx,p.ID,1,1);!errors.Is(err,ErrStaleAttempt){t.Fatal("queued preview authorized a reader")}
	if err=m.Reconcile(ctx);err!=nil{t.Fatal(err)}
	spec,err:=m.GetWorkSpec(ctx,p.ID,1,1);if err!=nil||spec.SubjectKind!="preview"||spec.Limit!=100{t.Fatalf("preview spec: %#v %v",spec,err)}
	if _,err=m.GetWorkSpec(ctx,p.ID,2,1);!errors.Is(err,ErrStaleAttempt){t.Fatal("preview wrong attempt accepted")}
	p=r.previews[p.ID];p.ExpiresAt=now.Add(-time.Second);r.previews[p.ID]=p
	if _,err=m.GetWorkSpec(ctx,p.ID,1,1);!errors.Is(err,ErrStaleAttempt){t.Fatal("expired preview allowed reads")}
	if _,err=m.GetReportSpec(ctx,p.ID,1,1);err!=nil{t.Fatal("expired preview cannot replay existing final receipt")}
	if _,err=m.GetReportSpec(ctx,p.ID,1,2);!errors.Is(err,ErrStaleAttempt){t.Fatal("preview final grant crossed generation")}
}

func TestMalformedWorkerReportsCannotChangeRun(t *testing.T){
	for _,tc:=range []struct{name string;change func(*Report)}{
		{"negative counters",func(r *Report){r.Progress.NetworkBytes=-1}},
		{"other manifest",func(r *Report){r.Files.Path="/work/other-run/manifest.json"}},
		{"manifest traversal",func(r *Report){r.Files.Path="/work/"+r.RunID+"/../manifest.json"}},
		{"too many files",func(r *Report){r.FileResults=make([]FileResult,1001)}},
		{"file path traversal",func(r *Report){r.FileResults=[]FileResult{{RelativePath:"../private",State:"VERIFIED"}}}},
		{"unknown file state",func(r *Report){r.FileResults=[]FileResult{{RelativePath:"file",State:"DELETED"}}}},
		{"out of range mapping",func(r *Report){r.FileResults=[]FileResult{{MappingIndex:2,RelativePath:"file",State:"VERIFIED"}}}},
		{"duplicate mapping counters",func(r *Report){r.MappingProgress=[]MappingProgress{{MappingIndex:0},{MappingIndex:0}}}},
		{"out of range mapping counters",func(r *Report){r.MappingProgress=[]MappingProgress{{MappingIndex:2}}}},
		{"drain before stop",func(r *Report){r.State="RUNNING";r.RequestsDrained=true}},
		{"wrong phase",func(r *Report){r.Phase="TRANSFER"}},
		{"unfrozen success",func(r *Report){r.Progress.ScanComplete=false}},
	}{t.Run(tc.name,func(t *testing.T){m,repo,_,_,run:=startedRun(t);report:=preflightReport(run);tc.change(&report);if err:=m.Report(context.Background(),report);err==nil{t.Fatal("invalid report accepted")};if repo.runs[run.ID].Sequence!=0||repo.runs[run.ID].ReceiptState!=""{t.Fatal("invalid callback changed run")}})}
}

func TestCumulativeReportsStayMonotonicAndFailuresAreRedacted(t *testing.T){
	m,r,_,_,run:=startedRun(t);ctx:=context.Background()
	one:=Report{WorkerID:"pod-one",RunID:run.ID,Attempt:1,Generation:1,Sequence:1,Phase:"SCANNING",State:"RUNNING",Progress:Progress{DiscoveredFiles:2}}
	if err:=m.Report(ctx,one);err!=nil{t.Fatal(err)}
	two:=one;two.Sequence=2;two.Progress.DiscoveredFiles=1;if err:=m.Report(ctx,two);!errors.Is(err,ErrInvalid){t.Fatalf("counter regression: %v",err)}
	two.Progress.DiscoveredFiles=3;if err:=m.Report(ctx,two);err!=nil{t.Fatal(err)}
	if err:=m.Report(ctx,one);err!=nil{t.Fatal("old cumulative report should be ignored")};if r.runs[run.ID].Progress.DiscoveredFiles!=3{t.Fatal("old callback rewound progress")}
	final:=two;final.Sequence=3;final.State="FAILED";final.RequestsDrained=true;final.FailureReason="bucket=private-nfs-secret token=do-not-store"
	if err:=m.Report(ctx,final);err!=nil{t.Fatal(err)};if r.runs[run.ID].FailureReason!="WORKER_FAILED"||strings.Contains(r.runs[run.ID].FailureReason,"private"){t.Fatal("worker raw error leaked")}
}

func TestValidationRejectsUnsafeSchedulesAndOverlappingMappings(t *testing.T){
	for _,schedule:=range []Schedule{{Kind:"DAILY",Timezone:"Asia/Shanghai",Time:"25:00"},{Kind:"DAILY",Timezone:"Asia/Shanghai",Time:"12:00",Weekday:2},{Kind:"WEEKLY",Timezone:"Asia/Shanghai",Time:"12:00",Weekday:7},{Kind:"INTERVAL",Timezone:"Asia/Shanghai",EveryHours:0},{Kind:"MANUAL",Timezone:"Asia/Shanghai",EveryHours:1},{Kind:"CRON",Timezone:"Asia/Shanghai"}}{if _,err:=schedule.Next(time.Now());!errors.Is(err,ErrInvalid){t.Fatalf("unsafe schedule accepted: %#v %v",schedule,err)}}
	for _,edit:=range []func(*Config){func(c *Config){c.Mode="MIRROR"},func(c *Config){c.Verification="SIZE"},func(c *Config){c.Mappings=nil},func(c *Config){c.Mappings[0].Source.SpaceID="../private"},func(c *Config){c.Mappings[0].Destination.RelativePath="../escape"},func(c *Config){c.Mappings[0].Layout="LINK"}}{c:=testConfig();edit(&c);if c.Validate()==nil{t.Fatal("unsafe configuration accepted")}}
	if _,err:=DestinationPrefix("../bad","dest","CONTENTS");err==nil{t.Fatal("unsafe source layout")};if _,err:=DestinationPrefix("source","../bad","CONTENTS");err==nil{t.Fatal("unsafe target layout")}
	a:=ResolvedLocation{Kind:"TOS",StorageID:"one",Region:"cn",Bucket:"bucket",Prefix:"a"};b:=a;b.StorageID="alias";b.Prefix="a/child"
	if err:=ValidateResolved([]ResolvedMapping{{Source:a,Destination:b}});!errors.Is(err,ErrConflict){t.Fatal("same physical bucket alias escaped recursive copy protection")}
	b.Region="other";if ValidateResolved([]ResolvedMapping{{Source:a,Destination:b}})==nil{t.Fatal("cross-region copy accepted")};b.Region="cn";b.Prefix="separate";b.Kind="IDC";if ValidateResolved([]ResolvedMapping{{Source:a,Destination:b}})==nil{t.Fatal("writable IDC target accepted")}
}

func TestPersonalPlanAndRunOwnershipCheckedInsideTransaction(t *testing.T){
	m,r,_,_,now:=fixture(t);ctx:=context.Background();config:=testConfig();config.Mappings[0].Destination.SpaceID="my-storage"
	plan,err:=m.CreatePlan(ctx,"owner","private copy",config);if err!=nil{t.Fatal(err)}
	if _,err=m.CreatePreview(ctx,"other-admin",plan.ID,plan.Revision);!errors.Is(err,ErrNotFound){t.Fatalf("cross-owner private preview: %v",err)}
	if _,err=m.UpdatePlan(ctx,"other-admin",plan.ID,plan.Revision,"stolen",false,testConfig());!errors.Is(err,ErrNotFound){t.Fatalf("cross-owner private plan update: %v",err)}
	p,err:=m.CreatePreview(ctx,"owner",plan.ID,plan.Revision);if err!=nil{t.Fatal(err)};p.State="SUCCEEDED";p.ManifestDigest="private-manifest";p.SourceFingerprint="source";p.TargetFingerprint="target";p.ExpiresAt=now.Add(time.Hour);r.previews[p.ID]=p
	// Even a forged actor-bound preview cannot bypass the current locked plan's owner.
	forged:=p;forged.ID="forged-preview";forged.Actor="other-admin";r.previews[forged.ID]=forged
	if _,err=m.Start(ctx,"other-admin",plan.ID,StartRequest{IdempotencyKey:"forged",PreviewID:forged.ID,ConfigRevision:1,ManifestDigest:forged.ManifestDigest});!errors.Is(err,ErrNotFound){t.Fatalf("cross-owner private start: %v",err)}
	run,err:=m.Start(ctx,"owner",plan.ID,StartRequest{IdempotencyKey:"owner",PreviewID:p.ID,ConfigRevision:1,ManifestDigest:p.ManifestDigest});if err!=nil{t.Fatal(err)}
	if _,err=m.Control(ctx,"other-admin",run.ID,"cancel");!errors.Is(err,ErrNotFound){t.Fatalf("cross-owner private control: %v",err)}
	if r.runs[run.ID].State!="QUEUED"||r.plans[plan.ID].Name!="private copy"{t.Fatal("denied private operation changed state")}
}
