package assistant

import (
	"encoding/json"
	"strings"
)

// Native Messages uses a top-level system prompt, not a system-role turn.
// This adapter intentionally enables no tools, thinking, attachments or stream.
func anthropicRequestBody(model string, input Input) ([]byte, error) {
	return json.Marshal(struct {
		Model     string              `json:"model"`
		System    string              `json:"system"`
		Messages  []map[string]string `json:"messages"`
		MaxTokens int                 `json:"max_tokens"`
		Stream    bool                `json:"stream"`
	}{
		Model: model, System: systemPrompt, MaxTokens: 1500, Stream: false,
		Messages: []map[string]string{{"role": "user", "content": evidencePrompt(input)}},
	})
}

func anthropicAnswer(body []byte) (string, error) {
	var result struct {
		Type    string `json:"type"`
		Role    string `json:"role"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if json.Unmarshal(body, &result) != nil || result.Type != "message" || result.Role != "assistant" {
		return "", errUnavailable
	}
	var parts []string
	for _, block := range result.Content {
		// Unknown, thinking, redacted-thinking and tool blocks are never surfaced,
		// even if a gateway includes a misleading text field on those blocks.
		if block.Type == "text" && strings.TrimSpace(block.Text) != "" {
			parts = append(parts, block.Text)
		}
	}
	return strings.Join(parts, "\n"), nil
}
