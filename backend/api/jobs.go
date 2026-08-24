package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
	"ray-train-platform-backend/httpapi"
	"ray-train-platform-backend/k8s"
	"ray-train-platform-backend/objectstore"
	"ray-train-platform-backend/observability"
	"ray-train-platform-backend/repositories"
)

type JobRepository interface {
	Create(context.Context, *domain.TrainingJob, string) error
	Get(context.Context, string, string) (*domain.TrainingJob, error)
	List(context.Context, domain.JobFilter) (domain.Page[domain.TrainingJob], error)
	SetDesiredState(context.Context, string, string, domain.DesiredState) error
	EnsureIdentity(context.Context, auth.Principal) error
}

type globalJobReader interface {
	GetByID(context.Context, string) (*domain.TrainingJob, error)
}

type Handler struct {
	repository             JobRepository
	logs                   LogProvider
	metrics                MetricsProvider
	experiments            ExperimentProvider
	allowAnonymous         bool
	imageAllowlist         []string
	gitAllowlist           []string
	workspaces             WorkspaceStore
	kubernetes             *k8s.Client
	workspaceImage         string
	rayVersion             string
	serviceAccount         string
	imagePullSecrets       []string
	platformNamespace      string
	idcClaim               string
	idcMountPath           string
	clusterQueue           string
	admin                  AdminStore
	gpuAllocations         GPUAllocationStore
	quota                  QuotaStore
	workspacePepper        []byte
	trainingNodeSelector   map[string]string
	workspaceUpstream      func(*domain.DevWorkspace) string
	dashboardUpstream      func(context.Context, *domain.TrainingJob) (string, error)
	images                 ImageStore
	gitCredentials         GitCredentialStore
	storageAssets          StorageAssetStore
	dataSpaces             DataSpaceStore
	dataSpacesEnabled      bool
	dataSpacesFSXAttrs     string
	dataSpacesCapacity     string
	dataSpacesPublicRoot   string
	idcDataSpacesEnabled   bool
	idcDataSpacesCapacity  string
	idcDataSpaceSources    map[domain.DataSpaceID]k8s.IDCDataMountSource
	directoryLister        objectstore.DirectoryLister
	directoryInitializer   objectstore.PersonalDataDirectoryInitializer
	dataObjectStore        objectstore.DataSpaceStore
	workspaceSnapshotStore objectstore.WorkspaceSnapshotStore
	workspaceSnapshots     WorkspaceSnapshotRepository
	artifactLister         objectstore.ArtifactLister
	artifactReader         objectstore.ArtifactReader
	gitCredentialTester    GitCredentialTester
	gitRefResolver         GitRefResolver
	newID                  func() (string, error)
	submission             *SubmissionService
	localCache             LocalCachePolicy
	mlflowDashboardEnabled bool
	mlflowDashboardStore   MLflowDashboardStore
	mlflowTrackingURL      string
	mlflowPublicOrigin     string
	mlflowDashboardPepper  []byte
	mlflowDashboardTTL     time.Duration
	mlflowDashboardNow     func() time.Time
	mlflowDashboardRandom  io.Reader
}

type LogProvider interface {
	QueryJobLogs(context.Context, string, int) ([]observability.LogLine, error)
}

type MetricsProvider interface {
	QueryJobMetrics(context.Context, string, time.Duration) (observability.JobMetrics, error)
}

type ExperimentProvider interface {
	QueryJobExperiment(context.Context, string, string) (observability.JobExperiment, error)
	ListTenantExperiments(context.Context, string, string, int) (observability.ExperimentCatalog, error)
}

type Options struct {
	AllowAnonymous          bool
	Logs                    LogProvider
	Metrics                 MetricsProvider
	Experiments             ExperimentProvider
	ImageAllowlist          []string
	GitAllowlist            []string
	Workspaces              WorkspaceStore
	Kubernetes              *k8s.Client
	WorkspaceImage          string
	RayVersion              string
	ServiceAccount          string
	ImagePullSecrets        []string
	PlatformNamespace       string
	IDCClaim                string
	IDCMountPath            string
	KueueClusterQueue       string
	Admin                   AdminStore
	GPUAllocations          GPUAllocationStore
	Quota                   QuotaStore
	WorkspacePepper         []byte
	TrainingNodeSelector    map[string]string
	Images                  ImageStore
	GitCredentials          GitCredentialStore
	StorageAssets           StorageAssetStore
	DataSpaces              DataSpaceStore
	DataSpacesEnabled       bool
	DataSpacesFSXAttributes string
	DataSpacesMountCapacity string
	// DataSpacesPublicRoot is deployment-owned and never comes from an HTTP
	// request. It controls the public dataset root during an explicit data
	// migration and defaults to ray-train/public/.
	DataSpacesPublicRoot       string
	IDCDataSpacesEnabled       bool
	IDCDataSpacesMountCapacity string
	IDCDataSpaceSources        map[domain.DataSpaceID]k8s.IDCDataMountSource
	DirectoryLister            objectstore.DirectoryLister
	DirectoryInitializer       objectstore.PersonalDataDirectoryInitializer
	DataObjectStore            objectstore.DataSpaceStore
	WorkspaceSnapshotStore     objectstore.WorkspaceSnapshotStore
	WorkspaceSnapshots         WorkspaceSnapshotRepository
	ArtifactLister             objectstore.ArtifactLister
	ArtifactReader             objectstore.ArtifactReader
	GitCredentialTester        GitCredentialTester
	GitRefResolver             GitRefResolver
	MLflowDashboardEnabled     bool
	MLflowDashboardStore       MLflowDashboardStore
	MLflowTrackingURL          string
	MLflowPublicOrigin         string
	MLflowDashboardPepper      []byte
	MLflowDashboardSessionTTL  time.Duration
	MLflowDashboardNow         func() time.Time
	MLflowDashboardRandom      io.Reader
	LocalCache                 LocalCachePolicy
}

func NewHandler(repository JobRepository, options Options) *Handler {
	idcSources := make(map[domain.DataSpaceID]k8s.IDCDataMountSource, len(options.IDCDataSpaceSources))
	for space, source := range options.IDCDataSpaceSources {
		idcSources[space] = source
	}
	handler := &Handler{repository: repository, logs: options.Logs, metrics: options.Metrics, experiments: options.Experiments, allowAnonymous: options.AllowAnonymous, imageAllowlist: append([]string(nil), options.ImageAllowlist...), gitAllowlist: append([]string(nil), options.GitAllowlist...), workspaces: options.Workspaces, kubernetes: options.Kubernetes, workspaceImage: options.WorkspaceImage, rayVersion: options.RayVersion, serviceAccount: options.ServiceAccount, imagePullSecrets: append([]string(nil), options.ImagePullSecrets...), platformNamespace: strings.TrimSpace(options.PlatformNamespace), idcClaim: options.IDCClaim, idcMountPath: options.IDCMountPath, clusterQueue: options.KueueClusterQueue, admin: options.Admin, gpuAllocations: options.GPUAllocations, quota: options.Quota, workspacePepper: append([]byte(nil), options.WorkspacePepper...), trainingNodeSelector: options.TrainingNodeSelector, images: options.Images, gitCredentials: options.GitCredentials, storageAssets: options.StorageAssets, dataSpaces: options.DataSpaces, dataSpacesEnabled: options.DataSpacesEnabled, dataSpacesFSXAttrs: options.DataSpacesFSXAttributes, dataSpacesCapacity: options.DataSpacesMountCapacity, dataSpacesPublicRoot: strings.TrimSpace(options.DataSpacesPublicRoot), idcDataSpacesEnabled: options.IDCDataSpacesEnabled, idcDataSpacesCapacity: options.IDCDataSpacesMountCapacity, idcDataSpaceSources: idcSources, directoryLister: options.DirectoryLister, directoryInitializer: options.DirectoryInitializer, dataObjectStore: options.DataObjectStore, workspaceSnapshotStore: options.WorkspaceSnapshotStore, workspaceSnapshots: options.WorkspaceSnapshots, artifactLister: options.ArtifactLister, artifactReader: options.ArtifactReader, gitCredentialTester: options.GitCredentialTester, gitRefResolver: options.GitRefResolver, newID: newJobID, mlflowDashboardEnabled: options.MLflowDashboardEnabled, mlflowDashboardStore: options.MLflowDashboardStore, mlflowTrackingURL: strings.TrimSpace(options.MLflowTrackingURL), mlflowPublicOrigin: strings.TrimSpace(options.MLflowPublicOrigin), mlflowDashboardPepper: append([]byte(nil), options.MLflowDashboardPepper...), mlflowDashboardTTL: options.MLflowDashboardSessionTTL, mlflowDashboardNow: options.MLflowDashboardNow, mlflowDashboardRandom: options.MLflowDashboardRandom}
	handler.localCache = cloneLocalCachePolicy(options.LocalCache)
	if handler.mlflowDashboardTTL <= 0 {
		handler.mlflowDashboardTTL = domain.MLflowDashboardSessionTTL
	}
	if handler.mlflowDashboardNow == nil {
		handler.mlflowDashboardNow = time.Now
	}
	if handler.mlflowDashboardRandom == nil {
		handler.mlflowDashboardRandom = rand.Reader
	}
	if handler.gitCredentialTester == nil {
		handler.gitCredentialTester = newHTTPSGitCredentialTester()
	}
	if handler.gitRefResolver == nil {
		handler.gitRefResolver = newHTTPSGitRefResolver()
	}
	if handler.workspaceSnapshotStore == nil {
		handler.workspaceSnapshotStore, _ = options.DataObjectStore.(objectstore.WorkspaceSnapshotStore)
	}
	if handler.workspaceSnapshots == nil {
		handler.workspaceSnapshots, _ = repository.(WorkspaceSnapshotRepository)
	}
	if handler.platformNamespace == "" {
		handler.platformNamespace = "ray-train-platform"
	}
	handler.submission = NewSubmissionService(repository, SubmissionServiceOptions{
		Images:               handler.images,
		ImageAllowlist:       handler.imageAllowlist,
		GitAllowlist:         handler.gitAllowlist,
		ClusterQueue:         handler.clusterQueue,
		StorageAssets:        handler.storageAssets,
		DataSpaces:           handler.dataSpaces,
		DataSpacesEnabled:    handler.dataSpacesEnabled,
		DataSpacesPublicRoot: handler.dataSpacesPublicRoot,
		IDCDataSpacesEnabled: handler.idcDataSpacesEnabled,
		WorkspaceSnapshots:   handler.workspaceSnapshots,
		LocalCache:           handler.localCache,
		EnsureTenantRuntime: func(ctx context.Context, tenantID, namespace, queue, clusterQueue string) error {
			if err := handler.ensureTenantNamespaceAndPullSecrets(ctx, tenantID, namespace); err != nil {
				return err
			}
			if handler.kubernetes == nil {
				return nil
			}
			return handler.kubernetes.EnsureLocalQueue(ctx, namespace, queue, clusterQueue)
		},
		EnsureDataSpaces: func(ctx context.Context, principal auth.Principal) error {
			if !handler.dataSpacesEnabled && !handler.idcDataSpacesEnabled {
				return nil
			}
			return handler.ensureDataSpacesForPrincipal(ctx, principal)
		},
		EnsureOutputDirectory: func(ctx context.Context, principal auth.Principal, mounts domain.ResolvedDataSpaceMounts) error {
			return handler.ensureTrainingOutputDirectory(ctx, principal, mounts)
		},
		NewID: func() (string, error) { return handler.newID() },
	})
	return handler
}

// ensureTrainingOutputDirectory creates the exact server-generated subPath
// before the RayJob is persisted. Kubernetes refuses a PVC subPath that does
// not yet exist; doing it in the control plane prevents a later GPU Pod from
// failing with an opaque container-creation error.
func (h *Handler) ensureTrainingOutputDirectory(ctx context.Context, principal auth.Principal, mounts domain.ResolvedDataSpaceMounts) error {
	output := mounts.Output
	if output == nil {
		return nil
	}
	if output.Space != domain.DataSpaceMyRuns || output.BindingSpace != domain.DataSpaceWorkspace || output.ReadOnly || strings.TrimSpace(output.SubPath) == "" {
		return fmt.Errorf("invalid resolved training output mount")
	}
	if h.dataObjectStore == nil {
		return fmt.Errorf("data object store is not configured")
	}
	spaces, err := h.personalDataSpacesForPrincipal(ctx, principal)
	if err != nil {
		return err
	}
	runs, ok := domain.FindDataSpace(spaces, domain.DataSpaceMyRuns)
	if !ok {
		return fmt.Errorf("personal runs space is not configured")
	}
	relativePath, err := trainingOutputRelativePath(output.SubPath, runs.RootPrefix)
	if err != nil {
		return err
	}
	return h.dataObjectStore.CreateDataDirectory(ctx, runs.RootPrefix, relativePath)
}

// trainingOutputRelativePath maps the server-resolved Kubernetes subPath back
// to the My Runs object root before creating it through the object API. Legacy
// personal PVCs use runs/<job>, while the tenant-root PVC uses the same path
// below its confined physical prefix. Both forms remain controlled entirely by
// the submission service and must resolve below the caller's runs root.
func trainingOutputRelativePath(outputSubPath, runsRoot string) (string, error) {
	outputSubPath, err := domain.NormalizeStorageRelativePath(outputSubPath)
	if err != nil || outputSubPath == "" {
		return "", fmt.Errorf("resolved output is outside personal runs")
	}
	if strings.HasPrefix(outputSubPath, "runs/") {
		return strings.TrimPrefix(outputSubPath, "runs/"), nil
	}

	const platformRoot = "ray-train/"
	physicalRunsRoot := strings.TrimSuffix(strings.TrimSpace(runsRoot), "/")
	if !strings.HasPrefix(physicalRunsRoot, platformRoot) {
		return "", fmt.Errorf("resolved output is outside personal runs")
	}
	physicalRunsSubPath := strings.TrimPrefix(physicalRunsRoot, platformRoot)
	if physicalRunsSubPath == "" || !strings.HasPrefix(outputSubPath, physicalRunsSubPath+"/") {
		return "", fmt.Errorf("resolved output is outside personal runs")
	}
	relativePath := strings.TrimPrefix(outputSubPath, physicalRunsSubPath+"/")
	if _, err := domain.NormalizeStorageRelativePath(relativePath); err != nil || relativePath == "" {
		return "", fmt.Errorf("resolved output is outside personal runs")
	}
	return relativePath, nil
}

// ensureTenantNamespaceAndPullSecrets prepares the Kubernetes resources that
// every tenant workload needs. Both RayJobs and interactive workspaces call it
// so a user may start with either workflow on a newly restored platform.
func (h *Handler) ensureTenantNamespaceAndPullSecrets(ctx context.Context, tenantID, namespace string) error {
	if h.kubernetes == nil {
		return nil
	}
	if err := h.kubernetes.EnsureNamespace(ctx, namespace, tenantID); err != nil {
		return err
	}
	for _, secretName := range h.imagePullSecrets {
		if err := h.kubernetes.EnsureImagePullSecret(ctx, h.platformNamespace, namespace, secretName); err != nil {
			return err
		}
	}
	return nil
}

type WorkspaceStore interface {
	CreateWorkspace(context.Context, *domain.DevWorkspace, int64) error
	GetWorkspace(context.Context, string, string) (*domain.DevWorkspace, error)
	GetWorkspaceByUser(context.Context, string) (*domain.DevWorkspace, error)
	UpdateWorkspaceState(context.Context, string, string, domain.WorkspaceState) error
}

var _ WorkspaceStore = (*repositories.GormRepository)(nil)

func (h *Handler) RegisterTrainingRoutes(group *gin.RouterGroup) {
	h.registerTrainingRoutes(group)
}

type submitRequest struct {
	Spec   domain.JobSpec          `json:"spec"`
	Origin domain.SubmissionOrigin `json:"origin,omitempty"`
}

func (h *Handler) submitJob(c *gin.Context) {
	principal, ok := h.principal(c)
	if !ok {
		h.writeError(c, http.StatusUnauthorized, "AUTH_REQUIRED", "authentication is required")
		return
	}
	if !principal.Allowed("Engineer") {
		h.writeError(c, http.StatusForbidden, "FORBIDDEN", "engineer role is required")
		return
	}
	var request submitRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		h.writeError(c, http.StatusBadRequest, "INVALID_JSON", "request body is invalid")
		return
	}
	origin := request.Origin
	if origin == "" {
		origin = domain.SubmissionOriginPortal
	}
	if origin != domain.SubmissionOriginPortal && origin != domain.SubmissionOriginRayCLI {
		h.writeError(c, http.StatusBadRequest, "INVALID_SUBMISSION_ORIGIN", "submission origin is not supported by this endpoint")
		return
	}
	job, err := h.submission.Submit(c.Request.Context(), SubmissionInput{
		Principal: principal, Spec: request.Spec, Origin: origin,
		IdempotencyKey: c.GetHeader("Idempotency-Key"),
	})
	if err != nil {
		h.writeSubmissionError(c, principal, err)
		return
	}
	h.writeSuccess(c, http.StatusAccepted, publicTrainingJob(job))
}

func (h *Handler) writeSubmissionError(c *gin.Context, principal auth.Principal, err error) {
	switch {
	case errors.Is(err, ErrSubmissionQueueNotAllowed):
		h.writeError(c, http.StatusBadRequest, "QUEUE_NOT_ALLOWED", "jobs may only use the authenticated tenant queue")
	case errors.Is(err, ErrSubmissionInvalidJobSpec):
		h.writeError(c, http.StatusBadRequest, "INVALID_JOB_SPEC", "training job spec is invalid")
	case errors.Is(err, ErrSubmissionImageNotAllowed):
		h.writeError(c, http.StatusBadRequest, "IMAGE_NOT_ALLOWED", "the requested image is not in the platform allowlist")
	case errors.Is(err, ErrSubmissionGitNotAllowed):
		h.writeError(c, http.StatusBadRequest, "GIT_SOURCE_NOT_ALLOWED", "the requested Git host is not in the platform allowlist")
	case errors.Is(err, ErrSubmissionCodeSourceNotAllowed):
		h.writeError(c, http.StatusBadRequest, "CODE_SOURCE_NOT_ALLOWED", "choose a Git repository or an immutable workspace code version")
	case errors.Is(err, ErrSubmissionArtifactNotFound):
		h.writeError(c, http.StatusNotFound, "SOURCE_ARTIFACT_NOT_FOUND", "source artifact was not found")
	case errors.Is(err, ErrSubmissionArtifactNotReady):
		h.writeError(c, http.StatusConflict, "SOURCE_ARTIFACT_NOT_READY", "source artifact must be READY before submission")
	case errors.Is(err, ErrSubmissionArtifactInvalid):
		h.writeError(c, http.StatusConflict, "SOURCE_ARTIFACT_INVALID", "source artifact is invalid")
	case errors.Is(err, ErrSubmissionQueueProvision):
		h.writeError(c, http.StatusBadGateway, "QUEUE_PROVISION_FAILED", "could not ensure the tenant LocalQueue")
	case errors.Is(err, ErrSubmissionIdentityPersist):
		h.writeError(c, http.StatusInternalServerError, "IDENTITY_PERSIST_FAILED", "could not persist authenticated identity")
	case errors.Is(err, ErrSubmissionIDGeneration):
		h.writeError(c, http.StatusInternalServerError, "ID_GENERATION_FAILED", "could not allocate job id")
	case errors.Is(err, ErrSubmissionStorageCatalogUnavailable):
		h.writeError(c, http.StatusServiceUnavailable, "STORAGE_CATALOG_UNAVAILABLE", "storage catalogue is not available")
	case errors.Is(err, ErrSubmissionStorageAssetNotAllowed):
		h.writeError(c, http.StatusNotFound, "STORAGE_ASSET_NOT_ALLOWED", "the selected storage asset is not available")
	case errors.Is(err, ErrSubmissionStorageAssetKindInvalid):
		h.writeError(c, http.StatusBadRequest, "STORAGE_ASSET_KIND_INVALID", "the selected storage asset cannot be used for this data type")
	case errors.Is(err, ErrSubmissionStorageOutputNotWritable):
		h.writeError(c, http.StatusBadRequest, "STORAGE_OUTPUT_NOT_WRITABLE", "the selected output storage asset is read-only")
	case errors.Is(err, ErrSubmissionDataSpacesUnavailable):
		h.writeError(c, http.StatusServiceUnavailable, "DATA_SPACES_UNAVAILABLE", "data spaces are not configured")
	case errors.Is(err, ErrSubmissionDataMountNotReady):
		h.writeError(c, http.StatusConflict, "DATA_SPACE_MOUNT_NOT_READY", "selected data spaces are still being prepared; try again after storage setup is complete")
	case errors.Is(err, ErrSubmissionWorkspaceSnapshotNotFound):
		h.writeError(c, http.StatusNotFound, "WORKSPACE_SNAPSHOT_NOT_FOUND", "the selected workspace version is not available to this user")
	default:
		var quotaErr *repositories.GPUQuotaExceededError
		if errors.As(err, &quotaErr) {
			h.writeError(c, http.StatusConflict, "GPU_QUOTA_EXCEEDED", "the tenant GPU quota does not have enough capacity for this job")
			return
		}
		var conflict *repositories.IdempotencyConflictError
		if errors.As(err, &conflict) {
			existing, getErr := h.repository.Get(c.Request.Context(), principal.TenantID, conflict.JobID)
			if getErr == nil {
				h.writeSuccess(c, http.StatusOK, publicTrainingJob(existing))
				return
			}
		}
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			h.writeError(c, http.StatusConflict, "DUPLICATE_JOB", "a job with this name already exists")
			return
		}
		h.writeError(c, http.StatusInternalServerError, "JOB_CREATE_FAILED", "could not create training job")
	}
}

func (h *Handler) listJobs(c *gin.Context) {
	principal, ok := h.principal(c)
	if !ok {
		h.writeError(c, http.StatusUnauthorized, "AUTH_REQUIRED", "authentication is required")
		return
	}
	limit, _ := strconv.Atoi(c.Query("limit"))
	offset, _ := strconv.Atoi(c.Query("offset"))
	tenantID := strings.TrimSpace(c.Query("tenantId"))
	filter := domain.JobFilter{TenantID: principal.TenantID, Status: domain.State(c.Query("status")), Keyword: c.Query("keyword"), Limit: limit, Offset: offset}
	if principal.HasRole(domain.RoleSuperAdmin) {
		if tenantID == "" {
			filter.AllTenants = true
		} else {
			filter.TenantID = tenantID
		}
	} else if tenantID != "" && tenantID != principal.TenantID {
		h.writeError(c, http.StatusForbidden, "TENANT_SCOPE_FORBIDDEN", "training jobs from another tenant are not accessible")
		return
	}
	page, err := h.repository.List(c.Request.Context(), filter)
	if err != nil {
		h.writeError(c, http.StatusInternalServerError, "JOB_LIST_FAILED", "could not list training jobs")
		return
	}
	h.writeSuccess(c, http.StatusOK, publicTrainingJobPage(page))
}

func (h *Handler) getJob(c *gin.Context) {
	principal, ok := h.principal(c)
	if !ok {
		h.writeError(c, http.StatusUnauthorized, "AUTH_REQUIRED", "authentication is required")
		return
	}
	job, err := h.jobForPrincipal(c.Request.Context(), principal, c.Param("id"))
	if err != nil {
		status := http.StatusInternalServerError
		if strings.Contains(err.Error(), "not found") {
			status = http.StatusNotFound
		}
		h.writeError(c, status, "JOB_NOT_FOUND", "training job was not found")
		return
	}
	h.writeSuccess(c, http.StatusOK, publicTrainingJob(job))
}

func (h *Handler) getJobRuntime(c *gin.Context) {
	principal, ok := h.principal(c)
	if !ok {
		h.writeError(c, http.StatusUnauthorized, "AUTH_REQUIRED", "authentication is required")
		return
	}
	if h.kubernetes == nil {
		h.writeError(c, http.StatusServiceUnavailable, "RUNTIME_UNAVAILABLE", "Kubernetes runtime is not configured")
		return
	}
	job, err := h.jobForPrincipal(c.Request.Context(), principal, c.Param("id"))
	if err != nil {
		h.writeError(c, http.StatusNotFound, "JOB_NOT_FOUND", "training job was not found")
		return
	}
	namespace := strings.TrimSpace(job.KubernetesNS)
	if namespace == "" {
		namespace = "tenant-" + sanitizeDNS(job.TenantID)
	}
	runtime, err := h.kubernetes.ListJobRuntime(c.Request.Context(), namespace, job.ID)
	if err != nil {
		h.writeError(c, http.StatusBadGateway, "RUNTIME_QUERY_FAILED", "could not query Kubernetes job runtime")
		return
	}
	h.writeSuccess(c, http.StatusOK, runtime)
}

func (h *Handler) cancelJob(c *gin.Context) {
	principal, ok := h.principal(c)
	if !ok {
		h.writeError(c, http.StatusUnauthorized, "AUTH_REQUIRED", "authentication is required")
		return
	}
	if !principal.Allowed("Engineer") {
		h.writeError(c, http.StatusForbidden, "FORBIDDEN", "engineer role is required")
		return
	}
	job, err := h.jobForPrincipal(c.Request.Context(), principal, c.Param("id"))
	if err != nil {
		h.writeError(c, http.StatusNotFound, "JOB_NOT_FOUND", "training job was not found")
		return
	}
	if err := h.repository.SetDesiredState(c.Request.Context(), job.TenantID, job.ID, domain.DesiredCanceled); err != nil {
		status := http.StatusInternalServerError
		if strings.Contains(err.Error(), "not found") {
			status = http.StatusNotFound
		}
		h.writeError(c, status, "JOB_CANCEL_FAILED", "could not request job cancellation")
		return
	}
	h.writeSuccess(c, http.StatusAccepted, map[string]string{"id": c.Param("id"), "desiredState": string(domain.DesiredCanceled)})
}

func (h *Handler) getJobLogs(c *gin.Context) {
	principal, ok := h.principal(c)
	if !ok {
		h.writeError(c, http.StatusUnauthorized, "AUTH_REQUIRED", "authentication is required")
		return
	}
	if h.logs == nil {
		h.writeError(c, http.StatusServiceUnavailable, "LOGS_UNAVAILABLE", "log service is not configured")
		return
	}
	job, err := h.jobForPrincipal(c.Request.Context(), principal, c.Param("id"))
	if err != nil {
		h.writeError(c, http.StatusNotFound, "JOB_NOT_FOUND", "training job was not found")
		return
	}
	request, err := NormalizeJobLogPageRequest(c.Query("limit"), c.Query("direction"), c.Query("before"), c.Query("after"))
	if err != nil {
		h.writeError(c, http.StatusBadRequest, "INVALID_LOG_QUERY", err.Error())
		return
	}
	page, err := QueryJobLogPage(c.Request.Context(), h.logs, *job, request, time.Now())
	if err != nil {
		h.writeError(c, http.StatusBadGateway, "LOG_QUERY_FAILED", "could not query job logs")
		return
	}
	h.writeSuccess(c, http.StatusOK, map[string]any{
		"jobId": job.ID,
		"items": page.Lines,
		"page": map[string]any{
			"direction":  page.Direction,
			"limit":      page.Limit,
			"hasMore":    page.HasMore,
			"nextCursor": page.NextCursor,
		},
	})
}

func (h *Handler) getJobMetrics(c *gin.Context) {
	principal, ok := h.principal(c)
	if !ok {
		h.writeError(c, http.StatusUnauthorized, "AUTH_REQUIRED", "authentication is required")
		return
	}
	if h.metrics == nil {
		h.writeError(c, http.StatusServiceUnavailable, "METRICS_UNAVAILABLE", "metrics service is not configured")
		return
	}
	job, err := h.jobForPrincipal(c.Request.Context(), principal, c.Param("id"))
	if err != nil {
		h.writeError(c, http.StatusNotFound, "JOB_NOT_FOUND", "training job was not found")
		return
	}
	metrics, err := h.metrics.QueryJobMetrics(c.Request.Context(), job.ID, 24*time.Hour)
	if err != nil {
		h.writeError(c, http.StatusBadGateway, "METRICS_QUERY_FAILED", "could not query job metrics")
		return
	}
	h.writeSuccess(c, http.StatusOK, metrics)
}

func (h *Handler) getJobExperiment(c *gin.Context) {
	principal, ok := h.principal(c)
	if !ok {
		h.writeError(c, http.StatusUnauthorized, "AUTH_REQUIRED", "authentication is required")
		return
	}
	if h.experiments == nil {
		h.writeError(c, http.StatusServiceUnavailable, "MLFLOW_UNAVAILABLE", "MLflow is not configured")
		return
	}
	job, err := h.jobForPrincipal(c.Request.Context(), principal, c.Param("id"))
	if err != nil {
		h.writeError(c, http.StatusNotFound, "JOB_NOT_FOUND", "training job was not found")
		return
	}
	if job.UserID != principal.Subject && !principal.Allowed(domain.RoleTenantAdmin) {
		h.writeError(c, http.StatusForbidden, "EXPERIMENT_FORBIDDEN", "training experiment is available only to the job owner or a tenant administrator")
		return
	}
	experiment, err := h.experiments.QueryJobExperiment(c.Request.Context(), job.TenantID, job.ID)
	if err != nil {
		h.writeError(c, http.StatusBadGateway, "MLFLOW_QUERY_FAILED", "could not query training experiment")
		return
	}
	h.writeSuccess(c, http.StatusOK, experiment)
}

func (h *Handler) jobForPrincipal(ctx context.Context, principal auth.Principal, jobID string) (*domain.TrainingJob, error) {
	if !principal.HasRole(domain.RoleSuperAdmin) {
		return h.repository.Get(ctx, principal.TenantID, jobID)
	}
	reader, ok := h.repository.(globalJobReader)
	if !ok {
		return nil, fmt.Errorf("global job lookup is not supported")
	}
	return reader.GetByID(ctx, jobID)
}

func (h *Handler) listExperiments(c *gin.Context) {
	principal, ok := h.principal(c)
	if !ok {
		h.writeError(c, http.StatusUnauthorized, "AUTH_REQUIRED", "authentication is required")
		return
	}
	if h.experiments == nil {
		h.writeError(c, http.StatusServiceUnavailable, "MLFLOW_UNAVAILABLE", "MLflow is not configured")
		return
	}
	limit, err := strconv.Atoi(c.DefaultQuery("limit", "50"))
	if err != nil || limit < 1 {
		limit = 50
	}
	if limit > 100 {
		limit = 100
	}
	subject := principal.Subject
	if principal.Allowed(domain.RoleTenantAdmin) {
		subject = ""
	}
	catalog, err := h.experiments.ListTenantExperiments(c.Request.Context(), principal.TenantID, subject, limit)
	if err != nil {
		h.writeError(c, http.StatusBadGateway, "MLFLOW_QUERY_FAILED", "could not query training experiments")
		return
	}
	verified := make([]observability.ExperimentRunSummary, 0, len(catalog.Runs))
	for _, run := range catalog.Runs {
		job, err := h.repository.Get(c.Request.Context(), principal.TenantID, run.JobID)
		if err != nil {
			continue
		}
		if subject != "" && job.UserID != principal.Subject {
			continue
		}
		// The platform database, not a user-controlled MLflow tag, is the
		// authority for ownership shown in the experiment catalog.
		run.SubmitterUserID = job.UserID
		verified = append(verified, run)
	}
	catalog.Runs = verified
	h.writeSuccess(c, http.StatusOK, catalog)
}

func (h *Handler) principal(c *gin.Context) (auth.Principal, bool) {
	if principal, ok := auth.PrincipalFromGin(c); ok {
		return principal, true
	}
	if !h.allowAnonymous {
		return auth.Principal{}, false
	}
	return auth.DemoPrincipal(), true
}

func (h *Handler) writeSuccess(c *gin.Context, status int, data any) {
	c.JSON(status, httpapi.Success(httpapi.RequestID(c.GetHeader("X-Request-ID")), data))
}

func (h *Handler) writeError(c *gin.Context, status int, code, message string) {
	c.JSON(status, httpapi.Failure[any](httpapi.RequestID(c.GetHeader("X-Request-ID")), code, message))
}

// Resolved mounts contain PVC claim names for the renderer. They are useful
// only inside the control plane and must not leak through job APIs, where a
// claimant could otherwise infer infrastructure naming conventions.
func publicTrainingJob(job *domain.TrainingJob) *domain.TrainingJob {
	if job == nil {
		return nil
	}
	copy := *job
	copy.Spec = job.Spec
	copy.Spec.ResolvedStorage = domain.ResolvedStorageMounts{}
	copy.Spec.ResolvedDataMounts = domain.ResolvedDataSpaceMounts{}
	copy.Spec.ResolvedDataRoots = domain.ResolvedDataSpaceRoots{}
	return &copy
}

func publicTrainingJobPage(page domain.Page[domain.TrainingJob]) domain.Page[domain.TrainingJob] {
	items := make([]domain.TrainingJob, 0, len(page.Items))
	for index := range page.Items {
		job := publicTrainingJob(&page.Items[index])
		if job != nil {
			items = append(items, *job)
		}
	}
	return domain.Page[domain.TrainingJob]{Items: items, Limit: page.Limit, Offset: page.Offset, Total: page.Total}
}

func newJobID() (string, error) {
	bytes := make([]byte, 12)
	if _, err := rand.Read(bytes); err != nil {
		return "", fmt.Errorf("read random job id: %w", err)
	}
	return "job-" + hex.EncodeToString(bytes), nil
}

func tenantQueue(tenantID string) string {
	return sanitizeDNS(tenantID) + "-gpu"
}

func sanitizeDNS(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var builder strings.Builder
	for _, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') || char == '-' {
			builder.WriteRune(char)
		} else {
			builder.WriteByte('-')
		}
	}
	result := strings.Trim(builder.String(), "-")
	if result == "" {
		result = "default"
	}
	if len(result) > 50 {
		result = result[:50]
	}
	return result
}

func matchesAllowlist(value string, allowlist []string) bool {
	if len(allowlist) == 0 {
		return true
	}
	for _, allowed := range allowlist {
		allowed = strings.TrimSpace(allowed)
		if allowed != "" && (value == allowed || strings.HasPrefix(value, allowed+"@")) {
			return true
		}
	}
	return false
}

func matchesGitAllowlist(rawURL string, allowlist []string) bool {
	if len(allowlist) == 0 {
		return true
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Hostname() == "" {
		return false
	}
	for _, allowed := range allowlist {
		allowed = strings.TrimSpace(strings.ToLower(allowed))
		if allowed != "" && (parsed.Hostname() == allowed || strings.HasSuffix(parsed.Hostname(), "."+allowed)) {
			return true
		}
	}
	return false
}
