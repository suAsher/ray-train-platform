package helpdocs

import (
	"strings"
	"testing"
)

func TestEvaluationHelpPreservesQuestionsAndExplainsRealExecution(t *testing.T) {
	sources, err := Documents()
	if err != nil {
		t.Fatal(err)
	}
	articles := helpArticleTestByID(ProjectHelpArticles(withPlatformSeedActor(sources)))
	if len(KnownHelpArticleIDs()) != 43 {
		t.Fatalf("public question count got %d want 43", len(KnownHelpArticleIDs()))
	}
	for id, markers := range map[string][]string{
		"model-evaluation-start":   {"评估方案", "READY", "val", "test", "latest", "配额", "权重", "SHA", "全部场地", "不可变", "TEAM", "普通成员", "源码 ZIP", "64 MiB", "evaluation_sdk.py", "内网", "不需要访问 Git", "代码不打进镜像", "预检不会创建", "确认并创建评估"},
		"model-evaluation-results": {"报告", "成功退出", "缺失", "比较", "配置", "零", "审批", "推理", "TenantAdmin"},
	} {
		article, ok := articles[id]
		if !ok {
			t.Errorf("missing evaluation question %s", id)
			continue
		}
		if article.CategoryID != "debug" {
			t.Errorf("unexpected category for %s", id)
		}
		for _, marker := range markers {
			if !strings.Contains(article.Markdown, marker) {
				t.Errorf("%s missing %q", id, marker)
			}
		}
		for _, related := range article.RelatedIDs {
			if _, exists := articles[related]; !exists {
				t.Errorf("%s links to missing %s", id, related)
			}
		}
	}
	data := articles["datasets"].Markdown
	for _, marker := range []string{"训练集", "验证集", "测试集", "0", "发布", "训练数据", "评估数据", "普通成员", "TenantAdmin", "SuperAdmin", "train 样本数大于 0", "val 或 test 样本数大于 0"} {
		if !strings.Contains(data, marker) {
			t.Errorf("dataset guidance missing %q", marker)
		}
	}
}
