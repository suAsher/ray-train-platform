package personalstorage

import (
	"context"
	"errors"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	"testing"
)

func TestPersonalStorageVaultRejectsForeignSecret(t *testing.T) {
	ctx := context.Background()
	client := fake.NewSimpleClientset(&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "personal-owner", Namespace: "credentials"}, Data: map[string][]byte{"privateKey": []byte("foreign")}})
	vault := NewKubernetesVault(client, "credentials")
	b := Binding{ID: "owner", OwnerID: "owner", Username: "guofeng.su", SecretName: "personal-owner", Generation: 1}
	if _, err := vault.Load(ctx, b); !errors.Is(err, ErrConflict) {
		t.Fatalf("foreign Secret accepted: %v", err)
	}
	if _, err := vault.Ensure(ctx, b, []byte("unused")); !errors.Is(err, ErrConflict) {
		t.Fatalf("foreign Secret overwritten: %v", err)
	}
}
func TestPersonalStorageVaultAtomicWinnerAndImmutability(t *testing.T) {
	client := fake.NewSimpleClientset()
	vault := NewKubernetesVault(client, "credentials")
	ctx := context.Background()
	seed := newTestService()
	view, err := seed.Enroll(ctx, Owner{ID: "owner", Username: "guofeng.su"})
	if err != nil {
		t.Fatal(err)
	}
	key := seed.vault.(*memoryVault).rows["owner"].PrivateKey
	binding := Binding{ID: view.BindingID, OwnerID: "owner", Username: "guofeng.su", SecretName: "personal-owner", Generation: 1}
	first, err := vault.Ensure(ctx, binding, key)
	if err != nil {
		t.Fatal(err)
	}
	again, err := vault.Ensure(ctx, binding, key)
	if err != nil || string(first.PrivateKey) != string(again.PrivateKey) {
		t.Fatal("Secret atomic winner changed")
	}
	binding.OwnerID = "foreign"
	if _, err := vault.Load(ctx, binding); !errors.Is(err, ErrConflict) {
		t.Fatal("foreign owner loaded Secret")
	}
}
func TestPersonalStorageVaultRejectsCredentialMetadataMismatch(t *testing.T) {
	ctx := context.Background()
	seed := newTestService()
	view, err := seed.Enroll(ctx, Owner{ID: "owner", Username: "guofeng.su"})
	if err != nil {
		t.Fatal(err)
	}
	key := seed.vault.(*memoryVault).rows["owner"].PrivateKey
	b := Binding{ID: view.BindingID, OwnerID: "owner", Username: "guofeng.su", SecretName: "personal-owner", Generation: 1}
	client := fake.NewSimpleClientset()
	vault := NewKubernetesVault(client, "credentials")
	if _, err := vault.Ensure(ctx, b, key); err != nil {
		t.Fatal(err)
	}
	original, err := client.CoreV1().Secrets("credentials").Get(ctx, b.SecretName, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	changes := []func(*corev1.Secret){func(s *corev1.Secret) { s.Immutable = nil }, func(s *corev1.Secret) { s.Labels[LabelPersonalStorage] = "false" }, func(s *corev1.Secret) { s.Data["ownerID"] = []byte("foreign") }, func(s *corev1.Secret) { s.Data["username"] = []byte("foreign") }, func(s *corev1.Secret) { s.Data["generation"] = []byte("2") }, func(s *corev1.Secret) { s.Data["privateKey"] = []byte("invalid") }}
	for _, change := range changes {
		secret := original.DeepCopy()
		change(secret)
		client := fake.NewSimpleClientset(secret)
		if _, err := NewKubernetesVault(client, "credentials").Load(ctx, b); !errors.Is(err, ErrConflict) {
			t.Fatal("mismatched Secret accepted")
		}
	}
}
