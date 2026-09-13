package main

import (
	"context"
	"encoding/json"
	"ray-train-platform-backend/domain"
	me "ray-train-platform-backend/modelevaluation"
	ms "ray-train-platform-backend/modelserving"
	"strings"
	"testing"
)

type servingLookupStub struct {
	value ms.Deployment
	calls int
}

func (s *servingLookupStub) GetDeploymentByJobID(context.Context, string) (ms.Deployment, error) {
	s.calls++
	return s.value, nil
}
func TestLoadTrustedServingRuntimeRejectsForgedIdentityAndExecution(t *testing.T) {
	code := &me.CodeSnapshot{ID: strings.Repeat("b", 32), SHA256: strings.Repeat("c", 64), SizeBytes: 123, Format: "zip"}
	d := ms.Deployment{ID: "deployment-1", JobID: "job-0123456789abcdef01234567", OwnerID: "owner", TenantID: "local", ModelSHA256: strings.Repeat("a", 64), ModelSizeBytes: 156, Contract: ms.Contract{Code: code, ImageReference: "registry/runtime@sha256:" + strings.Repeat("d", 64), EntryPoint: []string{"python", "adapter.py"}}, Resources: domain.Resources{WorkerReplicas: 1, GPUsPerWorker: 1, CPUPerWorker: 4, MemoryPerWorker: "16Gi"}}
	job := domain.TrainingJob{ID: d.JobID, UserID: d.OwnerID, TenantID: d.TenantID, ExternalSubmissionID: d.ID, SubmissionOrigin: domain.SubmissionOriginServing, Spec: domain.JobSpec{Image: d.Contract.ImageReference, Resources: d.Resources, Entrypoint: domain.Entrypoint{Command: append([]string{}, d.Contract.EntryPoint...)}, Source: domain.CodeSource{Type: "serving-archive", ArtifactID: code.ID, ArtifactSHA256: code.SHA256}}}
	lookup := &servingLookupStub{value: d}
	loaded, err := loadTrustedServingRuntime(context.Background(), &job, lookup)
	if err != nil || loaded.Spec.ServingRuntime == nil || job.Spec.ServingRuntime != nil {
		t.Fatalf("trusted runtime: %v", err)
	}
	raw, _ := json.Marshal(loaded.Spec)
	if strings.Contains(string(raw), "DeploymentID") || strings.Contains(string(raw), "ModelSizeBytes") {
		t.Fatal("trusted runtime became public JSON")
	}
	for _, alter := range []func(*domain.TrainingJob){func(j *domain.TrainingJob) { j.UserID = "other" }, func(j *domain.TrainingJob) { j.ExternalSubmissionID = "other" }, func(j *domain.TrainingJob) { j.Spec.Image = "registry/other" }, func(j *domain.TrainingJob) { j.Spec.Source.ArtifactSHA256 = strings.Repeat("f", 64) }, func(j *domain.TrainingJob) { j.Spec.Entrypoint.Command = []string{"python", "other.py"} }} {
		next := job
		alter(&next)
		if _, err := loadTrustedServingRuntime(context.Background(), &next, lookup); err == nil {
			t.Fatal("forged runtime accepted")
		}
	}
	ordinary := *loaded
	ordinary.SubmissionOrigin = domain.SubmissionOriginAPI
	calls := lookup.calls
	normal, err := loadTrustedServingRuntime(context.Background(), &ordinary, lookup)
	if err != nil || normal.Spec.ServingRuntime != nil || lookup.calls != calls {
		t.Fatal("ordinary job retained serving context")
	}
}
