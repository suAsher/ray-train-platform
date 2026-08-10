package k8s

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

func TestEnsureRayJobIsIdempotent(t *testing.T) {
	client := &Client{dynamic: dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())}
	job := validRenderJob()
	manifest, err := RenderRayJob(job, testRenderOptions())
	if err != nil {
		t.Fatalf("render ray job: %v", err)
	}
	first, err := client.EnsureRayJob(context.Background(), manifest)
	if err != nil {
		t.Fatalf("first ensure: %v", err)
	}
	second, err := client.EnsureRayJob(context.Background(), manifest)
	if err != nil {
		t.Fatalf("second ensure: %v", err)
	}
	if first.GetUID() != second.GetUID() || first.GetResourceVersion() != second.GetResourceVersion() {
		t.Fatalf("expected idempotent resource: first=%v second=%v", first, second)
	}
}

func TestEnsureRayJobDoesNotAdoptForeignResource(t *testing.T) {
	job := validRenderJob()
	manifest, err := RenderRayJob(job, testRenderOptions())
	if err != nil {
		t.Fatalf("render ray job: %v", err)
	}
	foreign := manifest.DeepCopy()
	labels := foreign.GetLabels()
	labels["ray.io/job-id"] = "other-job"
	foreign.SetLabels(labels)
	client := &Client{dynamic: dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), foreign)}

	if _, err := client.EnsureRayJob(context.Background(), manifest); err == nil {
		t.Fatal("expected ownership error")
	}
}

func TestRayJobResourceUsesExpectedGroupVersion(t *testing.T) {
	if rayJobGVR != (schema.GroupVersionResource{Group: "ray.io", Version: "v1", Resource: "rayjobs"}) {
		t.Fatalf("unexpected ray job resource: %s", rayJobGVR.String())
	}
	if (&unstructured.Unstructured{}).GetKind() != "" {
		t.Fatal("sanity check failed")
	}
}
