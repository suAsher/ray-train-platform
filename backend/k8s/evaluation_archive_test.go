package k8s

import (
	"encoding/json"
	"ray-train-platform-backend/domain"
	"strings"
	"testing"
)

func evaluationArchiveJob() domain.TrainingJob {
	job := evaluationRenderFixture()
	job.Spec.Source = domain.CodeSource{Type: "evaluation-archive", ArtifactID: strings.Repeat("c", 32), ArtifactSHA256: strings.Repeat("d", 64)}
	job.Spec.EvaluationRuntime.CodeID = job.Spec.Source.ArtifactID
	job.Spec.EvaluationRuntime.CodeSHA256 = job.Spec.Source.ArtifactSHA256
	job.Spec.EvaluationRuntime.CodeSizeBytes = 1234
	job.Spec.EvaluationRuntime.CodeFormat = "zip"
	return job
}
func TestEvaluationArchiveMaterializesOnceWithoutPersonalStorageOrPAT(t *testing.T) {
	job := evaluationArchiveJob()
	options := testRenderOptions()
	options.TrainingEventBaseURL = "http://backend.platform.svc:8080/api/v1/internal"
	manifest, err := RenderRayJob(job, options)
	if err != nil {
		t.Fatal(err)
	}
	submitter, _, _ := nestedMap(manifest.Object, "spec", "submitterPodTemplate", "spec")
	init, _, _ := nestedSlice(submitter, "initContainers")
	if len(init) != 1 {
		t.Fatalf("expected one source materializer: %v", init)
	}
	source := init[0].(map[string]any)
	raw, _ := json.Marshal(source)
	for _, want := range []string{"platform-fetch-evaluation-code.py", "platform-safe-extract.py", "--size-bytes", "1234", job.ID, strings.Repeat("d", 64), "evaluation-source-events", "/var/run/secrets/raytrain-events"} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("init lacks %s", want)
		}
	}
	for _, forbidden := range []string{"workspace-snapshot-source", "GIT_TOKEN", "RAYTRAIN_PAT", "ArtifactObjectKey"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("init leaked personal credential/source: %s", forbidden)
		}
	}
	volumes, _, _ := nestedSlice(submitter, "volumes")
	found := false
	for _, v := range volumes {
		volume := v.(map[string]any)
		if volume["name"] == "evaluation-source-events" {
			found = true
			secret := volume["secret"].(map[string]any)
			if secret["secretName"] != TrainingEventSecretName(job.ID) || secret["defaultMode"] != int64(0440) {
				t.Fatal("code init secret is not scoped/readable")
			}
		}
	}
	if !found {
		t.Fatal("submitter lacks source credential volume")
	}
	containers, _, _ := nestedSlice(submitter, "containers")
	main, _ := json.Marshal(containers)
	if strings.Contains(string(main), "evaluation-source-events") || strings.Contains(string(main), "RAYTRAIN_EVENT_TOKEN_FILE") {
		t.Fatal("submitter main received source secret")
	}
	security, _, _ := nestedMap(submitter, "securityContext")
	if security["fsGroup"] != int64(1000) {
		t.Fatal("nonroot init cannot read secret")
	}
	cluster, _, _ := nestedMap(manifest.Object, "spec", "rayClusterSpec")
	head, _, _ := nestedMap(cluster, "headGroupSpec", "template", "spec")
	workers, _, _ := nestedSlice(cluster, "workerGroupSpecs")
	worker, _, _ := nestedMap(workers[0].(map[string]any), "template", "spec")
	for _, pod := range []map[string]any{head, worker} {
		encoded, _ := json.Marshal(pod)
		if strings.Contains(string(encoded), "platform-fetch-evaluation-code.py") {
			t.Fatal("code downloaded repeatedly in cluster pods")
		}
	}
}
func TestEvaluationArchiveRejectsNormalOriginUntrustedCodeAndExternalBase(t *testing.T) {
	for _, mutate := range []func(*domain.TrainingJob){
		func(job *domain.TrainingJob) { job.SubmissionOrigin = domain.SubmissionOriginAPI },
		func(job *domain.TrainingJob) { job.Spec.EvaluationRuntime.CodeID = strings.Repeat("b", 32) },
		func(job *domain.TrainingJob) { job.Spec.EvaluationRuntime.CodeSHA256 = strings.Repeat("b", 64) },
		func(job *domain.TrainingJob) { job.Spec.EvaluationRuntime.CodeSizeBytes = 0 },
		func(job *domain.TrainingJob) { job.Spec.EvaluationRuntime.CodeSizeBytes = 64*1024*1024 + 1 },
		func(job *domain.TrainingJob) { job.Spec.EvaluationRuntime.CodeFormat = "tar" },
		func(job *domain.TrainingJob) { job.Spec.Source.ArtifactObjectKey = "personal/code.zip" },
		func(job *domain.TrainingJob) { job.Spec.Source.URL = "https://outside.example/code" },
	} {
		job := evaluationArchiveJob()
		mutate(&job)
		if _, err := RenderRayJob(job, testRenderOptions()); err == nil {
			t.Fatal("untrusted archive source rendered")
		}
	}
	options := testRenderOptions()
	options.TrainingEventBaseURL = "https://outside.example/api/v1/internal"
	if _, err := RenderRayJob(evaluationArchiveJob(), options); err == nil {
		t.Fatal("external code download origin rendered")
	}
}
