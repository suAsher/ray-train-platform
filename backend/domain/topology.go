package domain

type GPUNodeUsage struct {
	NodeName         string           `json:"nodeName"`
	Capacity         int64            `json:"capacity"`
	Allocatable      int64            `json:"allocatable"`
	Allocated        int64            `json:"allocated"`
	Available        int64            `json:"available"`
	NodeReady        *bool            `json:"nodeReady,omitempty"`
	Cordoned         *bool            `json:"cordoned,omitempty"`
	CacheReady       *bool            `json:"cacheReady,omitempty"`
	OnboardingStage  string           `json:"onboardingStage,omitempty"`
	OnboardingReason string           `json:"onboardingReason,omitempty"`
	AcceleratorClass AcceleratorClass `json:"acceleratorClass,omitempty"`
	GPUPool          string           `json:"gpuPool,omitempty"`
	AssignedTenant   string           `json:"assignedTenant,omitempty"`
}

type ClusterTopologyOverview struct {
	TotalNodes int            `json:"totalNodes"`
	TotalGPUs  int            `json:"totalGpus"`
	UsedGPUs   int            `json:"usedGpus"`
	Nodes      []GPUNodeUsage `json:"nodes"`
}
