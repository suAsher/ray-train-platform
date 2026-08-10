package k8s

import (
	"context"
	"fmt"
	"os"
	"time"

	coordinationv1 "k8s.io/client-go/kubernetes/typed/coordination/v1"
	"k8s.io/client-go/tools/leaderelection"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
)

func (c *Client) RunAsLeader(ctx context.Context, namespace, electionName string, run func(context.Context) error) error {
	if c == nil || c.restConfig == nil || c.kubernetes == nil {
		return fmt.Errorf("leader election requires a Kubernetes client")
	}
	if namespace == "" || electionName == "" {
		return fmt.Errorf("leader election namespace and name are required")
	}
	identity, err := os.Hostname()
	if err != nil || identity == "" {
		identity = "ray-train-platform-controller"
	}
	coordinationClient, err := coordinationv1.NewForConfig(c.restConfig)
	if err != nil {
		return fmt.Errorf("create coordination client: %w", err)
	}
	lock, err := resourcelock.New(resourcelock.LeasesResourceLock, namespace, electionName, c.kubernetes.CoreV1(), coordinationClient, resourcelock.ResourceLockConfig{Identity: identity})
	if err != nil {
		return fmt.Errorf("create leader election lock: %w", err)
	}
	elector, err := leaderelection.NewLeaderElector(leaderelection.LeaderElectionConfig{
		Lock:            lock,
		LeaseDuration:   15 * time.Second,
		RenewDeadline:   10 * time.Second,
		RetryPeriod:     2 * time.Second,
		ReleaseOnCancel: true,
		Callbacks: leaderelection.LeaderCallbacks{
			OnStartedLeading: func(leaderCtx context.Context) { _ = run(leaderCtx) },
			OnStoppedLeading: func() { /* another replica will take over */ },
			OnNewLeader:      func(string) {},
		},
	})
	if err != nil {
		return fmt.Errorf("create leader elector: %w", err)
	}
	elector.Run(ctx)
	return ctx.Err()
}
