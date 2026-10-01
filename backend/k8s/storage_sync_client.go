package k8s

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"ray-train-platform-backend/config"
	"ray-train-platform-backend/storagesync"
)

// StorageSyncClient never replaces an attempt or infers fencing from deletion.
// Final request-drain proof is a separately authenticated worker receipt.
type StorageSyncClient struct {
	kubernetes kubernetes.Interface
	config     config.StorageSyncConfig
}

var _ storagesync.JobClient = (*StorageSyncClient)(nil)

func NewStorageSyncClient(client kubernetes.Interface, cfg config.StorageSyncConfig) *StorageSyncClient {
	return &StorageSyncClient{kubernetes: client, config: cfg}
}

func (c *Client) StorageSyncRuntime(cfg config.StorageSyncConfig) *StorageSyncClient {
	if c == nil {
		return NewStorageSyncClient(nil, cfg)
	}
	return NewStorageSyncClient(c.kubernetes, cfg)
}

func (c *StorageSyncClient) Ensure(ctx context.Context, spec storagesync.WorkSpec) (storagesync.Observation, error) {
	if err := c.ready(); err != nil {
		return storagesync.Observation{}, err
	}
	desired, request, err := renderStorageSyncJob(c.config, spec)
	if err != nil {
		return storagesync.Observation{}, err
	}
	jobs := c.kubernetes.BatchV1().Jobs(c.config.Namespace)
	existing, err := jobs.Get(ctx, desired.Name, metav1.GetOptions{})
	if err == nil {
		if err := verifyStorageSyncJob(existing, desired); err != nil {
			return storagesync.Observation{}, err
		}
		return c.observeJob(ctx, existing)
	}
	if !apierrors.IsNotFound(err) {
		return storagesync.Observation{}, fmt.Errorf("get storage sync worker: %w", err)
	}
	claim, err := c.kubernetes.CoreV1().PersistentVolumeClaims(c.config.Namespace).Get(ctx, c.config.WorkClaimName, metav1.GetOptions{})
	if err != nil {
		return storagesync.Observation{}, fmt.Errorf("storage sync checkpoint storage is unavailable: %w", err)
	}
	// A read-only planning Job may be the first consumer of a WFFC claim. Its
	// process cannot start until Kubernetes binds and mounts that claim. Writing
	// transfers and receipt recovery require an already-bound checkpoint volume.
	canBind := claim.Status.Phase == corev1.ClaimPending && (spec.Phase == "BROWSE" || spec.Phase == "PREVIEW" || spec.Phase == "REVALIDATE")
	if claim.DeletionTimestamp != nil || (claim.Status.Phase != corev1.ClaimBound && !canBind) {
		return storagesync.Observation{}, fmt.Errorf("storage sync checkpoint storage is unavailable for this phase")
	}
	if err := c.ensureRequest(ctx, request); err != nil {
		return storagesync.Observation{}, err
	}
	created, createErr := jobs.Create(ctx, desired, metav1.CreateOptions{})
	if createErr != nil {
		// A timeout or AlreadyExists can hide successful creation. Adopt only the
		// exact deterministic attempt; never issue a differently named job.
		created, err = jobs.Get(ctx, desired.Name, metav1.GetOptions{})
		if err != nil {
			return storagesync.Observation{}, fmt.Errorf("create storage sync worker: %w", createErr)
		}
	}
	if err := verifyStorageSyncJob(created, desired); err != nil {
		return storagesync.Observation{}, err
	}
	return c.observeJob(ctx, created)
}

func (c *StorageSyncClient) ensureRequest(ctx context.Context, desired *corev1.Secret) error {
	secrets := c.kubernetes.CoreV1().Secrets(c.config.Namespace)
	_, err := secrets.Create(ctx, desired, metav1.CreateOptions{})
	if err == nil {
		return nil
	}
	existing, getErr := secrets.Get(ctx, desired.Name, metav1.GetOptions{})
	if getErr != nil {
		return fmt.Errorf("create storage sync request: %w", err)
	}
	if existing.Immutable == nil || !*existing.Immutable || existing.Labels[storageSyncRunLabel] != desired.Labels[storageSyncRunLabel] || existing.Labels[storageSyncAttemptLabel] != desired.Labels[storageSyncAttemptLabel] || !bytes.Equal(existing.Data["request.json"], desired.Data["request.json"]) || len(existing.Data["token"]) == 0 {
		return fmt.Errorf("storage sync request name is already owned by another attempt")
	}
	return nil
}

func verifyStorageSyncJob(existing, desired *batchv1.Job) error {
	for _, key := range []string{storageSyncRunLabel, storageSyncAttemptLabel, storageSyncPhaseLabel} {
		if existing.Labels[key] != desired.Labels[key] {
			return fmt.Errorf("storage sync Job name is already owned by another attempt")
		}
	}
	if existing.Annotations[storageSyncSpecAnnotation] != desired.Annotations[storageSyncSpecAnnotation] || len(existing.Spec.Template.Spec.Containers) != 1 || existing.Spec.Template.Spec.Containers[0].Image != desired.Spec.Template.Spec.Containers[0].Image {
		return fmt.Errorf("storage sync attempt configuration does not match its existing Job")
	}
	return nil
}

func (c *StorageSyncClient) Observe(ctx context.Context, runID string, attempt int) (storagesync.Observation, error) {
	if err := c.ready(); err != nil {
		return storagesync.Observation{}, err
	}
	job, err := c.getAttempt(ctx, runID, attempt)
	if apierrors.IsNotFound(err) {
		return storagesync.Observation{}, nil
	}
	if err != nil {
		return storagesync.Observation{}, err
	}
	return c.observeJob(ctx, job)
}

// RecoverReceipt starts a separate read-only reader only after observing the
// original attempt's owned containers terminate. It cannot overwrite the
// checkpoint, obtain write credentials, or replace the original stop evidence.
func (c *StorageSyncClient) RecoverReceipt(ctx context.Context, spec storagesync.WorkSpec) error {
	if err := c.ready(); err != nil {
		return err
	}
	job, err := c.getAttempt(ctx, spec.RunID, spec.Attempt)
	if err != nil {
		return fmt.Errorf("cannot verify original storage sync attempt for receipt recovery: %w", err)
	}
	observed, err := c.observeJob(ctx, job)
	if err != nil {
		return err
	}
	if observed.JobUID == "" || !observed.Terminated {
		return fmt.Errorf("storage sync original executor termination is not confirmed")
	}
	recovery := spec
	recovery.Phase = "RECOVER"
	_, err = c.Ensure(ctx, recovery)
	return err
}

func (c *StorageSyncClient) observeJob(ctx context.Context, job *batchv1.Job) (storagesync.Observation, error) {
	observation := storagesync.Observation{Exists: true, JobUID: string(job.UID)}
	selector := labels.Set(map[string]string{storageSyncRunLabel: job.Labels[storageSyncRunLabel], storageSyncAttemptLabel: job.Labels[storageSyncAttemptLabel]}).String()
	pods, err := c.kubernetes.CoreV1().Pods(job.Namespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return observation, fmt.Errorf("observe storage sync worker containers: %w", err)
	}
	owned, allTerminated := 0, true
	for _, pod := range pods.Items {
		if !storageSyncPodOwnedBy(pod, job) {
			continue
		}
		owned++
		if !storageSyncContainersTerminated(pod) {
			allTerminated = false
			observation.Running = true
		}
	}
	observation.Terminated = owned > 0 && allTerminated
	for _, condition := range job.Status.Conditions {
		if condition.Type == batchv1.JobFailed && condition.Status == corev1.ConditionTrue {
			observation.FailureReason = condition.Reason
		}
	}
	return observation, nil
}

func storageSyncPodOwnedBy(pod corev1.Pod, job *batchv1.Job) bool {
	if job.UID == "" {
		return false
	}
	for _, owner := range pod.OwnerReferences {
		if owner.Kind == "Job" && owner.UID == job.UID && owner.Controller != nil && *owner.Controller {
			return true
		}
	}
	return false
}

func storageSyncContainersTerminated(pod corev1.Pod) bool {
	if pod.Status.Phase != corev1.PodSucceeded && pod.Status.Phase != corev1.PodFailed {
		return false
	}
	if len(pod.Status.ContainerStatuses) == 0 || len(pod.Status.ContainerStatuses) < len(pod.Spec.Containers) {
		return false
	}
	for _, status := range pod.Status.ContainerStatuses {
		if status.State.Terminated == nil {
			return false
		}
	}
	for _, status := range pod.Status.InitContainerStatuses {
		if status.State.Terminated == nil {
			return false
		}
	}
	return true
}

// Stop records intent without deleting a Pod or changing Job parallelism. The
// worker receives the persisted PAUSE/CANCEL action in its authenticated report
// response, drains its SDK calls, reports a receipt, then exits. Unreachable
// workers retain their locks; removing an API object is never stop evidence.
func (c *StorageSyncClient) Stop(ctx context.Context, runID string, attempt int) error {
	if err := c.ready(); err != nil {
		return err
	}
	job, err := c.getAttempt(ctx, runID, attempt)
	if err != nil {
		return fmt.Errorf("cannot verify storage sync worker for stop: %w", err)
	}
	if job.Annotations[storageSyncStopAnnotation] == "true" {
		return nil
	}
	annotations := make(map[string]string, len(job.Annotations)+1)
	for key, value := range job.Annotations {
		annotations[key] = value
	}
	annotations[storageSyncStopAnnotation] = "true"
	patch := []map[string]any{{"op": "test", "path": "/metadata/uid", "value": string(job.UID)}}
	if job.ResourceVersion != "" {
		patch = append(patch, map[string]any{"op": "test", "path": "/metadata/resourceVersion", "value": job.ResourceVersion})
	}
	patch = append(patch, map[string]any{"op": "add", "path": "/metadata/annotations", "value": annotations})
	data, err := json.Marshal(patch)
	if err != nil {
		return fmt.Errorf("encode storage sync stop request: %w", err)
	}
	if _, err := c.kubernetes.BatchV1().Jobs(job.Namespace).Patch(ctx, job.Name, types.JSONPatchType, data, metav1.PatchOptions{}); err != nil {
		return fmt.Errorf("record storage sync stop request: %w", err)
	}
	return nil
}

func (c *StorageSyncClient) getAttempt(ctx context.Context, runID string, attempt int) (*batchv1.Job, error) {
	if runID == "" || attempt < 1 {
		return nil, fmt.Errorf("invalid storage sync attempt identity")
	}
	job, err := c.kubernetes.BatchV1().Jobs(c.config.Namespace).Get(ctx, storageSyncJobName(runID, attempt), metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	if job.Labels[storageSyncRunLabel] != runID || job.Labels[storageSyncAttemptLabel] != strconv.Itoa(attempt) {
		return nil, fmt.Errorf("storage sync Job does not belong to the requested attempt")
	}
	return job, nil
}

func (c *StorageSyncClient) ready() error {
	if c == nil || c.kubernetes == nil {
		return fmt.Errorf("storage sync Kubernetes client is not initialized")
	}
	if !c.config.Enabled {
		return fmt.Errorf("storage sync is disabled")
	}
	return nil
}
