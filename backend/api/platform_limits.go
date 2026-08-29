package api

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"ray-train-platform-backend/domain"
	"ray-train-platform-backend/runtimecatalog"
)

// platformLimitsResponse is the single source of truth the Portal and the CLI
// render their pickers from. Before it existed the Portal hard-coded a fleet
// size, so a user could compose a job the server was always going to reject.
type platformLimitsResponse struct {
	MaxWorkerReplicas int                          `json:"maxWorkerReplicas"`
	MaxGPUsPerWorker  int                          `json:"maxGpusPerWorker"`
	MaxTotalGPUs      int                          `json:"maxTotalGpus"`
	TenantQuota       *tenantQuotaDescriptor       `json:"tenantQuota,omitempty"`
	MountPaths        governedMountPaths           `json:"mountPaths"`
	ExecutionProfiles []executionProfileDescriptor `json:"executionProfiles"`
	Cache             cachePolicyDescriptor        `json:"cache"`
	Runtime           runtimeCapabilityDescriptor  `json:"runtime"`
}

type runtimeCapabilityDescriptor struct {
	AvailableEngines     []string `json:"availableEngines"`
	ProductionRayVersion string   `json:"productionRayVersion"`
	CanaryRayVersion     string   `json:"canaryRayVersion"`
	ManagedEnabled       bool     `json:"managedEnabled"`
	CanaryEnabled        bool     `json:"canaryEnabled"`
}

type cachePolicyDescriptor struct {
	Enabled      bool     `json:"enabled"`
	DefaultMode  string   `json:"defaultMode"`
	Modes        []string `json:"modes"`
	AllowedSizes []string `json:"allowedSizes"`
	DefaultSize  string   `json:"defaultSize"`
	MaxSize      string   `json:"maxSize"`
	MountPath    string   `json:"mountPath"`
	MountPaths   []string `json:"mountPaths"`
}

type tenantQuotaDescriptor struct {
	GPULimit     int `json:"gpuLimit"`
	GPUUsed      int `json:"gpuUsed"`
	GPUAvailable int `json:"gpuAvailable"`
}

// governedMountPaths tells the user exactly where a chosen logical directory
// appears inside the container. Users previously had to infer this mapping.
type governedMountPaths struct {
	Workspace  string `json:"workspace"`
	Dataset    string `json:"dataset"`
	Checkpoint string `json:"checkpoint"`
	Output     string `json:"output"`
}

// executionProfileDescriptor describes one selectable execution contract along
// with whether the current fleet can actually admit it.
type executionProfileDescriptor struct {
	Mode              string `json:"mode"`
	Name              string `json:"name"`
	Description       string `json:"description"`
	MinWorkerReplicas int    `json:"minWorkerReplicas"`
	MaxWorkerReplicas int    `json:"maxWorkerReplicas"`
	MinGPUsPerWorker  int    `json:"minGpusPerWorker"`
	Available         bool   `json:"available"`
	UnavailableReason string `json:"unavailableReason,omitempty"`
}

func (h *Handler) platformLimits(c *gin.Context) {
	principal, ok := h.principal(c)
	if !ok {
		h.writeError(c, http.StatusUnauthorized, "AUTH_REQUIRED", "authentication is required")
		return
	}
	limits := domain.CurrentResourceLimits()
	var tenantQuota *tenantQuotaDescriptor
	// Limits are authenticated platform metadata. A roleless identity still
	// receives tenant-safe limits; only SuperAdmin enters fleet context.
	if !principal.HasRole(domain.RoleSuperAdmin) {
		if h.quota == nil {
			h.writeError(c, http.StatusServiceUnavailable, "QUOTA_UNAVAILABLE", "quota information is not configured")
			return
		}
		quota, err := h.quota.TenantGPUQuota(c.Request.Context(), principal.TenantID)
		if err != nil {
			h.writeError(c, http.StatusInternalServerError, "QUOTA_QUERY_FAILED", "could not read tenant quota")
			return
		}
		if quota.TenantID != principal.TenantID {
			h.writeError(c, http.StatusInternalServerError, "QUOTA_QUERY_FAILED", "could not read tenant quota")
			return
		}
		descriptor := callerTenantQuota(quota)
		tenantQuota = &descriptor
		limits = tenantResourceLimits(limits, descriptor.GPUAvailable)
	}
	h.writeSuccess(c, http.StatusOK, platformLimitsResponse{
		MaxWorkerReplicas: limits.MaxWorkerReplicas,
		MaxGPUsPerWorker:  limits.MaxGPUsPerWorker,
		MaxTotalGPUs:      limits.MaxTotalGPUs,
		TenantQuota:       tenantQuota,
		MountPaths: governedMountPaths{
			Workspace:  domain.WorkspaceMountPath,
			Dataset:    domain.DataMountInputPath,
			Checkpoint: domain.DataMountCheckpointPath,
			Output:     domain.DataMountOutputPath,
		},
		ExecutionProfiles: executionProfileDescriptors(limits),
		Cache:             cachePolicyDescriptorFor(h.localCache),
		Runtime:           runtimeCapabilityDescriptorFor(h.runtimePolicy.EffectiveForTenant(principal.TenantID)),
	})
}

func runtimeCapabilityDescriptorFor(policy runtimecatalog.Policy) runtimeCapabilityDescriptor {
	engines := []string{string(domain.TrainingEngineRayDDP)}
	if policy.ManagedEnabled {
		engines = append(engines, string(domain.TrainingEngineRayTrain))
	}
	return runtimeCapabilityDescriptor{
		AvailableEngines:     append([]string(nil), engines...),
		ProductionRayVersion: domain.RayVersionProduction,
		CanaryRayVersion:     domain.RayVersionCanary,
		ManagedEnabled:       policy.ManagedEnabled,
		CanaryEnabled:        policy.CanaryEnabled,
	}
}

func cachePolicyDescriptorFor(policy LocalCachePolicy) cachePolicyDescriptor {
	descriptor := cachePolicyDescriptor{
		Enabled: policy.Enabled, DefaultMode: string(domain.CacheModeOff), Modes: []string{string(domain.CacheModeOff)}, AllowedSizes: []string{},
	}
	if !policy.Enabled {
		return descriptor
	}
	descriptor.Modes = append(descriptor.Modes, string(domain.CacheModeRuntime))
	descriptor.AllowedSizes = append([]string(nil), policy.AllowedSizes...)
	descriptor.DefaultSize = policy.DefaultSize
	descriptor.MaxSize = policy.MaxSize
	descriptor.MountPath = policy.MountPath
	descriptor.MountPaths = append([]string(nil), policy.MountPaths...)
	return descriptor
}

func callerTenantQuota(quota domain.TenantQuota) tenantQuotaDescriptor {
	limit := max(quota.GPULimit, 0)
	used := max(quota.GPUUsed, 0)
	return tenantQuotaDescriptor{
		GPULimit:     limit,
		GPUUsed:      used,
		GPUAvailable: min(max(quota.GPUAvailable, 0), max(limit-used, 0)),
	}
}

func tenantResourceLimits(physical domain.ResourceLimits, tenantGPUAvailable int) domain.ResourceLimits {
	effectiveTotal := min(physical.MaxTotalGPUs, max(tenantGPUAvailable, 0))
	return domain.ResourceLimits{
		MaxWorkerReplicas: min(physical.MaxWorkerReplicas, effectiveTotal),
		MaxGPUsPerWorker:  min(physical.MaxGPUsPerWorker, effectiveTotal),
		MaxTotalGPUs:      effectiveTotal,
	}
}

func executionProfileDescriptors(limits domain.ResourceLimits) []executionProfileDescriptor {
	descriptors := []executionProfileDescriptor{
		{
			Mode: string(domain.ExecutionModeSingleGPU), Name: "单卡",
			Description:       "先验证代码、数据与日志；命令在一张 GPU 上执行。",
			MinWorkerReplicas: 1, MaxWorkerReplicas: 1, MinGPUsPerWorker: 1,
		},
		{
			Mode: string(domain.ExecutionModeTorchrun), Name: "单机多卡 DDP",
			Description:       "一个 worker Pod 内由平台执行 torchrun；适合现有 PyTorch DDP 代码。启动命令不要自己写 torchrun。",
			MinWorkerReplicas: 1, MaxWorkerReplicas: 1, MinGPUsPerWorker: 2,
		},
		{
			Mode: string(domain.ExecutionModeRayTrain), Name: "多机多卡",
			Description:       "每个节点一个 worker Pod，由 Ray 严格分散放置后在各节点启动 torchrun。",
			MinWorkerReplicas: 2, MaxWorkerReplicas: limits.MaxWorkerReplicas, MinGPUsPerWorker: 1,
		},
	}
	for index, descriptor := range descriptors {
		descriptors[index].Available, descriptors[index].UnavailableReason = profileAvailability(descriptor, limits)
	}
	return descriptors
}

func profileAvailability(descriptor executionProfileDescriptor, limits domain.ResourceLimits) (bool, string) {
	if descriptor.MinWorkerReplicas > limits.MaxWorkerReplicas {
		return false, "当前集群单任务最多允许 " + plural(limits.MaxWorkerReplicas, "个训练节点")
	}
	if descriptor.MinGPUsPerWorker > limits.MaxGPUsPerWorker {
		return false, "当前集群每节点最多允许 " + plural(limits.MaxGPUsPerWorker, "张 GPU")
	}
	if descriptor.MinWorkerReplicas*descriptor.MinGPUsPerWorker > limits.MaxTotalGPUs {
		return false, "当前集群单任务最多允许 " + plural(limits.MaxTotalGPUs, "张 GPU")
	}
	return true, ""
}

func plural(count int, unit string) string {
	return strconv.Itoa(count) + " " + unit
}
