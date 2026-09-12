package helpdocs

import (
	"strings"
	"testing"

	"ray-train-platform-backend/domain"
)

func TestMLflowExternalArticlesExplainSDKAndRESTConnectionBoundaries(t *testing.T) {
	for _, id := range []string{"mlflow-api-with-pat", "mlflow-external-tracking"} {
		article := ProjectHelpArticle(domain.HelpDocument{ID: id, Markdown: "seed", UpdatedBy: PlatformSeedActor})
		for _, marker := range []string{
			"https://raytrain.wellspiking.ai/api/v1/mlflow-native",
			"tracking_uri",
			"/api/2.0/mlflow/...",
			"SDK 会自行拼接",
			"mlflow:full",
			"MLFLOW_TRACKING_TOKEN",
			"3.14.0",
			"平台内训练",
			"不要覆盖",
			"普通成员可以自行创建",
			"不需要管理员代建",
			"账户与安全",
			"绑定本人和创建时的当前团队",
			"含修改和删除",
			"集成令牌不能代替",
		} {
			if !strings.Contains(article.Markdown, marker) {
				t.Errorf("article %s missing connection boundary %q", id, marker)
			}
		}
	}
}

func TestMLflowTrainingArticlePreservesInjectedConnectionAndRealProvenance(t *testing.T) {
	article := ProjectHelpArticle(domain.HelpDocument{ID: "mlflow-framework-metrics", Markdown: "seed", UpdatedBy: PlatformSeedActor})
	for _, marker := range []string{
		"MLFLOW_TRACKING_URI",
		"不会注入个人 PAT",
		"不要覆盖",
		"global rank 0",
		"mlflow.log_params",
		"mlflow.log_metric",
		"code_commit",
		"dataset_version_id",
		"platform.dataset_version_id",
		"未知 / 未登记",
		"用户补充",
		"不会自动同步",
		"训练数据来源",
		"独立评估",
		"尚未上线",
		"Job ID 与 MLflow run_id 不要求相等",
	} {
		if !strings.Contains(article.Markdown, marker) {
			t.Errorf("training article missing workflow boundary %q", marker)
		}
	}
	if strings.Contains(article.Markdown, "export MLFLOW_TRACKING_TOKEN=") {
		t.Fatal("platform training article instructs copying an external personal PAT")
	}
}
