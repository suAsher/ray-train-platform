package k8s

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var clusterQueueGVR = schema.GroupVersionResource{Group: "kueue.x-k8s.io", Version: "v1beta2", Resource: "clusterqueues"}

// SyncClusterQueueQuota rewrites the ClusterQueue's nominal quota to match the
// measured capacity of the training pool.
//
// Kueue has no capacity discovery: nominalQuota is a static, required field
// that an operator is expected to keep in step with the hardware. Deriving it
// from the labelled nodes means adding a machine is a one-step operation —
// label it, and the admission budget follows within one reconcile interval.
//
// It reports whether anything actually changed so the caller can avoid
// pointless writes.
func (c *Client) SyncClusterQueueQuota(ctx context.Context, clusterQueueName string, capacity TrainingPoolCapacity) (bool, error) {
	return c.syncClusterQueueQuota(ctx, clusterQueueName, capacity)
}

// SyncClusterQueueFlavorQuotas updates each accelerator flavor from only the
// nodes of that class. This prevents an A100/H20 flavor from inheriting 4090
// capacity and admitting a workload that can never be placed.
func (c *Client) SyncClusterQueueFlavorQuotas(ctx context.Context, clusterQueueName string, capacities map[string]TrainingPoolCapacity) (bool, error) {
	for name, capacity := range capacities {
		if err := capacity.Validate(); err != nil {
			if name == "" {
				return false, fmt.Errorf("legacy aggregate capacity: %w", err)
			}
			return false, fmt.Errorf("resource flavor %q: %w", name, err)
		}
	}
	return c.syncClusterQueueQuotas(ctx, clusterQueueName, capacities)
}

func (c *Client) syncClusterQueueQuota(ctx context.Context, clusterQueueName string, capacity TrainingPoolCapacity) (bool, error) {
	if c == nil || c.dynamic == nil {
		return false, fmt.Errorf("Kubernetes dynamic client is not initialized")
	}
	if clusterQueueName == "" {
		return false, fmt.Errorf("cluster queue name is required")
	}
	// A successful observation of no eligible nodes removes the admission
	// budget. Failed node reads are handled by the caller and never reach here.
	if err := capacity.Validate(); err != nil {
		return false, err
	}

	return c.syncClusterQueueQuotas(ctx, clusterQueueName, map[string]TrainingPoolCapacity{"": capacity})
}

func (c *Client) syncClusterQueueQuotas(ctx context.Context, clusterQueueName string, capacities map[string]TrainingPoolCapacity) (bool, error) {
	if c == nil || c.dynamic == nil {
		return false, fmt.Errorf("Kubernetes dynamic client is not initialized")
	}
	if clusterQueueName == "" {
		return false, fmt.Errorf("cluster queue name is required")
	}
	queues := c.dynamic.Resource(clusterQueueGVR)
	existing, err := queues.Get(ctx, clusterQueueName, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return false, fmt.Errorf("cluster queue %q does not exist", clusterQueueName)
		}
		return false, fmt.Errorf("get cluster queue: %w", err)
	}

	updated := existing.DeepCopy()
	groups, found, err := unstructured.NestedSlice(updated.Object, "spec", "resourceGroups")
	if err != nil || !found || len(groups) == 0 {
		return false, fmt.Errorf("cluster queue %q has no resource groups", clusterQueueName)
	}

	changed := false
	for _, groupItem := range groups {
		group, ok := groupItem.(map[string]any)
		if !ok {
			continue
		}
		flavors, _ := group["flavors"].([]any)
		for _, flavorItem := range flavors {
			flavor, ok := flavorItem.(map[string]any)
			if !ok {
				continue
			}
			flavorName := fmt.Sprint(flavor["name"])
			capacity, found := capacities[flavorName]
			if !found && len(flavors) == 1 {
				capacity, found = capacities[""]
			}
			if !found {
				continue
			}
			desired := map[string]string{
				"cpu":            resource.NewMilliQuantity(capacity.CPUMillis, resource.DecimalSI).String(),
				"memory":         resource.NewQuantity(capacity.MemoryBytes, resource.BinarySI).String(),
				"nvidia.com/gpu": resource.NewQuantity(capacity.GPUs, resource.DecimalSI).String(),
			}
			resources, _ := flavor["resources"].([]any)
			for _, resourceItem := range resources {
				entry, ok := resourceItem.(map[string]any)
				if !ok {
					continue
				}
				name := fmt.Sprint(entry["name"])
				target, tracked := desired[name]
				if !tracked {
					continue
				}
				if fmt.Sprint(entry["nominalQuota"]) != target {
					entry["nominalQuota"] = target
					changed = true
				}
			}
		}
	}
	if !changed {
		return false, nil
	}
	if err := unstructured.SetNestedSlice(updated.Object, groups, "spec", "resourceGroups"); err != nil {
		return false, fmt.Errorf("set resource groups: %w", err)
	}
	if _, err := queues.Update(ctx, updated, metav1.UpdateOptions{}); err != nil {
		return false, fmt.Errorf("update cluster queue quota: %w", err)
	}
	return true, nil
}
