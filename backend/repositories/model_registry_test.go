package repositories

import (
	"context"
	"errors"
	ml "ray-train-platform-backend/modellifecycle"
	mr "ray-train-platform-backend/modelregistry"
	"testing"
	"time"
)

func TestModelRegistryLeaseAndOwnership(t *testing.T) {
	base := modelTestStore(t)
	if err := base.db.AutoMigrate(&mr.Record{}); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	m := ml.Model{ID: "model", Name: "test", OwnerID: "owner", TenantID: "team"}
	v := ml.Version{ID: "version", ModelID: m.ID, State: ml.Ready}
	if err := base.db.Create(&m).Error; err != nil {
		t.Fatal(err)
	}
	if err := base.db.Create(&v).Error; err != nil {
		t.Fatal(err)
	}
	s := NewModelRegistryStore(base.db)
	if _, _, err := s.Acquire(ctx, m.ID, v.ID, "other", false); !errors.Is(err, ml.ErrConflict) {
		t.Fatalf("owner guard: %v", err)
	}
	first, ok, err := s.Acquire(ctx, m.ID, v.ID, "owner", false)
	if err != nil || !ok || first.LeaseID == "" {
		t.Fatalf("acquire %v %v", ok, err)
	}
	second, ok, err := s.Acquire(ctx, m.ID, v.ID, "owner", false)
	if err != nil || ok || second.LeaseID != first.LeaseID {
		t.Fatalf("duplicate lease %v %v", ok, err)
	}
	if err := s.Renew(ctx, v.ID, "wrong"); !errors.Is(err, mr.ErrConflict) {
		t.Fatalf("wrong lease renewal %v", err)
	}
	if err := s.Renew(ctx, v.ID, first.LeaseID); err != nil {
		t.Fatal(err)
	}
	link := mr.Link{RegisteredName: "rt-model", Version: "1", SourceURI: "runs:/run/model", RunID: "run"}
	if err := s.Complete(ctx, v.ID, "wrong", link, ""); !errors.Is(err, mr.ErrConflict) {
		t.Fatalf("wrong completion %v", err)
	}
	if err := s.Complete(ctx, v.ID, first.LeaseID, link, ""); err != nil {
		t.Fatal(err)
	}
	ready, ok, err := s.Acquire(ctx, m.ID, v.ID, "owner", false)
	if err != nil || ok || ready.RegistryVersion != "1" || ready.State != "READY" {
		t.Fatalf("ready retry %#v %v %v", ready, ok, err)
	}
}
func TestModelRegistryExpiredLeaseRejectsOldWorker(t *testing.T) {
	base := modelTestStore(t)
	if err := base.db.AutoMigrate(&mr.Record{}); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	m := ml.Model{ID: "model", Name: "test", OwnerID: "owner", TenantID: "team"}
	v := ml.Version{ID: "version", ModelID: m.ID, State: ml.Ready}
	if err := base.db.Create(&m).Error; err != nil {
		t.Fatal(err)
	}
	if err := base.db.Create(&v).Error; err != nil {
		t.Fatal(err)
	}
	s := NewModelRegistryStore(base.db)
	first, _, err := s.Acquire(ctx, m.ID, v.ID, "owner", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := base.db.Model(&mr.Record{}).Where("version_id = ?", v.ID).Update("lease_expires_at", time.Now().Add(-time.Minute)).Error; err != nil {
		t.Fatal(err)
	}
	next, ok, err := s.Acquire(ctx, m.ID, v.ID, "owner", false)
	if err != nil || !ok || next.LeaseID == first.LeaseID {
		t.Fatalf("retry failed %v %v", ok, err)
	}
	if err := s.Complete(ctx, v.ID, first.LeaseID, mr.Link{}, "stale failure"); !errors.Is(err, mr.ErrConflict) {
		t.Fatalf("stale completion %v", err)
	}
	if err := s.Complete(ctx, v.ID, next.LeaseID, mr.Link{}, "retryable failure"); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.Acquire(ctx, m.ID, v.ID, "owner", false); err != nil || !ok {
		t.Fatalf("failure not retryable %v %v", ok, err)
	}
}
