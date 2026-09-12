package integrations
import "testing"
func TestIntegrationScopeAndPermissionValidationDoesNotElevate(t *testing.T){
 for _,input:=range [][]string{nil,{"jobs:read"},{"experiments:write"},{"experiments:read","artifacts:write"},{"experiments:read","mlflow:write"}}{if _,err:=NormalizeScopes(input);err==nil{t.Fatalf("accepted invalid scopes %v",input)}}
 for _,input:=range [][]string{{"experiments:read"},{"experiments:read","experiments:write","artifacts:read","artifacts:write"}}{got,err:=NormalizeScopes(input);if err!=nil||len(got)!=len(input){t.Fatalf("scopes %v -> %v %v",input,got,err)}}
 for _,input:=range [][]string{nil,{"write"},{"read","artifacts:write"},{"read","jobs:read"}}{if _,err:=NormalizePermissions(input);err==nil{t.Fatalf("accepted invalid permissions %v",input)}}
}
