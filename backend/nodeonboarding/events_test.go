package nodeonboarding

import (
	"context"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"strings"
	"testing"
)

func TestStageEventUsesNamespacedProofStoreIdentity(t *testing.T) {
	c, k := testController(t)
	ctx := context.Background()
	store, err := k.CoreV1().ConfigMaps(c.config.Namespace).Get(ctx, c.config.StateConfigMap, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	store.UID = "proof-store-uid"
	if _, err := k.CoreV1().ConfigMaps(c.config.Namespace).Update(ctx, store, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	node := eligibleNode()
	c.stageEvent(ctx, node, "ready", "cache and NFS verified")
	events, err := k.CoreV1().Events(c.config.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(events.Items) != 1 {
		t.Fatalf("events: %d", len(events.Items))
	}
	event := events.Items[0]
	if event.InvolvedObject.Kind != "ConfigMap" || event.InvolvedObject.APIVersion != "v1" || event.InvolvedObject.Name != store.Name || event.InvolvedObject.UID != store.UID || event.InvolvedObject.Namespace != event.Namespace {
		t.Fatalf("incorrect namespaced event identity: %#v", event.InvolvedObject)
	}
	for _, meaning := range []string{node.Name, string(node.UID), "ready", "cache and NFS verified"} {
		if !strings.Contains(event.Message, meaning) {
			t.Fatalf("missing stage meaning %q: %s", meaning, event.Message)
		}
	}
	if event.Reason != "OnboardingReady" {
		t.Fatal(event.Reason)
	}
}

func TestStageEventDoesNotInventProofStoreUID(t *testing.T) {
	c, k := testController(t)
	ctx := context.Background()
	c.stageEvent(ctx, eligibleNode(), "prepare", "waiting")
	events, err := k.CoreV1().Events(c.config.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(events.Items) != 0 {
		t.Fatal("created event without authoritative ConfigMap UID")
	}
}
