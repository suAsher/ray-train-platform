package helpdocs

import (
	"ray-train-platform-backend/domain"
	"strings"
	"testing"
)

func TestModelReleaseServingHelpPreservesQuestionsAndBoundaries(t *testing.T) {
	sources, err := Documents()
	if err != nil {
		t.Fatal(err)
	}
	articles := helpArticleTestByID(ProjectHelpArticles(withPlatformSeedActor(sources)))
	if len(KnownHelpArticleIDs()) != 43 {
		t.Fatalf("want 43 user questions, got %d", len(KnownHelpArticleIDs()))
	}
	for id, markers := range map[string][]string{
		modelRegistryArticleID:      {"Model Registry", "READY", "原权重", "不会", "MLflow flavor"},
		modelReleaseArticleID:       {"不能", "申请人", "模型所有者", "TenantAdmin", "SuperAdmin", "理由", "回滚", "不会", "TEAM"},
		modelServingCodeArticleID:   {"源码 ZIP", "serving_sdk.py", "model_adapter.py", "ServingClient", "64 MiB", "内网", "模型精度", "model-serving-http/v1"},
		modelServingUseArticleID:    {"1 GPU", "1 小时", "7 天", "先停后启", "中断", "/raytrain/api/v1/model-services/", "invocations", "application/json", "models:invoke", "credentials: 'same-origin'"},
		modelServingErrorsArticleID: {"排队", "SHA-256", "健康", "精度", "413", "429", "停止", "超时"},
	} {
		article, ok := articles[id]
		if !ok {
			t.Fatalf("missing %s", id)
		}
		if err := article.HelpDocument.Validate(); err != nil {
			t.Errorf("%s: %v", id, err)
		}
		for _, marker := range markers {
			if !strings.Contains(article.Markdown, marker) {
				t.Errorf("%s missing %q", id, marker)
			}
		}
		for _, related := range article.RelatedIDs {
			if _, ok := articles[related]; !ok {
				t.Errorf("%s links to missing %s", id, related)
			}
		}
	}
	for _, id := range []string{modelMaintenanceArticleID, modelEvaluationResultsID, "mlflow-framework-metrics"} {
		if strings.Contains(articles[id].Markdown, "Serving 尚未上线") || strings.Contains(articles[id].Markdown, "审批和推理服务尚未上线") {
			t.Errorf("stale lifecycle status in %s", id)
		}
	}
}
func TestModelServingHelpKeepsCustomPublishedContent(t *testing.T) {
	custom := domain.HelpDocument{ID: modelServingUseArticleID, Title: "团队实际调用约定", Category: "调试与训练结果", Markdown: "既有用户自定义正文", UpdatedBy: "team-editor", Version: 9}
	articles := ProjectHelpArticles([]domain.HelpDocument{custom})
	count := 0
	for _, a := range articles {
		if a.ID == custom.ID {
			count++
			if a.Markdown != custom.Markdown || a.Version != custom.Version || a.UpdatedBy != custom.UpdatedBy {
				t.Fatal("custom serving guide replaced")
			}
		}
	}
	if count != 1 {
		t.Fatalf("custom guide count %d", count)
	}
}
