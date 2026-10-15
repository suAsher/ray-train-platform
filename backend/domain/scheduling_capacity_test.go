package domain

import (
	"encoding/json"
	"testing"
)

func TestSchedulingCapacityOmitsResponseClockByDefault(t *testing.T) {
	encoded, err := json.Marshal(SchedulingCapacity{})
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	if _, found := fields["serverTime"]; found {
		t.Fatalf("collector and embedded diagnosis snapshots must not invent a response clock: %s", encoded)
	}
}
