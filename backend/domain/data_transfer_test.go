package domain

import "testing"

func TestNewDataTransferUsesLogicalPersonalDestinationAndCleanPaths(t *testing.T) {
	transfer, err := NewDataTransfer(
		"transfer-1", "tenant-a", "user-a", DataTransferIDCToTOS,
		"projects/demo/raw", DataLocation{Space: DataSpaceMyFiles, RelativePath: "datasets/demo/raw"},
	)
	if err != nil {
		t.Fatalf("new transfer: %v", err)
	}
	if transfer.State != DataTransferQueued {
		t.Fatalf("state = %q, want queued", transfer.State)
	}
	if transfer.IDCRelativePath != "projects/demo/raw" || transfer.TOSLocation.RelativePath != "datasets/demo/raw" {
		t.Fatalf("transfer paths were not preserved as clean logical paths: %#v", transfer)
	}
}

func TestNewDataTransferRejectsCrossScopeAndUnsafePaths(t *testing.T) {
	cases := []struct {
		name      string
		direction DataTransferDirection
		idcPath   string
		location  DataLocation
	}{
		{name: "IDC traversal", direction: DataTransferIDCToTOS, idcPath: "../other", location: DataLocation{Space: DataSpaceMyFiles}},
		{name: "IDC absolute path", direction: DataTransferIDCToTOS, idcPath: "/other", location: DataLocation{Space: DataSpaceMyFiles}},
		{name: "shared destination", direction: DataTransferIDCToTOS, idcPath: "dataset", location: DataLocation{Space: DataSpaceTeamShared}},
		{name: "public destination", direction: DataTransferIDCToTOS, idcPath: "dataset", location: DataLocation{Space: DataSpacePublic}},
		{name: "runs destination", direction: DataTransferIDCToTOS, idcPath: "dataset", location: DataLocation{Space: DataSpaceMyRuns}},
		{name: "unknown direction", direction: DataTransferDirection("erase"), idcPath: "dataset", location: DataLocation{Space: DataSpaceMyFiles}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewDataTransfer("transfer-1", "tenant-a", "user-a", tc.direction, tc.idcPath, tc.location)
			if err == nil {
				t.Fatalf("unsafe transfer was accepted: %#v", tc)
			}
		})
	}
}

func TestDataTransferStateDoesNotAllowTerminalMutation(t *testing.T) {
	transfer, err := NewDataTransfer("transfer-1", "tenant-a", "user-a", DataTransferTOSToIDC, "results/demo", DataLocation{Space: DataSpaceMyFiles, RelativePath: "results/demo"})
	if err != nil {
		t.Fatalf("new transfer: %v", err)
	}
	if err := transfer.TransitionTo(DataTransferRunning); err != nil {
		t.Fatalf("start transfer: %v", err)
	}
	if err := transfer.TransitionTo(DataTransferSucceeded); err != nil {
		t.Fatalf("complete transfer: %v", err)
	}
	if err := transfer.TransitionTo(DataTransferRunning); err == nil {
		t.Fatal("terminal transfer was allowed to restart")
	}
}
