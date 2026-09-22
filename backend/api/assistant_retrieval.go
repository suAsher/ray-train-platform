package api

import (
	"context"
	"sort"
	"strings"
	"unicode"

	"ray-train-platform-backend/assistant"
	"ray-train-platform-backend/domain"
	"ray-train-platform-backend/helpdocs"
)

const assistantExcerptLimit = 1600

// These are concepts, not arbitrary Chinese character pairs. Generic words
// such as "如何", "我的" and "怎么办" never establish relevance.
var assistantConcepts = [][]string{
	{"cli", "spk-rayjob", "命令行"}, {"登录", "sso", "账号", "账户", "角色", "权限"},
	{"pat", "令牌", "凭据"}, {"git", "仓库", "commit"}, {"zip", "源码包", "代码包", "working_dir"},
	{"配额", "额度", "限额"}, {"日志", "log", "logs"}, {"曲线", "loss", "metric", "指标"},
	{"mlflow", "run id", "run_id", "实验"}, {"上传", "分片", "续传", "413"},
	{"数据空间", "输入数据", "输出", "产物", "目录", "路径", "platform_output_path", "platform_input_path"},
	{"数据集", "dataset", "场地"}, {"镜像", "依赖", "cuda", "pytorch", "moduleNotFoundError"},
	{"缓存", "nvme", "预热"}, {"streaming", "流式"}, {"ray data", "ray-data-stage", "数据模式"},
	{"续训", "恢复训练", "断点", "checkpoint"}, {"分布式", "多机", "多卡", "torchrun", "ddp"},
	{"排队", "pending", "suspended", "调度", "碎片", "拓扑"}, {"jupyter", "vscode", "vs code", "交互调试", "工作区"},
	{"助手", "机器人", "仅文档"}, {"菜单", "功能入口", "哪些功能"},
	{"推理", "serving", "调用模型"}, {"模型注册", "共享模型", "registry"},
	{"评估", "发布模型", "模型版本"}, {"401", "403", "429", "oom", "out of memory", "显存不足"},
	{"训练慢", "瓶颈", "吞吐", "利用率"},
}

var assistantActions = []string{"安装", "升级", "登录", "提交", "取消", "停止", "查看", "查询", "配置", "下载", "失败", "没有", "打不开", "接入", "记录"}
var assistantStopwords = []string{"请问", "请帮我", "帮我", "我想知道", "我想", "一下", "应该", "如何", "怎么", "怎样", "为什么", "哪里", "在哪", "什么", "这个", "那个", "这些", "那些", "我的", "我们", "你们", "是否", "能否", "可以", "需要", "进行", "使用", "操作", "怎么办", "请", "吗", "呢", "的", "了", "是"}

type assistantSearchQuery struct {
	question  string
	preferred []string
	concepts  [][]string
	terms     []string
	selectors []string
}

func assistantContains(text, term string) bool {
	text, term = strings.ToLower(text), strings.ToLower(term)
	for start := 0; start < len(text); {
		i := strings.Index(text[start:], term)
		if i < 0 {
			return false
		}
		i += start
		ascii := len(term) > 0 && term[0] < 128 && term[len(term)-1] < 128
		word := func(b byte) bool { return b >= 'a' && b <= 'z' || b >= '0' && b <= '9' || b == '_' }
		if !ascii || (i == 0 || !word(text[i-1])) && (i+len(term) == len(text) || !word(text[i+len(term)])) {
			return true
		}
		start = i + len(term)
	}
	return false
}

func assistantSearch(question string) assistantSearchQuery {
	q := assistantSearchQuery{question: question}
	q.preferred, question = assistantNaturalIntent(question)
	if assistantModelBudgetQuestion(question) {
		return assistantSearchQuery{concepts: [][]string{{"模型额度", "模型预算", "API额度", "API 额度", "月预算"}}, terms: []string{"模型额度"}}
	}
	for _, concept := range assistantConcepts {
		for _, term := range concept {
			if assistantContains(question, term) {
				q.concepts = append(q.concepts, concept)
				q.terms = append(q.terms, term)
				break
			}
		}
	}
	for _, action := range assistantActions {
		if assistantContains(question, action) {
			q.selectors = append(q.selectors, action)
		}
	}
	if len(q.concepts) == 0 {
		remaining := strings.ToLower(question)
		for _, stop := range assistantStopwords {
			remaining = strings.ReplaceAll(remaining, stop, " ")
		}
		for _, term := range strings.FieldsFunc(remaining, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' && r != '-' }) {
			// GPU/训练/API alone in an otherwise unknown question is not a
			// reason to recommend an unrelated article about that technology.
			if len([]rune(term)) < 2 || term == "gpu" || term == "训练" || term == "任务" || term == "api" || term == "分析" {
				continue
			}
			q.concepts = append(q.concepts, []string{term})
			q.terms = append(q.terms, term)
		}
	}
	return q
}

func assistantConceptMatch(text string, concept []string) bool {
	for _, term := range concept {
		if assistantContains(text, term) {
			return true
		}
	}
	return false
}

func (q assistantSearchQuery) score(title, metadata, body string) int {
	if len(q.concepts) == 0 {
		return 0
	}
	matched, score := 0, 0
	for _, concept := range q.concepts {
		// Body-only mentions often occur in warnings or unrelated examples.
		// At least half the concrete question concepts must match metadata.
		if assistantConceptMatch(metadata, concept) {
			matched++
			score += 8
			if assistantConceptMatch(title, concept) {
				score += 12
			}
		} else if assistantConceptMatch(body, concept) {
			score++
		}
	}
	if matched == 0 || matched*2 < len(q.concepts) {
		return 0
	}
	for _, term := range q.terms {
		if assistantContains(title, term) {
			score += 8
		}
	}
	for _, term := range q.selectors {
		if assistantContains(title, term) {
			score += 5
		} else if assistantContains(metadata, term) {
			score += 2
		}
	}
	return score
}

func (h *Handler) assistantDocuments(ctx context.Context, question string) ([]assistant.Evidence, bool) {
	store, ok := h.helpDocuments.(HelpArticleStore)
	if !ok {
		return nil, false
	}
	articles, err := store.ListHelpArticles(ctx)
	if err != nil {
		return nil, false
	}
	query := assistantSearch(question)
	type candidate struct {
		evidence assistant.Evidence
		score    int
	}
	var matches []candidate
	for _, article := range articles {
		if !domain.ValidHelpID(article.ID) {
			continue
		}
		doc := helpdocs.ProjectMLflowAccess(article.HelpDocument, h.mlflowNativePublicEnabled, h.mlflowDashboardPublicEnabled)
		body := assistantPublishedText(doc.Markdown, 64000)
		title := assistantPlainText(doc.Title, 160)
		metadata := title + " " + article.Summary + " " + strings.Join(article.Keywords, " ")
		score := query.score(title, metadata, body)
		if strings.EqualFold(strings.TrimSpace(query.question), strings.TrimSpace(title)) {
			score = 10000
		}
		for _, id := range query.preferred {
			if doc.ID == id {
				score += 100
			}
		}
		if score == 0 {
			continue
		}
		version := doc.PublishedVersion
		if version == 0 {
			version = doc.Version
		}
		matches = append(matches, candidate{score: score, evidence: assistant.Evidence{ID: doc.ID, Title: title, URL: "/raytrain/rayTrain/help#article/" + doc.ID, Excerpt: assistantContextExcerpt(body, query), Version: version}})
	}
	sort.SliceStable(matches, func(i, j int) bool {
		if matches[i].score != matches[j].score {
			return matches[i].score > matches[j].score
		}
		return matches[i].evidence.ID < matches[j].evidence.ID
	})
	result := make([]assistant.Evidence, 0, 4)
	for _, match := range matches {
		// Do not pad a confident answer with much weaker unrelated citations.
		if len(result) == 4 || match.score*2 < matches[0].score {
			break
		}
		result = append(result, match.evidence)
	}
	return result, true
}

func assistantContextExcerpt(body string, query assistantSearchQuery) string {
	type section struct{ heading, text string }
	var sections []section
	current := section{}
	parentHeading, parentContext := "", ""
	fence := ""
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			marker := trimmed[:3]
			if fence == "" {
				fence = marker
			} else if fence == marker {
				fence = ""
			}
		}
		if fence == "" && (strings.HasPrefix(trimmed, "#") && strings.Contains(trimmed, " ") || strings.HasPrefix(trimmed, "**") && strings.HasSuffix(trimmed, "**")) {
			if strings.TrimSpace(current.text) != "" {
				sections = append(sections, current)
			}
			if strings.HasPrefix(trimmed, "#") {
				parentHeading, parentContext = trimmed, ""
				current = section{heading: trimmed}
			} else {
				if parentContext == "" {
					parentContext = current.text
				}
				current = section{heading: parentHeading + " " + trimmed, text: parentContext + "\n"}
			}
		}
		current.text += line + "\n"
	}
	if strings.TrimSpace(current.text) != "" {
		sections = append(sections, current)
	}
	best, bestScore := "", -1
	for _, section := range sections {
		score := 0
		for _, concept := range query.concepts {
			if assistantConceptMatch(section.heading, concept) {
				score += 10
			}
			if assistantConceptMatch(section.text, concept) {
				score += 2
			}
		}
		for _, term := range append(append([]string(nil), query.terms...), query.selectors...) {
			if assistantContains(section.heading, term) {
				score += 8
			}
			if assistantContains(section.text, term) {
				score++
			}
		}
		for _, action := range query.selectors {
			if assistantContains(section.text, action) {
				score += 4
			}
		}
		// A subsection may repeat its prerequisite paragraph with the same
		// relevance score. Prefer its complete command over the bare preface.
		completeChild := score == bestScore && strings.HasPrefix(strings.TrimSpace(section.text), strings.TrimSpace(best)) && !strings.Contains(best, "```") && strings.Contains(section.text, "```")
		if score > bestScore || completeChild {
			best, bestScore = section.text, score
		}
	}
	return assistantBoundSection(strings.TrimSpace(best))
}

// Keep complete fenced blocks and the preceding instructions. Never return a
// truncated shell command that could execute with different arguments.
func assistantBoundSection(text string) string {
	if len([]rune(text)) <= assistantExcerptLimit {
		return text
	}
	const note = "\n\n后续步骤请打开本条引用查看完整说明。"
	budget := assistantExcerptLimit - len([]rune(note))
	var blocks []string
	block, fence := "", ""
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if fence == "" && trimmed == "" {
			if block != "" {
				blocks = append(blocks, block)
				block = ""
			}
			continue
		}
		block += line + "\n"
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			marker := trimmed[:3]
			if fence == "" {
				fence = marker
			} else if marker == fence {
				fence = ""
			}
		}
	}
	if block != "" && fence == "" {
		blocks = append(blocks, block)
	}
	result := ""
	for _, block := range blocks {
		block = strings.TrimSpace(block)
		if len([]rune(result+"\n\n"+block)) > budget {
			if result == "" && !strings.Contains(block, "```") && !strings.Contains(block, "~~~") {
				result = assistantTruncate(block, budget-1)
			}
			break
		}
		if result != "" {
			result += "\n\n"
		}
		result += block
	}
	return result + note
}

// Match concrete platform goals before broad words such as upload or model.
// IDs only rank documents actually returned by the published store; this does
// not create answers, citations, or invented procedures for absent content.
func assistantNaturalIntent(question string) ([]string, string) {
	has := func(terms ...string) bool { return assistantHasAny(question, terms...) }
	switch {
	case has("代码", "源码") && has("提交", "上传", "开始跑", "本地电脑"):
		return []string{"code"}, question + " ZIP working_dir 代码"
	case has("调试") && has("保存", "环境", "固化"):
		return []string{"debug", "custom-environment"}, question + " 镜像 依赖 工作区 保存"
	case has("任务", "训练") && has("取消", "停止", "终止"):
		return []string{"command-recipes", "observability"}, question + " CLI 取消 停止"
	case has("训练", "任务") && has("不开始", "没开始", "排队", "不运行", "没启动"):
		return []string{"scheduling-topology"}, question + " 排队 调度"
	case has("文件夹", "目录") && has("上传", "传上去", "整个传"):
		return []string{"uploads", "storage"}, question + " 上传 目录"
	case has("权重", "模型") && has("功能仓", "共享", "同步"):
		return []string{"shared-model-registration"}, question + " 共享模型 功能仓"
	case has("两台", "多台", "多机", "分布式") && has("任务", "训练", "机器"):
		return []string{"submit"}, question + " 多机 分布式"
	case has("python包", "python 包", "缺包", "缺个", "缺少依赖", "安装包") && has("环境", "依赖", "包"):
		return []string{"custom-environment"}, question + " 镜像 依赖"
	case has("其他人", "别人", "队友", "同事") && has("任务", "训练") && has("看", "访问", "权限"):
		return []string{"unified-login-and-roles", "quickstart"}, question + " 团队 权限"
	case has("训练", "任务") && has("继续", "没跑完", "恢复", "续训", "中断"):
		return []string{"resume"}, question + " 续训 checkpoint"
	}
	return nil, question
}
