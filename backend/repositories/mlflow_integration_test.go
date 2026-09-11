package repositories

import (
	"context"
	"testing"
)

func TestMLflowIntegrationAuditActionIsExplicitAndAllowlisted(t *testing.T) {
	for _, action := range []MLflowAuditAction{"", MLflowAuditRunLogBatch, "unexpected.action"} {
		t.Run(string(action), func(t *testing.T) {
			repo, _ := mlflowDashboardTestRepositories(t)
			err := repo.CreateMLflowAuditLog(context.Background(), MLflowAuditEvent{Action: action, Method: "POST", Path: "/api/v1/jobs/job-01/mlflow/runs/run/log-batch", Status: 102})
			if action == "unexpected.action" {
				if err == nil {
					t.Fatal("unlisted action accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var record AuditLogRecord
			if err := repo.db.First(&record).Error; err != nil {
				t.Fatal(err)
			}
			want := "mlflow.dashboard.proxy"
			if action == MLflowAuditRunLogBatch {
				want = "mlflow.run.log_batch"
			}
			if record.Action != want {
				t.Fatalf("action=%s want=%s", record.Action, want)
			}
		})
	}
}
