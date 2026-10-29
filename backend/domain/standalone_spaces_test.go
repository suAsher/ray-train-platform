package domain

import "testing"

func TestStandaloneDataCatalogRejectsCompanyRoots(t *testing.T) {
 for _, id := range []DataSpaceID{DataSpaceIDCOriginal, DataSpaceIDCWellspiking, DataSpaceIDCShared, DataSpaceIDCSPKHybrid, DataSpaceIDCSPKSSD, DataSpaceWellspikingPersonal} {
  if IsKnownDataSpace(id) { t.Fatalf("legacy company space %q is accepted", id) }
  if _, err := NewDataLocation(id, "dataset"); err == nil { t.Fatalf("legacy company selection %q accepted", id) }
 }
 spaces, err := PersonalDataSpacesFor("team", "user")
 if err != nil { t.Fatal(err) }
 for _, space := range spaces { if space.Provider == StorageProviderIDC { t.Fatalf("legacy source in catalog: %s", space.ID) } }
}
