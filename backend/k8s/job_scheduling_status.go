package k8s

import (
	"context"
	"fmt"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"ray-train-platform-backend/domain"
)

// JobSchedulingAdmitted reads only Workloads belonging to the exact RayJob
// attempt. Unknown is distinct from not admitted; missing identity is not proof
// that a workload is waiting for GPUs.
func (c *Client) JobSchedulingAdmitted(ctx context.Context, job domain.TrainingJob) (admitted, known bool, err error) {
	if strings.TrimSpace(job.RayJobUID) == "" || job.KubernetesNS == "" || job.RayJobName == "" {
		return false, false, nil
	}
	if c == nil || c.dynamic == nil {
		return false, false, fmt.Errorf("Kubernetes dynamic client is not initialized")
	}
	gvr := schema.GroupVersionResource{Group: "kueue.x-k8s.io", Version: "v1beta2", Resource: "workloads"}
	items, err := c.dynamic.Resource(gvr).Namespace(job.KubernetesNS).List(ctx, metav1.ListOptions{
		LabelSelector: "kueue.x-k8s.io/job-uid=" + job.RayJobUID,
	})
	if err != nil {
		return false, false, fmt.Errorf("observe job admission: %w", err)
	}
	for _, item := range items.Items {
		if item.GetNamespace() != job.KubernetesNS || item.GetLabels()["kueue.x-k8s.io/job-uid"] != job.RayJobUID || item.GetDeletionTimestamp() != nil {
			continue
		}
		owned := false
		for _, owner := range item.GetOwnerReferences() {
			if owner.APIVersion == "ray.io/v1" && owner.Kind == "RayJob" && owner.Name == job.RayJobName && string(owner.UID) == job.RayJobUID {
				owned = true
			}
		}
		if !owned {
			continue
		}
		conditions, _, _ := unstructured.NestedSlice(item.Object, "status", "conditions")
		for _, raw := range conditions {
			condition, ok := raw.(map[string]any)
			if ok && condition["type"] == "Admitted" {
				if condition["status"] == "True" {
					admission, found, admissionErr := unstructured.NestedMap(item.Object, "status", "admission")
					if admissionErr == nil && found && len(admission) > 0 {
						return true, true, nil
					}
				}
				if condition["status"] == "False" {
					known = true
				}
			}
		}
	}
	return false, known, nil
}
