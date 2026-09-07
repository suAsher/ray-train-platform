package nodeonboarding

import (
	corev1 "k8s.io/api/core/v1"
	"testing"
)

func TestProbePriorityClassMatchesNonPreemptingAdmission(t *testing.T) {
	c, _ := testController(t)
	node := eligibleNode()
	for _, pod := range []*corev1.Pod{c.preparePod(node), c.probePod(node, []nfsShare{{Server: "server", Path: "/exports"}})} {
		if pod.Spec.PriorityClassName != "node-onboarding-probe" || pod.Spec.PreemptionPolicy == nil || *pod.Spec.PreemptionPolicy != corev1.PreemptNever {
			t.Fatalf("%s must use fixed non-preempting PriorityClass", pod.Name)
		}
		admitted := pod.DeepCopy()
		admitted.Spec.Priority = pointer(int32(-1000))
		if err := matchPodSpec(admitted, pod, node); err != nil {
			t.Fatalf("server-computed class priority rejected: %v", err)
		}
		admitted.Spec.Priority = pointer(int32(0))
		if err := matchPodSpec(admitted, pod, node); err == nil {
			t.Fatal("accepted priority inconsistent with fixed class")
		}
	}
}
