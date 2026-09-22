package repositories

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	databasepkg "ray-train-platform-backend/db"
	"ray-train-platform-backend/domain"
)

func TestAssistantDemandPostgresAggregatesPendingDemand(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("POSTGRES_TEST_DSN"))
	if dsn == "" {
		t.Skip("POSTGRES_TEST_DSN is not set")
	}
	admin := openArtifactPostgresConnection(t, dsn)
	schema := fmt.Sprintf("assistant_demand_%d", time.Now().UnixNano())
	if err := admin.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := admin.Exec("DROP SCHEMA " + schema + " CASCADE").Error; err != nil {
			t.Error(err)
		}
	})
	database := openArtifactPostgresConnection(t, dsn)
	if err := database.Exec("SET search_path TO " + schema).Error; err != nil {
		t.Fatal(err)
	}
	if err := databasepkg.ApplyMigrations(database); err != nil {
		t.Fatal(err)
	}
	repo := NewGormRepository(database)
	if err := database.Exec("INSERT INTO tenants(id, name, namespace, local_queue) VALUES ('tenant-a', 'Tenant A', 'tenant-a', 'tenant-a')").Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Exec("INSERT INTO users(id, oidc_subject, username, tenant_id) VALUES ('user-a', 'oidc-user-a', 'user-a', 'tenant-a')").Error; err != nil {
		t.Fatal(err)
	}
	seedGPUAllocationJob(t, repo, JobRecord{ID: "postgres-unknown", TenantID: "tenant-a", UserID: "user-a", Name: "postgres-unknown", DesiredState: string(domain.DesiredActive), ObservedState: string(domain.StateUnknown), KubernetesNS: "tenant-a", TrainingEngine: "ray-ddp", RayVersion: "2.58.0", ClusterAttempt: 1, CleanupJSON: "{}"}, domain.JobSpec{Resources: domain.Resources{WorkerReplicas: 1, GPUsPerWorker: 2}})
	seedGPUAllocationJob(t, repo, JobRecord{ID: "postgres-running", TenantID: "tenant-a", UserID: "user-a", Name: "postgres-running", DesiredState: string(domain.DesiredActive), ObservedState: string(domain.StateRunning), KubernetesNS: "tenant-a", TrainingEngine: "ray-ddp", RayVersion: "2.58.0", ClusterAttempt: 1, CleanupJSON: "{}"}, domain.JobSpec{Resources: domain.Resources{WorkerReplicas: 1, GPUsPerWorker: 8}})
	seedGPUAllocationWorkspace(t, repo, WorkspaceRecord{ID: "postgres-workspace", TenantID: "tenant-a", UserID: "user-a", Name: "debug", Namespace: "tenant-a", ObservedState: string(domain.WorkspaceSubmitted), GPUCount: 1, RayClusterName: "debug-cluster"})

	before := time.Now().UTC()
	got, err := repo.AggregateAssistantDemand(context.Background())
	if err != nil {
		t.Fatalf("aggregate assistant demand on postgres: %v", err)
	}
	if !got.HasDemand || got.TrainingJobCount != 1 || got.WorkspaceCount != 1 || got.GPUCount != 3 {
		t.Fatalf("unexpected postgres demand snapshot: %+v", got)
	}
	if got.ObservedAt.Before(before) || time.Since(got.ObservedAt) > time.Minute {
		t.Fatalf("postgres demand observedAt is not fresh query-start time: %s", got.ObservedAt)
	}
}
