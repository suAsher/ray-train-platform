package main

import (
	"context"
	"fmt"
	"ray-train-platform-backend/domain"
	ms "ray-train-platform-backend/modelserving"
	"reflect"
)

type servingRuntimeLookup interface {
	GetDeploymentByJobID(context.Context, string) (ms.Deployment, error)
}

func loadTrustedServingRuntime(ctx context.Context, job *domain.TrainingJob, lookup servingRuntimeLookup) (*domain.TrainingJob, error) {
	if job == nil {
		return nil, fmt.Errorf("job was not found")
	}
	loaded := *job
	loaded.Spec.ServingRuntime = nil
	if job.SubmissionOrigin != domain.SubmissionOriginServing {
		return &loaded, nil
	}
	if lookup == nil {
		return nil, fmt.Errorf("serving repository is not configured")
	}
	d, err := lookup.GetDeploymentByJobID(ctx, job.ID)
	if err != nil {
		return nil, err
	}
	if d.ID != job.ExternalSubmissionID || d.JobID != job.ID || d.OwnerID != job.UserID || d.TenantID != job.TenantID || d.Contract.Code == nil || job.Spec.Image != d.Contract.ImageReference || job.Spec.Resources != d.Resources || !reflect.DeepEqual(job.Spec.Entrypoint.Command, d.Contract.EntryPoint) || len(job.Spec.Entrypoint.Args) > 0 {
		return nil, fmt.Errorf("serving job does not match immutable deployment")
	}
	code := d.Contract.Code
	runtime := &domain.ServingRuntime{DeploymentID: d.ID, ModelSHA256: d.ModelSHA256, ModelSizeBytes: d.ModelSizeBytes, CodeID: code.ID, CodeSHA256: code.SHA256, CodeSizeBytes: code.SizeBytes, CodeFormat: code.Format, Protocol: ms.Protocol}
	if err := runtime.ValidateCodeSource(job.Spec.Source); err != nil {
		return nil, err
	}
	loaded.Spec.ServingRuntime = runtime
	return &loaded, nil
}
