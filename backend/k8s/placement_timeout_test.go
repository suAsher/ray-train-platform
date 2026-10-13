package k8s

import (
	"context"
	"reflect"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	typedcore "k8s.io/client-go/kubernetes/typed/core/v1"
	"ray-train-platform-backend/domain"
)

// Unlike client-go fake reactors, this List respects cancellation while the
// first cold API observation is still waiting for a response.
type delayedPlacementNodes struct{ typedcore.NodeInterface }

func (n delayedPlacementNodes) List(ctx context.Context, options metav1.ListOptions) (*corev1.NodeList, error) {
	timer := time.NewTimer(2200 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-timer.C:
		return n.NodeInterface.List(ctx, options)
	}
}

type delayedPlacementCore struct{ typedcore.CoreV1Interface }

func (c delayedPlacementCore) Nodes() typedcore.NodeInterface {
	return delayedPlacementNodes{c.CoreV1Interface.Nodes()}
}

type delayedPlacementClient struct{ kubernetes.Interface }

func (c delayedPlacementClient) CoreV1() typedcore.CoreV1Interface {
	return delayedPlacementCore{c.Interface.CoreV1()}
}

func TestTrainingCompactPreferenceAllowsColdCapacityObservation(t *testing.T) {
	core := fake.NewSimpleClientset(compactPlacementNode("node-cold"))
	client := &Client{dynamic: compactPlacementDynamic(), kubernetes: delayedPlacementClient{core}}
	reconciler := NewReconciler(nil, client, RenderOptions{})
	job := validRenderJob()
	job.Spec.Resources = domain.Resources{WorkerReplicas: 1, GPUsPerWorker: 1, CPUPerWorker: 4, MemoryPerWorker: "16Gi"}
	options := reconciler.withTeamNodeAffinity(context.Background(), job, RenderOptions{TopologyAwareScheduling: true, TeamNodeAffinityEnabled: true})
	if !reflect.DeepEqual(options.TeamNodeAffinityPreference, []string{"node-cold"}) {
		t.Fatalf("a healthy 2.2-second cold observation must retain compact placement, got %v", options.TeamNodeAffinityPreference)
	}
}
