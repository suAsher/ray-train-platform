package repositories

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"
	databasepkg "ray-train-platform-backend/db"
	ws "ray-train-platform-backend/warehousesync"
)

func TestWarehouseSyncPostgresConcurrentQuotaClaimsAndCAS(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("POSTGRES_TEST_DSN"))
	if dsn == "" {
		t.Skip("POSTGRES_TEST_DSN is not set")
	}
	admin := openArtifactPostgresConnection(t, dsn)
	schema := fmt.Sprintf("warehouse_sync_%d", time.Now().UnixNano())
	if err := admin.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := admin.Exec("DROP SCHEMA " + schema + " CASCADE").Error; err != nil {
			t.Error(err)
		}
	})
	first, second := openArtifactPostgresConnection(t, dsn), openArtifactPostgresConnection(t, dsn)
	for _, db := range []*gorm.DB{first, second} {
		if err := db.Exec("SET search_path TO " + schema).Error; err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 2; i++ {
		if err := databasepkg.ApplyMigrations(first); err != nil {
			t.Fatal(err)
		}
	}
	a, b := NewWarehouseSyncStore(first), NewWarehouseSyncStore(second)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	type outcome struct {
		op  ws.Operation
		err error
	}
	results := make(chan outcome, 2)
	start := make(chan struct{})
	for i, s := range []*WarehouseSyncStore{a, b} {
		go func(i int, s *WarehouseSyncStore) {
			<-start
			op := warehouseSyncRequest(fmt.Sprintf("same-%d", i))
			op.IdempotencyKey = "same-request"
			r, e := s.Create(ctx, op)
			results <- outcome{r, e}
		}(i, s)
	}
	close(start)
	r1, r2 := <-results, <-results
	if r1.err != nil || r2.err != nil || r1.op.ID != r2.op.ID {
		t.Fatalf("concurrent replay: %s/%s %v/%v", r1.op.ID, r2.op.ID, r1.err, r2.err)
	}
	for i := 1; i < 15; i++ {
		if _, err := a.Create(ctx, warehouseSyncRequest(fmt.Sprintf("quota-%d", i))); err != nil {
			t.Fatal(err)
		}
	}
	start = make(chan struct{})
	for i, s := range []*WarehouseSyncStore{a, b} {
		go func(i int, s *WarehouseSyncStore) {
			<-start
			op := warehouseSyncRequest(fmt.Sprintf("boundary-%d", i))
			op.TenantID = fmt.Sprintf("team-%d", i)
			r, e := s.Create(ctx, op)
			results <- outcome{r, e}
		}(i, s)
	}
	close(start)
	success, quota := 0, 0
	for i := 0; i < 2; i++ {
		r := <-results
		if r.err == nil {
			success++
		} else if errors.Is(r.err, ws.ErrQuota) {
			quota++
		} else {
			t.Fatal(r.err)
		}
	}
	if success != 1 || quota != 1 {
		t.Fatalf("quota race: success %d quota %d", success, quota)
	}
	start = make(chan struct{})
	now := time.Now().UTC().Truncate(time.Microsecond)
	for i, s := range []*WarehouseSyncStore{a, b} {
		go func(i int, s *WarehouseSyncStore) {
			<-start
			r, e := s.Claim(ctx, fmt.Sprintf("lease-%d", i), now, now.Add(time.Minute))
			results <- outcome{r, e}
		}(i, s)
	}
	close(start)
	r1, r2 = <-results, <-results
	if r1.err != nil || r2.err != nil || r1.op.ID == r2.op.ID {
		t.Fatalf("claim race %s/%s %v/%v", r1.op.ID, r2.op.ID, r1.err, r2.err)
	}
	op := r1.op
	extended := now.Add(2 * time.Minute)
	if err := a.Renew(ctx, op.ID, op.LeaseID, extended); err != nil {
		t.Fatal(err)
	}
	op.State = ws.Uploading
	op.Files = []ws.File{{Name: "best.pth", Size: 3}}
	if err := b.Save(ctx, op, op.LeaseID); err != nil {
		t.Fatal(err)
	}
	saved, err := a.Get(ctx, op.ID)
	if err != nil || saved.LeaseExpiresAt == nil || !saved.LeaseExpiresAt.Equal(extended) || len(saved.Files) != 1 {
		t.Fatalf("save lost renewal or JSON files: %v", err)
	}
	if err := first.Model(&ws.Operation{}).Where("id = ?", op.ID).Update("job_id", "changed-source").Error; err == nil {
		t.Fatal("frozen source changed")
	}
	actor := ws.Actor{ID: op.OwnerID, TenantID: op.TenantID}
	if _, err := a.Cancel(ctx, op.ID, actor); err != nil {
		t.Fatal(err)
	}
	if err := b.Save(ctx, op, op.LeaseID); !errors.Is(err, ws.ErrConflict) {
		t.Fatalf("canceled writer saved: %v", err)
	}
	if err := b.Renew(ctx, op.ID, op.LeaseID, extended); !errors.Is(err, ws.ErrConflict) {
		t.Fatalf("canceled writer renewed: %v", err)
	}
	// Registration and cancellation race on separate DB connections. Exactly one
	// may win; a canceled result must never coexist with an accepted registration.
	registering := r2.op
	registering.State = ws.Registering
	start = make(chan struct{})
	race := make(chan error, 2)
	go func() { <-start; race <- a.Save(ctx, registering, registering.LeaseID) }()
	go func() {
		<-start
		_, err := b.Cancel(ctx, registering.ID, ws.Actor{ID: registering.OwnerID, TenantID: registering.TenantID})
		race <- err
	}()
	close(start)
	success, conflicts := 0, 0
	for i := 0; i < 2; i++ {
		err := <-race
		if err == nil {
			success++
		} else if errors.Is(err, ws.ErrConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || conflicts != 1 {
		t.Fatalf("cancel/register race success %d conflicts %d", success, conflicts)
	}
	final, err := a.Get(ctx, registering.ID)
	if err != nil || (final.State != ws.Registering && final.State != ws.Canceled) {
		t.Fatalf("invalid race result: %s %v", final.State, err)
	}
}
