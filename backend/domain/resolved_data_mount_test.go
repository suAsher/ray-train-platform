package domain

import "testing"

func TestResolvedDataSpaceMountsAcceptsGovernedInputAndOutput(t *testing.T) {
	mounts := ResolvedDataSpaceMounts{
		Input: &ResolvedDataMount{
			Space:        DataSpaceMyFiles,
			BindingSpace: DataSpaceWorkspace,
			ClaimName:    "data-user-a",
			SubPath:      "files/train-v1",
			MountPath:    DataMountInputPath,
			ReadOnly:     true,
		},
		Output: &ResolvedDataMount{
			Space:        DataSpaceMyRuns,
			BindingSpace: DataSpaceWorkspace,
			ClaimName:    "data-user-a",
			SubPath:      "runs/job-01",
			MountPath:    DataMountOutputPath,
			ReadOnly:     false,
		},
	}
	if err := mounts.Validate(); err != nil {
		t.Fatalf("governed data mounts rejected: %v", err)
	}
}

func TestResolvedDataSpaceMountsAcceptsTheDirectPersonalStorageRootAsInput(t *testing.T) {
	mounts := ResolvedDataSpaceMounts{
		Input: &ResolvedDataMount{
			Space:        DataSpaceID("my-storage"),
			BindingSpace: DataSpaceWorkspace,
			ClaimName:    "data-user-a",
			SubPath:      "datasets/train-v1",
			MountPath:    DataMountInputPath,
			ReadOnly:     true,
		},
	}
	if err := mounts.Validate(); err != nil {
		t.Fatalf("direct personal storage input was rejected: %v", err)
	}
}

func TestResolvedDataSpaceMountsRejectsArbitraryClaimOrOutputPath(t *testing.T) {
	mounts := ResolvedDataSpaceMounts{
		Output: &ResolvedDataMount{
			Space:        DataSpaceMyRuns,
			BindingSpace: DataSpaceWorkspace,
			ClaimName:    "other-tenant-data",
			SubPath:      "files/not-a-run",
			MountPath:    DataMountOutputPath,
			ReadOnly:     false,
		},
	}
	if err := mounts.Validate(); err == nil {
		t.Fatal("output outside the governed runs path was accepted")
	}
}
