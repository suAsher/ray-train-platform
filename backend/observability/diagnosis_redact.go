package observability

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

const diagnosisLineBytes = 2048

var (
	diagnosisANSI = regexp.MustCompile(`\x1b(?:\[[0-?]*[ -/]*[@-~]|\][^\x07]*(?:\x07|\x1b\\)|[@-_])`)
	diagnosisSensitiveValue = regexp.MustCompile(`(?i)(["']?(?:[a-z0-9_]*(?:token|password|passwd|secret|api[_-]?key|access[_-]?key|credential)[a-z0-9_]*|authorization)["']?\s*[:=]\s*)(?:"[^"\r\n]*"|'[^'\r\n]*'|[^\s,;\x1b]+)`)
	diagnosisBearer = regexp.MustCompile(`(?i)\bBearer\s+[^\s,;"'\x1b]+`)
	diagnosisBasic = regexp.MustCompile(`(?i)(\bauthorization\s*[:=]\s*Basic\s+)[^\s,;"'\x1b]+`)
	diagnosisURL = regexp.MustCompile(`(?i)https?://[^\s<>"'\x1b]+`)
	diagnosisURLCredentials = regexp.MustCompile(`(?i)^https?://[^/?#]*@`)
	diagnosisSignedURL = regexp.MustCompile(`(?i)(?:[?&](?:x-amz-|x-tos-|x-goog-|signature=|sig=|token=|access_token=|credential=))`)
)

func RedactDiagnosisText(value string) (string, bool) {
	value = cleanDiagnosisControlText(value)
	value = diagnosisURL.ReplaceAllStringFunc(value, func(raw string) string {
		if diagnosisSignedURL.MatchString(raw) || diagnosisURLCredentials.MatchString(raw) { return "[REDACTED_URL]" }
		return raw
	})
	value = diagnosisBearer.ReplaceAllString(value, "Bearer [REDACTED]")
	value = diagnosisBasic.ReplaceAllString(value, "${1}[REDACTED]")
	value = diagnosisSensitiveValue.ReplaceAllString(value, "${1}[REDACTED]")
	value = strings.TrimSpace(value)
	if len(value) <= diagnosisLineBytes { return value, false }
	end := diagnosisLineBytes
	for end > 0 && !utf8.RuneStart(value[end]) { end-- }
	return value[:end] + " [TRUNCATED]", true
}

func cleanDiagnosisControlText(value string) string {
	value = diagnosisANSI.ReplaceAllString(value, "")
	return strings.Map(func(char rune) rune { if unicode.IsControl(char) { return ' ' }; return char }, value)
}

func RedactDiagnosisLogLine(line LogLine) (LogLine, bool) {
	text, truncated := RedactDiagnosisText(line.Line)
	clean := LogLine{Timestamp: line.Timestamp, Line: text}
	// Stream metadata is user-influenced too. Only bounded, useful Loki labels
	// leave this API; arbitrary labels may contain credentials or environment.
	for _, key := range []string{"pod", "container", "stream", "namespace", "node", "platform_job_id"} {
		if value, ok := line.Stream[key]; ok {
			if clean.Stream == nil { clean.Stream = make(map[string]string) }
			redacted, limited := RedactDiagnosisText(value)
			if len(redacted) > 128 { redacted = redacted[:128]; limited = true }
			clean.Stream[key] = strings.ToValidUTF8(redacted, "")
			truncated = truncated || limited
		}
	}
	return clean, truncated
}
