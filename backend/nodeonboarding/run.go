package nodeonboarding

import (
	"context"
	"fmt"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/leaderelection"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
	"log"
	"time"
)

// Run maintains a lease before reconciling. Losing leadership cancels the worker.
func (c *Controller) Run(ctx context.Context, lease, identity string) error {
	if !validNode(lease) || identity == "" {
		return fmt.Errorf("lease name and identity required")
	}
	lock := &resourcelock.LeaseLock{LeaseMeta: metav1.ObjectMeta{Name: lease, Namespace: c.config.Namespace}, Client: c.client.CoordinationV1(), LockConfig: resourcelock.ResourceLockConfig{Identity: identity}}
	elector, err := leaderelection.NewLeaderElector(leaderelection.LeaderElectionConfig{Lock: lock, LeaseDuration: 30 * time.Second, RenewDeadline: 20 * time.Second, RetryPeriod: 5 * time.Second, ReleaseOnCancel: true, Callbacks: leaderelection.LeaderCallbacks{OnStartedLeading: c.loop, OnStoppedLeading: func() { log.Print("node onboarding leader stopped") }}})
	if err != nil {
		return err
	}
	elector.Run(ctx)
	return nil
}
func (c *Controller) loop(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		nodes, err := c.client.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
		if err != nil {
			log.Printf("list onboarding nodes: %v", err)
		} else {
			for _, node := range nodes.Items {
				if ctx.Err() != nil {
					return
				}
				if !eligible(&node) && node.Labels[CacheReadyLabel] == "" && node.Annotations[StateAnnotation] == "" {
					continue
				}
				if err := c.Reconcile(ctx, node.Name); err != nil {
					log.Printf("onboarding node %s: %v", node.Name, err)
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
