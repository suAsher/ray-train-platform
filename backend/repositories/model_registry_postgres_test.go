package repositories

import (
	"context"
	"errors"
	mr "ray-train-platform-backend/modelregistry"
	"testing"
	"time"
)

func TestModelRegistryPostgresConcurrentLease(t *testing.T) {
	ea, eb := evaluationPostgresStores(t, true)
	_, evaluation := releaseFixture(t, ea)
	a, b := NewModelRegistryStore(ea.db), NewModelRegistryStore(eb.db)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	type result struct {
		record   mr.Record
		acquired bool
		err      error
	}
	results := make(chan result, 2)
	start := make(chan struct{})
	for _, s := range []*ModelRegistryStore{a, b} {
		go func(s *ModelRegistryStore) {
			<-start
			r, ok, err := s.Acquire(ctx, evaluation.ModelID, evaluation.VersionID, evaluation.OwnerID, false)
			results <- result{r, ok, err}
		}(s)
	}
	close(start)
	first, second := <-results, <-results
	if first.err != nil || second.err != nil || first.acquired == second.acquired || first.record.LeaseID != second.record.LeaseID {
		t.Fatalf("concurrent duplicate workers: %+v %+v", first, second)
	}
	lease := first.record.LeaseID
	if err := a.Renew(ctx, evaluation.VersionID, lease); err != nil {
		t.Fatal(err)
	}
	if err := b.Complete(ctx, evaluation.VersionID, "stale-worker", mr.Link{}, "failure"); !errors.Is(err, mr.ErrConflict) {
		t.Fatalf("stale worker completed: %v", err)
	}
	link := mr.Link{RegisteredName: "platform-model-test", Version: "1", RunID: "test-run", SourceURI: "mlflow-artifacts:/test/model.pt"}
	if err := a.Complete(ctx, evaluation.VersionID, lease, link, ""); err != nil {
		t.Fatal(err)
	}
	if r, acquired, err := b.Acquire(ctx, evaluation.ModelID, evaluation.VersionID, evaluation.OwnerID, false); err != nil || acquired || r.State != "READY" {
		t.Fatalf("ready version recreated: %+v %v %v", r, acquired, err)
	}
}
