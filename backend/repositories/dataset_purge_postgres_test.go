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
)

func TestDatasetPurgePostgresIdentityAndWriterFence(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("POSTGRES_TEST_DSN"))
	if dsn == "" {
		t.Skip("POSTGRES_TEST_DSN is not set")
	}
	admin := openArtifactPostgresConnection(t, dsn)
	schema := fmt.Sprintf("dataset_purge_%d", time.Now().UnixNano())
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
	if err := databasepkg.ApplyMigrations(first); err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{
		"INSERT INTO datasets(id,slug,name,source_space,source_relative_path,visibility,schema_version) VALUES ('purge-data','purge-data','Purge','public','labeled','PUBLIC','v1')",
		"INSERT INTO dataset_versions(id,dataset_id,version,state,schema_version) VALUES ('purge-version','purge-data','v1','FAILED','v1')",
		"INSERT INTO dataset_publication_runs(id,dataset_id,dataset_version_id,state) VALUES ('purge-run','purge-data','purge-version','FAILED')",
		"INSERT INTO dataset_partitions(id,dataset_version_id,name) VALUES ('purge-part','purge-version','p1')",
		"INSERT INTO dataset_publication_partition_attempts(dataset_version_id,partition_id,state,input_fingerprint,plan_sha256) VALUES ('purge-version','purge-part','FAILED',repeat('a',64),repeat('b',64))",
		"INSERT INTO dataset_version_shards(dataset_version_id,dataset_id,partition_id,split,ordinal,shard_sha256,object_key) VALUES ('purge-version','purge-data','purge-part','train',0,repeat('c',64),'root/purge-data/objects/sha256/cc/' || repeat('c',64) || '.parquet')",
	} {
		if err := first.Exec(sql).Error; err != nil {
			t.Fatal(err)
		}
	}
	r := NewGormRepository(first)
	other := NewGormRepository(second)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := other.WithDatasetPublicationWrite(ctx, "purge-data", "purge-version", func() error {
		_, err := r.PurgeFailedDatasetRecord(ctx, "admin", true, "purge-data", "purge-version", "admin", func(DatasetPurgePlan) (int, error) { t.Fatal("writer fence bypassed"); return 0, nil })
		if !errors.Is(err, ErrDatasetCleanupConflict) {
			t.Fatalf("fence: %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Exec("DELETE FROM dataset_versions WHERE id = 'purge-version'").Error; err == nil {
		t.Fatal("unregistered FAILED deletion allowed")
	}
	if _, err := r.PurgeFailedDatasetRecord(ctx, "admin", true, "purge-data", "purge-version", "admin", func(DatasetPurgePlan) (int, error) { return 0, errors.New("storage failed") }); err == nil {
		t.Fatal("missing storage error")
	}
	n, err := r.PurgeFailedDatasetRecord(ctx, "admin", true, "purge-data", "purge-version", "admin", func(DatasetPurgePlan) (int, error) { return 3, nil })
	if err != nil || n != 3 {
		t.Fatalf("purge n=%d err=%v", n, err)
	}
	for _, table := range []string{"dataset_versions", "dataset_publication_runs", "dataset_partitions", "dataset_publication_partition_attempts", "dataset_version_shards"} {
		var count int64
		if err := first.Table(table).Count(&count).Error; err != nil || count != 0 {
			t.Fatalf("%s left=%d err=%v", table, count, err)
		}
	}
	for _, sql := range []string{
		"INSERT INTO dataset_versions(id,dataset_id,version,state,schema_version) VALUES ('purge-version','purge-data','new','FAILED','v1')",
		"INSERT INTO dataset_versions(id,dataset_id,version,state,schema_version) VALUES ('different','purge-data','v1','FAILED','v1')",
		"DELETE FROM dataset_purge_identities",
	} {
		if err := first.Exec(sql).Error; err == nil {
			t.Fatalf("identity guard missing: %s", sql)
		}
	}
	if err := r.WithDatasetPublicationWrite(ctx, "purge-data", "purge-version", func() error { t.Fatal("purged writer resumed"); return nil }); !errors.Is(err, ErrDatasetCleanupNotFound) {
		t.Fatalf("late writer: %v", err)
	}
}
