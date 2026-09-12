package repositories

import (
	"context"
	"errors"
	"ray-train-platform-backend/domain"
	"strings"
	"testing"
)

func helpRepo(t *testing.T) *GormRepository {
	t.Helper()
	r := testRepository(t)
	if err := r.db.AutoMigrate(&HelpDocumentRecord{}, &HelpRevisionRecord{}); err != nil {
		t.Fatal(err)
	}
	return r
}
func TestHelpDocumentLifecycle(t *testing.T) {
	r := helpRepo(t)
	ctx := context.Background()
	d, err := r.CreateHelpDocument(ctx, domain.HelpDocument{ID: "first", Title: "Title", Category: "Start", Markdown: "original"}, "admin")
	if err != nil || d.Version != 1 {
		t.Fatalf("create: %+v %v", d, err)
	}
	items, err := r.ListHelpDocuments(ctx, false)
	if err != nil || len(items) != 0 {
		t.Fatalf("draft leaked: %v %v", items, err)
	}
	d, err = r.ChangeHelpDocument(ctx, d.ID, 1, "publish", 0, nil, "admin")
	if err != nil || d.PublishedVersion != 2 {
		t.Fatalf("publish: %+v %v", d, err)
	}
	draft := d
	draft.Markdown = "secret draft"
	d, err = r.ChangeHelpDocument(ctx, d.ID, 2, "save", 0, &draft, "editor")
	if err != nil {
		t.Fatal(err)
	}
	items, err = r.ListHelpDocuments(ctx, false)
	if err != nil || len(items) != 1 || items[0].Markdown != "original" {
		t.Fatalf("snapshot leaked: %v %v", items, err)
	}
	if _, err = r.ChangeHelpDocument(ctx, d.ID, 2, "publish", 0, nil, "admin"); !errors.Is(err, ErrHelpConflict) {
		t.Fatalf("stale update: %v", err)
	}
	d, err = r.ChangeHelpDocument(ctx, d.ID, 3, "restore", 1, nil, "admin")
	if err != nil || d.Version != 4 || d.Markdown != "original" || d.PublishedVersion != 2 {
		t.Fatalf("restore: %+v %v", d, err)
	}
	d, err = r.ChangeHelpDocument(ctx, d.ID, 4, "unpublish", 0, nil, "admin")
	if err != nil || d.PublishedVersion != 0 {
		t.Fatalf("unpublish: %+v %v", d, err)
	}
	if err = r.SeedHelpDocuments(ctx, []domain.HelpDocument{{ID: "first", Title: "seed", Category: "Start", Markdown: "seed"}}); err != nil {
		t.Fatal(err)
	}
	items, err = r.ListHelpDocuments(ctx, false)
	if err != nil || len(items) != 0 {
		t.Fatal("seed republished document", items, err)
	}
	hist, err := r.HelpDocumentHistory(ctx, d.ID)
	if err != nil || len(hist) != 5 || hist[0].UpdatedBy != "admin" || hist[0].Action != "unpublish" {
		t.Fatalf("history: %v %v", hist, err)
	}
}
func TestHelpSeedIdempotentAndTransactionRollback(t *testing.T) {
	r := helpRepo(t)
	ctx := context.Background()
	seed := []domain.HelpDocument{{ID: "seed", Title: "Seed", Category: "Start", Markdown: "seed"}}
	for i := 0; i < 2; i++ {
		if err := r.SeedHelpDocuments(ctx, seed); err != nil {
			t.Fatal(err)
		}
	}
	items, err := r.ListHelpDocuments(ctx, false)
	if err != nil || len(items) != 1 || items[0].Version != 1 {
		t.Fatalf("seed: %v %v", items, err)
	}
	if err := r.db.Migrator().DropTable(&HelpRevisionRecord{}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.ChangeHelpDocument(ctx, "seed", 1, "unpublish", 0, nil, "admin"); err == nil {
		t.Fatal("expected revision failure")
	}
	items, err = r.ListHelpDocuments(ctx, false)
	if err != nil || len(items) != 1 || items[0].Version != 1 {
		t.Fatalf("transaction leaked: %v %v", items, err)
	}
}

func TestHelpSeedRefreshesOnlyUneditedPlatformDocuments(t *testing.T) {
	r := helpRepo(t)
	ctx := context.Background()
	original := domain.HelpDocument{ID: "managed", Title: "Managed", Category: "Start", Markdown: "old seed"}
	if err := r.SeedHelpDocuments(ctx, []domain.HelpDocument{original}); err != nil {
		t.Fatal(err)
	}

	refreshed := original
	refreshed.Markdown = "new seed"
	if err := r.SeedHelpDocuments(ctx, []domain.HelpDocument{refreshed}); err != nil {
		t.Fatal(err)
	}
	items, err := r.ListHelpDocuments(ctx, false)
	if err != nil || len(items) != 1 || items[0].Markdown != "new seed" || items[0].Version != 2 || items[0].PublishedVersion != 2 {
		t.Fatalf("seed refresh: %+v %v", items, err)
	}

	if err := r.SeedHelpDocuments(ctx, []domain.HelpDocument{refreshed}); err != nil {
		t.Fatal(err)
	}
	items, err = r.ListHelpDocuments(ctx, false)
	if err != nil || items[0].Version != 2 {
		t.Fatalf("identical seed was not idempotent: %+v %v", items, err)
	}

	custom := items[0]
	custom.Markdown = "administrator draft"
	custom, err = r.ChangeHelpDocument(ctx, custom.ID, custom.Version, "save", 0, &custom, "admin")
	if err != nil {
		t.Fatal(err)
	}
	newer := refreshed
	newer.Markdown = "future seed"
	if err := r.SeedHelpDocuments(ctx, []domain.HelpDocument{newer}); err != nil {
		t.Fatal(err)
	}
	adminItems, err := r.ListHelpDocuments(ctx, true)
	if err != nil || len(adminItems) != 1 || adminItems[0].Markdown != "administrator draft" || adminItems[0].Version != custom.Version {
		t.Fatalf("seed overwrote administrator draft: %+v %v", adminItems, err)
	}
}

func TestPublicHelpDocumentsUseSummariesAndKeepCustomPublicDocs(t *testing.T) {
	r := helpRepo(t)
	ctx := context.Background()
	seed := []domain.HelpDocument{
		{ID: "quickstart", Title: "第一次跑通", Category: "01 开始使用", Markdown: "old quickstart", SortOrder: 10},
		{ID: "code", Title: "代码怎么进来", Category: "02 准备代码和数据", Markdown: "old code", SortOrder: 110},
		{ID: "mlflow-api-with-pat", Title: "API：读取或补充已有训练记录", Category: "04 结果与MLflow", Markdown: "old platform run id docs", SortOrder: 350},
		{ID: "admin-node-onboarding", Title: "管理员：新增 GPU 节点与缓存验收", Category: "06 进阶与管理员", Markdown: "kubectl node admin docs", SortOrder: 610},
	}
	if err := r.SeedHelpDocuments(ctx, seed); err != nil {
		t.Fatal(err)
	}
	custom, err := r.CreateHelpDocument(ctx, domain.HelpDocument{ID: "team-faq", Title: "团队 FAQ", Category: "07 团队补充", Markdown: "custom answer", SortOrder: 900}, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.ChangeHelpDocument(ctx, custom.ID, custom.Version, "publish", 0, nil, "admin"); err != nil {
		t.Fatal(err)
	}
	override, err := r.CreateHelpDocument(ctx, domain.HelpDocument{ID: "mlflow", Title: "团队 MLflow 补充", Category: "07 团队补充", Markdown: "custom mlflow override", SortOrder: 901}, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.ChangeHelpDocument(ctx, override.ID, override.Version, "publish", 0, nil, "admin"); err != nil {
		t.Fatal(err)
	}
	adminOnly, err := r.CreateHelpDocument(ctx, domain.HelpDocument{ID: "team-admin-runbook", Title: "团队管理员 Runbook", Category: "06 进阶与管理员", Markdown: "custom admin source", SortOrder: 902}, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.ChangeHelpDocument(ctx, adminOnly.ID, adminOnly.Version, "publish", 0, nil, "admin"); err != nil {
		t.Fatal(err)
	}

	items, err := r.ListHelpDocuments(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	byID := make(map[string]domain.HelpDocument, len(items))
	for _, item := range items {
		byID[item.ID] = item
	}
	for _, id := range []string{"quickstart", "account-api", "data", "training-guide", "debug", "mlflow", "troubleshooting", "team-faq"} {
		if _, ok := byID[id]; !ok {
			t.Fatalf("public help is missing %s: %+v", id, items)
		}
	}
	for _, oldID := range []string{"code", "mlflow-api-with-pat", "admin-node-onboarding", "team-admin-runbook"} {
		if _, ok := byID[oldID]; ok {
			t.Fatalf("old seed document %s leaked into public help: %+v", oldID, items)
		}
	}
	if byID["team-faq"].Markdown != "custom answer" {
		t.Fatalf("custom published document was not preserved: %+v", byID["team-faq"])
	}
	if byID["mlflow"].Markdown != "custom mlflow override" {
		t.Fatalf("custom document did not override generated guide ID: %+v", byID["mlflow"])
	}
	mlflow := byID["mlflow"].Markdown
	for _, marker := range []string{"custom mlflow override"} {
		if !strings.Contains(mlflow, marker) {
			t.Fatalf("public MLflow guide is missing %q", marker)
		}
	}

	adminItems, err := r.ListHelpDocuments(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	adminByID := make(map[string]domain.HelpDocument, len(adminItems))
	for _, item := range adminItems {
		adminByID[item.ID] = item
	}
	if adminByID["admin-node-onboarding"].Markdown != "kubectl node admin docs" || adminByID["mlflow-api-with-pat"].Markdown != "old platform run id docs" {
		t.Fatalf("admin source documents were not preserved: %+v", adminItems)
	}
}

func TestHelpStoreValidationAndMissingVersions(t *testing.T) {
	r := helpRepo(t)
	ctx := context.Background()
	if _, err := r.CreateHelpDocument(ctx, domain.HelpDocument{}, "admin"); err == nil {
		t.Fatal("invalid draft accepted")
	}
	if err := r.SeedHelpDocuments(ctx, []domain.HelpDocument{{}}); err == nil {
		t.Fatal("invalid seed accepted")
	}
	if _, err := r.ChangeHelpDocument(ctx, "missing", 1, "publish", 0, nil, "admin"); !errors.Is(err, ErrHelpNotFound) {
		t.Fatal(err)
	}
	d := domain.HelpDocument{ID: "first", Title: "Title", Category: "Start", Markdown: "body"}
	if _, err := r.CreateHelpDocument(ctx, d, "admin"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.CreateHelpDocument(ctx, d, "admin"); !errors.Is(err, ErrHelpConflict) {
		t.Fatal(err)
	}
	for _, action := range []string{"save", "unknown"} {
		if _, err := r.ChangeHelpDocument(ctx, "first", 1, action, 0, nil, "admin"); err == nil {
			t.Fatal(action, "accepted")
		}
	}
	if _, err := r.ChangeHelpDocument(ctx, "first", 1, "restore", 99, nil, "admin"); !errors.Is(err, ErrHelpNotFound) {
		t.Fatal(err)
	}
	d.Markdown = ""
	if _, err := r.ChangeHelpDocument(ctx, "first", 1, "save", 0, &d, "admin"); err == nil {
		t.Fatal("invalid save accepted")
	}
	if _, err := r.HelpDocumentHistory(ctx, "missing"); !errors.Is(err, ErrHelpNotFound) {
		t.Fatal(err)
	}
	items, err := r.ListHelpDocuments(ctx, true)
	if err != nil || len(items) != 1 || items[0].Markdown != "body" {
		t.Fatal(items, err)
	}
	// Corrupt persistence must fail closed rather than leak a partial document.
	if err := r.db.Model(&HelpDocumentRecord{}).Where("id = ?", "first").Update("draft_json", "invalid").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := r.ListHelpDocuments(ctx, true); err == nil {
		t.Fatal("corrupt draft accepted")
	}
	if _, err := r.ChangeHelpDocument(ctx, "first", 1, "publish", 0, nil, "admin"); err == nil {
		t.Fatal("corrupt draft published")
	}
}
