package assistantidle

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type fakeBackend struct {
	snapshot Snapshot
	err      error
	creates  int
	deletes  []string
}

func (b *fakeBackend) Observe(context.Context) (Snapshot, error) { return b.snapshot, b.err }
func (b *fakeBackend) Create(context.Context) error              { b.creates++; return nil }
func (b *fakeBackend) Delete(_ context.Context, uid string) error {
	b.deletes = append(b.deletes, uid)
	return nil
}
func newTestController(b *fakeBackend, now *time.Time) (*Controller, *Gate) {
	cfg := DefaultConfig()
	cfg.Enabled = true
	gate := NewGate("test", func() time.Time { return *now })
	return NewController(cfg, b, gate, func() time.Time { return *now }), gate
}
func TestControllerRequiresContinuousIdleAndRechecksBeforeCreating(t *testing.T) {
	now := time.Unix(1000, 0)
	b := &fakeBackend{snapshot: Snapshot{Observation: Observation{Fresh: true, Enabled: true, EligibleIdleGPU: 1}}}
	c, _ := newTestController(b, &now)
	if _, err := c.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	now = now.Add(9 * time.Minute)
	c.Step(context.Background())
	if b.creates != 0 {
		t.Fatal("created too early")
	}
	b.snapshot.Observation.TrainingDemand = true
	now = now.Add(time.Minute)
	c.Step(context.Background())
	b.snapshot.Observation.TrainingDemand = false
	now = now.Add(time.Minute)
	c.Step(context.Background())
	if b.creates != 0 {
		t.Fatal("idle clock did not reset")
	}
	now = now.Add(10 * time.Minute)
	c.Step(context.Background())
	if b.creates != 1 {
		t.Fatalf("creates=%d", b.creates)
	}
	now = now.Add(time.Second)
	c.Step(context.Background())
	if b.creates != 1 {
		t.Fatal("duplicate create before observation acknowledged")
	}
}
func TestControllerWithdrawsGateThenDeletesOnlyObservedUID(t *testing.T) {
	now := time.Unix(1000, 0)
	b := &fakeBackend{snapshot: Snapshot{UID: "own-uid", CreatedAt: now, Observation: Observation{Fresh: true, Enabled: true, ServiceExists: true, ServiceReady: true, Admitted: true}}}
	c, g := newTestController(b, &now)
	c.Step(context.Background())
	if !g.Snapshot().Allow {
		t.Fatal("ready not routed")
	}
	b.snapshot.Observation.TrainingDemand = true
	now = now.Add(time.Second)
	c.Step(context.Background())
	if g.Snapshot().Allow || len(b.deletes) != 0 {
		t.Fatal("must close before bounded drain")
	}
	b.snapshot.Observation.TrainingDemand = false
	now = now.Add(15 * time.Second)
	c.Step(context.Background())
	if len(b.deletes) != 1 || b.deletes[0] != "own-uid" {
		t.Fatalf("delete=%v", b.deletes)
	}
	if g.Snapshot().Allow {
		t.Fatal("drain must be sticky")
	}
}
func TestStaleObservationClosesAndDeletesKnownOwnService(t *testing.T) {
	now := time.Unix(1000, 0)
	b := &fakeBackend{snapshot: Snapshot{UID: "own", CreatedAt: now, Observation: Observation{Fresh: true, Enabled: true, ServiceExists: true, ServiceReady: true, Admitted: true}}}
	c, g := newTestController(b, &now)
	c.Step(context.Background())
	b.err = errors.New("unavailable")
	now = now.Add(time.Second)
	_, err := c.Step(context.Background())
	if err == nil || g.Snapshot().Allow || len(b.deletes) != 1 || b.deletes[0] != "own" {
		t.Fatal("failed to fail closed")
	}
}
func TestControllerRestartDoesNotResetMaximumLifetime(t *testing.T) {
	now := time.Unix(10000, 0)
	b := &fakeBackend{snapshot: Snapshot{UID: "own", CreatedAt: now.Add(-time.Hour), Observation: Observation{Fresh: true, Enabled: true, ServiceExists: true, ServiceReady: true, Admitted: true}}}
	c, g := newTestController(b, &now)
	d, _ := c.Step(context.Background())
	if d.Action != ActionDrain || g.Snapshot().Allow {
		t.Fatal("restart extended lifetime")
	}
}
func TestGateExpiresEvenWhenControllerStopsAndResponseIsUncacheable(t *testing.T) {
	now := time.Unix(1000, 0)
	g := NewGate("epoch", func() time.Time { return now })
	g.Open(now.Add(3 * time.Second))
	if !g.Snapshot().Allow {
		t.Fatal("gate not open")
	}
	now = now.Add(3 * time.Second)
	if g.Snapshot().Allow {
		t.Fatal("stale gate open")
	}
	w := httptest.NewRecorder()
	g.ServeHTTP(w, httptest.NewRequest("GET", "/gate", nil))
	if w.Header().Get("Cache-Control") != "no-store" || !strings.Contains(w.Body.String(), `"allow":false`) {
		t.Fatal(w.Body.String())
	}
	w = httptest.NewRecorder()
	g.ServeHTTP(w, httptest.NewRequest("POST", "/gate", nil))
	if w.Code != 405 {
		t.Fatal(w.Code)
	}
}
func TestReaperOnlyDeletesOwnObservedServiceAfterLeaseExpires(t *testing.T) {
	now := time.Unix(1000, 0)
	for _, tc := range []struct {
		name  string
		lease LeaseStatus
		want  bool
	}{
		{"fresh", LeaseStatus{Holder: "controller", RenewedAt: now, Duration: 30 * time.Second}, false},
		{"expired", LeaseStatus{Holder: "controller", RenewedAt: now.Add(-31 * time.Second), Duration: 30 * time.Second}, true},
		{"empty", LeaseStatus{}, true},
		{"future", LeaseStatus{Holder: "controller", RenewedAt: now.Add(time.Minute), Duration: 30 * time.Second}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if LeaseExpired(tc.lease, now) != tc.want {
				t.Fatal(tc.name)
			}
		})
	}
}
func TestOtherPendingReclaimsExistingService(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Enabled = true
	d := Decide(cfg, Observation{Enabled: true, Fresh: true, ServiceExists: true, ServiceReady: true, Admitted: true, OtherPending: true})
	if d.Action != ActionDrain {
		t.Fatalf("got %s", d.Action)
	}
}
func (b *fakeBackend) OwnService(context.Context) (string, time.Time, error) {
	return b.snapshot.UID, b.snapshot.CreatedAt, nil
}
func TestObservationFailureOnRestartStillReclaimsOwnedService(t *testing.T) {
	now := time.Unix(1000, 0)
	b := &fakeBackend{snapshot: Snapshot{UID: "old-own"}, err: context.DeadlineExceeded}
	c, g := newTestController(b, &now)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := c.Step(ctx)
	if err == nil || g.Snapshot().Allow || len(b.deletes) != 1 {
		t.Fatal("restart failed to reclaim own service after observation failure")
	}
}

type budgetBackend struct {
	fakeBackend
	observeCancel         context.CancelFunc
	deletedWithLiveBudget bool
}

func (b *budgetBackend) Observe(ctx context.Context) (Snapshot, error) {
	b.observeCancel()
	return b.snapshot, nil
}
func (b *budgetBackend) Delete(ctx context.Context, uid string) error {
	b.deletedWithLiveBudget = ctx.Err() == nil
	return nil
}
func TestDeleteAfterSuccessfulObservationHasIndependentTimeBudget(t *testing.T) {
	now := time.Unix(1000, 0)
	ctx, cancel := context.WithCancel(context.Background())
	b := &budgetBackend{fakeBackend: fakeBackend{snapshot: Snapshot{UID: "own", CreatedAt: now, Observation: Observation{Fresh: false, Enabled: true, ServiceExists: true}}}, observeCancel: cancel}
	cfg := DefaultConfig()
	cfg.Enabled = true
	c := NewController(cfg, b, NewGate("test", func() time.Time { return now }), func() time.Time { return now })
	_, err := c.Step(ctx)
	if err != nil || !b.deletedWithLiveBudget {
		t.Fatal("cleanup reused exhausted observation budget")
	}
}
