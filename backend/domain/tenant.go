package domain

import (
	"fmt"
	"strings"
)

// Tenant is a team with its own Kubernetes namespace, Kueue queue and GPU
// budget.
type Tenant struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Namespace     string `json:"namespace"`
	LocalQueue    string `json:"localQueue"`
	GPUQuotaLimit int    `json:"gpuQuotaLimit"`
	// AcceleratorClass is the team's platform-managed GPU pool. Submission
	// clients never choose it, preventing one distributed job from mixing GPU
	// architectures or escaping into another team's pool.
	AcceleratorClass AcceleratorClass `json:"acceleratorClass"`
}

func (t Tenant) Validate() error {
	if strings.TrimSpace(t.ID) == "" {
		return fmt.Errorf("tenant id is required")
	}
	if strings.TrimSpace(t.Namespace) == "" || strings.TrimSpace(t.LocalQueue) == "" {
		return fmt.Errorf("tenant namespace and queue are required")
	}
	if t.GPUQuotaLimit < 0 {
		return fmt.Errorf("gpu quota cannot be negative")
	}
	if err := t.AcceleratorClass.Validate(); err != nil {
		return err
	}
	return nil
}
