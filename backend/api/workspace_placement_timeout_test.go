package api

import (
	"context"
	"reflect"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	typedcore "k8s.io/client-go/kubernetes/typed/core/v1"
	"ray-train-platform-backend/domain"
	"ray-train-platform-backend/k8s"
)

type delayedWorkspacePlacementNodes struct{ typedcore.NodeInterface }

func (n delayedWorkspacePlacementNodes) List(ctx context.Context, options metav1.ListOptions) (*corev1.NodeList, error) {
	timer := time.NewTimer(2200 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-timer.C:
		return n.NodeInterface.List(ctx, options)
	}
}

type delayedWorkspacePlacementCore struct{ typedcore.CoreV1Interface }

func (c delayedWorkspacePlacementCore) Nodes() typedcore.NodeInterface {
	return delayedWorkspacePlacementNodes{c.CoreV1Interface.Nodes()}
}

type delayedWorkspacePlacementClient struct{ kubernetes.Interface }

func (c delayedWorkspacePlacementClient) CoreV1() typedcore.CoreV1Interface {
	return delayedWorkspacePlacementCore{c.Interface.CoreV1()}
}

func TestWorkspaceCompactPreferenceAllowsColdCapacityObservation(t *testing.T) {
	dynamic := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		{Group: "kueue.x-k8s.io", Version: "v1beta2", Resource: "workloads"}: "WorkloadList",
	})
	core := delayedWorkspacePlacementClient{fake.NewSimpleClientset(workspacePlacementNode("node-cold"))}
	handler := NewHandler(&fakeJobRepository{}, Options{Kubernetes: k8s.NewClientFromInterfaces(dynamic, core)})
	nodes := handler.workspaceWorkerNodePreference(context.Background(), domain.DevWorkspace{ID: "ws-cold", TenantID: "team-a", GPUCount: 1, State: domain.WorkspaceSubmitted}, domain.AcceleratorRTX4090, nil)
	if !reflect.DeepEqual(nodes, []string{"node-cold"}) {
		t.Fatalf("a healthy 2.2-second cold observation must retain debug compact placement, got %v", nodes)
	}
}
