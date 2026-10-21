package domain

import "testing"

func TestTeamStorageRootSupportsWritableAndLegacySnapshots(t *testing.T) {
	for _, readOnly := range []bool{false, true} {
		roots := ResolvedDataSpaceRoots{Team: &ResolvedDataRoot{Space: DataSpaceTeamShared, ClaimName: "data-tenant-a", SubPath: "tenants/tenant-a/shared", ReadOnly: readOnly}}
		if err := roots.Validate(); err != nil {
			t.Errorf("team readOnly=%v: %v", readOnly, err)
		}
	}
	for _, space := range []DataSpaceID{DataSpacePublic, DataSpaceIDCOriginal} {
		roots := ResolvedDataSpaceRoots{}
		value := &ResolvedDataRoot{Space: space, ClaimName: "data-a"}
		if space == DataSpacePublic {
			roots.Public = value
		} else {
			roots.IDCOriginal = value
		}
		if roots.Validate() == nil {
			t.Errorf("writable %s accepted", space)
		}
	}
}

func TestTeamStorageMetadataSeparatesMountFromWebManagement(t *testing.T) {
	spaces := PersonalDataSpaces("tenant-a", "user-a")
	team, ok := FindDataSpace(spaces, DataSpaceTeamShared)
	if !ok || team.ReadOnly || team.RootPrefix != "ray-train/tenants/tenant-a/shared/" {
		t.Fatalf("team mount contract: %#v", team)
	}
	if CanManageDataSpace(DataSpaceTeamShared, false, false) {
		t.Fatal("container write must not authorize ordinary web uploads")
	}
	public, _ := FindDataSpace(spaces, DataSpacePublic)
	if !public.ReadOnly {
		t.Fatal("public mount became writable")
	}
}
