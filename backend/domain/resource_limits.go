package domain

import (
	"fmt"
	"sync"
)

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

// UpdateResourceLimitsFromCapacity atomically replaces the runtime ceilings
// after a complete, internally consistent training-pool observation. Invalid
// observations leave the deployment profile or last valid observation intact.
func UpdateResourceLimitsFromCapacity(readyNodes int, guaranteedGPUsPerWorker, totalGPUs int64) error {
	if readyNodes <= 0 {
		return fmt.Errorf("observed training capacity must include at least one Ready node")
	}
	if guaranteedGPUsPerWorker <= 0 {
		return fmt.Errorf("observed training capacity must include GPUs on at least one node")
	}
	if totalGPUs <= 0 {
		return fmt.Errorf("observed training capacity must include at least one GPU")
	}
	// Division keeps this validation safe even when the corresponding
	// readyNodes*guaranteedGPUsPerWorker multiplication would overflow.
	if int64(readyNodes) > totalGPUs/guaranteedGPUsPerWorker {
		return fmt.Errorf("observed total GPUs cannot cover the guaranteed worker shape")
	}
	maxGPUsPerWorker := int(guaranteedGPUsPerWorker)
	maxTotalGPUs := int(totalGPUs)
	if int64(maxGPUsPerWorker) != guaranteedGPUsPerWorker || int64(maxTotalGPUs) != totalGPUs {
		return fmt.Errorf("observed GPU capacity exceeds supported integer limits")
	}

	limitsMutex.Lock()
	defer limitsMutex.Unlock()
	currentLimits = ResourceLimits{
		MaxWorkerReplicas: readyNodes,
		MaxGPUsPerWorker:  maxGPUsPerWorker,
		MaxTotalGPUs:      maxTotalGPUs,
	}
	return nil
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
