package domain

import "time"

type WorkspaceState string

const (
	WorkspaceSubmitted WorkspaceState = "SUBMITTED"
	WorkspaceRunning   WorkspaceState = "RUNNING"
	WorkspaceStopping  WorkspaceState = "STOPPING"
	WorkspaceStopped   WorkspaceState = "STOPPED"
	WorkspaceFailed    WorkspaceState = "FAILED"
)

type WorkspaceSpec struct {
	Name           string
	Image          string
	GPUs           int
	SnapshotID     string
	IdleTTLSeconds int64
}

type DevWorkspace struct {
	ID             string         `json:"id"`
	TenantID       string         `json:"tenantId"`
	UserID         string         `json:"userId"`
	Name           string         `json:"name"`
	Namespace      string         `json:"namespace"`
	RayClusterName string         `json:"rayClusterName"`
	JupyterURL     string         `json:"jupyterUrl"`
	SnapshotID     string         `json:"snapshotId"`
	GPUCount       int            `json:"gpuCount"`
	State          WorkspaceState `json:"state"`
	ExpiresAt      *time.Time     `json:"expiresAt,omitempty"`
}

// SupportedWorkspaceGPUCounts includes zero so a user can still open a debug
// session when training has taken every GPU. A CPU-only workspace still runs on
// a training node and mounts the same data; it simply reserves no device.
var SupportedWorkspaceGPUCounts = []int{0, 1, 2, 4, 8}

func IsSupportedWorkspaceGPUCount(value int) bool {
	for _, supported := range SupportedWorkspaceGPUCounts {
		if value == supported {
			return true
		}
	}
	return false
}
