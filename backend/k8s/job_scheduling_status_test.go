package k8s

import (
	"context"
	"errors"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	ktesting "k8s.io/client-go/testing"
	"ray-train-platform-backend/domain"
)

func TestJobSchedulingAdmittedRequiresExactAttemptOwnership(t *testing.T) {
	job := domain.TrainingJob{KubernetesNS: "team-a", RayJobName: "train-1", RayJobUID: "attempt-uid"}
	for _, test := range []struct {
		name                    string
		ownerUID                string
		status                  string
		wantAdmitted, wantKnown bool
	}{
		{"admitted", "attempt-uid", "True", true, true},
		{"waiting", "attempt-uid", "False", false, true},
		{"stale-owner", "stale-uid", "True", false, false},
		{"unknown", "attempt-uid", "Unknown", false, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			workload := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "kueue.x-k8s.io/v1beta2", "kind": "Workload", "status": map[string]any{"conditions": []any{map[string]any{"type": "Admitted", "status": test.status}}}}}
			if test.status == "True" {
				if err := unstructured.SetNestedMap(workload.Object, map[string]any{"clusterQueue": "gpu-pool"}, "status", "admission"); err != nil {
					t.Fatal(err)
				}
			}
			workload.SetName("train-workload")
			workload.SetNamespace(job.KubernetesNS)
			workload.SetLabels(map[string]string{"kueue.x-k8s.io/job-uid": job.RayJobUID})
			workload.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: "ray.io/v1", Kind: "RayJob", Name: job.RayJobName, UID: types.UID(test.ownerUID)}})
			gvr := schema.GroupVersionResource{Group: "kueue.x-k8s.io", Version: "v1beta2", Resource: "workloads"}
			dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "WorkloadList"}, workload)
			client := NewClientFromInterfaces(dyn, nil)
			admitted, known, err := client.JobSchedulingAdmitted(context.Background(), job)
			if err != nil || admitted != test.wantAdmitted || known != test.wantKnown {
				t.Fatalf("admitted=%t known=%t err=%v", admitted, known, err)
			}
			actions := dyn.Actions()
			if len(actions) != 1 {
				t.Fatalf("unexpected queries %v", actions)
			}
			list := actions[0].(ktesting.ListAction)
			if list.GetNamespace() != job.KubernetesNS || list.GetListRestrictions().Labels.String() != "kueue.x-k8s.io/job-uid=attempt-uid" {
				t.Fatalf("unbounded or unscoped query: %v", list)
			}
		})
	}
}

func TestJobSchedulingAdmittedMissingIdentityAndErrorsRemainUnknown(t *testing.T) {
	var missing *Client
	if admitted, known, err := missing.JobSchedulingAdmitted(context.Background(), domain.TrainingJob{}); admitted || known || err != nil {
		t.Fatalf("missing identity must remain unknown: %t %t %v", admitted, known, err)
	}
	gvr := schema.GroupVersionResource{Group: "kueue.x-k8s.io", Version: "v1beta2", Resource: "workloads"}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "WorkloadList"})
	dyn.PrependReactor("list", "workloads", func(ktesting.Action) (bool, runtime.Object, error) { return true, nil, errors.New("unavailable") })
	client := NewClientFromInterfaces(dyn, nil)
	if admitted, known, err := client.JobSchedulingAdmitted(context.Background(), domain.TrainingJob{KubernetesNS: "team-a", RayJobName: "train", RayJobUID: "uid"}); admitted || known || err == nil {
		t.Fatalf("failed observation must remain unknown: %t %t %v", admitted, known, err)
	}
}
