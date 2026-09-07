package nodeonboarding

import (
	"context"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ktesting "k8s.io/client-go/testing"
	"testing"
	"time"
)

func TestLeaderElectionRunsBoundedWorker(t *testing.T) {
	c, k := testController(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	created := make(chan struct{}, 1)
	k.PrependReactor("create", "pods", func(action ktesting.Action) (bool, runtime.Object, error) {
		select {
		case created <- struct{}{}:
		default:
		}
		return false, nil, nil
	})
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx, "node-onboarding", "test-controller") }()
	select {
	case <-created:
		cancel()
	case <-ctx.Done():
		t.Fatal("elected leader did not reconcile")
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("leader did not stop")
	}
	if err := c.Run(context.Background(), "bad/name", "test"); err == nil {
		t.Fatal("accepted invalid lease")
	}
}
func TestEligibilityAndRegistrationLoss(t *testing.T) {
	node := eligibleNode()
	node.Status.Allocatable = nil
	if eligible(node) {
		t.Fatal("accepted no GPUs")
	}
	node = eligibleNode()
	node.Status.Conditions = nil
	if eligible(node) {
		t.Fatal("accepted missing Ready")
	}
	node = eligibleNode()
	node.Status.Conditions[0].Status = corev1.ConditionFalse
	if eligible(node) {
		t.Fatal("accepted not Ready")
	}
	c, k := testController(t)
	if err := c.checkRegistered(context.Background(), "gpu-1"); err == nil {
		t.Fatal("accepted missing registrations")
	}
	if err := c.register(context.Background(), "gpu-1"); err != nil {
		t.Fatal(err)
	}
	n, _ := k.CoreV1().Nodes().Get(context.Background(), "gpu-1", metav1.GetOptions{})
	n.ResourceVersion = "123"
	n.Labels["foreign"] = "keep"
	n.Annotations = map[string]string{"foreign": "keep"}
	k.CoreV1().Nodes().Update(context.Background(), n, metav1.UpdateOptions{})
	if err := c.save(context.Background(), n, reconcileState{UID: n.UID}, false, "testing preservation"); err != nil {
		t.Fatal(err)
	}
	n, _ = k.CoreV1().Nodes().Get(context.Background(), "gpu-1", metav1.GetOptions{})
	if n.Labels["foreign"] != "keep" || n.Annotations["foreign"] != "keep" {
		t.Fatal("lost foreign metadata")
	}
}
