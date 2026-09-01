package datasetpublisher

import (
	"context"
	"errors"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"ray-train-platform-backend/domain"
)

func publicationControllerOptions() ControllerOptions {
	return ControllerOptions{
		Namespace:             "ray-train-platform",
		Image:                 "registry.example/dataset-publisher@sha256:" + strings.Repeat("a", 64),
		SourceBucket:          "shanghai-data-transfer",
		TargetBucket:          "shanghai-data-transfer",
		TOSEndpoint:           "tos-cn-shanghai.ivolces.com",
		TOSRegion:             "cn-shanghai",
		ServiceAccountName:    "dataset-publisher",
		QueueName:             "dataset-publisher-low",
		PriorityClassName:     "dataset-publisher-low",
		CPURequest:            "500m",
		CPULimit:              "2",
		MemoryRequest:         "1Gi",
		MemoryLimit:           "4Gi",
		InternalPrefix:        domain.DefaultDatasetInternalPrefix,
		ImagePullPolicy:       "IfNotPresent",
		WorkingDirectory:      "/data/output",
		NodeSelector:          map[string]string{"workload": "cpu"},
		PreferredNodeSelector: map[string]string{"node-role": "cpu"},
		Tolerations:           []PublicationToleration{{Key: "workload", Operator: "Equal", Value: "cpu", Effect: "NoSchedule"}},
		ClientMaxAttempts:     3,
		JobBackoffLimit:       2,
		JobActiveDeadline:     7 * 24 * time.Hour,
		JobTTLAfterFinished:   24 * time.Hour,
		InitialRetryBackoff:   time.Second,
		MaximumRetryBackoff:   4 * time.Second,
		Now:                   func() time.Time { return time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC) },
		Wait:                  func(context.Context, time.Duration) error { return nil },
	}
}

func completedPublicationReceipt() *domain.DatasetPublicationReceipt {
	return &domain.DatasetPublicationReceipt{
		DatasetID: "dataset-team-a", DatasetVersionID: "version-team-a", Version: "v1",
		ManifestSHA256:    strings.Repeat("b", 64),
		ManifestObjectKey: domain.DefaultDatasetInternalPrefix + "/dataset-team-a/manifests/version-team-a.parquet",
		SchemaVersion:     "s1h-lidar-parquet-v1", TrainSamples: 4,
		SourceObjectCount: 4, LogicalBytes: 4096, PackedBytes: 2048,
	}
}

func publicationReconcileRequest(runID string) ReconcileRequest {
	return ReconcileRequest{
		TenantID: "team-a", DatasetID: "dataset-team-a",
		DatasetVersionID: "version-team-a", RunID: runID,
		Version: "20260830.1", SchemaVersion: "s1h-lidar-parquet-v1",
		SourceRoot: "ray-train/tenants/team-a/shared", SourceIndex: "labeled/train-infos.pkl",
	}
}

func completedPublicationProgress() PublicationProgress {
	return PublicationProgress{
		TotalPartitions: 2, CompletedPartitions: 2,
		SourceObjectCount: 4, ProcessedObjectCount: 4,
	}
}

func TestPublicationProgressRejectsAnEmptySuccessfulPublication(t *testing.T) {
	if (PublicationProgress{}).complete() {
		t.Fatal("an empty publication must not become READY")
	}
	if (PublicationProgress{TotalPartitions: 1, CompletedPartitions: 1}).complete() {
		t.Fatal("a publication without source objects must not become READY")
	}
	if (PublicationProgress{SourceObjectCount: 1, ProcessedObjectCount: 1}).complete() {
		t.Fatal("a publication without partitions must not become READY")
	}
}

func TestControllerAdvancesPublicationRunThroughEveryState(t *testing.T) {
	repository := newMemoryPublicationRunRepository("team-a")
	jobs := &scriptedPublicationJobClient{results: []publicationJobResult{{
		status: PublicationJobStatus{Phase: PublicationJobSucceeded, Progress: completedPublicationProgress(), Receipt: completedPublicationReceipt()},
	}}}
	controller, err := NewController(repository, jobs, publicationControllerOptions())
	if err != nil {
		t.Fatalf("new controller: %v", err)
	}
	request := publicationReconcileRequest("publication-ready")
	before := request

	run, err := controller.Reconcile(context.Background(), request)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if run.State != domain.DatasetVersionReady || run.CompletedPartitions != run.TotalPartitions || run.ProcessedObjectCount != run.SourceObjectCount {
		t.Fatalf("completed run=%+v", run)
	}
	if !reflect.DeepEqual(request, before) {
		t.Fatalf("Reconcile mutated request: got=%+v want=%+v", request, before)
	}
	if got, want := repository.states(), []domain.DatasetVersionState{
		domain.DatasetVersionStabilizing,
		domain.DatasetVersionValidating,
		domain.DatasetVersionPacking,
		domain.DatasetVersionReady,
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("state progression=%v, want %v", got, want)
	}
	if jobs.callCount() != 1 {
		t.Fatalf("publication job ensure calls=%d, want 1", jobs.callCount())
	}
}

func TestControllerRunsDistributedPlanPackThenFinalizeWithoutChangingLegacyMode(t *testing.T) {
	repository := newMemoryPublicationRunRepository("team-a")
	jobs := &scriptedPublicationJobClient{results: []publicationJobResult{
		{status: PublicationJobStatus{Phase: PublicationJobValidating, Progress: PublicationProgress{TotalPartitions: 8}}},
		{status: PublicationJobStatus{Phase: PublicationJobPacked, Progress: PublicationProgress{TotalPartitions: 8, CompletedPartitions: 8}}},
		{status: PublicationJobStatus{Phase: PublicationJobSucceeded, Progress: completedPublicationProgress(), Receipt: completedPublicationReceipt()}},
	}}
	options := publicationControllerOptions()
	options.DistributedEnabled = true
	options.PartitionCount = 8
	options.MaxParallelism = 2
	controller, err := NewController(repository, jobs, options)
	if err != nil {
		t.Fatalf("new distributed controller: %v", err)
	}
	request := publicationReconcileRequest("publication-distributed")
	repository.run = &domain.DatasetPublicationRun{ID: request.RunID, DatasetID: request.DatasetID, DatasetVersionID: request.DatasetVersionID, ExecutionMode: domain.DatasetPublicationExecutionDistributed, State: domain.DatasetVersionDiscovering}
	for want := 0; want < 3; want++ {
		if _, err := controller.Reconcile(context.Background(), request); err != nil {
			t.Fatalf("distributed reconcile %d: %v", want+1, err)
		}
	}
	specs := jobs.recordedSpecs()
	if len(specs) != 3 {
		t.Fatalf("distributed spec count=%d, want 3", len(specs))
	}
	wantPhases := []PublicationExecutionPhase{PublicationExecutionPlan, PublicationExecutionPack, PublicationExecutionFinalize}
	for index, want := range wantPhases {
		if specs[index].ExecutionPhase() != want || specs[index].PartitionCount() != 8 || specs[index].MaxParallelism() != 2 {
			t.Fatalf("distributed spec %d=%+v, want phase %q", index, specs[index], want)
		}
		if !strings.HasSuffix(specs[index].Name(), "-"+string(want)) {
			t.Fatalf("distributed job name=%q, want phase suffix %q", specs[index].Name(), want)
		}
	}
}

func TestControllerKeepsPersistedLegacyRunOnLegacyJobAfterDistributedUpgrade(t *testing.T) {
	repository := newMemoryPublicationRunRepository("team-a")
	jobs := &scriptedPublicationJobClient{results: []publicationJobResult{{status: PublicationJobStatus{Phase: PublicationJobPending}}}}
	options := publicationControllerOptions()
	options.DistributedEnabled = true
	options.PartitionCount = 8
	options.MaxParallelism = 2
	controller, err := NewController(repository, jobs, options)
	if err != nil {
		t.Fatalf("new controller: %v", err)
	}
	request := publicationReconcileRequest("publication-legacy-upgrade")
	repository.run = &domain.DatasetPublicationRun{ID: request.RunID, DatasetID: request.DatasetID, DatasetVersionID: request.DatasetVersionID, ExecutionMode: domain.DatasetPublicationExecutionLegacy, State: domain.DatasetVersionPacking}
	if _, err := controller.Reconcile(context.Background(), request); err != nil {
		t.Fatalf("reconcile persisted legacy run: %v", err)
	}
	specs := jobs.recordedSpecs()
	if len(specs) != 1 || specs[0].ExecutionPhase() != PublicationExecutionLegacy || strings.HasSuffix(specs[0].Name(), "-finalize") {
		t.Fatalf("legacy run was reinterpreted after upgrade: %+v", specs)
	}
}

func TestControllerEnsureIsIdempotentAndJobNameIsStableDNSLabel(t *testing.T) {
	repository := newMemoryPublicationRunRepository("team-a")
	jobs := &scriptedPublicationJobClient{results: []publicationJobResult{
		{status: PublicationJobStatus{Phase: PublicationJobPending}},
		{status: PublicationJobStatus{Phase: PublicationJobPending}},
	}}
	controller, err := NewController(repository, jobs, publicationControllerOptions())
	if err != nil {
		t.Fatal(err)
	}
	request := publicationReconcileRequest("Publication.Run_With+Mixed.Case")
	for attempt := 0; attempt < 2; attempt++ {
		run, reconcileErr := controller.Reconcile(context.Background(), request)
		if reconcileErr != nil || run.State != domain.DatasetVersionStabilizing {
			t.Fatalf("reconcile %d run=%+v err=%v", attempt+1, run, reconcileErr)
		}
	}
	specs := jobs.recordedSpecs()
	if len(specs) != 2 || specs[0].Name() != specs[1].Name() {
		t.Fatalf("job names are not idempotent: %+v", specs)
	}
	name := specs[0].Name()
	if len(name) > 63 || !regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?$`).MatchString(name) {
		t.Fatalf("job name %q is not a DNS label", name)
	}
	derived, err := PublicationJobName(request.RunID)
	if err != nil || derived != name {
		t.Fatalf("PublicationJobName()=%q err=%v, want %q", derived, err, name)
	}
	if repository.ensureCalls != 2 || repository.claimCalls != 1 {
		t.Fatalf("ensure calls=%d claim calls=%d, want 2/1", repository.ensureCalls, repository.claimCalls)
	}
}

func TestControllerJobSpecIsImmutableCPUOnlyAndLowPriority(t *testing.T) {
	repository := newMemoryPublicationRunRepository("team-a")
	jobs := &scriptedPublicationJobClient{results: []publicationJobResult{{
		status: PublicationJobStatus{Phase: PublicationJobPending},
	}}}
	options := publicationControllerOptions()
	options.IRSARoleTRN = "trn:iam::2103446203:role/tos-rw"
	controller, err := NewController(repository, jobs, options)
	if err != nil {
		t.Fatal(err)
	}
	request := publicationReconcileRequest("publication-resources")
	if _, err := controller.Reconcile(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	spec := jobs.recordedSpecs()[0]
	if spec.RunID() != request.RunID || spec.DatasetID() != request.DatasetID || spec.DatasetVersionID() != request.DatasetVersionID {
		t.Fatalf("job spec identity is not bound to publication: %+v", spec)
	}
	if spec.Namespace() != options.Namespace || spec.Version() != request.Version || spec.SchemaVersion() != request.SchemaVersion || spec.SourceRoot() != request.SourceRoot || spec.SourceIndex() != request.SourceIndex {
		t.Fatalf("publication source identity was not preserved: namespace=%q version=%q schema=%q root=%q index=%q", spec.Namespace(), spec.Version(), spec.SchemaVersion(), spec.SourceRoot(), spec.SourceIndex())
	}
	if spec.SourceBucket() != options.SourceBucket || spec.TargetBucket() != options.TargetBucket || spec.TOSEndpoint() != options.TOSEndpoint || spec.TOSRegion() != options.TOSRegion {
		t.Fatalf("publication storage endpoint was not preserved")
	}
	if spec.Image() != options.Image || spec.ServiceAccountName() != options.ServiceAccountName {
		t.Fatalf("image=%q serviceAccount=%q", spec.Image(), spec.ServiceAccountName())
	}
	if spec.IRSARoleTRN() != options.IRSARoleTRN {
		t.Fatalf("IRSA role TRN=%q, want %q", spec.IRSARoleTRN(), options.IRSARoleTRN)
	}
	requests := spec.Resources().Requests()
	limits := spec.Resources().Limits()
	if !reflect.DeepEqual(requests, map[string]string{"cpu": options.CPURequest, "memory": options.MemoryRequest}) {
		t.Fatalf("resource requests=%v", requests)
	}
	if !reflect.DeepEqual(limits, map[string]string{"cpu": options.CPULimit, "memory": options.MemoryLimit}) {
		t.Fatalf("resource limits=%v", limits)
	}
	for key := range requests {
		if strings.Contains(strings.ToLower(key), "gpu") {
			t.Fatalf("publication Job requested GPU resource %q", key)
		}
	}
	for key := range limits {
		if strings.Contains(strings.ToLower(key), "gpu") {
			t.Fatalf("publication Job limited GPU resource %q", key)
		}
	}
	if spec.QueueName() != options.QueueName || spec.PriorityClassName() != options.PriorityClassName {
		t.Fatalf("queue=%q priority=%q", spec.QueueName(), spec.PriorityClassName())
	}
	labels := spec.Labels()
	if labels["kueue.x-k8s.io/queue-name"] != options.QueueName {
		t.Fatalf("Kueue queue label=%q", labels["kueue.x-k8s.io/queue-name"])
	}
	if spec.BackoffLimit() != options.JobBackoffLimit {
		t.Fatalf("job backoff limit=%d, want %d", spec.BackoffLimit(), options.JobBackoffLimit)
	}
	if spec.ActiveDeadline() != options.JobActiveDeadline || spec.TTLAfterFinished() != options.JobTTLAfterFinished || spec.InternalPrefix() != options.InternalPrefix {
		t.Fatalf("job lifecycle/internal prefix=%s/%s/%q", spec.ActiveDeadline(), spec.TTLAfterFinished(), spec.InternalPrefix())
	}
	if spec.ImagePullPolicy() != options.ImagePullPolicy || spec.WorkingDirectory() != options.WorkingDirectory {
		t.Fatalf("image pull policy=%q working directory=%q", spec.ImagePullPolicy(), spec.WorkingDirectory())
	}
	if !reflect.DeepEqual(spec.NodeSelector(), options.NodeSelector) || !reflect.DeepEqual(spec.PreferredNodeSelector(), options.PreferredNodeSelector) || !reflect.DeepEqual(spec.Tolerations(), options.Tolerations) {
		t.Fatalf("placement was not preserved: node=%v preferred=%v tolerations=%v", spec.NodeSelector(), spec.PreferredNodeSelector(), spec.Tolerations())
	}

	requests["nvidia.com/gpu"] = "8"
	limits["cpu"] = "999"
	labels["kueue.x-k8s.io/queue-name"] = "high-priority"
	nodes := spec.NodeSelector()
	nodes["nvidia.com/gpu.present"] = "true"
	preferred := spec.PreferredNodeSelector()
	preferred["node-role"] = "gpu"
	tolerations := spec.Tolerations()
	tolerations[0].Value = "gpu"
	if _, exists := spec.Resources().Requests()["nvidia.com/gpu"]; exists || spec.Resources().Limits()["cpu"] != options.CPULimit || spec.Labels()["kueue.x-k8s.io/queue-name"] != options.QueueName || spec.NodeSelector()["nvidia.com/gpu.present"] != "" || spec.PreferredNodeSelector()["node-role"] != "cpu" || spec.Tolerations()[0].Value != "cpu" {
		t.Fatal("JobSpec getters exposed mutable internal state")
	}
}

func TestControllerRetriesTransientFailureWithExponentialBackoff(t *testing.T) {
	repository := newMemoryPublicationRunRepository("team-a")
	sensitive := errors.New("AK=test-ak SK=test-sk tos://user:password@internal-bucket/private")
	jobs := &scriptedPublicationJobClient{results: []publicationJobResult{
		{err: sensitive}, {err: sensitive}, {status: PublicationJobStatus{Phase: PublicationJobPending}},
	}}
	var waited []time.Duration
	options := publicationControllerOptions()
	options.Wait = func(_ context.Context, delay time.Duration) error {
		waited = append(waited, delay)
		return nil
	}
	controller, err := NewController(repository, jobs, options)
	if err != nil {
		t.Fatal(err)
	}
	run, err := controller.Reconcile(context.Background(), publicationReconcileRequest("publication-transient"))
	if err != nil || run.State != domain.DatasetVersionStabilizing {
		t.Fatalf("reconcile run=%+v err=%v", run, err)
	}
	if got, want := waited, []time.Duration{time.Second, 2 * time.Second}; !reflect.DeepEqual(got, want) {
		t.Fatalf("retry backoff=%v, want %v", got, want)
	}
	if jobs.callCount() != options.ClientMaxAttempts {
		t.Fatalf("job attempts=%d, want %d", jobs.callCount(), options.ClientMaxAttempts)
	}
}

func TestControllerStopsAtRetryLimitAndSanitizesFailure(t *testing.T) {
	repository := newMemoryPublicationRunRepository("team-a")
	sensitive := errors.New("access_key=test-ak secret_key=test-sk tos://internal/private")
	jobs := &scriptedPublicationJobClient{results: []publicationJobResult{
		{err: sensitive}, {err: sensitive}, {err: sensitive}, {err: errors.New("must not run")},
	}}
	options := publicationControllerOptions()
	controller, err := NewController(repository, jobs, options)
	if err != nil {
		t.Fatal(err)
	}
	run, err := controller.Reconcile(context.Background(), publicationReconcileRequest("publication-exhausted"))
	if !errors.Is(err, ErrPublicationJobUnavailable) {
		t.Fatalf("retry exhaustion error=%v, want ErrPublicationJobUnavailable", err)
	}
	if run.State != domain.DatasetVersionStabilizing || jobs.callCount() != options.ClientMaxAttempts {
		t.Fatalf("failed run=%+v attempts=%d", run, jobs.callCount())
	}
	assertSanitizedControllerError(t, err)
}

func TestControllerMarksTerminalJobFailureWithoutLeakingDetails(t *testing.T) {
	repository := newMemoryPublicationRunRepository("team-a")
	jobs := &scriptedPublicationJobClient{results: []publicationJobResult{{
		status: PublicationJobStatus{
			Phase: PublicationJobFailed,
			Progress: PublicationProgress{
				TotalPartitions: 2, CompletedPartitions: 1, FailedPartitions: 1,
				SourceObjectCount: 4, ProcessedObjectCount: 3, FailedObjectCount: 1,
			},
		},
	}}}
	controller, err := NewController(repository, jobs, publicationControllerOptions())
	if err != nil {
		t.Fatal(err)
	}
	run, err := controller.Reconcile(context.Background(), publicationReconcileRequest("publication-job-failed"))
	if !errors.Is(err, ErrPublicationJobFailed) || run.State != domain.DatasetVersionFailed {
		t.Fatalf("terminal failure run=%+v err=%v", run, err)
	}
	assertSanitizedControllerError(t, err)
}

func TestControllerEnforcesTenantBoundaryBeforeCreatingJob(t *testing.T) {
	repository := newMemoryPublicationRunRepository("team-a")
	repository.scopeError = errors.New("postgres://admin:secret@internal-db/runs?AK=test-ak")
	jobs := &scriptedPublicationJobClient{}
	controller, err := NewController(repository, jobs, publicationControllerOptions())
	if err != nil {
		t.Fatal(err)
	}
	request := publicationReconcileRequest("publication-hidden")
	request.TenantID = "team-b"
	_, err = controller.Reconcile(context.Background(), request)
	if !errors.Is(err, ErrPublicationControllerUnavailable) {
		t.Fatalf("cross-tenant error=%v, want ErrPublicationControllerUnavailable", err)
	}
	if jobs.callCount() != 0 {
		t.Fatalf("cross-tenant request created %d jobs", jobs.callCount())
	}
	assertSanitizedControllerError(t, err)
}

func TestControllerReturnsContextCancellationWithoutRetrying(t *testing.T) {
	repository := newMemoryPublicationRunRepository("team-a")
	jobs := &scriptedPublicationJobClient{results: []publicationJobResult{{err: errors.New("temporary failure")}}}
	ctx, cancel := context.WithCancel(context.Background())
	options := publicationControllerOptions()
	options.Wait = func(waitCtx context.Context, _ time.Duration) error {
		cancel()
		<-waitCtx.Done()
		return waitCtx.Err()
	}
	controller, err := NewController(repository, jobs, options)
	if err != nil {
		t.Fatal(err)
	}
	_, err = controller.Reconcile(ctx, publicationReconcileRequest("publication-canceled"))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled reconcile error=%v, want context.Canceled", err)
	}
	if jobs.callCount() != 1 {
		t.Fatalf("canceled reconcile attempts=%d, want 1", jobs.callCount())
	}
}

func TestControllerDoesNotReportFailureAfterConcurrentCompletion(t *testing.T) {
	for _, test := range []struct {
		name      string
		results   []publicationJobResult
		wantState domain.DatasetVersionState
		wantError error
	}{
		{
			name: "client retry exhaustion",
			results: []publicationJobResult{
				{err: errors.New("temporary")}, {err: errors.New("temporary")}, {err: errors.New("temporary")},
			},
			wantState: domain.DatasetVersionStabilizing,
			wantError: ErrPublicationJobUnavailable,
		},
		{
			name: "terminal job status", results: []publicationJobResult{{status: PublicationJobStatus{Phase: PublicationJobFailed}}},
			wantState: domain.DatasetVersionReady,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository := newMemoryPublicationRunRepository("team-a")
			repository.completeBeforeFailure = true
			jobs := &scriptedPublicationJobClient{results: test.results}
			controller, err := NewController(repository, jobs, publicationControllerOptions())
			if err != nil {
				t.Fatal(err)
			}
			run, err := controller.Reconcile(context.Background(), publicationReconcileRequest("publication-raced-ready"))
			if !errors.Is(err, test.wantError) || run.State != test.wantState {
				t.Fatalf("concurrent completion run=%+v err=%v want state=%s err=%v", run, err, test.wantState, test.wantError)
			}
		})
	}
}

func TestNewControllerRejectsTypedNilDependencies(t *testing.T) {
	var nilRepository *memoryPublicationRunRepository
	var nilJobs *scriptedPublicationJobClient
	validRepository := newMemoryPublicationRunRepository("team-a")
	validJobs := &scriptedPublicationJobClient{}
	for _, test := range []struct {
		name       string
		repository PublicationRunRepository
		jobs       PublicationJobClient
	}{
		{name: "repository", repository: nilRepository, jobs: validJobs},
		{name: "job client", repository: validRepository, jobs: nilJobs},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewController(test.repository, test.jobs, publicationControllerOptions()); !errors.Is(err, ErrInvalidPublicationController) {
				t.Fatalf("NewController error=%v, want ErrInvalidPublicationController", err)
			}
		})
	}
}

func TestControllerMapsIntermediateAndInvalidJobStatuses(t *testing.T) {
	for _, test := range []struct {
		name      string
		status    PublicationJobStatus
		wantState domain.DatasetVersionState
		wantError error
	}{
		{
			name: "validating",
			status: PublicationJobStatus{Phase: PublicationJobValidating, Progress: PublicationProgress{
				TotalPartitions: 2, SourceObjectCount: 4,
			}},
			wantState: domain.DatasetVersionValidating,
		},
		{
			name: "packing",
			status: PublicationJobStatus{Phase: PublicationJobPacking, Progress: PublicationProgress{
				TotalPartitions: 2, CompletedPartitions: 1, SourceObjectCount: 4, ProcessedObjectCount: 2,
			}},
			wantState: domain.DatasetVersionPacking,
		},
		{
			name:      "incomplete success",
			status:    PublicationJobStatus{Phase: PublicationJobSucceeded, Progress: PublicationProgress{TotalPartitions: 2, CompletedPartitions: 1}},
			wantState: domain.DatasetVersionStabilizing,
			wantError: ErrPublicationJobUnavailable,
		},
		{
			name:      "success without receipt",
			status:    PublicationJobStatus{Phase: PublicationJobSucceeded, Progress: completedPublicationProgress()},
			wantState: domain.DatasetVersionStabilizing,
			wantError: ErrPublicationJobUnavailable,
		},
		{
			name:      "unknown phase",
			status:    PublicationJobStatus{Phase: PublicationJobPhase("LEAKING_INTERNAL_URI")},
			wantState: domain.DatasetVersionStabilizing,
			wantError: ErrPublicationControllerUnavailable,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository := newMemoryPublicationRunRepository("team-a")
			jobs := &scriptedPublicationJobClient{results: []publicationJobResult{{status: test.status}}}
			controller, err := NewController(repository, jobs, publicationControllerOptions())
			if err != nil {
				t.Fatal(err)
			}
			run, err := controller.Reconcile(context.Background(), publicationReconcileRequest("publication-phase-"+strings.ReplaceAll(test.name, " ", "-")))
			if run.State != test.wantState || !errors.Is(err, test.wantError) {
				t.Fatalf("phase run=%+v err=%v, want state=%s err=%v", run, err, test.wantState, test.wantError)
			}
		})
	}
}

func TestControllerRejectsInvalidRequestsAndConfiguration(t *testing.T) {
	if _, err := PublicationJobName("bad/run"); !errors.Is(err, ErrInvalidPublicationControllerRequest) {
		t.Fatalf("invalid job name error=%v", err)
	}
	var nilController *Controller
	if _, err := nilController.Reconcile(context.Background(), publicationReconcileRequest("publication-nil-controller")); !errors.Is(err, ErrInvalidPublicationController) {
		t.Fatalf("nil controller error=%v", err)
	}

	repository := newMemoryPublicationRunRepository("team-a")
	jobs := &scriptedPublicationJobClient{}
	controller, err := NewController(repository, jobs, publicationControllerOptions())
	if err != nil {
		t.Fatal(err)
	}
	invalidRequest := publicationReconcileRequest("bad/run")
	if _, err := controller.Reconcile(context.Background(), invalidRequest); !errors.Is(err, ErrInvalidPublicationControllerRequest) {
		t.Fatalf("invalid request error=%v", err)
	}
	unsafeSource := publicationReconcileRequest("publication-unsafe-source")
	unsafeSource.SourceIndex = "../private/index.pkl"
	if _, err := controller.Reconcile(context.Background(), unsafeSource); !errors.Is(err, ErrInvalidPublicationControllerRequest) {
		t.Fatalf("unsafe publication source error=%v", err)
	}
	overlappingSource := publicationReconcileRequest("publication-overlapping-source")
	overlappingSource.SourceRoot = domain.DefaultDatasetInternalPrefix
	if _, err := controller.Reconcile(context.Background(), overlappingSource); !errors.Is(err, ErrInvalidPublicationControllerRequest) {
		t.Fatalf("overlapping publication source error=%v", err)
	}
	anonymous := publicationReconcileRequest("publication-anonymous")
	anonymous.TenantID = ""
	if _, err := controller.Reconcile(context.Background(), anonymous); !errors.Is(err, ErrInvalidPublicationControllerRequest) {
		t.Fatalf("anonymous request error=%v", err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := controller.Reconcile(canceled, publicationReconcileRequest("publication-pre-canceled")); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-canceled request error=%v", err)
	}

	base := publicationControllerOptions()
	invalidOptions := []ControllerOptions{
		func() ControllerOptions { value := base; value.Namespace = ""; return value }(),
		func() ControllerOptions {
			value := base
			value.Image = "registry.example/dataset-publisher:latest"
			return value
		}(),
		func() ControllerOptions {
			value := base
			value.ServiceAccountName = "Bad_Service_Account"
			return value
		}(),
		func() ControllerOptions {
			value := base
			value.IRSARoleTRN = "trn:iam::2103446203:user/not-a-role"
			return value
		}(),
		func() ControllerOptions { value := base; value.CPURequest = "500 millicpu"; return value }(),
		func() ControllerOptions { value := base; value.ClientMaxAttempts = 0; return value }(),
		func() ControllerOptions { value := base; value.JobBackoffLimit = -1; return value }(),
		func() ControllerOptions { value := base; value.JobActiveDeadline = 0; return value }(),
		func() ControllerOptions { value := base; value.JobTTLAfterFinished = 31 * 24 * time.Hour; return value }(),
		func() ControllerOptions { value := base; value.JobTTLAfterFinished += time.Millisecond; return value }(),
		func() ControllerOptions { value := base; value.InitialRetryBackoff = -time.Second; return value }(),
		func() ControllerOptions { value := base; value.SourceBucket = "tos://bucket"; return value }(),
		func() ControllerOptions { value := base; value.TOSEndpoint = "https://tos.example.com"; return value }(),
		func() ControllerOptions { value := base; value.TOSEndpoint = "objects.example.com"; return value }(),
		func() ControllerOptions {
			value := base
			value.TOSEndpoint = "tos-cn-shanghai.ivolces.com.attacker.example"
			return value
		}(),
		func() ControllerOptions {
			value := base
			value.TOSEndpoint = "tos-cn-beijing.ivolces.com"
			return value
		}(),
		func() ControllerOptions {
			value := base
			value.NodeSelector = map[string]string{"": "cpu"}
			return value
		}(),
	}
	for index, options := range invalidOptions {
		if _, err := NewController(repository, jobs, options); !errors.Is(err, ErrInvalidPublicationController) {
			t.Errorf("invalid options %d error=%v", index, err)
		}
	}
}

func TestControllerHandlesClaimLossAndDependencyErrors(t *testing.T) {
	t.Run("claim loss", func(t *testing.T) {
		repository := newMemoryPublicationRunRepository("team-a")
		repository.loseClaim = true
		jobs := &scriptedPublicationJobClient{}
		controller, err := NewController(repository, jobs, publicationControllerOptions())
		if err != nil {
			t.Fatal(err)
		}
		run, err := controller.Reconcile(context.Background(), publicationReconcileRequest("publication-claim-lost"))
		if err != nil || run.State != domain.DatasetVersionStabilizing || jobs.callCount() != 0 {
			t.Fatalf("claim loser run=%+v jobs=%d err=%v", run, jobs.callCount(), err)
		}
	})

	for _, test := range []struct {
		name       string
		claimError error
		casError   error
		status     PublicationJobStatus
	}{
		{name: "claim error", claimError: errors.New("AK=test-ak tos://internal/claim")},
		{name: "CAS error", casError: errors.New("SK=test-sk tos://internal/cas"), status: PublicationJobStatus{Phase: PublicationJobValidating}},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository := newMemoryPublicationRunRepository("team-a")
			repository.claimError = test.claimError
			repository.casError = test.casError
			jobs := &scriptedPublicationJobClient{results: []publicationJobResult{{status: test.status}}}
			controller, err := NewController(repository, jobs, publicationControllerOptions())
			if err != nil {
				t.Fatal(err)
			}
			_, err = controller.Reconcile(context.Background(), publicationReconcileRequest("publication-dependency-error"))
			if !errors.Is(err, ErrPublicationControllerUnavailable) {
				t.Fatalf("dependency error=%v", err)
			}
			assertSanitizedControllerError(t, err)
		})
	}
}

func TestControllerDefaultClockWaitAndContextErrors(t *testing.T) {
	repository := newMemoryPublicationRunRepository("team-a")
	jobs := &scriptedPublicationJobClient{results: []publicationJobResult{
		{err: errors.New("temporary")}, {status: PublicationJobStatus{Phase: PublicationJobPending}},
	}}
	options := publicationControllerOptions()
	options.ClientMaxAttempts = 2
	options.InitialRetryBackoff = 0
	options.MaximumRetryBackoff = 0
	options.Now = nil
	options.Wait = nil
	controller, err := NewController(repository, jobs, options)
	if err != nil {
		t.Fatal(err)
	}
	if run, err := controller.Reconcile(context.Background(), publicationReconcileRequest("publication-defaults")); err != nil || run.State != domain.DatasetVersionStabilizing {
		t.Fatalf("default controller run=%+v err=%v", run, err)
	}

	waitCtx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := waitForPublicationRetry(waitCtx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatalf("default wait cancellation=%v", err)
	}
	if got := nextPublicationRetryDelay(3*time.Second, 4*time.Second); got != 4*time.Second {
		t.Fatalf("capped backoff=%v", got)
	}

	deadlineRepository := newMemoryPublicationRunRepository("team-a")
	deadlineJobs := &scriptedPublicationJobClient{results: []publicationJobResult{{err: context.DeadlineExceeded}}}
	deadlineController, err := NewController(deadlineRepository, deadlineJobs, publicationControllerOptions())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := deadlineController.Reconcile(context.Background(), publicationReconcileRequest("publication-deadline")); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("dependency deadline error=%v", err)
	}
}

func assertSanitizedControllerError(t *testing.T, err error) {
	t.Helper()
	message := strings.ToLower(err.Error())
	for _, forbidden := range []string{"test-ak", "test-sk", "secret", "password", "tos://", "postgres://", "internal-db", "internal-bucket", "access_key", "secret_key"} {
		if strings.Contains(message, forbidden) {
			t.Fatalf("controller error leaked %q: %v", forbidden, err)
		}
	}
}

type memoryPublicationRunRepository struct {
	mu                    sync.Mutex
	tenantID              string
	run                   *domain.DatasetPublicationRun
	transitions           []domain.DatasetVersionState
	ensureCalls           int
	claimCalls            int
	scopeError            error
	completeBeforeFailure bool
	loseClaim             bool
	claimError            error
	casError              error
}

func newMemoryPublicationRunRepository(tenantID string) *memoryPublicationRunRepository {
	return &memoryPublicationRunRepository{tenantID: tenantID}
}

func (repository *memoryPublicationRunRepository) EnsureDatasetPublicationRun(_ context.Context, tenantID string, superAdmin bool, run domain.DatasetPublicationRun) (domain.DatasetPublicationRun, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	repository.ensureCalls++
	if !superAdmin && tenantID != repository.tenantID {
		if repository.scopeError != nil {
			return domain.DatasetPublicationRun{}, repository.scopeError
		}
		return domain.DatasetPublicationRun{}, errors.New("publication run not found")
	}
	if repository.run == nil {
		copy := run
		repository.run = &copy
		return copy, nil
	}
	if repository.run.ID != run.ID || repository.run.DatasetID != run.DatasetID || repository.run.DatasetVersionID != run.DatasetVersionID {
		return domain.DatasetPublicationRun{}, errors.New("publication identity conflict")
	}
	return *repository.run, nil
}

func (repository *memoryPublicationRunRepository) ClaimDatasetPublicationRun(_ context.Context, tenantID string, superAdmin bool, datasetID, versionID, runID string, _ time.Time) (domain.DatasetPublicationRun, bool, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	repository.claimCalls++
	if repository.claimError != nil {
		return domain.DatasetPublicationRun{}, false, repository.claimError
	}
	if repository.run == nil || (!superAdmin && tenantID != repository.tenantID) || repository.run.ID != runID || repository.run.DatasetID != datasetID || repository.run.DatasetVersionID != versionID {
		return domain.DatasetPublicationRun{}, false, errors.New("publication run not found")
	}
	if repository.run.State != domain.DatasetVersionDiscovering {
		return *repository.run, false, nil
	}
	updated := *repository.run
	updated.State = domain.DatasetVersionStabilizing
	repository.run = &updated
	repository.transitions = append(repository.transitions, updated.State)
	if repository.loseClaim {
		return updated, false, nil
	}
	return updated, true, nil
}

func (repository *memoryPublicationRunRepository) CompareAndSwapDatasetPublicationRun(_ context.Context, tenantID string, superAdmin bool, expectedState domain.DatasetVersionState, next domain.DatasetPublicationRun, _ time.Time) (domain.DatasetPublicationRun, bool, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if repository.casError != nil {
		return domain.DatasetPublicationRun{}, false, repository.casError
	}
	if repository.run == nil || (!superAdmin && tenantID != repository.tenantID) || repository.run.ID != next.ID || repository.run.DatasetID != next.DatasetID || repository.run.DatasetVersionID != next.DatasetVersionID {
		return domain.DatasetPublicationRun{}, false, errors.New("publication run not found")
	}
	if repository.run.State != expectedState {
		return *repository.run, false, nil
	}
	if repository.completeBeforeFailure && next.State == domain.DatasetVersionFailed {
		completed := *repository.run
		completed.State = domain.DatasetVersionReady
		repository.run = &completed
		repository.transitions = append(repository.transitions, completed.State)
		return completed, false, nil
	}
	copy := next
	repository.run = &copy
	repository.transitions = append(repository.transitions, copy.State)
	return copy, true, nil
}

func (repository *memoryPublicationRunRepository) FinalizeDatasetPublicationRun(_ context.Context, tenantID string, superAdmin bool, expectedState domain.DatasetVersionState, next domain.DatasetPublicationRun, receipt domain.DatasetPublicationReceipt, internalPrefix string, _ time.Time) (domain.DatasetPublicationRun, bool, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if repository.casError != nil {
		return domain.DatasetPublicationRun{}, false, repository.casError
	}
	if repository.run == nil || (!superAdmin && tenantID != repository.tenantID) || repository.run.ID != next.ID || repository.run.DatasetID != next.DatasetID || repository.run.DatasetVersionID != next.DatasetVersionID {
		return domain.DatasetPublicationRun{}, false, errors.New("publication run not found")
	}
	if repository.run.State != expectedState {
		return *repository.run, false, nil
	}
	if next.State != domain.DatasetVersionReady || receipt.DatasetID != next.DatasetID || receipt.DatasetVersionID != next.DatasetVersionID || receipt.ValidateWithInternalPrefix(internalPrefix) != nil {
		return *repository.run, false, errors.New("publication receipt conflict")
	}
	copy := next
	repository.run = &copy
	repository.transitions = append(repository.transitions, copy.State)
	return copy, true, nil
}

func (repository *memoryPublicationRunRepository) states() []domain.DatasetVersionState {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	return append([]domain.DatasetVersionState(nil), repository.transitions...)
}

type publicationJobResult struct {
	status PublicationJobStatus
	err    error
}

type scriptedPublicationJobClient struct {
	mu      sync.Mutex
	results []publicationJobResult
	specs   []PublicationJobSpec
	calls   int
}

func (client *scriptedPublicationJobClient) EnsurePublicationJob(_ context.Context, spec PublicationJobSpec) (PublicationJobStatus, error) {
	client.mu.Lock()
	defer client.mu.Unlock()
	client.specs = append(client.specs, spec)
	index := client.calls
	client.calls++
	if len(client.results) == 0 {
		return PublicationJobStatus{}, errors.New("no scripted result")
	}
	if index >= len(client.results) {
		index = len(client.results) - 1
	}
	return client.results[index].status, client.results[index].err
}

func (client *scriptedPublicationJobClient) callCount() int {
	client.mu.Lock()
	defer client.mu.Unlock()
	return client.calls
}

func (client *scriptedPublicationJobClient) recordedSpecs() []PublicationJobSpec {
	client.mu.Lock()
	defer client.mu.Unlock()
	return append([]PublicationJobSpec(nil), client.specs...)
}
