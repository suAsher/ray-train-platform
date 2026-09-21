package environmentbuild

import (
 "context"
 "strings"
 "testing"
 "time"
)
type encryptedTestVault struct{data []byte}
func(v *encryptedTestVault)Put(context.Context,string,[]byte,time.Time)error{return nil}
func(v *encryptedTestVault)Get(context.Context,string)([]byte,error){return v.data,nil}
func(v *encryptedTestVault)Delete(context.Context,string)error{return nil}
func TestCredentialCipherBindsOwnerTargetAndExpiry(t *testing.T){
 now:=time.Now();v:=&encryptedTestVault{};s:=&Service{config:Config{EncryptionKey:[]byte(strings.Repeat("x",32))},vault:v,now:func()time.Time{return now}}
 a:=Authorization{ID:"a",OwnerID:"u",TenantID:"t",Username:"h",BuildID:"b",Target:"project/repo",ExpiresAt:now.Add(time.Hour)}
 sealed,err:=s.encrypt(a,Credentials{Username:"h",Secret:"unique-sensitive-value"});if err!=nil{t.Fatal(err)};v.data=sealed
 if strings.Contains(string(sealed),"unique-sensitive-value"){t.Fatal("ciphertext contains secret")}
 c,err:=s.credentials(context.Background(),a);if err!=nil||c.Secret!="unique-sensitive-value"{t.Fatal("decrypt failed")}
 for _,mutate:=range []func(*Authorization){func(a *Authorization){a.OwnerID="other"},func(a *Authorization){a.TenantID="other"},func(a *Authorization){a.Target="other/repo"},func(a *Authorization){a.BuildID="other"},func(a *Authorization){a.ExpiresAt=now}}{changed:=a;mutate(&changed);if _,err=s.credentials(context.Background(),changed);err==nil{t.Fatal("cross-binding decrypt succeeded")}}
}
func TestEnvironmentPublicationRejectsUnsafeTargetsAndCredentials(t *testing.T){
 for _,target:=range [][2]string{{"public","../other"},{"public","repo:tag"},{"https://evil","repo"},{"public","repo@sha256:bad"},{"public",""}}{if validTarget(target[0],target[1]){t.Fatal("unsafe target accepted")}}
 for _,c:=range []Credentials{{Username:"user",Secret:"secret\n"},{Username:"user",Secret:""},{Username:" user",Secret:"secret"}}{if validCredential(c){t.Fatal("unsafe credentials accepted")}}
}

func TestPhaseErrorOnlyAllowsSafeClassification(t *testing.T){
 for _,code:=range []string{"UNSUPPORTED_WORKSPACE","ENVIRONMENT_CHANGED","WHEEL_UNAVAILABLE","PACKAGE_MODIFIED","BUILD_TIMEOUT","PULL_FAILED","TEMP_STORAGE_FULL"}{
  if phaseMessage(code)==""||(&PhaseError{Code:code}).Error()!=code{t.Fatalf("missing safe classification %s",code)}
 }
 secret:="http://user:test-secret@unexpected.invalid/registry"
 if phaseMessage(secret)!=""||strings.Contains((&PhaseError{Code:secret}).Error(),"test-secret"){t.Fatal("unknown diagnostic exposed")}
}
