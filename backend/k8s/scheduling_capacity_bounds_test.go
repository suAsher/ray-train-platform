package k8s

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
)

func TestSchedulingCapacityRejectsOverflowingMemoryShape(t *testing.T) {
	c := capacityTestClient([]runtime.Object{capacityTestNode("a")})
	r := capacityTestRequest()
	r.Resources.MemoryPerWorker = "16Ei"
	s, err := c.SchedulingCapacity(context.Background(), r)
	if err == nil || s.Available {
		t.Fatalf("overflowing memory must be unknown, capacity=%+v err=%v", s, err)
	}
}
