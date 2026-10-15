package repositories

import (
 "context"
 "errors"
 "testing"
 ps "ray-train-platform-backend/personalstorage"
 "gorm.io/driver/sqlite"
 "gorm.io/gorm"
)
func TestPersonalStorageRepositoryOwnerIdentityAndCAS(t *testing.T){
 database,err:=gorm.Open(sqlite.Open("file:personal-storage-owner?mode=memory&cache=shared"),&gorm.Config{});if err!=nil{t.Fatal(err)}
 sqlDB,err:=database.DB();if err!=nil{t.Fatal(err)};defer sqlDB.Close()
 if err:=database.AutoMigrate(&personalStorageRecord{});err!=nil{t.Fatal(err)}
 r:=NewPersonalStorageRepository(database);ctx:=context.Background()
 b:=ps.Binding{ID:"binding",OwnerID:"owner",Username:"guofeng.su",SecretName:"secret",PublicKey:"public",Fingerprint:"fingerprint",Generation:1,State:ps.StatePending}
 if _,err:=r.Create(ctx,b);err!=nil{t.Fatal(err)}
 if _,err:=r.Get(ctx,"other");!errors.Is(err,ps.ErrNotFound){t.Fatalf("foreign owner read: %v",err)}
 if _,err:=r.MarkVerified(ctx,"owner","other",1);!errors.Is(err,ps.ErrConflict){t.Fatalf("wrong binding updated: %v",err)}
 if _,err:=r.MarkVerified(ctx,"owner","binding",2);!errors.Is(err,ps.ErrConflict){t.Fatalf("stale generation updated: %v",err)}
 ready,err:=r.MarkVerified(ctx,"owner","binding",1);if err!=nil||ready.State!=ps.StateReady{t.Fatalf("verify failed: %v",err)}
 b.ID="losing-enrollment";winner,err:=r.Create(ctx,b);if err!=nil||winner.ID!="binding"{t.Fatalf("idempotency lost: %+v %v",winner,err)}
}
