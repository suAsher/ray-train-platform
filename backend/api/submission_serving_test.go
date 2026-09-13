package api

import (
 "strings"
 "testing"
 "ray-train-platform-backend/domain"
)

func TestServingSubmissionRequiresReservedIdentityAndFixedCode(t *testing.T) {
 input:=SubmissionInput{Origin:domain.SubmissionOriginServing,ReservedJobID:"job-0123456789abcdef01234567",ExternalSubmissionID:"deployment-1",ExpectedImageDigest:strings.Repeat("a",64),Spec:domain.JobSpec{Source:domain.CodeSource{Type:"serving-archive",ArtifactID:strings.Repeat("b",32),ArtifactSHA256:strings.Repeat("c",64)}}}
 if err:=validateEvaluationSubmissionInput(input);err!=nil {t.Fatal(err)}
 for _,alter:=range []func(*SubmissionInput){
  func(i *SubmissionInput){i.Origin=domain.SubmissionOriginAPI},
  func(i *SubmissionInput){i.ReservedJobID=""},
  func(i *SubmissionInput){i.ExternalSubmissionID=""},
  func(i *SubmissionInput){i.ExpectedImageDigest="latest"},
  func(i *SubmissionInput){i.Spec.Source.Type="git"},
  func(i *SubmissionInput){i.Spec.Source.ArtifactSHA256=""},
  func(i *SubmissionInput){i.ExpectedDatasetManifestSHA256=strings.Repeat("f",64)},
 } {changed:=input;alter(&changed);if err:=validateEvaluationSubmissionInput(changed);err==nil {t.Fatalf("accepted forged serving submission: %+v",changed)}}
}
