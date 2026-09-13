package helpdocs

import (
	"strings"
	"testing"

	"ray-train-platform-backend/domain"
)

func TestLoggingTroubleshootingIsPublishedAndDiscoverable(t *testing.T) {
	seed, err := Documents()
	if err != nil {
		t.Fatal(err)
	}
	articles := ProjectHelpArticles(withPlatformSeedActor(seed))
	var logs, metrics domain.HelpArticle
	for _, article := range articles {
		switch article.ID {
		case "observability":
			logs = article
		case "mlflow-framework-metrics":
			metrics = article
		}
	}
	for _, marker := range []string{"训练日志为什么重复两行？", "logger.propagate = False", "NullHandler", "global rank 0", "原始", "初始化顺序", "源码维护者", "不需要个人 PAT"} {
		if !strings.Contains(logs.Markdown, marker) {
			t.Errorf("published logging article missing %q", marker)
		}
	}
	if !strings.Contains(strings.Join(logs.Keywords, " "), "重复日志") || !strings.Contains(metrics.Markdown, "#observability") {
		t.Fatal("duplicate logging guidance is not searchable or linked from training MLflow")
	}
	var legacy string
	for _, guide := range PublicGuides() {
		if guide.ID == "troubleshooting" {
			legacy = guide.Markdown
		}
	}
	if !strings.Contains(legacy, "logger.propagate = False") {
		t.Fatal("legacy help entry omits logging guidance")
	}
}

func TestLoggingSupplementPreservesExistingUserHelp(t *testing.T) {
	custom := domain.HelpDocument{ID: "observability", Markdown: "团队保留的日志查询步骤", UpdatedBy: "team-editor", Version: 12}
	article := ProjectHelpArticle(custom)
	if !strings.Contains(article.Markdown, custom.Markdown) || !strings.Contains(article.Markdown, "训练日志为什么重复两行？") {
		t.Fatal("logging supplement must preserve existing published content")
	}
	if article.Version != 12 || custom.Markdown != "团队保留的日志查询步骤" {
		t.Fatal("projection changed stored document")
	}
}
