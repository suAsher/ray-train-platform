package domain

import "time"

// GPUAllocationType identifies the kind of workload holding GPU capacity.
type GPUAllocationType string

const (
	GPUAllocationTrainingJob    GPUAllocationType = "TRAINING_JOB"
	GPUAllocationDebugWorkspace GPUAllocationType = "DEBUG_WORKSPACE"
)

// GPUAllocation is the common administrative projection for active training
// jobs and interactive debug workspaces.
type GPUAllocation struct {
	ID           string            `json:"id"`
	Type         GPUAllocationType `json:"type"`
	Name         string            `json:"name"`
	TenantID     string            `json:"tenantId"`
	UserID       string            `json:"userId"`
	Username     string            `json:"username"`
	State        string            `json:"state"`
	GPUCount     int               `json:"gpuCount"`
	Namespace    string            `json:"namespace"`
	ResourceName string            `json:"resourceName"`
	CreatedAt    time.Time         `json:"createdAt"`
	StartedAt    *time.Time        `json:"startedAt,omitempty"`
}
