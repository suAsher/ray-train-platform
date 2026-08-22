package k8s

import (
	"context"
	"fmt"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ResolveRayDashboardService discovers the KubeRay-created Head service by
// labels instead of guessing a possibly truncated service name.
func (c *Client) ResolveRayDashboardService(ctx context.Context, namespace, rayClusterName string) (string, error) {
	if c == nil || c.kubernetes == nil {
		return "", fmt.Errorf("Kubernetes client is not initialized")
	}
	namespace = strings.TrimSpace(namespace)
	rayClusterName = strings.TrimSpace(rayClusterName)
	if namespace == "" || rayClusterName == "" {
		return "", fmt.Errorf("namespace and RayCluster name are required")
	}
	selector := "ray.io/cluster=" + rayClusterName + ",ray.io/node-type=head"
	services, err := c.kubernetes.CoreV1().Services(namespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return "", fmt.Errorf("list Ray head services: %w", err)
	}
	for _, service := range services.Items {
		for _, port := range service.Spec.Ports {
			if port.Port == 8265 || port.Name == "dashboard" {
				return fmt.Sprintf("http://%s.%s.svc.cluster.local:%d", service.Name, namespace, port.Port), nil
			}
		}
	}
	return "", fmt.Errorf("Ray Dashboard is not available for RayCluster %s/%s", namespace, rayClusterName)
}
