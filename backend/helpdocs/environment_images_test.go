package helpdocs

import (
	"strings"
	"testing"
	"ray-train-platform-backend/domain"
)

func TestEnvironmentImageGuidePreservesUserQuestionsAndBoundaries(t *testing.T) {
	article := ProjectHelpArticle(domain.HelpDocument{ID:"custom-environment", UpdatedBy:PlatformSeedActor})
	for _, text := range []string{"保存训练环境", "CLI Secret", "集群 CPU", "固定摘要", "READY", "原有 Base", "不会把整个容器", "停止或重建", "Harbor 仓库自身"} {
		if !strings.Contains(article.Markdown, text) { t.Errorf("missing user guidance %q", text) }
	}
	manual := ProjectHelpArticle(domain.HelpDocument{ID:"custom-environment", UpdatedBy:"user", Markdown:"团队自己维护的说明"})
	if !strings.HasPrefix(manual.Markdown, "团队自己维护的说明") { t.Fatal("overwrote manually published guidance") }
}
