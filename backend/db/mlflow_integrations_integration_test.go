package db

import (
 "context"
 "errors"
 "fmt"
 "strings"
 "sync"
 "testing"
 "time"
 "ray-train-platform-backend/auth"
 "ray-train-platform-backend/integrations"
 "ray-train-platform-backend/repositories"
)

func TestMLflowIntegrationsMigrationPreservesQuotaAndSerializesOwnerLimit(t *testing.T){
 database:=openPostgresTestSchema(t);applyPostgresMigrationsThrough(t,database,46);seedPostgresIdentityRows(t,database)
 if err:=database.Exec(`INSERT INTO local_users(id,username,email,tenant_id,active_tenant_id,storage_key,roles,global_roles,password_hash,identity_provider) VALUES ('integration-test-owner','integration-test-owner','','tenant-a','tenant-a','unchanged-home','["Engineer"]','[]','unused','local')`).Error;err!=nil{t.Fatal(err)}
 if err:=database.Exec(`INSERT INTO tenant_memberships(identity_id,tenant_id,roles,status) VALUES ('integration-test-owner','tenant-a','["Engineer"]','active')`).Error;err!=nil{t.Fatal(err)}
 if err:=database.Exec("UPDATE tenants SET gpu_quota_limit=24 WHERE id='tenant-a'").Error;err!=nil{t.Fatal(err)}
 for i:=0;i<2;i++{if err:=ApplyMigrations(database);err!=nil{t.Fatal(err)}}
 var quota int;database.Raw("SELECT gpu_quota_limit FROM tenants WHERE id='tenant-a'").Scan(&quota);var home string;database.Raw("SELECT storage_key FROM local_users WHERE id='integration-test-owner'").Scan(&home);if quota!=24||home!="unchanged-home"{t.Fatal("migration changed existing owner data")}
 p:=auth.Principal{Subject:"integration-test-owner",TenantID:"tenant-a",AuthType:auth.AuthTypeLocal};store:=repositories.NewMLflowIntegrationStore(repositories.NewGormRepository(database));ctx:=context.Background()
 for i:=0;i<19;i++{if err:=store.Create(ctx,p,integrations.Identity{ID:fmt.Sprintf("%032x",i+1),TenantID:p.TenantID,OwnerUserID:p.Subject,Name:fmt.Sprintf("pipeline-%d",i),CreatedAt:time.Now().UTC()});err!=nil{t.Fatal(err)}}
 var schema string;database.Raw("SELECT current_schema()").Scan(&schema);stores:=make([]*repositories.MLflowIntegrationStore,4);for i:=range stores{conn:=openIndependentPostgres(t,postgresTestDSN(t));if err:=conn.Exec("SET search_path TO "+quotePostgresIdentifier(schema)).Error;err!=nil{t.Fatal(err)};stores[i]=repositories.NewMLflowIntegrationStore(repositories.NewGormRepository(conn))}
 results:=make(chan error,4);var wg sync.WaitGroup;for i:=range stores{wg.Add(1);go func(i int){defer wg.Done();results<-stores[i].Create(ctx,p,integrations.Identity{ID:fmt.Sprintf("%032x",100+i),TenantID:p.TenantID,OwnerUserID:p.Subject,Name:fmt.Sprintf("racer-%d",i),CreatedAt:time.Now().UTC()})}(i)};wg.Wait();close(results);success:=0;for err:=range results{if err==nil{success++}else if !errors.Is(err,integrations.ErrLimit){t.Fatal(err)}};if success!=1{t.Fatalf("limit race allowed %d",success)}
 // The audit table is mandatory and transactional, even on a revoke.
 id:=strings.Repeat("0",31)+"1";if err:=database.Exec("ALTER TABLE mlflow_integration_audit RENAME TO mlflow_integration_audit_unavailable").Error;err!=nil{t.Fatal(err)};if err:=store.Revoke(ctx,p,id,time.Now());err==nil{t.Fatal("revoke succeeded without audit")};var revoked int64;database.Raw("SELECT COUNT(*) FROM mlflow_integrations WHERE id = ? AND revoked_at IS NOT NULL",id).Scan(&revoked);if revoked!=0{t.Fatal("audit failure did not roll back revoke")}
}
