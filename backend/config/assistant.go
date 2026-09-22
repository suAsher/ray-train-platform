package config

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"

	"ray-train-platform-backend/assistant"
)

type AssistantConfig struct {
	Enabled bool
	Routing assistant.Config
}

type assistantProviderSettings struct {
	ID string `json:"id"`
	Kind string `json:"kind"`
	BaseURL string `json:"baseURL"`
	Model string `json:"model"`
	KeyEnv string `json:"keyEnv"`
	ThinkingDisabled bool `json:"thinkingDisabled"`
}

var assistantKeyEnv = regexp.MustCompile(`^ASSISTANT_PROVIDER_[A-Z0-9_]{1,64}_KEY$`)

func loadAssistantConfig() (AssistantConfig,error) {
	enabled,err:=parseBool("ASSISTANT_ENABLED",false)
	if err!=nil{return AssistantConfig{},err}
	cfg:=AssistantConfig{Enabled:enabled}
	if !enabled{return cfg,nil}
	cfg.Routing.LocalFirst,err=parseBool("ASSISTANT_LOCAL_FIRST",false)
	if err!=nil{return AssistantConfig{},err}
	raw:=strings.TrimSpace(os.Getenv("ASSISTANT_PROVIDERS"))
	if raw=="" {return cfg,nil} // Explicit retrieval-only mode needs no model or key.
	if len(raw)>16384{return AssistantConfig{},fmt.Errorf("ASSISTANT_PROVIDERS exceeds 16 KiB")}
	var entries []assistantProviderSettings
	decoder:=json.NewDecoder(strings.NewReader(raw));decoder.DisallowUnknownFields()
	if decoder.Decode(&entries)!=nil{return AssistantConfig{},fmt.Errorf("ASSISTANT_PROVIDERS must be a supported JSON provider list; credentials belong in referenced environment variables")}
	if decoder.Decode(new(any))!=io.EOF{return AssistantConfig{},fmt.Errorf("ASSISTANT_PROVIDERS must contain a single JSON value")}
	for _,entry:=range entries {
		key:=""
		if entry.KeyEnv!="" {
			if !assistantKeyEnv.MatchString(entry.KeyEnv){return AssistantConfig{},fmt.Errorf("assistant keyEnv must use the dedicated ASSISTANT_PROVIDER_*_KEY namespace")}
			key=os.Getenv(entry.KeyEnv)
			if key==""{return AssistantConfig{},fmt.Errorf("assistant referenced credential is missing")}
		}
		cfg.Routing.Providers=append(cfg.Routing.Providers,assistant.ProviderConfig{ID:entry.ID,Kind:entry.Kind,BaseURL:entry.BaseURL,Model:entry.Model,APIKey:key,ThinkingDisabled:entry.ThinkingDisabled})
	}
	if err:=assistant.ValidateConfig(cfg.Routing);err!=nil{return AssistantConfig{},fmt.Errorf("invalid assistant provider configuration: %w",err)}
	return cfg,nil
}
