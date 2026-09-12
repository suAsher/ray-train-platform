package repositories

import (
 "context"
 "errors"
 "fmt"
 "testing"
 "time"

 "gorm.io/driver/sqlite"
 "gorm.io/gorm"
 ml "ray-train-platform-backend/modellifecycle"
)
func modelTestStore(t *testing.T)*ModelLifecycleStore {
 t.Helper();db,err:=gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared",t.Name())),&gorm.Config{});if err!=nil{t.Fatal(err)}
 sqlDB,err:=db.DB();if err!=nil{t.Fatal(err)};sqlDB.SetMaxOpenConns(1);t.Cleanup(func(){sqlDB.Close()})
 if err:=db.AutoMigrate(&ml.Model{},&ml.Version{},&modelAudit{});err!=nil{t.Fatal(err)}
 return NewModelLifecycleStore(db)
}
func TestModelCatalogSharedCASAndAudit(t *testing.T) {
 s:=modelTestStore(t);ctx:=context.Background()
 m,err:=s.CreateModel(ctx,ml.Model{Name:"shared",OwnerID:"stable-owner",OwnerName:"Owner",TenantID:"old-team"});if err!=nil{t.Fatal(err)}
 page,err:=s.ListModels(ctx,ml.Filter{});if err!=nil || len(page.Items)!=1{t.Fatalf("shared read: %+v %v",page,err)}
 name:="renamed";m,err=s.UpdateModel(ctx,m.ID,ml.ModelUpdate{Name:&name,Revision:m.Revision},ml.Actor{ID:"stable-owner"});if err!=nil{t.Fatal(err)}
 if m.OwnerID!="stable-owner" || m.TenantID!="old-team" {t.Fatal("identity mutated")}
 if _,err=s.UpdateModel(ctx,m.ID,ml.ModelUpdate{Name:&name,Revision:1},ml.Actor{ID:"stable-owner"});!errors.Is(err,ml.ErrConflict){t.Fatal(err)}
 var n int64;s.db.Model(&modelAudit{}).Count(&n);if n!=2{t.Fatalf("audit count %d",n)}
}
func TestModelReservationIdempotencyQuotaAndLease(t *testing.T) {
 s:=modelTestStore(t);ctx:=context.Background();m,err:=s.CreateModel(ctx,ml.Model{Name:"model",OwnerID:"owner",TenantID:"team"});if err!=nil{t.Fatal(err)}
 request:=ml.Version{ModelID:m.ID,CreatorID:"owner",JobID:"job",FileName:"weights",SourceRoot:"/private",RelativePath:"weights",SizeBytes:1,IdempotencyKey:"key",RequestSHA256:"fingerprint"}
 v,err:=s.ReserveVersion(ctx,request);if err!=nil{t.Fatal(err)}
 again,err:=s.ReserveVersion(ctx,request);if err!=nil || again.ID!=v.ID{t.Fatalf("duplicate: %+v %v",again,err)}
 request.RequestSHA256="different";if _,err:=s.ReserveVersion(ctx,request);!errors.Is(err,ml.ErrConflict){t.Fatal(err)}
 now:=time.Now().UTC();claimed,err:=s.ClaimVersion(ctx,"lease",now,now.Add(time.Minute));if err!=nil || claimed.ID!=v.ID{t.Fatal(err)}
 if _,err=s.ClaimVersion(ctx,"other",now,now.Add(time.Minute));!errors.Is(err,ml.ErrNotFound){t.Fatal(err)}
 if err=s.FinishVersion(ctx,v.ID,"other",ml.Failed,nil,"",now);!errors.Is(err,ml.ErrConflict){t.Fatal(err)}
 if _,err=s.ClaimVersion(ctx,"new",now.Add(2*time.Minute),now.Add(3*time.Minute));!errors.Is(err,ml.ErrNotFound){t.Fatal(err)}
 failed,err:=s.GetVersion(ctx,m.ID,v.ID);if err!=nil || failed.State!=ml.Failed{t.Fatalf("expired lease not failed: %+v %v",failed,err)}
 if err=s.FinishVersion(ctx,v.ID,"lease",ml.Failed,nil,"",now);!errors.Is(err,ml.ErrConflict){t.Fatal("stale owner published")}
 for i:=0;i<ml.MaxPending;i++ {request.IdempotencyKey=fmt.Sprint(i);if _,err:=s.ReserveVersion(ctx,request);err!=nil{t.Fatal(err)}}
 request.IdempotencyKey="overflow";if _,err:=s.ReserveVersion(ctx,request);!errors.Is(err,ml.ErrQuota){t.Fatal(err)}
}
