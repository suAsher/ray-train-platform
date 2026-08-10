package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"ray-train-platform-backend/api"
	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/config"
	"ray-train-platform-backend/db"
	"ray-train-platform-backend/domain"
	"ray-train-platform-backend/httpapi"
	"ray-train-platform-backend/k8s"
	"ray-train-platform-backend/observability"
	"ray-train-platform-backend/rayapi"
	"ray-train-platform-backend/repositories"
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
	localSessionAuthenticator, localAuthHandler, err := newLocalAuthComponents(repository, cfg)
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
	jobHandler := api.NewHandler(repository, api.Options{AllowAnonymous: cfg.DemoMode, Logs: logs, Metrics: metrics, ImageAllowlist: cfg.RayImageAllowlist, GitAllowlist: cfg.GitAllowlist, Workspaces: repository, Kubernetes: kubeClient, WorkspaceImage: cfg.WorkspaceImage, RayVersion: cfg.RayVersion, ServiceAccount: cfg.RayJobServiceAccount, ImagePullSecrets: cfg.ImagePullSecrets, IDCClaim: cfg.IDCExistingClaim, IDCMountPath: cfg.IDCMountPath, KueueClusterQueue: cfg.KueueClusterQueue, Admin: repository, Quota: repository})
	rayHandler, err := newRayAPIHandler(repository, jobHandler.SubmissionService(), logs, cfg)
	if err != nil {
		log.Fatalf("initialize Ray Jobs API compatibility: %v", err)
	}
	reconciler := newReconciler(repository, kubeClient, cfg)
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

	router := gin.New()
	router.Use(gin.Logger(), gin.Recovery(), requestIDMiddleware())
	if len(cfg.CORSOrigins) > 0 {
		router.Use(cors.New(cors.Config{
			AllowOrigins:     cfg.CORSOrigins,
			AllowMethods:     []string{http.MethodGet, http.MethodPost, http.MethodDelete, http.MethodOptions},
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
func newLocalAuthComponents(repository *repositories.GormRepository, cfg config.Config) (auth.LocalSessionVerifier, *api.LocalAuthHandler, error) {
	if !cfg.LocalAuthEnabled {
		return nil, nil, nil
	}
	pepper := []byte(cfg.PATPepper)
	authenticator, err := auth.NewLocalSessionAuthenticator(repository, pepper, time.Now)
	if err != nil {
		return nil, nil, err
	}
	handler := api.NewLocalAuthHandler(api.LocalAuthOptions{
		Store:           repository,
		Pepper:          pepper,
		SessionLifetime: time.Duration(cfg.LocalSessionHours) * time.Hour,
		Enabled:         true,
		OIDCConfigured:  cfg.OIDCRequired,
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

	protected := router.Group("")
	protected.Use(auth.HybridMiddlewareWithLocal(oidc, pat, localSessions, cfg.OIDCRequired), auth.DemoIdentityMiddleware(cfg.DemoMode))
	v1 := protected.Group("/api/v1")
	jobs.RegisterSessionRoutes(v1)
	jobs.RegisterTrainingRoutes(v1)
	if artifacts != nil {
		artifacts.RegisterRoutes(v1)
	}
	if rays != nil {
		rays.RegisterRoutes(protected.Group("/ray"))
	}

	interactive := v1.Group("")
	interactive.Use(auth.RequireInteractiveSession(cfg.DemoMode))
	if locals != nil {
		locals.RegisterAuthenticatedRoutes(interactive)
		locals.RegisterUserAdminRoutes(interactive)
	}
	oidcOnly := interactive
	jobs.RegisterWorkspaceRoutes(oidcOnly)
	jobs.RegisterAdminRoutes(oidcOnly)
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
	return k8s.NewReconciler(repository, client, options).WithQuotaSync(k8s.QuotaSyncOptions{
		ClusterQueueName: cfg.KueueClusterQueue,
		Enabled:          cfg.KueueAutoQuota,
	})
}

func newReconciler(repository *repositories.GormRepository, client *k8s.Client, cfg config.Config) *k8s.Reconciler {
	if client == nil {
		return nil
	}
	return newReconcilerWithQuotaSync(repository, client, cfg, k8s.RenderOptions{
		ClusterSpecField:        cfg.RayJobClusterSpecField,
		RayVersion:              cfg.RayVersion,
		ServiceAccount:          cfg.RayJobServiceAccount,
		ImagePullSecrets:        cfg.ImagePullSecrets,
		SourceMaterializerImage: cfg.SourceMaterializerImage,
		IDCExistingClaim:        cfg.IDCExistingClaim,
		IDCMountPath:            cfg.IDCMountPath,
		TOSSecretName:           cfg.TOSSecretName,
		TOSEndpoint:             cfg.TOSEndpoint,
		TOSBucket:               cfg.TOSBucket,
		NodeSelector:            cfg.TrainingNodeSelector,
	})
}

func requestIDMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		requestID := httpapi.RequestID(c.GetHeader("X-Request-ID"))
		c.Request.Header.Set("X-Request-ID", requestID)
		c.Header("X-Request-ID", requestID)
		c.Next()
	}
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
