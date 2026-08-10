package domain

import "sync"

// ResourceLimits caps how large a single training job may be. The ceilings
// track the cluster the platform is deployed on, so growing the GPU fleet is a
// configuration change rather than a code change and image rebuild.
type ResourceLimits struct {
	MaxWorkerReplicas int
	MaxGPUsPerWorker  int
	MaxTotalGPUs      int
}

// Defaults match the initial 3 × RTX 4090×8 fleet.
const (
	defaultMaxWorkerReplicas = 3
	defaultMaxGPUsPerWorker  = 8
	defaultMaxTotalGPUs      = 24
)

var (
	limitsMutex   sync.RWMutex
	currentLimits = ResourceLimits{
		MaxWorkerReplicas: defaultMaxWorkerReplicas,
		MaxGPUsPerWorker:  defaultMaxGPUsPerWorker,
		MaxTotalGPUs:      defaultMaxTotalGPUs,
	}
)

// SetResourceLimits installs the process-wide job size ceilings. It is called
// once at startup from configuration; any field left at zero keeps its
// default so a partial override cannot accidentally remove a ceiling.
func SetResourceLimits(limits ResourceLimits) {
	limitsMutex.Lock()
	defer limitsMutex.Unlock()
	currentLimits = ResourceLimits{
		MaxWorkerReplicas: positiveOr(limits.MaxWorkerReplicas, defaultMaxWorkerReplicas),
		MaxGPUsPerWorker:  positiveOr(limits.MaxGPUsPerWorker, defaultMaxGPUsPerWorker),
		MaxTotalGPUs:      positiveOr(limits.MaxTotalGPUs, defaultMaxTotalGPUs),
	}
}

func CurrentResourceLimits() ResourceLimits {
	limitsMutex.RLock()
	defer limitsMutex.RUnlock()
	return currentLimits
}

func positiveOr(value, fallback int) int {
	if value <= 0 {
		return fallback
	}
	return value
}
