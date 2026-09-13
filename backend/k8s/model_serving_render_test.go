package k8s

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	"ray-train-platform-backend/domain"
)

func servingRenderOptions() RenderOptions {
	options := testRenderOptions()
	options.TrainingEventBaseURL = "http://backend.platform.svc:8080/api/v1/internal"
	return options
}

func TestServingRenderMaterializesOnlySubmitterAndInjectsOnlyWorker(t *testing.T) {
	job := servingRenderFixture()
	manifest, err := RenderRayJob(job, servingRenderOptions())
	if err != nil {
		t.Fatal(err)
	}
	cluster, _, _ := nestedMap(manifest.Object, "spec", "rayClusterSpec")
	head, _, _ := nestedMap(cluster, "headGroupSpec", "template", "spec")
	submitter, _, _ := nestedMap(manifest.Object, "spec", "submitterPodTemplate", "spec")
	workers, _, _ := nestedSlice(cluster, "workerGroupSpecs")
	if len(workers) != 1 || workers[0].(map[string]any)["replicas"] != int64(1) {
		t.Fatal("serving is not a single worker")
	}
	workerTemplate, _, _ := nestedMap(workers[0].(map[string]any), "template")
	worker, _, _ := nestedMap(workerTemplate, "spec")
	labels, _, _ := nestedMap(workerTemplate, "metadata", "labels")
	if labels["platform_job_id"] != job.ID {
		t.Fatal("Service task selector is absent from actual worker manifest")
	}
	workerRaw, _ := json.Marshal(worker)
	for _, want := range []string{"MODEL_SERVING_ID", "MODEL_SERVING_MODEL_SHA256", "MODEL_SERVING_MODEL_SIZE_BYTES", "model-serving-http/v1", "/jobs/" + job.ID + "/model-serving", "RAYTRAIN_EVENT_TOKEN_FILE", `"containerPort":8000`} {
		if !strings.Contains(string(workerRaw), want) {
			t.Fatalf("worker lacks %s", want)
		}
	}
	for _, pod := range []map[string]any{head, submitter} {
		raw, _ := json.Marshal(pod)
		if strings.Contains(string(raw), "MODEL_SERVING_") {
			t.Fatal("serving credentials/context escaped worker")
		}
	}
	init, _, _ := nestedSlice(submitter, "initContainers")
	if len(init) != 1 {
		t.Fatal("serving needs exactly one source materializer")
	}
	initRaw, _ := json.Marshal(init)
	for _, want := range []string{"platform-fetch-serving-code.py", "platform-safe-extract.py", "evaluation-source-events", "/var/run/secrets/raytrain-events", "--required-script", "smoke_adapter.py"} {
		if !strings.Contains(string(initRaw), want) {
			t.Fatalf("source init lacks %s", want)
		}
	}
	if strings.Contains(string(initRaw), "GIT_TOKEN") || strings.Contains(string(initRaw), "RAYTRAIN_PAT") || strings.Contains(string(initRaw), "workspace-snapshot-source") {
		t.Fatal("source init received unrelated credentials")
	}
	main, _, _ := nestedSlice(submitter, "containers")
	mainRaw, _ := json.Marshal(main)
	if strings.Contains(string(mainRaw), "evaluation-source-events") || strings.Contains(string(mainRaw), "RAYTRAIN_EVENT_TOKEN_FILE") {
		t.Fatal("submitter main can read job secret")
	}
	security, _, _ := nestedMap(submitter, "securityContext")
	if security["fsGroup"] != int64(1000) {
		t.Fatal("materializer cannot read job scoped secret")
	}
	for _, pod := range []map[string]any{head, worker} {
		raw, _ := json.Marshal(pod)
		if strings.Contains(string(raw), "platform-fetch-serving-code.py") {
			t.Fatal("code download repeated in cluster pod")
		}
	}
	entry, _, _ := unstructured.NestedString(manifest.Object, "spec", "entrypoint")
	if !strings.HasPrefix(entry, "raytrain-managed ") || !strings.Contains(entry, "--data-mode mount") || !strings.Contains(entry, " -- python smoke_adapter.py") || strings.Contains(entry, "--dataset-") {
		t.Fatalf("serving bypasses managed worker or initializes training: %s", entry)
	}
}

func TestServingRendererRejectsForgedContextAndMultipleRanks(t *testing.T) {
	for _, mutate := range []func(*domain.TrainingJob){
		func(j *domain.TrainingJob) { j.Spec.ServingRuntime = nil },
		func(j *domain.TrainingJob) { j.SubmissionOrigin = domain.SubmissionOriginAPI },
		func(j *domain.TrainingJob) { j.Spec.ServingRuntime.CodeSHA256 = strings.Repeat("f", 64) },
		func(j *domain.TrainingJob) { j.Spec.Source.URL = "https://external.example/code.zip" },
		func(j *domain.TrainingJob) { j.Spec.Resources.WorkerReplicas = 2 },
		func(j *domain.TrainingJob) { j.Spec.Resources.GPUsPerWorker = 2 },
		func(j *domain.TrainingJob) { j.Spec.Managed.MaxFailures = 1 },
		func(j *domain.TrainingJob) { j.Spec.TrainingEngine = domain.TrainingEngineRayDDP },
	} {
		job := servingRenderFixture()
		mutate(&job)
		if _, err := RenderRayJob(job, servingRenderOptions()); err == nil {
			t.Fatal("unsafe serving job rendered")
		}
	}
	options := servingRenderOptions()
	options.TrainingEventBaseURL = "https://external.example/api/v1/internal"
	if _, err := RenderRayJob(servingRenderFixture(), options); err == nil {
		t.Fatal("external code endpoint accepted")
	}
}

func TestOrdinaryTrainingManifestUnaffectedByInjectedServingRuntime(t *testing.T) {
	for _, baseline := range []domain.TrainingJob{validRenderJob(), evaluationRenderFixture()} {
		before, err := RenderRayJob(baseline, servingRenderOptions())
		if err != nil {
			t.Fatal(err)
		}
		injected := baseline
		injected.Spec.ServingRuntime = servingRenderFixture().Spec.ServingRuntime
		after, err := RenderRayJob(injected, servingRenderOptions())
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(before.Object, after.Object) {
			t.Fatal("ordinary training/evaluation changed when supplied serving runtime")
		}
		if injected.Spec.ServingRuntime == nil {
			t.Fatal("renderer mutated caller")
		}
	}
}

func TestRenderedWorkerMatchesOwnedServingService(t *testing.T) {
	job := servingRenderFixture()
	job.RayJobName, job.RayJobUID, job.RayClusterName = "serving-attempt-1", "serving-job-uid", "serving-cluster-1"
	manifest, err := RenderRayJob(job, servingRenderOptions())
	if err != nil {
		t.Fatal(err)
	}
	manifest.SetUID(types.UID(job.RayJobUID))
	_ = unstructured.SetNestedField(manifest.Object, job.RayClusterName, "status", "rayClusterName")
	clusterSpec, _, _ := unstructured.NestedMap(manifest.Object, "spec", "rayClusterSpec")
	cluster := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "ray.io/v1", "kind": "RayCluster", "spec": clusterSpec}}
	cluster.SetName(job.RayClusterName)
	cluster.SetNamespace(job.KubernetesNS)
	cluster.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: "ray.io/v1", Kind: "RayJob", Name: job.RayJobName, UID: types.UID(job.RayJobUID), Controller: ptrTrue()}})
	client := NewClientFromInterfaces(dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), manifest, cluster), k8sfake.NewSimpleClientset())
	if _, err := client.EnsureModelServingService(context.Background(), &job); err != nil {
		t.Fatalf("real renderer and service do not agree: %v", err)
	}
}
