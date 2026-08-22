package domain

import "testing"

func TestDataLocationRejectsEscapeAndReadonlyOutput(t *testing.T) {
	if _, err := NewDataLocation(DataSpaceMyFiles, "../other"); err == nil {
		t.Fatal("traversal accepted")
	}
	if err := ValidateOutputSpace(DataSpaceTeamShared); err == nil {
		t.Fatal("readonly output space accepted")
	}
	if err := ValidateOutputSpace(DataSpaceMyFiles); err == nil {
		t.Fatal("unmanaged personal output space accepted")
	}
	location, err := NewDataLocation(DataSpaceMyFiles, "datasets/cats")
	if err != nil {
		t.Fatalf("new data location: %v", err)
	}
	if got, want := location.RelativePath, "datasets/cats"; got != want {
		t.Fatalf("relative path = %q, want %q", got, want)
	}
}

func TestJobSpecValidateAcceptsLogicalDataLocationsAndRejectsMixedStorage(t *testing.T) {
	spec := specWithResources(1, 1)
	spec.Input = DataLocation{Space: DataSpaceTeamShared, RelativePath: "datasets/v1"}
	spec.Output = DataLocation{Space: DataSpaceMyRuns}
	if err := spec.Validate(); err != nil {
		t.Fatalf("logical data locations rejected: %v", err)
	}
	spec.DatasetStorage = StorageSelection{AssetID: "legacy-dataset"}
	if err := spec.Validate(); err == nil {
		t.Fatal("mixed logical and legacy storage was accepted")
	}
}

func TestJobSpecValidateAcceptsSupportedDataLocations(t *testing.T) {
	spec := specWithResources(1, 1)
	spec.DatasetURI = "tos://training-data/datasets/support-v1/"
	spec.CheckpointURI = "idc:///teams/nlp/checkpoints/base/"
	spec.OutputURI = "idc:///teams/nlp/outputs/run-01/"

	if err := spec.Validate(); err != nil {
		t.Fatalf("expected supported data locations to be accepted: %v", err)
	}
}

func TestJobSpecValidateRejectsUnsafeDataLocations(t *testing.T) {
	cases := []struct {
		name  string
		apply func(*JobSpec)
	}{
		{
			name: "unsupported scheme",
			apply: func(spec *JobSpec) {
				spec.DatasetURI = "https://storage.example/dataset"
			},
		},
		{
			name: "tos traversal",
			apply: func(spec *JobSpec) {
				spec.OutputURI = "tos://training-data/outputs/../other-team"
			},
		},
		{
			name: "empty idc path",
			apply: func(spec *JobSpec) {
				spec.CheckpointURI = "idc:///"
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec := specWithResources(1, 1)
			tc.apply(&spec)
			if err := spec.Validate(); err == nil {
				t.Fatalf("unsafe data location was accepted: %+v", spec)
			}
		})
	}
}

func TestJobSpecValidateUsesStorageSelectionsInsteadOfMixedURIs(t *testing.T) {
	spec := specWithResources(1, 1)
	spec.DatasetStorage = StorageSelection{AssetID: "dataset-a", RelativePath: "train"}
	if err := spec.Validate(); err != nil {
		t.Fatalf("storage selection should validate: %v", err)
	}

	spec.DatasetURI = "tos://training-data/datasets/train"
	if err := spec.Validate(); err == nil {
		t.Fatal("mixed legacy URI and storage asset selection was accepted")
	}

	spec = specWithResources(1, 1)
	spec.OutputStorage = StorageSelection{AssetID: "output-a", RelativePath: "user-controlled"}
	if err := spec.Validate(); err == nil {
		t.Fatal("output subdirectory override was accepted")
	}
}
