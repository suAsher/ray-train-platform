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
func ProjectMLflowAccess(document domain.HelpDocument, nativePublicEnabled, dashboardPublicEnabled bool) domain.HelpDocument {
	document = projectMLflowNativeAccess(document, nativePublicEnabled)
	return projectMLflowDashboardAccess(document, dashboardPublicEnabled)
}

func projectMLflowNativeAccess(document domain.HelpDocument, publicEnabled bool) domain.HelpDocument {
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

const mlflowAnonymousWebGuide = `MLflow 网页已开放匿名访问，无需登录、Cookie 或先从平台跳转。可直接打开 [MLflow 网页](https://raytrain.wellspiking.ai/mlflow/)，进入目标 Run 后复制并分享浏览器完整地址，格式为 https://raytrain.wellspiking.ai/mlflow/#/experiments/EXPERIMENT_ID/runs/RUN_ID。接收者须能访问该域名；原有“打开 MLflow”按钮仍可使用，分享时使用跳转完成后不含 access_token 的地址。

该网页面向现有网络可达范围共享全部实验、Run、Artifact 和 Registry，允许读取、创建、修改和删除；操作会影响所有使用这些内容的人。平台任务、个人目录、数据空间和调度接口仍需原有认证。网页不会自动生成训练指标或改变训练接入方式。

程序调用继续使用 https://raytrain.wellspiking.ai/api/v1/mlflow-native，认证方式按本页 API 说明；不要把带 # 的网页地址设为 tracking_uri。`

func projectMLflowDashboardAccess(document domain.HelpDocument, publicEnabled bool) domain.HelpDocument {
	if !publicEnabled || document.UpdatedBy != PlatformSeedActor {
		return document
	}
	markdown := strings.NewReplacer(
		"- MLflow 网页 /mlflow/ 仍需要从已登录的平台打开。MLFLOW_DASHBOARD_AUTH_REQUIRED 表示误用了网页路径，不表示 Token 过期。",
		"- MLflow 网页 /mlflow/ 已开放匿名访问，Run 直链可直接打开和分享；程序调用仍使用原生 API 前缀。",
		"网页仍使用浏览器登录会话，不能用页面跳转地址代替原生 API。",
		"网页也无需登录，程序仍使用原生 API 前缀，不能用带 # 的页面地址代替。",
		"免令牌原生地址返回 401/403 时先核对是否误用了 /mlflow/ 网页路径，再检查当前服务开关和网络代理，不要据此判断令牌过期；",
		"网页和原生 API 的免令牌地址返回 401/403 时，核对入口、当前服务开关和网络代理，不要据此判断令牌过期；",
		"通过浏览器会话查看共享实验、Run、Artifact 和 Registry",
		"直接打开或分享 Run 直链，查看共享实验、Run、Artifact 和 Registry",
		"浏览器票据和原生程序 API 是两种通道，重新从任务详情进入",
		"直接打开 https://raytrain.wellspiking.ai/mlflow/；核对域名连通性、Experiment ID / Run ID 和浏览器错误，无需先从平台跳转",
		"网页路径返回 MLFLOW_DASHBOARD_AUTH_REQUIRED 不表示 PAT 过期",
		"网页已开放匿名访问；若仍返回 MLFLOW_DASHBOARD_AUTH_REQUIRED，请核对入口和部署配置，不表示 PAT 过期",
		"从任务详情或实验中心点击「MLflow 详情 / 打开该 Run」，不要把 API 的 PAT 填入浏览器地址。平台先签发一次性票据，再在 `https://raytrain.wellspiking.ai` 的受保护路径换成 HttpOnly Cookie；地址栏不应保留 `access_token`，中间地址不可分享。",
		"MLflow 网页和 Run 直链无需登录或平台跳转。直接打开 https://raytrain.wellspiking.ai/mlflow/，进入 Run 后分享含 #/experiments/.../runs/... 的完整地址。平台原有打开按钮仍可使用；不要分享含 access_token 的中间跳转地址。直链无法打开时检查域名连通性、Experiment ID / Run ID 和浏览器错误，不要通过重提训练解决。",
	).Replace(document.Markdown)
	switch document.ID {
	case "mlflow", "mlflow-api-with-pat", "mlflow-external-tracking", queueAndToolArticleID:
		if !strings.Contains(markdown, mlflowAnonymousWebGuide) {
			markdown += "\n\n### 直接打开和分享 MLflow 网页\n\n" + mlflowAnonymousWebGuide
		}
	}
	document.Markdown = markdown
	return document
}
