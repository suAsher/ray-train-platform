package main

import (
 "context"
 "encoding/json"
 "errors"
 "strings"
 "testing"
 "ray-train-platform-backend/domain"
 "ray-train-platform-backend/modelevaluation"
)

type evaluationLookupStub struct { value modelevaluation.Evaluation; err error; calls int }
func(s *evaluationLookupStub)GetEvaluationByJobID(_ context.Context,_ string)(modelevaluation.Evaluation,error){s.calls++;return s.value,s.err}
func TestTrustedEvaluationRuntimeLoadsOnlyMatchingDatabaseOwnership(t *testing.T){
 job:=&domain.TrainingJob{ID:"job-1",TenantID:"team-1",UserID:"owner-1",SubmissionOrigin:domain.SubmissionOriginEvaluation}
 record:=modelevaluation.Evaluation{ID:"evaluation-1",JobID:job.ID,TenantID:job.TenantID,OwnerID:job.UserID,ModelSHA256:strings.Repeat("a",64),Dataset:modelevaluation.DatasetSnapshot{ManifestSHA256:strings.Repeat("b",64),Split:"test",SampleCount:10},Evaluator:modelevaluation.Evaluator{ID:"evaluator-1",Protocol:"model-evaluation-report/v1"},Config:json.RawMessage(`{}`),ConfigSHA256:"44136fa355b3678a1146ad16f7e8649e94fb4fc21fe77e8310c060f61caaff8a"}
 lookup:=&evaluationLookupStub{value:record}
 trusted,err:=loadTrustedEvaluationRuntime(context.Background(),job,lookup)
 if err!=nil{t.Fatal(err)}
 if lookup.calls!=1||trusted.Spec.EvaluationRuntime==nil||trusted.Spec.EvaluationRuntime.EvaluationID!=record.ID||trusted.Spec.EvaluationRuntime.ConfigJSON!="{}"{t.Fatal("trusted database runtime not loaded")}
 if job.Spec.EvaluationRuntime!=nil{t.Fatal("caller job mutated")}
 for _,mutate:=range []func(*modelevaluation.Evaluation){func(v *modelevaluation.Evaluation){v.JobID="other-job"},func(v *modelevaluation.Evaluation){v.OwnerID="other-owner"},func(v *modelevaluation.Evaluation){v.TenantID="other-team"}}{
  next:=record;mutate(&next);if _,err:=loadTrustedEvaluationRuntime(context.Background(),job,&evaluationLookupStub{value:next});err==nil{t.Fatal("foreign evaluation metadata accepted")}
 }
 if _,err:=loadTrustedEvaluationRuntime(context.Background(),job,&evaluationLookupStub{err:errors.New("database down")});err==nil{t.Fatal("lookup failure ignored")}
 ordinary:=*trusted;ordinary.SubmissionOrigin=domain.SubmissionOriginAPI
 before:=lookup.calls;normal,err:=loadTrustedEvaluationRuntime(context.Background(),&ordinary,lookup)
 if err!=nil||lookup.calls!=before||normal.Spec.EvaluationRuntime!=nil{t.Fatal("ordinary job retained injected runtime or queried evaluations")}
}

func TestEvaluationDatasetResolverAllowsZeroTrainOnlyForTrustedEvaluation(t *testing.T){
 version:=readyDatasetVersion();version.TrainSamples=0
 catalog:=&fakeDatasetManifestCatalog{version:version,bindings:[]domain.DataMountBinding{readyTenantRootBinding()}}
 resolver,err:=newPrivateDatasetManifestResolver(catalog,"ray-train/platform/datasets");if err!=nil{t.Fatal(err)}
 request:=datasetResolutionRequest();request.Evaluation=true
 mount,err:=resolver.ResolveDatasetManifestMount(context.Background(),request)
 if err!=nil||mount.TrainSamples!=0{t.Fatalf("val/test-only dataset rejected or fabricated train count: %+v %v",mount,err)}
 request.Evaluation=false;if _,err:=resolver.ResolveDatasetManifestMount(context.Background(),request);err==nil{t.Fatal("normal training accepted zero train samples")}
 request.Evaluation=true;catalog.version.TrainSamples=-1
 if _,err:=resolver.ResolveDatasetManifestMount(context.Background(),request);err==nil{t.Fatal("negative train sample count accepted")}
}
