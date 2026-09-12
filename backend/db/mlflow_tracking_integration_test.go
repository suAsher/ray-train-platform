package db

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	tracking "ray-train-platform-backend/mlflowtracking"
	"ray-train-platform-backend/repositories"
)

func TestMLflowTrackingMigrationPreservesExistingOwnershipAndSerializesReservations(t *testing.T) {
	database:=openPostgresTestSchema(t)
	applyPostgresMigrationsThrough(t,database,45)
	seedPostgresIdentityRows(t,database)
	if err:=database.Exec("UPDATE tenants SET gpu_quota_limit = 24 WHERE id = 'tenant-a'").Error;err!=nil {t.Fatal(err)}
	if err:=database.Exec(`INSERT INTO source_artifacts(id,tenant_id,user_id,sha256,size_bytes,object_key,upload_expires_at) VALUES ('kept-artifact','tenant-a','user-a1',?,1,'unchanged.zip',NOW()+INTERVAL '1 hour')`,strings.Repeat("d",64)).Error;err!=nil {t.Fatal(err)}
	for i:=0;i<2;i++ {if err:=ApplyMigrations(database);err!=nil {t.Fatal(err)}}
	var quota,artifacts int64
	if err:=database.Raw("SELECT gpu_quota_limit FROM tenants WHERE id = 'tenant-a'").Scan(&quota).Error;err!=nil {t.Fatal(err)}
	if err:=database.Raw("SELECT COUNT(*) FROM source_artifacts WHERE id = 'kept-artifact' AND object_key = 'unchanged.zip'").Scan(&artifacts).Error;err!=nil {t.Fatal(err)}
	if quota!=24||artifacts!=1 {t.Fatalf("migration changed existing quota/data: %d %d",quota,artifacts)}
	store:=repositories.NewMLflowTrackingStore(database)
	var schema string
	if err:=database.Raw("SELECT current_schema()").Scan(&schema).Error;err!=nil {t.Fatal(err)}
	stores:=make([]*repositories.MLflowTrackingStore,8)
	for i:=range stores {
		connection:=openIndependentPostgres(t,postgresTestDSN(t))
		if err:=connection.Exec("SET search_path TO "+quotePostgresIdentifier(schema)).Error;err!=nil {t.Fatal(err)}
		stores[i]=repositories.NewMLflowTrackingStore(connection)
	}
	var wait sync.WaitGroup
	results:=make(chan bool,8);failures:=make(chan error,8)
	start:=make(chan struct{})
	for i:=0;i<8;i++ {wait.Add(1);go func(i int){defer wait.Done();<-start;now:=time.Now().UTC();_,claimed,err:=stores[i].ReserveExperiment(context.Background(),tracking.Experiment{ID:fmt.Sprintf("%032x",i+1),TenantID:"tenant-a",UserID:"user-a1",IdempotencyHash:strings.Repeat("a",64),Name:"one-experiment",State:"PENDING",CreatedAt:now,UpdatedAt:now});results<-claimed;failures<-err}(i)}
	close(start)
	wait.Wait();close(results);close(failures)
	claims:=0;for claimed:=range results {if claimed {claims++}};for err:=range failures {if err!=nil {t.Fatal(err)}}
	if claims!=1 {t.Fatalf("concurrent requests made %d durable claims",claims)}
	actor:=tracking.Actor{TenantID:"tenant-a",UserID:"user-a1"}
	page,err:=store.ListExperiments(context.Background(),actor,"",100);if err!=nil||len(page)!=1 {t.Fatalf("reserved experiment=%+v %v",page,err)}
	if _,err:=store.CompleteExperiment(context.Background(),actor,page[0].ID,"123");err!=nil {t.Fatal(err)}
	now:=time.Now().UTC()
	run:=tracking.Run{ID:strings.Repeat("b",32),ExperimentID:page[0].ID,TenantID:actor.TenantID,UserID:actor.UserID,IdempotencyHash:strings.Repeat("c",64),Name:"lease-race",State:"PENDING",CreatedAt:now,UpdatedAt:now}
	if _,_,err:=store.ReserveRun(context.Background(),run);err!=nil {t.Fatal(err)}
	if _,err:=store.CompleteRun(context.Background(),actor,run.ID,strings.Repeat("d",32));err!=nil {t.Fatal(err)}
	leaseErrors:=make(chan error,8);leaseStart:=make(chan struct{})
	for i:=0;i<8;i++ {wait.Add(1);go func(i int){defer wait.Done();<-leaseStart;_,err:=stores[i].ClaimRunLease(context.Background(),actor,run.ID,fmt.Sprintf("lease-%d",i),"",now,now.Add(time.Minute),0);leaseErrors<-err}(i)}
	close(leaseStart);wait.Wait();close(leaseErrors)
	leases:=0
	for err:=range leaseErrors {if err==nil {leases++} else if !errors.Is(err,tracking.ErrBusy) {t.Fatalf("unexpected lease race error: %v",err)}}
	if leases!=1 {t.Fatalf("independent PG connections acquired %d simultaneous leases",leases)}
}
