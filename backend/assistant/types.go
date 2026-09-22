// Package assistant provides bounded, read-only evidence summarization. Providers
// never receive platform credentials or authority to execute tools.
package assistant

import "context"

type Evidence struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	URL     string `json:"url"`
	Excerpt string `json:"excerpt"`
	Version int64  `json:"version,omitempty"`
}

type Input struct {
	Question string
	Evidence []Evidence
}

type Result struct {
	Answer, Mode, Reason string
	BackendID            string
}

type ProviderStatus struct {
	Configured bool `json:"configured"`
}

type BackendStatus struct {
	ID         string `json:"id"`
	Kind       string `json:"kind"`
	Model      string `json:"model"`
	Configured bool   `json:"configured"`
}

type Capabilities struct {
	Enabled     bool                      `json:"enabled"`
	ReadOnly    bool                      `json:"readOnly"`
	Modes       []string                  `json:"modes"`
	DefaultMode string                    `json:"defaultMode"`
	Providers   map[string]ProviderStatus `json:"providers"`
	Backends    []BackendStatus           `json:"backends"`
	Limitations []string                  `json:"limitations"`
}

type Engine interface {
	Answer(context.Context, string, Input) (Result, error)
	Capabilities() Capabilities
}

// ProviderConfig describes a server-controlled OpenAI-compatible Chat
// Completions endpoint. Kind is a routing category, not a wire protocol.
// Native Anthropic and Gemini protocols require separate future adapters.
type ProviderConfig struct {
	ID               string `json:"id"`
	Kind             string `json:"kind"`
	BaseURL          string `json:"baseUrl"`
	Model            string `json:"model"`
	APIKey           string `json:"-"`
	ThinkingDisabled bool   `json:"thinkingDisabled,omitempty"`
}

type Config struct {
	Providers  []ProviderConfig
	LocalFirst bool
}
