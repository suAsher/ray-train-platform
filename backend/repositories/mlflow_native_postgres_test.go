package repositories

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"ray-train-platform-backend/auth"
	databasepkg "ray-train-platform-backend/db"
)

func TestMLflowNativeAnonymousAuditPostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("POSTGRES_TEST_DSN"))
	if dsn == "" {
		t.Skip("POSTGRES_TEST_DSN is not set")
	}
	database := openArtifactPostgresConnection(t, dsn)
	schema := fmt.Sprintf("mlflow_native_%d", time.Now().UnixNano())
	if err := database.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := database.Exec("DROP SCHEMA " + schema + " CASCADE").Error; err != nil {
			t.Error(err)
		}
	})
	if err := database.Exec("SET search_path TO " + schema).Error; err != nil {
		t.Fatal(err)
	}
	if err := databasepkg.ApplyMigrations(database); err != nil {
		t.Fatal(err)
	}
	repository := NewGormRepository(database)
	for _, status := range []int{102, 200} {
		if err := repository.CreateMLflowAuditLog(context.Background(), MLflowAuditEvent{
			Action:    MLflowAuditNativeProxy,
			Principal: auth.Principal{Subject: "mlflow-anonymous", AuthType: auth.AuthTypeAnonymous},
			Method:    "DELETE", Path: "/api/v1/mlflow-native/api/2.0/mlflow/runs/delete", Status: status,
		}); err != nil {
			t.Fatal(err)
		}
	}
	var records []AuditLogRecord
	if err := database.Where("action = ?", string(MLflowAuditNativeProxy)).Find(&records).Error; err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 {
		t.Fatalf("anonymous attempt and completion not persisted: %d", len(records))
	}
	for _, record := range records {
		var payload map[string]any
		if err := json.Unmarshal([]byte(record.PayloadJSON), &payload); err != nil {
			t.Fatal(err)
		}
		if record.TenantID != "" || record.UserID != "mlflow-anonymous" || payload["auth_type"] != "anonymous" {
			t.Fatalf("unexpected audit identity: %+v", record)
		}
	}
}
