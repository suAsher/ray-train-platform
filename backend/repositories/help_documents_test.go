package repositories

import (
	"context"
	"errors"
	"strings"
	"testing"

	"ray-train-platform-backend/domain"
	"ray-train-platform-backend/helpdocs"
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
	customEnvironment, err := r.CreateHelpDocument(ctx, domain.HelpDocument{ID: "custom-environment", Title: "团队镜像登记补充", Category: "02 准备代码和数据", Markdown: "full custom environment body", SortOrder: 150}, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.ChangeHelpDocument(ctx, customEnvironment.ID, customEnvironment.Version, "publish", 0, nil, "admin"); err != nil {
		t.Fatal(err)
	}
	workerConnect, err := r.CreateHelpDocument(ctx, domain.HelpDocument{ID: "worker-connect-and-scheduling-boundary", Title: "团队 Worker 连接补充", Category: "03 提交与运行", Markdown: "full worker connect body", SortOrder: 250}, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.ChangeHelpDocument(ctx, workerConnect.ID, workerConnect.Version, "publish", 0, nil, "admin"); err != nil {
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
	for _, oldID := range []string{"code", "mlflow-api-with-pat", "admin-node-onboarding", "team-admin-runbook", "custom-environment", "worker-connect-and-scheduling-boundary"} {
		if _, ok := byID[oldID]; ok {
			t.Fatalf("old seed document %s leaked into public help: %+v", oldID, items)
		}
	}
	if byID["team-faq"].Markdown != "custom answer" {
		t.Fatalf("custom published document was not preserved: %+v", byID["team-faq"])
	}
	if !strings.HasPrefix(byID["mlflow"].Markdown, "custom mlflow override") || !strings.Contains(byID["mlflow"].Markdown, "查询实验、Run 和历史指标") {
		t.Fatalf("custom document did not override generated guide ID: %+v", byID["mlflow"])
	}
	for _, marker := range []string{"团队镜像登记补充", "full custom environment body"} {
		if !strings.Contains(byID["data"].Markdown, marker) {
			t.Fatalf("legacy custom environment was not folded into data guide; missing %q in %q", marker, byID["data"].Markdown)
		}
	}
	for _, marker := range []string{"团队 Worker 连接补充", "full worker connect body"} {
		if !strings.Contains(byID["debug"].Markdown, marker) {
			t.Fatalf("legacy worker connect was not folded into debug guide; missing %q in %q", marker, byID["debug"].Markdown)
		}
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
	if adminByID["custom-environment"].Markdown != "full custom environment body" || adminByID["worker-connect-and-scheduling-boundary"].Markdown != "full worker connect body" {
		t.Fatalf("admin legacy custom source documents were not preserved: %+v", adminItems)
	}
	environmentHistory, err := r.HelpDocumentHistory(ctx, "custom-environment")
	if err != nil {
		t.Fatal(err)
	}
	if environmentHistory[0].Markdown != "full custom environment body" || environmentHistory[len(environmentHistory)-1].Markdown != "full custom environment body" {
		t.Fatalf("custom environment history changed original markdown: %+v", environmentHistory)
	}
	workerHistory, err := r.HelpDocumentHistory(ctx, "worker-connect-and-scheduling-boundary")
	if err != nil {
		t.Fatal(err)
	}
	if workerHistory[0].Markdown != "full worker connect body" || workerHistory[len(workerHistory)-1].Markdown != "full worker connect body" {
		t.Fatalf("worker connect history changed original markdown: %+v", workerHistory)
	}
}

func TestPublicHelpDocumentsFoldPublishedSeedContentIntoSevenGuides(t *testing.T) {
	r := helpRepo(t)
	ctx := context.Background()
	seed, err := helpdocs.Documents()
	if err != nil {
		t.Fatal(err)
	}
	if err := r.SeedHelpDocuments(ctx, seed); err != nil {
		t.Fatal(err)
	}

	items, err := r.ListHelpDocuments(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 7 {
		t.Fatalf("public help should keep seven top-level guides, got %d: %+v", len(items), items)
	}
	byID := make(map[string]domain.HelpDocument, len(items))
	for _, item := range items {
		byID[item.ID] = item
	}
	for _, id := range []string{"quickstart", "account-api", "data", "training-guide", "debug", "mlflow", "troubleshooting"} {
		if _, ok := byID[id]; !ok {
			t.Fatalf("public help missing consolidated guide %s: %+v", id, items)
		}
	}
	for _, oldID := range []string{
		"access",
		"cache",
		"cli-onboarding-v2",
		"code",
		"command-recipes",
		"custom-environment",
		"data-mode",
		"datasets",
		"errors",
		"mlflow-api-with-pat",
		"mlflow-external-tracking",
		"mlflow-framework-metrics",
		"ray-data",
		"resume",
		"scaling",
		"scheduling-topology",
		"storage",
		"streaming",
		"streaming-validation",
		"submit",
		"uploads",
		"worker-connect-and-scheduling-boundary",
	} {
		if _, ok := byID[oldID]; ok {
			t.Fatalf("legacy seed document %s leaked as a top-level public document: %+v", oldID, items)
		}
	}
	for _, adminID := range []string{"admin-node-onboarding", "admin-team-retirement", "idc-sync-lifecycle"} {
		if _, ok := byID[adminID]; ok {
			t.Fatalf("admin seed document %s leaked into public help: %+v", adminID, items)
		}
	}
	assertMarkdownContains(t, byID["quickstart"].Markdown, []string{
		"### 第一次跑通",
		"### CLI 安装、登录与升级",
		"SHA-256",
		"spk-rayjob login --server 'https://raytrain.wellspiking.ai' --token-stdin",
	})
	assertMarkdownContains(t, byID["data"].Markdown, []string{
		"### 代码怎么进来",
		"### 缺少环境？自定义训练镜像",
		"已发布基础镜像",
		"raytrain-base:ray2.58.0-py310-torch2.4.1-cu121-20260906",
		"### 如何使用缓存加速",
		"5,625 MiB/s",
		"### 五种数据模式怎么选",
		"### 版本化数据集",
		"### 大文件上传与恢复",
	})
	assertMarkdownContains(t, byID["training-guide"].Markdown, []string{
		"### 命令提交示例：spk-rayjob 与原生 Ray",
		"RAY_JOB_HEADERS",
		"### 提交任务与分布式训练",
		"### 如何使用 Ray Data",
		"get_dataset_shard",
		"### Ray Train 托管 + Ray Data + Parquet + NVMe",
		"### 断点续训",
		"### 扩卡效果怎么验收",
		"### 多 Worker、多机与拓扑排队",
		"### 验收固定版本的数据训练",
	})
	assertMarkdownContains(t, byID["debug"].Markdown, []string{
		"### 交互式调试环境",
		"### 连接自己的训练 Worker",
	})
	assertMarkdownContains(t, byID["mlflow"].Markdown, []string{
		"MLFLOW_TRACKING_URI='https://raytrain.wellspiking.ai/api/v1/mlflow-native'",
		"### 让训练指标显示在 MLflow",
		"mlflow.log_metric",
		"### 查询实验、Run 和历史指标",
		"### 在自己的程序中记录实验",
		"mlflow.log_artifact",
		"experiments/search",
		"runs/log-batch",
		"artifacts/list",
		"MlflowClient.download_artifacts",
		"REPLACE_EXPERIMENT_ID_FROM_SEARCH",
		"next_page_token 字段，把它原样放进下一次请求正文的 page_token",
		"403 查 PAT 是否包含 mlflow:full",
		"start_managed_mlflow_run(training_parameters, rank=global_rank, world_size=world_size)",
	})
	for _, staleMarker := range []string{
		"MLflow 总览",
		"API 接入",
		"高级",
		"外部实验",
		"独立实验兼容入口",
		"当前仅支持六个方法",
		"SDK Tracking URI 为 `https://raytrain.wellspiking.ai/api/v1/mlflow-tracking`，不是原生管理页面的地址",
		"平台 Run ID",
		"平台实验 ID",
		"受限集成",
		"集成接入",
		"mlflow-tracking",
	} {
		if strings.Contains(byID["mlflow"].Markdown, staleMarker) {
			t.Fatalf("public MLflow guide exposed stale legacy wording %q in %q", staleMarker, byID["mlflow"].Markdown)
		}
	}
	assertMarkdownContains(t, byID["mlflow"].Markdown, []string{
		"HTTP 错误按 MLflow 原生响应处理",
	})
	assertMarkdownContains(t, byID["troubleshooting"].Markdown, []string{
		"### 训练慢，怎么定位瓶颈",
		"### 常见错误速查",
		"### 打不开工具或任务一直排队",
		"### 日志有 Loss，为什么页面没有曲线",
	})

	adminItems, err := r.ListHelpDocuments(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(adminItems) != len(seed) {
		t.Fatalf("admin list should retain original seed documents, got %d want %d", len(adminItems), len(seed))
	}
	adminByID := make(map[string]domain.HelpDocument, len(adminItems))
	for _, item := range adminItems {
		adminByID[item.ID] = item
	}
	for _, source := range seed {
		if adminByID[source.ID].Markdown != source.Markdown {
			t.Fatalf("admin source markdown changed for %s", source.ID)
		}
	}
}

func TestPublicHelpDocumentsPreserveFullSeedMarkdownInFoldedGuides(t *testing.T) {
	r := helpRepo(t)
	ctx := context.Background()
	seed, err := helpdocs.Documents()
	if err != nil {
		t.Fatal(err)
	}
	if err := r.SeedHelpDocuments(ctx, seed); err != nil {
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
	rewrittenMLflowSeed := map[string]bool{
		"mlflow":                   true,
		"mlflow-api-with-pat":      true,
		"mlflow-external-tracking": true,
	}
	for _, source := range seed {
		if publicAdminOnlyHelpIDs[source.ID] {
			continue
		}
		target := legacyPublicHelpGuideIDs[source.ID]
		if target == "" {
			t.Fatalf("seed document %s has no public target", source.ID)
		}
		targetGuide, ok := byID[target]
		if !ok {
			t.Fatalf("public target %s for seed %s is missing", target, source.ID)
		}
		if rewrittenMLflowSeed[source.ID] {
			continue
		}
		expected := source.Markdown
		if source.ID == "mlflow-framework-metrics" {
			expected = markdownAfterFirstParagraph(t, source.Markdown)
		}
		if !strings.Contains(targetGuide.Markdown, expected) {
			t.Fatalf("public guide %s does not preserve full markdown for seed %s", target, source.ID)
		}
	}
}

func TestPublicHelpAllowsKnownUserAdvancedDocsButFiltersAdminDocs(t *testing.T) {
	items := []domain.HelpDocument{
		{ID: "quickstart", Title: "Seed", Category: "01 开始使用", Markdown: "seed", Version: 3, PublishedVersion: 3, UpdatedBy: platformSeedActor},
		{ID: "cache", Title: "Cache", Category: "06 进阶与管理员", Markdown: "cache user guide", Version: 3, PublishedVersion: 3, UpdatedBy: platformSeedActor},
		{ID: "ray-data", Title: "Ray Data", Category: "06 进阶与管理员", Markdown: "ray data user guide", Version: 3, PublishedVersion: 3, UpdatedBy: platformSeedActor},
		{ID: "admin-node-onboarding", Title: "Admin", Category: "06 进阶与管理员", Markdown: "node admin guide", Version: 3, PublishedVersion: 3, UpdatedBy: platformSeedActor},
		{ID: "team-admin-runbook", Title: "Admin Custom", Category: "06 进阶与管理员", Markdown: "team admin guide", Version: 2, PublishedVersion: 2, UpdatedBy: "admin"},
	}

	got := publicHelpDocuments(items)
	byID := make(map[string]domain.HelpDocument, len(got))
	for _, item := range got {
		byID[item.ID] = item
	}
	if _, ok := byID["admin-node-onboarding"]; ok {
		t.Fatalf("platform admin guide leaked into public help: %+v", got)
	}
	if _, ok := byID["team-admin-runbook"]; ok {
		t.Fatalf("custom admin guide leaked into public help: %+v", got)
	}
	for _, marker := range []string{"cache user guide", "ray data user guide"} {
		if !strings.Contains(byID["data"].Markdown, marker) && !strings.Contains(byID["training-guide"].Markdown, marker) {
			t.Fatalf("known user advanced seed content was filtered by old admin category; missing %q in %+v", marker, got)
		}
	}
}

func TestPublicHelpSeedCoverageMapAccountsForEverySeedDocument(t *testing.T) {
	seed, err := helpdocs.Documents()
	if err != nil {
		t.Fatal(err)
	}
	adminOnly := map[string]bool{
		"admin-node-onboarding": true,
		"admin-team-retirement": true,
		"idc-sync-lifecycle":    true,
	}
	guideIDs := publicGuideIDs()
	for _, source := range seed {
		target, mapped := legacyPublicHelpGuideIDs[source.ID]
		if adminOnly[source.ID] {
			if mapped {
				t.Fatalf("admin seed %s should not map to public guide %s", source.ID, target)
			}
			continue
		}
		if !mapped {
			t.Fatalf("user seed %s (%s) has no public guide mapping", source.ID, source.Title)
		}
		if _, ok := guideIDs[target]; !ok {
			t.Fatalf("user seed %s maps to unknown public guide %s", source.ID, target)
		}
	}
}

func assertMarkdownContains(t *testing.T, markdown string, markers []string) {
	t.Helper()
	for _, marker := range markers {
		if !strings.Contains(markdown, marker) {
			t.Fatalf("markdown missing %q", marker)
		}
	}
}

func markdownAfterFirstParagraph(t *testing.T, markdown string) string {
	t.Helper()
	index := strings.Index(markdown, "\n\n")
	if index < 0 {
		t.Fatalf("markdown has no paragraph break: %q", markdown)
	}
	return markdown[index+2:]
}

func TestPublicHelpWithoutPlatformSeedKeepsLegacyCustomDocsInInputOrder(t *testing.T) {
	items := []domain.HelpDocument{
		{ID: "worker-connect-and-scheduling-boundary", Title: "Worker", Category: "03 提交与运行", Markdown: "worker original", SortOrder: 250, UpdatedBy: "admin"},
		{ID: "team-faq", Title: "FAQ", Category: "07 团队补充", Markdown: "faq", SortOrder: 900, UpdatedBy: "admin"},
		{ID: "custom-environment", Title: "Environment", Category: "02 准备代码和数据", Markdown: "environment original", SortOrder: 150, UpdatedBy: "admin"},
		{ID: "team-admin-runbook", Title: "Admin", Category: "06 进阶与管理员", Markdown: "admin", SortOrder: 910, UpdatedBy: "admin"},
	}
	got := publicHelpDocuments(items)
	if len(got) != 3 {
		t.Fatalf("got %d public documents: %+v", len(got), got)
	}
	for i, want := range []string{"worker-connect-and-scheduling-boundary", "team-faq", "custom-environment"} {
		if got[i].ID != want {
			t.Fatalf("document %d got %s want %s: %+v", i, got[i].ID, want, got)
		}
	}
}

func TestPublicHelpFoldsLegacyIntoExactGuideOverrideOnce(t *testing.T) {
	items := []domain.HelpDocument{
		{ID: "quickstart", Title: "Seed", Category: "01 开始使用", Markdown: "seed", Version: 3, PublishedVersion: 3, UpdatedBy: platformSeedActor},
		{ID: "custom-environment", Title: "Legacy Environment", Category: "02 准备代码和数据", Markdown: "  legacy leading\n\nlegacy trailing  ", Version: 2, PublishedVersion: 2, SortOrder: 150, UpdatedBy: "admin"},
		{ID: "data", Title: "Data Override", Category: "06 进阶与管理员", Markdown: "data override body", Version: 4, PublishedVersion: 4, SortOrder: 901, UpdatedBy: "admin"},
	}
	got := publicHelpDocuments(items)
	var dataCount int
	var data domain.HelpDocument
	for _, item := range got {
		if item.ID == "custom-environment" {
			t.Fatalf("legacy custom document leaked as standalone item: %+v", got)
		}
		if item.ID == "data" {
			dataCount++
			data = item
		}
	}
	if dataCount != 1 {
		t.Fatalf("data guide count got %d want 1: %+v", dataCount, got)
	}
	if !strings.Contains(data.Markdown, "data override body") || !strings.Contains(data.Markdown, "### Legacy Environment") || !strings.Contains(data.Markdown, "  legacy leading\n\nlegacy trailing  ") {
		t.Fatalf("exact guide override did not retain override and legacy markdown: %q", data.Markdown)
	}
}

func TestAppendLegacyPublicSectionsDoesNotMutateInputOrTrimMarkdown(t *testing.T) {
	sections := []domain.HelpDocument{
		{ID: "b", Title: "B", Markdown: "  b body\n", SortOrder: 20},
		{ID: "a", Title: "A", Markdown: "\n a body  ", SortOrder: 10},
	}
	got := appendLegacyPublicSections(domain.HelpDocument{ID: "data", Markdown: "base"}, sections)
	if sections[0].ID != "b" || sections[1].ID != "a" {
		t.Fatalf("appendLegacyPublicSections mutated input order: %+v", sections)
	}
	a := strings.Index(got.Markdown, "### A")
	b := strings.Index(got.Markdown, "### B")
	if a < 0 || b < 0 || a > b {
		t.Fatalf("folded sections were not sorted by document order: %q", got.Markdown)
	}
	for _, marker := range []string{"\n a body  ", "  b body\n"} {
		if !strings.Contains(got.Markdown, marker) {
			t.Fatalf("folded markdown was trimmed or changed; missing %q in %q", marker, got.Markdown)
		}
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
