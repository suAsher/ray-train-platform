package helpdocs

import (
	"strings"

	"ray-train-platform-backend/domain"
)

const mlflowAnonymousConnectionGuide = `当前原生 MLflow API 已开放免令牌调用：在现有网络可达范围内，任何调用者均可读取、创建、修改和删除全部共享实验、Run、Artifact 和 Model Registry。无需个人 PAT 或 Authorization 请求头；没有按调用者的权限撤销和身份归属保证。请勿向共享实验记录密钥或不应共享的数据。

当前验证的客户端为 mlflow==3.14.0。先在本地电脑或调用服务的环境安装客户端，不要在无外网的训练节点临时下载安装。

- Python SDK 的 tracking_uri 只填 https://raytrain.wellspiking.ai/api/v1/mlflow-native，SDK 会自行拼接 /api/2.0/mlflow/...；不要把 REST 后缀加到 tracking_uri。
- HTTP 请求在此前缀后追加原生 REST 路径。文件读写使用 SDK 的 log_artifact / download_artifacts，避免猜测存储路由。
- 平台内训练沿用已注入 MLFLOW_TRACKING_URI，不要覆盖为外部原生入口；无需个人 PAT，保留平台 Job 来源关联。
- MLflow 网页 /mlflow/ 仍需要从已登录的平台打开。MLFLOW_DASHBOARD_AUTH_REQUIRED 表示误用了网页路径，不表示 Token 过期。
- CLI、平台任务、个人目录、数据空间和调度接口仍按原认证和授权处理，不能使用免令牌 MLflow 入口操作这些资源。`

// ProjectMLflowAccess adapts only platform-owned, published help. The stored
// document and manual edits remain untouched when configuration changes.
func ProjectMLflowAccess(document domain.HelpDocument, publicEnabled bool) domain.HelpDocument {
	if !publicEnabled || document.UpdatedBy != PlatformSeedActor {
		return document
	}
	markdown := strings.ReplaceAll(document.Markdown, mlflowNativeConnectionGuide, mlflowAnonymousConnectionGuide)
	markdown = strings.ReplaceAll(markdown,
		"401 先查 PAT 是否有效、过期或撤销；403 查 PAT 是否包含 mlflow:full；",
		"免令牌原生地址返回 401/403 时先核对是否误用了 /mlflow/ 网页路径，再检查当前服务开关和网络代理，不要据此判断令牌过期；")
	markdown = strings.ReplaceAll(markdown,
		"个人 PAT 需要显式 `mlflow:full`。原生 SDK 使用原生 Experiment ID / Run ID，可对共享实验、Run、Artifact 和 Registry 做读写修改删除。",
		"当前原生 MLflow API 免令牌调用，无需个人 PAT；在现有网络可达范围内可对全部共享实验、Run、Artifact 和 Registry 做读取、创建、修改和删除。原生 SDK 使用原生 Experiment ID / Run ID。")
	markdown = strings.ReplaceAll(markdown,
		"程序接入仍使用上面的 Tracking URI 和 PAT，不使用浏览器 Cookie 或页面跳转地址。",
		"程序接入使用上面的 Tracking URI，无需令牌；网页仍使用浏览器登录会话，不能用页面跳转地址代替原生 API。")
	markdown = strings.ReplaceAll(markdown,
		"外部调用的个人 PAT 需要显式 `mlflow:full`，旧 token 不会自动升级。该 scope 与共享 MLflow 页面范围一致，包含实验、Run、Metric、Param、Tag、Artifact、删除和 Model Registry 操作。",
		"当前外部原生 API 免令牌调用，可读取、创建、修改和删除全部共享实验、Run、Metric、Param、Tag、Artifact 和 Model Registry。")
	// These snippets occur only in native MLflow guides, not CLI examples.
	markdown = strings.ReplaceAll(markdown, "export MLFLOW_TRACKING_TOKEN=\"$RAYTRAIN_PAT\"", "unset MLFLOW_TRACKING_TOKEN MLFLOW_TRACKING_USERNAME MLFLOW_TRACKING_PASSWORD")
	if document.ID == "mlflow" || document.ID == "mlflow-api-with-pat" || document.ID == "mlflow-external-tracking" {
		markdown = strings.ReplaceAll(markdown, "  -H \"Authorization: Bearer ${RAYTRAIN_PAT}\" \\\n", "")
	}
	document.Markdown = markdown
	return document
}
