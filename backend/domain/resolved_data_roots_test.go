package domain

import "testing"

func TestResolvedDataSpaceRootsAcceptsPersonalAndReadonlyRoots(t *testing.T) {
	roots := ResolvedDataSpaceRoots{
		Personal:    &ResolvedDataRoot{Space: DataSpaceWorkspace, ClaimName: "data-user-a"},
		Team:        &ResolvedDataRoot{Space: DataSpaceTeamShared, ClaimName: "data-team-a", ReadOnly: true},
		IDCOriginal: &ResolvedDataRoot{Space: DataSpaceIDCOriginal, ClaimName: "idc-original-ro", ReadOnly: true},
	}
	if err := roots.Validate(); err != nil {
		t.Fatalf("resolved data roots rejected: %v", err)
	}
}

func TestResolvedDataSpaceRootsRejectsWritablePublicRoot(t *testing.T) {
	roots := ResolvedDataSpaceRoots{
		Public: &ResolvedDataRoot{Space: DataSpacePublic, ClaimName: "data-public"},
	}
	if err := roots.Validate(); err == nil {
		t.Fatal("writable shared root was accepted")
	}
}
