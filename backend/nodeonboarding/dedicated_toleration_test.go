package nodeonboarding

import (
	"reflect"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
)

func TestProbeToleratesOnlyItsNodesDedicatedTenant(t *testing.T) {
	controller, _ := testController(t)
	for _, test := range []struct {
		name   string
		taints []corev1.Taint
		want   []corev1.Toleration
	}{
		{name: "shared node"},
		{name: "dedicated tenant", taints: []corev1.Taint{{Key: dedicatedTenantTaintKey, Value: "algorithm", Effect: corev1.TaintEffectNoSchedule}}, want: []corev1.Toleration{{Key: dedicatedTenantTaintKey, Operator: corev1.TolerationOpEqual, Value: "algorithm", Effect: corev1.TaintEffectNoSchedule}}},
		{name: "other tenant", taints: []corev1.Taint{{Key: dedicatedTenantTaintKey, Value: "team-b", Effect: corev1.TaintEffectNoSchedule}}, want: []corev1.Toleration{{Key: dedicatedTenantTaintKey, Operator: corev1.TolerationOpEqual, Value: "team-b", Effect: corev1.TaintEffectNoSchedule}}},
		{name: "unrelated taint", taints: []corev1.Taint{{Key: "unrelated.example/maintenance", Value: "true", Effect: corev1.TaintEffectNoSchedule}}},
		{name: "no execute", taints: []corev1.Taint{{Key: dedicatedTenantTaintKey, Value: "algorithm", Effect: corev1.TaintEffectNoExecute}}},
		{name: "empty tenant", taints: []corev1.Taint{{Key: dedicatedTenantTaintKey, Effect: corev1.TaintEffectNoSchedule}}},
		{name: "invalid tenant", taints: []corev1.Taint{{Key: dedicatedTenantTaintKey, Value: "Team_A", Effect: corev1.TaintEffectNoSchedule}}},
		{name: "too long", taints: []corev1.Taint{{Key: dedicatedTenantTaintKey, Value: strings.Repeat("a", 64), Effect: corev1.TaintEffectNoSchedule}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			node := eligibleNode()
			node.Spec.Taints = test.taints
			before := node.DeepCopy()
			pod := controller.probePod(node, nil)
			if !reflect.DeepEqual(pod.Spec.Tolerations, test.want) {
				t.Fatalf("tolerations = %#v, want %#v", pod.Spec.Tolerations, test.want)
			}
			if !reflect.DeepEqual(node, before) {
				t.Fatal("probe construction mutated node")
			}
			prepare := controller.preparePod(node)
			if len(prepare.Spec.Tolerations) != 0 || prepare.Spec.NodeName != node.Name {
				t.Fatal("preparation must retain its exact nodeName without tolerations")
			}
		})
	}
}

func TestDedicatedProbeProofRejectsBroadenedTolerations(t *testing.T) {
	controller, _ := testController(t)
	node := eligibleNode()
	node.Spec.Taints = []corev1.Taint{{Key: dedicatedTenantTaintKey, Value: "algorithm", Effect: corev1.TaintEffectNoSchedule}}
	desired := controller.probePod(node, nil)
	if err := matchPodSpec(desired.DeepCopy(), desired, node); err != nil {
		t.Fatal(err)
	}
	for _, tolerance := range []corev1.Toleration{
		{Key: dedicatedTenantTaintKey, Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoSchedule},
		{Key: dedicatedTenantTaintKey, Operator: corev1.TolerationOpEqual, Value: "team-b", Effect: corev1.TaintEffectNoSchedule},
		{Operator: corev1.TolerationOpExists},
	} {
		actual := desired.DeepCopy()
		actual.Spec.Tolerations = []corev1.Toleration{tolerance}
		if err := matchPodSpec(actual, desired, node); err == nil {
			t.Fatalf("accepted changed toleration: %#v", tolerance)
		}
	}
}
