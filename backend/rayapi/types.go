package rayapi

import "ray-train-platform-backend/domain"

// JobSubmitRequest is the Ray 2.35 HTTP request shape. Metadata is deliberately
// string-only because Ray's public SDK serializes it as Dict[str, str].
type JobSubmitRequest struct {
	Entrypoint          string             `json:"entrypoint"`
	SubmissionID        string             `json:"submission_id,omitempty"`
	JobID               string             `json:"job_id,omitempty"`
	RuntimeEnv          map[string]any     `json:"runtime_env,omitempty"`
	Metadata            map[string]string  `json:"metadata,omitempty"`
	EntrypointNumCPUs   *float64           `json:"entrypoint_num_cpus,omitempty"`
	EntrypointNumGPUs   *float64           `json:"entrypoint_num_gpus,omitempty"`
	EntrypointMemory    *int64             `json:"entrypoint_memory,omitempty"`
	EntrypointResources map[string]float64 `json:"entrypoint_resources,omitempty"`
}

type PackageName struct {
	Name string
}

// SubmissionDefaults are the safe, intentionally small shape used when a
// caller uses the stock `ray job submit --working-dir .` command. Ray's CLI
// has no first-class flags for the Kubernetes image, Kueue queue, or GPU pod
// topology, so the platform owns those values.
type SubmissionDefaults struct {
	Image           string
	WorkerReplicas  int
	GPUsPerWorker   int
	CPUPerWorker    int64
	MemoryPerWorker string
}

type TranslatedSubmitRequest struct {
	Package              PackageName
	Spec                 domain.JobSpec
	ExternalSubmissionID string
}

type jobSubmitResponse struct {
	SubmissionID string `json:"submission_id"`
	JobID        string `json:"job_id"`
}

type jobStopResponse struct {
	Stopped bool `json:"stopped"`
}

type jobDeleteResponse struct {
	Deleted bool `json:"deleted"`
}

type jobLogsResponse struct {
	Logs string `json:"logs"`
}

type jobDetailsResponse struct {
	Type         string            `json:"type"`
	JobID        *string           `json:"job_id"`
	SubmissionID string            `json:"submission_id"`
	Status       string            `json:"status"`
	Entrypoint   string            `json:"entrypoint"`
	Message      string            `json:"message,omitempty"`
	ErrorType    *string           `json:"error_type"`
	StartTime    int64             `json:"start_time,omitempty"`
	EndTime      *int64            `json:"end_time"`
	Metadata     map[string]string `json:"metadata"`
	RuntimeEnv   map[string]any    `json:"runtime_env"`
}
