package k8s

import (
	"context"
	core "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"testing"
)

func TestAssistantStatusObservesPlainPodWithoutRayService(t *testing.T) {
	c, k := assistantStatusFixture()
	ctx := context.Background()
	ns := "raytrain-assistant-test"
	cm, _ := k.CoreV1().ConfigMaps(ns).Get(ctx, "assistant-idle-config-current", metav1.GetOptions{})
	cm.Data["config.json"] = `{"runtimeType":"pod","enabled":true,"render":{"Name":"assistant-canary","Namespace":"raytrain-assistant-test"}}`
	if _, err := k.CoreV1().ConfigMaps(ns).Update(ctx, cm, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	pod, _ := k.CoreV1().Pods(ns).Get(ctx, "worker-a", metav1.GetOptions{})
	pod.Name = "assistant-canary"
	pod.ResourceVersion = ""
	pod.UID = "plain-pod"
	pod.Labels["raytrain.wellspiking.ai/assistant-role"] = "inference"
	if _, err := k.CoreV1().Pods(ns).Create(ctx, pod, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	got, err := c.ObserveAssistantStatus(ctx, ns)
	if err != nil || got.RuntimeType != "pod" || !got.InferencePod.Ready || got.RayService.Present {
		t.Fatalf("incorrect plain inference state: %+v %v", got, err)
	}
	pod.Spec.SchedulingGates = []core.PodSchedulingGate{{Name: "kueue.x-k8s.io/admission"}}
	if _, err := k.CoreV1().Pods(ns).Update(ctx, pod, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	got, err = c.ObserveAssistantStatus(ctx, ns)
	if err != nil || got.InferencePod.Ready || !got.InferencePod.Suspended {
		t.Fatalf("gated pod claimed ready: %+v %v", got, err)
	}
}
