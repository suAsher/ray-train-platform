package api

import (
	"net/url"
	"strings"

	"ray-train-platform-backend/assistant"
)

// Only fixed public platform origins may survive inside source command text.
// No query, fragment, credentials, escaped path or alternate port is accepted.
func assistantSafeSourceURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(raw, "#") || u.RawPath != "" {
		return false
	}
	switch u.Host {
	case "raytrain.wellspiking.ai", "spiking.wellspiking.ai", "spiking-dev.wellspiking.ai":
		return true
	default:
		return false
	}
}

func assistantSourceText(text string, limit int, allowed func(string) bool) string {
	text = assistantURL.ReplaceAllStringFunc(text, func(raw string) string {
		clean := strings.TrimRight(raw, ".,;:!?，。；：！？」』】）)")
		if allowed(clean) {
			return clean + strings.TrimPrefix(raw, clean)
		}
		return "[地址已省略]"
	})
	text = assistantRedact(text)
	text = assistantSourceMarkup(text)
	return assistantTruncate(strings.TrimSpace(text), limit)
}

// Markdown syntax inside fenced/inline code is literal. Stripping it as HTML
// or a link can turn comparisons, placeholders and Python calls into invalid
// code. Credential and URL filtering runs on all text before this step.
func assistantSourceMarkup(text string) string {
	lines := strings.Split(text, "\n")
	fence := ""
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			marker := trimmed[:3]
			if fence == "" {
				fence = marker
			} else if fence == marker {
				fence = ""
			}
			continue
		}
		if fence == "" {
			lines[i] = assistantInlineSourceMarkup(line)
		}
	}
	return strings.Join(lines, "\n")
}

func assistantInlineSourceMarkup(line string) string {
	strip := func(text string) string {
		return assistantHTML.ReplaceAllString(assistantMarkdownLink.ReplaceAllString(text, "$1"), "")
	}
	var result strings.Builder
	for {
		start := strings.IndexByte(line, '`')
		if start < 0 {
			result.WriteString(strip(line))
			return result.String()
		}
		width := 1
		for start+width < len(line) && line[start+width] == '`' {
			width++
		}
		end := strings.Index(line[start+width:], strings.Repeat("`", width))
		if end < 0 {
			result.WriteString(strip(line))
			return result.String()
		}
		end += start + 2*width
		result.WriteString(strip(line[:start]))
		result.WriteString(line[start:end])
		line = line[end:]
	}
}

func assistantPublishedText(text string, limit int) string {
	return assistantSourceText(text, limit, assistantSafeSourceURL)
}

// A model may repeat an exact safe URL already in a published document
// excerpt. Fact/log evidence and model-invented paths never add destinations.
func assistantGroundedText(text string, limit int, evidence []assistant.Evidence) string {
	allowed := map[string]bool{}
	for _, item := range evidence {
		if item.URL != "/raytrain/rayTrain/help#article/"+item.ID {
			continue
		}
		for _, raw := range assistantURL.FindAllString(item.Excerpt, -1) {
			value := strings.TrimRight(raw, ".,;:!?，。；：！？」』】）)")
			if assistantSafeSourceURL(value) {
				allowed[value] = true
			}
		}
	}
	return assistantSourceText(text, limit, func(value string) bool { return allowed[value] })
}
