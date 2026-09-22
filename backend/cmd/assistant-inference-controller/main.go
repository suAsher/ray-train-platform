package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/google/uuid"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/leaderelection"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
	"ray-train-platform-backend/assistantidle"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}
func run() error {
	configPath := flag.String("config", "/etc/assistant-idle/config.json", "administrator-owned configuration")
	mode := flag.String("mode", "inspect", "inspect (read-only), controller, or reaper")
	listen := flag.String("listen", ":8080", "internal gate listen address")
	kubeconfig := flag.String("kubeconfig", "", "only supported by read-only inspect mode")
	flag.Parse()
	if *mode != "inspect" && *mode != "controller" && *mode != "reaper" {
		return errors.New("invalid mode")
	}
	if *kubeconfig != "" && *mode != "inspect" {
		return errors.New("mutating modes require in-cluster identity")
	}
	file, err := os.Open(*configPath)
	if err != nil {
		return err
	}
	defer file.Close()
	cfg, err := assistantidle.ParseRuntimeConfig(file)
	if err != nil {
		return err
	}
	var rc *rest.Config
	if *kubeconfig != "" {
		rc, err = clientcmd.BuildConfigFromFlags("", *kubeconfig)
	} else {
		rc, err = rest.InClusterConfig()
	}
	if err != nil {
		return err
	}
	rc.Timeout = 2 * time.Second
	typed, err := kubernetes.NewForConfig(rc)
	if err != nil {
		return err
	}
	dyn, err := dynamic.NewForConfig(rc)
	if err != nil {
		return err
	}
	backend := assistantidle.NewKubeBackend(assistantidle.KubeAdapterConfig{Dynamic: dyn, Kubernetes: typed, Namespace: cfg.Render.Namespace, Name: cfg.Render.Name, InstanceID: cfg.InstanceID, Render: cfg.Render, NodeAllowlist: cfg.Render.AllowedWorkerNodes, RequiredLabels: cfg.Render.RequiredNodeLabels, ToleratedTaintKey: cfg.Render.ToleratedTaintKeys})
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	if *mode == "inspect" {
		attempt, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		snapshot, err := backend.Observe(attempt)
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(snapshot)
	}
	if *mode == "reaper" {
		return runReaper(ctx, typed, backend, cfg)
	}
	gate := assistantidle.NewGate(uuid.NewString(), time.Now)
	mux := http.NewServeMux()
	mux.Handle("/gate", gate)
	mux.HandleFunc("/livez", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	server := &http.Server{Addr: *listen, Handler: mux, ReadHeaderTimeout: 2 * time.Second, ReadTimeout: 3 * time.Second, WriteTimeout: 3 * time.Second, IdleTimeout: 10 * time.Second, MaxHeaderBytes: 4096}
	serverErrors := make(chan error, 1)
	go func() { serverErrors <- server.ListenAndServe(); stop() }()
	policy := assistantidle.DefaultConfig()
	policy.Enabled = cfg.Enabled
	controller := assistantidle.NewController(policy, backend, gate, time.Now)
	lock := &resourcelock.LeaseLock{LeaseMeta: metav1.ObjectMeta{Name: cfg.LeaseName, Namespace: cfg.Render.Namespace}, Client: typed.CoordinationV1(), LockConfig: resourcelock.ResourceLockConfig{Identity: uuid.NewString()}}
	elector, err := leaderelection.NewLeaderElector(leaderelection.LeaderElectionConfig{Lock: lock, LeaseDuration: 30 * time.Second, RenewDeadline: 10 * time.Second, RetryPeriod: 2 * time.Second, ReleaseOnCancel: false, Callbacks: leaderelection.LeaderCallbacks{
		OnStartedLeading: func(leaderCtx context.Context) {
			var previous assistantidle.State
			controller.Run(leaderCtx, func(d assistantidle.Decision, err error) {
				// Provider messages, user logs and prompts never enter this component.
				if d.State != previous || err != nil {
					log.Printf("state=%s action=%s observation_or_action_failed=%t", d.State, d.Action, err != nil)
					previous = d.State
				}
			})
		},
		OnStoppedLeading: func() { gate.Close(); stop() },
	}})
	if err != nil {
		_ = server.Close()
		return err
	}
	elector.Run(ctx)
	gate.Close()
	_ = server.Close()
	select {
	case err := <-serverErrors:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	default:
	}
	return nil
}
func runReaper(ctx context.Context, client kubernetes.Interface, backend *assistantidle.KubeBackend, cfg assistantidle.RuntimeConfig) error {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return nil
		}
		attempt, cancel := context.WithTimeout(ctx, 5*time.Second)
		err := reap(attempt, client, backend, cfg, time.Now())
		cancel()
		if err != nil {
			log.Print("reaper observation or deletion failed; will retry")
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}
func reap(ctx context.Context, client kubernetes.Interface, backend *assistantidle.KubeBackend, cfg assistantidle.RuntimeConfig, now time.Time) error {
	uid, created, err := backend.OwnService(ctx)
	if err != nil || uid == "" {
		return err
	}
	lease, err := client.CoordinationV1().Leases(cfg.Render.Namespace).Get(ctx, cfg.LeaseName, metav1.GetOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	status := assistantidle.LeaseStatus{}
	if err == nil && lease.Spec.HolderIdentity != nil && lease.Spec.RenewTime != nil && lease.Spec.LeaseDurationSeconds != nil {
		status.Holder = *lease.Spec.HolderIdentity
		status.RenewedAt = lease.Spec.RenewTime.Time
		status.Duration = time.Duration(*lease.Spec.LeaseDurationSeconds) * time.Second
	}
	if assistantidle.LeaseExpired(status, now) || created.IsZero() || created.After(now.Add(5*time.Second)) || now.Sub(created) >= time.Hour+15*time.Second {
		return backend.Delete(ctx, uid)
	}
	return nil
}
