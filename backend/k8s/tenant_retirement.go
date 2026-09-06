package k8s

import (
	"context"
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation"
)

type retirementResource struct {
	gvr  schema.GroupVersionResource
	kind string
}

func tenantRetirementResources(kueue schema.GroupVersionResource) []retirementResource {
	return []retirementResource{
		{rayJobGVR, "RayJob"}, {rayClusterGVR, "RayCluster"},
		{schema.GroupVersionResource{Group: kueue.Group, Version: kueue.Version, Resource: "workloads"}, "Workload"},
		{schema.GroupVersionResource{Version: "v1", Resource: "pods"}, "Pod"},
		{schema.GroupVersionResource{Group: "batch", Version: "v1", Resource: "jobs"}, "Job"},
		{schema.GroupVersionResource{Group: "batch", Version: "v1", Resource: "cronjobs"}, "CronJob"},
		{schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}, "Deployment"},
		{schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "statefulsets"}, "StatefulSet"},
		{schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "daemonsets"}, "DaemonSet"},
	}
}

// TenantRetirementBlockers inventories only; errors and unknown state fail closed.
// It does not stop work or remove even an empty namespace/storage claim.
func (c *Client) TenantRetirementBlockers(ctx context.Context, namespace string) ([]string, error) {
	if c == nil || c.dynamic == nil || namespace == "" || len(validation.IsDNS1123Label(namespace)) != 0 {
		return nil, fmt.Errorf("valid tenant namespace and Kubernetes client required")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	blockers := []string{}
	for _, resource := range tenantRetirementResources(c.kueueGVR) {
		continuation := ""
		total, active := 0, 0
		for {
			items, err := c.dynamic.Resource(resource.gvr).Namespace(namespace).List(ctx, metav1.ListOptions{Limit: 500, Continue: continuation})
			if err != nil {
				return nil, fmt.Errorf("inspect tenant %s: %w", resource.kind, err)
			}
			total += len(items.Items)
			if total > 10000 {
				return nil, fmt.Errorf("tenant %s inventory exceeds safe preflight limit", resource.kind)
			}
			for _, item := range items.Items {
				if retirementResourceActive(resource.kind, item.Object) {
					active++
				}
			}
			continuation = items.GetContinue()
			if continuation == "" {
				break
			}
		}
		if active > 0 {
			blockers = append(blockers, fmt.Sprintf("Kubernetes %s 存在 %d 项活动或状态未确认资源", resource.kind, active))
		}
	}
	return blockers, nil
}

func retirementResourceActive(kind string, object map[string]any) bool {
	switch kind {
	case "Pod":
		state, _, _ := unstructured.NestedString(object, "status", "phase")
		return state != "Succeeded" && state != "Failed"
	case "RayJob":
		state, _, _ := unstructured.NestedString(object, "status", "jobDeploymentStatus")
		return state != "Complete" && state != "Failed"
	case "Job", "Workload":
		conditions, _, _ := unstructured.NestedSlice(object, "status", "conditions")
		for _, condition := range conditions {
			value, ok := condition.(map[string]any)
			if !ok {
				continue
			}
			terminal := value["type"] == "Finished" && kind == "Workload" || kind == "Job" && (value["type"] == "Complete" || value["type"] == "Failed")
			if terminal && value["status"] == "True" {
				return false
			}
		}
		return true
	default:
		// Controllers, including suspended ones, retain the ability to create work.
		// Treat any remaining controller/RayCluster as a retirement blocker.
		return true
	}
}
