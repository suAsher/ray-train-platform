package domain

type JobRuntime struct {
	JobID     string          `json:"jobId"`
	Namespace string          `json:"namespace"`
	Pods      []JobPodRuntime `json:"pods"`
}

type JobPodRuntime struct {
	Name         string `json:"name"`
	Role         string `json:"role"`
	Phase        string `json:"phase"`
	Reason       string `json:"reason,omitempty"`
	Message      string `json:"message,omitempty"`
	NodeName     string `json:"nodeName,omitempty"`
	PodIP        string `json:"podIp,omitempty"`
	GPURequested int64  `json:"gpuRequested"`
}
