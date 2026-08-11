package k8s

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EnsureGitCredentialSecret writes a private-repository token into the tenant
// namespace. Keeping it in Kubernetes rather than the platform database means
// a database dump never contains usable repository credentials, and the
// materializer reads it through the normal Secret mechanism.
func (c *Client) EnsureGitCredentialSecret(ctx context.Context, namespace, name, username, token string) error {
	if c == nil || c.kubernetes == nil {
		return fmt.Errorf("Kubernetes client is not initialized")
	}
	if namespace == "" || name == "" || token == "" {
		return fmt.Errorf("namespace, name and token are required")
	}
	if username == "" {
		// Most providers accept any non-empty username when a token is used as
		// the password.
		username = "git"
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels:    map[string]string{"app.kubernetes.io/managed-by": "ray-train-platform"},
		},
		Type: corev1.SecretTypeOpaque,
		StringData: map[string]string{
			"GIT_USERNAME": username,
			"GIT_TOKEN":    token,
		},
	}
	secrets := c.kubernetes.CoreV1().Secrets(namespace)
	_, err := secrets.Create(ctx, secret, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		if _, err = secrets.Update(ctx, secret, metav1.UpdateOptions{}); err != nil {
			return fmt.Errorf("update git credential secret: %w", err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("create git credential secret: %w", err)
	}
	return nil
}

func (c *Client) DeleteSecret(ctx context.Context, namespace, name string) error {
	if c == nil || c.kubernetes == nil {
		return fmt.Errorf("Kubernetes client is not initialized")
	}
	err := c.kubernetes.CoreV1().Secrets(namespace).Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete secret: %w", err)
	}
	return nil
}
