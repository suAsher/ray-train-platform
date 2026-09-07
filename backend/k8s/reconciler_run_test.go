package k8s

import (
	"context"
	"errors"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	"ray-train-platform-backend/domain"
)

type failingCleanupRunStore struct {
	*memoryJobStore
	cycle      func()
	persistent bool
	calls      int
}

func (s *failingCleanupRunStore) ListManagedAttemptCleanup(context.Context, int, time.Time) ([]domain.ManagedAttemptResource, error) {
	s.calls++
	s.cycle()
	if s.persistent || s.calls == 1 {
		return nil, errors.New("cleanup database constraint rejected")
	}
	return nil, nil
}

func (*failingCleanupRunStore) ListReconcileCandidates(context.Context, int) ([]string, error) {
	return nil, nil
}

func TestReconcilerRunContinuesQuotaSyncAfterInitialCleanupFailure(t *testing.T) {
	for _, persistent := range []bool{false, true} {
		name := "transient"
		if persistent {
			name = "persistent"
		}
		t.Run(name, func(t *testing.T) {
			previous := domain.CurrentResourceLimits()
			t.Cleanup(func() { domain.SetResourceLimits(previous) })
			client, dynamic := quotaTestClient(clusterQueueObject("cluster-gpu-queue", "16", "16", "64Gi"))
			client.kubernetes = fake.NewSimpleClientset(
				trainingNode("gpu-1", trainingPool, "8", "8", "32Gi"),
				trainingNode("gpu-2", trainingPool, "8", "8", "32Gi"),
			)
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			store := &failingCleanupRunStore{memoryJobStore: &memoryJobStore{}, persistent: persistent}
			store.cycle = func() {
				if store.calls == 1 {
					if got := nominalQuotaFor(t, dynamic, "cluster-gpu-queue", "nvidia.com/gpu"); got != "16" {
						t.Fatalf("initial GPU quota = %s, want 16", got)
					}
					// Node becomes available after the first quota observation.
					if _, err := client.kubernetes.CoreV1().Nodes().Create(ctx, trainingNode("gpu-3", trainingPool, "8", "8", "32Gi"), metav1.CreateOptions{}); err != nil {
						t.Fatal(err)
					}
				}
				if store.calls == 3 {
					cancel()
				}
			}
			reconciler := NewReconciler(store, client, RenderOptions{NodeSelector: trainingPool}).WithRayJobRetention(0).WithQuotaSync(QuotaSyncOptions{ClusterQueueName: "cluster-gpu-queue", Enabled: true})
			reconciler.interval = time.Millisecond
			if err := reconciler.Run(ctx); !errors.Is(err, context.Canceled) {
				t.Fatalf("Run must survive cleanup failures until cancellation, got %v after %d cycles", err, store.calls)
			}
			if store.calls != 3 {
				t.Fatalf("cycles = %d, want 3", store.calls)
			}
			if got := nominalQuotaFor(t, dynamic, "cluster-gpu-queue", "nvidia.com/gpu"); got != "24" {
				t.Fatalf("GPU quota after node joins = %s, want 24 without manual patch", got)
			}
			if got := domain.CurrentResourceLimits().MaxTotalGPUs; got != 24 {
				t.Fatalf("runtime GPU limit = %d, want 24", got)
			}
		})
	}
}
