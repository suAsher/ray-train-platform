package api

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/gin-gonic/gin"
	"ray-train-platform-backend/assistant"
	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
)

var (
	assistantAuthorization = regexp.MustCompile(`(?im)(authorization["']?\s*[:=]\s*["']?)[^\r\n]+`)
	assistantCredential    = regexp.MustCompile(`(?is)([a-z0-9_]*(?:token|password|passwd|secret|api[_-]?key|access[_-]?key)[a-z0-9_]*["']?\s*[:=]\s*)(?:"(?:\\.|[^"\\])*(?:"|\\?\z)|'(?:\\.|[^'\\])*(?:'|\\?\z)|[^\s"'\x60,;}]+)`)
	assistantBearer        = regexp.MustCompile(`(?i)\bBearer\s+[a-z0-9._~+/=-]+`)
	assistantBareKey       = regexp.MustCompile(`\bsk-[a-zA-Z0-9_-]{8,}\b`)
	assistantJWT           = regexp.MustCompile(`\beyJ[a-zA-Z0-9_-]+\.[a-zA-Z0-9_-]+\.[a-zA-Z0-9_-]+`)
	assistantMarkdownLink  = regexp.MustCompile(`!?\[([^\]\n]*)\]\([^\)\n]*\)`)
	assistantHTML          = regexp.MustCompile(`<[^>]*>`)
	assistantURL           = regexp.MustCompile(`(?i)(?:https?://|ftp://|javascript:|data:|www\.)[^\s<>"'\x60]+`)
)

// Common credential patterns only: this is not an exhaustive DLP filter.
// A quoted credential may be truncated by logging. Without a closing quote,
// consume through EOF, including newlines and a final incomplete escape.
func assistantRedact(text string) string {
	text = assistantAuthorization.ReplaceAllString(text, "${1}[已脱敏]")
	text = assistantRedactCredentials(text)
	text = assistantBearer.ReplaceAllString(text, "Bearer [已脱敏]")
	text = assistantBareKey.ReplaceAllString(text, "[已脱敏]")
	return assistantJWT.ReplaceAllString(text, "[已脱敏]")
}

func assistantRedactCredentials(text string) string {
	var result strings.Builder
	end := 0
	for _, match := range assistantCredential.FindAllStringSubmatchIndex(text, -1) {
		result.WriteString(text[end:match[0]])
		prefix, value := text[match[2]:match[3]], text[match[3]:match[1]]
		if assistantPaginationCode(text[:match[0]], prefix, value) {
			result.WriteString(text[match[0]:match[1]])
		} else {
			result.WriteString(prefix)
			result.WriteString("[已脱敏]")
		}
		end = match[1]
	}
	result.WriteString(text[end:])
	return result.String()
}

// Pagination variable references are code, not authentication material. Keep
// only known nonliteral expressions; quoted or arbitrary values under these
// same names still pass through credential redaction.
func assistantPaginationCode(before, prefix, value string) bool {
	assignment := strings.TrimSpace(prefix)
	name := strings.TrimSpace(strings.TrimRight(assignment, "=:"))
	switch name {
	case "page_token", "run_token", "experiment_token":
	default:
		return false
	}
	if strings.HasSuffix(assignment, ":") {
		line := before[strings.LastIndex(before, "\n")+1:]
		return strings.TrimSpace(line) == "if not" && value == "break"
	}
	switch strings.TrimRight(value, ")]") {
	case "None", "page_token", "run_token", "experiment_token", "page.token", "runs.token", "experiments.token":
		return true
	}
	return false
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
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit]) + "…"
}

func (h *Handler) assistantJobEvidence(c *gin.Context, id string, includeLogs bool, response *assistantQueryResponse, questions ...string) bool {
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
	if status == "" {
		status = "UNKNOWN"
	}
	title := "任务状态"
	response.Context = &assistantJobContext{JobID: id, Status: status}
	excerpt := "当前平台任务状态：" + status + "（查询时的观察快照）。"
	if len(questions) > 0 {
		excerpt += h.assistantTaskDetails(c.Request.Context(), *job, questions[0], includeLogs, response)
	}
	if job.LastObservedAt != nil {
		excerpt += "\n任务状态最后观察时间：" + job.LastObservedAt.UTC().Format("2006-01-02T15:04:05Z")
	}
	if includeLogs && h.logs != nil {
		request, err := NormalizeJobLogPageRequest("30", "backward", "", "")
		if err == nil {
			page, logErr := QueryJobLogPage(c.Request.Context(), h.logs, *job, request, response.ObservedAt)
			if logErr != nil {
				response.Warnings = append(response.Warnings, "任务日志暂不可用，未据此推断原因。")
			} else if len(page.Lines) > 0 {
				lines := page.Lines
				if len(lines) > 30 {
					lines = lines[len(lines)-30:]
				}
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

func assistantDocsAnswer(evidence []assistant.Evidence, questions ...string) string {
	if len(evidence) == 0 {
		if len(questions) > 0 && assistantModelBudgetQuestion(questions[0]) {
			return "没有找到当前模型额度的实时依据。团队 GPU 配额不能回答模型 API 余额；请说明使用的是哪个已配置模型服务，并让管理员核对该服务的额度或错误提示。不要提供 API Key。"
		}
		if len(questions) > 0 && assistantHasAny(questions[0], "失败", "报错", "原因", "状态") {
			return "没有找到足够相关的依据。请显式选择要排查的任务，并提供具体错误信息；日志需要为本次问题单独勾选同意。不要发送令牌或密码。"
		}
		return "没有找到足够相关的已发布说明。您要操作哪项平台功能，当前位于哪个页面或正在执行哪条命令？请补充这两项信息后再问。"
	}
	parts := []string{"根据本次检索到的说明和授权信息："}
	for i, item := range evidence {
		parts = append(parts, fmt.Sprintf("%d. %s\n%s", i+1, item.Title, item.Excerpt))
	}
	parts = append(parts, "以上为只读检索结果；命令由您核对后执行，助手没有执行任何操作。")
	return strings.Join(parts, "\n\n")
}
