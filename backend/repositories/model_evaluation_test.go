package repositories

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"ray-train-platform-backend/domain"
	me "ray-train-platform-backend/modelevaluation"
	ml "ray-train-platform-backend/modellifecycle"
)

func evaluationTestStore(t *testing.T) *ModelEvaluationRepository {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:" + t.Name() + "?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil { t.Fatal(err) }
	sqlDB, err := db.DB()
	if err != nil { t.Fatal(err) }
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.AutoMigrate(&modelEvaluatorRecord{}, &modelEvaluationRecord{}, &ml.Model{}, &ml.Version{}, &DatasetRecord{}, &DatasetVersionRecord{}, &JobRecord{}, &TrainingJobEventTokenRecord{}, &OutboxRecord{}, &IdempotencyRecord{}, &TenantRecord{}, &WorkspaceRecord{}); err != nil { t.Fatal(err) }
	for _, sql := range []string{
		"CREATE UNIQUE INDEX evaluation_request_unique ON model_evaluations(owner_id,tenant_id,idempotency_key)",
		"CREATE UNIQUE INDEX evaluation_job_unique ON model_evaluations(job_id)",
		"CREATE TABLE model_evaluation_audits (id TEXT PRIMARY KEY, evaluation_id TEXT, actor_id TEXT, action TEXT, created_at DATETIME)",
	} { if err := db.Exec(sql).Error; err != nil { t.Fatal(err) } }
	return NewModelEvaluationRepository(db)
}

func seedEvaluationSources(t *testing.T, s *ModelEvaluationRepository) me.Evaluation {
	t.Helper()
	ctx := context.Background()
	if s.db.Dialector.Name() == "postgres" {
		if err := s.db.Exec("INSERT INTO tenants (id,name,namespace,local_queue) VALUES ('eval-team','Evaluation','eval-test','eval-test') ON CONFLICT DO NOTHING").Error; err != nil { t.Fatal(err) }
		if err := s.db.Exec("INSERT INTO users (id,oidc_subject,username,tenant_id) VALUES ('eval-owner','eval-owner','eval-owner','eval-team') ON CONFLICT DO NOTHING").Error; err != nil { t.Fatal(err) }
	} else {
		if err:=s.db.Create(&TenantRecord{ID:"eval-team",Name:"Evaluation",Namespace:"eval-test",LocalQueue:"eval-test",GPUQuotaLimit:64,MaxPriority:"normal"}).Error;err!=nil{t.Fatal(err)}
	}
	if err:=s.db.Model(&TenantRecord{}).Where("id = ?","eval-team").Update("gpu_quota_limit",64).Error;err!=nil{t.Fatal(err)}
	model := ml.Model{ID: "eval-model", Name: "model", OwnerID: "eval-owner", TenantID: "eval-team", Revision: 1}
	if err := s.db.Create(&model).Error; err != nil { t.Fatal(err) }
	version := ml.Version{ID: "eval-version", ModelID: model.ID, Number: 1, CreatorID: "eval-owner", JobID: "training-source", FileName: "weights.pt", SourceRoot: "runs/source/output", SourceETag: "etag", RelativePath: "weights.pt", SizeBytes: 1, SHA256: strings.Repeat("a",64), State: ml.Ready, Revision: 1, IdempotencyKey: "source", RequestSHA256: strings.Repeat("b",64), Parts: []ml.Part{{Index: 0, SizeBytes: 1, SHA256: strings.Repeat("a",64)}}}
	if err := s.db.Create(&version).Error; err != nil { t.Fatal(err) }
	team := "eval-team"
	dataset := DatasetRecord{ID: "eval-data", Slug: "eval-data", Name: "Evaluation", SourceSpace: "team-shared", SourceRelativePath: "eval", OwnerTenantID: &team, Visibility: me.Team, SchemaVersion: "schema-v1"}
	if err := s.db.Create(&dataset).Error; err != nil { t.Fatal(err) }
	manifest, key := strings.Repeat("b",64), "ray-train/platform/datasets/eval-data/manifests/eval-data-v1.parquet"
	dataVersion := DatasetVersionRecord{ID: "eval-data-v1", DatasetID: dataset.ID, Version: "v1", State: "READY", SchemaVersion: "schema-v1", ManifestSHA256: &manifest, ManifestObjectKey: &key, ValSamples: 10, TestSamples: 10}
	if err := s.db.Create(&dataVersion).Error; err != nil { t.Fatal(err) }
	evaluator, err := s.CreateEvaluator(ctx, me.Evaluator{Name: "approved evaluator", OwnerID: "eval-owner", TenantID: "eval-team", ImageReference: "registry.example/eval:v1", ImageDigest: "sha256:"+strings.Repeat("c",64), GitURL: "https://example.org/eval.git", GitCommit: strings.Repeat("d",40), EntryPoint: []string{"python", "evaluate.py"}, SchemaVersion: "schema-v1", Protocol: me.Protocol})
	if err != nil { t.Fatal(err) }
	config, hash, err := me.CanonicalConfig([]byte(`{"threshold":0}`))
	if err != nil { t.Fatal(err) }
	return me.Evaluation{ID: uuid.NewString(), ModelID: model.ID, VersionID: version.ID, ModelSHA256: version.SHA256, FileName: version.FileName, Dataset: me.DatasetSnapshot{ID: dataset.ID, VersionID: dataVersion.ID, ManifestSHA256: manifest, SchemaVersion: "schema-v1", Split: "val", Sites: []string{}, SampleCount: 10, Visibility: me.Team, TenantID: "eval-team"}, Evaluator: evaluator, Config: config, ConfigSHA256: hash, Resources: domain.Resources{WorkerReplicas: 1, GPUsPerWorker: 1, CPUPerWorker: 1, MemoryPerWorker: "1Gi"}, JobSpec: domain.JobSpec{Name: "frozen-evaluation-job"}, OwnerID: "eval-owner", TenantID: "eval-team", JobID: "job-"+uuid.NewString(), IdempotencyKey: "request", RequestSHA256: strings.Repeat("e",64)}
}

func reserveEvaluationFixture(t *testing.T, s *ModelEvaluationRepository, base me.Evaluation, key string) me.Evaluation {
	t.Helper()
	base.ID, base.JobID, base.IdempotencyKey = uuid.NewString(), "job-"+uuid.NewString(), key
	e, created, err := s.ReserveEvaluation(context.Background(), base)
	if err != nil || !created { t.Fatalf("reserve created=%v error=%v", created, err) }
	return e
}

func createEvaluationJob(t *testing.T, s *ModelEvaluationRepository, e me.Evaluation) []byte {
	t.Helper()
	job := JobRecord{ID: e.JobID, TenantID: e.TenantID, UserID: e.OwnerID, Name: e.ID, SpecJSON: "{}", DesiredState: "ACTIVE", ObservedState: string(domain.StateRunning), KubernetesNS: "eval-test", TrainingEngine: string(domain.TrainingEngineRayTrain), RayVersion: "2.58.0", ClusterAttempt: 1, CleanupJSON: "{}", SubmissionOrigin: string(domain.SubmissionOriginEvaluation), ExternalSubmissionID: e.ID}
	if err := s.db.Create(&job).Error; err != nil { t.Fatal(err) }
	token := []byte(strings.ReplaceAll(e.ID,"-","")[:32])
	now := time.Now().UTC()
	if err := s.db.Create(&TrainingJobEventTokenRecord{JobID: e.JobID, TokenSHA256: trainingEventTokenDigest(token), ExpiresAt: now.Add(time.Hour), RateWindowStartedAt: now, UpdatedAt: now}).Error; err != nil { t.Fatal(err) }
	if err := s.MarkEvaluationSubmitted(context.Background(), e.ID, e.JobID); err != nil { t.Fatal(err) }
	return token
}

func evaluationReportBytes(t *testing.T, e me.Evaluation, value float64) []byte {
	t.Helper()
	raw, err := json.Marshal(me.Report{Protocol: me.Protocol, EvaluationID: e.ID, ModelSHA256: e.ModelSHA256, DatasetManifestSHA256: e.Dataset.ManifestSHA256, EvaluatorID: e.Evaluator.ID, ConfigSHA256: e.ConfigSHA256, Metrics: []me.Metric{{Name: "accuracy", Value: value, Unit: "%", Direction: me.Higher}}})
	if err != nil { t.Fatal(err) }
	return raw
}

func TestModelEvaluationReservationSnapshotACLAndCAS(t *testing.T) {
	s := evaluationTestStore(t)
	runEvaluationReservationTests(t, s)
}

func runEvaluationReservationTests(t *testing.T, s *ModelEvaluationRepository) {
	ctx := context.Background()
	base := seedEvaluationSources(t, s)
	e, created, err := s.ReserveEvaluation(ctx, base)
	if err != nil || !created { t.Fatalf("reserve %v %v", created, err) }
	retry := base
	retry.ID, retry.JobID = uuid.NewString(), "different-job"
	again, created, err := s.ReserveEvaluation(ctx, retry)
	if err != nil || created || again.ID != e.ID || again.JobID != e.JobID || again.JobSpec.Name != base.JobSpec.Name { t.Fatalf("retry changed identity %+v %v", again, err) }
	retry.RequestSHA256 = strings.Repeat("f",64)
	if _, _, err := s.ReserveEvaluation(ctx, retry); !errors.Is(err, me.ErrConflict) { t.Fatalf("conflicting retry: %v", err) }
	if _, err := s.FindEvaluationRequest(ctx, e.TenantID, e.OwnerID, e.IdempotencyKey); err != nil { t.Fatal(err) }
	for i, change := range []func(*me.Evaluation){
		func(v *me.Evaluation){v.ModelSHA256=strings.Repeat("f",64)},
		func(v *me.Evaluation){v.Dataset.ManifestSHA256=strings.Repeat("f",64)},
		func(v *me.Evaluation){v.Dataset.SampleCount++},
		func(v *me.Evaluation){v.Evaluator.GitCommit=strings.Repeat("f",40)},
	} {
		invalid:=base;invalid.ID=uuid.NewString();invalid.JobID="job-"+uuid.NewString();invalid.IdempotencyKey=fmt.Sprint("bad-snapshot-",i);change(&invalid)
		if _,_,err:=s.ReserveEvaluation(ctx,invalid);!errors.Is(err,me.ErrNotReady)&&!errors.Is(err,me.ErrConflict){t.Fatalf("changed dependency accepted: %v",err)}
	}
	if _, err := s.GetEvaluation(ctx, e.ID, "other-team", false); !errors.Is(err, me.ErrNotFound) { t.Fatalf("cross-team detail: %v", err) }
	if _, err := s.GetEvaluation(ctx, e.ID, "other-team", true); err != nil { t.Fatal(err) }
	page, err := s.ListEvaluations(ctx, me.Filter{TenantID: "other-team"})
	if err != nil || len(page.Items) != 0 { t.Fatalf("cross-team list: %+v %v", page, err) }
	page, err = s.ListEvaluations(ctx, me.Filter{TenantID: "eval-team", OwnerID: e.OwnerID, ModelID: e.ModelID, VersionID: e.VersionID})
	if err != nil || len(page.Items) != 1 { t.Fatalf("filtered list: %+v %v", page, err) }
	publicData:=DatasetRecord{ID:"public-eval-data",Slug:"public-eval-data",Name:"Public evaluation",SourceSpace:"public",SourceRelativePath:"public-eval",Visibility:me.Public,SchemaVersion:"schema-v1"}
	if err:=s.db.Create(&publicData).Error;err!=nil{t.Fatal(err)}
	publicHash,publicKey:=strings.Repeat("b",64),"ray-train/platform/datasets/public-eval-data/manifests/public-eval-v1.parquet"
	if err:=s.db.Create(&DatasetVersionRecord{ID:"public-eval-v1",DatasetID:publicData.ID,Version:"v1",State:"READY",SchemaVersion:"schema-v1",ManifestSHA256:&publicHash,ManifestObjectKey:&publicKey,ValSamples:10}).Error;err!=nil{t.Fatal(err)}
	publicRequest:=base;publicRequest.Dataset.ID=publicData.ID;publicRequest.Dataset.VersionID="public-eval-v1";publicRequest.Dataset.Visibility=me.Public;publicRequest.Dataset.TenantID=""
	publicEvaluation:=reserveEvaluationFixture(t,s,publicRequest,"public-evaluation")
	if _,err:=s.GetEvaluation(ctx,publicEvaluation.ID,"other-team",false);err!=nil{t.Fatalf("public result not shared: %v",err)}
	disabled, err := s.SetEvaluatorActive(ctx, base.Evaluator.ID, false, base.Evaluator.Revision)
	if err != nil || disabled.Active { t.Fatalf("deactivate: %+v %v", disabled, err) }
	if _, err := s.SetEvaluatorActive(ctx, base.Evaluator.ID, true, base.Evaluator.Revision); !errors.Is(err, me.ErrConflict) { t.Fatalf("stale evaluator revision: %v", err) }
	if items, err := s.ListEvaluators(ctx, false); err != nil || len(items) != 0 { t.Fatalf("active list %v %v", items, err) }
	if items, err := s.ListEvaluators(ctx, true); err != nil || len(items) != 1 { t.Fatalf("all list %v %v", items, err) }
	// Existing requests remain recoverable after source/evaluator deactivation.
	again, _, err = s.ReserveEvaluation(ctx, base)
	if err != nil || !again.Evaluator.Active { t.Fatalf("historical snapshot changed: %+v %v", again, err) }
	base.ID, base.JobID, base.IdempotencyKey = uuid.NewString(), "new-job", "new-request"
	if _, _, err := s.ReserveEvaluation(ctx, base); !errors.Is(err, me.ErrNotReady) { t.Fatalf("inactive evaluator admitted: %v", err) }
	if err := s.FailEvaluationSubmission(ctx, e.ID, "secret source path must not persist"); err != nil { t.Fatal(err) }
	failed, err := s.GetEvaluation(ctx, e.ID, "eval-team", false)
	if err != nil || failed.State != me.Failed || strings.Contains(failed.Error,"secret") { t.Fatalf("failed submission: %+v %v", failed, err) }
}

func TestModelEvaluationReportsTokensAndActualTerminalState(t *testing.T) {
	s := evaluationTestStore(t)
	runEvaluationReportTests(t, s)
}

func TestModelEvaluationCancelsJoblessReservationsAndDelegatesExistingJobs(t *testing.T) {
	s:=evaluationTestStore(t);base:=seedEvaluationSources(t,s);ctx:=context.Background()
	e:=reserveEvaluationFixture(t,s,base,"jobless")
	for i:=0;i<2;i++{done,err:=s.CancelEvaluationReservation(ctx,e.ID);if err!=nil||!done{t.Fatalf("cancel: %v %v",done,err)}}
	got,err:=s.GetEvaluation(ctx,e.ID,e.TenantID,false)
	if err!=nil||got.State!=me.Cancelled||got.ReportState!=me.ReportMissing||got.FinishedAt==nil{t.Fatalf("jobless terminal: %+v %v",got,err)}
	existing:=reserveEvaluationFixture(t,s,base,"has-job");createEvaluationJob(t,s,existing)
	if done,err:=s.CancelEvaluationReservation(ctx,existing.ID);err!=nil||done{t.Fatalf("live job must use existing cancellation: %v %v",done,err)}
	var audits int64
	if err:=s.db.Table("model_evaluation_audits").Where("evaluation_id = ? AND action = ?",e.ID,"evaluation.reservation_cancelled").Count(&audits).Error;err!=nil||audits!=1{t.Fatalf("cancel audit count=%d error=%v",audits,err)}
}

func runEvaluationReportTests(t *testing.T, s *ModelEvaluationRepository) {
	ctx := context.Background()
	base := seedEvaluationSources(t, s)
	e := reserveEvaluationFixture(t, s, base, "report")
	token := createEvaluationJob(t, s, e)
	now := time.Now().UTC()
	if _, err := s.AuthorizeEvaluationJobToken(ctx, e.JobID, []byte(strings.Repeat("x",32)), now); !errors.Is(err, me.ErrUnauthorized) { t.Fatalf("wrong token: %v", err) }
	if _, err := s.AuthorizeEvaluationJobToken(ctx, e.JobID, token, now.Add(2*time.Hour)); !errors.Is(err, me.ErrUnauthorized) { t.Fatalf("expired token: %v", err) }
	if _, err := s.AuthorizeEvaluationJobToken(ctx, e.JobID, token, now); err != nil { t.Fatal(err) }
	other:=reserveEvaluationFixture(t,s,base,"different-job-token");createEvaluationJob(t,s,other)
	if _,err:=s.AuthorizeEvaluationJobToken(ctx,other.JobID,token,now);!errors.Is(err,me.ErrUnauthorized){t.Fatalf("cross-job token accepted: %v",err)}
	if _,err:=s.StoreEvaluationReport(ctx,other.JobID,token,evaluationReportBytes(t,other,0),now);!errors.Is(err,me.ErrUnauthorized){t.Fatalf("cross-job report accepted: %v",err)}
	if err := s.FailEvaluationSubmission(ctx, e.ID, "error"); err != nil { t.Fatal(err) }
	raw := evaluationReportBytes(t, e, 0)
	for _, bad := range [][]byte{
		[]byte(strings.Replace(string(raw),e.ModelSHA256,strings.Repeat("f",64),1)),
		[]byte(strings.Replace(string(raw),`"value":0`,`"value":1e999`,1)),
		[]byte(strings.Replace(string(raw),`"value":0`,`"value":null`,1)),
	} { if _, err := s.StoreEvaluationReport(ctx, e.JobID, token, bad, now); !errors.Is(err, me.ErrInvalid) { t.Fatalf("bad report accepted: %v", err) } }
	reported, err := s.StoreEvaluationReport(ctx, e.JobID, token, raw, now)
	if err != nil || reported.ReportState != me.ReportValid || reported.State == me.Succeeded || reported.Report.Metrics[0].Value != 0 { t.Fatalf("report promoted too soon or lost zero: %+v %v", reported, err) }
	replayed, err := s.StoreEvaluationReport(ctx, e.JobID, token, raw, now)
	if err != nil || replayed.Revision != reported.Revision { t.Fatalf("report replay: %+v %v", replayed, err) }
	if _, err := s.StoreEvaluationReport(ctx, e.JobID, token, evaluationReportBytes(t,e,1), now); !errors.Is(err, me.ErrConflict) { t.Fatalf("report overwrite: %v", err) }
	// Caller says SUCCEEDED, but DB says RUNNING: no terminal promotion.
	if err := s.FinalizeEvaluationJob(ctx, &domain.TrainingJob{ID:e.JobID, ObservedState:domain.StateSucceeded}); err != nil { t.Fatal(err) }
	current, err := s.GetEvaluationByJobID(ctx,e.JobID)
	if err != nil || current.State != me.Running { t.Fatalf("stale terminal observation: %+v %v",current,err) }
	if err := s.db.Model(&JobRecord{}).Where("id = ?",e.JobID).Update("observed_state",string(domain.StateSucceeded)).Error; err != nil { t.Fatal(err) }
	for i:=0;i<2;i++ { if err:=s.FinalizeEvaluationJob(ctx,&domain.TrainingJob{ID:e.JobID});err!=nil{t.Fatal(err)} }
	current,err=s.GetEvaluationByJobID(ctx,e.JobID)
	if err!=nil||current.State!=me.Succeeded||current.FinishedAt==nil{t.Fatalf("finalize: %+v %v",current,err)}
	if _,err:=s.StoreEvaluationReport(ctx,e.JobID,token,raw,now);!errors.Is(err,me.ErrConflict){t.Fatalf("terminal report accepted: %v",err)}
	for _,state:=range []domain.State{domain.StateSucceeded,domain.StateFailed,domain.StateCanceled,domain.StateTimedOut}{
		missing:=reserveEvaluationFixture(t,s,base,fmt.Sprint("missing-",state));createEvaluationJob(t,s,missing)
		if err:=s.db.Model(&JobRecord{}).Where("id = ?",missing.JobID).Update("observed_state",string(state)).Error;err!=nil{t.Fatal(err)}
		if err:=s.FinalizeEvaluationJob(ctx,&domain.TrainingJob{ID:missing.JobID});err!=nil{t.Fatal(err)}
		got,err:=s.GetEvaluationByJobID(ctx,missing.JobID);want:=me.Failed;if state==domain.StateCanceled{want=me.Cancelled}
		if err!=nil||got.State!=want||got.ReportState!=me.ReportMissing{t.Fatalf("missing %s: %+v %v",state,got,err)}
	}
}
