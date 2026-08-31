package k8s

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"path"
	"reflect"
	"regexp"
	"sort"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	corev1client "k8s.io/client-go/kubernetes/typed/core/v1"
	"ray-train-platform-backend/datasetpublisher"
	"ray-train-platform-backend/domain"
)

const (
	datasetPublisherContainerName  = "dataset-publisher"
	publicationWorkVolumeName      = "work"
	publicationTmpVolumeName       = "tmp"
	publicationIRSATokenVolumeName = "vke-irsa-token"
	publicationIRSATokenMountPath  = "/var/run/secrets/vke.volcengine.com/irsa-tokens"
	publicationIRSATokenFilePath   = publicationIRSATokenMountPath + "/token"
	publicationIRSAAudience        = "sts.volcengine.com"
	publicationIRSATokenTTLSeconds = int64(3600)
	publicationContainerUserID     = int64(65532)
	publicationResultMaxBytes      = 4096

	publicationManagedByLabel       = "app.kubernetes.io/managed-by"
	publicationNameLabel            = "app.kubernetes.io/name"
	publicationQueueLabel           = "kueue.x-k8s.io/queue-name"
	publicationJobNameLabel         = "platform.wellspiking.ai/publication-job-name"
	publicationRunHashLabel         = "platform.wellspiking.ai/publication-run-hash"
	publicationDatasetHashLabel     = "platform.wellspiking.ai/dataset-hash"
	publicationVersionIDHashLabel   = "platform.wellspiking.ai/dataset-version-hash"
	publicationVersionHashLabel     = "platform.wellspiking.ai/version-hash"
	publicationRunIDAnnotation      = "platform.wellspiking.ai/publication-run-id"
	publicationDatasetIDAnnotation  = "platform.wellspiking.ai/dataset-id"
	publicationVersionIDAnnotation  = "platform.wellspiking.ai/dataset-version-id"
	publicationVersionAnnotation    = "platform.wellspiking.ai/dataset-version"
	publicationSchemaAnnotation     = "platform.wellspiking.ai/schema-version"
	publicationSpecHashAnnotation   = "platform.wellspiking.ai/publication-spec-sha256"
	publicationManagedByValue       = "ray-train-platform"
	publicationNameValue            = "dataset-publisher"
	publicationIdentityHashByteSize = 16
	publicationKubernetesJobLabel   = "batch.kubernetes.io/job-name"
)

// publicationJobSpec keeps the renderer testable without exposing mutable
// fields from datasetpublisher.PublicationJobSpec. The production adapter below
// only forwards its read-only getters.
type publicationJobSpec interface {
	Namespace() string
	Name() string
	RunID() string
	DatasetID() string
	DatasetVersionID() string
	Version() string
	SchemaVersion() string
	SourceRoot() string
	SourceIndex() string
	Image() string
	SourceBucket() string
	TargetBucket() string
	TOSEndpoint() string
	TOSRegion() string
	ImagePullPolicy() string
	ServiceAccountName() string
	IRSARoleTRN() string
	CredentialSecretName() string
	QueueName() string
	PriorityClassName() string
	WorkingDirectory() string
	InternalPrefix() string
	NodeSelector() map[string]string
	PreferredNodeSelector() map[string]string
	Tolerations() []datasetpublisher.PublicationToleration
	CPURequest() string
	CPULimit() string
	MemoryRequest() string
	MemoryLimit() string
	BackoffLimit() int
	ActiveDeadline() time.Duration
	TTLAfterFinished() time.Duration
	Labels() map[string]string
}

type datasetPublicationJobSpec struct {
	datasetpublisher.PublicationJobSpec
}

func (view datasetPublicationJobSpec) CPURequest() string {
	return view.Resources().Requests()[string(corev1.ResourceCPU)]
}
func (view datasetPublicationJobSpec) CPULimit() string {
	return view.Resources().Limits()[string(corev1.ResourceCPU)]
}
func (view datasetPublicationJobSpec) MemoryRequest() string {
	return view.Resources().Requests()[string(corev1.ResourceMemory)]
}
func (view datasetPublicationJobSpec) MemoryLimit() string {
	return view.Resources().Limits()[string(corev1.ResourceMemory)]
}

func (c *Client) EnsurePublicationJob(ctx context.Context, spec datasetpublisher.PublicationJobSpec) (datasetpublisher.PublicationJobStatus, error) {
	if c == nil || isNilPublicationInterface(c.kubernetes) {
		return datasetpublisher.PublicationJobStatus{}, errors.New("dataset publication Kubernetes client is not initialized")
	}
	return c.ensureDatasetPublicationJob(ctx, datasetPublicationJobSpec{PublicationJobSpec: spec})
}

func (c *Client) ensureDatasetPublicationJob(ctx context.Context, spec publicationJobSpec) (datasetpublisher.PublicationJobStatus, error) {
	if ctx == nil {
		return datasetpublisher.PublicationJobStatus{}, errors.New("dataset publication context is required")
	}
	if c == nil || isNilPublicationInterface(c.kubernetes) {
		return datasetpublisher.PublicationJobStatus{}, errors.New("dataset publication Kubernetes client is not initialized")
	}
	batchClient := c.kubernetes.BatchV1()
	coreClient := c.kubernetes.CoreV1()
	if isNilPublicationInterface(batchClient) || isNilPublicationInterface(coreClient) {
		return datasetpublisher.PublicationJobStatus{}, errors.New("dataset publication Kubernetes interfaces are not initialized")
	}
	desired, err := renderDatasetPublicationJob(spec)
	if err != nil {
		return datasetpublisher.PublicationJobStatus{}, err
	}
	jobs := batchClient.Jobs(desired.Namespace)
	if isNilPublicationInterface(jobs) {
		return datasetpublisher.PublicationJobStatus{}, errors.New("dataset publication Job interface is not initialized")
	}

	existing, err := jobs.Get(ctx, desired.Name, metav1.GetOptions{})
	if err == nil {
		if ownershipErr := verifyDatasetPublicationJobOwnership(existing, desired); ownershipErr != nil {
			return datasetpublisher.PublicationJobStatus{}, ownershipErr
		}
		return readDatasetPublicationJobStatus(ctx, coreClient, existing, spec)
	}
	if !apierrors.IsNotFound(err) {
		return datasetpublisher.PublicationJobStatus{}, cleanPublicationKubernetesError(ctx, "get dataset publication Job")
	}

	created, err := jobs.Create(ctx, desired, metav1.CreateOptions{})
	if err == nil {
		if created == nil {
			return datasetpublisher.PublicationJobStatus{}, errors.New("create dataset publication Job failed")
		}
		return readDatasetPublicationJobStatus(ctx, coreClient, created, spec)
	}
	if !apierrors.IsAlreadyExists(err) {
		return datasetpublisher.PublicationJobStatus{}, cleanPublicationKubernetesError(ctx, "create dataset publication Job")
	}

	existing, err = jobs.Get(ctx, desired.Name, metav1.GetOptions{})
	if err != nil || existing == nil {
		return datasetpublisher.PublicationJobStatus{}, cleanPublicationKubernetesError(ctx, "get concurrently created dataset publication Job")
	}
	if ownershipErr := verifyDatasetPublicationJobOwnership(existing, desired); ownershipErr != nil {
		return datasetpublisher.PublicationJobStatus{}, ownershipErr
	}
	return readDatasetPublicationJobStatus(ctx, coreClient, existing, spec)
}

func verifyDatasetPublicationJobOwnership(existing, desired *batchv1.Job) error {
	if existing == nil || desired == nil || existing.Namespace != desired.Namespace || existing.Name != desired.Name {
		return errors.New("existing dataset publication Job is not owned by this publication")
	}
	labelKeys := []string{
		publicationManagedByLabel,
		publicationNameLabel,
		publicationQueueLabel,
		publicationJobNameLabel,
		publicationRunHashLabel,
		publicationDatasetHashLabel,
		publicationVersionIDHashLabel,
		publicationVersionHashLabel,
	}
	annotationKeys := []string{
		publicationRunIDAnnotation,
		publicationDatasetIDAnnotation,
		publicationVersionIDAnnotation,
		publicationVersionAnnotation,
		publicationSchemaAnnotation,
		publicationSpecHashAnnotation,
	}
	if !publicationMetadataMatches(existing.Labels, desired.Labels, labelKeys) ||
		!publicationMetadataMatches(existing.Annotations, desired.Annotations, annotationKeys) ||
		!publicationMetadataMatches(existing.Spec.Template.Labels, desired.Spec.Template.Labels, labelKeys) ||
		!publicationMetadataMatches(existing.Spec.Template.Annotations, desired.Spec.Template.Annotations, annotationKeys) {
		return errors.New("existing dataset publication Job is not owned by this publication")
	}
	return nil
}

func publicationMetadataMatches(existing, desired map[string]string, keys []string) bool {
	for _, key := range keys {
		if desired[key] == "" || existing[key] != desired[key] {
			return false
		}
	}
	return true
}

func readDatasetPublicationJobStatus(
	ctx context.Context,
	coreClient corev1client.CoreV1Interface,
	job *batchv1.Job,
	spec publicationJobSpec,
) (datasetpublisher.PublicationJobStatus, error) {
	if job == nil {
		return datasetpublisher.PublicationJobStatus{}, errors.New("dataset publication Job status is unavailable")
	}
	failed := false
	complete := false
	for _, condition := range job.Status.Conditions {
		if condition.Status != corev1.ConditionTrue {
			continue
		}
		switch condition.Type {
		case batchv1.JobFailed:
			failed = true
		case batchv1.JobComplete:
			complete = true
		}
	}
	if failed {
		return datasetpublisher.PublicationJobStatus{Phase: datasetpublisher.PublicationJobFailed}, nil
	}
	if complete {
		return readCompletedDatasetPublicationJob(ctx, coreClient, job, spec)
	}
	if job.Status.Active > 0 {
		return datasetpublisher.PublicationJobStatus{Phase: datasetpublisher.PublicationJobPacking}, nil
	}
	return datasetpublisher.PublicationJobStatus{Phase: datasetpublisher.PublicationJobPending}, nil
}

func readCompletedDatasetPublicationJob(
	ctx context.Context,
	coreClient corev1client.CoreV1Interface,
	job *batchv1.Job,
	spec publicationJobSpec,
) (datasetpublisher.PublicationJobStatus, error) {
	status := datasetpublisher.PublicationJobStatus{Phase: datasetpublisher.PublicationJobSucceeded}
	if isNilPublicationInterface(coreClient) {
		return datasetpublisher.PublicationJobStatus{}, errors.New("dataset publication Pod interface is not initialized")
	}
	pods := coreClient.Pods(job.Namespace)
	if isNilPublicationInterface(pods) {
		return datasetpublisher.PublicationJobStatus{}, errors.New("dataset publication Pod interface is not initialized")
	}
	selector := labels.Set{
		publicationKubernetesJobLabel: job.Name,
		publicationRunHashLabel:       job.Labels[publicationRunHashLabel],
	}.AsSelector().String()
	listed, err := pods.List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil || listed == nil {
		return datasetpublisher.PublicationJobStatus{}, cleanPublicationKubernetesError(ctx, "list dataset publication Pods")
	}
	for index := range listed.Items {
		pod := &listed.Items[index]
		if pod.Labels[publicationKubernetesJobLabel] != job.Name ||
			pod.Labels[publicationRunHashLabel] != job.Labels[publicationRunHashLabel] ||
			!datasetPublicationPodOwnedByJob(pod, job) {
			continue
		}
		for _, container := range pod.Status.ContainerStatuses {
			terminated := container.State.Terminated
			if container.Name != datasetPublisherContainerName || terminated == nil || terminated.ExitCode != 0 || terminated.Message == "" {
				continue
			}
			parsed, err := parseDatasetPublicationResult(terminated.Message, spec)
			if err != nil {
				return datasetpublisher.PublicationJobStatus{}, err
			}
			return parsed, nil
		}
	}
	return status, nil
}

func datasetPublicationPodOwnedByJob(pod *corev1.Pod, job *batchv1.Job) bool {
	if pod == nil || job == nil {
		return false
	}
	for _, owner := range pod.OwnerReferences {
		if owner.APIVersion != batchv1.SchemeGroupVersion.String() || owner.Kind != "Job" || owner.Name != job.Name ||
			owner.Controller == nil || !*owner.Controller {
			continue
		}
		if job.UID != "" && owner.UID != job.UID {
			continue
		}
		return true
	}
	return false
}

type publicationResultPayload struct {
	Progress publicationProgressPayload `json:"progress"`
	Receipt  *publicationReceiptPayload `json:"receipt"`
}

type publicationProgressPayload struct {
	TotalPartitions      int64 `json:"total_partitions"`
	CompletedPartitions  int64 `json:"completed_partitions"`
	FailedPartitions     int64 `json:"failed_partitions"`
	SourceObjectCount    int64 `json:"source_object_count"`
	ProcessedObjectCount int64 `json:"processed_object_count"`
	FailedObjectCount    int64 `json:"failed_object_count"`
}

type publicationReceiptPayload struct {
	DatasetID         string `json:"dataset_id"`
	DatasetVersionID  string `json:"dataset_version_id"`
	Version           string `json:"version"`
	ManifestSHA256    string `json:"manifest_sha256"`
	ManifestObjectKey string `json:"manifest_object_key"`
	SchemaVersion     string `json:"schema_version"`
	TrainSamples      int64  `json:"train_samples"`
	ValSamples        int64  `json:"val_samples"`
	TestSamples       int64  `json:"test_samples"`
	SourceObjectCount int64  `json:"source_object_count"`
	LogicalBytes      int64  `json:"logical_bytes"`
	PackedBytes       int64  `json:"packed_bytes"`
}

func parseDatasetPublicationResult(message string, spec publicationJobSpec) (datasetpublisher.PublicationJobStatus, error) {
	if len(message) > publicationResultMaxBytes {
		return datasetpublisher.PublicationJobStatus{}, errors.New("dataset publication result exceeds the allowed size")
	}
	trimmed := bytes.TrimSpace([]byte(message))
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return datasetpublisher.PublicationJobStatus{}, errors.New("dataset publication result is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.DisallowUnknownFields()
	var payload publicationResultPayload
	if err := decoder.Decode(&payload); err != nil {
		return datasetpublisher.PublicationJobStatus{}, errors.New("dataset publication result is invalid")
	}
	if err := ensurePublicationJSONEnd(decoder); err != nil {
		return datasetpublisher.PublicationJobStatus{}, errors.New("dataset publication result is invalid")
	}
	progress := datasetpublisher.PublicationProgress{
		TotalPartitions: payload.Progress.TotalPartitions, CompletedPartitions: payload.Progress.CompletedPartitions,
		FailedPartitions: payload.Progress.FailedPartitions, SourceObjectCount: payload.Progress.SourceObjectCount,
		ProcessedObjectCount: payload.Progress.ProcessedObjectCount, FailedObjectCount: payload.Progress.FailedObjectCount,
	}
	if !validPublicationProgress(progress) {
		return datasetpublisher.PublicationJobStatus{}, errors.New("dataset publication result is invalid")
	}
	status := datasetpublisher.PublicationJobStatus{Phase: datasetpublisher.PublicationJobSucceeded, Progress: progress}
	if payload.Receipt == nil {
		return status, nil
	}
	receipt := domain.DatasetPublicationReceipt{
		DatasetID: payload.Receipt.DatasetID, DatasetVersionID: payload.Receipt.DatasetVersionID,
		Version: payload.Receipt.Version, ManifestSHA256: payload.Receipt.ManifestSHA256,
		ManifestObjectKey: payload.Receipt.ManifestObjectKey, SchemaVersion: payload.Receipt.SchemaVersion,
		TrainSamples: payload.Receipt.TrainSamples, ValSamples: payload.Receipt.ValSamples, TestSamples: payload.Receipt.TestSamples,
		SourceObjectCount: payload.Receipt.SourceObjectCount, LogicalBytes: payload.Receipt.LogicalBytes, PackedBytes: payload.Receipt.PackedBytes,
	}
	if receipt.DatasetID != spec.DatasetID() || receipt.DatasetVersionID != spec.DatasetVersionID() ||
		receipt.Version != spec.Version() || receipt.SchemaVersion != spec.SchemaVersion() ||
		receipt.ValidateWithInternalPrefix(spec.InternalPrefix()) != nil {
		return datasetpublisher.PublicationJobStatus{}, errors.New("dataset publication receipt is invalid")
	}
	status.Receipt = &receipt
	return status, nil
}

func ensurePublicationJSONEnd(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return errors.New("trailing dataset publication result")
	}
	return nil
}

func validPublicationProgress(progress datasetpublisher.PublicationProgress) bool {
	return progress.TotalPartitions >= 0 && progress.CompletedPartitions >= 0 && progress.FailedPartitions >= 0 &&
		progress.SourceObjectCount >= 0 && progress.ProcessedObjectCount >= 0 && progress.FailedObjectCount >= 0 &&
		progress.CompletedPartitions <= progress.TotalPartitions && progress.ProcessedObjectCount <= progress.SourceObjectCount
}

func cleanPublicationKubernetesError(ctx context.Context, operation string) error {
	if ctx != nil && ctx.Err() != nil {
		return fmt.Errorf("%s: %w", operation, ctx.Err())
	}
	return errors.New(operation + " failed")
}

func renderDatasetPublicationJob(spec publicationJobSpec) (*batchv1.Job, error) {
	if isNilPublicationJobSpec(spec) || !validPublicationRenderIdentity(spec) {
		return nil, errors.New("invalid dataset publication Job identity")
	}
	if !validPublicationIRSARoleTRN(spec.IRSARoleTRN()) {
		return nil, errors.New("invalid dataset publication IRSA role TRN")
	}
	if spec.CredentialSecretName() != "" && !isDNSSubdomain(spec.CredentialSecretName()) {
		return nil, errors.New("invalid dataset publication credential Secret name")
	}
	if spec.CredentialSecretName() != "" && spec.IRSARoleTRN() != "" {
		return nil, errors.New("dataset publication credentials must use either IRSA or a Secret")
	}
	if domain.ValidatePinnedImage(spec.Image()) != nil {
		return nil, errors.New("dataset publication image must be pinned by digest")
	}
	pullPolicy, ok := publicationImagePullPolicy(spec.ImagePullPolicy())
	if !ok {
		return nil, errors.New("invalid dataset publication image pull policy")
	}
	resources, err := publicationResourceRequirements(spec)
	if err != nil {
		return nil, err
	}
	tolerations, err := publicationTolerations(spec.Tolerations())
	if err != nil {
		return nil, err
	}
	backoff, activeDeadline, ttl, err := publicationLifecycle(spec)
	if err != nil {
		return nil, err
	}

	labels := publicationBaseLabels(spec)
	specHash, err := publicationCanonicalSpecHash(spec, labels)
	if err != nil {
		return nil, errors.New("hash dataset publication Job specification")
	}
	labels[publicationRunHashLabel] = boundedPublicationIdentityHash(spec.RunID())
	labels[publicationDatasetHashLabel] = boundedPublicationIdentityHash(spec.DatasetID())
	labels[publicationVersionIDHashLabel] = boundedPublicationIdentityHash(spec.DatasetVersionID())
	labels[publicationVersionHashLabel] = boundedPublicationIdentityHash(spec.Version())
	annotations := publicationIdentityAnnotations(spec, specHash)

	trueValue := true
	falseValue := false
	userID := publicationContainerUserID
	args := []string{
		"--run-id", spec.RunID(),
		"--dataset-id", spec.DatasetID(),
		"--dataset-version-id", spec.DatasetVersionID(),
		"--version", spec.Version(),
		"--schema-version", spec.SchemaVersion(),
		"--source-bucket", spec.SourceBucket(),
		"--target-bucket", spec.TargetBucket(),
		"--tos-endpoint", spec.TOSEndpoint(),
		"--tos-region", spec.TOSRegion(),
		"--source-root", spec.SourceRoot(),
		"--source-index", spec.SourceIndex(),
		"--internal-prefix", spec.InternalPrefix(),
		"--output-dir", spec.WorkingDirectory(),
	}
	container := corev1.Container{
		Name:            datasetPublisherContainerName,
		Image:           spec.Image(),
		ImagePullPolicy: pullPolicy,
		Command:         []string{"python3", "-m", "raytrain_publisher.cloud_publish"},
		Args:            args,
		WorkingDir:      spec.WorkingDirectory(),
		Resources:       resources,
		SecurityContext: &corev1.SecurityContext{
			RunAsUser:                &userID,
			RunAsGroup:               &userID,
			RunAsNonRoot:             &trueValue,
			ReadOnlyRootFilesystem:   &trueValue,
			AllowPrivilegeEscalation: &falseValue,
			Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
			SeccompProfile:           &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
		},
		VolumeMounts: []corev1.VolumeMount{
			{Name: publicationWorkVolumeName, MountPath: spec.WorkingDirectory()},
			{Name: publicationTmpVolumeName, MountPath: "/tmp"},
		},
	}
	volumes := []corev1.Volume{
		{Name: publicationWorkVolumeName, VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
		{Name: publicationTmpVolumeName, VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
	}
	if spec.IRSARoleTRN() != "" {
		expirationSeconds := publicationIRSATokenTTLSeconds
		container.Env = []corev1.EnvVar{
			{Name: "VOLCENGINE_OIDC_ROLE_TRN", Value: spec.IRSARoleTRN()},
			{Name: "VOLCENGINE_OIDC_TOKEN_FILE", Value: publicationIRSATokenFilePath},
		}
		container.VolumeMounts = append(container.VolumeMounts, corev1.VolumeMount{
			Name: publicationIRSATokenVolumeName, MountPath: publicationIRSATokenMountPath, ReadOnly: true,
		})
		volumes = append(volumes, corev1.Volume{
			Name: publicationIRSATokenVolumeName,
			VolumeSource: corev1.VolumeSource{Projected: &corev1.ProjectedVolumeSource{
				Sources: []corev1.VolumeProjection{{ServiceAccountToken: &corev1.ServiceAccountTokenProjection{
					Audience: publicationIRSAAudience, ExpirationSeconds: &expirationSeconds, Path: "token",
				}}},
			}},
		})
	}
	if spec.CredentialSecretName() != "" {
		for name, key := range map[string]string{"TOS_ACCESS_KEY": "access-key", "TOS_SECRET_KEY": "secret-key"} {
			container.Env = append(container.Env, corev1.EnvVar{
				Name: name,
				ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: spec.CredentialSecretName()},
					Key:                  key,
				}},
			})
		}
		sort.Slice(container.Env, func(i, j int) bool { return container.Env[i].Name < container.Env[j].Name })
	}
	podSpec := corev1.PodSpec{
		AutomountServiceAccountToken: &falseValue,
		ServiceAccountName:           spec.ServiceAccountName(),
		PriorityClassName:            spec.PriorityClassName(),
		RestartPolicy:                corev1.RestartPolicyNever,
		SecurityContext: &corev1.PodSecurityContext{
			RunAsUser:      &userID,
			RunAsGroup:     &userID,
			RunAsNonRoot:   &trueValue,
			SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
		},
		NodeSelector: clonePublicationJobStringMap(spec.NodeSelector()),
		Affinity:     publicationPreferredNodeAffinity(spec.PreferredNodeSelector()),
		Tolerations:  tolerations,
		Containers:   []corev1.Container{container},
		Volumes:      volumes,
	}

	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:   spec.Namespace(),
			Name:        spec.Name(),
			Labels:      clonePublicationJobStringMap(labels),
			Annotations: clonePublicationJobStringMap(annotations),
		},
		Spec: batchv1.JobSpec{
			BackoffLimit:            &backoff,
			ActiveDeadlineSeconds:   &activeDeadline,
			TTLSecondsAfterFinished: &ttl,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels:      clonePublicationJobStringMap(labels),
					Annotations: clonePublicationJobStringMap(annotations),
				},
				Spec: podSpec,
			},
		},
	}, nil
}

var publicationIRSARoleTRNPattern = regexp.MustCompile(`^trn:iam::[0-9]+:role/[A-Za-z0-9+=,.@_/-]+$`)

func validPublicationIRSARoleTRN(value string) bool {
	return value == "" || publicationIRSARoleTRNPattern.MatchString(value)
}

func validPublicationRenderIdentity(spec publicationJobSpec) bool {
	values := []string{
		spec.Namespace(), spec.Name(), spec.RunID(), spec.DatasetID(), spec.DatasetVersionID(),
		spec.Version(), spec.SchemaVersion(), spec.SourceRoot(), spec.SourceIndex(),
		spec.SourceBucket(), spec.TargetBucket(), spec.TOSEndpoint(), spec.TOSRegion(),
		spec.ServiceAccountName(), spec.QueueName(), spec.PriorityClassName(), spec.InternalPrefix(),
	}
	for _, value := range values {
		if value == "" {
			return false
		}
	}
	workingDirectory := spec.WorkingDirectory()
	return workingDirectory != "" && workingDirectory != "/" && workingDirectory != "/tmp" &&
		path.IsAbs(workingDirectory) && path.Clean(workingDirectory) == workingDirectory
}

func publicationImagePullPolicy(value string) (corev1.PullPolicy, bool) {
	switch corev1.PullPolicy(value) {
	case corev1.PullAlways, corev1.PullIfNotPresent, corev1.PullNever:
		return corev1.PullPolicy(value), true
	default:
		return "", false
	}
}

func publicationResourceRequirements(spec publicationJobSpec) (corev1.ResourceRequirements, error) {
	values := []struct {
		name  corev1.ResourceName
		value string
		list  string
	}{
		{name: corev1.ResourceCPU, value: spec.CPURequest(), list: "request"},
		{name: corev1.ResourceMemory, value: spec.MemoryRequest(), list: "request"},
		{name: corev1.ResourceCPU, value: spec.CPULimit(), list: "limit"},
		{name: corev1.ResourceMemory, value: spec.MemoryLimit(), list: "limit"},
	}
	result := corev1.ResourceRequirements{Requests: corev1.ResourceList{}, Limits: corev1.ResourceList{}}
	for _, item := range values {
		quantity, err := resource.ParseQuantity(item.value)
		if err != nil || quantity.Sign() <= 0 {
			return corev1.ResourceRequirements{}, errors.New("invalid dataset publication CPU or memory resource")
		}
		if item.list == "request" {
			result.Requests[item.name] = quantity
		} else {
			result.Limits[item.name] = quantity
		}
	}
	cpuRequest, cpuLimit := result.Requests[corev1.ResourceCPU], result.Limits[corev1.ResourceCPU]
	memoryRequest, memoryLimit := result.Requests[corev1.ResourceMemory], result.Limits[corev1.ResourceMemory]
	if cpuRequest.Cmp(cpuLimit) > 0 || memoryRequest.Cmp(memoryLimit) > 0 {
		return corev1.ResourceRequirements{}, errors.New("dataset publication resource request exceeds its limit")
	}
	return result, nil
}

func publicationLifecycle(spec publicationJobSpec) (int32, int64, int32, error) {
	if spec.BackoffLimit() < 0 || int64(spec.BackoffLimit()) > math.MaxInt32 ||
		spec.ActiveDeadline() <= 0 || spec.ActiveDeadline()%time.Second != 0 ||
		spec.TTLAfterFinished() <= 0 || spec.TTLAfterFinished()%time.Second != 0 {
		return 0, 0, 0, errors.New("invalid dataset publication Job lifecycle")
	}
	activeSeconds := spec.ActiveDeadline() / time.Second
	ttlSeconds := spec.TTLAfterFinished() / time.Second
	if activeSeconds > time.Duration(math.MaxInt64) || ttlSeconds > time.Duration(math.MaxInt32) {
		return 0, 0, 0, errors.New("invalid dataset publication Job lifecycle")
	}
	return int32(spec.BackoffLimit()), int64(activeSeconds), int32(ttlSeconds), nil
}

func publicationTolerations(source []datasetpublisher.PublicationToleration) ([]corev1.Toleration, error) {
	result := make([]corev1.Toleration, 0, len(source))
	for _, value := range source {
		operator := corev1.TolerationOperator(value.Operator)
		if operator != corev1.TolerationOpEqual && operator != corev1.TolerationOpExists {
			return nil, errors.New("invalid dataset publication toleration")
		}
		if operator == corev1.TolerationOpExists && value.Value != "" {
			return nil, errors.New("invalid dataset publication toleration")
		}
		effect := corev1.TaintEffect(value.Effect)
		if effect != "" && effect != corev1.TaintEffectNoSchedule && effect != corev1.TaintEffectPreferNoSchedule && effect != corev1.TaintEffectNoExecute {
			return nil, errors.New("invalid dataset publication toleration")
		}
		converted := corev1.Toleration{
			Key: value.Key, Operator: operator, Value: value.Value, Effect: effect,
		}
		if value.HasSeconds {
			if effect != corev1.TaintEffectNoExecute || value.Seconds < 0 {
				return nil, errors.New("invalid dataset publication toleration")
			}
			seconds := value.Seconds
			converted.TolerationSeconds = &seconds
		}
		result = append(result, converted)
	}
	return result, nil
}

func publicationPreferredNodeAffinity(selector map[string]string) *corev1.Affinity {
	if len(selector) == 0 {
		return nil
	}
	keys := make([]string, 0, len(selector))
	for key := range selector {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	expressions := make([]corev1.NodeSelectorRequirement, 0, len(keys))
	for _, key := range keys {
		expressions = append(expressions, corev1.NodeSelectorRequirement{
			Key: key, Operator: corev1.NodeSelectorOpIn, Values: []string{selector[key]},
		})
	}
	return &corev1.Affinity{NodeAffinity: &corev1.NodeAffinity{
		PreferredDuringSchedulingIgnoredDuringExecution: []corev1.PreferredSchedulingTerm{{
			Weight:     100,
			Preference: corev1.NodeSelectorTerm{MatchExpressions: expressions},
		}},
	}}
}

func publicationBaseLabels(spec publicationJobSpec) map[string]string {
	labels := clonePublicationJobStringMap(spec.Labels())
	labels[publicationManagedByLabel] = publicationManagedByValue
	labels[publicationNameLabel] = publicationNameValue
	labels[publicationQueueLabel] = spec.QueueName()
	labels[publicationJobNameLabel] = spec.Name()
	return labels
}

func publicationIdentityAnnotations(spec publicationJobSpec, specHash string) map[string]string {
	return map[string]string{
		publicationRunIDAnnotation:     spec.RunID(),
		publicationDatasetIDAnnotation: spec.DatasetID(),
		publicationVersionIDAnnotation: spec.DatasetVersionID(),
		publicationVersionAnnotation:   spec.Version(),
		publicationSchemaAnnotation:    spec.SchemaVersion(),
		publicationSpecHashAnnotation:  specHash,
	}
}

type canonicalPublicationJobSpec struct {
	Namespace             string                                   `json:"namespace"`
	Name                  string                                   `json:"name"`
	RunID                 string                                   `json:"run_id"`
	DatasetID             string                                   `json:"dataset_id"`
	DatasetVersionID      string                                   `json:"dataset_version_id"`
	Version               string                                   `json:"version"`
	SchemaVersion         string                                   `json:"schema_version"`
	SourceRoot            string                                   `json:"source_root"`
	SourceIndex           string                                   `json:"source_index"`
	Image                 string                                   `json:"image"`
	SourceBucket          string                                   `json:"source_bucket"`
	TargetBucket          string                                   `json:"target_bucket"`
	TOSEndpoint           string                                   `json:"tos_endpoint"`
	TOSRegion             string                                   `json:"tos_region"`
	ImagePullPolicy       string                                   `json:"image_pull_policy"`
	ServiceAccountName    string                                   `json:"service_account_name"`
	IRSARoleTRN           string                                   `json:"irsa_role_trn,omitempty"`
	CredentialSecretName  string                                   `json:"credential_secret_name,omitempty"`
	QueueName             string                                   `json:"queue_name"`
	PriorityClassName     string                                   `json:"priority_class_name"`
	WorkingDirectory      string                                   `json:"working_directory"`
	InternalPrefix        string                                   `json:"internal_prefix"`
	NodeSelector          map[string]string                        `json:"node_selector"`
	PreferredNodeSelector map[string]string                        `json:"preferred_node_selector"`
	Tolerations           []datasetpublisher.PublicationToleration `json:"tolerations"`
	CPURequest            string                                   `json:"cpu_request"`
	CPULimit              string                                   `json:"cpu_limit"`
	MemoryRequest         string                                   `json:"memory_request"`
	MemoryLimit           string                                   `json:"memory_limit"`
	BackoffLimit          int                                      `json:"backoff_limit"`
	ActiveDeadlineSeconds int64                                    `json:"active_deadline_seconds"`
	TTLAfterFinished      int64                                    `json:"ttl_after_finished_seconds"`
	Labels                map[string]string                        `json:"labels"`
}

func publicationCanonicalSpecHash(spec publicationJobSpec, labels map[string]string) (string, error) {
	canonical := canonicalPublicationJobSpec{
		Namespace: spec.Namespace(), Name: spec.Name(), RunID: spec.RunID(), DatasetID: spec.DatasetID(),
		DatasetVersionID: spec.DatasetVersionID(), Version: spec.Version(), SchemaVersion: spec.SchemaVersion(),
		SourceRoot: spec.SourceRoot(), SourceIndex: spec.SourceIndex(), Image: spec.Image(),
		SourceBucket: spec.SourceBucket(), TargetBucket: spec.TargetBucket(), TOSEndpoint: spec.TOSEndpoint(), TOSRegion: spec.TOSRegion(),
		ImagePullPolicy: spec.ImagePullPolicy(), ServiceAccountName: spec.ServiceAccountName(), IRSARoleTRN: spec.IRSARoleTRN(), CredentialSecretName: spec.CredentialSecretName(), QueueName: spec.QueueName(),
		PriorityClassName: spec.PriorityClassName(), WorkingDirectory: spec.WorkingDirectory(), InternalPrefix: spec.InternalPrefix(),
		NodeSelector: clonePublicationJobStringMap(spec.NodeSelector()), PreferredNodeSelector: clonePublicationJobStringMap(spec.PreferredNodeSelector()),
		Tolerations: append([]datasetpublisher.PublicationToleration(nil), spec.Tolerations()...),
		CPURequest:  spec.CPURequest(), CPULimit: spec.CPULimit(), MemoryRequest: spec.MemoryRequest(), MemoryLimit: spec.MemoryLimit(),
		BackoffLimit: spec.BackoffLimit(), ActiveDeadlineSeconds: int64(spec.ActiveDeadline() / time.Second),
		TTLAfterFinished: int64(spec.TTLAfterFinished() / time.Second), Labels: clonePublicationJobStringMap(labels),
	}
	payload, err := json.Marshal(canonical)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:]), nil
}

func boundedPublicationIdentityHash(identity string) string {
	digest := sha256.Sum256([]byte(identity))
	return hex.EncodeToString(digest[:publicationIdentityHashByteSize])
}

func clonePublicationJobStringMap(source map[string]string) map[string]string {
	result := make(map[string]string, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

func isNilPublicationJobSpec(spec publicationJobSpec) bool {
	return isNilPublicationInterface(spec)
}

func isNilPublicationInterface(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}
