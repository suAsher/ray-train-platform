package nodeonboarding

import (
	"context"
	"encoding/json"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
	"testing"
)

func TestClaimProofPersistsBeforeProbeCreation(t *testing.T) {
	c, k := testController(t)
	node := eligibleNode()
	step(t, c)
	completePod(t, k, c.config.Namespace, resourceName(node, "prepare"))
	step(t, c)
	k.ClearActions()
	step(t, c)
	recorded := false
	created := false
	for _, action := range k.Actions() {
		if action.GetVerb() == "update" && action.GetResource().Resource == "configmaps" {
			cm := action.(ktesting.UpdateAction).GetObject().(*corev1.ConfigMap)
			if cm.Name == c.config.StateConfigMap {
				var states map[string]reconcileState
				if err := json.Unmarshal([]byte(cm.Data["state.json"]), &states); err != nil {
					t.Fatal(err)
				}
				state := states[string(node.UID)]
				if sameClaimUIDs(state.ClaimUIDs, state.ClaimUIDs) {
					recorded = true
				}
			}
		}
		if action.GetVerb() == "create" && action.GetResource().Resource == "pods" {
			pod := action.(ktesting.CreateAction).GetObject().(*corev1.Pod)
			if pod.Name == resourceName(node, "probe") {
				if !recorded {
					t.Fatal("probe created before durable PVC identity proof")
				}
				created = true
			}
		}
	}
	if !created {
		t.Fatal("probe creation not exercised")
	}
}

func completedProbeFixture(t *testing.T) (*Controller, *fake.Clientset) {
	t.Helper()
	c, k := testController(t)
	step(t, c)
	completePod(t, k, c.config.Namespace, resourceName(eligibleNode(), "prepare"))
	step(t, c)
	step(t, c)
	bindClaims(t, c, k)
	completePod(t, k, c.config.Namespace, resourceName(eligibleNode(), "probe"))
	return c, k
}

func replaceClaimUID(t *testing.T, c *Controller, k *fake.Clientset, replacePV bool) {
	t.Helper()
	ctx := context.Background()
	claim, err := k.CoreV1().PersistentVolumeClaims(c.config.Namespace).Get(ctx, resourceName(eligibleNode(), "cache1"), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	claim.UID = "replacement-claim-uid"
	if _, err := k.CoreV1().PersistentVolumeClaims(c.config.Namespace).Update(ctx, claim, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if replacePV {
		pv, err := k.CoreV1().PersistentVolumes().Get(ctx, claim.Spec.VolumeName, metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		pv.Spec.ClaimRef.UID = claim.UID
		if _, err := k.CoreV1().PersistentVolumes().Update(ctx, pv, metav1.UpdateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestReplacementClaimCannotReuseProbeSuccess(t *testing.T) {
	c, k := completedProbeFixture(t)
	replaceClaimUID(t, c, k, true)
	if err := c.Reconcile(context.Background(), "gpu-1"); err == nil {
		t.Fatal("replacement claim reused a probe run against a different PVC UID")
	}
}

func TestCleanupRejectsSameNameSameOwnerReplacementClaim(t *testing.T) {
	c, k := completedProbeFixture(t)
	step(t, c)
	replaceClaimUID(t, c, k, false)
	k.ClearActions()
	if err := c.Reconcile(context.Background(), "gpu-1"); err == nil {
		t.Fatal("cleanup accepted replacement PVC UID")
	}
	for _, action := range k.Actions() {
		if action.GetVerb() == "delete" {
			t.Fatalf("deleted resource after proof mismatch: %s", action.GetResource().Resource)
		}
	}
	node, err := k.CoreV1().Nodes().Get(context.Background(), "gpu-1", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if node.Labels[CacheReadyLabel] != "" {
		t.Fatal("replacement PVC granted readiness")
	}
}
