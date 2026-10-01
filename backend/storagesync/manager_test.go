package storagesync

import (
	"context"
	"errors"
	"testing"
	"time"
)

type memoryRepo struct { plans map[string]Plan; previews map[string]Preview; runs map[string]Run; locks map[string][]PathLock }
func newMemoryRepo()*memoryRepo{return &memoryRepo{plans:map[string]Plan{},previews:map[string]Preview{},runs:map[string]Run{},locks:map[string][]PathLock{}}}
func(r *memoryRepo)Transact(_ context.Context,f func(Tx)error)error{return f((*memoryTx)(r))}
func(r *memoryRepo)ListPlans(_ context.Context)([]Plan,error){return (*memoryTx)(r).ListPlans()}
func(r *memoryRepo)GetPlan(_ context.Context,id string)(Plan,error){return (*memoryTx)(r).GetPlan(id)}
func(r *memoryRepo)GetPreview(_ context.Context,id string)(Preview,error){return (*memoryTx)(r).GetPreview(id)}
func(r *memoryRepo)GetRun(_ context.Context,id string)(Run,error){return (*memoryTx)(r).GetRun(id)}
func(r *memoryRepo)ListRuns(_ context.Context,id string)([]Run,error){return (*memoryTx)(r).ListRuns(id)}
func(r *memoryRepo)ListRunFiles(context.Context,string,string,int)(FilePage,error){return FilePage{},nil}
type memoryTx memoryRepo
func(r *memoryTx)GetPlan(id string)(Plan,error){v,ok:=r.plans[id];if !ok{return v,ErrNotFound};return v,nil}
func(r *memoryTx)PutPlan(p Plan)error{r.plans[p.ID]=p;return nil}
func(r *memoryTx)ListPlans()([]Plan,error){out:=[]Plan{};for _,p:=range r.plans{out=append(out,p)};return out,nil}
func(r *memoryTx)GetPreview(id string)(Preview,error){v,ok:=r.previews[id];if !ok{return v,ErrNotFound};return v,nil}
func(r *memoryTx)PutPreview(p Preview)error{r.previews[p.ID]=p;return nil}
func(r *memoryTx)ListPreviews()([]Preview,error){out:=[]Preview{};for _,p:=range r.previews{out=append(out,p)};return out,nil}
func(r *memoryTx)GetRun(id string)(Run,error){v,ok:=r.runs[id];if !ok{return v,ErrNotFound};return v,nil}
func(r *memoryTx)PutRun(p Run)error{r.runs[p.ID]=p;return nil}
func(r *memoryTx)ListRuns(id string)([]Run,error){out:=[]Run{};for _,p:=range r.runs{if id==""||p.PlanID==id{out=append(out,p)}};return out,nil}
func(r *memoryTx)AcquireLocks(id string,_ int,locks []PathLock)error{for owner,existing:=range r.locks{if owner==id{continue};for _,a:=range existing{for _,b:=range locks{if LocksConflict(a,b){return ErrLocked}}}};r.locks[id]=locks;return nil}
func(r *memoryTx)ReleaseLocks(id string)error{delete(r.locks,id);return nil}
func(r *memoryTx)PutFileResults(string,int,int64,[]FileResult)error{return nil}

type fakeResolver struct { denied bool; revision string }
func(r *fakeResolver)IsAuthorized(context.Context,string)error{if r.denied{return ErrForbidden};return nil}
func(r *fakeResolver)Resolve(_ context.Context,_ string,l Location)(ResolvedLocation,error){if r.denied{return ResolvedLocation{},ErrForbidden};kind:="TOS";if l.SpaceID=="idc"{kind="IDC"};return ResolvedLocation{Location:l,Kind:kind,StorageID:l.SpaceID,Region:"cn",Bucket:"bucket",Prefix:l.RelativePath,Revision:r.revision},nil}
type fakeJobs struct{ specs []WorkSpec; observation Observation; stops int }
func(j *fakeJobs)Ensure(_ context.Context,s WorkSpec)(Observation,error){j.specs=append(j.specs,s);return j.observation,nil}
func(j *fakeJobs)Observe(context.Context,string,int)(Observation,error){return j.observation,nil}
func(j *fakeJobs)Stop(context.Context,string,int)error{j.stops++;return nil}
func fixture(t *testing.T)(*Manager,*memoryRepo,*fakeJobs,*fakeResolver,time.Time){t.Helper();r:=newMemoryRepo();j:=&fakeJobs{observation:Observation{Exists:true,Running:true,JobUID:"job-1"}};resolver:=&fakeResolver{revision:"v1"};now:=time.Date(2026,10,1,0,0,0,0,time.UTC);m:=NewManager(r,j,resolver,Options{Now:func()time.Time{return now},PreviewTTL:time.Hour});return m,r,j,resolver,now}
func readyPreview(t *testing.T,m *Manager,r *memoryRepo,now time.Time)(Plan,Preview){t.Helper();p,err:=m.CreatePlan(context.Background(),"admin","copy",testConfig());if err!=nil{t.Fatal(err)};v,err:=m.CreatePreview(context.Background(),"admin",p.ID,p.Revision);if err!=nil{t.Fatal(err)};v.State="SUCCEEDED";v.ManifestDigest="sha256:manifest";v.SourceFingerprint="source";v.TargetFingerprint="target";v.ExpiresAt=now.Add(time.Hour);r.previews[v.ID]=v;return p,v}

func TestPreviewExpiredOrTargetChangedBeforeStart(t *testing.T){
	for _,change:=range []string{"expired","resolution","actor"}{t.Run(change,func(t *testing.T){m,r,j,resolver,now:=fixture(t);p,v:=readyPreview(t,m,r,now);actor:="admin";if change=="expired"{v.ExpiresAt=now.Add(-time.Second);r.previews[v.ID]=v};if change=="resolution"{resolver.revision="v2"};if change=="actor"{actor="another-admin"};_,err:=m.Start(context.Background(),actor,p.ID,StartRequest{IdempotencyKey:"one",PreviewID:v.ID,ConfigRevision:p.Revision,ManifestDigest:v.ManifestDigest});if err==nil{t.Fatal("invalid preview accepted")};for _,s:=range j.specs{if s.Phase=="TRANSFER"{t.Fatal("created writer before valid preflight")}}})}
}

func TestManualStartIdempotentAndRequiresReadOnlyRevalidation(t *testing.T){m,r,j,_,now:=fixture(t);p,v:=readyPreview(t,m,r,now);req:=StartRequest{IdempotencyKey:"same",PreviewID:v.ID,ConfigRevision:p.Revision,ManifestDigest:v.ManifestDigest};one,err:=m.Start(context.Background(),"admin",p.ID,req);if err!=nil{t.Fatal(err)};two,err:=m.Start(context.Background(),"admin",p.ID,req);if err!=nil||two.ID!=one.ID{t.Fatalf("idempotency: %#v %v",two,err)};if one.Phase!="PREVIEW"{t.Fatal("manual run bypassed revalidation")};if err:=m.Reconcile(context.Background());err!=nil{t.Fatal(err)};for _,s:=range j.specs{if s.Phase=="TRANSFER"{t.Fatal("premature writer")}}
	if _,err:=m.Start(context.Background(),"admin",p.ID,StartRequest{IdempotencyKey:"different",PreviewID:v.ID,ConfigRevision:p.Revision,ManifestDigest:v.ManifestDigest});!errors.Is(err,ErrConflict){t.Fatalf("parallel same-plan run: %v",err)}
}

func TestPartitionedWriterStillAuthorized(t *testing.T){m,r,j,_,now:=fixture(t);p,v:=readyPreview(t,m,r,now);run,err:=m.Start(context.Background(),"admin",p.ID,StartRequest{IdempotencyKey:"one",PreviewID:v.ID,ConfigRevision:p.Revision,ManifestDigest:v.ManifestDigest});if err!=nil{t.Fatal(err)};run.State="RUNNING";run.Phase="TRANSFER";run.JobUID="job-1";r.runs[run.ID]=run;r.locks[run.ID]=[]PathLock{{StorageID:"personal",Bucket:"bucket",Prefix:"sync-test",Mode:"WRITE"}};if _,err:=m.Control(context.Background(),"admin",run.ID,"pause");err!=nil{t.Fatal(err)};j.observation=Observation{};if err:=m.Reconcile(context.Background());err!=nil{t.Fatal(err)};current:=r.runs[run.ID];if current.State!="PAUSING"||len(r.locks[run.ID])==0{t.Fatal("partition released writer lock")};if _,err:=m.Control(context.Background(),"admin",run.ID,"resume");err==nil{t.Fatal("partition permitted replacement writer")}}

func TestPauseStopAndResumeLockConflict(t *testing.T){m,r,j,_,now:=fixture(t);p,v:=readyPreview(t,m,r,now);run,err:=m.Start(context.Background(),"admin",p.ID,StartRequest{IdempotencyKey:"one",PreviewID:v.ID,ConfigRevision:p.Revision,ManifestDigest:v.ManifestDigest});if err!=nil{t.Fatal(err)};run.State="PAUSING";run.Phase="TRANSFER";run.JobUID="job-1";run.RequestsDrained=true;r.runs[run.ID]=run;r.locks[run.ID]=[]PathLock{{StorageID:"personal",Region:"cn",Bucket:"bucket",Prefix:"sync-test",Mode:"WRITE"}};j.observation=Observation{Exists:true,Terminated:true,JobUID:"job-1"};if err:=m.Reconcile(context.Background());err!=nil{t.Fatal(err)};if r.runs[run.ID].State!="PAUSED"||len(r.locks[run.ID])!=0{t.Fatal("verified stop did not pause/release")};r.locks["other"]=[]PathLock{{StorageID:"personal",Region:"cn",Bucket:"bucket",Prefix:"sync-test/child",Mode:"WRITE"}};if _,err:=m.Control(context.Background(),"admin",run.ID,"resume");!errors.Is(err,ErrLocked){t.Fatalf("resume must block on overlapping writer: %v",err)};if r.runs[run.ID].State!="PAUSED"{t.Fatal("conflict changed paused state")}}

func TestAttemptFencingAndReceiptRequiredForSuccess(t *testing.T){m,r,j,_,now:=fixture(t);p,v:=readyPreview(t,m,r,now);run,err:=m.Start(context.Background(),"admin",p.ID,StartRequest{IdempotencyKey:"one",PreviewID:v.ID,ConfigRevision:p.Revision,ManifestDigest:v.ManifestDigest});if err!=nil{t.Fatal(err)};run.State="RUNNING";run.Phase="TRANSFER";run.Attempt=2;run.Generation=2;run.JobUID="job-1";r.runs[run.ID]=run;if err:=m.Report(context.Background(),Report{RunID:run.ID,Attempt:1,Generation:1,Sequence:99,State:"SUCCEEDED"});!errors.Is(err,ErrStaleAttempt){t.Fatalf("old callback: %v",err)};j.observation=Observation{Exists:true,Terminated:true,RequestsDrained:true,JobUID:"job-1"};if err:=m.Reconcile(context.Background());err!=nil{t.Fatal(err)};if r.runs[run.ID].State=="SUCCEEDED"{t.Fatal("exit zero became success without verified receipt")}}
