package k8s

import (
	"context"
	"fmt"
	"math"
	"sort"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"ray-train-platform-backend/domain"
)

type SchedulingCapacityRequest struct {
	TenantID         string
	AcceleratorClass domain.AcceleratorClass
	NodeSelector     map[string]string
	DedicatedNodes   map[string][]string
	Resources        domain.Resources
	ExcludeJobID     string
}

// SchedulingCapacity accounts for bound Pods (including debug and CPU-only
// Pods) and the portion of topology reservations not yet represented by Pods.
func (c *Client) SchedulingCapacity(ctx context.Context, request SchedulingCapacityRequest) (domain.SchedulingCapacity, error) {
	result := domain.SchedulingCapacity{ObservedAt: time.Now().UTC(), AcceleratorClass: string(request.AcceleratorClass.Resolved()), Fit: domain.SchedulingCapacityFit{WorkerReplicas: request.Resources.WorkerReplicas, GPUsPerWorker: request.Resources.GPUsPerWorker, CPUPerWorker: request.Resources.CPUPerWorker, MemoryPerWorker: request.Resources.MemoryPerWorker, Reason: "capacity_unknown"}}
	unknown := func(err error) (domain.SchedulingCapacity, error) {
		result.Available = false
		result.UnavailableReason = "capacity_observation_unavailable"
		result.Nodes = nil
		result.Pool = domain.SchedulingCapacityPool{}
		return result, err
	}
	if c == nil || c.kubernetes == nil || c.dynamic == nil {
		return unknown(fmt.Errorf("capacity clients unavailable"))
	}
	if err := request.AcceleratorClass.Validate(); err != nil {
		return unknown(fmt.Errorf("invalid accelerator pool"))
	}
	memory, err := resource.ParseQuantity(request.Resources.MemoryPerWorker)
	if err != nil || memory.Sign() <= 0 || memory.Cmp(resource.MustParse("1Pi")) > 0 || len(request.Resources.MemoryPerWorker) > 64 ||
		request.Resources.WorkerReplicas <= 0 || request.Resources.WorkerReplicas > 4096 || request.Resources.GPUsPerWorker < 0 || request.Resources.GPUsPerWorker > 4096 || request.Resources.CPUPerWorker <= 0 || request.Resources.CPUPerWorker > 1_000_000 {
		return unknown(fmt.Errorf("invalid capacity worker shape"))
	}
	selector := map[string]string{}
	for key, value := range request.NodeSelector {
		selector[key] = value
	}
	selector["accelerator"] = request.AcceleratorClass.Resolved().NodeLabelValue()
	nodes, err := c.kubernetes.CoreV1().Nodes().List(ctx, metav1.ListOptions{LabelSelector: labels.Set(selector).AsSelector().String()})
	if err != nil {
		return unknown(fmt.Errorf("list capacity nodes: %w", err))
	}
	pods, err := c.kubernetes.CoreV1().Pods("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return unknown(fmt.Errorf("list capacity pods: %w", err))
	}
	indices := map[string]int{}
	hostnames := map[string]string{}
	for _, node := range nodes.Items {
		if !capacityNodeEligible(node, request) {
			continue
		}
		gpus := capacityResourceValue(node.Status.Allocatable, "nvidia.com/gpu")
		if gpus <= 0 {
			continue
		}
		indices[node.Name] = len(result.Nodes)
		hostname := node.Labels["kubernetes.io/hostname"]
		if hostname == "" {
			hostname = node.Name
		}
		if previous, found := hostnames[hostname]; found && previous != node.Name {
			return unknown(fmt.Errorf("ambiguous topology hostname"))
		}
		hostnames[hostname] = node.Name
		result.Nodes = append(result.Nodes, domain.SchedulingCapacityNode{Name: node.Name, TotalGPUs: gpus, FreeGPUs: gpus, FreeCPUMillis: capacityResourceValue(node.Status.Allocatable, corev1.ResourceCPU), FreeMemoryBytes: capacityResourceValue(node.Status.Allocatable, corev1.ResourceMemory)})
	}
	for _, pod := range pods.Items {
		index, found := indices[pod.Spec.NodeName]
		if !found || !capacityPodActive(pod) {
			continue
		}
		requests := SchedulingPodRequests(pod)
		node := &result.Nodes[index]
		gpu := capacityResourceValue(requests, "nvidia.com/gpu")
		node.AllocatedGPUs += gpu
		node.FreeGPUs -= gpu
		node.FreeCPUMillis -= capacityResourceValue(requests, corev1.ResourceCPU)
		node.FreeMemoryBytes -= capacityResourceValue(requests, corev1.ResourceMemory)
		if pod.Labels["platform_tenant_id"] == request.TenantID || pod.Labels["ray.io/tenant-id"] == request.TenantID || pod.Namespace == "tenant-"+request.TenantID {
			node.TeamGPUs += gpu
		}
	}
	if len(result.Nodes) == 0 {
		finishSchedulingCapacity(&result, request.Resources, memory.Value())
		return result, nil
	}
	if err := c.subtractCapacityReservations(ctx, request, pods.Items, indices, hostnames, result.Nodes); err != nil {
		return unknown(err)
	}
	finishSchedulingCapacity(&result, request.Resources, memory.Value())
	return result, nil
}

func capacityPodActive(pod corev1.Pod) bool {
	return pod.Spec.NodeName != "" && pod.Status.Phase != corev1.PodSucceeded && pod.Status.Phase != corev1.PodFailed
}

func capacityNodeEligible(node corev1.Node, request SchedulingCapacityRequest) bool {
	if isVirtualNode(node) || node.Spec.Unschedulable || !isNodeReady(node) {
		return false
	}
	hostname := node.Labels["kubernetes.io/hostname"]
	if hostname == "" {
		hostname = node.Name
	}
	owned := request.DedicatedNodes[request.TenantID]
	if len(owned) > 0 {
		found := false
		for _, name := range owned {
			if name == hostname {
				found = true
			}
		}
		if !found {
			return false
		}
	} else {
		for _, names := range request.DedicatedNodes {
			for _, name := range names {
				if name == hostname {
					return false
				}
			}
		}
	}
	for _, taint := range node.Spec.Taints {
		if taint.Effect != corev1.TaintEffectNoSchedule && taint.Effect != corev1.TaintEffectNoExecute {
			continue
		}
		if taint.Key == DedicatedTenantTaintKey && taint.Value == request.TenantID && len(owned) > 0 && taint.Effect == corev1.TaintEffectNoSchedule {
			continue
		}
		return false
	}
	return true
}

func finishSchedulingCapacity(result *domain.SchedulingCapacity, resources domain.Resources, memoryBytes int64) {
	var gpuSlots int64
	for index := range result.Nodes {
		node := &result.Nodes[index]
		node.FreeGPUs = max(node.FreeGPUs, 0)
		node.FreeCPUMillis = max(node.FreeCPUMillis, 0)
		node.FreeMemoryBytes = max(node.FreeMemoryBytes, 0)
		slots := node.FreeCPUMillis / (resources.CPUPerWorker * 1000)
		slots = min(slots, node.FreeMemoryBytes/memoryBytes)
		if resources.GPUsPerWorker > 0 {
			gpuFit := node.FreeGPUs / int64(resources.GPUsPerWorker)
			gpuSlots += gpuFit
			slots = min(slots, gpuFit)
		}
		node.WorkerSlots = int(min(slots, int64(math.MaxInt32)))
		if slots > 0 {
			result.Fit.FittingNodes++
		}
		result.Fit.WorkerSlots += node.WorkerSlots
		result.Pool.TotalGPUs += node.TotalGPUs
		result.Pool.AllocatedGPUs += node.AllocatedGPUs
		result.Pool.ReservedGPUs += node.ReservedGPUs
		result.Pool.FreeGPUs += node.FreeGPUs
	}
	result.Available = true
	result.Fit.CanFit = result.Fit.WorkerSlots >= resources.WorkerReplicas
	result.Fit.Reason = "fits"
	if !result.Fit.CanFit {
		switch {
		case len(result.Nodes) == 0:
			result.Fit.Reason = "no_eligible_nodes"
		case resources.GPUsPerWorker > 0 && result.Pool.FreeGPUs < int64(resources.WorkerReplicas)*int64(resources.GPUsPerWorker):
			result.Fit.Reason = "insufficient_gpus"
		case resources.GPUsPerWorker > 0 && gpuSlots < int64(resources.WorkerReplicas):
			result.Fit.Reason = "gpu_fragmentation"
		default:
			result.Fit.Reason = "insufficient_cpu_or_memory"
		}
	}
	sort.Slice(result.Nodes, func(i, j int) bool { return result.Nodes[i].Name < result.Nodes[j].Name })
}
