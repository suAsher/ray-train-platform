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
 ml "ray-train-platform-backend/modellifecycle"
)
func TestModelLifecyclePostgresConcurrentQuotaClaimCAS(t *testing.T) {
 dsn:=strings.TrimSpace(os.Getenv("POSTGRES_TEST_DSN"));if dsn==""{t.Skip("POSTGRES_TEST_DSN is not set")}
 admin:=openArtifactPostgresConnection(t,dsn);schema:=fmt.Sprintf("model_lifecycle_%d",time.Now().UnixNano())
 if err:=admin.Exec("CREATE SCHEMA "+schema).Error;err!=nil{t.Fatal(err)}
 t.Cleanup(func(){if err:=admin.Exec("DROP SCHEMA "+schema+" CASCADE").Error;err!=nil{t.Error(err)}})
 first:=openArtifactPostgresConnection(t,dsn);second:=openArtifactPostgresConnection(t,dsn)
 for _,db:=range []*gorm.DB{first,second}{if err:=db.Exec("SET search_path TO "+schema).Error;err!=nil{t.Fatal(err)}}
 if err:=databasepkg.ApplyMigrations(first);err!=nil{t.Fatal(err)}
 a,b:=NewModelLifecycleStore(first),NewModelLifecycleStore(second);ctx,cancel:=context.WithTimeout(context.Background(),30*time.Second);defer cancel()
 m,err:=a.CreateModel(ctx,ml.Model{Name:"first",OwnerID:"stable-owner",TenantID:"old-team"});if err!=nil{t.Fatal(err)}
 other,err:=a.CreateModel(ctx,ml.Model{Name:"second",OwnerID:"stable-owner",TenantID:"new-team"});if err!=nil{t.Fatal(err)}
 makeRequest:=func(modelID,key string)ml.Version{return ml.Version{SourceETag:"source-etag",ModelID:modelID,CreatorID:"creator",JobID:"job",FileName:"weights",SourceRoot:"/server/private",RelativePath:"weights",IdempotencyKey:key,RequestSHA256:strings.Repeat("a",64),SizeBytes:ml.MaxFileSize}}
 // 80 GiB reserved across models sharing the same stable owner.
 for i:=0;i<4;i++{if _,err:=a.ReserveVersion(ctx,makeRequest(m.ID,fmt.Sprint(i)));err!=nil{t.Fatal(err)}}
 start:=make(chan struct{});results:=make(chan error,2)
 go func(){<-start;_,err:=a.ReserveVersion(ctx,makeRequest(m.ID,"concurrent-a"));results<-err}()
 go func(){<-start;_,err:=b.ReserveVersion(ctx,makeRequest(other.ID,"concurrent-b"));results<-err}()
 close(start);success,quota:=0,0
 for i:=0;i<2;i++{err:=<-results;if err==nil{success++}else if errors.Is(err,ml.ErrQuota){quota++}else{t.Fatal(err)}}
 if success!=1 || quota!=1{t.Fatalf("quota race: success %d rejected %d",success,quota)}
 // Separate connections claim distinct pending rows.
 versions:=make(chan ml.Version,2);start=make(chan struct{})
 for _,store:=range []*ModelLifecycleStore{a,b}{go func(s *ModelLifecycleStore){<-start;now:=time.Now().UTC();v,err:=s.ClaimVersion(ctx,fmt.Sprintf("lease-%p",s),now,now.Add(time.Minute));versions<-v;results<-err}(store)}
 close(start);v1,v2:=<-versions,<-versions
 for i:=0;i<2;i++{if err:=<-results;err!=nil{t.Fatal(err)}}
 if v1.ID=="" || v1.ID==v2.ID{t.Fatal("duplicate claim")}
 name:="concurrent rename";start=make(chan struct{})
 for _,store:=range []*ModelLifecycleStore{a,b}{go func(s *ModelLifecycleStore){<-start;_,err:=s.UpdateModel(ctx,m.ID,ml.ModelUpdate{Name:&name,Revision:m.Revision},ml.Actor{ID:"stable-owner"});results<-err}(store)}
 close(start);success,conflicts:=0,0
 for i:=0;i<2;i++{err:=<-results;if err==nil{success++}else if errors.Is(err,ml.ErrConflict){conflicts++}else{t.Fatal(err)}}
 if success!=1 || conflicts!=1{t.Fatalf("CAS race: success %d conflicts %d",success,conflicts)}
 // Pending-count reservations also serialize across separate models of one owner.
 smallA,err:=a.CreateModel(ctx,ml.Model{Name:"pending-a",OwnerID:"pending-owner",TenantID:"a"});if err!=nil{t.Fatal(err)}
 smallB,err:=a.CreateModel(ctx,ml.Model{Name:"pending-b",OwnerID:"pending-owner",TenantID:"b"});if err!=nil{t.Fatal(err)}
 for i:=0;i<ml.MaxPending-1;i++ {r:=makeRequest(smallA.ID,fmt.Sprint(i));r.SizeBytes=1;if _,err:=a.ReserveVersion(ctx,r);err!=nil{t.Fatal(err)}}
 start=make(chan struct{})
 for i,store:=range []*ModelLifecycleStore{a,b} {go func(i int,s *ModelLifecycleStore){<-start;id:=smallA.ID;if i==1{id=smallB.ID};r:=makeRequest(id,"pending-boundary");r.SizeBytes=1;_,err:=s.ReserveVersion(ctx,r);results<-err}(i,store)}
 close(start);success,quota=0,0
 for i:=0;i<2;i++ {err:=<-results;if err==nil{success++}else if errors.Is(err,ml.ErrQuota){quota++}else{t.Fatal(err)}}
 if success!=1 || quota!=1{t.Fatalf("pending race: success %d rejected %d",success,quota)}
 // Concurrent requests on the same model allocate distinct monotonically increasing numbers.
 numbered,err:=a.CreateModel(ctx,ml.Model{Name:"numbered",OwnerID:"number-owner",TenantID:"a"});if err!=nil{t.Fatal(err)}
 start=make(chan struct{})
 for i,store:=range []*ModelLifecycleStore{a,b} {go func(i int,s *ModelLifecycleStore){<-start;r:=makeRequest(numbered.ID,fmt.Sprint(i));r.SizeBytes=1;v,err:=s.ReserveVersion(ctx,r);versions<-v;results<-err}(i,store)}
 close(start);n1,n2:=<-versions,<-versions
 for i:=0;i<2;i++{if err:=<-results;err!=nil{t.Fatal(err)}}
 if n1.Number+n2.Number!=3 || n1.Number==n2.Number{t.Fatalf("duplicate version sequence %d %d",n1.Number,n2.Number)}
 // An arbitrary metadata mutation cannot change frozen source provenance.
 if err:=first.Model(&ml.Version{}).Where("id = ?",v1.ID).Update("relative_path","other").Error;err==nil{t.Fatal("source provenance mutable")}
 // The expired worker cannot publish, and failed snapshots continue charging quota.
 future:=time.Now().UTC().Add(3*time.Minute)
 if _,err:=a.ClaimVersion(ctx,"recover",future,future.Add(time.Minute));err!=nil{t.Fatal(err)}
 if err:=b.FinishVersion(ctx,v1.ID,v1.LeaseID,ml.Failed,nil,"",future);!errors.Is(err,ml.ErrConflict){t.Fatalf("stale lease: %v",err)}
 if _,err:=a.ReserveVersion(ctx,makeRequest(m.ID,"after-failed"));!errors.Is(err,ml.ErrQuota){t.Fatalf("failed reservation escaped quota: %v",err)}
}
