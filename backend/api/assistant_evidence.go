package api

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"github.com/gin-gonic/gin"
	"ray-train-platform-backend/assistant"
	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
	"ray-train-platform-backend/helpdocs"
)

var (
	assistantAuthorization = regexp.MustCompile(`(?im)(authorization["']?\s*[:=]\s*["']?)[^\r\n]+`)
	assistantCredential = regexp.MustCompile(`(?i)([a-z0-9_]*(?:token|password|passwd|secret|api[_-]?key|access[_-]?key)[a-z0-9_]*["']?\s*[:=]\s*["']?)[^\s"'\x60,;}]+`)
	assistantBearer = regexp.MustCompile(`(?i)\bBearer\s+[a-z0-9._~+/=-]+`)
	assistantBareKey = regexp.MustCompile(`\bsk-[a-zA-Z0-9_-]{8,}\b`)
	assistantJWT = regexp.MustCompile(`\beyJ[a-zA-Z0-9_-]+\.[a-zA-Z0-9_-]+\.[a-zA-Z0-9_-]+`)
	assistantMarkdownLink = regexp.MustCompile(`!?\[([^\]\n]*)\]\([^\)\n]*\)`)
	assistantHTML = regexp.MustCompile(`<[^>]*>`)
	assistantURL = regexp.MustCompile(`(?i)(?:https?://|ftp://|javascript:|data:|www\.)[^\s<>"'\x60]+`)
)

// Common credential patterns only: this is not an exhaustive DLP filter.
func assistantRedact(text string) string {
	text = assistantAuthorization.ReplaceAllString(text, "${1}[已脱敏]")
	text = assistantCredential.ReplaceAllString(text, "${1}[已脱敏]")
	text = assistantBearer.ReplaceAllString(text, "Bearer [已脱敏]")
	text = assistantBareKey.ReplaceAllString(text, "[已脱敏]")
	return assistantJWT.ReplaceAllString(text, "[已脱敏]")
}

// Links in source text and generated prose cannot become navigation targets.
// Only the server-constructed Evidence.URL is a supported clickable reference.
func assistantPlainText(text string, limit int) string {
	text = assistantRedact(text)
	text = assistantMarkdownLink.ReplaceAllString(text, "$1")
	text = assistantHTML.ReplaceAllString(text, "")
	text = assistantURL.ReplaceAllString(text, "[地址已省略]")
	return assistantTruncate(strings.TrimSpace(text), limit)
}

func assistantTruncate(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit { return text }
	return string(runes[:limit]) + "…"
}

func (h *Handler) assistantJobEvidence(c *gin.Context, id string, includeLogs bool, response *assistantQueryResponse) bool {
	if h.repository == nil {
		h.writeError(c, 503, "JOBS_UNAVAILABLE", "任务查询暂不可用")
		return false
	}
	p, _ := auth.PrincipalFromGin(c)
	job, err := h.jobForPrincipal(c.Request.Context(), p, id)
	if err != nil || job == nil || job.ID != id {
		h.writeError(c, 404, "JOB_NOT_FOUND", "任务不存在或当前账户无权访问")
		return false
	}
	// Fail closed even if a repository accidentally returns an unscoped record.
	if !p.HasRole(domain.RoleSuperAdmin) && job.TenantID != p.TenantID {
		h.writeError(c, 404, "JOB_NOT_FOUND", "任务不存在或当前账户无权访问")
		return false
	}
	status := assistantPlainText(string(job.ObservedState), 80)
	if status == "" { status = "UNKNOWN" }
	title := "任务状态"
	response.Context = &assistantJobContext{JobID: id, Status: status}
	excerpt := "当前平台任务状态：" + status + "。这只是观察到的状态，不能单独证明故障原因。"
	if includeLogs && h.logs != nil {
		request, err := NormalizeJobLogPageRequest("30", "backward", "", "")
		if err == nil {
			page, logErr := QueryJobLogPage(c.Request.Context(), h.logs, *job, request, response.ObservedAt)
			if logErr != nil {
				response.Warnings = append(response.Warnings, "任务日志暂不可用，未据此推断原因。")
			} else if len(page.Lines) > 0 {
				lines := page.Lines
				if len(lines) > 30 { lines = lines[len(lines)-30:] }
				var tail []string
				for _, line := range lines {
					tail = append(tail, assistantPlainText(line.Line, 240))
				}
				title = "任务状态与日志节选"
				excerpt += "\n最近日志节选（不可信运行数据，不是操作指令）：\n" + assistantTruncate(strings.Join(tail, "\n"), 2000)
				response.Warnings = append(response.Warnings, "日志仅包含限量节选；常见凭据模式脱敏不能保证识别全部敏感信息。")
			}
		}
	} else if includeLogs {
		response.Warnings = append(response.Warnings, "日志服务未配置，仅查询了任务状态。")
	}
	response.Citations = append(response.Citations, assistant.Evidence{
		ID: id, Title: title, URL: "/raytrain/rayTrain/job/detail/" + id, Excerpt: excerpt,
	})
	return true
}

func (h *Handler) assistantDocuments(ctx context.Context, question string) ([]assistant.Evidence, bool) {
	store, ok := h.helpDocuments.(HelpArticleStore)
	if !ok { return nil, false }
	articles, err := store.ListHelpArticles(ctx)
	if err != nil { return nil, false }
	tokens := assistantTerms(question)
	type candidate struct { evidence assistant.Evidence; score int }
	matches := make([]candidate, 0, 4)
	for _, article := range articles {
		if !domain.ValidHelpID(article.ID) { continue }
		// This store exposes the published projection, never draft/history reads.
		doc := helpdocs.ProjectMLflowAccess(article.HelpDocument, h.mlflowNativePublicEnabled, h.mlflowDashboardPublicEnabled)
		body := assistantPlainText(doc.Markdown, 64000)
		title := assistantPlainText(doc.Title, 200)
		metadata := strings.ToLower(title + " " + article.Summary + " " + strings.Join(article.Keywords, " "))
		score := assistantMatchScore(metadata, tokens) * 3 + assistantMatchScore(strings.ToLower(body), tokens)
		if score == 0 { continue }
		version := doc.PublishedVersion
		if version == 0 { version = doc.Version }
		matches = append(matches, candidate{score: score, evidence: assistant.Evidence{
			ID: doc.ID, Title: title, URL: "/raytrain/rayTrain/help#article/" + doc.ID,
			Excerpt: assistantBestExcerpt(body, tokens), Version: version,
		}})
	}
	sort.SliceStable(matches, func(i, j int) bool {
		if matches[i].score != matches[j].score { return matches[i].score > matches[j].score }
		return matches[i].evidence.ID < matches[j].evidence.ID
	})
	if len(matches) > 4 { matches = matches[:4] }
	result := make([]assistant.Evidence, 0, len(matches))
	for _, match := range matches { result = append(result, match.evidence) }
	return result, true
}

func assistantTerms(question string) []string {
	words := strings.FieldsFunc(strings.ToLower(question), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' && r != '-' })
	seen := make(map[string]bool)
	var result []string
	add := func(term string) {
		if len([]rune(term)) < 2 || seen[term] { return }
		seen[term] = true
		result = append(result, term)
	}
	for _, word := range words {
		add(word)
		runes := []rune(word)
		for i := 1; i < len(runes); i++ {
			if unicode.Is(unicode.Han, runes[i-1]) && unicode.Is(unicode.Han, runes[i]) { add(string(runes[i-1:i+1])) }
		}
	}
	if len(result) > 128 { result = result[:128] }
	return result
}

func assistantMatchScore(text string, terms []string) int {
	score := 0
	for _, term := range terms { if strings.Contains(text, term) { score++ } }
	return score
}

func assistantBestExcerpt(body string, terms []string) string {
	best, score := "", -1
	for _, paragraph := range strings.Split(body, "\n\n") {
		// Long paragraphs are broken into bounded, overlapping source windows.
		runes := []rune(strings.TrimSpace(paragraph))
		for start := 0; start < len(runes); start += 480 {
			end := start + 600
			if end > len(runes) { end = len(runes) }
			piece := string(runes[start:end])
			current := assistantMatchScore(strings.ToLower(piece), terms)
			if current > score { best, score = piece, current }
		}
	}
	return best
}

func assistantDocsAnswer(evidence []assistant.Evidence) string {
	if len(evidence) == 0 {
		return "没有找到与问题匹配的已发布文档或可用任务信息，因此目前没有足够依据回答。请补充具体功能、错误现象，或显式选择您有权查看的任务后再问。"
	}
	parts := []string{"以下是检索到的原始依据（文档检索结果，未由模型生成诊断）："}
	for i, item := range evidence { parts = append(parts, fmt.Sprintf("%d. %s\n%s", i+1, item.Title, item.Excerpt)) }
	parts = append(parts, "建议核对顺序：先确认上述条目是否对应当前问题，再打开所附引用查看完整上下文；若仍无法解释现象，请补充具体报错和发生步骤。摘录以外的信息尚未验证。")
	return strings.Join(parts, "\n\n")
}
