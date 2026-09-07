package nodeonboarding

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"testing"
)

func TestExactVKEProbeENIDefault(t *testing.T) {
	c, _ := testController(t)
	node := eligibleNode()
	desired := c.probePod(node, []nfsShare{{Server: "server", Path: "/exports"}})
	for _, test := range []struct {
		name, request, limit string
		allowed              bool
	}{
		{"single ENI", "1", "1", true}, {"two ENIs", "2", "2", false},
		{"unequal", "1", "2", false}, {"missing limit", "1", "", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			actual := desired.DeepCopy()
			actual.Spec.Containers[0].Resources.Requests["vke.volcengine.com/eni-ip"] = resource.MustParse(test.request)
			if test.limit != "" {
				actual.Spec.Containers[0].Resources.Limits["vke.volcengine.com/eni-ip"] = resource.MustParse(test.limit)
			}
			err := matchPodSpec(actual, desired, node)
			if (err == nil) != test.allowed {
				t.Fatalf("unexpected ENI normalization: %v", err)
			}
		})
	}
	actual := desired.DeepCopy()
	actual.Spec.Containers[0].Resources.Requests["nvidia.com/gpu"] = resource.MustParse("1")
	actual.Spec.Containers[0].Resources.Limits["nvidia.com/gpu"] = resource.MustParse("1")
	if err := matchPodSpec(actual, desired, node); err == nil {
		t.Fatal("accepted injected GPU")
	}
	prepare := c.preparePod(node)
	for index := range prepare.Spec.Containers {
		actual := prepare.DeepCopy()
		actual.Spec.Containers[index].Resources.Requests[corev1.ResourceName("vke.volcengine.com/eni-ip")] = resource.MustParse("1")
		actual.Spec.Containers[index].Resources.Limits[corev1.ResourceName("vke.volcengine.com/eni-ip")] = resource.MustParse("1")
		if err := matchPodSpec(actual, prepare, node); err == nil {
			t.Fatal("accepted ENI outside fixed probe check container")
		}
	}
}
