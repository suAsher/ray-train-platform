package k8s

import (
	"context"
	"fmt"
	"strconv"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"ray-train-platform-backend/domain"
)

func (c *Client) subtractCapacityReservations(ctx context.Context, request SchedulingCapacityRequest, pods []corev1.Pod, indices map[string]int, hostnames map[string]string, nodes []domain.SchedulingCapacityNode) error {
	gvr := c.kueueGVR
	if gvr.Version == "" { gvr = localQueueGVR }
	gvr.Resource = "workloads"
	// Pagination bounds each response even when the cluster retains completed
	// Workloads. No historical workload identity leaves this function.
	continuation := ""
	for {
		workloads, err := c.dynamic.Resource(gvr).List(ctx, metav1.ListOptions{Limit: 500, Continue: continuation})
		if err != nil { return fmt.Errorf("list admission reservations: %w", err) }
		for _, workload := range workloads.Items {
			if capacityWorkloadFinished(workload) { continue }
			if request.ExcludeJobID != "" && capacityWorkloadJobID(workload) == request.ExcludeJobID { continue }
			assignments, found, err := unstructured.NestedSlice(workload.Object, "status", "admission", "podSetAssignments")
			if err != nil { return fmt.Errorf("invalid admission assignments") }
			if !found { continue }
			for _, value := range assignments {
				assignment, ok := value.(map[string]any)
				if !ok { return fmt.Errorf("invalid admission assignment") }
				if err := subtractCapacityAssignment(workload, assignment, pods, indices, hostnames, nodes); err != nil { return err }
			}
		}
		continuation = workloads.GetContinue()
		if continuation == "" { break }
	}
	return nil
}

func capacityWorkloadFinished(workload unstructured.Unstructured) bool {
	conditions, _, _ := unstructured.NestedSlice(workload.Object, "status", "conditions")
	for _, value := range conditions {
		condition, ok := value.(map[string]any)
		if !ok { continue }
		if condition["type"] == "Finished" && condition["status"] == "True" { return true }
		if condition["type"] == "QuotaReserved" && condition["status"] == "False" { return true }
	}
	return false
}

func capacityWorkloadJobID(workload unstructured.Unstructured) string {
	for _, owner := range workload.GetOwnerReferences() {
		if owner.Kind == "RayJob" { return owner.Name }
	}
	return workload.GetLabels()["platform_job_id"]
}

func subtractCapacityAssignment(workload unstructured.Unstructured, assignment map[string]any, pods []corev1.Pod, indices map[string]int, hostnames map[string]string, nodes []domain.SchedulingCapacityNode) error {
	name, _ := assignment["name"].(string)
	count, ok := capacityInteger(assignment["count"])
	if !ok || count <= 0 || name == "" { return fmt.Errorf("invalid reservation pod count") }
	usage, ok := assignment["resourceUsage"].(map[string]any)
	if !ok { return fmt.Errorf("invalid reservation resources") }
	resources := map[corev1.ResourceName]int64{}
	for _, key := range []corev1.ResourceName{"nvidia.com/gpu", corev1.ResourceCPU, corev1.ResourceMemory} {
		value, found := usage[string(key)]
		if !found { continue }
		text, ok := value.(string)
		if !ok { return fmt.Errorf("invalid reservation quantity") }
		quantity, err := resource.ParseQuantity(text)
		if err != nil || quantity.Sign() < 0 { return fmt.Errorf("invalid reservation quantity") }
		amount := quantity.Value()
		if key == corev1.ResourceCPU { amount = quantity.MilliValue() }
		if amount < 0 || amount % count != 0 { return fmt.Errorf("reservation quantity cannot be apportioned") }
		resources[key] = amount/count
	}
	topology, ok := assignment["topologyAssignment"].(map[string]any)
	if !ok { return fmt.Errorf("reservation has no hostname topology") }
	domains, err := capacityTopologyCounts(topology)
	if err != nil { return err }
	var total int64
	for _, number := range domains { total += number }
	if total != count { return fmt.Errorf("reservation topology count mismatch") }
	for hostname, number := range domains {
		nodeName, found := hostnames[hostname]
		if !found { continue } // Reservation belongs to another eligible pool.
		bound := int64(0)
		for _, pod := range pods {
			if !capacityPodActive(pod) || pod.Namespace != workload.GetNamespace() || pod.Spec.NodeName != nodeName { continue }
			podWorkload := pod.Annotations["kueue.x-k8s.io/workload"]
			if podWorkload == "" { podWorkload = pod.Labels["kueue.x-k8s.io/workload-name"] }
			if podWorkload == workload.GetName() && pod.Labels["kueue.x-k8s.io/podset"] == name { bound++ }
		}
		remaining := max(number-bound, 0)
		index := indices[nodeName]
		node := &nodes[index]
		gpu := remaining*resources["nvidia.com/gpu"]
		node.ReservedGPUs += gpu
		node.FreeGPUs -= gpu
		node.FreeCPUMillis -= remaining*resources[corev1.ResourceCPU]
		node.FreeMemoryBytes -= remaining*resources[corev1.ResourceMemory]
	}
	return nil
}

func capacityInteger(value any) (int64, bool) {
	switch number := value.(type) {
	case int64: return number, true
	case int: return int64(number), true
	case float64:
		parsed, err := strconv.ParseInt(strconv.FormatFloat(number, 'f', -1, 64), 10, 64)
		return parsed, err == nil
	default: return 0, false
	}
}

// Both legacy domains and Kueue v0.19's universal single-domain slices are
// supported. Other compressed encodings remain unknown, never optimistic zero.
func capacityTopologyCounts(topology map[string]any) (map[string]int64, error) {
	levels, ok := topology["levels"].([]any)
	if !ok { return nil, fmt.Errorf("reservation topology levels missing") }
	hostIndex := -1
	for index, level := range levels { if level == "kubernetes.io/hostname" { hostIndex = index } }
	if hostIndex < 0 { return nil, fmt.Errorf("reservation lacks hostname level") }
	result := map[string]int64{}
	if domains, ok := topology["domains"].([]any); ok {
		for _, value := range domains {
			domain, ok := value.(map[string]any)
			if !ok { return nil, fmt.Errorf("invalid topology domain") }
			values, ok := domain["values"].([]any)
			if !ok || hostIndex >= len(values) { return nil, fmt.Errorf("invalid topology hostname") }
			hostname, ok := values[hostIndex].(string)
			count, valid := capacityInteger(domain["count"])
			if !ok || hostname == "" || !valid || count <= 0 { return nil, fmt.Errorf("invalid topology count") }
			result[hostname] += count
		}
		return result, nil
	}
	slices, ok := topology["slices"].([]any)
	if !ok { return nil, fmt.Errorf("unsupported reservation topology") }
	for _, value := range slices {
		slice, ok := value.(map[string]any)
		if !ok { return nil, fmt.Errorf("invalid topology slice") }
		domainCount, valid := capacityInteger(slice["domainCount"])
		if !valid || domainCount != 1 { return nil, fmt.Errorf("unsupported compressed topology slice") }
		values, ok := slice["valuesPerLevel"].([]any)
		if !ok || hostIndex >= len(values) { return nil, fmt.Errorf("invalid slice hostname levels") }
		level, ok := values[hostIndex].(map[string]any)
		if !ok { return nil, fmt.Errorf("invalid slice hostname") }
		hostname, ok := level["universal"].(string)
		counts, valid := slice["podCounts"].(map[string]any)
		if !valid || !ok || hostname == "" { return nil, fmt.Errorf("unsupported slice encoding") }
		count, valid := capacityInteger(counts["universal"])
		if !valid || count <= 0 { return nil, fmt.Errorf("invalid slice count") }
		result[hostname] += count
	}
	return result, nil
}
