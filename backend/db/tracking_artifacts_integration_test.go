package db

import (
 "context"
 "errors"
 "fmt"
 "sync"
 "testing"
 "time"

 "ray-train-platform-backend/repositories"
 "ray-train-platform-backend/trackingartifacts"
)
func TestTrackingArtifactReservationsIntegration(t *testing.T){
 database:=openPostgresTestSchema(t);if err:=ApplyMigrations(database);err!=nil{t.Fatal(err)}
 if err:=database.Exec(`INSERT INTO tenants(id,name,namespace,local_queue) VALUES ('artifact-test','Artifact test','artifact-test','artifact-test')`).Error;err!=nil{t.Fatal(err)}
 const exp="11111111111111111111111111111111"; const run="22222222222222222222222222222222"
 if err:=database.Exec(`INSERT INTO mlflow_tracking_experiments(id,tenant_id,user_id,idempotency_hash,name,state,upstream_id) VALUES (?,'artifact-test','owner',?,'artifact-test','READY','100001')`,exp,fmt.Sprintf("%064x",1)).Error;err!=nil{t.Fatal(err)}
 if err:=database.Exec(`INSERT INTO mlflow_tracking_runs(id,experiment_id,tenant_id,user_id,idempotency_hash,name,state,upstream_id) VALUES (?,?,'artifact-test','owner',?,'artifact-test','RUNNING',?)`,run,exp,fmt.Sprintf("%064x",2),run).Error;err!=nil{t.Fatal(err)}
 store:=repositories.NewTrackingArtifactStore(database);scope:=trackingartifacts.Scope{TenantID:"artifact-test",OwnerID:"owner",RunID:run};now:=time.Now().UTC()
 makeRecord:=func(i int)trackingartifacts.Record{return trackingartifacts.Record{Artifact:trackingartifacts.Artifact{ID:fmt.Sprintf("%032x",i+100),RunID:run,Name:fmt.Sprintf("model-%d.bin",i),SizeBytes:trackingartifacts.MaxFileBytes,SHA256:fmt.Sprintf("%064x",1),State:"PENDING",PartSizeBytes:trackingartifacts.PartSizeBytes,TotalParts:2560,ExpiresAt:now.Add(24*time.Hour),CreatedAt:now},Scope:scope,IdempotencyHash:fmt.Sprintf("%064x",i+100)}}
 var schema string;if err:=database.Raw("SELECT current_schema()").Scan(&schema).Error;err!=nil{t.Fatal(err)};stores:=make([]*repositories.TrackingArtifactStore,8);for i:=range stores{conn:=openIndependentPostgres(t,postgresTestDSN(t));if err:=conn.Exec("SET search_path TO "+quotePostgresIdentifier(schema)).Error;err!=nil{t.Fatal(err)};stores[i]=repositories.NewTrackingArtifactStore(conn)}
 var wg sync.WaitGroup;results:=make(chan error,8);for i:=0;i<8;i++{wg.Add(1);go func(i int){defer wg.Done();_,err:=stores[i].Reserve(context.Background(),makeRecord(i));results<-err}(i)};wg.Wait();close(results);success,quota:=0,0;for err:=range results{if err==nil{success++}else if errors.Is(err,trackingartifacts.ErrQuota){quota++}else{t.Fatal(err)}};if success!=5||quota!=3{t.Fatalf("concurrent 100GiB budget: success=%d quota=%d",success,quota)}
 var id string;if err:=database.Raw(`SELECT id FROM mlflow_tracking_artifacts ORDER BY id LIMIT 1`).Scan(&id).Error;err!=nil{t.Fatal(err)}
 other:=scope;other.OwnerID="other";if _,err:=store.Get(context.Background(),other,id);!errors.Is(err,trackingartifacts.ErrNotFound){t.Fatalf("wrong owner: %v",err)}
 if _,err:=store.Mutate(context.Background(),scope,id,func(r trackingartifacts.Record)(trackingartifacts.Record,error){r.State="CANCELLED";return r,errors.New("cleanup failed")});err==nil{t.Fatal("callback failure accepted")};r,err:=store.Get(context.Background(),scope,id);if err!=nil||r.State!="PENDING"{t.Fatalf("mutation rolled forward: %+v %v",r,err)}
 if _,err:=store.Mutate(context.Background(),scope,id,func(r trackingartifacts.Record)(trackingartifacts.Record,error){r.State="CANCELLED";return r,nil});err!=nil{t.Fatal(err)}
 if _,err:=store.Reserve(context.Background(),makeRecord(20));err!=nil{t.Fatalf("cancel failed to release budget: %v",err)}
}

func TestTrackingArtifactDatabaseImmutabilityIntegration(t *testing.T){
 database:=openPostgresTestSchema(t);if err:=ApplyMigrations(database);err!=nil{t.Fatal(err)}
 if err:=database.Exec(`INSERT INTO tenants(id,name,namespace,local_queue) VALUES ('artifact-immutable','Artifact immutable','artifact-immutable','artifact-immutable')`).Error;err!=nil{t.Fatal(err)}
 exp:=fmt.Sprintf("%032x",20);run:=fmt.Sprintf("%032x",21);id:=fmt.Sprintf("%032x",22);hash:=fmt.Sprintf("%064x",20)
 if err:=database.Exec(`INSERT INTO mlflow_tracking_experiments(id,tenant_id,user_id,idempotency_hash,name,state,upstream_id) VALUES (?,'artifact-immutable','owner',?,'artifact-test','READY','100002')`,exp,hash).Error;err!=nil{t.Fatal(err)}
 if err:=database.Exec(`INSERT INTO mlflow_tracking_runs(id,experiment_id,tenant_id,user_id,idempotency_hash,name,state,upstream_id) VALUES (?,?,'artifact-immutable','owner',?,'artifact-test','RUNNING',?)`,run,exp,hash,run).Error;err!=nil{t.Fatal(err)}
 if err:=database.Exec(`INSERT INTO mlflow_tracking_artifacts(id,run_id,tenant_id,user_id,idempotency_hash,name,size_bytes,sha256,state,total_parts,expires_at) VALUES (?,?,'artifact-immutable','owner',?,'model.bin',3,?,'PENDING',1,NOW()+INTERVAL '24 hours')`,id,run,hash,hash).Error;err!=nil{t.Fatal(err)}
 if err:=database.Exec(`INSERT INTO mlflow_tracking_artifact_parts(artifact_id,part_index,size_bytes,sha256) VALUES (?,1,3,?)`,id,hash).Error;err!=nil{t.Fatal(err)}
 if err:=database.Exec(`UPDATE mlflow_tracking_artifacts SET state='READY' WHERE id=?`,id).Error;err!=nil{t.Fatal(err)}
 for _,statement:=range []string{`UPDATE mlflow_tracking_artifacts SET state='PENDING' WHERE id=?`,`UPDATE mlflow_tracking_artifacts SET name='other.bin' WHERE id=?`,`UPDATE mlflow_tracking_artifacts SET user_id='other' WHERE id=?`,`DELETE FROM mlflow_tracking_artifacts WHERE id=?`,`UPDATE mlflow_tracking_artifact_parts SET sha256=repeat('b',64) WHERE artifact_id=?`,`DELETE FROM mlflow_tracking_artifact_parts WHERE artifact_id=?`}{if err:=database.Exec(statement,id).Error;err==nil{t.Errorf("immutable mutation accepted: %s",statement)}}
}
