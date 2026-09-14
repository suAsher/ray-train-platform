package k8s

import (
	"context"
	"errors"
	"reflect"
	"strconv"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
	"ray-train-platform-backend/domain"
)

func TestPreferredTeamWorkerNodesOrdersSameTeamAcceleratorByAllocatedGPU(t *testing.T) {
	deleting := metav1.Now()
	client := &Client{kubernetes: fake.NewSimpleClientset(
		teamWorkerPod("same-small", "tenant-a", "job-small", "node-a", domain.AcceleratorRTX4090, corev1.PodRunning, 1),
		teamWorkerPod("same-large", "tenant-a", "job-large", "node-b", domain.AcceleratorRTX4090, corev1.PodRunning, 4),
		teamWorkerPod("same-large-2", "tenant-a", "job-large-2", "node-b", domain.AcceleratorRTX4090, corev1.PodPending, 2),
		teamWorkerPod("same-current", "tenant-a", "job-new", "node-c", domain.AcceleratorRTX4090, corev1.PodRunning, 8),
		teamWorkerPod("other-tenant", "tenant-b", "job-other", "node-d", domain.AcceleratorRTX4090, corev1.PodRunning, 8),
		teamWorkerPod("other-accelerator", "tenant-a", "job-a100", "node-e", domain.AcceleratorA100, corev1.PodRunning, 8),
		teamWorkerPod("cpu-only", "tenant-a", "job-cpu", "node-f", domain.AcceleratorRTX4090, corev1.PodRunning, 0),
		teamWorkerPod("finished", "tenant-a", "job-finished", "node-g", domain.AcceleratorRTX4090, corev1.PodSucceeded, 8),
		func() *corev1.Pod {
			pod := teamWorkerPod("deleting", "tenant-a", "job-deleting", "node-h", domain.AcceleratorRTX4090, corev1.PodRunning, 8)
			pod.DeletionTimestamp = &deleting
			return pod
		}(),
		func() *corev1.Pod {
			pod := teamWorkerPod("unbound", "tenant-a", "job-unbound", "", domain.AcceleratorRTX4090, corev1.PodPending, 8)
			return pod
		}(),
	)}

	nodes, err := client.PreferredTeamWorkerNodes(context.Background(), TeamWorkerNodePreferenceRequest{TenantID: "tenant-a", JobID: "job-new", AcceleratorClass: domain.AcceleratorRTX4090})
	if err != nil {
		t.Fatalf("preferred nodes: %v", err)
	}
	if want := []string{"node-b", "node-a"}; !reflect.DeepEqual(nodes, want) {
		t.Fatalf("nodes=%v want %v", nodes, want)
	}
}

func TestRenderOptionsTeamNodeAffinityDegradesWhenPodListFails(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	clientset.PrependReactor("list", "pods", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("api temporarily unavailable")
	})
	reconciler := NewReconciler(nil, &Client{kubernetes: clientset}, RenderOptions{TopologyAwareScheduling: true, TeamNodeAffinityEnabled: true, TeamNodeAffinityPreference: []string{"stale-node"}})
	job := validRenderJob()

	options, err := reconciler.renderOptionsForJob(context.Background(), job)
	if err != nil {
		t.Fatalf("render options should degrade safely: %v", err)
	}
	if len(options.TeamNodeAffinityPreference) != 0 {
		t.Fatalf("query failure must leave no preferred nodes: %v", options.TeamNodeAffinityPreference)
	}
}

func TestRenderOptionsTeamNodeAffinityDoesNotQueryWhenDisabledOrExisting(t *testing.T) {
	for _, tt := range []struct {
		name          string
		options       RenderOptions
		uid           string
		gpusPerWorker int
	}{
		{name: "gate disabled", options: RenderOptions{TopologyAwareScheduling: true, TeamNodeAffinityEnabled: false, TeamNodeAffinityPreference: []string{"stale-node"}}},
		{name: "tas disabled", options: RenderOptions{TopologyAwareScheduling: false, TeamNodeAffinityEnabled: true, TeamNodeAffinityPreference: []string{"stale-node"}}},
		{name: "existing rayjob", options: RenderOptions{TopologyAwareScheduling: true, TeamNodeAffinityEnabled: true, TeamNodeAffinityPreference: []string{"stale-node"}}, uid: "uid-existing"},
		{name: "cpu-only", options: RenderOptions{TopologyAwareScheduling: true, TeamNodeAffinityEnabled: true, TeamNodeAffinityPreference: []string{"stale-node"}}, gpusPerWorker: 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			queried := false
			clientset := fake.NewSimpleClientset(teamWorkerPod("same", "tenant-a", "job-old", "node-a", domain.AcceleratorRTX4090, corev1.PodRunning, 1))
			clientset.PrependReactor("list", "pods", func(action ktesting.Action) (bool, runtime.Object, error) {
				queried = true
				return false, nil, nil
			})
			reconciler := NewReconciler(nil, &Client{kubernetes: clientset}, tt.options)
			job := validRenderJob()
			job.RayJobUID = tt.uid
			if tt.gpusPerWorker == 0 && tt.name == "cpu-only" {
				job.Spec.Resources.GPUsPerWorker = 0
			}

			options, err := reconciler.renderOptionsForJob(context.Background(), job)
			if err != nil {
				t.Fatalf("render options: %v", err)
			}
			if queried || len(options.TeamNodeAffinityPreference) != 0 {
				t.Fatalf("unexpected query=%t preference=%v", queried, options.TeamNodeAffinityPreference)
			}
		})
	}
}

func teamWorkerPod(name, tenantID, jobID, nodeName string, accelerator domain.AcceleratorClass, phase corev1.PodPhase, gpus int64) *corev1.Pod {
	requests := corev1.ResourceList{}
	if gpus > 0 {
		requests[corev1.ResourceName("nvidia.com/gpu")] = resource.MustParse(strconv.FormatInt(gpus, 10))
	}
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "tenant-" + tenantID,
			Name:      name,
			Labels: map[string]string{
				"platform_tenant_id": tenantID,
				platformJobIDLabel:   jobID,
				"ray.io/node-type":   "worker",
			},
		},
		Spec: corev1.PodSpec{
			NodeName:     nodeName,
			NodeSelector: map[string]string{"accelerator": accelerator.NodeLabelValue()},
			Containers:   []corev1.Container{{Resources: corev1.ResourceRequirements{Requests: requests}}},
		},
		Status: corev1.PodStatus{Phase: phase},
	}
}
