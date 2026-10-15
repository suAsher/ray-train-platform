package personalstorage
import (
 "context"
 "errors"
 "testing"
 corev1 "k8s.io/api/core/v1"
 metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
 "k8s.io/client-go/kubernetes/fake"
)
func TestPersonalStorageVaultRejectsForeignSecret(t *testing.T){
 ctx:=context.Background();client:=fake.NewSimpleClientset(&corev1.Secret{ObjectMeta:metav1.ObjectMeta{Name:"personal-owner",Namespace:"credentials"},Data:map[string][]byte{"privateKey":[]byte("foreign")}})
 vault:=NewKubernetesVault(client,"credentials");b:=Binding{ID:"owner",OwnerID:"owner",Username:"guofeng.su",SecretName:"personal-owner",Generation:1}
 if _,err:=vault.Load(ctx,b);!errors.Is(err,ErrConflict){t.Fatalf("foreign Secret accepted: %v",err)}
 if _,err:=vault.Ensure(ctx,b,[]byte("unused"));!errors.Is(err,ErrConflict){t.Fatalf("foreign Secret overwritten: %v",err)}
}
