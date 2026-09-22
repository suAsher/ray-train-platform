// Package assistant provides bounded, read-only evidence summarization. Providers
// never receive platform credentials or authority to execute tools.
package assistant

import "context"

type Evidence struct {
	ID string `json:"id"`
	Title string `json:"title"`
	URL string `json:"url"`
	Excerpt string `json:"excerpt"`
	Version int64 `json:"version,omitempty"`
}

type Input struct { Question string; Evidence []Evidence }
type Result struct { Answer, Mode, Reason string }
type ProviderStatus struct { Configured bool `json:"configured"` }
type Capabilities struct {
	Enabled bool `json:"enabled"`
	ReadOnly bool `json:"readOnly"`
	Modes []string `json:"modes"`
	DefaultMode string `json:"defaultMode"`
	Providers map[string]ProviderStatus `json:"providers"`
	Limitations []string `json:"limitations"`
}
type Engine interface {
	Answer(context.Context, string, Input) (Result, error)
	Capabilities() Capabilities
}

type ProviderConfig struct {
	BaseURL string
	Model string
	APIKey string `json:"-"`
}
type Config struct { API, Local ProviderConfig; LocalFirst bool }
