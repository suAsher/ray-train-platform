package mlflowtracking_test

import (
	"context"
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
	experimentCreates,runCreates,logs,finishes int
	experiments map[string]string
	runs map[string]string
	createErr,finishErr error
	beforeLog func()
}

func (p *testProvider) CreateExperiment(_ context.Context,op string)(string,error) {p.experimentCreates++;id:=fmt.Sprint(p.experimentCreates);p.experiments[op]=id;return id,p.createErr}
func (p *testProvider) FindExperiment(_ context.Context,op string)(string,bool,error) {id,ok:=p.experiments[op];return id,ok,nil}
func (p *testProvider) CreateRun(_ context.Context,exp,op,name string)(string,error) {p.runCreates++;id:=fmt.Sprintf("%032x",p.runCreates);p.runs[op]=id;return id,p.createErr}
func (p *testProvider) FindRun(_ context.Context,exp,op string)(string,bool,error) {id,ok:=p.runs[op];return id,ok,nil}
func (p *testProvider) ReadRun(context.Context,string,string,string)(tracking.Snapshot,error) {return tracking.Snapshot{Status:"RUNNING",Latest:map[string]float64{"loss":0.5},Params:map[string]string{"epochs":"5"}},nil}
func (p *testProvider) LogRun(context.Context,string,string,string,tracking.Batch)error {p.logs++;if p.beforeLog!=nil {p.beforeLog()};return nil}
func (p *testProvider) FinishRun(context.Context,string,string,string,string)error {p.finishes++;return p.finishErr}

func fixture(t *testing.T)(*tracking.Service,*repositories.MLflowTrackingStore,*testProvider,*time.Time) {
	t.Helper()
	database,err:=gorm.Open(sqlite.Open(filepath.Join(t.TempDir(),"tracking.db")+"?_busy_timeout=5000&_journal_mode=WAL"),&gorm.Config{Logger:logger.Default.LogMode(logger.Silent)})
	if err!=nil {t.Fatal(err)}
	if err:=database.AutoMigrate(&repositories.MLflowTrackingExperimentRecord{},&repositories.MLflowTrackingRunRecord{});err!=nil {t.Fatal(err)}
	store:=repositories.NewMLflowTrackingStore(database)
	provider:=&testProvider{experiments:map[string]string{},runs:map[string]string{}}
	now:=time.Date(2026,9,12,12,0,0,0,time.UTC)
	service:=tracking.New(store,provider,tracking.Options{CursorKey:[]byte(strings.Repeat("k",32)),Now:func()time.Time{return now}})
	return service,store,provider,&now
}

var owner=tracking.Actor{TenantID:"team-a",UserID:"owner-a"}

func createPair(t *testing.T,s *tracking.Service)(tracking.Experiment,tracking.Run) {
	t.Helper()
	exp,err:=s.CreateExperiment(context.Background(),owner,"experiment-key-1","Quality evaluation");if err!=nil {t.Fatal(err)}
	run,err:=s.CreateRun(context.Background(),owner,exp.ID,"run-key-1","candidate-one");if err!=nil {t.Fatal(err)}
	return exp,run
}

func TestCreationIdempotencyDurablyReconcilesAmbiguousResults(t *testing.T) {
	s,store,p,_:=fixture(t)
	p.createErr=errors.New("upstream timeout after accepting create")
	if _,err:=s.CreateExperiment(context.Background(),owner,"experiment-key-1","Quality evaluation");!errors.Is(err,tracking.ErrPending) {t.Fatalf("ambiguous create=%v",err)}
	restarted:=tracking.New(store,p,tracking.Options{CursorKey:[]byte(strings.Repeat("k",32))})
	exp,err:=restarted.CreateExperiment(context.Background(),owner,"experiment-key-1","Quality evaluation")
	if err!=nil||exp.UpstreamID==""||p.experimentCreates!=1 {t.Fatalf("replay duplicated experiment: %+v %v creates=%d",exp,err,p.experimentCreates)}
	if _,err:=restarted.CreateExperiment(context.Background(),owner,"experiment-key-1","Changed payload");!errors.Is(err,tracking.ErrConflict) {t.Fatalf("changed replay accepted: %v",err)}
	if _,err:=s.CreateRun(context.Background(),owner,exp.ID,"run-key-1","candidate-one");!errors.Is(err,tracking.ErrPending) {t.Fatalf("ambiguous run=%v",err)}
	run,err:=restarted.CreateRun(context.Background(),owner,exp.ID,"run-key-1","candidate-one")
	if err!=nil||run.UpstreamID==""||p.runCreates!=1 {t.Fatalf("replay duplicated run: %+v %v creates=%d",run,err,p.runCreates)}
}

func TestUnresolvedCreateNeverBlindlyCreatesAgain(t *testing.T) {
	s,_,p,_:=fixture(t)
	p.createErr=errors.New("unknown outcome")
	_,_=s.CreateExperiment(context.Background(),owner,"unknown-key","Uncertain experiment")
	p.experiments=map[string]string{}
	for i:=0;i<3;i++ {if _,err:=s.CreateExperiment(context.Background(),owner,"unknown-key","Uncertain experiment");!errors.Is(err,tracking.ErrPending) {t.Fatalf("missing reconcile=%v",err)}}
	if p.experimentCreates!=1 {t.Fatal("unknown create was replayed")}
}

func TestPrivateOwnershipAndExternalRecordsAreAuthoritative(t *testing.T) {
	s,_,p,_:=fixture(t)
	exp,run:=createPair(t,s)
	for _,actor:=range []tracking.Actor{{TenantID:"team-a",UserID:"another"},{TenantID:"team-b",UserID:"owner-a"}} {
		if _,err:=s.GetRun(context.Background(),actor,run.ID);!errors.Is(err,tracking.ErrNotFound) {t.Fatalf("cross-owner read=%v",err)}
		if _,err:=s.CreateRun(context.Background(),actor,exp.ID,"forged-key","forged");!errors.Is(err,tracking.ErrNotFound) {t.Fatalf("cross-owner create=%v",err)}
		if err:=s.LogRun(context.Background(),actor,run.ID,tracking.Batch{Tags:[]tracking.Pair{{Key:"external.source",Value:"test"}}});!errors.Is(err,tracking.ErrNotFound) {t.Fatalf("cross-owner write=%v",err)}
	}
	if _,err:=s.GetRun(context.Background(),owner,"0123456789abcdef0123456789abcdef");!errors.Is(err,tracking.ErrNotFound) {t.Fatalf("unregistered training run accepted: %v",err)}
	if p.logs!=0 {t.Fatal("unauthorized writes reached MLflow")}
}

func TestFinishIntentSurvivesFailureAndCannotReopen(t *testing.T) {
	s,_,p,_:=fixture(t)
	_,run:=createPair(t,s)
	p.finishErr=errors.New("finish timeout")
	if _,err:=s.FinishRun(context.Background(),owner,run.ID,"FINISHED");err==nil {t.Fatal("ambiguous finish reported success")}
	if err:=s.LogRun(context.Background(),owner,run.ID,tracking.Batch{Tags:[]tracking.Pair{{Key:"external.source",Value:"test"}}});!errors.Is(err,tracking.ErrConflict) {t.Fatalf("write after finish intent=%v",err)}
	if _,err:=s.FinishRun(context.Background(),owner,run.ID,"FAILED");!errors.Is(err,tracking.ErrConflict) {t.Fatalf("different finish accepted: %v",err)}
	p.finishErr=nil
	finished,err:=s.FinishRun(context.Background(),owner,run.ID,"FINISHED");if err!=nil||finished.State!="FINISHED" {t.Fatalf("reconcile finish=%+v %v",finished,err)}
	if _,err:=s.FinishRun(context.Background(),owner,run.ID,"RUNNING");!errors.Is(err,tracking.ErrInvalid) {t.Fatalf("reopen accepted: %v",err)}
	if _,err:=s.FinishRun(context.Background(),owner,run.ID,"FINISHED");err!=nil||p.finishes!=2 {t.Fatalf("idempotent finish repeated upstream: %v %d",err,p.finishes)}
}

func TestMutationLeaseSerializesWithoutHoldingDatabaseTransaction(t *testing.T) {
	s,_,p,_:=fixture(t)
	_,run:=createPair(t,s)
	p.beforeLog=func(){
		if _,err:=s.FinishRun(context.Background(),owner,run.ID,"FINISHED");!errors.Is(err,tracking.ErrBusy) {t.Errorf("concurrent finish=%v",err)}
	}
	if err:=s.LogRun(context.Background(),owner,run.ID,tracking.Batch{Tags:[]tracking.Pair{{Key:"external.source",Value:"test"}}});err!=nil {t.Fatal(err)}
	if p.finishes!=0 {t.Fatal("finish bypassed in-flight log lease")}
}

func TestSignedPaginationBindsOwnerResourceAndExpiry(t *testing.T) {
	s,_,_,now:=fixture(t)
	for i:=0;i<3;i++ {if _,err:=s.CreateExperiment(context.Background(),owner,fmt.Sprintf("key-%d",i),fmt.Sprintf("experiment-%d",i));err!=nil {t.Fatal(err)}}
	page,err:=s.ListExperiments(context.Background(),owner,1,"");if err!=nil||len(page.Items)!=1||page.NextCursor=="" {t.Fatalf("page=%+v %v",page,err)}
	next,err:=s.ListExperiments(context.Background(),owner,1,page.NextCursor);if err!=nil||next.Items[0].ID==page.Items[0].ID {t.Fatalf("keyset repeat: %+v %v",next,err)}
	other:=tracking.Actor{TenantID:owner.TenantID,UserID:"another"}
	if _,err:=s.ListExperiments(context.Background(),other,1,page.NextCursor);!errors.Is(err,tracking.ErrInvalid) {t.Fatalf("cursor not owner bound: %v",err)}
	if _,err:=s.ListRuns(context.Background(),owner,page.Items[0].ID,1,page.NextCursor);!errors.Is(err,tracking.ErrInvalid) {t.Fatalf("cursor not resource bound: %v",err)}
	*now=now.Add(time.Hour)
	if _,err:=s.ListExperiments(context.Background(),owner,1,page.NextCursor);!errors.Is(err,tracking.ErrInvalid) {t.Fatalf("expired cursor accepted: %v",err)}
}
