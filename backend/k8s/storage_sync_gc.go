package k8s

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	k8svalidation "k8s.io/apimachinery/pkg/util/validation"
	"ray-train-platform-backend/storagesync"
)

var errStorageSyncGCIdentity = errors.New("storage sync executor identity is not verified")

var _ storagesync.ExecutorGarbageCollector = (*StorageSyncClient)(nil)

// ListExecutors discovers only this platform's terminal executors in explicitly
// configured namespaces. A TTL is deliberately absent until business fencing
// proof has been persisted and ScheduleExecutorGC verifies the live objects.
func (c *StorageSyncClient) ListExecutors(ctx context.Context) ([]storagesync.ExecutorIdentity, error) {
	if err := c.ready(); err != nil {
		return nil, err
	}
	executors := []storagesync.ExecutorIdentity{}
	for _, namespace := range c.storageSyncGCNamespaces() {
		jobs, err := c.storageSyncGCJobs(ctx, namespace)
		if err != nil {
			return nil, err
		}
		if len(jobs) == 0 {
			continue
		}
		pods, err := c.storageSyncGCPods(ctx, namespace)
		if err != nil {
			return nil, err
		}
		for i := range jobs {
			identity, _, err := c.storageSyncGCIdentity(ctx, &jobs[i])
			if errors.Is(err, errStorageSyncGCIdentity) {
				continue
			}
			if err != nil {
				return nil, err
			}
			identity.Terminated = storageSyncGCPodsTerminated(&jobs[i], pods)
			executors = append(executors, identity)
		}
	}
	return executors, nil
}

func (c *StorageSyncClient) storageSyncGCNamespaces() []string {
	result, seen := []string{}, map[string]bool{}
	for _, namespace := range append([]string{c.config.Namespace}, c.config.GCNamespaces...) {
		if namespace != "" && !seen[namespace] {
			result, seen[namespace] = append(result, namespace), true
		}
	}
	return result
}

func (c *StorageSyncClient) storageSyncGCNamespaceAllowed(namespace string) bool {
	for _, allowed := range c.storageSyncGCNamespaces() {
		if namespace == allowed {
			return true
		}
	}
	return false
}

func (c *StorageSyncClient) storageSyncGCJobs(ctx context.Context, namespace string) ([]batchv1.Job, error) {
	selector := labels.Set(map[string]string{"app.kubernetes.io/name": storageSyncContainer, "app.kubernetes.io/managed-by": "ray-train-platform"}).String()
	result, next := []batchv1.Job{}, ""
	for {
		page, err := c.kubernetes.BatchV1().Jobs(namespace).List(ctx, metav1.ListOptions{LabelSelector: selector, Limit: 200, Continue: next})
		if err != nil {
			return nil, fmt.Errorf("list storage sync executors for retention: %w", err)
		}
		for _, job := range page.Items {
			if job.Spec.TTLSecondsAfterFinished == nil && storageSyncGCJobTerminal(&job) {
				result = append(result, job)
			}
		}
		if page.Continue == "" {
			return result, nil
		}
		next = page.Continue
	}
}

func (c *StorageSyncClient) storageSyncGCPods(ctx context.Context, namespace string) ([]corev1.Pod, error) {
	result, next := []corev1.Pod{}, ""
	for {
		// Labels are mutable. Verify every Pod owned by the exact Job UID, even
		// when an operator has changed or removed that Pod's attempt labels.
		page, err := c.kubernetes.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{Limit: 200, Continue: next})
		if err != nil {
			return nil, fmt.Errorf("verify storage sync executor termination for retention: %w", err)
		}
		result = append(result, page.Items...)
		if page.Continue == "" {
			return result, nil
		}
		next = page.Continue
	}
}

func storageSyncGCJobTerminal(job *batchv1.Job) bool {
	if job.DeletionTimestamp != nil || job.Status.Active != 0 {
		return false
	}
	terminal := 0
	for _, condition := range job.Status.Conditions {
		if (condition.Type == batchv1.JobComplete || condition.Type == batchv1.JobFailed) && condition.Status == corev1.ConditionTrue {
			terminal++
		}
	}
	return terminal == 1
}

func storageSyncGCPodsTerminated(job *batchv1.Job, pods []corev1.Pod) bool {
	owned := 0
	for _, pod := range pods {
		for _, owner := range pod.OwnerReferences {
			if owner.UID != job.UID {
				continue
			}
			if owner.APIVersion != "batch/v1" || owner.Kind != "Job" || owner.Name != job.Name || owner.Controller == nil || !*owner.Controller || pod.Namespace != job.Namespace {
				return false
			}
			owned++
			if !storageSyncContainersTerminated(pod) || !storageSyncGCAllContainersExited(pod) {
				return false
			}
		}
	}
	return job.UID != "" && owned > 0
}

func storageSyncGCAllContainersExited(pod corev1.Pod) bool {
	expected := []string{}
	for _, container := range pod.Spec.Containers {
		expected = append(expected, container.Name)
	}
	for _, container := range pod.Spec.InitContainers {
		expected = append(expected, container.Name)
	}
	for _, container := range pod.Spec.EphemeralContainers {
		expected = append(expected, container.Name)
	}
	terminated := map[string]bool{}
	statuses := append([]corev1.ContainerStatus{}, pod.Status.ContainerStatuses...)
	statuses = append(statuses, pod.Status.InitContainerStatuses...)
	statuses = append(statuses, pod.Status.EphemeralContainerStatuses...)
	for _, status := range statuses {
		if status.State.Terminated == nil || terminated[status.Name] {
			return false
		}
		terminated[status.Name] = true
	}
	if len(expected) == 0 || len(expected) != len(terminated) {
		return false
	}
	for _, name := range expected {
		if !terminated[name] {
			return false
		}
	}
	return true
}

func (c *StorageSyncClient) storageSyncGCIdentity(ctx context.Context, job *batchv1.Job) (storagesync.ExecutorIdentity, *corev1.Secret, error) {
	empty := storagesync.ExecutorIdentity{}
	if !c.storageSyncGCNamespaceAllowed(job.Namespace) || job.UID == "" || job.ResourceVersion == "" || !storageSyncGCManaged(job.Labels) {
		return empty, nil, errStorageSyncGCIdentity
	}
	runID, phase := job.Labels[storageSyncRunLabel], job.Labels[storageSyncPhaseLabel]
	attempt, err := strconv.Atoi(job.Labels[storageSyncAttemptLabel])
	if err != nil || attempt < 1 || strconv.Itoa(attempt) != job.Labels[storageSyncAttemptLabel] || runID == "" || runID == "." || runID == ".." || len(k8svalidation.IsValidLabelValue(runID)) != 0 {
		return empty, nil, errStorageSyncGCIdentity
	}
	name := storageSyncJobName(runID, attempt)
	if phase == "RECOVER" {
		name += "-receipt"
	}
	if job.Name != name {
		return empty, nil, errStorageSyncGCIdentity
	}
	secret, err := c.kubernetes.CoreV1().Secrets(job.Namespace).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return empty, nil, errStorageSyncGCIdentity
	}
	if err != nil {
		return empty, nil, fmt.Errorf("verify storage sync request identity for retention: %w", err)
	}
	spec, valid := storageSyncGCRequestIdentity(job, secret)
	if !valid {
		return empty, nil, errStorageSyncGCIdentity
	}
	return storagesync.ExecutorIdentity{Namespace: job.Namespace, Name: name, JobUID: string(job.UID), RunID: runID,
		Attempt: attempt, Generation: spec.Generation, Phase: phase, SubjectKind: spec.SubjectKind, ParentJobUID: spec.RecoveryJobUID}, secret, nil
}

func storageSyncGCManaged(values map[string]string) bool {
	return values["app.kubernetes.io/name"] == storageSyncContainer && values["app.kubernetes.io/managed-by"] == "ray-train-platform"
}

func storageSyncGCRequestIdentity(job *batchv1.Job, secret *corev1.Secret) (storagesync.WorkSpec, bool) {
	var spec storagesync.WorkSpec
	if secret.UID == "" || secret.ResourceVersion == "" || secret.DeletionTimestamp != nil || secret.Namespace != job.Namespace || secret.Name != job.Name || secret.Immutable == nil || !*secret.Immutable || !storageSyncGCManaged(secret.Labels) {
		return spec, false
	}
	for _, key := range []string{storageSyncRunLabel, storageSyncAttemptLabel, storageSyncPhaseLabel} {
		if secret.Labels[key] != job.Labels[key] {
			return spec, false
		}
	}
	payload := secret.Data["request.json"]
	digest := sha256.Sum256(payload)
	encoded := hex.EncodeToString(digest[:])
	if len(payload) == 0 || len(payload) > 900000 || job.Annotations[storageSyncSpecAnnotation] != encoded || secret.Annotations[storageSyncSpecAnnotation] != encoded || json.Unmarshal(payload, &spec) != nil {
		return spec, false
	}
	if spec.RunID != job.Labels[storageSyncRunLabel] || strconv.Itoa(spec.Attempt) != job.Labels[storageSyncAttemptLabel] || spec.Phase != job.Labels[storageSyncPhaseLabel] || spec.Generation < 1 || (spec.SubjectKind != "run" && spec.SubjectKind != "preview") {
		return spec, false
	}
	switch spec.Phase {
	case "BROWSE":
		return spec, spec.SubjectKind == "preview" && spec.RecoveryJobUID == ""
	case "PREVIEW", "REVALIDATE":
		return spec, spec.RecoveryJobUID == ""
	case "TRANSFER":
		return spec, spec.SubjectKind == "run" && spec.RecoveryJobUID == ""
	case "RECOVER":
		return spec, spec.RecoveryJobUID != "" && spec.RecoveryJobUID != string(job.UID)
	default:
		return spec, false
	}
}

// ScheduleExecutorGC attaches only the immutable per-attempt request Secret,
// then adds a TTL to the already terminal Job. Both patches use UID and resource
// version tests; no checkpoint volume, source object or target object is deleted.
func (c *StorageSyncClient) ScheduleExecutorGC(ctx context.Context, request storagesync.ExecutorGCRequest) error {
	if err := c.ready(); err != nil {
		return err
	}
	if !c.storageSyncGCNamespaceAllowed(request.Executor.Namespace) || request.TTLSeconds <= 0 || !storageSyncGCState(request.State) {
		return errStorageSyncGCIdentity
	}
	job, err := c.kubernetes.BatchV1().Jobs(request.Executor.Namespace).Get(ctx, request.Executor.Name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("verify storage sync executor before retention: %w", err)
	}
	identity, secret, err := c.storageSyncGCIdentity(ctx, job)
	if err != nil {
		return err
	}
	expected := request.Executor
	expected.Terminated = false
	if identity != expected || !storageSyncGCJobTerminal(job) {
		return errStorageSyncGCIdentity
	}
	pods, err := c.storageSyncGCPods(ctx, job.Namespace)
	if err != nil {
		return err
	}
	if !storageSyncGCPodsTerminated(job, pods) {
		return fmt.Errorf("storage sync executor termination is not verified for retention")
	}
	if err := c.storageSyncGCAttachRequest(ctx, job, secret); err != nil {
		return err
	}
	if job.Spec.TTLSecondsAfterFinished != nil {
		return nil
	}
	patch := storageSyncGCCAS(job.UID, job.ResourceVersion)
	patch = append(patch, map[string]any{"op": "add", "path": "/spec/ttlSecondsAfterFinished", "value": request.TTLSeconds})
	data, err := json.Marshal(patch)
	if err != nil {
		return err
	}
	_, err = c.kubernetes.BatchV1().Jobs(job.Namespace).Patch(ctx, job.Name, types.JSONPatchType, data, metav1.PatchOptions{})
	if err != nil {
		return fmt.Errorf("schedule storage sync executor retention: %w", err)
	}
	return nil
}

func storageSyncGCState(state string) bool {
	return state == "SUCCEEDED" || state == "FAILED" || state == "CANCELLED" || state == "PAUSED"
}

func (c *StorageSyncClient) storageSyncGCAttachRequest(ctx context.Context, job *batchv1.Job, secret *corev1.Secret) error {
	if len(secret.OwnerReferences) != 0 {
		if len(secret.OwnerReferences) != 1 {
			return errStorageSyncGCIdentity
		}
		owner := secret.OwnerReferences[0]
		if owner.APIVersion != "batch/v1" || owner.Kind != "Job" || owner.Name != job.Name || owner.UID != job.UID {
			return errStorageSyncGCIdentity
		}
		return nil
	}
	owner := metav1.OwnerReference{APIVersion: "batch/v1", Kind: "Job", Name: job.Name, UID: job.UID, Controller: pointerTo(true), BlockOwnerDeletion: pointerTo(false)}
	patch := storageSyncGCCAS(secret.UID, secret.ResourceVersion)
	patch = append(patch, map[string]any{"op": "add", "path": "/metadata/ownerReferences", "value": []metav1.OwnerReference{owner}})
	data, err := json.Marshal(patch)
	if err != nil {
		return err
	}
	_, err = c.kubernetes.CoreV1().Secrets(secret.Namespace).Patch(ctx, secret.Name, types.JSONPatchType, data, metav1.PatchOptions{})
	if err != nil {
		return fmt.Errorf("attach storage sync request to its executor: %w", err)
	}
	return nil
}

func storageSyncGCCAS(uid types.UID, resourceVersion string) []map[string]any {
	return []map[string]any{
		{"op": "test", "path": "/metadata/uid", "value": string(uid)},
		{"op": "test", "path": "/metadata/resourceVersion", "value": resourceVersion},
	}
}
