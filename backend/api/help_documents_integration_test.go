package api

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"net/http/httptest"
	"path/filepath"
	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
	"ray-train-platform-backend/helpdocs"
	"ray-train-platform-backend/repositories"
	"strings"
	"testing"
)

func helpIntegrationRouterAndStore(t *testing.T) (*gin.Engine, *repositories.GormRepository) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "help.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err = db.AutoMigrate(&repositories.HelpDocumentRecord{}, &repositories.HelpRevisionRecord{}); err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	t.Cleanup(func() { sqlDB.Close() })
	store := repositories.NewGormRepository(db)
	h := NewHandler(store, Options{})
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("ray-platform-principal", auth.Principal{Subject: "admin", TenantID: "team", Roles: []string{domain.RoleSuperAdmin}, AuthType: auth.AuthTypeLocal})
	})
	h.RegisterHelpReadRoutes(router.Group("/api/v1"))
	h.RegisterHelpManagementRoutes(router.Group("/api/v1"))
	return router, store
}

func helpIntegrationRouter(t *testing.T) *gin.Engine {
	t.Helper()
	router, _ := helpIntegrationRouterAndStore(t)
	return router
}
func helpRequest(t *testing.T, r *gin.Engine, method, path, body string, want int) json.RawMessage {
	t.Helper()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(method, "/api/v1/"+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if w.Code != want {
		t.Fatalf("%s %s got %d want %d: %s", method, path, w.Code, want, w.Body.String())
	}
	var env struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	return env.Data
}
func TestHelpAPIRealStoreLifecycle(t *testing.T) {
	r := helpIntegrationRouter(t)
	base := "admin/help/documents"
	create := `{"id":"faq","title":"FAQ","category":"Start","markdown":"public","sortOrder":0}`
	helpRequest(t, r, "POST", base, create, 201)
	adminDrafts := helpRequest(t, r, "GET", base, "", 200)
	if !strings.Contains(string(adminDrafts), `"markdown":"public"`) {
		t.Fatal("administrator draft missing", string(adminDrafts))
	}
	helpRequest(t, r, "POST", base, create, 409)
	data := helpRequest(t, r, "GET", "help/documents", "", 200)
	if string(data) != `{"items":[]}` {
		t.Fatal("draft visible", string(data))
	}
	helpRequest(t, r, "POST", base+"/faq/publish", `{"expectedVersion":1}`, 200)
	helpRequest(t, r, "PUT", base+"/faq", `{"title":"Secret title","category":"Private draft","markdown":"secret draft","sortOrder":5,"expectedVersion":2}`, 200)
	data = helpRequest(t, r, "GET", "help/documents", "", 200)
	if strings.Contains(string(data), "secret") || strings.Contains(string(data), "Secret") || !strings.Contains(string(data), "public") {
		t.Fatal("published snapshot incorrect", string(data))
	}
	helpRequest(t, r, "POST", base+"/faq/publish", `{"expectedVersion":2}`, 409)
	helpRequest(t, r, "POST", base+"/faq/restore", `{"expectedVersion":3,"restoreVersion":1}`, 200)
	data = helpRequest(t, r, "GET", base+"/faq/history", "", 200)
	if !strings.Contains(string(data), `"action":"restore"`) {
		t.Fatal(string(data))
	}
	helpRequest(t, r, "POST", base+"/faq/unpublish", `{"expectedVersion":4}`, 200)
	data = helpRequest(t, r, "GET", "help/documents", "", 200)
	if string(data) != `{"items":[]}` {
		t.Fatal(string(data))
	}
	helpRequest(t, r, "POST", base+"/missing/publish", `{"expectedVersion":1}`, 404)
	helpRequest(t, r, "GET", base+"/missing/history", "", 404)
	helpRequest(t, r, "POST", base+"/faq/restore", `{"expectedVersion":5,"restoreVersion":99}`, 404)
}

func TestHelpAPIPublicRouteReturnsUserGuidesNotAdminSourceDocs(t *testing.T) {
	r, store := helpIntegrationRouterAndStore(t)
	seed, err := helpdocs.Documents()
	if err != nil {
		t.Fatal(err)
	}
	if err = store.SeedHelpDocuments(context.Background(), seed); err != nil {
		t.Fatal(err)
	}
	custom := `{"id":"team-faq","title":"团队 FAQ","category":"07 团队补充","markdown":"custom answer","sortOrder":900}`
	helpRequest(t, r, "POST", "admin/help/documents", custom, 201)
	helpRequest(t, r, "POST", "admin/help/documents/team-faq/publish", `{"expectedVersion":1}`, 200)

	data := helpRequest(t, r, "GET", "help/documents", "", 200)
	body := string(data)
	for _, marker := range []string{"快速开始：从登录到第一条训练任务", "实验与 MLflow 接入", "https://raytrain.wellspiking.ai/api/v1/mlflow-native", "custom answer"} {
		if !strings.Contains(body, marker) {
			t.Fatalf("public help response is missing %q: %s", marker, body)
		}
	}
	for _, forbidden := range []string{"管理员：新增 GPU 节点与缓存验收", "API：读取或补充已有训练记录", `"id":"mlflow-api-with-pat"`} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("public help response leaked %q: %s", forbidden, body)
		}
	}

	adminData := helpRequest(t, r, "GET", "admin/help/documents", "", 200)
	if !strings.Contains(string(adminData), "管理员：新增 GPU 节点与缓存验收") || !strings.Contains(string(adminData), `"id":"mlflow-api-with-pat"`) {
		t.Fatalf("admin help response lost source docs: %s", string(adminData))
	}
}

func TestHelpAPIInputBounds(t *testing.T) {
	r := helpIntegrationRouter(t)
	base := "admin/help/documents"
	for _, body := range []string{`{`, `{} {}`, `{"unknown":true}`, `{}`, `{"id":"bad/id","title":"x","category":"x","markdown":"x"}`, `{"id":"x","title":"x","category":"x","markdown":"x","sortOrder":1.1}`} {
		helpRequest(t, r, "POST", base, body, 400)
	}
	helpRequest(t, r, "POST", base, fmt.Sprintf(`{"markdown":%q}`, strings.Repeat("x", 601*1024)), 413)
	helpRequest(t, r, "POST", base, fmt.Sprintf(`{"id":"large","title":"x","category":"x","markdown":%q}`, strings.Repeat("x", 513*1024)), 400)
	helpRequest(t, r, "PUT", base+"/faq", `{"expectedVersion":0}`, 400)
	helpRequest(t, r, "PUT", base+"/faq", `{"expectedVersion":1,"id":"changed"}`, 400)
	helpRequest(t, r, "PUT", base+"/faq", `{"expectedVersion":1}`, 400)
	helpRequest(t, r, "POST", base+"/faq/restore", `{"expectedVersion":1,"restoreVersion":0}`, 400)
	helpRequest(t, r, "GET", base+"/BAD/history", "", 400)
}
func TestHelpAPIRateLimit(t *testing.T) {
	r := helpIntegrationRouter(t)
	for i := 0; i < 120; i++ {
		helpRequest(t, r, "GET", "help/documents", "", 200)
	}
	helpRequest(t, r, "GET", "help/documents", "", 429)
}
