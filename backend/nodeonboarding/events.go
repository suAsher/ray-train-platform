package nodeonboarding

import (
	"context"
	"fmt"
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
	store, err := c.client.CoreV1().ConfigMaps(c.config.Namespace).Get(ctx, c.config.StateConfigMap, metav1.GetOptions{})
	if err != nil {
		log.Printf("read onboarding event proof store: %v", err)
		return
	}
	if store.UID == "" {
		log.Print("skip onboarding event: proof store has no UID")
		return
	}
	message := fmt.Sprintf("Node %s (UID %s), stage %s: %s", node.Name, node.UID, stage, reason)
	if len(message) > 400 {
		message = strings.ToValidUTF8(message[:400], "")
	}
	event := &corev1.Event{ObjectMeta: metav1.ObjectMeta{Name: resourceName(node, "event-"+stage), Namespace: c.config.Namespace}, InvolvedObject: corev1.ObjectReference{APIVersion: "v1", Kind: "ConfigMap", Name: store.Name, Namespace: store.Namespace, UID: store.UID}, Reason: "Onboarding" + strings.ToUpper(stage[:1]) + stage[1:], Message: message, Source: corev1.EventSource{Component: "node-onboarding"}, FirstTimestamp: metav1.NewTime(c.now()), LastTimestamp: metav1.NewTime(c.now()), Count: 1, Type: corev1.EventTypeNormal}
	if _, err := c.client.CoreV1().Events(c.config.Namespace).Create(ctx, event, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		log.Printf("emit onboarding stage event: %v", err)
	}
}
