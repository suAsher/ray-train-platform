package helpdocs

import (
	"strings"
	"unicode"

	"ray-train-platform-backend/domain"
)

const PlatformSeedActor = "platform-seed"

type HelpArticleMeta struct {
	Title         string
	CategoryID    string
	Category      string
	SortOrder     int
	Summary       string
	Keywords      []string
	RelatedIDs    []string
	LegacyTopicID string
}

var helpArticleMetaByID = map[string]HelpArticleMeta{
	"quickstart": {Title: "第一次如何跑通一条训练任务？", CategoryID: "start", Category: "开始使用与账号", SortOrder: 10, Summary: "从登录、确认配额到提交第一条 1 卡训练并查看结果。", Keywords: []string{"新手", "第一条任务", "GPU 配额", "训练详情"}, RelatedIDs: []string{"cli-onboarding-v2", "submit", "observability"}, LegacyTopicID: "quickstart"},
	"cli-onboarding-v2": {Title: "CLI 如何安装、登录和升级？", CategoryID: "start", Category: "开始使用与账号", SortOrder: 20, Summary: "安装 spk-rayjob、配置 PATH、用 PAT 登录并检查版本。", Keywords: []string{"CLI", "spk-rayjob", "PATH", "PAT", "升级"}, RelatedIDs: []string{"access", "command-recipes", "quickstart"}, LegacyTopicID: "quickstart"},
	"quota": {Title: "我的 GPU 配额和已用额度在哪里看？", CategoryID: "start", Category: "开始使用与账号", SortOrder: 30, Summary: "在“我的训练任务”顶部查看限额、已用和剩余；训练和交互调试都会占用额度。", Keywords: []string{"GPU", "配额", "已用", "剩余", "排队"}, RelatedIDs: []string{"scheduling-topology", "errors", "debug"}, LegacyTopicID: "quickstart"},
	"access": {Title: "PAT 和私有 Git 仓库凭据怎么配置？", CategoryID: "start", Category: "开始使用与账号", SortOrder: 40, Summary: "区分平台 PAT、Git 凭据和网页会话，配置可提交的访问凭据。", Keywords: []string{"PAT", "Git", "私有仓库", "令牌", "凭据"}, RelatedIDs: []string{"cli-onboarding-v2", "unified-login-and-roles", "mlflow-api-with-pat"}, LegacyTopicID: "account-api"},
	"portal-user-feature-map": {Title: "各个菜单分别能做什么？", CategoryID: "start", Category: "开始使用与账号", SortOrder: 50, Summary: "按训练、数据、调试、实验和账号目标找到 Portal 菜单入口。", Keywords: []string{"菜单", "Portal", "入口", "使用说明"}, RelatedIDs: []string{"quickstart", "observability", "datasets"}, LegacyTopicID: "quickstart"},
	"unified-login-and-roles": {Title: "登录后如何确认团队与权限？", CategoryID: "start", Category: "开始使用与账号", SortOrder: 60, Summary: "确认当前登录身份、团队、角色和权限边界。", Keywords: []string{"登录", "团队", "角色", "权限", "SSO"}, RelatedIDs: []string{"access", "quota", "portal-user-feature-map"}, LegacyTopicID: "quickstart"},

	"code": {Title: "如何使用 Git、ZIP 或工作区里的训练代码？", CategoryID: "data", Category: "代码、环境与数据", SortOrder: 110, Summary: "选择训练代码来源，理解 Git commit、ZIP 和 working_dir 的固定方式。", Keywords: []string{"代码", "Git", "ZIP", "working_dir", "commit"}, RelatedIDs: []string{"custom-environment", "submit", "command-recipes"}, LegacyTopicID: "data"},
	"storage": {Title: "输入数据和训练输出分别放在哪里？", CategoryID: "data", Category: "代码、环境与数据", SortOrder: 120, Summary: "区分个人、团队、公共、IDC、版本化数据和训练结果目录。", Keywords: []string{"数据空间", "输出", "checkpoint", "PLATFORM_OUTPUT_PATH"}, RelatedIDs: []string{"uploads", "datasets", "artifacts"}, LegacyTopicID: "data"},
	"uploads": {Title: "大文件怎么上传，中断或报 413 怎么办？", CategoryID: "data", Category: "代码、环境与数据", SortOrder: 130, Summary: "使用分片上传、续传和 hash 校验处理大文件。", Keywords: []string{"上传", "413", "分片", "续传", "hash"}, RelatedIDs: []string{"storage", "errors", "datasets"}, LegacyTopicID: "data"},
	"datasets": {Title: "如何选择固定的数据集版本？", CategoryID: "data", Category: "代码、环境与数据", SortOrder: 140, Summary: "选择 READY 数据集版本和场地，保证训练输入可复现。", Keywords: []string{"版本化数据集", "READY", "场地", "site", "复现"}, RelatedIDs: []string{"data-mode", "streaming", "streaming-validation"}, LegacyTopicID: "data"},
	"custom-environment": {Title: "缺少依赖，如何准备自己的训练镜像？", CategoryID: "data", Category: "代码、环境与数据", SortOrder: 150, Summary: "判断缺少依赖时该复用基础镜像还是登记自定义训练镜像。", Keywords: []string{"镜像", "依赖", "CUDA", "PyTorch", "登记"}, RelatedIDs: []string{"code", "submit", "errors"}, LegacyTopicID: "data"},
	"data-mode": {Title: "mount、cache、Ray Data 和 streaming 怎么选？", CategoryID: "data", Category: "代码、环境与数据", SortOrder: 160, Summary: "比较 mount、cache、ray-data-stage、Ray Data 和 streaming 的适用场景。", Keywords: []string{"mount", "cache", "Ray Data", "streaming", "数据模式"}, RelatedIDs: []string{"cache", "ray-data", "streaming"}, LegacyTopicID: "data"},
	"cache": {Title: "什么时候适合使用 NVMe 缓存？", CategoryID: "data", Category: "代码、环境与数据", SortOrder: 170, Summary: "判断数据是否适合缓存，配置 NVMe 预热并验证效果。", Keywords: []string{"NVMe", "缓存", "预热", "吞吐"}, RelatedIDs: []string{"data-mode", "diagnose", "streaming"}, LegacyTopicID: "data"},

	"preflight": {Title: "提交训练前需要检查什么？", CategoryID: "training", Category: "提交、分布式与续训", SortOrder: 210, Summary: "提交前检查镜像、代码、数据、输出目录、资源和权限。", Keywords: []string{"提交前", "检查", "镜像", "资源", "权限"}, RelatedIDs: []string{"submit", "quota", "errors"}, LegacyTopicID: "training-guide"},
	"submit": {Title: "如何提交单卡、单机多卡和多机训练？", CategoryID: "training", Category: "提交、分布式与续训", SortOrder: 220, Summary: "用 spk-rayjob 提交训练，理解 Worker、GPU、镜像、入口和数据参数。", Keywords: []string{"提交", "单卡", "多机", "Worker", "ray-train"}, RelatedIDs: []string{"command-recipes", "preflight", "scheduling-topology"}, LegacyTopicID: "training-guide"},
	"resume": {Title: "训练中断后如何从 checkpoint 续训？", CategoryID: "training", Category: "提交、分布式与续训", SortOrder: 230, Summary: "区分托管 Ray Train 续训和普通脚本自读 checkpoint。", Keywords: []string{"续训", "checkpoint", "resume", "max-failures"}, RelatedIDs: []string{"artifacts", "submit", "streaming-validation"}, LegacyTopicID: "training-guide"},
	"scheduling-topology": {Title: "任务一直排队，为什么还没开始？", CategoryID: "training", Category: "提交、分布式与续训", SortOrder: 240, Summary: "从配额、队列准入和节点整体可放置性定位排队。", Keywords: []string{"排队", "Pending", "Kueue", "拓扑", "Worker"}, RelatedIDs: []string{"quota", "errors", "portal-browser-tools-and-queue"}, LegacyTopicID: "training-guide"},
	"command-recipes": {Title: "CLI 与原生 Ray 的完整命令怎么写？", CategoryID: "training", Category: "提交、分布式与续训", SortOrder: 250, Summary: "复制 spk-rayjob 与原生 Ray Jobs 的完整 Bash、zsh 和 PowerShell 模板。", Keywords: []string{"命令", "Ray Jobs", "PowerShell", "RAY_JOB_HEADERS", "metadata"}, RelatedIDs: []string{"cli-onboarding-v2", "submit", "streaming"}, LegacyTopicID: "training-guide"},
	"ray-data": {Title: "训练代码如何读取 Ray Data 分片？", CategoryID: "training", Category: "提交、分布式与续训", SortOrder: 260, Summary: "在训练代码里读取 Ray Data shard，避免重复切分样本。", Keywords: []string{"Ray Data", "get_dataset_shard", "分片", "DataIterator"}, RelatedIDs: []string{"data-mode", "streaming", "scaling"}, LegacyTopicID: "training-guide"},
	"streaming": {Title: "如何使用固定版本数据做 streaming 训练？", CategoryID: "training", Category: "提交、分布式与续训", SortOrder: 270, Summary: "用固定数据集版本、场地和缓存策略提交 streaming 训练。", Keywords: []string{"streaming", "数据集版本", "场地", "Parquet", "NVMe"}, RelatedIDs: []string{"datasets", "ray-data", "streaming-validation"}, LegacyTopicID: "training-guide"},
	"scaling": {Title: "增加 GPU 后，如何判断训练是否变快？", CategoryID: "training", Category: "提交、分布式与续训", SortOrder: 280, Summary: "用样本数、吞吐、GPU 利用率和瓶颈定位评估扩卡效果。", Keywords: []string{"扩卡", "吞吐", "GPU 利用率", "瓶颈"}, RelatedIDs: []string{"diagnose", "ray-data", "scheduling-topology"}, LegacyTopicID: "training-guide"},
	"streaming-validation": {Title: "如何验证 streaming 训练的数据与结果？", CategoryID: "training", Category: "提交、分布式与续训", SortOrder: 290, Summary: "核对固定版本、样本范围、日志、结果和失败边界。", Keywords: []string{"验收", "streaming", "样本数", "结果", "场地"}, RelatedIDs: []string{"streaming", "datasets", "artifacts"}, LegacyTopicID: "training-guide"},

	"debug": {Title: "如何使用 JupyterLab 和 VS Code？", CategoryID: "debug", Category: "调试与训练结果", SortOrder: 310, Summary: "启动交互式调试环境，检查依赖、样本读取和入口脚本。", Keywords: []string{"JupyterLab", "VS Code", "调试", "票据", "配额"}, RelatedIDs: []string{"custom-environment", "worker-connect-and-scheduling-boundary", "portal-browser-tools-and-queue"}, LegacyTopicID: "debug"},
	"worker-connect-and-scheduling-boundary": {Title: "如何连接自己的训练 Worker？", CategoryID: "debug", Category: "调试与训练结果", SortOrder: 320, Summary: "用 spk-rayjob connect 连接运行中的 Worker，并理解调试与排队边界。", Keywords: []string{"Worker", "connect", "shell", "运行中任务"}, RelatedIDs: []string{"debug", "scheduling-topology", "errors"}, LegacyTopicID: "debug"},
	"observability": {Title: "训练状态、日志和指标在哪里看？", CategoryID: "debug", Category: "调试与训练结果", SortOrder: 330, Summary: "在任务详情查看状态、日志、GPU 曲线、训练指标和 MLflow 入口。", Keywords: []string{"日志", "指标", "状态", "GPU 曲线", "MLflow"}, RelatedIDs: []string{"mlflow", "telemetry-boundary", "artifacts"}, LegacyTopicID: "mlflow"},
	"artifacts": {Title: "如何下载训练结果与模型权重？", CategoryID: "debug", Category: "调试与训练结果", SortOrder: 340, Summary: "从训练结果目录取回模型权重、报告、checkpoint 和日志。", Keywords: []string{"产物", "模型权重", "下载", "checkpoint", "结果目录"}, RelatedIDs: []string{"storage", "resume", "observability"}, LegacyTopicID: "training-guide"},

	"mlflow": {Title: "如何查看实验、比较 Run，Job ID 和 Run ID 怎么对应？", CategoryID: "mlflow", Category: "MLflow 与 API", SortOrder: 410, Summary: "Job ID 标识平台任务，Run ID 标识实验记录；一个任务可关联多个 Run，两者不要求相等。", Keywords: []string{"MLflow", "训练记录", "Run ID", "Job ID", "run_id", "job_id", "打开 MLflow"}, RelatedIDs: []string{"observability", "mlflow-framework-metrics", "mlflow-api-with-pat"}, LegacyTopicID: "mlflow"},
	"mlflow-framework-metrics": {Title: "如何向 MLflow 记录训练参数和指标？", CategoryID: "mlflow", Category: "MLflow 与 API", SortOrder: 420, Summary: "在 MMCV、普通 PyTorch 和自定义训练中记录参数、指标和文件。", Keywords: []string{"MLflow", "指标", "参数", "MMCV", "PyTorch"}, RelatedIDs: []string{"mlflow", "telemetry-boundary", "mlflow-external-tracking"}, LegacyTopicID: "mlflow"},
	"mlflow-api-with-pat": {Title: "如何调用 MLflow API 查询实验与 Run？", CategoryID: "mlflow", Category: "MLflow 与 API", SortOrder: 430, Summary: "使用 PAT、原生 MLflow SDK 和 HTTP 查询实验、Run、指标历史与文件。", Keywords: []string{"MLflow API", "PAT", "search_experiments", "search_runs", "page_token"}, RelatedIDs: []string{"mlflow", "mlflow-external-tracking", "access"}, LegacyTopicID: "mlflow"},
	"mlflow-external-tracking": {Title: "如何用自己的程序向 MLflow 写入数据和文件？", CategoryID: "mlflow", Category: "MLflow 与 API", SortOrder: 440, Summary: "在自己的程序中用原生 MLflow SDK 或 HTTP 记录实验、Run、指标和 Artifact。", Keywords: []string{"MLflow", "log_metric", "log_artifact", "runs/log-batch", "Artifact"}, RelatedIDs: []string{"mlflow-api-with-pat", "mlflow-framework-metrics", "access"}, LegacyTopicID: "mlflow"},

	"errors": {Title: "遇到 401、403、Pending 等错误先检查什么？", CategoryID: "troubleshooting", Category: "常见故障", SortOrder: 510, Summary: "按错误码和训练状态快速定位登录、权限、配额、数据和运行时问题。", Keywords: []string{"401", "403", "Pending", "413", "错误"}, RelatedIDs: []string{"access", "uploads", "scheduling-topology"}, LegacyTopicID: "troubleshooting"},
	"diagnose": {Title: "训练很慢，如何定位瓶颈？", CategoryID: "troubleshooting", Category: "常见故障", SortOrder: 520, Summary: "从数据读取、缓存、GPU 利用率、Worker 拓扑和日志定位性能瓶颈。", Keywords: []string{"训练慢", "瓶颈", "GPU", "数据读取", "吞吐"}, RelatedIDs: []string{"scaling", "cache", "observability"}, LegacyTopicID: "troubleshooting"},
	"portal-browser-tools-and-queue": {Title: "JupyterLab、VS Code 或 MLflow 页面打不开怎么办？", CategoryID: "troubleshooting", Category: "常见故障", SortOrder: 530, Summary: "区分浏览器票据过期、代理路由、任务结束和排队状态。", Keywords: []string{"JupyterLab", "VS Code", "MLflow", "打不开", "排队"}, RelatedIDs: []string{"debug", "scheduling-topology", "errors"}, LegacyTopicID: "troubleshooting"},
	"telemetry-boundary": {Title: "日志里有 loss，为什么页面没有曲线？", CategoryID: "troubleshooting", Category: "常见故障", SortOrder: 540, Summary: "打印 loss 日志不会自动生成曲线；训练代码需要上报 MLflow metric。", Keywords: []string{"loss", "曲线", "metric", "日志", "MLflow"}, RelatedIDs: []string{"mlflow-framework-metrics", "observability", "mlflow"}, LegacyTopicID: "troubleshooting"},
}

type publicGuideSectionTarget struct {
	guideID   string
	heading   string
	articleID string
}

var publicGuideSectionTargets = []publicGuideSectionTarget{
	{guideID: "quickstart", heading: "从哪里开始", articleID: "portal-user-feature-map"},
	{guideID: "quickstart", heading: "安装 CLI", articleID: "cli-onboarding-v2"},
	{guideID: "quickstart", heading: "登录和检查", articleID: "cli-onboarding-v2"},
	{guideID: "quickstart", heading: "第一条任务", articleID: "quickstart"},
	{guideID: "account-api", heading: "身份和令牌", articleID: "access"},
	{guideID: "account-api", heading: "常用地址", articleID: "access"},
	{guideID: "account-api", heading: "ID 边界", articleID: "mlflow"},
	{guideID: "data", heading: "代码和镜像", articleID: "code"},
	{guideID: "data", heading: "数据空间", articleID: "storage"},
	{guideID: "data", heading: "数据模式", articleID: "data-mode"},
	{guideID: "data", heading: "版本和场地", articleID: "datasets"},
	{guideID: "training-guide", heading: "提交前自检", articleID: "preflight"},
	{guideID: "training-guide", heading: "单卡、多机和 streaming 模板", articleID: "submit"},
	{guideID: "training-guide", heading: "续训", articleID: "resume"},
	{guideID: "training-guide", heading: "训练代码要点", articleID: "submit"},
	{guideID: "debug", heading: "交互式调试", articleID: "debug"},
	{guideID: "debug", heading: "连接运行中的 Worker", articleID: "worker-connect-and-scheduling-boundary"},
	{guideID: "mlflow", heading: "页面和记录关系", articleID: "mlflow"},
	{guideID: "mlflow", heading: "原生 MLflow SDK", articleID: "mlflow-api-with-pat"},
	{guideID: "mlflow", heading: "打开 MLflow 页面", articleID: "mlflow"},
	{guideID: "troubleshooting", heading: "排障顺序", articleID: "errors"},
	{guideID: "troubleshooting", heading: "常见现象", articleID: "errors"},
	{guideID: "troubleshooting", heading: "性能定位", articleID: "diagnose"},
	{guideID: "troubleshooting", heading: "推荐入口", articleID: "portal-browser-tools-and-queue"},
}

var articlePublicSupplements = buildArticlePublicSupplements()

var extraLegacyTopicsByArticleID = map[string][]string{
	"custom-environment": {"training-guide"},
}

func HelpArticleMetaForID(id string) (HelpArticleMeta, bool) {
	meta, ok := helpArticleMetaByID[id]
	if !ok {
		return HelpArticleMeta{}, false
	}
	meta.Keywords = append([]string(nil), meta.Keywords...)
	meta.RelatedIDs = append([]string(nil), meta.RelatedIDs...)
	return meta, true
}

func KnownHelpArticleIDs() map[string]bool {
	out := make(map[string]bool, len(helpArticleMetaByID))
	for id := range helpArticleMetaByID {
		out[id] = true
	}
	return out
}

func PublicGuideSectionTargets() []struct{ GuideID, Heading, ArticleID string } {
	out := make([]struct{ GuideID, Heading, ArticleID string }, 0, len(publicGuideSectionTargets))
	for _, target := range publicGuideSectionTargets {
		out = append(out, struct{ GuideID, Heading, ArticleID string }{GuideID: target.guideID, Heading: target.heading, ArticleID: target.articleID})
	}
	return out
}

func ProjectHelpArticle(document domain.HelpDocument) domain.HelpArticle {
	meta, known := HelpArticleMetaForID(document.ID)
	projected := document
	if known && document.UpdatedBy == PlatformSeedActor {
		projected = PublicSectionForSeedDocument(projected)
	}
	if supplement := articleSupplementForDocument(projected.ID); supplement != "" {
		projected.Markdown += "\n\n" + supplement
	}
	article := domain.HelpArticle{
		HelpDocument: projected,
		CategoryID:   categoryIDForHelpDocument(projected),
		Summary:      summaryForHelpDocument(projected),
		Keywords:     []string{},
		RelatedIDs:   []string{},
	}
	if !known {
		return article
	}
	article.Title = meta.Title
	article.Category = meta.Category
	article.SortOrder = meta.SortOrder
	article.CategoryID = meta.CategoryID
	article.Summary = meta.Summary
	article.Keywords = append([]string(nil), meta.Keywords...)
	article.RelatedIDs = append([]string(nil), meta.RelatedIDs...)
	sectionID := SectionAnchorID(projected.Title)
	article.LegacyAnchors = make([]domain.HelpArticleLegacyAnchor, 0, 1+len(extraLegacyTopicsByArticleID[projected.ID]))
	seenTopics := map[string]bool{}
	for _, topicID := range append([]string{meta.LegacyTopicID}, extraLegacyTopicsByArticleID[projected.ID]...) {
		if topicID == "" || seenTopics[topicID] {
			continue
		}
		seenTopics[topicID] = true
		article.LegacyAnchors = append(article.LegacyAnchors, domain.HelpArticleLegacyAnchor{
			TopicID:          topicID,
			SectionID:        sectionID,
			ArticleSectionID: "",
		})
	}
	return article
}

func articleSupplementForDocument(id string) string {
	if supplement, ok := articlePublicSupplements[id]; ok {
		return supplement
	}
	return ""
}

func buildArticlePublicSupplements() map[string]string {
	guideSections := map[string]map[string]string{}
	for _, guide := range PublicGuides() {
		guideSections[guide.ID] = markdownSectionsByHeading(guide.Markdown)
	}
	sectionsByArticle := map[string][]string{}
	for _, target := range publicGuideSectionTargets {
		if section := guideSections[target.guideID][target.heading]; section != "" {
			sectionsByArticle[target.articleID] = append(sectionsByArticle[target.articleID], section)
		}
	}
	out := make(map[string]string, len(sectionsByArticle))
	for articleID, sections := range sectionsByArticle {
		out[articleID] = strings.Join(sections, "\n\n")
	}
	return out
}

func markdownSectionsByHeading(markdown string) map[string]string {
	sections := map[string]string{}
	parts := strings.Split(markdown, "\n### ")
	for index, part := range parts {
		if strings.TrimSpace(part) == "" {
			continue
		}
		section := part
		if index > 0 {
			section = "### " + part
		}
		if strings.HasPrefix(section, "### ") {
			lineEnd := strings.Index(section, "\n")
			if lineEnd < 0 {
				lineEnd = len(section)
			}
			heading := strings.TrimSpace(strings.TrimPrefix(section[:lineEnd], "### "))
			sections[heading] = section
		}
	}
	return sections
}

func categoryIDForHelpDocument(document domain.HelpDocument) string {
	category := document.Category
	switch {
	case strings.Contains(category, "代码") || strings.Contains(category, "数据"):
		return "data"
	case strings.Contains(category, "提交") || strings.Contains(category, "运行"):
		return "training"
	case strings.Contains(category, "结果") || strings.Contains(category, "MLflow"):
		return "mlflow"
	case strings.Contains(category, "故障") || strings.Contains(category, "排查"):
		return "troubleshooting"
	case strings.Contains(category, "调试"):
		return "debug"
	default:
		return "start"
	}
}

func summaryForHelpDocument(document domain.HelpDocument) string {
	text := strings.TrimSpace(document.Markdown)
	text = strings.TrimLeft(text, "# ")
	if index := strings.Index(text, "\n\n"); index >= 0 {
		text = text[:index]
	}
	text = strings.ReplaceAll(text, "\n", " ")
	if len([]rune(text)) > 80 {
		runes := []rune(text)
		text = string(runes[:80]) + "…"
	}
	return text
}

func SectionAnchorID(title string) string {
	var builder strings.Builder
	lastDash := false
	for _, r := range strings.TrimSpace(title) {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			builder.WriteRune(unicode.ToLower(r))
			lastDash = false
			continue
		}
		if !lastDash && builder.Len() > 0 {
			builder.WriteByte('-')
			lastDash = true
		}
	}
	id := strings.Trim(builder.String(), "-")
	if id == "" {
		id = "top"
	}
	return "section-" + id
}
