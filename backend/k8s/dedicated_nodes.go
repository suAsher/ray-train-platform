package k8s

import "sort"

// DedicatedTenantTaintKey keeps pre-cutover templates and non-platform Pods
// from entering a newly dedicated node. NoSchedule never evicts existing Pods.
const DedicatedTenantTaintKey = "platform.wellspiking.ai/dedicated-tenant"

// dedicatedNodeAffinity is independent of TAS and soft team preferences. The
// server-owned assignment is included in each PodSet before Kueue admission.
// Config validation guarantees nonempty, disjoint node sets for each tenant.
func dedicatedNodeAffinity(tenantID string, assignments map[string][]string) map[string]any {
	if len(assignments) == 0 {
		return nil
	}
	operator := "In"
	nodes := append([]string(nil), assignments[tenantID]...)
	if len(nodes) == 0 {
		operator = "NotIn"
		for _, assigned := range assignments {
			nodes = append(nodes, assigned...)
		}
	}
	sort.Strings(nodes)
	values := make([]any, len(nodes))
	for i, node := range nodes {
		values[i] = node
	}
	return map[string]any{"nodeAffinity": map[string]any{
		"requiredDuringSchedulingIgnoredDuringExecution": map[string]any{
			"nodeSelectorTerms": []any{map[string]any{"matchExpressions": []any{map[string]any{
				"key": "kubernetes.io/hostname", "operator": operator, "values": values,
			}}}},
		},
	}}
}

func dedicatedNodeTolerations(tenantID string, assignments map[string][]string) []any {
	if len(assignments[tenantID]) == 0 {
		return nil
	}
	return []any{map[string]any{"key": DedicatedTenantTaintKey, "operator": "Equal", "value": tenantID, "effect": "NoSchedule"}}
}
