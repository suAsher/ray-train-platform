package api

import (
	"regexp"
	"strings"

	"ray-train-platform-backend/observability"
)

var (
	ansiControlSequence = regexp.MustCompile(`\x1b(?:\[[0-?]*[ -/]*[@-~]|\][^\x07]*(?:\x07|\x1b\\)|[@-_])`)
	nonTextControlByte  = regexp.MustCompile(`[\x00-\x08\x0b\x0c\x0e-\x1f\x7f]`)
)

func sanitizeJobLogLines(lines []observability.LogLine) []observability.LogLine {
	cleaned := make([]observability.LogLine, 0, len(lines))
	for _, line := range lines {
		text := sanitizeJobLogText(line.Line)
		if text == "" {
			continue
		}
		copy := line
		copy.Line = text
		cleaned = append(cleaned, copy)
	}
	return cleaned
}

func sanitizeJobLogText(value string) string {
	// Terminal progress renderers redraw one physical line with carriage
	// returns. Logs have no cursor, so only the final frame is meaningful.
	if redraw := strings.LastIndex(value, "\r"); redraw >= 0 {
		value = value[redraw+1:]
	}
	value = ansiControlSequence.ReplaceAllString(value, "")
	value = nonTextControlByte.ReplaceAllString(value, "")
	value = strings.ReplaceAll(value, "\n", " ")
	return strings.TrimSpace(value)
}
