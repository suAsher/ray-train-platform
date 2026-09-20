package helpdocs

import (
	"strings"
	"testing"

	"ray-train-platform-backend/domain"
)

func TestNativeMLflowHelpReflectsActiveAuthenticationMode(t *testing.T) {
	for _, id := range []string{"mlflow-api-with-pat", "mlflow-external-tracking", "mlflow"} {
		article := ProjectHelpArticle(domain.HelpDocument{ID: id, Markdown: "seed", UpdatedBy: PlatformSeedActor})
		public := ProjectMLflowAccess(article.HelpDocument, true, false)
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
		private := ProjectMLflowAccess(article.HelpDocument, false, false)
		if private.Markdown != article.Markdown || !strings.Contains(private.Markdown, "mlflow:full") {
			t.Errorf("%s changed legacy authenticated mode", id)
		}
	}
}

func TestMLflowAccessProjectionPreservesUserEdits(t *testing.T) {
	document := domain.HelpDocument{ID: "mlflow", UpdatedBy: "human", Markdown: mlflowNativeConnectionGuide}
	if got := ProjectMLflowAccess(document, true, true); got != document {
		t.Fatal("manual document was overwritten")
	}
}

func TestMLflowWebHelpReflectsIndependentAuthenticationModes(t *testing.T) {
	for _, nativePublic := range []bool{false, true} {
		for _, id := range []string{"mlflow", "mlflow-api-with-pat", "mlflow-external-tracking"} {
			article := ProjectHelpArticle(domain.HelpDocument{ID: id, UpdatedBy: PlatformSeedActor})
			private := ProjectMLflowAccess(article.HelpDocument, nativePublic, false)
			public := ProjectMLflowAccess(article.HelpDocument, nativePublic, true)
			for _, marker := range []string{"无需登录", "https://raytrain.wellspiking.ai/mlflow/", "#/experiments/", "分享", "原有“打开 MLflow”按钮仍可使用", "读取、创建、修改和删除", "平台任务、个人目录、数据空间和调度接口仍需原有认证"} {
				if !strings.Contains(public.Markdown, marker) {
					t.Errorf("nativePublic=%v %s missing public web guidance %q", nativePublic, id, marker)
				}
			}
			for _, obsolete := range []string{"网页仍使用浏览器登录会话", "仍需要从已登录的平台打开", "通过浏览器会话查看共享实验"} {
				if strings.Contains(public.Markdown, obsolete) {
					t.Errorf("nativePublic=%v %s retains %q", nativePublic, id, obsolete)
				}
			}
			if strings.Contains(private.Markdown, mlflowAnonymousWebGuide) {
				t.Errorf("%s advertises anonymous webpage in authenticated mode", id)
			}
			if !nativePublic && !strings.Contains(public.Markdown, "mlflow:full") {
				t.Errorf("%s public webpage removed native API authentication requirement", id)
			}
		}
	}
}

func TestMLflowPublicWebTroubleshootingPreservesWorkspaceAuthentication(t *testing.T) {
	documents, err := Documents()
	if err != nil {
		t.Fatal(err)
	}
	for _, document := range documents {
		if document.ID != queueAndToolArticleID {
			continue
		}
		document.UpdatedBy = PlatformSeedActor
		article := ProjectHelpArticle(document)
		public := ProjectMLflowAccess(article.HelpDocument, true, true)
		if strings.Contains(public.Markdown, "平台先签发一次性票据，再在") || !strings.Contains(public.Markdown, "Run 直链") {
			t.Fatal("browser troubleshooting retained mandatory MLflow ticket flow")
		}
		for _, marker := range []string{"JupyterLab / VS Code 打不开", "允许新标签页、WebSocket 和安全 Cookie", "票据过期、工作区停止或账号停用"} {
			if !strings.Contains(public.Markdown, marker) {
				t.Errorf("workspace protection guidance lost: %q", marker)
			}
		}
		private := ProjectMLflowAccess(article.HelpDocument, true, false)
		if !strings.Contains(private.Markdown, "平台先签发一次性票据，再在") {
			t.Fatal("authenticated webpage troubleshooting was changed")
		}
		return
	}
	t.Fatal("browser troubleshooting seed not found")
}

func TestTrainingHelpContainsExecutableLegacyDDPExample(t *testing.T) {
	article := ProjectHelpArticle(domain.HelpDocument{ID: "mlflow-framework-metrics", UpdatedBy: PlatformSeedActor})
	for _, marker := range []string{platformMLflowPython, platformMLflowUsagePython, "普通 ray-ddp / torchrun 不要照搬", "不支持 log_artifact", "RAYTRAIN_CLUSTER_ATTEMPT", "LOCAL_RANK", "初始化失败", "只适用于配套托管"} {
		if !strings.Contains(article.Markdown, marker) {
			t.Errorf("training tutorial missing %q", marker)
		}
	}
}
