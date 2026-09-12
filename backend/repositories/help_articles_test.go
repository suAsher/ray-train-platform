package repositories

import (
	"context"
	"strings"
	"testing"

	"ray-train-platform-backend/domain"
	"ray-train-platform-backend/helpdocs"
)

func TestHelpArticlesExposeThirtyFourQuestionDocumentsFromPublishedSeed(t *testing.T) {
	r := helpRepo(t)
	ctx := context.Background()
	seed, err := helpdocs.Documents()
	if err != nil {
		t.Fatal(err)
	}
	if err := r.SeedHelpDocuments(ctx, seed); err != nil {
		t.Fatal(err)
	}

	items, err := r.ListHelpArticles(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 34 {
		t.Fatalf("public articles got %d want 34", len(items))
	}
	byID := helpArticlesByID(items)
	for _, source := range seed {
		if publicAdminOnlyHelpIDs[source.ID] {
			if _, ok := byID[source.ID]; ok {
				t.Fatalf("admin seed %s leaked into articles", source.ID)
			}
			continue
		}
		article, ok := byID[source.ID]
		if !ok {
			t.Fatalf("user seed %s missing from articles", source.ID)
		}
		if article.CategoryID == "" || article.Summary == "" || len(article.Keywords) == 0 || len(article.RelatedIDs) == 0 {
			t.Fatalf("article %s missing user metadata: %+v", source.ID, article)
		}
	}
	assertArticle(t, byID, "quickstart", "start", "第一次如何跑通一条训练任务？", []string{"### 第一条任务", "我的 GPU 配额"})
	assertArticle(t, byID, "cli-onboarding-v2", "start", "CLI 如何安装、登录和升级？", []string{"spk-rayjob login --server 'https://raytrain.wellspiking.ai' --token-stdin", "command -v spk-rayjob"})
	assertArticle(t, byID, "custom-environment", "data", "缺少依赖，如何准备自己的训练镜像？", []string{"raytrain-base:ray2.58.0-py310-torch2.4.1-cu121-20260906"})
	assertArticle(t, byID, "submit", "training", "如何提交单卡、单机多卡和多机训练？", []string{"python3 tools/train_managed.py", "--cpu-per-worker 8", "--memory-per-worker 32Gi"})
	assertArticle(t, byID, "debug", "debug", "如何使用 JupyterLab 和 VS Code？", []string{"JupyterLab", "VS Code"})
	assertArticle(t, byID, "worker-connect-and-scheduling-boundary", "debug", "如何连接自己的训练 Worker？", []string{"spk-rayjob connect JOB_ID"})
	assertArticle(t, byID, "mlflow-api-with-pat", "mlflow", "如何调用 MLflow API 查询实验与 Run？", []string{"MLFLOW_TRACKING_URI='https://raytrain.wellspiking.ai/api/v1/mlflow-native'", "next_page_token 字段，把它原样放进下一次请求正文的 page_token", "403 查 PAT 是否包含 mlflow:full"})
	assertArticle(t, byID, "errors", "troubleshooting", "遇到 401、403、Pending 等错误先检查什么？", []string{"401 / INVALID_AUTHENTICATION", "413"})
}

func TestHelpArticlesPreserveSeedMarkdownAndPublicGuideSupplements(t *testing.T) {
	r := helpRepo(t)
	ctx := context.Background()
	seed, err := helpdocs.Documents()
	if err != nil {
		t.Fatal(err)
	}
	if err := r.SeedHelpDocuments(ctx, seed); err != nil {
		t.Fatal(err)
	}
	items, err := r.ListHelpArticles(ctx)
	if err != nil {
		t.Fatal(err)
	}
	byID := helpArticlesByID(items)
	rewrittenMLflowSeed := map[string]bool{
		"mlflow":                   true,
		"mlflow-api-with-pat":      true,
		"mlflow-external-tracking": true,
	}
	for _, source := range seed {
		if publicAdminOnlyHelpIDs[source.ID] || rewrittenMLflowSeed[source.ID] {
			continue
		}
		article := byID[source.ID]
		if source.ID == "portal-browser-tools-and-queue" {
			projected, moved, ok := splitSubmittedSuspendedForArticleTest(t, source.Markdown)
			if !ok {
				t.Fatal("portal queue source did not contain submitted/suspended section")
			}
			if !strings.Contains(article.Markdown, projected) || strings.Contains(article.Markdown, moved) || !strings.Contains(byID["scheduling-topology"].Markdown, moved) {
				t.Fatalf("portal queue section was not split into scheduling while preserving the rest")
			}
			continue
		}
		expected := source.Markdown
		if source.ID == "mlflow-framework-metrics" {
			expected = markdownAfterFirstParagraph(t, source.Markdown)
		}
		if !strings.Contains(article.Markdown, expected) {
			t.Fatalf("article %s does not preserve source markdown", source.ID)
		}
	}

	assertMarkdownContains(t, byID["cli-onboarding-v2"].Markdown, []string{"### 安装 CLI", "### 登录和检查"})
	assertMarkdownContains(t, byID["storage"].Markdown, []string{"### 数据空间"})
	assertMarkdownContains(t, byID["data-mode"].Markdown, []string{"### 数据模式"})
	assertMarkdownContains(t, byID["resume"].Markdown, []string{"### 续训"})
	assertMarkdownContains(t, byID["mlflow-api-with-pat"].Markdown, []string{"### 原生 MLflow SDK", "page.token"})
	assertMarkdownContains(t, byID["mlflow"].Markdown, []string{"### 推荐入口"})
}

func TestHelpArticlesReadPublishedJSONAndKeepDraftPrivate(t *testing.T) {
	r := helpRepo(t)
	ctx := context.Background()
	published := "published custom FAQ"
	draft := "secret draft FAQ"
	doc, err := r.CreateHelpDocument(ctx, domain.HelpDocument{ID: "team-faq", Title: "团队 FAQ", Category: "07 团队补充", SortOrder: 900, Markdown: published}, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.ChangeHelpDocument(ctx, doc.ID, doc.Version, "publish", 0, nil, "admin"); err != nil {
		t.Fatal(err)
	}
	doc.Markdown = draft
	if _, err := r.ChangeHelpDocument(ctx, doc.ID, 2, "save", 0, &doc, "editor"); err != nil {
		t.Fatal(err)
	}

	items, err := r.ListHelpArticles(ctx)
	if err != nil {
		t.Fatal(err)
	}
	byID := helpArticlesByID(items)
	article := byID["team-faq"]
	if article.ID == "" || article.Markdown != published || article.Category != "07 团队补充" || article.CategoryID == "" {
		t.Fatalf("custom article did not use published snapshot and preserve category: %+v", article)
	}
	if strings.Contains(article.Markdown, draft) {
		t.Fatalf("draft markdown leaked into public articles: %+v", article)
	}
}

func TestHelpArticlesPreserveManualKnownArticleSourceAndHistory(t *testing.T) {
	r := helpRepo(t)
	ctx := context.Background()
	seed, err := helpdocs.Documents()
	if err != nil {
		t.Fatal(err)
	}
	if err := r.SeedHelpDocuments(ctx, seed); err != nil {
		t.Fatal(err)
	}
	environmentBody := "  custom environment full body\n\nkeeps every byte  "
	workerBody := "worker connect full body\nwith scheduling note"
	manualQueueSection := "### 任务处于 SUBMITTED / Suspended\n\nmanual queue body stays here"
	portalBody := "manual tool intro\n\n" + manualQueueSection + "\n\n### JupyterLab / VS Code 打不开\n\nmanual browser body"
	for _, change := range []struct{ id, body string }{{"custom-environment", environmentBody}, {"worker-connect-and-scheduling-boundary", workerBody}, {"portal-browser-tools-and-queue", portalBody}} {
		adminItems, err := r.ListHelpDocuments(ctx, true)
		if err != nil {
			t.Fatal(err)
		}
		current := helpDocumentsByID(adminItems)[change.id]
		current.Markdown = change.body
		current.Title = "人工编辑 " + change.id
		if _, err := r.ChangeHelpDocument(ctx, change.id, current.Version, "save", 0, &current, "admin"); err != nil {
			t.Fatal(err)
		}
		if _, err := r.ChangeHelpDocument(ctx, change.id, current.Version+1, "publish", 0, nil, "admin"); err != nil {
			t.Fatal(err)
		}
	}

	articles, err := r.ListHelpArticles(ctx)
	if err != nil {
		t.Fatal(err)
	}
	articleByID := helpArticlesByID(articles)
	if !strings.HasPrefix(articleByID["custom-environment"].Markdown, environmentBody) || !strings.HasPrefix(articleByID["worker-connect-and-scheduling-boundary"].Markdown, workerBody) || !strings.Contains(articleByID["portal-browser-tools-and-queue"].Markdown, manualQueueSection) {
		t.Fatal("manual published body must remain an unchanged prefix before any public supplement")
	}
	if strings.Contains(articleByID["scheduling-topology"].Markdown, "manual queue body stays here") {
		t.Fatal("manual portal queue section must not move into scheduling article")
	}
	adminItems, err := r.ListHelpDocuments(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	adminByID := helpDocumentsByID(adminItems)
	if adminByID["custom-environment"].Markdown != environmentBody || adminByID["worker-connect-and-scheduling-boundary"].Markdown != workerBody || adminByID["portal-browser-tools-and-queue"].Markdown != portalBody {
		t.Fatalf("admin sources changed after article projection: %+v", adminByID)
	}
	for _, change := range []struct{ id, body string }{{"custom-environment", environmentBody}, {"worker-connect-and-scheduling-boundary", workerBody}, {"portal-browser-tools-and-queue", portalBody}} {
		history, err := r.HelpDocumentHistory(ctx, change.id)
		if err != nil {
			t.Fatal(err)
		}
		if history[0].Markdown != change.body {
			t.Fatalf("history latest for %s lost manual body: %+v", change.id, history[0])
		}
	}
}

func TestHelpArticleLegacyAnchorsExposeOldGuideSections(t *testing.T) {
	r := helpRepo(t)
	ctx := context.Background()
	seed, err := helpdocs.Documents()
	if err != nil {
		t.Fatal(err)
	}
	if err := r.SeedHelpDocuments(ctx, seed); err != nil {
		t.Fatal(err)
	}
	items, err := r.ListHelpArticles(ctx)
	if err != nil {
		t.Fatal(err)
	}
	byID := helpArticlesByID(items)
	assertLegacyAnchor(t, byID["errors"], "troubleshooting", "section-常见错误速查", "")
	assertLegacyAnchor(t, byID["custom-environment"], "training-guide", "section-缺少环境-自定义训练镜像", "")
	assertLegacyAnchor(t, byID["custom-environment"], "data", "section-缺少环境-自定义训练镜像", "")
	assertLegacyAnchor(t, byID["quickstart"], "quickstart", "section-第一次跑通", "")
	assertLegacyAnchor(t, byID["debug"], "debug", "section-交互式调试环境", "")
	assertLegacyAnchor(t, byID["mlflow"], "mlflow", "section-查看训练实验与结果", "")
	assertLegacyAnchor(t, byID["scheduling-topology"], "troubleshooting", "section-任务处于-submitted-suspended", "section-任务处于-submitted-suspended")
}

func assertArticle(t *testing.T, byID map[string]domain.HelpArticle, id, categoryID, title string, markers []string) {
	t.Helper()
	article, ok := byID[id]
	if !ok {
		t.Fatalf("article %s missing", id)
	}
	if article.CategoryID != categoryID || article.Title != title {
		t.Fatalf("article %s got category/title %s/%s want %s/%s", id, article.CategoryID, article.Title, categoryID, title)
	}
	for _, marker := range markers {
		if !strings.Contains(article.Markdown, marker) {
			t.Fatalf("article %s missing marker %q", id, marker)
		}
	}
}

func assertLegacyAnchor(t *testing.T, article domain.HelpArticle, topicID, sectionID, articleSectionID string) {
	t.Helper()
	for _, anchor := range article.LegacyAnchors {
		if anchor.TopicID == topicID && anchor.SectionID == sectionID && anchor.ArticleSectionID == articleSectionID {
			return
		}
	}
	t.Fatalf("article %s missing legacy anchor %s/%s -> %q: %+v", article.ID, topicID, sectionID, articleSectionID, article.LegacyAnchors)
}

func helpArticlesByID(items []domain.HelpArticle) map[string]domain.HelpArticle {
	out := make(map[string]domain.HelpArticle, len(items))
	for _, item := range items {
		out[item.ID] = item
	}
	return out
}

func helpDocumentsByID(items []domain.HelpDocument) map[string]domain.HelpDocument {
	out := make(map[string]domain.HelpDocument, len(items))
	for _, item := range items {
		out[item.ID] = item
	}
	return out
}

func splitSubmittedSuspendedForArticleTest(t *testing.T, markdown string) (string, string, bool) {
	t.Helper()
	startMarker := "### 任务处于 SUBMITTED / Suspended"
	endMarker := "\n\n### JupyterLab / VS Code 打不开"
	start := strings.Index(markdown, startMarker)
	if start < 0 {
		return markdown, "", false
	}
	endRelative := strings.Index(markdown[start+len(startMarker):], endMarker)
	if endRelative < 0 {
		return markdown, "", false
	}
	end := start + len(startMarker) + endRelative
	moved := markdown[start:end]
	projected := markdown[:start] + "排队说明已移到[任务一直排队，为什么还没开始？](#scheduling-topology)，本篇保留浏览器工具和 MLflow 页面打不开的处理方法。" + markdown[end:]
	return projected, moved, true
}
