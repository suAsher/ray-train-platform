package api

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"ray-train-platform-backend/assistant"
	databasepkg "ray-train-platform-backend/db"
	"ray-train-platform-backend/domain"
	"ray-train-platform-backend/helpdocs"
	"ray-train-platform-backend/repositories"
)

// Use the production repository and NewHandler assembly. Hand-assigned slices
// cannot prove that published JSON is decoded and wired into the HTTP route.
func assistantPostgresStore(t *testing.T) (*gorm.DB, *repositories.GormRepository) {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("POSTGRES_TEST_DSN"))
	if dsn == "" {
		t.Skip("POSTGRES_TEST_DSN is not set")
	}
	connect := func() *gorm.DB {
		db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
		if err != nil {
			t.Fatal("cannot open isolated PostgreSQL test connection")
		}
		sqlDB, err := db.DB()
		if err != nil {
			t.Fatal(err)
		}
		sqlDB.SetMaxOpenConns(1)
		t.Cleanup(func() { _ = sqlDB.Close() })
		return db
	}
	admin := connect()
	schema := fmt.Sprintf("assistant_help_%d", time.Now().UnixNano())
	if err := admin.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := admin.Exec("DROP SCHEMA " + schema + " CASCADE").Error; err != nil {
			t.Error(err)
		}
	})
	db := connect()
	if err := db.Exec("SET search_path TO " + schema).Error; err != nil {
		t.Fatal(err)
	}
	if err := databasepkg.ApplyMigrations(db); err != nil {
		t.Fatal(err)
	}
	store := repositories.NewGormRepository(db)
	seed, err := helpdocs.Documents()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SeedHelpDocuments(context.Background(), seed); err != nil {
		t.Fatal(err)
	}
	return db, store
}

func TestAssistantPublishedStorePostgresHTTPAssembly(t *testing.T) {
	_, store := assistantPostgresStore(t)
	engine := &assistantTestEngine{result: assistant.Result{Answer: "根据已发布说明回答。", Mode: "local", Reason: "selected"}}
	h := NewHandler(store, Options{Assistant: engine, MLflowNativePublicEnabled: true, MLflowDashboardPublicEnabled: true})
	articles, err := store.ListHelpArticles(context.Background())
	if err != nil || len(articles) != 43 {
		t.Fatalf("expected 43 published articles, got %d, error=%v", len(articles), err)
	}
	for _, article := range articles {
		t.Run(article.ID, func(t *testing.T) {
			body, _ := json.Marshal(assistantQueryRequest{Question: article.Title, Mode: "auto"})
			before := engine.calls
			w := assistantRequest(assistantTestRouter(h, assistantTestPrincipal()), string(body))
			var response struct {
				Data assistantQueryResponse `json:"data"`
			}
			if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &response) != nil {
				t.Fatalf("HTTP response: %d %s", w.Code, w.Body.String())
			}
			if len(response.Data.Citations) == 0 || response.Data.Citations[0].ID != article.ID || response.Data.Citations[0].Excerpt == "" || response.Data.Citations[0].Version < 1 {
				t.Fatalf("published article not retrieved: %+v", response.Data)
			}
			if response.Data.Mode != "local" || engine.calls != before+1 {
				t.Fatalf("production repository audit/engine assembly failed: %+v", response.Data)
			}
		})
	}
}

func TestAssistantPublishedStorePostgresExcludesDraftAndUnpublished(t *testing.T) {
	_, store := assistantPostgresStore(t)
	ctx := context.Background()
	doc := domain.HelpDocument{ID: "assistant-private-draft", Title: "量子猫训练说明", Category: "团队说明", Markdown: "private-draft-marker"}
	if _, err := store.CreateHelpDocument(ctx, doc, "human"); err != nil {
		t.Fatal(err)
	}
	h := NewHandler(store, Options{Assistant: &assistantTestEngine{}})
	if evidence, available := h.assistantDocuments(ctx, doc.Title); !available || len(evidence) != 0 {
		t.Fatalf("draft exposed: %+v available=%t", evidence, available)
	}
	if _, err := store.ChangeHelpDocument(ctx, doc.ID, 1, "publish", 0, nil, "human"); err != nil {
		t.Fatal(err)
	}
	doc.Markdown = "new-secret-draft-marker"
	if _, err := store.ChangeHelpDocument(ctx, doc.ID, 2, "save", 0, &doc, "human"); err != nil {
		t.Fatal(err)
	}
	evidence, available := h.assistantDocuments(ctx, doc.Title)
	if !available || len(evidence) != 1 || evidence[0].Version != 2 || strings.Contains(evidence[0].Excerpt, "new-secret") {
		t.Fatalf("published snapshot incorrect: %+v", evidence)
	}
	if _, err := store.ChangeHelpDocument(ctx, doc.ID, 3, "unpublish", 0, nil, "human"); err != nil {
		t.Fatal(err)
	}
	if evidence, available := h.assistantDocuments(ctx, doc.Title); !available || len(evidence) != 0 {
		t.Fatalf("unpublished document exposed: %+v", evidence)
	}
}

func TestAssistantPublishedStorePostgresCommonUserQuestions(t *testing.T) {
	_, store := assistantPostgresStore(t)
	engine := &assistantTestEngine{result: assistant.Result{Answer: "根据已发布说明回答。", Mode: "local", Reason: "selected"}}
	h := NewHandler(store, Options{Assistant: engine})
	for _, tc := range []struct{ question, first string }{
		{"如何提交训练任务？", "quickstart"},
		{"怎么提交任务", "quickstart"},
		{"第一次用平台，怎么开始训练？", "quickstart"},
		{"我的代码在本地电脑，如何提交训练？", "code"},
		{"CLI 安装后怎样登录？", "cli-onboarding-v2"},
		{"日志里有 loss 但页面没有曲线，怎么办？", "telemetry-boundary"},
		{"调试环境安装的依赖如何保存？", "custom-environment"},
	} {
		t.Run(tc.question, func(t *testing.T) {
			body, _ := json.Marshal(assistantQueryRequest{Question: tc.question, Mode: "auto"})
			w := assistantRequest(assistantTestRouter(h, assistantTestPrincipal()), string(body))
			var response struct {
				Data assistantQueryResponse `json:"data"`
			}
			if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &response) != nil || len(response.Data.Citations) == 0 || response.Data.Citations[0].ID != tc.first || response.Data.Mode != "local" {
				t.Fatalf("common user question did not reach the model with relevant evidence: %d %s", w.Code, w.Body.String())
			}
		})
	}
}
