package domain

type GPUNodeUsage struct {
	NodeName    string `json:"nodeName"`
	Capacity    int64  `json:"capacity"`
	Allocatable int64  `json:"allocatable"`
	Allocated   int64  `json:"allocated"`
	Available   int64  `json:"available"`
}

type ClusterTopologyOverview struct {
	TotalNodes int            `json:"totalNodes"`
	TotalGPUs  int            `json:"totalGpus"`
	UsedGPUs   int            `json:"usedGpus"`
	Nodes      []GPUNodeUsage `json:"nodes"`
}
