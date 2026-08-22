package domain

import "testing"

func TestWorkspaceSnapshotPrefixIsOwnerScoped(t *testing.T) {
	prefix, err := WorkspaceSnapshotPrefix("team-a", "user-a", "snapshot-a")
	if err != nil {
		t.Fatal(err)
	}
	if prefix != "ray-train/tenants/team-a/users/user-a/snapshots/snapshot-a/" {
		t.Fatalf("prefix=%q", prefix)
	}
	if _, err := WorkspaceSnapshotPrefix("team-a", "../other", "snapshot-a"); err == nil {
		t.Fatal("unsafe owner was accepted")
	}
}

func TestWorkspaceSnapshotPrefixUsesThePersistedStorageRoot(t *testing.T) {
	prefix, err := WorkspaceSnapshotPrefixForRoot("team-a", "ray-train/tenants/team-a/users/guofeng.su/", "snapshot-a")
	if err != nil {
		t.Fatal(err)
	}
	if prefix != "ray-train/tenants/team-a/users/guofeng.su/snapshots/snapshot-a/" {
		t.Fatalf("prefix=%q", prefix)
	}
}

func TestWorkspaceSnapshotRejectsEmptyOrUnsafeSource(t *testing.T) {
	base := WorkspaceSnapshot{ID: "snapshot-a", TenantID: "team-a", UserID: "user-a", FileCount: 1}
	if err := base.Validate(); err != nil {
		t.Fatalf("valid root snapshot: %v", err)
	}
	unsafe := base
	unsafe.SourcePath = "../other"
	if err := unsafe.Validate(); err == nil {
		t.Fatal("unsafe source path was accepted")
	}
	empty := base
	empty.FileCount = 0
	if err := empty.Validate(); err == nil {
		t.Fatal("empty snapshot was accepted")
	}
}
