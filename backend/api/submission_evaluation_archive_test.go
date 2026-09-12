package api

import (
 "context"
 "strings"
 "testing"
 "ray-train-platform-backend/domain"
)
func TestEvaluationArchiveSubmissionPreservesFrozenSourceWithoutPersonalArtifactLookup(t *testing.T){
 repository:=&submissionServiceRepository{}
 service:=evaluationSubmissionService(repository,func()(string,error){t.Fatal("reserved job id not used");return "",nil})
 spec:=evaluationSubmissionSpec();spec.Source=domain.CodeSource{Type:"evaluation-archive",ArtifactID:strings.Repeat("c",32),ArtifactSHA256:strings.Repeat("d",64)}
 input:=SubmissionInput{Principal:streamingPrincipal(),Spec:spec,Origin:domain.SubmissionOriginEvaluation,IdempotencyKey:"evaluation-code",ExternalSubmissionID:"evaluation-1",ReservedJobID:"job-0123456789abcdef01234567",ExpectedImageDigest:strings.Repeat("a",64),ExpectedDatasetManifestSHA256:strings.Repeat("b",64)}
 if _,err:=service.Preflight(context.Background(),input);err!=nil{t.Fatal(err)}
 job,err:=service.Submit(context.Background(),input);if err!=nil{t.Fatal(err)}
 if job.Spec.Source!=spec.Source||job.SourceArtifactID!=""{t.Fatal("independent code source was rewritten as a personal artifact")}
}
func TestPublicSubmissionsCannotSelectEvaluationArchive(t *testing.T){
 for _,origin:=range []domain.SubmissionOrigin{domain.SubmissionOriginAPI,domain.SubmissionOriginPortal,domain.SubmissionOriginRayCLI}{
  spec:=evaluationSubmissionSpec();spec.Source=domain.CodeSource{Type:"evaluation-archive",ArtifactID:strings.Repeat("c",32),ArtifactSHA256:strings.Repeat("d",64)}
  if _,err:=normalizeSubmissionSpec(streamingPrincipal(),origin,spec,LocalCachePolicy{});err==nil{t.Fatalf("ordinary origin %s accepted evaluation code",origin)}
 }
}
func TestEvaluationArchiveSourceRequiresOnlyFrozenIDAndSHA(t *testing.T){
 base:=evaluationSubmissionSpec();base.Source=domain.CodeSource{Type:"evaluation-archive",ArtifactID:strings.Repeat("c",32),ArtifactSHA256:strings.Repeat("d",64)}
 for _,mutate:=range []func(*domain.CodeSource){func(s *domain.CodeSource){s.ArtifactID="bad"},func(s *domain.CodeSource){s.ArtifactSHA256="bad"},func(s *domain.CodeSource){s.URL="https://external.example/code"},func(s *domain.CodeSource){s.Commit=strings.Repeat("a",40)},func(s *domain.CodeSource){s.ArtifactObjectKey="user/code.zip"},func(s *domain.CodeSource){s.Snapshot="private"}}{
  spec:=base;mutate(&spec.Source);if _,err:=normalizeSubmissionSpec(streamingPrincipal(),domain.SubmissionOriginEvaluation,spec,LocalCachePolicy{});err==nil{t.Fatal("unfrozen source fields accepted")}
 }
}
