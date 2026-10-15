package api
import (
 "testing"
 "ray-train-platform-backend/auth"
)
func TestPersonalStorageOwnerUsesStableIdentityNotTeam(t *testing.T) {
 first, err := personalStorageOwner(auth.Principal{Subject:"platform-user-1",Username:"guofeng.su",TenantID:"team-a",AuthType:auth.AuthTypeOIDC})
 if err != nil { t.Fatal(err) }
 second, err := personalStorageOwner(auth.Principal{Subject:"platform-user-1",Username:"guofeng.su",TenantID:"team-b",AuthType:auth.AuthTypeOAuth2Proxy})
 if err != nil || first != second || first.ID != "platform-user-1" { t.Fatalf("team must not select personal storage: %#v %#v %v", first,second,err) }
}
func TestPersonalStorageRejectsMachineAndMissingIdentity(t *testing.T) {
 for _, principal := range []auth.Principal{{Subject:"alice",Username:"alice",AuthType:auth.AuthTypePAT},{Username:"alice",AuthType:auth.AuthTypeOIDC},{Subject:"alice",AuthType:auth.AuthTypeOIDC}} {
  if _, err := personalStorageOwner(principal); err == nil { t.Fatalf("must reject invalid owner: %#v", principal) }
 }
}
