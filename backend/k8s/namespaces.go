package k8s

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EnsureNamespace creates a tenant's namespace if it does not exist. Training
// Pods run there, so a tenant without one cannot run anything.
func (c *Client) EnsureNamespace(ctx context.Context, name, tenantID string) error {
	if c == nil || c.kubernetes == nil {
		return fmt.Errorf("Kubernetes client is not initialized")
	}
	namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name: name,
		Labels: map[string]string{
			"app.kubernetes.io/part-of":    "ray-train-platform",
			"app.kubernetes.io/managed-by": "ray-train-platform",
			"ray.io/tenant-id":             tenantID,
		},
	}}
	_, err := c.kubernetes.CoreV1().Namespaces().Create(ctx, namespace, metav1.CreateOptions{})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("create namespace: %w", err)
	}
	return nil
}
