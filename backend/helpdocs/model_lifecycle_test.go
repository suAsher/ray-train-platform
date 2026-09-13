package helpdocs

import (
	"strings"
	"testing"

	"ray-train-platform-backend/domain"
)

func TestModelLifecycleArticlesExposeUserWorkflowWithoutChangingSources(t *testing.T) {
	sources, err := Documents()
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 37 {
		t.Fatalf("original sources got %d want 37", len(sources))
	}
	articles := helpArticleTestByID(ProjectHelpArticles(withPlatformSeedActor(sources)))
	for _, id := range []string{modelRegistrationArticleID, modelMaintenanceArticleID} {
		article, ok := articles[id]
		if !ok {
			t.Fatalf("model article %s missing from article projection", id)
		}
		if err := article.HelpDocument.Validate(); err != nil {
			t.Fatalf("invalid model article %s: %v", id, err)
		}
		if article.CategoryID != "debug" || article.Summary == "" || len(article.Keywords) == 0 {
			t.Fatalf("model article %s missing results-category metadata: %+v", id, article)
		}
		for _, related := range article.RelatedIDs {
			if _, ok := articles[related]; !ok {
				t.Fatalf("model article %s links to missing article %s", id, related)
			}
		}
	}
	registration := articles[modelRegistrationArticleID].Markdown
	for _, marker := range []string{"本人", ".pth", ".pt", ".ckpt", ".onnx", ".safetensors", "原文件", "原目录", "8 MiB", "20 GiB", "PENDING", "COPYING", "READY", "FAILED", "SHA-256", "未知 / 未登记", "用户补充", "不能覆盖"} {
		if !strings.Contains(registration, marker) {
			t.Errorf("registration guidance missing %q", marker)
		}
	}
	maintenance := articles[modelMaintenanceArticleID].Markdown
	for _, marker := range []string{"跨团队", "SuperAdmin", "归档", "恢复", "不可变", "原有权限", "Model Registry", "独立评估", "审批", "Serving", "分别操作"} {
		if !strings.Contains(maintenance, marker) {
			t.Errorf("maintenance guidance missing %q", marker)
		}
	}
	for _, source := range sources {
		if source.ID == "artifacts" && !strings.Contains(articles[source.ID].Markdown, source.Markdown) {
			t.Fatal("model links replaced existing artifact guidance")
		}
	}
}

func TestModelLifecycleArticlesPreservePublishedOverridesAndInput(t *testing.T) {
	manual := domain.HelpDocument{ID: modelRegistrationArticleID, Title: "团队登记说明", Category: "调试与训练结果", Markdown: "用户保存的登记要求", UpdatedBy: "editor", Version: 7}
	input := []domain.HelpDocument{manual}
	articles := ProjectHelpArticles(input)
	count := 0
	for _, article := range articles {
		if article.ID != manual.ID {
			continue
		}
		count++
		if !strings.Contains(article.Markdown, manual.Markdown) || article.UpdatedBy != manual.UpdatedBy || article.Version != manual.Version {
			t.Fatalf("published manual model guidance was replaced: %+v", article)
		}
	}
	if count != 1 {
		t.Fatalf("got %d copies of manual model article", count)
	}
	if len(input) != 1 || input[0] != manual {
		t.Fatal("projection changed caller-owned source documents")
	}
}
