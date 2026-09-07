package nodeonboarding

import (
	"context"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"log"
	"strings"
)

func (c *Controller) stageEvent(ctx context.Context, node *corev1.Node, stage, reason string) {
	if stage != "prepare" && stage != "probe" && stage != "cleanup" && stage != "ready" && stage != "reset" {
		return
	}
	if len(reason) > 400 {
		reason = reason[:400]
	}
	event := &corev1.Event{ObjectMeta: metav1.ObjectMeta{Name: resourceName(node, "event-"+stage), Namespace: c.config.Namespace}, InvolvedObject: corev1.ObjectReference{APIVersion: "v1", Kind: "Node", Name: node.Name, UID: node.UID}, Reason: "Onboarding" + strings.ToUpper(stage[:1]) + stage[1:], Message: reason, Source: corev1.EventSource{Component: "node-onboarding"}, FirstTimestamp: metav1.NewTime(c.now()), LastTimestamp: metav1.NewTime(c.now()), Count: 1, Type: corev1.EventTypeNormal}
	if _, err := c.client.CoreV1().Events(c.config.Namespace).Create(ctx, event, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		log.Printf("emit onboarding stage event: %v", err)
	}
}
