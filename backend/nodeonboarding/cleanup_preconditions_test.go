package nodeonboarding

import (
	"context"
	"fmt"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ktesting "k8s.io/client-go/testing"
)

func TestCleanupDeletesWithExactUIDPreconditions(t *testing.T) {
	c, k := testController(t)
	ctx := context.Background()
	node := eligibleNode()
	expected := map[string]types.UID{}
	for _, suffix := range []string{"prepare", "probe"} {
		pod := c.preparePod(node)
		pod.ObjectMeta = objectMeta(node, c.config.Namespace, suffix)
		pod.UID = types.UID(suffix + "-uid")
		expected[pod.Name] = pod.UID
		if _, err := k.CoreV1().Pods(c.config.Namespace).Create(ctx, pod, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.ensureClaims(ctx, node); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 2; i++ {
		name := resourceName(node, fmt.Sprintf("cache%d", i))
		claim, err := k.CoreV1().PersistentVolumeClaims(c.config.Namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		claim.UID = types.UID(name + "-uid")
		expected[name] = claim.UID
		if _, err := k.CoreV1().PersistentVolumeClaims(c.config.Namespace).Update(ctx, claim, metav1.UpdateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	k.ClearActions()
	identities := map[string]types.UID{"prepare": "prepare-uid", "probe": "probe-uid", "cache1": expected[resourceName(node, "cache1")], "cache2": expected[resourceName(node, "cache2")]}
	if _, err := c.cleanup(ctx, node, nil, identities); err != nil {
		t.Fatal(err)
	}
	if _, err := c.cleanup(ctx, node, nil, identities); err != nil {
		t.Fatal(err)
	}
	deletes := 0
	for _, action := range k.Actions() {
		if action.GetVerb() != "delete" {
			continue
		}
		deletion, ok := action.(ktesting.DeleteAction)
		if !ok {
			t.Fatal("delete action type missing")
		}
		options := deletion.GetDeleteOptions()
		if options.Preconditions == nil || options.Preconditions.UID == nil || *options.Preconditions.UID != expected[deletion.GetName()] {
			t.Fatalf("unsafe delete preconditions for %s: %#v", deletion.GetName(), options)
		}
		deletes++
	}
	if deletes != 4 {
		t.Fatalf("expected 2 Pod + 2 PVC deletes, got %d", deletes)
	}
}

func TestFailedPodRetryUsesUIDPrecondition(t *testing.T) {
	c, k := testController(t)
	ctx := context.Background()
	node := eligibleNode()
	pod := c.preparePod(node)
	pod.UID = "failed-pod-uid"
	pod.Status.Phase = corev1.PodFailed
	if _, err := k.CoreV1().Pods(c.config.Namespace).Create(ctx, pod, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	k.ClearActions()
	if err := c.failed(ctx, node, pod, reconcileState{UID: node.UID, Stage: "prepare", At: c.now().Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, action := range k.Actions() {
		if action.GetVerb() != "delete" {
			continue
		}
		deletion := action.(ktesting.DeleteAction)
		preconditions := deletion.GetDeleteOptions().Preconditions
		if preconditions == nil || preconditions.UID == nil || *preconditions.UID != pod.UID {
			t.Fatal("retry omitted exact Pod UID precondition")
		}
		found = true
	}
	if !found {
		t.Fatal("retry did not delete failed Pod")
	}
}
