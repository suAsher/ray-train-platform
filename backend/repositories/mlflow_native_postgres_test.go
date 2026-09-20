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
	testMLflowAnonymousAuditPostgres(t, MLflowAuditNativeProxy, "/api/v1/mlflow-native/api/2.0/mlflow/runs/delete")
}

func TestMLflowDashboardAnonymousAuditPostgres(t *testing.T) {
	testMLflowAnonymousAuditPostgres(t, MLflowAuditDashboardProxy, "/mlflow/ajax-api/2.0/mlflow/runs/delete")
}

func testMLflowAnonymousAuditPostgres(t *testing.T, action MLflowAuditAction, path string) {
	t.Helper()
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
			Action:    action,
			Principal: auth.Principal{Subject: "mlflow-anonymous", AuthType: auth.AuthTypeAnonymous},
			Method:    "DELETE", Path: path, Status: status,
		}); err != nil {
			t.Fatal(err)
		}
	}
	var records []AuditLogRecord
	if err := database.Where("action = ?", string(action)).Find(&records).Error; err != nil {
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
		if (payload["status"] == float64(102) && payload["outcome"] != "attempt") || (payload["status"] == float64(200) && payload["outcome"] != "success") {
			t.Fatalf("incorrect anonymous audit outcome: %+v", payload)
		}
	}
}
