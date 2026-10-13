package api

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"k8s.io/apimachinery/pkg/api/resource"
	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
	"ray-train-platform-backend/k8s"
)

func (h *Handler) currentSchedulingCapacity(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	principal, ok := h.principal(c)
	if !ok || principal.AuthType == auth.AuthTypeDemo || principal.TenantID == "" {
		h.writeError(c, http.StatusUnauthorized, "AUTH_REQUIRED", "authentication is required")
		return
	}
	shape, err := parseSchedulingCapacityShape(c)
	if err != nil {
		h.writeError(c, http.StatusBadRequest, "CAPACITY_SHAPE_INVALID", "资源规格无效：请使用正整数 worker/CPU、非负整数 GPU 和有效内存容量")
		return
	}
	capacity, _ := h.schedulingCapacity(c.Request.Context(), principal.TenantID, shape, "")
	if capacity.Available && capacity.Quota != nil && int64(shape.WorkerReplicas)*int64(shape.GPUsPerWorker) > int64(capacity.Quota.GPUAvailable) {
		capacity.Fit.CanFit = false
		capacity.Fit.Reason = "team_quota_exceeded"
	}
	if !principal.HasRole(domain.RoleSuperAdmin) {
		capacity.Nodes = nil
	}
	h.writeSuccess(c, http.StatusOK, capacity)
}

func parseSchedulingCapacityShape(c *gin.Context) (domain.Resources, error) {
	shape := domain.Resources{WorkerReplicas: 1, GPUsPerWorker: 1, CPUPerWorker: 4, MemoryPerWorker: "16Gi"}
	for key, values := range c.Request.URL.Query() {
		if len(values) != 1 {
			return shape, fmt.Errorf("duplicate shape field")
		}
		switch key {
		case "workerReplicas", "gpusPerWorker", "cpuPerWorker", "memoryPerWorker":
		default:
			return shape, fmt.Errorf("unsupported shape field")
		}
	}
	parse := func(key string, current, maximum int64, allowZero bool) (int64, error) {
		values, found := c.Request.URL.Query()[key]
		if !found {
			return current, nil
		}
		text := values[0]
		if text == "" {
			return 0, fmt.Errorf("empty shape field")
		}
		for _, character := range text {
			if character < '0' || character > '9' {
				return 0, fmt.Errorf("shape must be unsigned decimal")
			}
		}
		value, err := strconv.ParseInt(text, 10, 64)
		if err != nil || value > maximum || value < 0 || (!allowZero && value == 0) {
			return 0, fmt.Errorf("shape out of range")
		}
		return value, nil
	}
	workers, err := parse("workerReplicas", 1, 4096, false)
	if err != nil {
		return shape, err
	}
	gpus, err := parse("gpusPerWorker", 1, 4096, true)
	if err != nil {
		return shape, err
	}
	cpu, err := parse("cpuPerWorker", 4, 1_000_000, false)
	if err != nil {
		return shape, err
	}
	shape.WorkerReplicas, shape.GPUsPerWorker, shape.CPUPerWorker = int(workers), int(gpus), cpu
	if values, found := c.Request.URL.Query()["memoryPerWorker"]; found {
		shape.MemoryPerWorker = values[0]
	}
	memory, err := resource.ParseQuantity(shape.MemoryPerWorker)
	if err != nil || len(shape.MemoryPerWorker) > 64 || memory.Sign() <= 0 || memory.Cmp(resource.MustParse("1Pi")) > 0 {
		return shape, fmt.Errorf("memory out of range")
	}
	return shape, nil
}

// schedulingCapacity is shared by the submit form and job diagnosis. Its Fit
// describes physical capacity; callers apply their appropriate quota context.
func (h *Handler) schedulingCapacity(ctx context.Context, tenantID string, shape domain.Resources, excludeJobID string) (domain.SchedulingCapacity, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	accelerator := domain.AcceleratorRTX4090
	unknown := domain.SchedulingCapacity{ObservedAt: time.Now().UTC(), AcceleratorClass: string(accelerator), UnavailableReason: "capacity_observation_unavailable", Fit: domain.SchedulingCapacityFit{WorkerReplicas: shape.WorkerReplicas, GPUsPerWorker: shape.GPUsPerWorker, CPUPerWorker: shape.CPUPerWorker, MemoryPerWorker: shape.MemoryPerWorker, Reason: "capacity_unknown"}}
	if h.tenantScheduling != nil {
		resolved, err := h.tenantScheduling.TenantAcceleratorClass(ctx, tenantID)
		if err != nil {
			return unknown, fmt.Errorf("tenant scheduling unavailable")
		}
		accelerator = resolved.Resolved()
	}
	unknown.AcceleratorClass = string(accelerator)
	if err := accelerator.Validate(); err != nil {
		return unknown, fmt.Errorf("tenant scheduling invalid")
	}
	capacity, err := h.kubernetes.SchedulingCapacity(ctx, k8s.SchedulingCapacityRequest{TenantID: tenantID, AcceleratorClass: accelerator, NodeSelector: h.trainingNodeSelector, DedicatedNodes: h.trainingDedicatedNodes, Resources: shape, ExcludeJobID: excludeJobID})
	if err != nil {
		capacity = unknown
	}
	if h.quota == nil {
		capacity.Available = false
		capacity.UnavailableReason = "quota_unavailable"
		return capacity, fmt.Errorf("quota unavailable")
	}
	quota, quotaErr := h.quota.TenantGPUQuota(ctx, tenantID)
	if quotaErr != nil || quota.TenantID != tenantID || quota.GPULimit < 0 || quota.GPUUsed < 0 || quota.GPUAvailable < 0 {
		capacity.Available = false
		capacity.UnavailableReason = "quota_unavailable"
		return capacity, fmt.Errorf("quota unavailable")
	}
	capacity.Quota = &quota
	return capacity, err
}
