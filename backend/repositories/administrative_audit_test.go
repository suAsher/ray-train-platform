package repositories

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"ray-train-platform-backend/auth"
)

func TestAdministrativeAuditStoresOnlyAllowlistedMetadata(t *testing.T) {
	database, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.AutoMigrate(&AuditLogRecord{}); err != nil {
		t.Fatal(err)
	}
	repository := NewGormRepository(database)
	event := AdministrativeAuditEvent{
		Action: "tenant_membership.reassigned", ResourceID: "user-a",
		SourceTenantID: "local", TargetTenantID: "devops", RequestID: "request-a",
		Principal: auth.Principal{Subject: "root", Username: "guofeng.su", TenantID: "local", AuthType: auth.AuthTypeOAuth2Proxy},
	}
	if err := repository.CreateAdministrativeAuditLog(context.Background(), event); err != nil {
		t.Fatalf("create audit: %v", err)
	}
	var record AuditLogRecord
	if err := database.First(&record).Error; err != nil {
		t.Fatal(err)
	}
	if record.Action != event.Action || record.ResourceType != "local_user" || record.ResourceID != "user-a" || record.RequestID != "request-a" {
		t.Fatalf("unexpected audit record: %+v", record)
	}
	var payload map[string]string
	if err := json.Unmarshal([]byte(record.PayloadJSON), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["source_tenant"] != "local" || payload["target_tenant"] != "devops" || payload["outcome"] != "success" {
		t.Fatalf("unexpected audit payload: %+v", payload)
	}
}

func TestAdministrativeAuditRejectsUnknownActions(t *testing.T) {
	repository := &GormRepository{}
	if err := repository.CreateAdministrativeAuditLog(context.Background(), AdministrativeAuditEvent{Action: "arbitrary.action"}); err == nil {
		t.Fatal("expected an unsupported administrative action to be rejected")
	}
}
