package helpdocs

import (
	"strings"
	"testing"

	"ray-train-platform-backend/domain"
)

func TestAssistantHelpAppearsInExistingMenuArticle(t *testing.T) {
	seed, err := Documents()
	if err != nil {
		t.Fatal(err)
	}
	articles := ProjectHelpArticles(withPlatformSeedActor(seed))
	var menu domain.HelpArticle
	for _, article := range articles {
		if article.ID == "portal-user-feature-map" {
			menu = article
		}
	}
	if menu.ID == "" || len(KnownHelpArticleIDs()) != 43 {
		t.Fatal("assistant guidance must reuse the existing menu article")
	}
	for _, marker := range []string{
		"平台启用助手后", "拖拽入口", "关闭面板", "每次提问都是独立请求",
		"不在服务端保存聊天历史", "已发布使用说明", "显式选中任务", "沿用任务详情权限",
		"清除任务选择后", "任务日志默认不发送", "为本次问题勾选同意", "下一问需要重新勾选",
		"并不能保证识别全部敏感信息", "| 自动 |", "| API |", "| 本地 |", "| 仅文档 |",
		"降级为文档检索结果", "不调用模型", "不表示自建 GPU 服务已经启用",
		"不承诺剩余额度或平台月预算硬限制", "不能提交、修改、停止或重启任务",
	} {
		if !strings.Contains(menu.Markdown, marker) {
			t.Errorf("published menu article missing assistant boundary %q", marker)
		}
	}
	if !strings.Contains(strings.Join(menu.Keywords, " "), "助手") {
		t.Fatal("assistant guidance is not discoverable through help search")
	}
	for _, guide := range PublicGuides() {
		if guide.ID == "quickstart" && strings.Contains(guide.Markdown, "页面助手怎么用？") {
			return
		}
	}
	t.Fatal("legacy quickstart entry omits assistant guidance")
}

func TestAssistantHelpSupplementPreservesPublishedEdits(t *testing.T) {
	source := domain.HelpDocument{
		ID: "portal-user-feature-map", Markdown: "团队维护的菜单操作步骤", UpdatedBy: "human-editor", Version: 12,
	}
	article := ProjectHelpArticle(source)
	if !strings.Contains(article.Markdown, source.Markdown) || !strings.Contains(article.Markdown, "页面助手怎么用？") {
		t.Fatal("assistant supplement must preserve existing published menu content")
	}
	if article.Version != source.Version || source.Markdown != "团队维护的菜单操作步骤" {
		t.Fatal("assistant supplement mutated stored content or version")
	}
}
