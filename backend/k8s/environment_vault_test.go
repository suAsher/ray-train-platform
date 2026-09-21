package k8s

import (
 "context"
 "testing"
 "time"
 corev1 "k8s.io/api/core/v1"
 metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
 "k8s.io/client-go/kubernetes/fake"
)

func TestEnvironmentVaultIsImmutableAndOwnerScoped(t *testing.T) {
 ctx:=context.Background()
 client:=NewClientFromInterfaces(nil,fake.NewSimpleClientset())
 vault:=NewEnvironmentVault(client,"platform")
 expires:=time.Now().Add(time.Hour)
 if err:=vault.Put(ctx,"auth-a",[]byte("encrypted-payload"),expires);err!=nil {t.Fatal(err)}
 got,err:=vault.Get(ctx,"auth-a");if err!=nil || string(got)!="encrypted-payload" {t.Fatalf("get = %q, %v",got,err)}
 if err:=vault.Put(ctx,"auth-a",[]byte("other-ciphertext"),expires);err==nil {t.Fatal("overwrote immutable ciphertext")}
 foreign:=&corev1.Secret{ObjectMeta:metav1.ObjectMeta{Name:environmentResourceName("auth","auth-b"),Namespace:"platform"},Data:map[string][]byte{"ciphertext":[]byte("other-owner")}}
 if _,err:=client.kubernetes.CoreV1().Secrets("platform").Create(ctx,foreign,metav1.CreateOptions{});err!=nil {t.Fatal(err)}
 if _,err:=vault.Get(ctx,"auth-b");err==nil {t.Fatal("read foreign secret")}
 if err:=vault.Delete(ctx,"auth-b");err==nil {t.Fatal("deleted foreign secret")}
 if err:=vault.Delete(ctx,"auth-a");err!=nil {t.Fatal(err)}
 if err:=vault.Delete(ctx,"auth-a");err!=nil {t.Fatal(err)}
}

func TestEnvironmentVaultRejectsExpiredCiphertext(t *testing.T) {
 vault:=NewEnvironmentVault(NewClientFromInterfaces(nil,fake.NewSimpleClientset()),"platform")
 if err:=vault.Put(context.Background(),"auth",[]byte("ciphertext"),time.Now().Add(-time.Second));err==nil {t.Fatal("accepted expired authorization")}
}
