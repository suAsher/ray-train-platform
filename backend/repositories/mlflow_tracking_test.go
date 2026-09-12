package repositories

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	tracking "ray-train-platform-backend/mlflowtracking"
)

func trackingStoreFixture(t *testing.T)*MLflowTrackingStore {
	t.Helper()
	db,err:=gorm.Open(sqlite.Open(filepath.Join(t.TempDir(),"tracking.db")),&gorm.Config{Logger:logger.Default.LogMode(logger.Silent)});if err!=nil {t.Fatal(err)}
	if err:=db.AutoMigrate(&MLflowTrackingExperimentRecord{},&MLflowTrackingRunRecord{});err!=nil {t.Fatal(err)}
	return NewMLflowTrackingStore(db)
}

func TestMLflowTrackingReservationsAndLeaseCAS(t *testing.T) {
	ctx:=context.Background();store:=trackingStoreFixture(t);actor:=tracking.Actor{TenantID:"team",UserID:"owner"};now:=time.Now().UTC()
	exp:=tracking.Experiment{ID:strings.Repeat("a",32),TenantID:actor.TenantID,UserID:actor.UserID,IdempotencyHash:strings.Repeat("e",64),Name:"private",State:"PENDING",CreatedAt:now,UpdatedAt:now}
	got,claimed,err:=store.ReserveExperiment(ctx,exp);if err!=nil||!claimed||got.ID!=exp.ID {t.Fatalf("reservation=%+v %v %v",got,claimed,err)}
	if _,err:=store.CompleteExperiment(ctx,actor,exp.ID,"1");err!=nil {t.Fatal(err)}
	run:=tracking.Run{ID:strings.Repeat("b",32),ExperimentID:exp.ID,TenantID:actor.TenantID,UserID:actor.UserID,IdempotencyHash:strings.Repeat("f",64),Name:"one",State:"PENDING",CreatedAt:now,UpdatedAt:now}
	if _,claimed,err:=store.ReserveRun(ctx,run);err!=nil||!claimed {t.Fatal(err)}
	if _,err:=store.CompleteRun(ctx,actor,run.ID,strings.Repeat("c",32));err!=nil {t.Fatal(err)}
	if _,err:=store.ClaimRunLease(ctx,actor,run.ID,"lease-one","",now,now.Add(time.Minute),0);err!=nil {t.Fatal(err)}
	if _,err:=store.ClaimRunLease(ctx,actor,run.ID,"lease-two","FINISHED",now,now.Add(time.Minute),now.UnixMilli());!errors.Is(err,tracking.ErrBusy) {t.Fatalf("parallel mutation accepted: %v",err)}
	if _,err:=store.ReleaseRunLease(ctx,actor,run.ID,"not-the-lease","",now);!errors.Is(err,tracking.ErrBusy) {t.Fatalf("wrong lease release=%v",err)}
	if _,err:=store.ReleaseRunLease(ctx,actor,run.ID,"lease-one","",now);err!=nil {t.Fatal(err)}
	finishing,err:=store.ClaimRunLease(ctx,actor,run.ID,"lease-finish","FINISHED",now,now.Add(time.Minute),now.UnixMilli());if err!=nil||finishing.State!="FINISHING" {t.Fatalf("finish intent not durable: %+v %v",finishing,err)}
	if _,err:=store.ReleaseRunLease(ctx,actor,run.ID,"lease-finish","",now);err!=nil {t.Fatal(err)}
	if _,err:=store.ClaimRunLease(ctx,actor,run.ID,"lease-log","",now,now.Add(time.Minute),0);!errors.Is(err,tracking.ErrConflict) {t.Fatalf("finishing reopened: %v",err)}
}
