package api

import (
	"context"
	"log"
	"ray-train-platform-backend/domain"
	ms "ray-train-platform-backend/modelserving"
	"time"
)

// RunModelServing observes only jobs bound to serving reservations. Kubernetes
// job admission, cancellation and compute reclamation stay with the reconciler.
func (h *Handler) RunModelServing(ctx context.Context) {
	if h.modelServing == nil || h.modelServingKubernetes == nil {
		return
	}
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			items, err := h.modelServing.GetPendingDeployments(ctx, 100)
			if err != nil {
				log.Print("model serving reconciliation query failed")
				continue
			}
			for _, d := range items {
				if ctx.Err() != nil {
					return
				}
				itemCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
				h.reconcileModelService(itemCtx, d)
				cancel()
			}
		}
	}
}
func (h *Handler) reconcileModelService(ctx context.Context, d ms.Deployment) {
	job, err := h.repository.Get(ctx, d.TenantID, d.JobID)
	if err != nil {
		// Unknown DB/network state must never be treated as reclaimed compute.
		if d.State == ms.Creating && !d.ExpiresAt.After(time.Now()) {
			_, _ = h.modelServing.RequestStop(ctx, d.ID, d.OwnerID, d.Revision)
		}
		return
	}
	if !servingJobMatches(job, d) {
		return
	}
	expired := !d.ExpiresAt.After(time.Now())
	if servingJobTerminal(job) {
		if job.RayJobUID != "" {
			if err := h.modelServingKubernetes.DeleteModelServingService(ctx, job.KubernetesNS, job.ID, job.RayJobUID); err != nil {
				return
			}
		}
		state := ms.Failed
		if expired {
			state = ms.Expired
		} else if d.State == ms.Stopping || job.DesiredState == domain.DesiredCanceled {
			state = ms.Stopped
		}
		_, _ = h.modelServing.UpdateObserved(ctx, d.ID, state, d.Revision)
		return
	}
	if d.State == ms.Stopping || expired {
		if d.State != ms.Stopping {
			changed, err := h.modelServing.RequestStop(ctx, d.ID, d.OwnerID, d.Revision)
			if err != nil {
				return
			}
			d = changed
		}
		if err := h.repository.SetDesiredState(ctx, d.TenantID, d.JobID, domain.DesiredCanceled); err != nil {
			return
		}
		_, _ = h.modelServing.UpdateObserved(ctx, d.ID, ms.Stopping, d.Revision)
		return
	}
	target, err := h.servingTarget(ctx, d)
	state := ms.Submitted
	if err == nil && checkServingHealth(ctx, target, d) == nil {
		state = ms.Ready
	}
	_, _ = h.modelServing.UpdateObserved(ctx, d.ID, state, d.Revision)
}
