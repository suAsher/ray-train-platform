package main

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"ray-train-platform-backend/api"
	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/config"
	"ray-train-platform-backend/datasetpublisher"
	"ray-train-platform-backend/db"
	"ray-train-platform-backend/domain"
	"ray-train-platform-backend/httpapi"
	"ray-train-platform-backend/k8s"
	"ray-train-platform-backend/objectstore"
	"ray-train-platform-backend/observability"
	"ray-train-platform-backend/rayapi"
	"ray-train-platform-backend/repositories"
	"ray-train-platform-backend/runtimecatalog"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("load configuration: %v", err)
	}
	database, err := db.Open(cfg)
	if err != nil {
		log.Fatalf("open database: %v", err)
	}
	if err := db.ApplyMigrations(database); err != nil {
		log.Fatalf("apply database migrations: %v", err)
	}
	if cfg.MigrationsOnly {
		return
	}
	domain.SetResourceLimits(domain.ResourceLimits{
		MaxWorkerReplicas: cfg.MaxWorkerReplicas,
		MaxGPUsPerWorker:  cfg.MaxGPUsPerWorker,
		MaxTotalGPUs:      cfg.MaxTotalGPUs,
	})
	repository := repositories.NewGormRepository(database)

	kubeClient, err := newKubernetesClient(cfg)
	if err != nil {
		if cfg.AppEnv == "production" {
			log.Fatalf("initialize Kubernetes client: %v", err)
		}
		log.Printf("Kubernetes integration disabled in development: %v", err)
	}
	if kubeClient != nil {
		if err := kubeClient.ValidateRayCapabilities(context.Background(), cfg.RayJobClusterSpecField); err != nil {
			if cfg.AppEnv == "production" {
				log.Fatalf("validate KubeRay/Kueue capabilities: %v", err)
			}
			log.Printf("KubeRay/Kueue capability validation failed in development: %v", err)
		}
	}

	validator, err := newOIDCValidator(cfg)
	if err != nil {
		log.Fatalf("initialize Keycloak OIDC validator: %v", err)
	}
	patAuthenticator, patHandler, err := newPATComponents(repository, cfg)
	if err != nil {
		log.Fatalf("initialize personal access token authentication: %v", err)
	}
	sourceArtifactHandler, err := newSourceArtifactComponents(repository, cfg)
	if err != nil {
		log.Fatalf("initialize source artifact storage: %v", err)
	}
	directoryLister, err := newStorageDirectoryLister(cfg)
	if err != nil {
		log.Fatalf("initialize storage directory browser: %v", err)
	}
	directoryInitializer, _ := directoryLister.(objectstore.PersonalDataDirectoryInitializer)
	if cfg.DataSpacesEnabled && directoryInitializer == nil {
		log.Fatal("initialize governed data spaces: the backend requires complete TOS credentials to create personal mount directories")
	}
	var personalStorageQuota objectstore.PersonalStorageQuotaManager
	if cfg.TOSObjectSetQuotasEnabled {
		store, ok := directoryLister.(*objectstore.TOSStore)
		if !ok {
			log.Fatal("initialize personal storage quota governance: the backend requires a complete TOS SDK store")
		}
		personalStorageQuota, err = objectstore.NewPersonalStorageQuotaManager(store, cfg.PersonalStorageDefaultQuotaBytes, cfg.PersonalStorageMaxQuotaBytes)
		if err != nil {
			log.Fatalf("initialize personal storage quota governance: %v", err)
		}
	}
	artifactLister, _ := directoryLister.(objectstore.ArtifactLister)
	artifactReader, _ := directoryLister.(objectstore.ArtifactReader)
	localSessionAuthenticator, localAuthHandler, err := newLocalAuthComponents(repository, cfg, directoryInitializer, personalStorageQuota)
	if err != nil {
		log.Fatalf("initialize local authentication: %v", err)
	}
	if localAuthHandler != nil {
		created, err := api.EnsureBootstrapAdmin(context.Background(), repository, cfg.BootstrapAdminUsername, cfg.BootstrapAdminPassword, cfg.BootstrapAdminTenant)
		if err != nil {
			log.Fatalf("bootstrap local administrator: %v", err)
		}
		if created {
			log.Printf("created bootstrap local administrator %q in tenant %q", cfg.BootstrapAdminUsername, cfg.BootstrapAdminTenant)
		}
	}

	logs := &observability.LokiClient{BaseURL: cfg.LokiURL}
	metrics := &observability.PrometheusClient{BaseURL: cfg.PrometheusURL}
	var experiments api.ExperimentProvider
	var mlflowClient *observability.MLflowClient
	if cfg.MLflowEnabled {
		mlflowClient = &observability.MLflowClient{BaseURL: cfg.MLflowTrackingURL, ExperimentPrefix: cfg.MLflowExperimentPrefix, ProvenanceKey: []byte(cfg.PATPepper)}
		experiments = mlflowClient
	}
	datasetPublicationManager, err := newDatasetPublicationManager(repository, kubeClient, cfg)
	if err != nil {
		log.Fatalf("initialize dataset publication controller: %v", err)
	}
	dataObjectStore, _ := directoryLister.(objectstore.DataSpaceStore)
	workspaceSnapshotStore, _ := directoryLister.(objectstore.WorkspaceSnapshotStore)
	jobHandler := api.NewHandler(repository, api.Options{AllowAnonymous: cfg.DemoMode, Logs: logs, Metrics: metrics, Experiments: experiments, ImageAllowlist: cfg.RayImageAllowlist, GitAllowlist: cfg.GitAllowlist, Workspaces: repository, Kubernetes: kubeClient, WorkspaceImage: cfg.WorkspaceImage, RayVersion: cfg.RayVersion, ServiceAccount: cfg.RayJobServiceAccount, ImagePullSecrets: cfg.ImagePullSecrets, PlatformNamespace: runtimeNamespace(), IDCClaim: cfg.IDCExistingClaim, IDCMountPath: cfg.IDCMountPath, KueueClusterQueue: cfg.KueueClusterQueue, Admin: repository, GPUAllocations: repository, Quota: repository, WorkspacePepper: []byte(cfg.PATPepper), TrainingNodeSelector: cfg.TrainingNodeSelector, Images: repository, GitCredentials: repository, StorageAssets: repository, Datasets: repository, DatasetPublications: datasetPublicationManager, DatasetInternalPrefix: cfg.DatasetInternalPrefix, DatasetVersioningEnabled: cfg.DatasetVersioningEnabled, RayDataStreamingEnabled: cfg.RayDataStreamingEnabled, DataSpaces: repository, DataSpacesEnabled: cfg.DataSpacesEnabled, DataSpacesFSXAttributes: cfg.DataSpacesFSXAttributes, DataSpacesMountCapacity: cfg.DataSpacesMountCapacity, DataSpacesPublicRoot: cfg.DataSpacesPublicRoot, IDCDataSpacesEnabled: cfg.IDCDataSpacesEnabled, IDCDataSpacesMountCapacity: cfg.IDCDataSpacesMountCapacity, IDCDataSpaceSources: idcDataSpaceSources(cfg), DirectoryLister: directoryLister, DirectoryInitializer: directoryInitializer, DataObjectStore: dataObjectStore, WorkspaceSnapshotStore: workspaceSnapshotStore, WorkspaceSnapshots: repository, ArtifactLister: artifactLister, ArtifactReader: artifactReader, LocalCache: api.LocalCachePolicy{Enabled: cfg.LocalCacheEnabled, AllowedSizes: cfg.LocalCacheAllowedSizes, DefaultSize: cfg.LocalCacheSize, MaxSize: cfg.LocalCacheMaxSize, MountPath: cfg.LocalCacheMountPathData1, MountPaths: []string{cfg.LocalCacheMountPathData1, cfg.LocalCacheMountPathData2}}, RuntimePolicy: runtimecatalog.NewPolicy(cfg.RayTrainManagedEnabled, cfg.RayTrainCanaryEnabled, cfg.RayTrainManagedTenants, cfg.RayTrainCanaryTenants), MLflowDashboardEnabled: cfg.MLflowDashboardEnabled, MLflowDashboardStore: repository, MLflowTrackingURL: cfg.MLflowTrackingURL, MLflowPublicOrigin: cfg.MLflowPublicOrigin, MLflowDashboardPepper: []byte(cfg.PATPepper), MLflowDashboardSessionTTL: time.Duration(cfg.MLflowDashboardSessionHours) * time.Hour})
	rayHandler, err := newRayAPIHandler(repository, jobHandler.SubmissionService(), logs, cfg)
	if err != nil {
		log.Fatalf("initialize Ray Jobs API compatibility: %v", err)
	}
	reconciler := newReconciler(repository, kubeClient, cfg)
	if reconciler != nil && mlflowClient != nil {
		reconciler.WithExperimentFinalizer(mlflowClient)
	}
	platformNamespace := runtimeNamespace()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if reconciler != nil {
		go func() {
			if err := kubeClient.RunAsLeader(ctx, platformNamespace, "ray-train-platform-controller", reconciler.Run); err != nil && ctx.Err() == nil {
				log.Printf("reconciler stopped: %v", err)
			}
		}()
	}
	if datasetPublicationManager != nil {
		go func() {
			if err := kubeClient.RunAsLeader(ctx, platformNamespace, "ray-train-platform-dataset-publisher", datasetPublicationManager.Run); err != nil && ctx.Err() == nil {
				log.Printf("dataset publication controller stopped: %v", err)
			}
		}()
	}

	router := gin.New()
	router.Use(querySafeGINLogger(), querySafeGINRecovery(), requestIDMiddleware())
	if len(cfg.CORSOrigins) > 0 {
		router.Use(cors.New(cors.Config{
			AllowOrigins:     cfg.CORSOrigins,
			AllowMethods:     []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodOptions},
			AllowHeaders:     []string{"Origin", "Content-Type", "Authorization", "Idempotency-Key", "X-Request-ID"},
			ExposeHeaders:    []string{"X-Request-ID"},
			AllowCredentials: true,
		}))
	}
	router.GET("/healthz", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"status": "ok"}) })
	router.GET("/readyz", readinessHandler(database, kubeClient, cfg.AppEnv == "production"))
	registerAPIRoutesWithLocalAuth(router, jobHandler, patHandler, sourceArtifactHandler, localAuthHandler, validator, patAuthenticator, localSessionAuthenticator, kubeClient, rayHandler, cfg)

	server := &http.Server{Addr: cfg.HTTPAddr, Handler: router}
	go func() {
		<-ctx.Done()
		shutdownContext, cancel := context.WithTimeout(context.Background(), shutdownTimeout())
		defer cancel()
		_ = server.Shutdown(shutdownContext)
	}()
	log.Printf("Ray training control plane listening on %s", cfg.HTTPAddr)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("server failed: %v", err)
	}
}

func idcDataSpaceSources(cfg config.Config) map[domain.DataSpaceID]k8s.IDCDataMountSource {
	sources := make(map[domain.DataSpaceID]k8s.IDCDataMountSource, len(cfg.IDCDataSpaceSources))
	for name, source := range cfg.IDCDataSpaceSources {
		space := map[string]domain.DataSpaceID{
			"original": domain.DataSpaceIDCOriginal, "wellspiking": domain.DataSpaceIDCWellspiking, "shared": domain.DataSpaceIDCShared,
		}[name]
		if space != "" {
			sources[space] = k8s.IDCDataMountSource{Server: source.Server, Path: source.Path}
		}
	}
	return sources
}

func newOIDCValidator(cfg config.Config) (*auth.Validator, error) {
	if !cfg.OIDCRequired {
		return nil, nil
	}
	return auth.NewValidator(context.Background(), cfg.OIDCIssuerURL, cfg.OIDCClientID, cfg.OIDCAudience, cfg.OIDCGroupPrefix)
}

func newPATComponents(repository *repositories.GormRepository, cfg config.Config) (*auth.PATAuthenticator, *api.PersonalAccessTokenHandler, error) {
	if !cfg.PATEnabled {
		return nil, nil, nil
	}
	pepper := []byte(cfg.PATPepper)
	authenticator, err := auth.NewPATAuthenticator(repository, pepper, time.Now)
	if err != nil {
		return nil, nil, err
	}
	handler, err := api.NewPersonalAccessTokenHandler(repository, api.PersonalAccessTokenOptions{
		Pepper: pepper, DefaultExpiryDays: cfg.PATDefaultExpiryDays,
		MaxExpiryDays: cfg.PATMaxExpiryDays, AllowDemo: cfg.DemoMode,
	})
	if err != nil {
		return nil, nil, err
	}
	return authenticator, handler, nil
}

// newLocalAuthComponents wires username/password login. It reuses the PAT
// pepper so a deployment only has to manage one authentication secret.
func newLocalAuthComponents(repository *repositories.GormRepository, cfg config.Config, directoryInitializer objectstore.PersonalDataDirectoryInitializer, personalStorageQuota objectstore.PersonalStorageQuotaManager) (auth.LocalSessionVerifier, *api.LocalAuthHandler, error) {
	if !cfg.LocalAuthEnabled {
		return nil, nil, nil
	}
	pepper := []byte(cfg.PATPepper)
	authenticator, err := auth.NewLocalSessionAuthenticator(repository, pepper, time.Now)
	if err != nil {
		return nil, nil, err
	}
	handler := api.NewLocalAuthHandler(api.LocalAuthOptions{
		Store:                       repository,
		Pepper:                      pepper,
		SessionLifetime:             time.Duration(cfg.LocalSessionHours) * time.Hour,
		Enabled:                     true,
		OIDCConfigured:              cfg.OIDCRequired,
		PersonalDataInitializer:     api.NewPersonalDataSpaceInitializer(directoryInitializer),
		PersonalStorageQuota:        personalStorageQuota,
		PersonalStorageQuotaEnabled: cfg.TOSObjectSetQuotasEnabled,
	})
	return authenticator, handler, nil
}

func registerAPIRoutes(router *gin.Engine, jobs *api.Handler, pats *api.PersonalAccessTokenHandler, artifacts *api.SourceArtifactHandler, oidc auth.OIDCVerifier, pat auth.PATVerifier, kubeClient *k8s.Client, cfg config.Config) {
	registerAPIRoutesWithRay(router, jobs, pats, artifacts, oidc, pat, kubeClient, nil, cfg)
}

func registerAPIRoutesWithRay(router *gin.Engine, jobs *api.Handler, pats *api.PersonalAccessTokenHandler, artifacts *api.SourceArtifactHandler, oidc auth.OIDCVerifier, pat auth.PATVerifier, kubeClient *k8s.Client, rays *rayapi.Handler, cfg config.Config) {
	registerAPIRoutesWithLocalAuth(router, jobs, pats, artifacts, nil, oidc, pat, nil, kubeClient, rays, cfg)
}

func registerAPIRoutesWithLocalAuth(router *gin.Engine, jobs *api.Handler, pats *api.PersonalAccessTokenHandler, artifacts *api.SourceArtifactHandler, locals *api.LocalAuthHandler, oidc auth.OIDCVerifier, pat auth.PATVerifier, localSessions auth.LocalSessionVerifier, kubeClient *k8s.Client, rays *rayapi.Handler, cfg config.Config) {
	// Sign-in must be reachable before the caller holds a credential, so it is
	// mounted outside the authenticating group.
	if locals != nil {
		locals.RegisterPublicRoutes(router.Group("/api/v1"))
	}
	// Browser navigation cannot attach the Portal's bearer token. This route
	// verifies its own short-lived, job-scoped token and never exposes port
	// 8265 outside the cluster.
	jobs.RegisterJobDashboardProxyRoute(router.Group("/api/v1"))
	// MLflow authenticates browser navigation with its own path-scoped cookie,
	// so the proxy must remain outside bearer middleware.
	jobs.RegisterMLflowDashboardProxyRoute(router.Group(""))
	// Managed workers authenticate with a random job-scoped credential rather
	// than a user session or cluster-wide service-account token.
	jobs.RegisterTrainingEventRoutes(router.Group("/api/v1/internal"))

	protected := router.Group("")
	protected.Use(auth.HybridMiddlewareWithLocal(oidc, pat, localSessions, cfg.OIDCRequired), auth.DemoIdentityMiddleware(cfg.DemoMode))
	v1 := protected.Group("/api/v1")
	jobs.RegisterSessionRoutes(v1)
	// Mounted before the interactive guard: the proxy authorises browser
	// navigation with its own workspace-scoped token.
	jobs.RegisterWorkspaceProxyRoute(v1)
	jobs.RegisterTrainingRoutes(v1)
	jobs.RegisterCheckpointRoutes(v1)
	if cfg.DatasetVersioningEnabled {
		jobs.RegisterDatasetReadRoutes(v1)
	}
	if artifacts != nil {
		artifacts.RegisterRoutes(v1)
	}
	if rays != nil {
		rays.RegisterRoutes(protected.Group("/ray"))
	}

	interactive := v1.Group("")
	interactive.Use(auth.RequireInteractiveSession(cfg.DemoMode))
	jobs.RegisterMLflowDashboardAccessRoute(interactive)
	if locals != nil {
		locals.RegisterAuthenticatedRoutes(interactive)
		locals.RegisterUserAdminRoutes(interactive)
	}
	oidcOnly := interactive
	jobs.RegisterWorkspaceRoutes(oidcOnly)
	jobs.RegisterAdminRoutes(oidcOnly)
	jobs.RegisterImageRoutes(interactive)
	jobs.RegisterStorageAssetRoutes(interactive)
	if cfg.DatasetVersioningEnabled {
		jobs.RegisterDatasetManagementRoutes(interactive)
	}
	jobs.RegisterDataSpaceRoutes(interactive)
	jobs.RegisterWorkspaceSnapshotRoutes(interactive)
	jobs.RegisterGitCredentialRoutes(interactive)
	oidcOnly.GET("/cluster/topology", clusterTopologyHandler(kubeClient))
	if pats != nil {
		pats.RegisterRoutes(oidcOnly)
	}
}

func runtimeNamespace() string {
	if value := os.Getenv("PLATFORM_NAMESPACE"); value != "" {
		return value
	}
	if data, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/namespace"); err == nil && len(data) > 0 {
		return string(data)
	}
	return "default"
}

func newKubernetesClient(cfg config.Config) (*k8s.Client, error) {
	if cfg.KubeConfig == "" && os.Getenv("KUBERNETES_SERVICE_HOST") == "" {
		return nil, fmt.Errorf("Kubernetes configuration is not available")
	}
	return k8s.NewClient(cfg)
}

func newReconcilerWithQuotaSync(repository *repositories.GormRepository, client *k8s.Client, cfg config.Config, options k8s.RenderOptions) *k8s.Reconciler {
	store := k8s.JobStore(repository)
	if client != nil {
		store = &managedCredentialJobStore{GormRepository: repository, kubernetes: client, now: time.Now}
	}
	return k8s.NewReconciler(store, client, options).WithGitCredentials(repository).WithQuotaSync(k8s.QuotaSyncOptions{
		ClusterQueueName: cfg.KueueClusterQueue,
		Enabled:          cfg.KueueAutoQuota,
	})
}

func newReconciler(repository *repositories.GormRepository, client *k8s.Client, cfg config.Config) *k8s.Reconciler {
	if client == nil {
		return nil
	}
	reconciler := newReconcilerWithQuotaSync(repository, client, cfg, k8s.RenderOptions{
		ClusterSpecField:        cfg.RayJobClusterSpecField,
		RayVersion:              cfg.RayVersion,
		ServiceAccount:          cfg.RayJobServiceAccount,
		ImagePullSecrets:        cfg.ImagePullSecrets,
		SourceMaterializerImage: cfg.SourceMaterializerImage,
		IDCExistingClaim:        cfg.IDCExistingClaim,
		IDCMountPath:            cfg.IDCMountPath,
		NodeSelector:            cfg.TrainingNodeSelector,
		LocalCache: k8s.LocalCacheOptions{
			Enabled:           cfg.LocalCacheEnabled,
			StorageClassData1: cfg.LocalCacheStorageClassData1,
			StorageClassData2: cfg.LocalCacheStorageClassData2,
			AllowedSizes:      cfg.LocalCacheAllowedSizes,
			DefaultSize:       cfg.LocalCacheSize,
			MaxSize:           cfg.LocalCacheMaxSize,
			MountPathData1:    cfg.LocalCacheMountPathData1,
			MountPathData2:    cfg.LocalCacheMountPathData2,
		},
		MLflow: k8s.MLflowOptions{
			Enabled:          cfg.MLflowEnabled,
			TrackingURI:      cfg.MLflowIngestURL,
			ExperimentPrefix: cfg.MLflowExperimentPrefix,
			ProvenanceKey:    []byte(cfg.PATPepper),
		},
		TrainingEventBaseURL: managedTrainingEventBaseURL(runtimeNamespace()),
	})
	if cfg.DatasetVersioningEnabled && cfg.RayDataStreamingEnabled {
		resolver, err := newPrivateDatasetManifestResolver(repository, cfg.DatasetInternalPrefix)
		if err != nil {
			// Configuration validation normally makes this unreachable. Keep the
			// reconciler alive for legacy jobs while streaming remains fail-closed.
			log.Printf("streaming dataset resolver disabled: %v", err)
			return reconciler
		}
		reconciler.WithDatasetManifestResolver(resolver)
	}
	return reconciler
}

func newDatasetPublicationManager(
	repository *repositories.GormRepository,
	client *k8s.Client,
	cfg config.Config,
) (*datasetpublisher.Manager, error) {
	if !cfg.DatasetPublisherEnabled {
		return nil, nil
	}
	if repository == nil || client == nil {
		return nil, fmt.Errorf("dataset publication requires PostgreSQL and Kubernetes")
	}
	controller, err := datasetpublisher.NewController(repository, client, datasetPublicationControllerOptions(cfg))
	if err != nil {
		return nil, fmt.Errorf("configure dataset publication jobs: %w", err)
	}
	manager, err := datasetpublisher.NewManager(repository, controller, datasetpublisher.ManagerOptions{
		PublicRoot:      cfg.DataSpacesPublicRoot,
		SourceIndexName: cfg.DatasetPublisherSourceIndexName,
		PollInterval:    time.Duration(cfg.DatasetPublisherPollIntervalSeconds) * time.Second,
		OnReconcileError: func(err error) {
			log.Printf("dataset publication reconciliation deferred: %s", datasetpublisher.ReconcileDiagnostic(err))
		},
	})
	if err != nil {
		return nil, fmt.Errorf("configure dataset publication manager: %w", err)
	}
	return manager, nil
}

func datasetPublicationControllerOptions(cfg config.Config) datasetpublisher.ControllerOptions {
	return datasetpublisher.ControllerOptions{
		Namespace:             runtimeNamespace(),
		Image:                 cfg.DatasetPublisherImage,
		SourceBucket:          cfg.DatasetPublisherSourceBucket,
		TargetBucket:          cfg.DatasetPublisherTargetBucket,
		TOSEndpoint:           cfg.DatasetPublisherTOSEndpoint,
		TOSRegion:             cfg.DatasetPublisherTOSRegion,
		ImagePullPolicy:       cfg.DatasetPublisherImagePullPolicy,
		ServiceAccountName:    cfg.DatasetPublisherServiceAccount,
		IRSARoleTRN:           cfg.DatasetPublisherIRSARoleTRN,
		ProxySecretName:       cfg.DatasetPublisherProxySecret,
		QueueName:             cfg.DatasetPublisherQueueName,
		PriorityClassName:     cfg.DatasetPublisherPriorityClassName,
		WorkingDirectory:      cfg.DatasetPublisherWorkingDirectory,
		InternalPrefix:        cfg.DatasetInternalPrefix,
		NodeSelector:          cfg.DatasetPublisherNodeSelector,
		PreferredNodeSelector: cfg.DatasetPublisherPreferredNodeSelector,
		Tolerations:           datasetPublisherTolerations(cfg.DatasetPublisherTolerations),
		CPURequest:            cfg.DatasetPublisherCPURequest,
		CPULimit:              cfg.DatasetPublisherCPULimit,
		MemoryRequest:         cfg.DatasetPublisherMemoryRequest,
		MemoryLimit:           cfg.DatasetPublisherMemoryLimit,
		ClientMaxAttempts:     cfg.DatasetPublisherClientMaxAttempts,
		JobBackoffLimit:       cfg.DatasetPublisherJobBackoffLimit,
		JobActiveDeadline:     time.Duration(cfg.DatasetPublisherJobActiveDeadlineSeconds) * time.Second,
		JobTTLAfterFinished:   time.Duration(cfg.DatasetPublisherJobTTLSeconds) * time.Second,
		InitialRetryBackoff:   time.Duration(cfg.DatasetPublisherInitialRetrySeconds) * time.Second,
		MaximumRetryBackoff:   time.Duration(cfg.DatasetPublisherMaximumRetrySeconds) * time.Second,
	}
}

func datasetPublisherTolerations(values []config.DatasetPublisherToleration) []datasetpublisher.PublicationToleration {
	result := make([]datasetpublisher.PublicationToleration, len(values))
	for index, value := range values {
		result[index] = datasetpublisher.PublicationToleration{
			Key: value.Key, Operator: value.Operator, Value: value.Value, Effect: value.Effect,
		}
		if value.TolerationSeconds != nil {
			result[index].Seconds = *value.TolerationSeconds
			result[index].HasSeconds = true
		}
	}
	return result
}

const managedTrainingEventTokenTTL = 30 * 24 * time.Hour

// managedCredentialJobStore is a narrow reconciler adapter. Before a managed
// Ray Train manifest can be rendered, it makes the namespace-local Secret and
// PostgreSQL digest agree. The raw credential never enters a JobRecord.
type managedCredentialJobStore struct {
	*repositories.GormRepository
	kubernetes *k8s.Client
	now        func() time.Time
}

func (store *managedCredentialJobStore) GetByID(ctx context.Context, jobID string) (*domain.TrainingJob, error) {
	job, err := store.GormRepository.GetByID(ctx, jobID)
	if err != nil || job.Spec.TrainingEngine.Resolved() != domain.TrainingEngineRayTrain {
		return job, err
	}
	namespace := strings.TrimSpace(job.KubernetesNS)
	if namespace == "" {
		namespace = "tenant-" + job.TenantID
	}
	token, err := store.kubernetes.EnsureTrainingEventTokenSecret(ctx, namespace, job.ID)
	if err != nil {
		return nil, fmt.Errorf("ensure managed training event credential: %w", err)
	}
	now := store.now().UTC()
	ttl := managedTrainingEventTokenTTL
	if job.Spec.TimeoutSeconds > int64(ttl/time.Second) {
		ttl = time.Duration(job.Spec.TimeoutSeconds)*time.Second + 24*time.Hour
	}
	if err := store.GormRepository.EnsureTrainingEventToken(ctx, job.ID, token, now.Add(ttl)); err != nil {
		return nil, fmt.Errorf("persist managed training event credential: %w", err)
	}
	return job, nil
}

func managedTrainingEventBaseURL(namespace string) string {
	if configured := strings.TrimSpace(os.Getenv("TRAINING_EVENT_BASE_URL")); configured != "" {
		return strings.TrimRight(configured, "/")
	}
	namespace = strings.TrimSpace(namespace)
	if namespace == "" {
		namespace = "ray-train-platform"
	}
	return "http://ray-train-backend." + namespace + ".svc.cluster.local:8080/api/v1/internal"
}

func requestIDMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		requestID := httpapi.RequestID(c.GetHeader("X-Request-ID"))
		c.Request.Header.Set("X-Request-ID", requestID)
		c.Header("X-Request-ID", requestID)
		c.Next()
	}
}

func querySafeGINLogger() gin.HandlerFunc {
	return gin.LoggerWithFormatter(func(params gin.LogFormatterParams) string {
		path := params.Path
		if index := strings.IndexByte(path, '?'); index >= 0 {
			path = path[:index]
		}
		return fmt.Sprintf("[GIN] %v | %3d | %13v | %15s | %-7s %q\n%s",
			params.TimeStamp.Format("2006/01/02 - 15:04:05"), params.StatusCode,
			params.Latency, params.ClientIP, params.Method, path, params.ErrorMessage)
	})
}

func querySafeGINRecovery() gin.HandlerFunc {
	return gin.CustomRecoveryWithWriter(io.Discard, func(c *gin.Context, recovered any) {
		log.Printf("panic recovered for %s %s (%T)\n%s", c.Request.Method, c.Request.URL.Path, recovered, debug.Stack())
		c.AbortWithStatus(http.StatusInternalServerError)
	})
}

func clusterTopologyHandler(client *k8s.Client) gin.HandlerFunc {
	return func(c *gin.Context) {
		if client == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"success": false, "error": gin.H{"code": "KUBERNETES_UNAVAILABLE", "message": "Kubernetes integration is not configured"}})
			return
		}
		topology, err := client.ClusterTopology(c.Request.Context())
		if err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"success": false, "error": gin.H{"code": "TOPOLOGY_QUERY_FAILED", "message": "could not query Kubernetes topology"}})
			return
		}
		c.JSON(http.StatusOK, httpapi.Success(httpapi.RequestID(c.GetHeader("X-Request-ID")), topology))
	}
}

func readinessHandler(database interface{ DB() (*sql.DB, error) }, client *k8s.Client, production bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		dbConn, err := database.DB()
		if err != nil || dbConn.PingContext(c.Request.Context()) != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"status": "database_unavailable"})
			return
		}
		if production && client == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"status": "kubernetes_unavailable"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ready"})
	}
}

func shutdownTimeout() time.Duration { return 10 * time.Second }
