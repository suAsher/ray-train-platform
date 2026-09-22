package config

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"unicode"

	"ray-train-platform-backend/assistant"
)

type AssistantConfig struct {
	Enabled         bool
	PreviewSubjects []string
	Routing         assistant.Config
}

type assistantProviderSettings struct {
	ID               string `json:"id"`
	Kind             string `json:"kind"`
	Protocol         string `json:"protocol"`
	BaseURL          string `json:"baseURL"`
	Model            string `json:"model"`
	KeyEnv           string `json:"keyEnv"`
	KeyFile          string `json:"keyFile"`
	CAFile           string `json:"caFile"`
	ThinkingDisabled bool   `json:"thinkingDisabled"`
}

var assistantKeyEnv = regexp.MustCompile(`^ASSISTANT_PROVIDER_[A-Z0-9_]{1,64}_KEY$`)

func loadAssistantConfig() (AssistantConfig, error) {
	enabled, err := parseBool("ASSISTANT_ENABLED", false)
	if err != nil {
		return AssistantConfig{}, err
	}
	cfg := AssistantConfig{Enabled: enabled}
	if !enabled {
		return cfg, nil
	}
	if raw := strings.TrimSpace(os.Getenv("ASSISTANT_PREVIEW_SUBJECTS")); raw != "" {
		if len(raw) > 32768 || !strings.HasPrefix(raw, "[") || json.Unmarshal([]byte(raw), &cfg.PreviewSubjects) != nil || len(cfg.PreviewSubjects) > 100 {
			return AssistantConfig{}, fmt.Errorf("ASSISTANT_PREVIEW_SUBJECTS must be a JSON list of at most 100 subjects")
		}
		for _, subject := range cfg.PreviewSubjects {
			if subject == "" || len(subject) > 256 || strings.TrimSpace(subject) != subject || strings.IndexFunc(subject, unicode.IsControl) >= 0 {
				return AssistantConfig{}, fmt.Errorf("assistant preview subject is invalid")
			}
		}
	}
	cfg.Routing.LocalFirst, err = parseBool("ASSISTANT_LOCAL_FIRST", false)
	if err != nil {
		return AssistantConfig{}, err
	}
	raw := strings.TrimSpace(os.Getenv("ASSISTANT_PROVIDERS"))
	if raw == "" {
		return cfg, nil
	} // Capabilities keep the bot unavailable until a provider is configured.
	if len(raw) > 16384 {
		return AssistantConfig{}, fmt.Errorf("ASSISTANT_PROVIDERS exceeds 16 KiB")
	}
	var entries []assistantProviderSettings
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&entries) != nil {
		return AssistantConfig{}, fmt.Errorf("ASSISTANT_PROVIDERS must be a supported JSON provider list; credentials belong in referenced environment variables or Secret files")
	}
	if decoder.Decode(new(any)) != io.EOF {
		return AssistantConfig{}, fmt.Errorf("ASSISTANT_PROVIDERS must contain a single JSON value")
	}
	for _, entry := range entries {
		key := ""
		if entry.KeyEnv != "" && entry.KeyFile != "" {
			return AssistantConfig{}, fmt.Errorf("assistant provider must choose keyEnv or keyFile")
		}
		if entry.KeyFile != "" {
			if !strings.HasPrefix(entry.BaseURL, "https://") {
				return AssistantConfig{}, fmt.Errorf("assistant credential file requires an HTTPS provider")
			}
			key, err = assistant.ReadProviderKeyFile(entry.KeyFile)
			if err != nil {
				return AssistantConfig{}, err
			}
		}
		if entry.KeyEnv != "" {
			if !assistantKeyEnv.MatchString(entry.KeyEnv) {
				return AssistantConfig{}, fmt.Errorf("assistant keyEnv must use the dedicated ASSISTANT_PROVIDER_*_KEY namespace")
			}
			key = os.Getenv(entry.KeyEnv)
			if key == "" {
				return AssistantConfig{}, fmt.Errorf("assistant referenced credential is missing")
			}
		}
		cfg.Routing.Providers = append(cfg.Routing.Providers, assistant.ProviderConfig{ID: entry.ID, Kind: entry.Kind, Protocol: entry.Protocol, BaseURL: entry.BaseURL, Model: entry.Model, APIKey: key, CAFile: entry.CAFile, ThinkingDisabled: entry.ThinkingDisabled})
	}
	if err := assistant.ValidateConfig(cfg.Routing); err != nil {
		return AssistantConfig{}, fmt.Errorf("invalid assistant provider configuration: %w", err)
	}
	return cfg, nil
}
