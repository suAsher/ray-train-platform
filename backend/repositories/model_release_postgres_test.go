package repositories

import (
	"context"
	"errors"
	"testing"
	"time"

	mr "ray-train-platform-backend/modelrelease"
)

func TestModelReleasePostgresMigrationsAndWorkflow(t *testing.T) {
	a, _ := evaluationPostgresStores(t, true)
	runModelReleaseTests(t, a)
	var approved mr.Release
	if err := a.db.Where("state = ?", mr.Approved).First(&approved).Error; err != nil {
		t.Fatal(err)
	}
	for _, u := range []map[string]any{{"report_sha256": "replacement"}, {"state": mr.Pending, "reviewer_id": "", "reviewed_at": nil, "review_reason": ""}} {
		if err := a.db.Model(&mr.Release{}).Where("id = ?", approved.ID).Updates(u).Error; err == nil {
			t.Fatal("decided approval changed")
		}
	}
	if err := a.db.Model(&mr.History{}).Where("model_id = ?", approved.ModelID).Update("reason", "rewrite history").Error; err == nil {
		t.Fatal("history rewritten")
	}
	var count int64
	if err := a.db.Table("model_registry_links").Count(&count).Error; err != nil {
		t.Fatal(err)
	}
}
func TestModelReleasePostgresConcurrentDecisionsAndPointer(t *testing.T) {
	ea, eb := evaluationPostgresStores(t, false)
	a, e := releaseFixture(t, ea)
	b := NewModelReleaseRepository(eb.db)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	owner := mr.Actor{ID: e.OwnerID, TenantID: e.TenantID}
	req := mr.Request{ModelID: e.ModelID, VersionID: e.VersionID, EvaluationID: e.ID, Reason: "ready", IdempotencyKey: "race"}
	type result struct {
		release mr.Release
		err     error
	}
	ch := make(chan result, 2)
	start := make(chan struct{})
	for _, s := range []*ModelReleaseRepository{a, b} {
		go func(s *ModelReleaseRepository) {
			<-start
			r, err := s.CreateRequest(ctx, req, owner)
			ch <- result{r, err}
		}(s)
	}
	close(start)
	first, second := <-ch, <-ch
	if first.err != nil || second.err != nil || first.release.ID != second.release.ID {
		t.Fatalf("concurrent requests %+v %+v", first, second)
	}
	start = make(chan struct{})
	for i, s := range []*ModelReleaseRepository{a, b} {
		go func(i int, s *ModelReleaseRepository) {
			<-start
			r, err := s.Decide(ctx, first.release.ID, mr.Decision{Revision: 1, Approve: true, Reason: "reviewed"}, mr.Actor{ID: []string{"review-a", "review-b"}[i], SuperAdmin: true})
			ch <- result{r, err}
		}(i, s)
	}
	close(start)
	first, second = <-ch, <-ch
	if !(first.err == nil && errors.Is(second.err, mr.ErrConflict) || second.err == nil && errors.Is(first.err, mr.ErrConflict)) {
		t.Fatalf("decision CAS %+v %+v", first, second)
	}
	r := first.release
	if first.err != nil {
		r = second.release
	}
	results := make(chan error, 2)
	start = make(chan struct{})
	for i, s := range []*ModelReleaseRepository{a, b} {
		go func(i int, s *ModelReleaseRepository) {
			<-start
			_, err := s.Publish(ctx, e.ModelID, mr.PublishRequest{ReleaseID: r.ID, Revision: 0, Reason: "publish", IdempotencyKey: []string{"pub-a", "pub-b"}[i]}, owner)
			results <- err
		}(i, s)
	}
	close(start)
	x, y := <-results, <-results
	if !(x == nil && errors.Is(y, mr.ErrConflict) || y == nil && errors.Is(x, mr.ErrConflict)) {
		t.Fatalf("pointer CAS %v %v", x, y)
	}
	// DB cannot link a pointer to a pending or different-model review.
	pendingReq := req
	pendingReq.IdempotencyKey = "pending"
	pending, err := a.CreateRequest(ctx, pendingReq, owner)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.db.Model(&mr.Publication{}).Where("model_id = ?", e.ModelID).Update("release_id", pending.ID).Error; err == nil {
		t.Fatal("pending review published through SQL")
	}
}
