package assistantidle

import (
	"encoding/json"
	"errors"
	"io"
	"strings"
)

// RuntimeConfig is mounted from an administrator-owned ConfigMap, never a user request.
// Operational time bounds stay fixed until new acceptance evidence justifies changing them.
type RuntimeConfig struct {
	Enabled    bool         `json:"enabled"`
	InstanceID string       `json:"instanceID"`
	LeaseName  string       `json:"leaseName"`
	Render     RenderConfig `json:"render"`
}

func ParseRuntimeConfig(r io.Reader) (RuntimeConfig, error) {
	var cfg RuntimeConfig
	decoder := json.NewDecoder(io.LimitReader(r, 65537))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cfg); err != nil {
		return cfg, err
	}
	if decoder.Decode(new(any)) != io.EOF {
		return cfg, errors.New("expected one configuration object")
	}
	cfg.Render = cfg.Render.withDefaults()
	if len(cfg.InstanceID) > 63 || !dnsLabelPattern.MatchString(cfg.InstanceID) || cfg.InstanceID != cfg.Render.Name {
		return cfg, errors.New("instanceID must equal the fixed service name")
	}
	if len(cfg.LeaseName) > 63 || !dnsLabelPattern.MatchString(cfg.LeaseName) {
		return cfg, errors.New("invalid lease name")
	}
	if !strings.HasPrefix(cfg.Render.Namespace, "raytrain-assistant-") {
		return cfg, errors.New("a dedicated raytrain-assistant- namespace is required")
	}
	if _, err := RenderRayService(cfg.Render); err != nil {
		return cfg, err
	}
	return cfg, nil
}
