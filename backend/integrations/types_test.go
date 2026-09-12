package integrations
import ("testing";"strings")
func TestIntegrationScopeAndPermissionValidationDoesNotElevate(t *testing.T){
 for _,input:=range [][]string{nil,{"jobs:read"},{"experiments:write"},{"experiments:read","artifacts:write"},{"experiments:read","mlflow:write"}}{if _,err:=NormalizeScopes(input);err==nil{t.Fatalf("accepted invalid scopes %v",input)}}
 for _,input:=range [][]string{{"experiments:read"},{"experiments:read","experiments:write","artifacts:read","artifacts:write"}}{got,err:=NormalizeScopes(input);if err!=nil||len(got)!=len(input){t.Fatalf("scopes %v -> %v %v",input,got,err)}}
 for _,input:=range [][]string{nil,{"write"},{"read","artifacts:write"},{"read","jobs:read"}}{if _,err:=NormalizePermissions(input);err==nil{t.Fatalf("accepted invalid permissions %v",input)}}
}
func TestIntegrationNamesIDsAndScopePermissionMapping(t *testing.T){
 for _,name:=range []string{""," leading","trailing ","line\nbreak",strings.Repeat("x",129)}{if ValidName(name){t.Fatalf("accepted name %q",name)}}
 if !ValidName("评估服务")||!ValidID(strings.Repeat("a",32))||ValidID(strings.Repeat("A",32))||ValidID("../../jobs"){t.Fatal("name/id validation failed")}
 got,err:=PermissionsFromScopes([]string{"artifacts:read","experiments:read","experiments:write"});if err!=nil||!HasPermission(got,"read")||!HasPermission(got,"write")||HasPermission(got,"artifacts:write"){t.Fatalf("scope mapping elevated: %v %v",got,err)}
 if _,err:=PermissionsFromScopes([]string{"jobs:read"});err==nil{t.Fatal("unsupported scope accepted")}
}
