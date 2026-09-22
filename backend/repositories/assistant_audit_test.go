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

func TestAssistantAuditPostgresUsesExistingSchemaAndNoContent(t *testing.T) {
	dsn:=strings.TrimSpace(os.Getenv("POSTGRES_TEST_DSN"));if dsn==""{t.Skip("POSTGRES_TEST_DSN is not set")}
	admin:=openArtifactPostgresConnection(t,dsn)
	schema:=fmt.Sprintf("assistant_audit_%d",time.Now().UnixNano())
	if err:=admin.Exec("CREATE SCHEMA "+schema).Error;err!=nil{t.Fatal(err)}
	t.Cleanup(func(){if err:=admin.Exec("DROP SCHEMA "+schema+" CASCADE").Error;err!=nil{t.Error(err)}})
	db:=openArtifactPostgresConnection(t,dsn)
	if err:=db.Exec("SET search_path TO "+schema).Error;err!=nil{t.Fatal(err)}
	if err:=databasepkg.ApplyMigrations(db);err!=nil{t.Fatal(err)}
	r:=NewGormRepository(db)
	p:=auth.Principal{Subject:"member",TenantID:"team",Username:"sensitive-name-must-not-persist",AuthType:auth.AuthTypeOAuth2Proxy}
	if err:=r.CreateAssistantAuditLog(context.Background(),p,"test-request","auto",true);err!=nil{t.Fatal(err)}
	var record AuditLogRecord
	if err:=db.Where("request_id = ?","test-request").First(&record).Error;err!=nil{t.Fatal(err)}
	if record.ResourceType!="assistant_query"||record.Action!="assistant.query.started"||record.UserID!="member"||record.TenantID!="team"{t.Fatal("incorrect attribution")}
	var payload map[string]any;if json.Unmarshal([]byte(record.PayloadJSON),&payload)!=nil{t.Fatal("invalid payload")}
	if len(payload)!=3||payload["mode"]!="auto"||payload["includesLogs"]!=true||payload["outcome"]!="started"||strings.Contains(record.PayloadJSON,p.Username){t.Fatal("audit must store only allowed intent metadata")}
	if err:=r.CreateAssistantAuditLog(context.Background(),p,"test-request-2","arbitrary",false);err==nil{t.Fatal("invalid mode accepted")}
}
