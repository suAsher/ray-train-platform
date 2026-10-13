package api

import (
	"context"
	"strings"
	"time"

	"ray-train-platform-backend/domain"
)

// The snapshot helps explain early scheduling, while retained logs and the
// observed platform status remain the authoritative evidence for startup errors.
func (h *Handler) jobSchedulingCapacity(ctx context.Context, job domain.TrainingJob) *domain.SchedulingCapacity {
	switch job.ObservedState {
	case domain.StateSubmitted, domain.StateValidating, domain.StateQueued, domain.StateAdmitted, domain.StateProvisioning:
	default:
		return nil
	}
	// Allow a cold shared capacity observation plus the job's admission lookup.
	queryCtx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	capacity, err := h.schedulingCapacity(queryCtx, job.TenantID, job.Spec.Resources, job.ID)
	capacity.Nodes = nil // Per-job diagnosis never exposes a platform node inventory.
	if err != nil {
		capacity.Available = false
		if capacity.UnavailableReason == "" {
			capacity.UnavailableReason = "capacity_observation_unavailable"
		}
	}
	if capacity.ObservedAt.IsZero() {
		capacity.ObservedAt = time.Now().UTC()
	}
	if capacity.AcceleratorClass != "" && capacity.AcceleratorClass != string(job.Spec.AcceleratorClass.Resolved()) {
		capacity.Available = false
		capacity.UnavailableReason = "job_pool_changed"
	}
	capacity = jobCapacityQuota(capacity, job)
	if schedulingStartupEvidence(job.StatusReason) {
		capacity.Fit.Reason = "startup_status_capacity_reference"
		return &capacity
	}
	// Provisioning is past the queue, even if a transient Workload read fails.
	if job.ObservedState == domain.StateAdmitted || job.ObservedState == domain.StateProvisioning {
		capacity.Fit.Reason = "already_admitted_capacity_reference"
		return &capacity
	}
	admitted, known, admissionErr := h.kubernetes.JobSchedulingAdmitted(queryCtx, job)
	if admissionErr != nil || (!known && job.RayJobUID != "") {
		capacity.Fit.Reason = "admission_unknown_capacity_reference"
	} else if admitted {
		capacity.Fit.Reason = "already_admitted_capacity_reference"
	}
	return &capacity
}

func jobCapacityQuota(capacity domain.SchedulingCapacity, job domain.TrainingJob) domain.SchedulingCapacity {
	if capacity.Quota == nil {
		return capacity
	}
	quota := *capacity.Quota
	requested := job.Spec.Resources.WorkerReplicas * job.Spec.Resources.GPUsPerWorker
	// The same non-terminal ACTIVE rows are already counted by TenantGPUQuota.
	// Compare this job against the budget excluding its own reservation.
	if job.DesiredState == domain.DesiredActive {
		quota.GPUUsed = max(0, quota.GPUUsed-requested)
		quota.GPUAvailable = max(0, quota.GPULimit-quota.GPUUsed)
	}
	capacity.Quota = &quota
	if capacity.Available && requested > quota.GPUAvailable {
		capacity.Fit.CanFit = false
		capacity.Fit.Reason = "team_quota_exceeded"
	}
	return capacity
}

// These concrete startup failures do not become a GPU shortage merely because
// unrelated jobs have filled the pool after this task began provisioning.
func schedulingStartupEvidence(reason string) bool {
	switch strings.ToLower(strings.TrimSpace(reason)) {
	case "imagepullbackoff", "errimagepull", "failedmount", "failedattachvolume", "volumemountfailed", "createcontainerconfigerror", "createcontainererror", "crashloopbackoff":
		return true
	default:
		return false
	}
}
