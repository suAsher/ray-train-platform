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
