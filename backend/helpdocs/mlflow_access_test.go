package helpdocs

import (
	"strings"
	"testing"

	"ray-train-platform-backend/domain"
)

func TestNativeMLflowHelpReflectsActiveAuthenticationMode(t *testing.T) {
	for _, id := range []string{"mlflow-api-with-pat", "mlflow-external-tracking", "mlflow"} {
		article := ProjectHelpArticle(domain.HelpDocument{ID: id, Markdown: "seed", UpdatedBy: PlatformSeedActor})
		public := ProjectMLflowAccess(article.HelpDocument, true)
		for _, marker := range []string{"免令牌", "删除", "https://raytrain.wellspiking.ai/api/v1/mlflow-native"} {
			if !strings.Contains(public.Markdown, marker) {
				t.Errorf("%s public guide missing %q", id, marker)
			}
		}
		for _, oldInstruction := range []string{"Authorization: Bearer ${RAYTRAIN_PAT}", "export MLFLOW_TRACKING_TOKEN=", "两种外部调用都需要有效个人 PAT", "个人 PAT 需要显式"} {
			if strings.Contains(public.Markdown, oldInstruction) {
				t.Errorf("%s retains obsolete native instruction %q", id, oldInstruction)
			}
		}
		private := ProjectMLflowAccess(article.HelpDocument, false)
		if private.Markdown != article.Markdown || !strings.Contains(private.Markdown, "mlflow:full") {
			t.Errorf("%s changed legacy authenticated mode", id)
		}
	}
}

func TestMLflowAccessProjectionPreservesUserEdits(t *testing.T) {
	document := domain.HelpDocument{ID: "mlflow", UpdatedBy: "human", Markdown: mlflowNativeConnectionGuide}
	if got := ProjectMLflowAccess(document, true); got != document {
		t.Fatal("manual document was overwritten")
	}
}

func TestTrainingHelpContainsExecutableLegacyDDPExample(t *testing.T) {
	article := ProjectHelpArticle(domain.HelpDocument{ID: "mlflow-framework-metrics", UpdatedBy: PlatformSeedActor})
	for _, marker := range []string{platformMLflowPython, platformMLflowUsagePython, "普通 ray-ddp / torchrun 不要照搬", "不支持 log_artifact", "RAYTRAIN_CLUSTER_ATTEMPT", "LOCAL_RANK", "初始化失败", "只适用于配套托管"} {
		if !strings.Contains(article.Markdown, marker) {
			t.Errorf("training tutorial missing %q", marker)
		}
	}
}
