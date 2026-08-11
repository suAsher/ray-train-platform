package k8s

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"ray-train-platform-backend/domain"
)

// GitCredentialResolver supplies the Secret name for a private repository, so
// the renderer only wires a credential in when the tenant registered one.
type GitCredentialResolver interface {
	GitCredentialSecretFor(ctx context.Context, tenantID, repositoryURL string) string
}

type JobStore interface {
	GetByID(context.Context, string) (*domain.TrainingJob, error)
	ListReconcileCandidates(context.Context, int) ([]string, error)
	ClaimOutbox(context.Context, int) ([]domain.OutboxEvent, error)
	MarkOutboxDone(context.Context, string) error
	MarkOutboxRetry(context.Context, string, time.Time, string) error
	ApplyObservedState(context.Context, domain.ObservedJobState) error
}

type Reconciler struct {
	store            JobStore
	client           *Client
	renderOptions    RenderOptions
	interval         time.Duration
	clusterQueueName string
	autoQuota        bool
	gitCredentials   GitCredentialResolver
	lastQuotaError   string
}

// QuotaSyncOptions turns on continuous alignment of the Kueue admission budget
// with the measured training pool.
type QuotaSyncOptions struct {
	ClusterQueueName string
	Enabled          bool
}

func (r *Reconciler) WithGitCredentials(resolver GitCredentialResolver) *Reconciler {
	r.gitCredentials = resolver
	return r
}

func (r *Reconciler) WithQuotaSync(options QuotaSyncOptions) *Reconciler {
	r.clusterQueueName = options.ClusterQueueName
	r.autoQuota = options.Enabled && options.ClusterQueueName != ""
	return r
}

// syncClusterQueueQuota keeps the Kueue budget in step with the hardware so an
// operator only has to label a new machine. Failures are logged once rather
// than on every tick and never abort job reconciliation.
func (r *Reconciler) syncClusterQueueQuota(ctx context.Context) {
	if !r.autoQuota || r.client == nil {
		return
	}
	capacity, err := r.client.TrainingPoolCapacity(ctx, r.renderOptions.NodeSelector)
	if err == nil {
		var changed bool
		changed, err = r.client.SyncClusterQueueQuota(ctx, r.clusterQueueName, capacity)
		if err == nil {
			r.lastQuotaError = ""
			if changed {
				log.Printf("kueue quota synced from training pool: %d nodes, %d GPUs, %d cores", capacity.Nodes, capacity.GPUs, capacity.CPUMillis/1000)
			}
			return
		}
	}
	if message := err.Error(); message != r.lastQuotaError {
		r.lastQuotaError = message
		log.Printf("kueue quota sync skipped: %v", err)
	}
}

func NewReconciler(store JobStore, client *Client, renderOptions RenderOptions) *Reconciler {
	return &Reconciler{store: store, client: client, renderOptions: renderOptions, interval: 5 * time.Second}
}

func (r *Reconciler) Run(ctx context.Context) error {
	if r == nil || r.store == nil || r.client == nil {
		return fmt.Errorf("reconciler is not initialized")
	}
	r.syncClusterQueueQuota(ctx)
	if err := r.ProcessOnce(ctx); err != nil {
		return err
	}
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			r.syncClusterQueueQuota(ctx)
			if err := r.ProcessOnce(ctx); err != nil {
				// A single transient Kubernetes or database error must not terminate the control loop.
				continue
			}
		}
	}
}

func (r *Reconciler) ProcessOnce(ctx context.Context) error {
	events, err := r.store.ClaimOutbox(ctx, 50)
	if err != nil {
		return err
	}
	var firstErr error
	for _, event := range events {
		if err := r.processEvent(ctx, event); err != nil {
			if markErr := r.store.MarkOutboxRetry(ctx, event.ID, time.Now().UTC().Add(15*time.Second), err.Error()); markErr != nil && firstErr == nil {
				firstErr = markErr
			}
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if err := r.store.MarkOutboxDone(ctx, event.ID); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	ids, err := r.store.ListReconcileCandidates(ctx, 100)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err := r.ReconcileJob(ctx, id); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (r *Reconciler) processEvent(ctx context.Context, event domain.OutboxEvent) error {
	if event.EventType != "TRAINING_JOB_SUBMITTED" && event.EventType != "TRAINING_JOB_CANCEL_REQUESTED" {
		return nil
	}
	var payload struct {
		JobID string `json:"job_id"`
	}
	if err := json.Unmarshal(event.Payload, &payload); err != nil || payload.JobID == "" {
		return fmt.Errorf("invalid outbox payload for %s", event.ID)
	}
	return r.ReconcileJob(ctx, payload.JobID)
}

func (r *Reconciler) ReconcileJob(ctx context.Context, jobID string) error {
	job, err := r.store.GetByID(ctx, jobID)
	if err != nil {
		return err
	}
	if job.DesiredState == domain.DesiredCanceled {
		return r.reconcileCancellation(ctx, job)
	}
	options := r.renderOptions
	if r.gitCredentials != nil && job.Spec.Source.Type == "git" {
		options.GitCredentialSecret = r.gitCredentials.GitCredentialSecretFor(ctx, job.TenantID, job.Spec.Source.URL)
	}
	manifest, err := RenderRayJob(*job, options)
	if err != nil {
		return err
	}
	resource, err := r.client.EnsureRayJob(ctx, manifest)
	if err != nil {
		return err
	}
	status, found, statusErr := nestedMap(resource.Object, "status")
	if statusErr != nil {
		return statusErr
	}
	if !found {
		status = map[string]any{}
	}
	observed := MapRayJobStatus(job.ID, status, resource.GetResourceVersion())
	observed.KubernetesNS = resource.GetNamespace()
	observed.RayJobName = resource.GetName()
	observed.RayJobUID = string(resource.GetUID())
	return r.store.ApplyObservedState(ctx, observed)
}

func (r *Reconciler) reconcileCancellation(ctx context.Context, job *domain.TrainingJob) error {
	name := job.RayJobName
	if name == "" {
		name = job.Spec.Name
	}
	namespace := job.KubernetesNS
	if namespace == "" {
		manifest, err := RenderRayJob(*job, r.renderOptions)
		if err != nil {
			return err
		}
		namespace = manifest.GetNamespace()
	}
	resource, err := r.client.GetRayJob(ctx, namespace, name)
	if apierrors.IsNotFound(err) {
		return r.store.ApplyObservedState(ctx, domain.ObservedJobState{ID: job.ID, State: domain.StateCanceled, KubernetesNS: namespace, RayJobName: name})
	}
	if err != nil {
		return err
	}
	if err := r.client.DeleteRayJob(ctx, namespace, name, job.ID); err != nil {
		return err
	}
	return r.store.ApplyObservedState(ctx, domain.ObservedJobState{ID: job.ID, State: domain.StateDeleting, KubernetesNS: namespace, RayJobName: name, RayJobUID: string(resource.GetUID()), ResourceVersion: resource.GetResourceVersion()})
}
