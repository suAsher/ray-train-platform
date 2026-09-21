package k8s

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"ray-train-platform-backend/environmentbuild"
	"time"
)

const environmentManagedLabel = "platform.wellspiking.ai/environment-build"
const environmentOwnerLabel = "platform.wellspiking.ai/environment-owner"
const environmentIdentityAnnotation = "platform.wellspiking.ai/environment-identity"
const environmentExpiryAnnotation = "platform.wellspiking.ai/environment-expires-at"

func environmentHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:16])
}
func environmentResourceName(kind, id string) string {
	return "rt-env-" + kind + "-" + environmentHash(id)
}
func environmentLabels(id string) map[string]string {
	return map[string]string{"app.kubernetes.io/managed-by": "ray-train-platform", environmentManagedLabel: environmentHash(id)}
}
func environmentOwned(meta metav1.Object, id string) bool {
	return meta.GetLabels()["app.kubernetes.io/managed-by"] == "ray-train-platform" && meta.GetLabels()[environmentManagedLabel] == environmentHash(id) && meta.GetAnnotations()[environmentIdentityAnnotation] == id
}
func environmentDeleteOptions(meta metav1.Object) metav1.DeleteOptions {
	uid := meta.GetUID()
	rv := meta.GetResourceVersion()
	return metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid, ResourceVersion: &rv}}
}

type EnvironmentVault struct {
	client    *Client
	namespace string
}

var _ environmentbuild.Vault = (*EnvironmentVault)(nil)

func NewEnvironmentVault(client *Client, namespace string) *EnvironmentVault {
	return &EnvironmentVault{client: client, namespace: namespace}
}
func (v *EnvironmentVault) Put(ctx context.Context, id string, ciphertext []byte, expires time.Time) error {
	if v.client == nil || v.client.kubernetes == nil || !isDNSLabel(v.namespace) || id == "" || len(ciphertext) == 0 || len(ciphertext) > 64*1024 || !expires.After(time.Now()) {
		return environmentbuild.ErrInvalid
	}
	immutable := true
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: environmentResourceName("auth", id), Namespace: v.namespace, Labels: environmentLabels(id), Annotations: map[string]string{environmentIdentityAnnotation: id, environmentExpiryAnnotation: expires.UTC().Format(time.RFC3339Nano)}}, Type: corev1.SecretTypeOpaque, Immutable: &immutable, Data: map[string][]byte{"ciphertext": append([]byte(nil), ciphertext...)}}
	_, err := v.client.kubernetes.CoreV1().Secrets(v.namespace).Create(ctx, secret, metav1.CreateOptions{})
	if !apierrors.IsAlreadyExists(err) {
		return err
	}
	existing, err := v.client.kubernetes.CoreV1().Secrets(v.namespace).Get(ctx, secret.Name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if !environmentOwned(existing, id) || existing.Immutable == nil || !*existing.Immutable || !bytes.Equal(existing.Data["ciphertext"], ciphertext) {
		return fmt.Errorf("refusing to replace environment authorization")
	}
	return nil
}
func (v *EnvironmentVault) Get(ctx context.Context, id string) ([]byte, error) {
	if v.client == nil || v.client.kubernetes == nil || !isDNSLabel(v.namespace) || id == "" {
		return nil, environmentbuild.ErrInvalid
	}
	secret, err := v.client.kubernetes.CoreV1().Secrets(v.namespace).Get(ctx, environmentResourceName("auth", id), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, environmentbuild.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if !environmentOwned(secret, id) || secret.Type != corev1.SecretTypeOpaque || secret.Immutable == nil || !*secret.Immutable {
		return nil, fmt.Errorf("refusing to read unmanaged environment authorization")
	}
	expires, err := time.Parse(time.RFC3339Nano, secret.Annotations[environmentExpiryAnnotation])
	if err != nil || !expires.After(time.Now()) {
		return nil, environmentbuild.ErrAuthorization
	}
	if len(secret.Data["ciphertext"]) == 0 {
		return nil, environmentbuild.ErrAuthorization
	}
	return append([]byte(nil), secret.Data["ciphertext"]...), nil
}
func (v *EnvironmentVault) Delete(ctx context.Context, id string) error {
	if v.client == nil || v.client.kubernetes == nil || !isDNSLabel(v.namespace) || id == "" {
		return environmentbuild.ErrInvalid
	}
	secrets := v.client.kubernetes.CoreV1().Secrets(v.namespace)
	secret, err := secrets.Get(ctx, environmentResourceName("auth", id), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !environmentOwned(secret, id) {
		return fmt.Errorf("refusing to delete unmanaged environment authorization")
	}
	err = secrets.Delete(ctx, secret.Name, environmentDeleteOptions(secret))
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}
