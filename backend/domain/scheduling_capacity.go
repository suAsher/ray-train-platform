package domain

import "time"

// SchedulingCapacity is a point-in-time observation, not an admission promise.
// Available=false means callers must show unknown instead of interpreting zeros.
// ServerTime is optional response metadata, never the collection timestamp.
type SchedulingCapacity struct {
	ObservedAt        time.Time                `json:"observedAt"`
	ServerTime        *time.Time               `json:"serverTime,omitempty"`
	AcceleratorClass  string                   `json:"acceleratorClass"`
	Available         bool                     `json:"available"`
	UnavailableReason string                   `json:"unavailableReason,omitempty"`
	Pool              SchedulingCapacityPool   `json:"pool"`
	Fit               SchedulingCapacityFit    `json:"fit"`
	Nodes             []SchedulingCapacityNode `json:"nodes,omitempty"`
	Quota             *TenantQuota             `json:"quota,omitempty"`
}

type SchedulingCapacityPool struct {
	TotalGPUs     int64 `json:"totalGpus"`
	AllocatedGPUs int64 `json:"allocatedGpus"`
	ReservedGPUs  int64 `json:"reservedGpus"`
	FreeGPUs      int64 `json:"freeGpus"`
}

type SchedulingCapacityFit struct {
	WorkerReplicas  int    `json:"workerReplicas"`
	GPUsPerWorker   int    `json:"gpusPerWorker"`
	CPUPerWorker    int64  `json:"cpuPerWorker"`
	MemoryPerWorker string `json:"memoryPerWorker"`
	FittingNodes    int    `json:"fittingNodes"`
	WorkerSlots     int    `json:"workerSlots"`
	CanFit          bool   `json:"canFit"`
	Reason          string `json:"reason"`
}

// Node details contain aggregate resources only, never workload identities.
type SchedulingCapacityNode struct {
	Name            string `json:"name"`
	TotalGPUs       int64  `json:"totalGpus"`
	AllocatedGPUs   int64  `json:"allocatedGpus"`
	ReservedGPUs    int64  `json:"reservedGpus"`
	FreeGPUs        int64  `json:"freeGpus"`
	FreeCPUMillis   int64  `json:"freeCpuMillis"`
	FreeMemoryBytes int64  `json:"freeMemoryBytes"`
	TeamGPUs        int64  `json:"-"`
	WorkerSlots     int    `json:"workerSlots"`
}
