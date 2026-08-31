package k8s

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
	"ray-train-platform-backend/datasetpublisher"
	"ray-train-platform-backend/domain"
)

var _ datasetpublisher.PublicationJobClient = (*Client)(nil)

func TestRenderDatasetPublicationJobIsSecureCPUOnlyAndImmutable(t *testing.T) {
	spec := publicationJobSpecForTest()
	before := spec.deepCopy()

	job, err := renderDatasetPublicationJob(spec)
	if err != nil {
		t.Fatalf("render dataset publication Job: %v", err)
	}
	if !reflect.DeepEqual(spec, before) {
		t.Fatalf("renderer mutated its input: got=%+v want=%+v", spec, before)
	}
	if job.Namespace != spec.namespace || job.Name != spec.name {
		t.Fatalf("Job identity=%s/%s, want %s/%s", job.Namespace, job.Name, spec.namespace, spec.name)
	}

	if len(job.Spec.Template.Spec.Containers) != 1 {
		t.Fatalf("containers=%d, want 1", len(job.Spec.Template.Spec.Containers))
	}
	container := job.Spec.Template.Spec.Containers[0]
	if container.Name != "dataset-publisher" {
		t.Fatalf("container name=%q", container.Name)
	}
	wantCommand := []string{"python3", "-m", "raytrain_publisher.cloud_publish"}
	if !reflect.DeepEqual(container.Command, wantCommand) {
		t.Fatalf("command=%q, want %q", container.Command, wantCommand)
	}
	wantArgs := []string{
		"--run-id", spec.runID,
		"--dataset-id", spec.datasetID,
		"--dataset-version-id", spec.datasetVersionID,
		"--version", spec.version,
		"--schema-version", spec.schemaVersion,
		"--source-bucket", spec.sourceBucket,
		"--target-bucket", spec.targetBucket,
		"--tos-endpoint", spec.tosEndpoint,
		"--tos-region", spec.tosRegion,
		"--source-root", spec.sourceRoot,
		"--source-index", spec.sourceIndex,
		"--internal-prefix", spec.internalPrefix,
		"--output-dir", spec.workingDirectory,
	}
	if !reflect.DeepEqual(container.Args, wantArgs) {
		t.Fatalf("args=%q, want %q", container.Args, wantArgs)
	}
	for _, value := range append(append([]string(nil), container.Command...), container.Args...) {
		if value == "sh" || value == "bash" || value == "-c" {
			t.Fatalf("publisher command invokes a shell: command=%q args=%q", container.Command, container.Args)
		}
	}
	if container.Image != spec.image || !strings.Contains(container.Image, "@sha256:") {
		t.Fatalf("publisher image=%q", container.Image)
	}
	if container.ImagePullPolicy != corev1.PullIfNotPresent || container.WorkingDir != spec.workingDirectory {
		t.Fatalf("pull policy/working dir=%q/%q", container.ImagePullPolicy, container.WorkingDir)
	}
	if len(container.Env) != 0 || len(container.EnvFrom) != 0 {
		t.Fatalf("publisher must not receive environment credentials: env=%#v envFrom=%#v", container.Env, container.EnvFrom)
	}

	assertPublicationResources(t, container, spec)
	assertPublicationSecurityContext(t, job.Spec.Template.Spec, container)
	assertPublicationVolumes(t, job.Spec.Template.Spec, container, spec.workingDirectory)
	assertPublicationSchedulingAndLifecycle(t, job, spec)
	assertPublicationIdentityMetadata(t, job, spec)

	manifest := string(mustJSON(job))
	for _, forbidden := range []string{"hostPath", "secretKeyRef", "secretRef", "TOS_ACCESS_KEY", "TOS_SECRET_KEY", "nvidia.com/gpu"} {
		if strings.Contains(manifest, forbidden) {
			t.Fatalf("publisher Job contains forbidden %q", forbidden)
		}
	}

	second, err := renderDatasetPublicationJob(spec)
	if err != nil {
		t.Fatal(err)
	}
	if second.Annotations["platform.wellspiking.ai/publication-spec-sha256"] != job.Annotations["platform.wellspiking.ai/publication-spec-sha256"] {
		t.Fatal("canonical spec hash is not stable")
	}
	changed := spec.deepCopy()
	changed.sourceIndex = "labeled/val-infos.pkl"
	changedJob, err := renderDatasetPublicationJob(changed)
	if err != nil {
		t.Fatal(err)
	}
	if changedJob.Annotations["platform.wellspiking.ai/publication-spec-sha256"] == job.Annotations["platform.wellspiking.ai/publication-spec-sha256"] {
		t.Fatal("canonical spec hash did not bind source index")
	}
}

func TestRenderDatasetPublicationJobInjectsManualVKEIRSAWhenConfigured(t *testing.T) {
	spec := publicationJobSpecForTest()
	spec.irsaRoleTRN = "trn:iam::2103446203:role/tos-rw"
	spec.proxySecretName = "dataset-publisher-egress"

	job, err := renderDatasetPublicationJob(spec)
	if err != nil {
		t.Fatalf("render configured IRSA publication Job: %v", err)
	}
	pod := job.Spec.Template.Spec
	if pod.AutomountServiceAccountToken == nil || *pod.AutomountServiceAccountToken {
		t.Fatalf("publisher Kubernetes API token automount changed: %v", pod.AutomountServiceAccountToken)
	}
	if len(pod.Containers) != 1 {
		t.Fatalf("containers=%d, want 1", len(pod.Containers))
	}
	container := pod.Containers[0]
	wantTokenFile := "/var/run/secrets/vke.volcengine.com/irsa-tokens/token"
	wantEnv := []corev1.EnvVar{
		{Name: "VOLCENGINE_OIDC_ROLE_TRN", Value: spec.irsaRoleTRN},
		{Name: "VOLCENGINE_OIDC_TOKEN_FILE", Value: wantTokenFile},
	}
	for _, name := range []string{"http_proxy", "https_proxy", "no_proxy"} {
		wantEnv = append(wantEnv, corev1.EnvVar{
			Name: name,
			ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: spec.proxySecretName},
				Key:                  name,
			}},
		})
	}
	if !reflect.DeepEqual(container.Env, wantEnv) || len(container.EnvFrom) != 0 {
		t.Fatalf("manual IRSA env=%#v envFrom=%#v, want %#v and no envFrom", container.Env, container.EnvFrom, wantEnv)
	}

	const tokenMountPath = "/var/run/secrets/vke.volcengine.com/irsa-tokens"
	var tokenMount *corev1.VolumeMount
	for index := range container.VolumeMounts {
		if container.VolumeMounts[index].MountPath == tokenMountPath {
			tokenMount = &container.VolumeMounts[index]
			break
		}
	}
	if tokenMount == nil || !tokenMount.ReadOnly {
		t.Fatalf("read-only VKE IRSA token mount is missing: %#v", container.VolumeMounts)
	}
	var tokenVolume *corev1.Volume
	for index := range pod.Volumes {
		if pod.Volumes[index].Name == tokenMount.Name {
			tokenVolume = &pod.Volumes[index]
			break
		}
	}
	if tokenVolume == nil || tokenVolume.Projected == nil || len(tokenVolume.Projected.Sources) != 1 {
		t.Fatalf("projected VKE IRSA token volume is missing: %#v", pod.Volumes)
	}
	projection := tokenVolume.Projected.Sources[0].ServiceAccountToken
	if projection == nil || projection.Audience != "sts.volcengine.com" || projection.Path != "token" ||
		projection.ExpirationSeconds == nil || *projection.ExpirationSeconds != 3600 {
		t.Fatalf("VKE IRSA token projection=%#v", projection)
	}
	if len(pod.Volumes) != 3 || len(container.VolumeMounts) != 3 {
		t.Fatalf("configured volumes=%d mounts=%d, want 3/3", len(pod.Volumes), len(container.VolumeMounts))
	}

	manifest := string(mustJSON(job))
	for _, forbidden := range []string{"secretRef", "TOS_ACCESS_KEY", "TOS_SECRET_KEY", "VOLCENGINE_ACCESS_KEY", "VOLCENGINE_SECRET_KEY"} {
		if strings.Contains(manifest, forbidden) {
			t.Fatalf("manual IRSA Job contains forbidden %q", forbidden)
		}
	}

	emptyJob, err := renderDatasetPublicationJob(publicationJobSpecForTest())
	if err != nil {
		t.Fatal(err)
	}
	if emptyJob.Annotations[publicationSpecHashAnnotation] == job.Annotations[publicationSpecHashAnnotation] {
		t.Fatal("canonical spec hash did not bind the optional IRSA role TRN")
	}
}

func TestRenderDatasetPublicationJobRejectsUnsafeInputs(t *testing.T) {
	base := publicationJobSpecForTest()
	tests := []struct {
		name   string
		mutate func(*fakePublicationJobSpec)
	}{
		{name: "mutable image tag", mutate: func(spec *fakePublicationJobSpec) {
			spec.image = "registry.example/dataset-publisher:latest"
		}},
		{name: "invalid CPU", mutate: func(spec *fakePublicationJobSpec) {
			spec.cpuRequest = "not-a-quantity"
		}},
		{name: "CPU request exceeds limit", mutate: func(spec *fakePublicationJobSpec) {
			spec.cpuRequest = "3"
			spec.cpuLimit = "2"
		}},
		{name: "overlapping tmp workdir", mutate: func(spec *fakePublicationJobSpec) {
			spec.workingDirectory = "/tmp"
		}},
		{name: "invalid toleration", mutate: func(spec *fakePublicationJobSpec) {
			spec.tolerations[0].Operator = "Never"
		}},
		{name: "toleration seconds on wrong effect", mutate: func(spec *fakePublicationJobSpec) {
			spec.tolerations[0].Effect = "NoSchedule"
		}},
		{name: "Exists toleration with value", mutate: func(spec *fakePublicationJobSpec) {
			spec.tolerations[0].Operator = "Exists"
		}},
		{name: "invalid IRSA role TRN", mutate: func(spec *fakePublicationJobSpec) {
			spec.irsaRoleTRN = "trn:iam::2103446203:user/not-a-role"
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			spec := base.deepCopy()
			test.mutate(&spec)
			if _, err := renderDatasetPublicationJob(spec); err == nil {
				t.Fatal("unsafe publication spec was rendered")
			}
		})
	}

	var typedNil *fakePublicationJobSpec
	if _, err := renderDatasetPublicationJob(typedNil); err == nil {
		t.Fatal("typed-nil publication spec was rendered")
	}
}

func TestEnsureDatasetPublicationJobCreatesOnceAndIsIdempotent(t *testing.T) {
	ctx := context.Background()
	spec := publicationJobSpecForTest()
	before := spec.deepCopy()
	clientset := k8sfake.NewSimpleClientset()
	client := &Client{kubernetes: clientset}

	for attempt := 0; attempt < 2; attempt++ {
		status, err := client.ensureDatasetPublicationJob(ctx, spec)
		if err != nil {
			t.Fatalf("ensure attempt %d: %v", attempt+1, err)
		}
		if status.Phase != datasetpublisher.PublicationJobPending || status.Receipt != nil {
			t.Fatalf("ensure attempt %d status=%+v", attempt+1, status)
		}
	}
	if !reflect.DeepEqual(spec, before) {
		t.Fatalf("ensure mutated spec: got=%+v want=%+v", spec, before)
	}
	jobs, err := clientset.BatchV1().Jobs(spec.namespace).List(ctx, metav1.ListOptions{})
	if err != nil || len(jobs.Items) != 1 {
		t.Fatalf("stored Jobs=%d err=%v", len(jobs.Items), err)
	}
	creates := 0
	for _, action := range clientset.Actions() {
		if action.Matches("create", "jobs") {
			creates++
		}
		if action.GetVerb() == "update" || action.GetVerb() == "delete" {
			t.Fatalf("idempotent ensure performed forbidden action: %#v", action)
		}
	}
	if creates != 1 {
		t.Fatalf("Job create actions=%d, want 1", creates)
	}
}

func TestEnsureDatasetPublicationJobRejectsExternalAndMismatchedJobs(t *testing.T) {
	spec := publicationJobSpecForTest()
	desired, err := renderDatasetPublicationJob(spec)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		mutate func(*batchv1.Job)
	}{
		{name: "external managed-by", mutate: func(job *batchv1.Job) {
			job.Labels[publicationManagedByLabel] = "external-controller"
		}},
		{name: "wrong application name", mutate: func(job *batchv1.Job) {
			job.Labels[publicationNameLabel] = "unrelated-workload"
		}},
		{name: "run identity mismatch", mutate: func(job *batchv1.Job) {
			job.Annotations[publicationRunIDAnnotation] = "attacker-run"
		}},
		{name: "dataset identity mismatch", mutate: func(job *batchv1.Job) {
			job.Annotations[publicationDatasetIDAnnotation] = "attacker-dataset"
		}},
		{name: "version identity mismatch", mutate: func(job *batchv1.Job) {
			job.Annotations[publicationVersionIDAnnotation] = "attacker-version"
		}},
		{name: "version value mismatch", mutate: func(job *batchv1.Job) {
			job.Annotations[publicationVersionAnnotation] = "attacker-version-value"
		}},
		{name: "identity hash mismatch", mutate: func(job *batchv1.Job) {
			job.Labels[publicationRunHashLabel] = strings.Repeat("0", 32)
		}},
		{name: "spec hash mismatch", mutate: func(job *batchv1.Job) {
			job.Annotations[publicationSpecHashAnnotation] = strings.Repeat("0", 64)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			existing := desired.DeepCopy()
			test.mutate(existing)
			clientset := k8sfake.NewSimpleClientset(existing)
			client := &Client{kubernetes: clientset}

			if _, err := client.ensureDatasetPublicationJob(context.Background(), spec); err == nil {
				t.Fatal("mismatched Job was adopted")
			}
			for _, action := range clientset.Actions() {
				if action.GetVerb() == "create" || action.GetVerb() == "update" || action.GetVerb() == "delete" {
					t.Fatalf("collision handling mutated Kubernetes state: %#v", action)
				}
			}
		})
	}
}

func TestEnsureDatasetPublicationJobAdoptsConcurrentCreateOnlyAfterOwnershipCheck(t *testing.T) {
	spec := publicationJobSpecForTest()
	desired, err := renderDatasetPublicationJob(spec)
	if err != nil {
		t.Fatal(err)
	}
	clientset := k8sfake.NewSimpleClientset()
	clientset.PrependReactor("create", "jobs", func(action k8stesting.Action) (bool, runtime.Object, error) {
		create := action.(k8stesting.CreateAction)
		created := create.GetObject().(*batchv1.Job).DeepCopy()
		if trackerErr := clientset.Tracker().Create(batchv1.SchemeGroupVersion.WithResource("jobs"), created, created.Namespace); trackerErr != nil {
			t.Fatalf("seed concurrent Job: %v", trackerErr)
		}
		return true, nil, apierrors.NewAlreadyExists(schema.GroupResource{Group: "batch", Resource: "jobs"}, desired.Name)
	})
	client := &Client{kubernetes: clientset}

	status, err := client.ensureDatasetPublicationJob(context.Background(), spec)
	if err != nil {
		t.Fatalf("adopt concurrently created Job: %v", err)
	}
	if status.Phase != datasetpublisher.PublicationJobPending {
		t.Fatalf("concurrent Job status=%+v", status)
	}
	getsAfterCreate := 0
	seenCreate := false
	for _, action := range clientset.Actions() {
		if action.Matches("create", "jobs") {
			seenCreate = true
		}
		if seenCreate && action.Matches("get", "jobs") {
			getsAfterCreate++
		}
	}
	if getsAfterCreate != 1 {
		t.Fatalf("post-AlreadyExists Get actions=%d, want 1", getsAfterCreate)
	}
}

func TestEnsureDatasetPublicationJobRejectsConcurrentExternalCollision(t *testing.T) {
	spec := publicationJobSpecForTest()
	external, err := renderDatasetPublicationJob(spec)
	if err != nil {
		t.Fatal(err)
	}
	external.Labels[publicationManagedByLabel] = "external-controller"
	clientset := k8sfake.NewSimpleClientset()
	clientset.PrependReactor("create", "jobs", func(k8stesting.Action) (bool, runtime.Object, error) {
		if trackerErr := clientset.Tracker().Create(batchv1.SchemeGroupVersion.WithResource("jobs"), external.DeepCopy(), external.Namespace); trackerErr != nil {
			t.Fatalf("seed external Job: %v", trackerErr)
		}
		return true, nil, apierrors.NewAlreadyExists(schema.GroupResource{Group: "batch", Resource: "jobs"}, external.Name)
	})
	client := &Client{kubernetes: clientset}

	if _, err := client.ensureDatasetPublicationJob(context.Background(), spec); err == nil {
		t.Fatal("concurrently created external Job was adopted")
	}
	for _, action := range clientset.Actions() {
		if action.GetVerb() == "update" || action.GetVerb() == "delete" {
			t.Fatalf("external collision was mutated: %#v", action)
		}
	}
}

func TestEnsurePublicationJobHandlesNilClientsAndSanitizesAPIErrors(t *testing.T) {
	ctx := context.Background()
	var zeroSpec datasetpublisher.PublicationJobSpec
	clients := []struct {
		name   string
		client *Client
	}{
		{name: "nil receiver", client: nil},
		{name: "nil interface", client: &Client{}},
		{name: "typed nil interface", client: &Client{kubernetes: (*k8sfake.Clientset)(nil)}},
	}
	for _, test := range clients {
		t.Run(test.name, func(t *testing.T) {
			if _, err := test.client.EnsurePublicationJob(ctx, zeroSpec); err == nil {
				t.Fatal("nil Kubernetes client was accepted")
			}
		})
	}

	sensitive := "AK=secret tos://private/path termination-payload"
	t.Run("get", func(t *testing.T) {
		clientset := k8sfake.NewSimpleClientset()
		clientset.PrependReactor("get", "jobs", func(k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, errors.New(sensitive)
		})
		_, err := (&Client{kubernetes: clientset}).ensureDatasetPublicationJob(ctx, publicationJobSpecForTest())
		assertSanitizedPublicationError(t, err, sensitive)
	})
	t.Run("create", func(t *testing.T) {
		clientset := k8sfake.NewSimpleClientset()
		clientset.PrependReactor("create", "jobs", func(k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, errors.New(sensitive)
		})
		_, err := (&Client{kubernetes: clientset}).ensureDatasetPublicationJob(ctx, publicationJobSpecForTest())
		assertSanitizedPublicationError(t, err, sensitive)
	})
}

func TestEnsureDatasetPublicationJobMapsRunningAndFailedConditions(t *testing.T) {
	spec := publicationJobSpecForTest()
	tests := []struct {
		name      string
		status    batchv1.JobStatus
		wantPhase datasetpublisher.PublicationJobPhase
	}{
		{
			name:      "active is packing",
			status:    batchv1.JobStatus{Active: 1},
			wantPhase: datasetpublisher.PublicationJobPacking,
		},
		{
			name: "failed condition",
			status: batchv1.JobStatus{Conditions: []batchv1.JobCondition{{
				Type: batchv1.JobFailed, Status: corev1.ConditionTrue,
			}}},
			wantPhase: datasetpublisher.PublicationJobFailed,
		},
		{
			name: "failed wins over complete",
			status: batchv1.JobStatus{Conditions: []batchv1.JobCondition{
				{Type: batchv1.JobComplete, Status: corev1.ConditionTrue},
				{Type: batchv1.JobFailed, Status: corev1.ConditionTrue},
			}},
			wantPhase: datasetpublisher.PublicationJobFailed,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			job := mustRenderPublicationJob(t, spec)
			job.Status = test.status
			clientset := k8sfake.NewSimpleClientset(job)

			status, err := (&Client{kubernetes: clientset}).ensureDatasetPublicationJob(context.Background(), spec)
			if err != nil {
				t.Fatalf("ensure: %v", err)
			}
			if status.Phase != test.wantPhase || status.Receipt != nil {
				t.Fatalf("status=%+v, want phase %s", status, test.wantPhase)
			}
			for _, action := range clientset.Actions() {
				if action.Matches("list", "pods") {
					t.Fatal("non-complete Job caused a Pod list")
				}
			}
		})
	}
}

func TestEnsureDatasetPublicationJobParsesSucceededReceiptFromOwnedPod(t *testing.T) {
	spec := publicationJobSpecForTest()
	job := completedPublicationJobForTest(t, spec)
	payload := validPublicationTerminationPayload(t, spec)
	pod := publicationPodForTest(job, job.UID, payload)
	clientset := k8sfake.NewSimpleClientset(job, pod)

	status, err := (&Client{kubernetes: clientset}).ensureDatasetPublicationJob(context.Background(), spec)
	if err != nil {
		t.Fatalf("ensure complete Job: %v", err)
	}
	wantProgress := datasetpublisher.PublicationProgress{
		TotalPartitions: 2, CompletedPartitions: 2, FailedPartitions: 0,
		SourceObjectCount: 4, ProcessedObjectCount: 4, FailedObjectCount: 0,
	}
	if status.Phase != datasetpublisher.PublicationJobSucceeded || !reflect.DeepEqual(status.Progress, wantProgress) {
		t.Fatalf("succeeded status=%+v", status)
	}
	if status.Receipt == nil || status.Receipt.DatasetID != spec.datasetID ||
		status.Receipt.DatasetVersionID != spec.datasetVersionID ||
		status.Receipt.Version != spec.version ||
		status.Receipt.SchemaVersion != spec.schemaVersion ||
		status.Receipt.ManifestSHA256 != strings.Repeat("b", 64) ||
		status.Receipt.TrainSamples != 4 || status.Receipt.SourceObjectCount != 4 {
		t.Fatalf("parsed receipt=%+v", status.Receipt)
	}

	foundRestrictedList := false
	for _, action := range clientset.Actions() {
		if !action.Matches("list", "pods") {
			continue
		}
		selector := action.(k8stesting.ListAction).GetListRestrictions().Labels.String()
		if !strings.Contains(selector, "batch.kubernetes.io/job-name="+job.Name) ||
			!strings.Contains(selector, publicationRunHashLabel+"="+job.Labels[publicationRunHashLabel]) {
			t.Fatalf("Pod list selector=%q", selector)
		}
		foundRestrictedList = true
	}
	if !foundRestrictedList {
		t.Fatal("complete Job did not list Pods")
	}
}

func TestEnsureDatasetPublicationJobAllowsMissingPodOrReceiptForRetry(t *testing.T) {
	spec := publicationJobSpecForTest()
	tests := []struct {
		name         string
		podPayload   *string
		wantProgress datasetpublisher.PublicationProgress
	}{
		{name: "missing Pod"},
		{name: "empty termination message", podPayload: publicationStringPointer("")},
		{
			name:         "missing receipt",
			podPayload:   publicationStringPointer(`{"progress":{"total_partitions":2,"completed_partitions":1,"source_object_count":4,"processed_object_count":2}}`),
			wantProgress: datasetpublisher.PublicationProgress{TotalPartitions: 2, CompletedPartitions: 1, SourceObjectCount: 4, ProcessedObjectCount: 2},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			job := completedPublicationJobForTest(t, spec)
			objects := []runtime.Object{job}
			if test.podPayload != nil {
				objects = append(objects, publicationPodForTest(job, job.UID, *test.podPayload))
			}
			clientset := k8sfake.NewSimpleClientset(objects...)

			status, err := (&Client{kubernetes: clientset}).ensureDatasetPublicationJob(context.Background(), spec)
			if err != nil {
				t.Fatalf("ensure complete Job: %v", err)
			}
			if status.Phase != datasetpublisher.PublicationJobSucceeded || status.Receipt != nil || !reflect.DeepEqual(status.Progress, test.wantProgress) {
				t.Fatalf("retryable complete status=%+v", status)
			}
		})
	}
}

func TestEnsureDatasetPublicationJobRejectsOversizeAndInvalidReceiptsWithoutLeaks(t *testing.T) {
	spec := publicationJobSpecForTest()
	invalidReceipt := map[string]any{
		"progress": map[string]any{"total_partitions": 1},
		"receipt": map[string]any{
			"dataset_id": spec.datasetID, "dataset_version_id": spec.datasetVersionID,
			"version": spec.version, "schema_version": spec.schemaVersion,
			"manifest_sha256":     strings.Repeat("b", 64),
			"manifest_object_key": "ray-train/platform/datasets/../../AK=secret/private/path",
			"train_samples":       1, "source_object_count": 1, "logical_bytes": 1, "packed_bytes": 1,
		},
	}
	invalidReceiptJSON, err := json.Marshal(invalidReceipt)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name      string
		payload   string
		sensitive string
	}{
		{name: "oversize", payload: strings.Repeat("x", 4097), sensitive: strings.Repeat("x", 128)},
		{name: "invalid JSON", payload: `{"receipt":"AK=secret tos://private/path"`, sensitive: "AK=secret tos://private/path"},
		{name: "invalid receipt", payload: string(invalidReceiptJSON), sensitive: "AK=secret/private/path"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			job := completedPublicationJobForTest(t, spec)
			pod := publicationPodForTest(job, job.UID, test.payload)
			clientset := k8sfake.NewSimpleClientset(job, pod)

			_, err := (&Client{kubernetes: clientset}).ensureDatasetPublicationJob(context.Background(), spec)
			assertSanitizedPublicationError(t, err, test.sensitive)
		})
	}
}

func TestEnsureDatasetPublicationJobIgnoresForeignPodsAndSanitizesListErrors(t *testing.T) {
	spec := publicationJobSpecForTest()
	t.Run("foreign owner UID", func(t *testing.T) {
		job := completedPublicationJobForTest(t, spec)
		foreign := publicationPodForTest(job, types.UID("foreign-job-uid"), validPublicationTerminationPayload(t, spec))
		clientset := k8sfake.NewSimpleClientset(job, foreign)

		status, err := (&Client{kubernetes: clientset}).ensureDatasetPublicationJob(context.Background(), spec)
		if err != nil {
			t.Fatal(err)
		}
		if status.Phase != datasetpublisher.PublicationJobSucceeded || status.Receipt != nil || status.Progress != (datasetpublisher.PublicationProgress{}) {
			t.Fatalf("foreign Pod influenced status: %+v", status)
		}
	})

	t.Run("list error", func(t *testing.T) {
		job := completedPublicationJobForTest(t, spec)
		clientset := k8sfake.NewSimpleClientset(job)
		sensitive := "AK=secret tos://private/path termination-payload"
		clientset.PrependReactor("list", "pods", func(k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, errors.New(sensitive)
		})

		_, err := (&Client{kubernetes: clientset}).ensureDatasetPublicationJob(context.Background(), spec)
		assertSanitizedPublicationError(t, err, sensitive)
	})
}

func mustRenderPublicationJob(t *testing.T, spec fakePublicationJobSpec) *batchv1.Job {
	t.Helper()
	job, err := renderDatasetPublicationJob(spec)
	if err != nil {
		t.Fatal(err)
	}
	return job
}

func completedPublicationJobForTest(t *testing.T, spec fakePublicationJobSpec) *batchv1.Job {
	t.Helper()
	job := mustRenderPublicationJob(t, spec)
	job.UID = types.UID("publication-job-uid")
	job.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}}
	return job
}

func publicationPodForTest(job *batchv1.Job, ownerUID types.UID, payload string) *corev1.Pod {
	controller := true
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: job.Namespace,
			Name:      job.Name + "-pod",
			Labels: map[string]string{
				"batch.kubernetes.io/job-name": job.Name,
				publicationRunHashLabel:        job.Labels[publicationRunHashLabel],
			},
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "batch/v1", Kind: "Job", Name: job.Name, UID: ownerUID, Controller: &controller,
			}},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodSucceeded,
			ContainerStatuses: []corev1.ContainerStatus{{
				Name:  datasetPublisherContainerName,
				State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0, Message: payload}},
			}},
		},
	}
}

func validPublicationTerminationPayload(t *testing.T, spec fakePublicationJobSpec) string {
	t.Helper()
	payload := map[string]any{
		"progress": map[string]any{
			"total_partitions": 2, "completed_partitions": 2, "failed_partitions": 0,
			"source_object_count": 4, "processed_object_count": 4, "failed_object_count": 0,
		},
		"receipt": map[string]any{
			"dataset_id": spec.datasetID, "dataset_version_id": spec.datasetVersionID,
			"version": spec.version, "schema_version": spec.schemaVersion,
			"manifest_sha256":     strings.Repeat("b", 64),
			"manifest_object_key": spec.internalPrefix + "/" + spec.datasetID + "/manifests/" + spec.datasetVersionID + ".parquet",
			"train_samples":       4, "val_samples": 0, "test_samples": 0,
			"source_object_count": 4, "logical_bytes": 4096, "packed_bytes": 2048,
		},
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) > 4096 {
		t.Fatalf("test payload unexpectedly too large: %d", len(encoded))
	}
	return string(encoded)
}

func publicationStringPointer(value string) *string { return &value }

func assertSanitizedPublicationError(t *testing.T, err error, sensitive string) {
	t.Helper()
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, fragment := range []string{sensitive, "AK=", "tos://", "private/path", "termination-payload"} {
		if strings.Contains(err.Error(), fragment) {
			t.Fatalf("error leaked sensitive context: %q", err)
		}
	}
}

func assertPublicationResources(t *testing.T, container corev1.Container, spec fakePublicationJobSpec) {
	t.Helper()
	want := map[corev1.ResourceName]string{
		corev1.ResourceCPU:    spec.cpuRequest,
		corev1.ResourceMemory: spec.memoryRequest,
	}
	if len(container.Resources.Requests) != len(want) || len(container.Resources.Limits) != len(want) {
		t.Fatalf("resource keys requests=%v limits=%v", container.Resources.Requests, container.Resources.Limits)
	}
	for name, value := range want {
		if got := container.Resources.Requests[name]; got.Cmp(resource.MustParse(value)) != 0 {
			t.Fatalf("request %s=%s, want %s", name, got.String(), value)
		}
	}
	want[corev1.ResourceCPU] = spec.cpuLimit
	want[corev1.ResourceMemory] = spec.memoryLimit
	for name, value := range want {
		if got := container.Resources.Limits[name]; got.Cmp(resource.MustParse(value)) != 0 {
			t.Fatalf("limit %s=%s, want %s", name, got.String(), value)
		}
	}
}

func assertPublicationSecurityContext(t *testing.T, pod corev1.PodSpec, container corev1.Container) {
	t.Helper()
	if pod.RestartPolicy != corev1.RestartPolicyNever || pod.AutomountServiceAccountToken == nil || *pod.AutomountServiceAccountToken {
		t.Fatalf("unsafe Pod lifecycle/token settings: restart=%q automount=%v", pod.RestartPolicy, pod.AutomountServiceAccountToken)
	}
	if pod.SecurityContext == nil || pod.SecurityContext.RunAsNonRoot == nil || !*pod.SecurityContext.RunAsNonRoot ||
		pod.SecurityContext.RunAsUser == nil || *pod.SecurityContext.RunAsUser != 65532 ||
		pod.SecurityContext.RunAsGroup == nil || *pod.SecurityContext.RunAsGroup != 65532 ||
		pod.SecurityContext.SeccompProfile == nil || pod.SecurityContext.SeccompProfile.Type != corev1.SeccompProfileTypeRuntimeDefault {
		t.Fatalf("unsafe Pod security context: %#v", pod.SecurityContext)
	}
	security := container.SecurityContext
	if security == nil || security.RunAsNonRoot == nil || !*security.RunAsNonRoot ||
		security.RunAsUser == nil || *security.RunAsUser != 65532 ||
		security.RunAsGroup == nil || *security.RunAsGroup != 65532 ||
		security.ReadOnlyRootFilesystem == nil || !*security.ReadOnlyRootFilesystem ||
		security.AllowPrivilegeEscalation == nil || *security.AllowPrivilegeEscalation ||
		security.SeccompProfile == nil || security.SeccompProfile.Type != corev1.SeccompProfileTypeRuntimeDefault ||
		security.Capabilities == nil || !reflect.DeepEqual(security.Capabilities.Drop, []corev1.Capability{"ALL"}) {
		t.Fatalf("unsafe container security context: %#v", security)
	}
}

func assertPublicationVolumes(t *testing.T, pod corev1.PodSpec, container corev1.Container, workingDirectory string) {
	t.Helper()
	if len(pod.Volumes) != 2 || len(container.VolumeMounts) != 2 {
		t.Fatalf("volumes=%#v mounts=%#v", pod.Volumes, container.VolumeMounts)
	}
	volumeNames := map[string]bool{}
	for _, volume := range pod.Volumes {
		if volume.EmptyDir == nil {
			t.Fatalf("non-EmptyDir volume rendered: %#v", volume)
		}
		volumeNames[volume.Name] = true
	}
	mounts := map[string]string{}
	for _, mount := range container.VolumeMounts {
		if mount.ReadOnly {
			t.Fatalf("publisher writable scratch mount is read-only: %#v", mount)
		}
		if !volumeNames[mount.Name] {
			t.Fatalf("mount refers to unknown volume: %#v", mount)
		}
		mounts[mount.Name] = mount.MountPath
	}
	if mounts["work"] != workingDirectory || mounts["tmp"] != "/tmp" {
		t.Fatalf("scratch mounts=%v", mounts)
	}
}

func assertPublicationSchedulingAndLifecycle(t *testing.T, job *batchv1.Job, spec fakePublicationJobSpec) {
	t.Helper()
	pod := job.Spec.Template.Spec
	if pod.ServiceAccountName != spec.serviceAccountName || pod.PriorityClassName != spec.priorityClassName {
		t.Fatalf("service account/priority=%q/%q", pod.ServiceAccountName, pod.PriorityClassName)
	}
	if !reflect.DeepEqual(pod.NodeSelector, spec.nodeSelector) {
		t.Fatalf("hard node selector=%v, want %v", pod.NodeSelector, spec.nodeSelector)
	}
	if pod.Affinity == nil || pod.Affinity.NodeAffinity == nil || len(pod.Affinity.NodeAffinity.PreferredDuringSchedulingIgnoredDuringExecution) != 1 {
		t.Fatalf("preferred node affinity=%#v", pod.Affinity)
	}
	preferred := pod.Affinity.NodeAffinity.PreferredDuringSchedulingIgnoredDuringExecution[0]
	if preferred.Weight != 100 || len(preferred.Preference.MatchExpressions) != len(spec.preferredNodeSelector) {
		t.Fatalf("preferred scheduling term=%#v", preferred)
	}
	gotPreferred := map[string]string{}
	for _, expression := range preferred.Preference.MatchExpressions {
		if expression.Operator != corev1.NodeSelectorOpIn || len(expression.Values) != 1 {
			t.Fatalf("preferred selector expression=%#v", expression)
		}
		gotPreferred[expression.Key] = expression.Values[0]
	}
	if !reflect.DeepEqual(gotPreferred, spec.preferredNodeSelector) {
		t.Fatalf("preferred node selector=%v, want %v", gotPreferred, spec.preferredNodeSelector)
	}
	if len(pod.Tolerations) != 1 || pod.Tolerations[0].TolerationSeconds == nil || *pod.Tolerations[0].TolerationSeconds != 30 {
		t.Fatalf("tolerations=%#v", pod.Tolerations)
	}
	if job.Spec.BackoffLimit == nil || *job.Spec.BackoffLimit != int32(spec.backoffLimit) ||
		job.Spec.ActiveDeadlineSeconds == nil || *job.Spec.ActiveDeadlineSeconds != int64(spec.activeDeadline/time.Second) ||
		job.Spec.TTLSecondsAfterFinished == nil || *job.Spec.TTLSecondsAfterFinished != int32(spec.ttlAfterFinished/time.Second) {
		t.Fatalf("Job lifecycle=%#v", job.Spec)
	}
}

func assertPublicationIdentityMetadata(t *testing.T, job *batchv1.Job, spec fakePublicationJobSpec) {
	t.Helper()
	annotations := job.Annotations
	wantAnnotations := map[string]string{
		"platform.wellspiking.ai/publication-run-id": spec.runID,
		"platform.wellspiking.ai/dataset-id":         spec.datasetID,
		"platform.wellspiking.ai/dataset-version-id": spec.datasetVersionID,
		"platform.wellspiking.ai/dataset-version":    spec.version,
		"platform.wellspiking.ai/schema-version":     spec.schemaVersion,
	}
	for key, value := range wantAnnotations {
		if annotations[key] != value {
			t.Fatalf("annotation %q=%q, want %q", key, annotations[key], value)
		}
	}
	specHash := annotations["platform.wellspiking.ai/publication-spec-sha256"]
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(specHash) {
		t.Fatalf("spec hash=%q", specHash)
	}
	labels := job.Labels
	for key, value := range map[string]string{
		"app.kubernetes.io/managed-by":                 "ray-train-platform",
		"app.kubernetes.io/name":                       "dataset-publisher",
		"kueue.x-k8s.io/queue-name":                    spec.queueName,
		"platform.wellspiking.ai/publication-job-name": spec.name,
	} {
		if labels[key] != value {
			t.Fatalf("label %q=%q, want %q", key, labels[key], value)
		}
	}
	for _, key := range []string{
		"platform.wellspiking.ai/publication-run-hash",
		"platform.wellspiking.ai/dataset-hash",
		"platform.wellspiking.ai/dataset-version-hash",
		"platform.wellspiking.ai/version-hash",
	} {
		if !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(labels[key]) {
			t.Fatalf("bounded identity label %q=%q", key, labels[key])
		}
	}
	for key, value := range annotations {
		if job.Spec.Template.Annotations[key] != value {
			t.Fatalf("Pod annotation %q=%q, want %q", key, job.Spec.Template.Annotations[key], value)
		}
	}
	for key, value := range labels {
		if job.Spec.Template.Labels[key] != value {
			t.Fatalf("Pod label %q=%q, want %q", key, job.Spec.Template.Labels[key], value)
		}
	}
}

type fakePublicationJobSpec struct {
	namespace             string
	name                  string
	runID                 string
	datasetID             string
	datasetVersionID      string
	version               string
	schemaVersion         string
	sourceRoot            string
	sourceIndex           string
	image                 string
	sourceBucket          string
	targetBucket          string
	tosEndpoint           string
	tosRegion             string
	imagePullPolicy       string
	serviceAccountName    string
	irsaRoleTRN           string
	proxySecretName       string
	queueName             string
	priorityClassName     string
	workingDirectory      string
	internalPrefix        string
	nodeSelector          map[string]string
	preferredNodeSelector map[string]string
	tolerations           []datasetpublisher.PublicationToleration
	cpuRequest            string
	cpuLimit              string
	memoryRequest         string
	memoryLimit           string
	backoffLimit          int
	activeDeadline        time.Duration
	ttlAfterFinished      time.Duration
	labels                map[string]string
}

func publicationJobSpecForTest() fakePublicationJobSpec {
	name := "dataset-publisher-0123456789abcdef0123456789abcdef"
	return fakePublicationJobSpec{
		namespace: "ray-train-platform", name: name,
		runID: "publication-run-1", datasetID: "dataset-team-a", datasetVersionID: "version-team-a",
		version: "20260830.1", schemaVersion: "s1h-lidar-parquet-v1",
		sourceRoot: "ray-train/tenants/team-a/shared", sourceIndex: "labeled/train-infos.pkl",
		image:        "registry.example/dataset-publisher@sha256:" + strings.Repeat("a", 64),
		sourceBucket: "source-bucket", targetBucket: "target-bucket",
		tosEndpoint: "tos-cn-shanghai.ivolces.com", tosRegion: "cn-shanghai",
		imagePullPolicy: "IfNotPresent", serviceAccountName: "dataset-publisher",
		queueName: "dataset-publisher-low", priorityClassName: "dataset-publisher-low",
		workingDirectory: "/data/output", internalPrefix: domain.DefaultDatasetInternalPrefix,
		nodeSelector:          map[string]string{"platform.wellspiking.ai/pool": "cpu"},
		preferredNodeSelector: map[string]string{"platform.wellspiking.ai/fallback": "gpu"},
		tolerations: []datasetpublisher.PublicationToleration{{
			Key: "workload", Operator: "Equal", Value: "cpu", Effect: "NoExecute", Seconds: 30, HasSeconds: true,
		}},
		cpuRequest: "500m", cpuLimit: "2", memoryRequest: "1Gi", memoryLimit: "4Gi",
		backoffLimit: 2, activeDeadline: 7 * 24 * time.Hour, ttlAfterFinished: 24 * time.Hour,
		labels: map[string]string{
			"app.kubernetes.io/managed-by":                 "ray-train-platform",
			"app.kubernetes.io/name":                       "dataset-publisher",
			"kueue.x-k8s.io/queue-name":                    "dataset-publisher-low",
			"platform.wellspiking.ai/publication-job-name": name,
		},
	}
}

func (spec fakePublicationJobSpec) deepCopy() fakePublicationJobSpec {
	result := spec
	result.nodeSelector = cloneTestStringMap(spec.nodeSelector)
	result.preferredNodeSelector = cloneTestStringMap(spec.preferredNodeSelector)
	result.tolerations = append([]datasetpublisher.PublicationToleration(nil), spec.tolerations...)
	result.labels = cloneTestStringMap(spec.labels)
	return result
}

func cloneTestStringMap(source map[string]string) map[string]string {
	result := make(map[string]string, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

func (spec fakePublicationJobSpec) Namespace() string          { return spec.namespace }
func (spec fakePublicationJobSpec) Name() string               { return spec.name }
func (spec fakePublicationJobSpec) RunID() string              { return spec.runID }
func (spec fakePublicationJobSpec) DatasetID() string          { return spec.datasetID }
func (spec fakePublicationJobSpec) DatasetVersionID() string   { return spec.datasetVersionID }
func (spec fakePublicationJobSpec) Version() string            { return spec.version }
func (spec fakePublicationJobSpec) SchemaVersion() string      { return spec.schemaVersion }
func (spec fakePublicationJobSpec) SourceRoot() string         { return spec.sourceRoot }
func (spec fakePublicationJobSpec) SourceIndex() string        { return spec.sourceIndex }
func (spec fakePublicationJobSpec) Image() string              { return spec.image }
func (spec fakePublicationJobSpec) SourceBucket() string       { return spec.sourceBucket }
func (spec fakePublicationJobSpec) TargetBucket() string       { return spec.targetBucket }
func (spec fakePublicationJobSpec) TOSEndpoint() string        { return spec.tosEndpoint }
func (spec fakePublicationJobSpec) TOSRegion() string          { return spec.tosRegion }
func (spec fakePublicationJobSpec) ImagePullPolicy() string    { return spec.imagePullPolicy }
func (spec fakePublicationJobSpec) ServiceAccountName() string { return spec.serviceAccountName }
func (spec fakePublicationJobSpec) IRSARoleTRN() string        { return spec.irsaRoleTRN }
func (spec fakePublicationJobSpec) ProxySecretName() string    { return spec.proxySecretName }
func (spec fakePublicationJobSpec) QueueName() string          { return spec.queueName }
func (spec fakePublicationJobSpec) PriorityClassName() string  { return spec.priorityClassName }
func (spec fakePublicationJobSpec) WorkingDirectory() string   { return spec.workingDirectory }
func (spec fakePublicationJobSpec) InternalPrefix() string     { return spec.internalPrefix }
func (spec fakePublicationJobSpec) NodeSelector() map[string]string {
	return spec.nodeSelector
}
func (spec fakePublicationJobSpec) PreferredNodeSelector() map[string]string {
	return spec.preferredNodeSelector
}
func (spec fakePublicationJobSpec) Tolerations() []datasetpublisher.PublicationToleration {
	return spec.tolerations
}
func (spec fakePublicationJobSpec) CPURequest() string              { return spec.cpuRequest }
func (spec fakePublicationJobSpec) CPULimit() string                { return spec.cpuLimit }
func (spec fakePublicationJobSpec) MemoryRequest() string           { return spec.memoryRequest }
func (spec fakePublicationJobSpec) MemoryLimit() string             { return spec.memoryLimit }
func (spec fakePublicationJobSpec) BackoffLimit() int               { return spec.backoffLimit }
func (spec fakePublicationJobSpec) ActiveDeadline() time.Duration   { return spec.activeDeadline }
func (spec fakePublicationJobSpec) TTLAfterFinished() time.Duration { return spec.ttlAfterFinished }
func (spec fakePublicationJobSpec) Labels() map[string]string       { return spec.labels }
