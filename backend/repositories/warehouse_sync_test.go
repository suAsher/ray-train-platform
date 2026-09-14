package repositories

import (
 "context"
 "errors"
 "fmt"
 "strings"
 "testing"
 "time"

 "gorm.io/driver/sqlite"
 "gorm.io/gorm"
 ws "ray-train-platform-backend/warehousesync"
)

func warehouseSyncTestStore(t *testing.T) *WarehouseSyncStore {
 t.Helper()
 db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())), &gorm.Config{})
 if err != nil { t.Fatal(err) }
 sqlDB, err := db.DB(); if err != nil { t.Fatal(err) }
 sqlDB.SetMaxOpenConns(1)
 t.Cleanup(func(){ sqlDB.Close() })
 if err := db.AutoMigrate(&ws.Operation{}); err != nil { t.Fatal(err) }
 if err := db.Exec("CREATE UNIQUE INDEX warehouse_sync_request ON function_warehouse_syncs(owner_id,tenant_id,idempotency_key)").Error; err != nil { t.Fatal(err) }
 return NewWarehouseSyncStore(db)
}

func warehouseSyncRequest(id string) ws.Operation {
 return ws.Operation{ID:id, OwnerID:"owner", OwnerName:"Owner", TenantID:"team", JobID:"job-000000000000000000000001", Environment:"development", WarehouseID:"warehouse", ModelTypeID:"model", Version:"v1", Paths:[]string{"best.pth"}, State:ws.Queued, IdempotencyKey:id, RequestSHA256:strings.Repeat("a",64), Credential:[]byte("encrypted"), NextAttemptAt:time.Now().UTC()}
}

func TestWarehouseSyncIntentIdempotencyAndOwnerScope(t *testing.T) {
 s:=warehouseSyncTestStore(t); ctx:=context.Background()
 first,err:=s.Create(ctx,warehouseSyncRequest("one")); if err!=nil {t.Fatal(err)}
 replay:=warehouseSyncRequest("different-id"); replay.IdempotencyKey="one"
 old,err:=s.Create(ctx,replay); if err!=nil || old.ID!=first.ID {t.Fatalf("replay duplicated: %s %v",old.ID,err)}
 replay.RequestSHA256=strings.Repeat("b",64)
 if _,err=s.Create(ctx,replay); !errors.Is(err,ws.ErrConflict){t.Fatalf("different body: %v",err)}
 if _,err=s.Create(ctx,warehouseSyncRequest("two")); err!=nil {t.Fatal("deliberate repeat rejected",err)}
 for _,scope:=range [][3]string{{"other","team",first.JobID},{"owner","other",first.JobID},{"owner","team","another-job"}} {
  rows,err:=s.ListJob(ctx,scope[0],scope[1],scope[2]); if err!=nil || len(rows)!=0 {t.Fatal("list leaked another scope")}
 }
 rows,err:=s.ListJob(ctx,"owner","team",first.JobID); if err!=nil || len(rows)!=2 {t.Fatalf("own list %d %v",len(rows),err)}
 if _,err=s.Get(ctx,"missing"); !errors.Is(err,ws.ErrNotFound){t.Fatal(err)}
 for i:=2;i<16;i++ {if _,err=s.Create(ctx,warehouseSyncRequest(fmt.Sprint(i)));err!=nil{t.Fatal(err)}}
 if _,err=s.Create(ctx,warehouseSyncRequest("overflow")); !errors.Is(err,ws.ErrQuota){t.Fatalf("quota: %v",err)}
}

func TestWarehouseSyncLeaseCancellationAndUncertainCreation(t *testing.T) {
 s:=warehouseSyncTestStore(t);ctx:=context.Background();actor:=ws.Actor{ID:"owner",TenantID:"team"}
 _,err:=s.Create(ctx,warehouseSyncRequest("one"));if err!=nil{t.Fatal(err)}
 now:=time.Now().UTC();op,err:=s.Claim(ctx,"lease",now,now.Add(time.Minute));if err!=nil{t.Fatal(err)}
 op.State=ws.Uploading;op.Files=[]ws.File{{Name:"best.pth",Size:3}}
 if err=s.Save(ctx,op,"lease");err!=nil{t.Fatal(err)}
 if _,err=s.Cancel(ctx,op.ID,ws.Actor{ID:"intruder",TenantID:"team"});!errors.Is(err,ws.ErrNotFound){t.Fatal("cancel ownership",err)}
 canceled,err:=s.Cancel(ctx,op.ID,actor);if err!=nil||canceled.State!=ws.Canceled||len(canceled.Credential)!=0{t.Fatal("cancel",err)}
 if err=s.Save(ctx,op,"lease");!errors.Is(err,ws.ErrConflict){t.Fatal("stale writer",err)}
 if err=s.Renew(ctx,op.ID,"lease",now.Add(time.Minute));!errors.Is(err,ws.ErrConflict){t.Fatal("stale renewal",err)}
 _,err=s.Create(ctx,warehouseSyncRequest("two"));if err!=nil{t.Fatal(err)}
 op,err=s.Claim(ctx,"lease2",now.Add(time.Second),now.Add(time.Minute));if err!=nil{t.Fatal(err)}
 op.State=ws.Registering;if err=s.Save(ctx,op,"lease2");err!=nil{t.Fatal(err)}
 if _,err=s.Cancel(ctx,op.ID,actor);!errors.Is(err,ws.ErrConflict){t.Fatal("registering cancel",err)}
 if _,err=s.Claim(ctx,"next",now.Add(2*time.Minute),now.Add(3*time.Minute));!errors.Is(err,ws.ErrNotFound){t.Fatal(err)}
 expired,err:=s.Get(ctx,op.ID);if err!=nil||expired.State!=ws.Unknown||len(expired.Credential)!=0||expired.LeaseID!=""{t.Fatalf("uncertain expiry: %+v %v",expired,err)}
 if _,err=s.Resume(ctx,op.ID,actor,[]byte("new-encrypted"));!errors.Is(err,ws.ErrConflict){t.Fatal("unknown retried",err)}
}

func TestWarehouseSyncResumePreservesIntentAndExpiredLeaseFails(t *testing.T){
 s:=warehouseSyncTestStore(t);ctx:=context.Background();actor:=ws.Actor{ID:"owner",TenantID:"team"}
 _,err:=s.Create(ctx,warehouseSyncRequest("one"));if err!=nil{t.Fatal(err)}
 now:=time.Now().UTC();op,err:=s.Claim(ctx,"lease",now,now.Add(time.Minute));if err!=nil{t.Fatal(err)}
 if _,err=s.Claim(ctx,"next",now.Add(2*time.Minute),now.Add(3*time.Minute));!errors.Is(err,ws.ErrNotFound){t.Fatal(err)}
 failed,err:=s.Get(ctx,op.ID);if err!=nil||failed.State!=ws.Failed||len(failed.Credential)!=0{t.Fatal("expired source",err)}
 if _,err=s.Resume(ctx,op.ID,ws.Actor{ID:"owner",TenantID:"other"},[]byte("new"));!errors.Is(err,ws.ErrNotFound){t.Fatal("resume ownership",err)}
 resumed,err:=s.Resume(ctx,op.ID,actor,[]byte("new-encrypted"));if err!=nil||resumed.State!=ws.Queued||resumed.JobID!=op.JobID||resumed.Paths[0]!=op.Paths[0]||resumed.LeaseID!=""{t.Fatal("resume mutated intent",err)}
 if err=s.Save(ctx,op,"lease");!errors.Is(err,ws.ErrConflict){t.Fatal("old lease saved after resume",err)}
}

func TestWarehouseSyncSaveRetainsHeartbeatAndRejectsExpiredLease(t *testing.T){
 s:=warehouseSyncTestStore(t);ctx:=context.Background()
 if _,err:=s.Create(ctx,warehouseSyncRequest("one"));err!=nil{t.Fatal(err)}
 now:=time.Now().UTC();op,err:=s.Claim(ctx,"lease",now,now.Add(time.Minute));if err!=nil{t.Fatal(err)}
 extended:=now.Add(2*time.Minute)
 if err=s.Renew(ctx,op.ID,"lease",extended);err!=nil{t.Fatal(err)}
 op.State=ws.Uploading;op.Files=[]ws.File{{Name:"weight.pth",Size:1}}
 if err=s.Save(ctx,op,"lease");err!=nil{t.Fatal(err)}
 saved,err:=s.Get(ctx,op.ID);if err!=nil||saved.LeaseExpiresAt==nil||!saved.LeaseExpiresAt.Equal(extended)||len(saved.Files)!=1{t.Fatal("save lost renewal",err)}
 if err=s.db.Model(&ws.Operation{}).Where("id = ?",op.ID).Update("lease_expires_at",now.Add(-time.Minute)).Error;err!=nil{t.Fatal(err)}
 if err=s.Save(ctx,op,"lease");!errors.Is(err,ws.ErrConflict){t.Fatal("expired save",err)}
 if err=s.Renew(ctx,op.ID,"lease",extended);!errors.Is(err,ws.ErrConflict){t.Fatal("expired renewal",err)}
}

func TestWarehouseSyncResumeRejectsLeasedFailureAndExistingTarget(t *testing.T){
 s:=warehouseSyncTestStore(t);ctx:=context.Background();actor:=ws.Actor{ID:"owner",TenantID:"team"}
 if _,err:=s.Create(ctx,warehouseSyncRequest("one"));err!=nil{t.Fatal(err)}
 now:=time.Now().UTC();op,err:=s.Claim(ctx,"lease",now,now.Add(time.Minute));if err!=nil{t.Fatal(err)}
 // Even a malformed old failure cannot be resumed while its writer owns a lease.
 if err=s.db.Model(&ws.Operation{}).Where("id = ?",op.ID).Update("state",ws.Failed).Error;err!=nil{t.Fatal(err)}
 if _,err=s.Resume(ctx,op.ID,actor,[]byte("new-encrypted"));!errors.Is(err,ws.ErrConflict){t.Fatal("leased failure resumed",err)}
 if err=s.db.Model(&ws.Operation{}).Where("id = ?",op.ID).Updates(map[string]any{"state":ws.WaitingReauth,"lease_id":"","lease_expires_at":nil,"target_version_id":"existing-target"}).Error;err!=nil{t.Fatal(err)}
 if _,err=s.Resume(ctx,op.ID,actor,[]byte("new-encrypted"));!errors.Is(err,ws.ErrConflict){t.Fatal("existing target resumed",err)}
 if _,err=s.Cancel(ctx,op.ID,actor);!errors.Is(err,ws.ErrConflict){t.Fatal("existing target canceled",err)}
}
