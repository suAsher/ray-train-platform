package k8s

import (
	"context"
	"fmt"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func TestEnsureGitCredentialSecretRotatesExistingSecretWithResourceVersion(t *testing.T) {
	const namespace = "tenant-a"
	const name = "git-cred-gitexamplecom"
	kube := k8sfake.NewSimpleClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: namespace, ResourceVersion: "17",
			Labels: map[string]string{"app.kubernetes.io/managed-by": "ray-train-platform"},
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{"GIT_TOKEN": []byte("old-token")},
	})

	var updated *corev1.Secret
	kube.PrependReactor("update", "secrets", func(action k8stesting.Action) (bool, runtime.Object, error) {
		candidate := action.(k8stesting.UpdateAction).GetObject().(*corev1.Secret)
		updated = candidate.DeepCopy()
		if candidate.ResourceVersion != "17" {
			return true, nil, fmt.Errorf("update missing existing resource version")
		}
		return false, nil, nil
	})

	client := &Client{kubernetes: kube}
	if err := client.EnsureGitCredentialSecret(context.Background(), namespace, name, "token-user", "new-token"); err != nil {
		t.Fatalf("rotate git credential secret: %v", err)
	}
	if updated == nil {
		t.Fatal("expected an update for the existing secret")
	}
	if got := updated.StringData["GIT_USERNAME"]; got != "token-user" {
		t.Fatalf("unexpected rotated username %q", got)
	}
	if got := updated.StringData["GIT_TOKEN"]; got != "new-token" {
		t.Fatalf("unexpected rotated token %q", got)
	}
}

func TestEnsureImagePullSecretCopiesOnlyTheNamedPlatformSecret(t *testing.T) {
	const sourceNamespace = "ray-train-platform"
	const targetNamespace = "tenant-local"
	const name = "registry-pull"
	source := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: sourceNamespace},
		Type:       corev1.SecretTypeDockerConfigJson,
		Data:       map[string][]byte{corev1.DockerConfigJsonKey: []byte(`{"auths":{"registry.example":{"auth":"token"}}}`)},
	}
	client := &Client{kubernetes: k8sfake.NewSimpleClientset(source)}

	if err := client.EnsureImagePullSecret(context.Background(), sourceNamespace, targetNamespace, name); err != nil {
		t.Fatalf("copy image pull secret: %v", err)
	}
	copied, err := client.kubernetes.CoreV1().Secrets(targetNamespace).Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get copied image pull secret: %v", err)
	}
	if copied.Type != corev1.SecretTypeDockerConfigJson || string(copied.Data[corev1.DockerConfigJsonKey]) != string(source.Data[corev1.DockerConfigJsonKey]) {
		t.Fatalf("image pull secret was not copied faithfully: %#v", copied)
	}
	if copied.Labels["app.kubernetes.io/managed-by"] != "ray-train-platform" || copied.Labels["ray-train-platform/source-secret"] != name {
		t.Fatalf("copied image pull secret lacks ownership labels: %#v", copied.Labels)
	}
}

func TestEnsureIDCPrivateKeySecretCreatesOwnerScopedNonRotatingKey(t *testing.T) {
	const namespace = "tenant-team-a"
	const name = "idc-sftp-user-a"
	client := &Client{kubernetes: k8sfake.NewSimpleClientset()}
	if err := client.EnsureIDCPrivateKeySecret(context.Background(), namespace, name, []byte("private-key-material")); err != nil {
		t.Fatalf("create IDC private-key Secret: %v", err)
	}
	secret, err := client.kubernetes.CoreV1().Secrets(namespace).Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get IDC private-key Secret: %v", err)
	}
	if secret.Type != corev1.SecretTypeOpaque || string(secret.Data["id_ed25519"]) != "private-key-material" {
		t.Fatalf("unexpected IDC private-key Secret: %#v", secret)
	}
	if secret.Labels["app.kubernetes.io/managed-by"] != "ray-train-platform" || secret.Labels["platform.wellspiking.ai/idc-sftp-key"] != "true" {
		t.Fatalf("IDC private-key Secret lacks ownership labels: %#v", secret.Labels)
	}
	if err := client.EnsureIDCPrivateKeySecret(context.Background(), namespace, name, []byte("replacement-key")); err != nil {
		t.Fatalf("repeat IDC private-key Secret creation: %v", err)
	}
	secret, _ = client.kubernetes.CoreV1().Secrets(namespace).Get(context.Background(), name, metav1.GetOptions{})
	if string(secret.Data["id_ed25519"]) != "private-key-material" {
		t.Fatalf("existing IDC private key was rotated without an explicit rotation request: %#v", secret.Data)
	}
}
