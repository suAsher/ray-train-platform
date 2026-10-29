package domain

import "testing"

func TestRetiredPersonalStorageForbiddenForBrowsingAndTraining(t *testing.T) {
	if IsKnownDataSpace(DataSpaceWellspikingPersonal) {
		t.Fatal("retired personal browser space must not be known")
	}
	for _, location := range []DataLocation{{Space: DataSpaceWellspikingPersonal}, {Space: DataSpaceWellspikingPersonal, RelativePath: "data"}} {
		for _, spec := range []JobSpec{{Input: location}, {Checkpoint: location}, {Output: location}} {
			if err := validateLogicalDataLocations(spec); err == nil {
				t.Fatalf("personal space must never become training input/checkpoint/output: %#v", spec)
			}
		}
	}
}
