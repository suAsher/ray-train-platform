package repositories

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	ml "ray-train-platform-backend/modellifecycle"
)

func modelTestStore(t *testing.T) *ModelLifecycleStore {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { sqlDB.Close() })
	if err := db.AutoMigrate(&ml.Model{}, &ml.Version{}, &modelAudit{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("CREATE UNIQUE INDEX model_catalog_request_idx ON model_catalog(owner_id, idempotency_key) WHERE idempotency_key <> ''").Error; err != nil {
		t.Fatal(err)
	}
	return NewModelLifecycleStore(db)
}
func TestModelCatalogSharedCASAndAudit(t *testing.T) {
	s := modelTestStore(t)
	ctx := context.Background()
	m, err := s.CreateModel(ctx, ml.Model{Name: "shared", OwnerID: "stable-owner", OwnerName: "Owner", TenantID: "old-team"})
	if err != nil {
		t.Fatal(err)
	}
	page, err := s.ListModels(ctx, ml.Filter{})
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("shared read: %+v %v", page, err)
	}
	name := "renamed"
	m, err = s.UpdateModel(ctx, m.ID, ml.ModelUpdate{Name: &name, Revision: m.Revision}, ml.Actor{ID: "stable-owner"})
	if err != nil {
		t.Fatal(err)
	}
	if m.OwnerID != "stable-owner" || m.TenantID != "old-team" {
		t.Fatal("identity mutated")
	}
	if _, err = s.UpdateModel(ctx, m.ID, ml.ModelUpdate{Name: &name, Revision: 1}, ml.Actor{ID: "stable-owner"}); !errors.Is(err, ml.ErrConflict) {
		t.Fatal(err)
	}
	var n int64
	s.db.Model(&modelAudit{}).Count(&n)
	if n != 2 {
		t.Fatalf("audit count %d", n)
	}
}
func TestModelReservationIdempotencyQuotaAndLease(t *testing.T) {
	s := modelTestStore(t)
	ctx := context.Background()
	m, err := s.CreateModel(ctx, ml.Model{Name: "model", OwnerID: "owner", TenantID: "team"})
	if err != nil {
		t.Fatal(err)
	}
	request := ml.Version{SourceETag: "source-etag", ModelID: m.ID, CreatorID: "owner", JobID: "job", FileName: "weights", SourceRoot: "/private", RelativePath: "weights", SizeBytes: 1, IdempotencyKey: "key", RequestSHA256: "fingerprint"}
	v, err := s.ReserveVersion(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.ReserveVersion(ctx, request)
	if err != nil || again.ID != v.ID {
		t.Fatalf("duplicate: %+v %v", again, err)
	}
	request.RequestSHA256 = "different"
	if _, err := s.ReserveVersion(ctx, request); !errors.Is(err, ml.ErrConflict) {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	claimed, err := s.ClaimVersion(ctx, "lease", now, now.Add(time.Minute))
	if err != nil || claimed.ID != v.ID {
		t.Fatal(err)
	}
	if _, err = s.ClaimVersion(ctx, "other", now, now.Add(time.Minute)); !errors.Is(err, ml.ErrNotFound) {
		t.Fatal(err)
	}
	if err = s.FinishVersion(ctx, v.ID, "other", ml.Failed, nil, "", now); !errors.Is(err, ml.ErrConflict) {
		t.Fatal(err)
	}
	if _, err = s.ClaimVersion(ctx, "new", now.Add(2*time.Minute), now.Add(3*time.Minute)); !errors.Is(err, ml.ErrNotFound) {
		t.Fatal(err)
	}
	failed, err := s.GetVersion(ctx, m.ID, v.ID)
	if err != nil || failed.State != ml.Failed {
		t.Fatalf("expired lease not failed: %+v %v", failed, err)
	}
	if err = s.FinishVersion(ctx, v.ID, "lease", ml.Failed, nil, "", now); !errors.Is(err, ml.ErrConflict) {
		t.Fatal("stale owner published")
	}
	for i := 0; i < ml.MaxPending; i++ {
		request.IdempotencyKey = fmt.Sprint(i)
		if _, err := s.ReserveVersion(ctx, request); err != nil {
			t.Fatal(err)
		}
	}
	request.IdempotencyKey = "overflow"
	if _, err := s.ReserveVersion(ctx, request); !errors.Is(err, ml.ErrQuota) {
		t.Fatal(err)
	}
}

func TestModelDescriptionsUseCharacterLimits(t *testing.T) {
	s := modelTestStore(t)
	ctx := context.Background()
	text := strings.Repeat("模", 4000)
	m, err := s.CreateModel(ctx, ml.Model{Name: strings.Repeat("型", 200), Description: text, OwnerID: "owner", TenantID: "team"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateModel(ctx, ml.Model{Name: strings.Repeat("型", 201), OwnerID: "owner", TenantID: "team"}); !errors.Is(err, ml.ErrInvalid) {
		t.Fatal("201 character name accepted")
	}
	if _, err := s.UpdateModel(ctx, m.ID, ml.ModelUpdate{Description: &text, Revision: m.Revision}, ml.Actor{ID: "owner"}); err != nil {
		t.Fatal(err)
	}
	tooLong := text + "型"
	if _, err := s.UpdateModel(ctx, m.ID, ml.ModelUpdate{Description: &tooLong, Revision: m.Revision + 1}, ml.Actor{ID: "owner"}); !errors.Is(err, ml.ErrInvalid) {
		t.Fatal("4001 character description accepted")
	}
	v, err := s.ReserveVersion(ctx, ml.Version{SourceETag: "source-etag", ModelID: m.ID, CreatorID: "owner", SizeBytes: 1, IdempotencyKey: "key", RequestSHA256: "hash"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateVersion(ctx, m.ID, v.ID, ml.VersionUpdate{Description: text, Revision: v.Revision}, ml.Actor{ID: "owner"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateVersion(ctx, m.ID, v.ID, ml.VersionUpdate{Description: tooLong, Revision: v.Revision + 1}, ml.Actor{ID: "owner"}); !errors.Is(err, ml.ErrInvalid) {
		t.Fatal("4001 character version description accepted")
	}
}

func TestCreateModelIdempotencyIsOwnerScopedAndAuditedOnce(t *testing.T) {
	s := modelTestStore(t)
	ctx := context.Background()
	request := ml.Model{Name: "Model", Description: "description", OwnerID: "owner", OwnerName: "Old name", TenantID: "team", IdempotencyKey: "model-request"}
	first, err := s.CreateModel(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	request.OwnerName = "New display name"
	replayed, err := s.CreateModel(ctx, request)
	if err != nil || replayed.ID != first.ID || replayed.OwnerName != first.OwnerName || len(replayed.RequestSHA256) != 64 {
		t.Fatalf("lost-response retry duplicated model: %+v %v", replayed, err)
	}
	for _, change := range []func(*ml.Model){
		func(m *ml.Model) { m.Name = "different" },
		func(m *ml.Model) { m.Name = " Model " },
		func(m *ml.Model) { m.Description = "different" },
		func(m *ml.Model) { m.TenantID = "other-team" },
	} {
		changed := request
		change(&changed)
		if _, err := s.CreateModel(ctx, changed); !errors.Is(err, ml.ErrConflict) {
			t.Fatalf("request key accepted different body: %v", err)
		}
	}
	var count int64
	if err := s.db.Model(&modelAudit{}).Where("model_id = ?", first.ID).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("retries wrote audit rows: %d %v", count, err)
	}
	request.OwnerID = "another-owner"
	other, err := s.CreateModel(ctx, request)
	if err != nil || other.ID == first.ID {
		t.Fatalf("key is not owner-scoped: %+v %v", other, err)
	}
	request.IdempotencyKey = ""
	one, err := s.CreateModel(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	two, err := s.CreateModel(ctx, request)
	if err != nil || one.ID == two.ID || one.RequestSHA256 != "" {
		t.Fatalf("keyless internal creates are not independent: %+v %v", two, err)
	}
}

func TestModelCreateIdempotencyFieldsArePrivate(t *testing.T) {
	encoded, err := json.Marshal(ml.Model{IdempotencyKey: "private-key", RequestSHA256: "private-hash"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "private-") {
		t.Fatalf("private request identity exposed: %s", encoded)
	}
}
