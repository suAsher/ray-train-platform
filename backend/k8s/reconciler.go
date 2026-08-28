package k8s

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strconv"
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
	AcquireManagedAttemptCreation(context.Context, domain.ManagedAttemptCreationLeaseRequest, time.Time) (*domain.TrainingJob, *domain.ManagedAttemptResource, bool, error)
	AdoptManagedAttemptIdentity(context.Context, domain.ManagedAttemptAdoptionRequest) (*domain.TrainingJob, bool, error)
	AuthorizeManagedAttemptActivation(context.Context, domain.ManagedAttemptActivationRequest) (*domain.TrainingJob, *domain.ManagedAttemptResource, bool, error)
	ConfirmManagedAttemptActivation(context.Context, domain.ManagedAttemptActivationRequest) (*domain.TrainingJob, bool, error)
	RetireManagedAttemptResource(context.Context, domain.ManagedAttemptRetireRequest) (*domain.ManagedAttemptResource, bool, error)
	ListManagedAttemptCleanup(context.Context, int, time.Time) ([]domain.ManagedAttemptResource, error)
	ListManagedAttemptTombstoneAudit(context.Context, int, time.Time) ([]domain.ManagedAttemptResource, error)
	RecordManagedAttemptCleanupFailure(context.Context, domain.ManagedAttemptCleanupFailureRequest) error
	CompleteManagedAttemptCleanup(context.Context, domain.ManagedAttemptCleanupRequest) (bool, error)
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
	leaseOwner       string
	creationLease    time.Duration
	cleanupWait      time.Duration
	cleanupPoll      time.Duration
	now              func() time.Time
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
	return &Reconciler{
		store: store, client: client, renderOptions: renderOptions, interval: 5 * time.Second,
		leaseOwner: newReconcilerLeaseOwner(), creationLease: 30 * time.Second,
		cleanupWait: 100 * time.Millisecond, cleanupPoll: 25 * time.Millisecond,
		now: func() time.Time { return time.Now().UTC() },
	}
}

func newReconcilerLeaseOwner() string {
	var value [12]byte
	if _, err := rand.Read(value[:]); err == nil {
		return fmt.Sprintf("reconciler-%x", value[:])
	}
	return fmt.Sprintf("reconciler-%d", time.Now().UnixNano())
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
	cleanupErr := r.reconcileManagedAttemptCleanups(ctx)
	events, err := r.store.ClaimOutbox(ctx, 50)
	if err != nil {
		return err
	}
	firstErr := cleanupErr
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

func (r *Reconciler) reconcileManagedAttemptCleanups(ctx context.Context) error {
	resources, err := r.store.ListManagedAttemptCleanup(ctx, 20, r.now())
	if err != nil {
		return err
	}
	if err := r.reconcileManagedAttemptCleanupBatch(ctx, resources); err != nil {
		return err
	}
	audits, err := r.store.ListManagedAttemptTombstoneAudit(ctx, 2, r.now())
	if err != nil {
		return err
	}
	return r.reconcileManagedAttemptCleanupBatch(ctx, audits)
}

func (r *Reconciler) reconcileManagedAttemptCleanupBatch(ctx context.Context, resources []domain.ManagedAttemptResource) error {
	var firstErr error
	for _, resource := range resources {
		if _, err := r.cleanupManagedAttemptResource(ctx, resource); err != nil {
			var permanent *managedCleanupVerificationError
			recordErr := r.store.RecordManagedAttemptCleanupFailure(ctx, domain.ManagedAttemptCleanupFailureRequest{
				JobID: resource.JobID, ClusterAttempt: resource.ClusterAttempt,
				RayJobName: resource.RayJobName, RayJobUID: resource.RayJobUID,
				Message: err.Error(), Permanent: errors.As(err, &permanent), ObservedAt: r.now(),
			})
			if recordErr != nil && firstErr == nil {
				firstErr = recordErr
			}
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
	var creationLease *domain.ManagedAttemptResource
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
		current, lease, acquired, err := r.store.AcquireManagedAttemptCreation(ctx, domain.ManagedAttemptCreationLeaseRequest{
			JobID: job.ID, ExpectedClusterAttempt: job.ClusterAttempt, ExpectedState: job.ObservedState,
			RayJobName: job.RayJobName, LeaseOwner: r.leaseOwner, LeaseDuration: r.creationLease,
		}, r.now())
		if err != nil {
			return err
		}
		if current == nil {
			return fmt.Errorf("managed creation lease returned no job")
		}
		if !acquired {
			if managedJobSnapshotChanged(*job, *current) {
				return r.reconcileCurrentManagedJob(ctx, current)
			}
			return nil
		}
		job, creationLease = current, lease
	}
	options := r.renderOptions
	if creationLease != nil {
		options.managedCreationFence = creationLease.ResourceFence
	}
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
		resourceFence, fenceErr := managedResourceCreationFence(resource)
		if fenceErr != nil {
			return fenceErr
		}
		if resourceFence != creationLease.ResourceFence {
			if resourceFence > creationLease.ResourceFence {
				return fmt.Errorf("managed RayJob creation fence %d is newer than authorized fence %d", resourceFence, creationLease.ResourceFence)
			}
			return r.removeSupersededManagedCreation(ctx, job, resource, resourceFence)
		}
		current, adopted, adoptionErr := r.adoptManagedAttempt(ctx, job, creationLease, resource)
		if adoptionErr != nil {
			return adoptionErr
		}
		if !adopted {
			retiring, _, retireErr := r.store.RetireManagedAttemptResource(ctx, domain.ManagedAttemptRetireRequest{
				JobID: job.ID, ClusterAttempt: job.ClusterAttempt, KubernetesNS: resource.GetNamespace(),
				RayJobName: resource.GetName(), RayJobUID: string(resource.GetUID()),
			})
			if retireErr != nil {
				return retireErr
			}
			if retiring == nil {
				return fmt.Errorf("managed adoption compensation returned no resource")
			}
			cleaned, cleanupErr := r.cleanupManagedAttemptResource(ctx, *retiring)
			if cleanupErr != nil || !cleaned {
				return cleanupErr
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
	if managed {
		var current *domain.TrainingJob
		var activated bool
		resource, current, activated, err = r.activateManagedRayJob(ctx, job, resource)
		if err != nil {
			return err
		}
		if !activated {
			return r.reconcileCurrentManagedJob(ctx, current)
		}
		job = current
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
		RayJobName:   managedAttemptRayJobName(*loaded),
		KubernetesNS: managedJobNamespace(*loaded),
	})
	if err != nil {
		return false, nil, err
	}
	if current == nil {
		return false, nil, fmt.Errorf("managed attempt reservation returned no job")
	}
	return reserved, current, nil
}

func (r *Reconciler) adoptManagedAttempt(ctx context.Context, reserved *domain.TrainingJob, lease *domain.ManagedAttemptResource, resource *unstructured.Unstructured) (*domain.TrainingJob, bool, error) {
	uid := strings.TrimSpace(string(resource.GetUID()))
	if uid == "" {
		return nil, false, fmt.Errorf("managed RayJob %s has no Kubernetes UID", resource.GetName())
	}
	if lease == nil || lease.LeaseOwner != r.leaseOwner || lease.LeaseVersion < 1 {
		return nil, false, fmt.Errorf("managed RayJob creation lease is missing")
	}
	if resource.GetLabels()["ray.io/job-id"] != reserved.ID || resource.GetLabels()[managedAttemptIdentityKey] != strconv.Itoa(reserved.ClusterAttempt) || resource.GetAnnotations()[managedAttemptIdentityKey] != strconv.Itoa(reserved.ClusterAttempt) {
		return nil, false, fmt.Errorf("managed RayJob creation identity does not match the reserved attempt")
	}
	fence, err := managedResourceCreationFence(resource)
	if err != nil || fence != lease.ResourceFence || fence != lease.LeaseVersion {
		return nil, false, fmt.Errorf("managed RayJob creation fence does not match the current lease")
	}
	if err := verifyManagedRayJobFence(resource, reserved.ID, uid, reserved.ClusterAttempt, fence); err != nil {
		return nil, false, err
	}
	current, adopted, err := r.store.AdoptManagedAttemptIdentity(ctx, domain.ManagedAttemptAdoptionRequest{
		JobID: reserved.ID, ExpectedClusterAttempt: reserved.ClusterAttempt,
		ExpectedState: reserved.ObservedState, RayJobName: resource.GetName(), RayJobUID: uid,
		KubernetesNS: resource.GetNamespace(), ResourceVersion: resource.GetResourceVersion(),
		LeaseOwner: lease.LeaseOwner, LeaseVersion: lease.LeaseVersion, ResourceFence: fence,
	})
	if err != nil {
		return nil, false, err
	}
	if current == nil {
		return nil, false, fmt.Errorf("managed attempt adoption returned no job")
	}
	return current, adopted, nil
}

func (r *Reconciler) activateManagedRayJob(ctx context.Context, job *domain.TrainingJob, resource *unstructured.Unstructured) (*unstructured.Unstructured, *domain.TrainingJob, bool, error) {
	fence, err := managedResourceCreationFence(resource)
	if err != nil {
		return nil, nil, false, fmt.Errorf("managed RayJob %s has an invalid creation fence", resource.GetName())
	}
	request := domain.ManagedAttemptActivationRequest{
		JobID: job.ID, ExpectedClusterAttempt: job.ClusterAttempt,
		RayJobName: resource.GetName(), RayJobUID: string(resource.GetUID()), ResourceFence: fence,
	}
	current, ledger, authorized, err := r.store.AuthorizeManagedAttemptActivation(ctx, request)
	if err != nil {
		return nil, nil, false, err
	}
	if current == nil || ledger == nil {
		return nil, nil, false, fmt.Errorf("managed activation authorization returned incomplete state")
	}
	if !authorized {
		if err := r.compensateManagedActivation(ctx, job, resource, fence); err != nil {
			return nil, nil, false, err
		}
		return resource, current, false, nil
	}
	active, err := r.client.ActivateManagedRayJob(ctx, resource.GetNamespace(), resource.GetName(), job.ID, string(resource.GetUID()), job.ClusterAttempt, fence, job.Spec.Queue)
	if err != nil {
		return nil, nil, false, err
	}
	current, confirmed, err := r.store.ConfirmManagedAttemptActivation(ctx, request)
	if err != nil {
		return nil, nil, false, err
	}
	if current == nil {
		return nil, nil, false, fmt.Errorf("managed activation confirmation returned no job")
	}
	if !confirmed {
		if err := r.compensateManagedActivation(ctx, job, active, fence); err != nil {
			return nil, nil, false, err
		}
		return active, current, false, nil
	}
	return active, current, true, nil
}

func managedResourceCreationFence(resource *unstructured.Unstructured) (int64, error) {
	labelFence := resource.GetLabels()[managedCreationFenceKey]
	if labelFence == "" || resource.GetAnnotations()[managedCreationFenceKey] != labelFence {
		return 0, fmt.Errorf("creation fence label and annotation must match")
	}
	fence, err := strconv.ParseInt(labelFence, 10, 64)
	if err != nil || fence < 1 {
		return 0, fmt.Errorf("creation fence must be positive")
	}
	return fence, nil
}

func (r *Reconciler) removeSupersededManagedCreation(ctx context.Context, job *domain.TrainingJob, resource *unstructured.Unstructured, fence int64) error {
	uid := strings.TrimSpace(string(resource.GetUID()))
	if uid == "" {
		return fmt.Errorf("superseded managed RayJob %s has no UID", resource.GetName())
	}
	if err := verifyManagedRayJobFence(resource, job.ID, uid, job.ClusterAttempt, fence); err != nil {
		return err
	}
	if err := r.client.DeleteRayJob(ctx, resource.GetNamespace(), resource.GetName(), job.ID, uid); err != nil {
		return err
	}
	waitCtx, cancel := context.WithTimeout(ctx, r.cleanupWait)
	defer cancel()
	if err := r.client.WaitForRayJobDeletion(waitCtx, resource.GetNamespace(), resource.GetName()); err != nil {
		return fmt.Errorf("wait for superseded managed RayJob deletion: %w", err)
	}
	return nil
}

func (r *Reconciler) compensateManagedActivation(ctx context.Context, job *domain.TrainingJob, resource *unstructured.Unstructured, fence int64) error {
	uid := strings.TrimSpace(string(resource.GetUID()))
	retiring, _, err := r.store.RetireManagedAttemptResource(ctx, domain.ManagedAttemptRetireRequest{
		JobID: job.ID, ClusterAttempt: job.ClusterAttempt, KubernetesNS: resource.GetNamespace(),
		RayJobName: resource.GetName(), RayJobUID: uid,
	})
	if err != nil {
		return err
	}
	if retiring == nil {
		return fmt.Errorf("managed activation compensation returned no retirement ledger")
	}
	if err := r.client.DeactivateManagedRayJob(ctx, resource.GetNamespace(), resource.GetName(), job.ID, uid, job.ClusterAttempt, fence); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	_, err = r.cleanupManagedAttemptResource(ctx, *retiring)
	return err
}

func managedJobNamespace(job domain.TrainingJob) string {
	if namespace := strings.TrimSpace(job.KubernetesNS); namespace != "" {
		return namespace
	}
	return "tenant-" + sanitizeDNS(job.TenantID)
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
	retiringAttempt := job.ClusterAttempt - 1
	if retiringAttempt < 1 {
		return fmt.Errorf("managed retiring attempt is invalid")
	}
	resource, _, err := r.store.RetireManagedAttemptResource(ctx, domain.ManagedAttemptRetireRequest{
		JobID: job.ID, ClusterAttempt: retiringAttempt, KubernetesNS: managedJobNamespace(*job),
		RayJobName: job.RayJobName, RayJobUID: job.RayJobUID,
	})
	if err != nil {
		return err
	}
	cleaned, err := r.cleanupManagedAttemptResource(ctx, *resource)
	if err != nil || !cleaned {
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
	if job.Spec.TrainingEngine.Resolved() == domain.TrainingEngineRayTrain {
		return r.reconcileManagedCancellation(ctx, job)
	}
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

func (r *Reconciler) reconcileManagedCancellation(ctx context.Context, job *domain.TrainingJob) error {
	name := strings.TrimSpace(job.RayJobName)
	if name == "" {
		name = managedAttemptRayJobName(*job)
	}
	attempt := job.ClusterAttempt
	if managedJobHasRetiringIdentity(*job) && attempt > 1 {
		attempt--
	}
	resource, _, err := r.store.RetireManagedAttemptResource(ctx, domain.ManagedAttemptRetireRequest{
		JobID: job.ID, ClusterAttempt: attempt, KubernetesNS: managedJobNamespace(*job),
		RayJobName: name, RayJobUID: job.RayJobUID,
	})
	if err != nil {
		return err
	}
	cleaned, err := r.cleanupManagedAttemptResource(ctx, *resource)
	if err != nil || !cleaned {
		return err
	}
	observed := domain.ObservedJobState{
		ID: job.ID, State: domain.StateCanceled, KubernetesNS: managedJobNamespace(*job),
		RayJobName: job.RayJobName, RayJobUID: job.RayJobUID,
	}
	setObservedStateFence(&observed, *job)
	return r.store.ApplyObservedState(ctx, observed)
}

func (r *Reconciler) cleanupManagedAttemptResource(ctx context.Context, resource domain.ManagedAttemptResource) (bool, error) {
	if resource.State == domain.ManagedAttemptResourceQuarantined {
		return false, nil
	}
	if resource.State != domain.ManagedAttemptResourceRetiring {
		retiring, _, err := r.store.RetireManagedAttemptResource(ctx, domain.ManagedAttemptRetireRequest{
			JobID: resource.JobID, ClusterAttempt: resource.ClusterAttempt, KubernetesNS: resource.KubernetesNS,
			RayJobName: resource.RayJobName, RayJobUID: resource.RayJobUID,
		})
		if err != nil {
			return false, err
		}
		if retiring == nil {
			return false, fmt.Errorf("managed cleanup retirement returned no resource")
		}
		resource = *retiring
	}
	current, err := r.client.GetRayJob(ctx, resource.KubernetesNS, resource.RayJobName)
	if apierrors.IsNotFound(err) {
		return r.store.CompleteManagedAttemptCleanup(ctx, domain.ManagedAttemptCleanupRequest{
			JobID: resource.JobID, ClusterAttempt: resource.ClusterAttempt,
			RayJobName: resource.RayJobName, RayJobUID: resource.RayJobUID,
		})
	}
	if err != nil {
		return false, err
	}
	if err := verifyManagedAttemptResource(current, resource); err != nil {
		return false, err
	}
	uid := string(current.GetUID())
	if resource.RayJobUID == "" {
		captured, _, err := r.store.RetireManagedAttemptResource(ctx, domain.ManagedAttemptRetireRequest{
			JobID: resource.JobID, ClusterAttempt: resource.ClusterAttempt, KubernetesNS: resource.KubernetesNS,
			RayJobName: resource.RayJobName, RayJobUID: uid,
		})
		if err != nil {
			return false, err
		}
		resource = *captured
	}
	if err := r.client.DeleteRayJob(ctx, resource.KubernetesNS, resource.RayJobName, resource.JobID, uid); err != nil && !apierrors.IsNotFound(err) {
		return false, err
	}
	deadline := r.now().Add(r.cleanupWait)
	for {
		current, err = r.client.GetRayJob(ctx, resource.KubernetesNS, resource.RayJobName)
		if apierrors.IsNotFound(err) {
			return r.store.CompleteManagedAttemptCleanup(ctx, domain.ManagedAttemptCleanupRequest{
				JobID: resource.JobID, ClusterAttempt: resource.ClusterAttempt,
				RayJobName: resource.RayJobName, RayJobUID: resource.RayJobUID,
			})
		}
		if err != nil {
			return false, err
		}
		if string(current.GetUID()) != uid {
			return false, fmt.Errorf("managed RayJob %s was replaced before cleanup completed", resource.RayJobName)
		}
		if !r.now().Before(deadline) {
			return false, nil
		}
		timer := time.NewTimer(r.cleanupPoll)
		select {
		case <-ctx.Done():
			timer.Stop()
			return false, ctx.Err()
		case <-timer.C:
		}
	}
}

func verifyManagedAttemptResource(resource *unstructured.Unstructured, expected domain.ManagedAttemptResource) error {
	if resource.GetLabels()["ray.io/job-id"] != expected.JobID {
		return &managedCleanupVerificationError{message: "refusing to clean foreign RayJob owner"}
	}
	attempt := strconv.Itoa(expected.ClusterAttempt)
	if resource.GetLabels()[managedAttemptIdentityKey] != attempt || resource.GetAnnotations()[managedAttemptIdentityKey] != attempt {
		return &managedCleanupVerificationError{message: "refusing to clean RayJob with unexpected cluster attempt identity"}
	}
	if expected.RayJobUID != "" && string(resource.GetUID()) != expected.RayJobUID {
		return &managedCleanupVerificationError{message: "refusing to clean RayJob with unexpected UID"}
	}
	labelFence := resource.GetLabels()[managedCreationFenceKey]
	annotationFence := resource.GetAnnotations()[managedCreationFenceKey]
	if labelFence == "" && annotationFence == "" && expected.LeaseVersion == 0 {
		return nil
	}
	if labelFence == "" || annotationFence != labelFence {
		return &managedCleanupVerificationError{message: "refusing to clean RayJob with invalid creation fence identity"}
	}
	fence, err := strconv.ParseInt(labelFence, 10, 64)
	maxFence := expected.ResourceFence
	if maxFence == 0 {
		maxFence = expected.LeaseVersion
	}
	if err != nil || fence < 1 || maxFence < 1 || fence > maxFence {
		return &managedCleanupVerificationError{message: "refusing to clean RayJob with unexpected creation fence identity"}
	}
	return nil
}

type managedCleanupVerificationError struct{ message string }

func (err *managedCleanupVerificationError) Error() string { return err.message }
