package k8s

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"
	"unicode/utf8"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"ray-train-platform-backend/domain"
)

// GitCredentialResolver supplies the Secret name for a private repository, so
// the renderer only wires a credential in when the tenant registered one.
type GitCredentialResolver interface {
	GitCredentialSecretFor(ctx context.Context, tenantID, userID, repositoryURL string) string
}

type JobStore interface {
	GetByID(context.Context, string) (*domain.TrainingJob, error)
	ListReconcileCandidates(context.Context, int) ([]string, error)
	ClaimOutbox(context.Context, int) ([]domain.OutboxEvent, error)
	MarkOutboxDone(context.Context, string) error
	MarkOutboxRetry(context.Context, string, time.Time, string) error
	ApplyObservedState(context.Context, domain.ObservedJobState) error
	ReserveManagedAttemptIdentity(context.Context, domain.ManagedAttemptReservationRequest) (*domain.TrainingJob, bool, error)
	AdoptManagedAttemptIdentity(context.Context, domain.ManagedAttemptAdoptionRequest) (*domain.TrainingJob, bool, error)
	BeginManagedRecovery(context.Context, domain.ManagedRecoveryRequest) (*domain.TrainingJob, bool, error)
	ClearManagedRecoveryRetiringIdentity(context.Context, domain.ManagedRetiringIdentityRequest) (*domain.TrainingJob, bool, error)
}

type ExperimentFinalizer interface {
	FinalizeJobRuns(context.Context, string, string, domain.State, time.Time) error
}

type Reconciler struct {
	store            JobStore
	client           *Client
	renderOptions    RenderOptions
	interval         time.Duration
	clusterQueueName string
	autoQuota        bool
	gitCredentials   GitCredentialResolver
	experimentRuns   ExperimentFinalizer
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

func (r *Reconciler) WithExperimentFinalizer(finalizer ExperimentFinalizer) *Reconciler {
	r.experimentRuns = finalizer
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
		err = domain.UpdateResourceLimitsFromCapacity(capacity.Nodes, capacity.GuaranteedGPUsPerWorker, capacity.GPUs)
	}
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
	if event.EventType != "TRAINING_JOB_SUBMITTED" && event.EventType != "TRAINING_JOB_CANCEL_REQUESTED" && event.EventType != "TRAINING_JOB_TERMINAL" {
		return nil
	}
	var payload struct {
		JobID string `json:"job_id"`
	}
	if err := json.Unmarshal(event.Payload, &payload); err != nil || payload.JobID == "" {
		return fmt.Errorf("invalid outbox payload for %s", event.ID)
	}
	if event.EventType == "TRAINING_JOB_TERMINAL" {
		if r.experimentRuns == nil {
			return nil
		}
		job, err := r.store.GetByID(ctx, payload.JobID)
		if err != nil {
			return err
		}
		if !terminalJobState(job.ObservedState) {
			return fmt.Errorf("terminal event %s references non-terminal job %s", event.ID, job.ObservedState)
		}
		finishedAt := time.Now().UTC()
		if job.FinishedAt != nil {
			finishedAt = job.FinishedAt.UTC()
		}
		return r.experimentRuns.FinalizeJobRuns(ctx, job.TenantID, job.ID, job.ObservedState, finishedAt)
	}
	return r.ReconcileJob(ctx, payload.JobID)
}

func terminalJobState(state domain.State) bool {
	switch state {
	case domain.StateSucceeded, domain.StateFailed, domain.StateCanceled, domain.StateTimedOut:
		return true
	default:
		return false
	}
}

func (r *Reconciler) ReconcileJob(ctx context.Context, jobID string) error {
	job, err := r.store.GetByID(ctx, jobID)
	if err != nil {
		return err
	}
	if terminalJobState(job.ObservedState) {
		return nil
	}
	return r.reconcileLoadedJob(ctx, job)
}

func (r *Reconciler) reconcileLoadedJob(ctx context.Context, job *domain.TrainingJob) error {
	if job.DesiredState == domain.DesiredCanceled {
		return r.reconcileCancellation(ctx, job)
	}
	if managedJobHasRetiringIdentity(*job) {
		return r.reconcileManagedRetirement(ctx, job)
	}
	managed := job.Spec.TrainingEngine.Resolved() == domain.TrainingEngineRayTrain
	needsAdoption := false
	if managed && strings.TrimSpace(job.RayJobUID) == "" {
		reserved, current, err := r.reserveManagedAttempt(ctx, job)
		if err != nil {
			return err
		}
		if !reserved {
			return r.reconcileCurrentManagedJob(ctx, current)
		}
		job = current
		needsAdoption = true
	}
	options := r.renderOptions
	if r.gitCredentials != nil && job.Spec.Source.Type == "git" {
		options.GitCredentialSecret = r.gitCredentials.GitCredentialSecretFor(ctx, job.TenantID, job.UserID, job.Spec.Source.URL)
	}
	manifest, err := RenderRayJob(*job, options)
	if err != nil {
		return err
	}
	resource, err := r.getOrEnsureRayJob(ctx, job, manifest)
	if apierrors.IsNotFound(err) && managedJobHasPersistedIdentity(*job) {
		return r.reconcileMissingManagedWorkload(ctx, job, manifest.GetNamespace())
	}
	if err != nil {
		return err
	}
	if needsAdoption {
		current, adopted, adoptionErr := r.adoptManagedAttempt(ctx, job, resource)
		if adoptionErr != nil {
			return adoptionErr
		}
		if !adopted {
			if deleteErr := r.client.DeleteRayJob(ctx, resource.GetNamespace(), resource.GetName(), job.ID, string(resource.GetUID())); deleteErr != nil && !apierrors.IsNotFound(deleteErr) {
				return deleteErr
			}
			return r.reconcileCurrentManagedJob(ctx, current)
		}
		job = current
		if job.DesiredState == domain.DesiredCanceled {
			return r.reconcileCancellation(ctx, job)
		}
		if terminalJobState(job.ObservedState) {
			return nil
		}
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
	setObservedStateFence(&observed, *job)
	if observed.State == domain.StateFailed && managedJobCanRecover(*job) {
		failureClass, recoverable := managedInfrastructureFailureClass(observed.Reason)
		if recoverable {
			recovered, transitioned, recoveryErr := r.store.BeginManagedRecovery(ctx, domain.ManagedRecoveryRequest{
				JobID: job.ID, ExpectedClusterAttempt: job.ClusterAttempt,
				ExpectedRayJobName: job.RayJobName, ExpectedRayJobUID: job.RayJobUID,
				FailureClass: failureClass, FailureMessage: managedRecoveryFailureMessage(observed.Message),
			})
			if recoveryErr != nil {
				return recoveryErr
			}
			if recovered == nil {
				return fmt.Errorf("managed recovery returned no job")
			}
			if recovered.DesiredState == domain.DesiredCanceled {
				return r.reconcileCancellation(ctx, recovered)
			}
			if transitioned || recovered.ClusterAttempt != job.ClusterAttempt {
				return r.reconcileLoadedJob(ctx, recovered)
			}
		}
	}
	if job.ObservedState == domain.StateRecovering {
		switch observed.State {
		case domain.StateQueued, domain.StateProvisioning, domain.StateUnknown:
			observed.State = domain.StateRecovering
			observed.Reason = job.StatusReason
			observed.Message = job.StatusMessage
		case domain.StateSucceeded:
			// A short recovered attempt may finish between polls. Persist the
			// required RECOVERING -> RUNNING edge before the terminal result.
			running := observed
			running.State = domain.StateRunning
			running.FinishedAt = nil
			if err := r.store.ApplyObservedState(ctx, running); err != nil {
				return err
			}
		}
	}
	// Persist the terminal result before best-effort cleanup tuning. A transient
	// Kubernetes update conflict must never leave a completed job non-terminal
	// in PostgreSQL, where a later reconcile could recreate the workload.
	if err := r.store.ApplyObservedState(ctx, observed); err != nil {
		return err
	}
	if observed.State == domain.StateSucceeded {
		_, err = r.client.UpdateRayJobCleanupTTL(ctx, resource, job.ID, successCleanupTTL(*job))
		if err != nil {
			return err
		}
	}
	return nil
}

func (r *Reconciler) reserveManagedAttempt(ctx context.Context, loaded *domain.TrainingJob) (bool, *domain.TrainingJob, error) {
	if strings.TrimSpace(loaded.RayJobUID) != "" {
		return false, nil, fmt.Errorf("managed attempt reservation requires an empty UID")
	}
	current, reserved, err := r.store.ReserveManagedAttemptIdentity(ctx, domain.ManagedAttemptReservationRequest{
		JobID: loaded.ID, ExpectedClusterAttempt: loaded.ClusterAttempt,
		ExpectedState: loaded.ObservedState, ExpectedRayJobName: loaded.RayJobName,
		RayJobName: managedAttemptRayJobName(*loaded),
	})
	if err != nil {
		return false, nil, err
	}
	if current == nil {
		return false, nil, fmt.Errorf("managed attempt reservation returned no job")
	}
	return reserved, current, nil
}

func (r *Reconciler) adoptManagedAttempt(ctx context.Context, reserved *domain.TrainingJob, resource *unstructured.Unstructured) (*domain.TrainingJob, bool, error) {
	uid := strings.TrimSpace(string(resource.GetUID()))
	if uid == "" {
		return nil, false, fmt.Errorf("managed RayJob %s has no Kubernetes UID", resource.GetName())
	}
	current, adopted, err := r.store.AdoptManagedAttemptIdentity(ctx, domain.ManagedAttemptAdoptionRequest{
		JobID: reserved.ID, ExpectedClusterAttempt: reserved.ClusterAttempt,
		ExpectedState: reserved.ObservedState, RayJobName: resource.GetName(), RayJobUID: uid,
		KubernetesNS: resource.GetNamespace(), ResourceVersion: resource.GetResourceVersion(),
	})
	if err != nil {
		return nil, false, err
	}
	if current == nil {
		return nil, false, fmt.Errorf("managed attempt adoption returned no job")
	}
	return current, adopted, nil
}

func (r *Reconciler) reconcileCurrentManagedJob(ctx context.Context, current *domain.TrainingJob) error {
	if current == nil {
		return fmt.Errorf("managed attempt CAS returned no current job")
	}
	if terminalJobState(current.ObservedState) {
		return nil
	}
	return r.reconcileLoadedJob(ctx, current)
}

func (r *Reconciler) getOrEnsureRayJob(ctx context.Context, job *domain.TrainingJob, manifest *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	if job.Spec.TrainingEngine.Resolved() != domain.TrainingEngineRayTrain {
		return r.client.EnsureRayJob(ctx, manifest)
	}
	name, uid := strings.TrimSpace(job.RayJobName), strings.TrimSpace(job.RayJobUID)
	if name == "" && uid != "" {
		return nil, fmt.Errorf("managed RayJob persisted identity is incomplete")
	}
	if uid == "" {
		if name == "" {
			return nil, fmt.Errorf("managed RayJob name was not reserved")
		}
		return r.client.EnsureRayJob(ctx, manifest)
	}
	return r.client.GetOwnedRayJob(ctx, manifest.GetNamespace(), name, job.ID, uid)
}

func managedJobHasPersistedIdentity(job domain.TrainingJob) bool {
	return job.Spec.TrainingEngine.Resolved() == domain.TrainingEngineRayTrain &&
		strings.TrimSpace(job.RayJobName) != "" && strings.TrimSpace(job.RayJobUID) != ""
}

func (r *Reconciler) reconcileMissingManagedWorkload(ctx context.Context, job *domain.TrainingJob, namespace string) error {
	const failureClass = "RAY_CLUSTER_DELETED"
	const failureMessage = "persisted managed RayJob is missing"
	recovered, transitioned, err := r.store.BeginManagedRecovery(ctx, domain.ManagedRecoveryRequest{
		JobID: job.ID, ExpectedClusterAttempt: job.ClusterAttempt,
		ExpectedRayJobName: job.RayJobName, ExpectedRayJobUID: job.RayJobUID,
		FailureClass: failureClass, FailureMessage: failureMessage,
	})
	if err != nil {
		return err
	}
	if recovered == nil {
		return fmt.Errorf("managed recovery returned no job")
	}
	if recovered.DesiredState == domain.DesiredCanceled {
		return r.reconcileCancellation(ctx, recovered)
	}
	if transitioned || managedJobSnapshotChanged(*job, *recovered) {
		if terminalJobState(recovered.ObservedState) {
			return nil
		}
		return r.reconcileLoadedJob(ctx, recovered)
	}
	observed := domain.ObservedJobState{
		ID: job.ID, State: domain.StateFailed, Reason: failureClass, Message: failureMessage,
		KubernetesNS: namespace, RayJobName: job.RayJobName, RayJobUID: job.RayJobUID,
	}
	setObservedStateFence(&observed, *job)
	return r.store.ApplyObservedState(ctx, observed)
}

func managedJobSnapshotChanged(loaded, current domain.TrainingJob) bool {
	return current.ClusterAttempt != loaded.ClusterAttempt ||
		current.RayJobName != loaded.RayJobName || current.RayJobUID != loaded.RayJobUID ||
		current.ObservedState != loaded.ObservedState || current.DesiredState != loaded.DesiredState
}

func setObservedStateFence(observed *domain.ObservedJobState, job domain.TrainingJob) {
	observed.ExpectedClusterAttempt = job.ClusterAttempt
	observed.ExpectedRayJobName = job.RayJobName
	observed.ExpectedRayJobUID = job.RayJobUID
}

func managedJobHasRetiringIdentity(job domain.TrainingJob) bool {
	return job.Spec.TrainingEngine.Resolved() == domain.TrainingEngineRayTrain &&
		job.ObservedState == domain.StateRecovering && job.RayJobName != "" &&
		job.RayJobName != managedAttemptRayJobName(job)
}

func (r *Reconciler) reconcileManagedRetirement(ctx context.Context, job *domain.TrainingJob) error {
	if job.DesiredState == domain.DesiredCanceled {
		return r.reconcileCancellation(ctx, job)
	}
	if strings.TrimSpace(job.RayJobUID) == "" {
		return fmt.Errorf("managed retiring RayJob UID is missing")
	}
	namespace := job.KubernetesNS
	if namespace == "" {
		manifest, err := RenderRayJob(*job, r.renderOptions)
		if err != nil {
			return err
		}
		namespace = manifest.GetNamespace()
	}
	resource, err := r.client.GetRayJob(ctx, namespace, job.RayJobName)
	if err == nil {
		if string(resource.GetUID()) != job.RayJobUID {
			return fmt.Errorf("refusing to retire RayJob with an unexpected UID")
		}
		if err := r.client.DeleteRayJob(ctx, namespace, job.RayJobName, job.ID, job.RayJobUID); err != nil {
			return err
		}
		if _, err = r.client.GetRayJob(ctx, namespace, job.RayJobName); err == nil {
			return nil
		}
	}
	if !apierrors.IsNotFound(err) {
		return err
	}
	current, cleared, err := r.store.ClearManagedRecoveryRetiringIdentity(ctx, domain.ManagedRetiringIdentityRequest{
		JobID: job.ID, ExpectedClusterAttempt: job.ClusterAttempt,
		RayJobName: job.RayJobName, RayJobUID: job.RayJobUID,
	})
	if err != nil {
		return err
	}
	if current == nil {
		return fmt.Errorf("managed retirement returned no job")
	}
	if current.DesiredState == domain.DesiredCanceled {
		return r.reconcileCancellation(ctx, current)
	}
	if cleared || !managedJobHasRetiringIdentity(*current) {
		return r.reconcileLoadedJob(ctx, current)
	}
	return nil
}

func managedJobCanRecover(job domain.TrainingJob) bool {
	return job.DesiredState == domain.DesiredActive &&
		job.Spec.TrainingEngine.Resolved() == domain.TrainingEngineRayTrain &&
		(job.ObservedState == domain.StateRunning || job.ObservedState == domain.StateRecovering)
}

func managedInfrastructureFailureClass(reason string) (string, bool) {
	return domain.NormalizeManagedInfrastructureFailureClass(reason)
}

func managedRecoveryFailureMessage(message string) string {
	if len(message) <= domain.ManagedRecoveryFailureMessageMaxBytes {
		return message
	}
	bounded := message[:domain.ManagedRecoveryFailureMessageMaxBytes]
	for !utf8.ValidString(bounded) {
		bounded = bounded[:len(bounded)-1]
	}
	return bounded
}

func isManagedInfrastructureFailure(reason string) bool {
	_, ok := managedInfrastructureFailureClass(reason)
	return ok
}

func (r *Reconciler) reconcileCancellation(ctx context.Context, job *domain.TrainingJob) error {
	name := job.RayJobName
	if name == "" {
		name = rayJobResourceName(*job)
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
		observed := domain.ObservedJobState{
			ID: job.ID, State: domain.StateCanceled, KubernetesNS: namespace,
			RayJobName: name, RayJobUID: job.RayJobUID,
		}
		setObservedStateFence(&observed, *job)
		return r.store.ApplyObservedState(ctx, observed)
	}
	if err != nil {
		return err
	}
	expectedUID := job.RayJobUID
	if expectedUID == "" {
		expectedUID = string(resource.GetUID())
	}
	if job.RayJobUID != "" && string(resource.GetUID()) != job.RayJobUID {
		return fmt.Errorf("refusing to cancel RayJob with an unexpected UID")
	}
	if err := r.client.DeleteRayJob(ctx, namespace, name, job.ID, expectedUID); err != nil {
		return err
	}
	state := domain.StateDeleting
	if job.ObservedState == domain.StateRecovering {
		state = domain.StateCanceled
	}
	observed := domain.ObservedJobState{ID: job.ID, State: state, KubernetesNS: namespace, RayJobName: name, RayJobUID: string(resource.GetUID()), ResourceVersion: resource.GetResourceVersion()}
	setObservedStateFence(&observed, *job)
	return r.store.ApplyObservedState(ctx, observed)
}
