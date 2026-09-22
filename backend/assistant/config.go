package assistant

import (
	"errors"
	"net/url"
	"path"
	"regexp"
	"strings"
	"unicode"
)

var backendIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

// ValidateConfig is shared by startup configuration and router construction.
// Errors intentionally contain no configured values, URLs, or credentials.
func ValidateConfig(cfg Config) error {
	if len(cfg.Providers) > 4 {
		return errors.New("assistant supports at most four providers")
	}
	seen := make(map[string]bool, len(cfg.Providers))
	for _, p := range cfg.Providers {
		if !backendIDPattern.MatchString(p.ID) || seen[p.ID] {
			return errors.New("assistant provider IDs must be unique safe identifiers")
		}
		seen[p.ID] = true
		if p.Kind != "api" && p.Kind != "local" {
			return errors.New("assistant provider kind must be api or local")
		}
		if strings.TrimSpace(p.Model) == "" || len(p.Model) > 200 || containsControl(p.Model) {
			return errors.New("assistant provider requires a valid model")
		}
		if p.Kind == "api" && strings.TrimSpace(p.APIKey) == "" {
			return errors.New("assistant API provider requires a company API key")
		}
		if containsControl(p.APIKey) || len(p.APIKey) > 8192 || strings.TrimSpace(p.APIKey) != p.APIKey {
			return errors.New("assistant provider API key is invalid")
		}
		if _, err := providerEndpoint(p); err != nil {
			return err
		}
	}
	return nil
}

func containsControl(value string) bool {
	return strings.IndexFunc(value, unicode.IsControl) >= 0
}

func providerEndpoint(cfg ProviderConfig) (string, error) {
	u, err := url.Parse(cfg.BaseURL)
	if err != nil || len(cfg.BaseURL) > 2048 || containsControl(cfg.BaseURL) ||
		u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" ||
		u.RawPath != "" || u.ForceQuery || u.Opaque != "" || strings.Contains(cfg.BaseURL, "#") {
		return "", errors.New("assistant endpoint must be a fixed credential-free URL")
	}
	internalHTTP := cfg.Kind == "local" && u.Scheme == "http" && strings.HasSuffix(strings.ToLower(u.Hostname()), ".svc.cluster.local")
	if u.Scheme != "https" && !internalHTTP {
		return "", errors.New("assistant endpoint requires HTTPS; internal local provider may use cluster Service HTTP")
	}
	if strings.Contains(u.Path, "//") || path.Clean(u.Path) != strings.TrimSuffix(u.Path, "/") && u.Path != "" && u.Path != "/" {
		return "", errors.New("assistant endpoint has non-canonical path")
	}
	prefix := strings.TrimSuffix(u.Path, "/")
	if prefix == "" {
		prefix = "/v1"
	}
	u.Path = prefix + "/chat/completions"
	return u.String(), nil
}
