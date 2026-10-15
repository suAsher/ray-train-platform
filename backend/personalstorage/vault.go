package personalstorage

import (
	"context"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"strconv"
)

const (
	LabelPersonalStorage = "raytrain.wellspiking.ai/personal-storage"
	LabelBindingID       = "raytrain.wellspiking.ai/binding-id"
	AnnotationOwnerID    = "raytrain.wellspiking.ai/owner-id"
)

type KubernetesVault struct {
	client    kubernetes.Interface
	namespace string
}

func NewKubernetesVault(client kubernetes.Interface, namespace string) *KubernetesVault {
	return &KubernetesVault{client: client, namespace: namespace}
}
func (v *KubernetesVault) Ensure(ctx context.Context, b Binding, key []byte) (Credential, error) {
	if existing, err := v.Load(ctx, b); err == nil {
		return existing, nil
	} else if err != ErrNotFound {
		return Credential{}, err
	}
	if _, _, err := publicKey(key); err != nil {
		return Credential{}, ErrConflict
	}
	immutable := true
	sec := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: b.SecretName, Namespace: v.namespace, Labels: map[string]string{LabelPersonalStorage: "true", LabelBindingID: b.ID}, Annotations: map[string]string{AnnotationOwnerID: b.OwnerID}}, Type: corev1.SecretTypeOpaque, Immutable: &immutable, Data: map[string][]byte{"ownerID": []byte(b.OwnerID), "bindingID": []byte(b.ID), "username": []byte(b.Username), "generation": []byte(strconv.FormatInt(b.Generation, 10)), "privateKey": append([]byte(nil), key...)}}
	created, err := v.client.CoreV1().Secrets(v.namespace).Create(ctx, sec, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		return v.Load(ctx, b)
	}
	if err != nil {
		return Credential{}, ErrUnavailable
	}
	return credentialFromSecret(created, b)
}
func (v *KubernetesVault) Load(ctx context.Context, b Binding) (Credential, error) {
	if v == nil || v.client == nil || v.namespace == "" || b.SecretName == "" {
		return Credential{}, ErrUnavailable
	}
	sec, err := v.client.CoreV1().Secrets(v.namespace).Get(ctx, b.SecretName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return Credential{}, ErrNotFound
	}
	if err != nil {
		return Credential{}, ErrUnavailable
	}
	return credentialFromSecret(sec, b)
}
func credentialFromSecret(sec *corev1.Secret, b Binding) (Credential, error) {
	if sec.Type != corev1.SecretTypeOpaque || sec.Immutable == nil || !*sec.Immutable || sec.Labels[LabelPersonalStorage] != "true" || sec.Labels[LabelBindingID] != b.ID || sec.Annotations[AnnotationOwnerID] != b.OwnerID {
		return Credential{}, ErrConflict
	}
	data := sec.Data
	if string(data["ownerID"]) != b.OwnerID || string(data["bindingID"]) != b.ID || string(data["username"]) != b.Username || string(data["generation"]) != strconv.FormatInt(b.Generation, 10) {
		return Credential{}, ErrConflict
	}
	if _, _, err := publicKey(data["privateKey"]); err != nil {
		return Credential{}, ErrConflict
	}
	return Credential{OwnerID: b.OwnerID, BindingID: b.ID, Username: b.Username, Generation: b.Generation, PrivateKey: append([]byte(nil), data["privateKey"]...)}, nil
}
