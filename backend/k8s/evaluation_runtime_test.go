package k8s

import (
 "context"
 "encoding/json"
 "errors"
 "strings"
 "testing"

 "ray-train-platform-backend/domain"
)

func evaluationRenderFixture() domain.TrainingJob {
 job := validRenderJob()
 job.ID = "job-0123456789abcdef01234567"
 job.SubmissionOrigin = domain.SubmissionOriginEvaluation
 job.Spec.TrainingEngine = domain.TrainingEngineRayTrain
 job.Spec.RayVersion = domain.RayVersionProduction
 job.Spec.Managed = domain.ManagedTrainingPolicy{MaxFailures: 0, Checkpoint: domain.CheckpointPolicy{EveryEpochs: 1, KeepLatest: 1, KeepBest: 1}}
 job.Spec.EvaluationRuntime = &domain.EvaluationRuntime{
  EvaluationID: "evaluation-1", ModelSHA256: strings.Repeat("a",64), DatasetManifestSHA256: strings.Repeat("b",64),
  DatasetSplit: "test", ConfigJSON: "{}", ConfigSHA256: "44136fa355b3678a1146ad16f7e8649e94fb4fc21fe77e8310c060f61caaff8a",
  EvaluatorID: "evaluator-1", Protocol: "model-evaluation-report/v1", DatasetSampleCount: 10,
 }
 return job
}

func TestEvaluationRuntimeOnlyInjectedIntoEvaluationWorkers(t *testing.T) {
 job := evaluationRenderFixture()
 options := testRenderOptions()
 options.TrainingEventBaseURL = "http://backend.platform.svc:8080/api/v1/internal"
 manifest, err := RenderRayJob(job, options)
 if err != nil { t.Fatal(err) }
 cluster,_,_ := nestedMap(manifest.Object,"spec","rayClusterSpec")
 head,_,_ := nestedMap(cluster,"headGroupSpec","template","spec")
 submitter,_,_ := nestedMap(manifest.Object,"spec","submitterPodTemplate","spec")
 workers,_,_ := nestedSlice(cluster,"workerGroupSpecs")
 worker,_,_ := nestedMap(workers[0].(map[string]any),"template","spec")
 encoded,_ := json.Marshal(worker)
 for _, expected := range []string{"MODEL_EVALUATION_ID","MODEL_EVALUATION_CONFIG_JSON", "MODEL_EVALUATION_DATASET_MANIFEST_SHA256", "/jobs/"+job.ID+"/model-evaluation", "RAYTRAIN_EVENT_TOKEN_FILE"} {
  if !strings.Contains(string(encoded),expected) { t.Fatalf("worker lacks %s",expected) }
 }
 for _, pod := range []map[string]any{head,submitter} { raw,_:=json.Marshal(pod); if strings.Contains(string(raw),"MODEL_EVALUATION_") { t.Fatal("evaluation env escaped worker") } }
 if strings.Contains(string(encoded),"RAYTRAIN_PAT") || strings.Contains(string(encoded),"Authorization") { t.Fatal("worker received personal credential") }
 job.SubmissionOrigin = domain.SubmissionOriginAPI
 normal,err:=RenderRayJob(job,options); if err!=nil {t.Fatal(err)}
 raw,_:=json.Marshal(normal.Object); if strings.Contains(string(raw),"MODEL_EVALUATION_") {t.Fatal("normal job got evaluation runtime")}
}

func TestEvaluationRuntimeJSONCannotSupplyTrustedFields(t *testing.T) {
 var spec domain.JobSpec
 if err:=json.Unmarshal([]byte(`{"evaluationRuntime":{"EvaluationID":"forged"}}`),&spec);err!=nil {t.Fatal(err)}
 if spec.EvaluationRuntime!=nil {t.Fatal("public JSON populated trusted runtime")}
 spec.EvaluationRuntime=evaluationRenderFixture().Spec.EvaluationRuntime
 raw,err:=json.Marshal(spec);if err!=nil {t.Fatal(err)}
 if strings.Contains(string(raw),"evaluation-1") {t.Fatal("trusted runtime leaked to serialized JobSpec")}
}

func TestEvaluationRendererFailsClosedWithoutTrustedRuntime(t *testing.T) {
 for _, mutate:=range []func(*domain.TrainingJob){
  func(job *domain.TrainingJob){job.Spec.EvaluationRuntime=nil},
  func(job *domain.TrainingJob){job.Spec.EvaluationRuntime.ConfigSHA256=strings.Repeat("0",64)},
  func(job *domain.TrainingJob){job.Spec.EvaluationRuntime.Protocol="unknown"},
  func(job *domain.TrainingJob){job.Spec.EvaluationRuntime.ModelSHA256="invalid"},
  func(job *domain.TrainingJob){job.Spec.TrainingEngine=domain.TrainingEngineRayDDP;job.Spec.RayVersion=domain.RayVersionLegacy;job.Spec.Managed=domain.ManagedTrainingPolicy{}},
 } { job:=evaluationRenderFixture();mutate(&job);if _,err:=RenderRayJob(job,testRenderOptions());err==nil {t.Fatal("untrusted evaluation runtime rendered")} }
}

type recordingEvaluationFinalizer struct { calls int; jobID string; err error }
func(f *recordingEvaluationFinalizer) FinalizeEvaluationJob(_ context.Context,job *domain.TrainingJob)error {f.calls++;f.jobID=job.ID;return f.err}
func TestTerminalEvaluationFinalizationIndependentOfMLflow(t *testing.T){
 for _,withMLflow:=range []bool{false,true}{
  job:=evaluationRenderFixture();job.ObservedState=domain.StateSucceeded
  evaluation:=&recordingEvaluationFinalizer{}
  reconciler:=NewReconciler(&memoryJobStore{job:&job},nil,testRenderOptions()).WithEvaluationFinalizer(evaluation)
  if withMLflow {reconciler.WithExperimentFinalizer(&recordingExperimentFinalizer{err:errors.New("MLflow down")})}
  err:=reconciler.processEvent(context.Background(),domain.OutboxEvent{ID:"terminal",EventType:"TRAINING_JOB_TERMINAL",Payload:[]byte(`{"job_id":"`+job.ID+`"}`)})
  if evaluation.calls!=1||evaluation.jobID!=job.ID {t.Fatal("evaluation finalization skipped")}
  if withMLflow&&err==nil {t.Fatal("MLflow failure did not retain retry")}
 }
}
func TestTerminalEvaluationFinalizationSkipsNormalAndNonterminalJobs(t *testing.T){
 for _,normal:=range []bool{false,true}{
  job:=evaluationRenderFixture();job.ObservedState=domain.StateRunning
  if normal {job.SubmissionOrigin=domain.SubmissionOriginAPI;job.ObservedState=domain.StateSucceeded}
  evaluation:=&recordingEvaluationFinalizer{}
  reconciler:=NewReconciler(&memoryJobStore{job:&job},nil,testRenderOptions()).WithEvaluationFinalizer(evaluation)
  err:=reconciler.processEvent(context.Background(),domain.OutboxEvent{ID:"terminal",EventType:"TRAINING_JOB_TERMINAL",Payload:[]byte(`{"job_id":"`+job.ID+`"}`)})
  if evaluation.calls!=0 {t.Fatal("ineligible job finalized")};if !normal&&err==nil {t.Fatal("nonterminal event accepted")}
 }
}
func TestEvaluationFinalizationFailureStillRunsMLflow(t *testing.T){
 job:=evaluationRenderFixture();job.ObservedState=domain.StateFailed
 evaluation:=&recordingEvaluationFinalizer{err:errors.New("report unavailable")}; mlflow:=&recordingExperimentFinalizer{}
 reconciler:=NewReconciler(&memoryJobStore{job:&job},nil,testRenderOptions()).WithEvaluationFinalizer(evaluation).WithExperimentFinalizer(mlflow)
 err:=reconciler.processEvent(context.Background(),domain.OutboxEvent{ID:"terminal",EventType:"TRAINING_JOB_TERMINAL",Payload:[]byte(`{"job_id":"`+job.ID+`"}`)})
 if err==nil||mlflow.jobID!=job.ID {t.Fatal("evaluation error swallowed or MLflow skipped")}
}

type runtimeReloadStore struct { *memoryJobStore; loaded *domain.TrainingJob; reads int; err error }
func(s *runtimeReloadStore)GetByID(_ context.Context,_ string)(*domain.TrainingJob,error){s.reads++;return s.loaded,s.err}
func TestEvaluationRuntimeRestoredAfterAttemptSnapshotWithoutLosingLeaseState(t *testing.T){
 loaded:=evaluationRenderFixture();loaded.ClusterAttempt=1
 current:=loaded;current.Spec.EvaluationRuntime=nil;current.ClusterAttempt=2;current.RayJobName="attempt-two"
 store:=&runtimeReloadStore{memoryJobStore:&memoryJobStore{job:&loaded},loaded:&loaded}
 reconciler:=NewReconciler(store,nil,testRenderOptions())
 restored,err:=reconciler.restoreEvaluationRuntime(context.Background(),&current)
 if err!=nil {t.Fatal(err)}
 if store.reads!=1||restored.Spec.EvaluationRuntime==nil||restored.ClusterAttempt!=2||restored.RayJobName!="attempt-two" {t.Fatal("restore lost current attempt or runtime")}
 if current.Spec.EvaluationRuntime!=nil {t.Fatal("restore mutated caller snapshot")}
 manifest,err:=RenderRayJob(*restored,testRenderOptions());if err!=nil{t.Fatal(err)}
 raw,_:=json.Marshal(manifest.Object);if !strings.Contains(string(raw),"MODEL_EVALUATION_ID"){t.Fatal("new attempt lacks evaluation env")}
 ordinary:=current;ordinary.SubmissionOrigin=domain.SubmissionOriginAPI
 if _,err:=reconciler.restoreEvaluationRuntime(context.Background(),&ordinary);err!=nil{t.Fatal(err)}
 if store.reads!=1{t.Fatal("normal job triggered extra DB lookup")}
}
func TestEvaluationRuntimeRestoreRejectsDifferentDatabaseIdentity(t *testing.T){
 for _,mutate:=range []func(*domain.TrainingJob){
  func(job *domain.TrainingJob){job.ID="other-job"},func(job *domain.TrainingJob){job.TenantID="other-team"},func(job *domain.TrainingJob){job.UserID="other-owner"},func(job *domain.TrainingJob){job.Spec.EvaluationRuntime=nil},func(job *domain.TrainingJob){job.SubmissionOrigin=domain.SubmissionOriginAPI},
 }{current:=evaluationRenderFixture();current.Spec.EvaluationRuntime=nil;loaded:=evaluationRenderFixture();mutate(&loaded)
  store:=&runtimeReloadStore{memoryJobStore:&memoryJobStore{job:&current},loaded:&loaded};reconciler:=NewReconciler(store,nil,testRenderOptions())
  if _,err:=reconciler.restoreEvaluationRuntime(context.Background(),&current);err==nil{t.Fatal("mismatched trusted runtime copied")}
 }
}
