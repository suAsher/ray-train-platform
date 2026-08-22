package k8s

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var localQueueGVR = schema.GroupVersionResource{Group: "kueue.x-k8s.io", Version: "v1beta2", Resource: "localqueues"}

func (c *Client) EnsureLocalQueue(ctx context.Context, namespace, queueName, clusterQueueName string) error {
	if c == nil || c.dynamic == nil {
		return fmt.Errorf("Kubernetes dynamic client is not initialized")
	}
	if namespace == "" || queueName == "" || clusterQueueName == "" {
		return fmt.Errorf("Kueue LocalQueue namespace and names are required")
	}
	gvr := c.kueueGVR
	if gvr.Version == "" {
		gvr = localQueueGVR
	}
	queues := c.dynamic.Resource(gvr).Namespace(namespace)
	existing, err := queues.Get(ctx, queueName, metav1.GetOptions{})
	if err == nil {
		clusterQueue, _, _ := unstructured.NestedString(existing.Object, "spec", "clusterQueue")
		if clusterQueue != clusterQueueName {
			return fmt.Errorf("LocalQueue %s/%s points to unexpected ClusterQueue", namespace, queueName)
		}
		return nil
	}
	if !apierrors.IsNotFound(err) {
		return fmt.Errorf("get LocalQueue: %w", err)
	}
	manifest := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": gvr.Group + "/" + gvr.Version,
		"kind":       "LocalQueue",
		"metadata": map[string]any{
			"name":      queueName,
			"namespace": namespace,
			"labels": map[string]any{
				"app.kubernetes.io/part-of":    "ray-train-platform",
				"app.kubernetes.io/managed-by": "ray-train-platform",
			},
		},
		"spec": map[string]any{"clusterQueue": clusterQueueName},
	}}
	if _, err := queues.Create(ctx, manifest, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("create LocalQueue: %w", err)
	}
	return nil
}
