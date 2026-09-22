package assistant

import (
	"context"
	"errors"
	"sync"
	"time"
)

var (
	errBudget         = errors.New("budget_exhausted")
	errAuthentication = errors.New("authentication_error")
	errRateLimited    = errors.New("rate_limited")
	errUnavailable    = errors.New("provider_unavailable")
	errInvalidInput   = errors.New("invalid_input")
)

type provider interface {
	complete(context.Context, Input) (string, error)
}

type backend struct {
	id, kind, model, protocol string
	provider                  provider
}

type Router struct {
	backends      []backend
	localFirst    bool
	mu            sync.Mutex
	blockedUntil  map[string]time.Time
	blockedReason map[string]string
}

func NewRouter(cfg Config) (*Router, error) {
	if err := ValidateConfig(cfg); err != nil {
		return nil, err
	}
	r := &Router{localFirst: cfg.LocalFirst}
	for _, c := range cfg.Providers {
		p, err := newHTTPProvider(c)
		if err != nil {
			return nil, err
		}
		r.backends = append(r.backends, backend{id: c.ID, kind: c.Kind, model: c.Model, protocol: effectiveProtocol(c.Protocol), provider: p})
	}
	return r, nil
}

func (r *Router) Capabilities() Capabilities {
	statuses := map[string]ProviderStatus{"api": {}, "local": {}}
	backends := make([]BackendStatus, 0, len(r.backends))
	for _, b := range r.backends {
		configured := b.provider != nil
		statuses[b.kind] = ProviderStatus{Configured: statuses[b.kind].Configured || configured}
		backends = append(backends, BackendStatus{ID: b.id, Kind: b.kind, Model: b.model, Protocol: effectiveProtocol(b.protocol), Configured: configured})
	}
	modes := []string{}
	defaultMode := ""
	enabled := statuses["api"].Configured || statuses["local"].Configured
	if enabled {
		modes = append(modes, "auto")
		for _, kind := range []string{"api", "local"} {
			if statuses[kind].Configured {
				modes = append(modes, kind)
			}
		}
		modes = append(modes, "docs")
		defaultMode = "auto"
	}
	return Capabilities{
		Enabled: enabled, ReadOnly: true, Modes: modes, DefaultMode: defaultMode,
		Providers: statuses, Backends: backends,
		Limitations: []string{
			"只读助手，不执行命令或修改训练任务",
			"自建推理仅连接已配置服务，不自动占用 GPU",
			"支持 OpenAI 兼容 Chat Completions 与 Anthropic 原生 Messages 接口",
			"预算由模型网关执行；失败后的五分钟冷却不代表真实月额度",
			"当前回答基于本次提问与引用证据，不保存服务端聊天历史",
		},
	}
}

func (r *Router) Answer(ctx context.Context, mode string, input Input) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if mode == "docs" {
		return Result{Mode: "docs", Reason: "requested"}, nil
	}
	order, err := r.route(mode)
	if err != nil {
		return Result{}, err
	}
	if err := validateInput(input); err != nil {
		return Result{}, err
	}
	reason := "not_configured"
	for i, b := range order {
		if blocked := r.blocked(b.id); blocked != "" {
			reason = blocked
			continue
		}
		attempt, cancel := context.WithTimeout(ctx, attemptTimeout(ctx, len(order)-i))
		answer, err := b.provider.complete(attempt, input)
		cancel()
		if ctx.Err() != nil {
			return Result{}, ctx.Err()
		}
		if err == nil {
			selected := "selected"
			if reason != "not_configured" {
				selected = "fallback_" + reason
			}
			return Result{Answer: answer, Mode: b.kind, BackendID: b.id, Reason: selected}, nil
		}
		if errors.Is(err, context.Canceled) {
			return Result{}, err
		}
		reason = providerReason(err)
		r.cooldown(b.id, reason)
	}
	return Result{Mode: "docs", Reason: reason}, nil
}

func (r *Router) route(mode string) ([]backend, error) {
	kinds := []string{"api", "local"}
	if r.localFirst {
		kinds = []string{"local", "api"}
	}
	if mode == "api" || mode == "local" {
		kinds = []string{mode}
	} else if mode != "auto" {
		return nil, errors.New("invalid_mode")
	}
	var ordered []backend
	for _, kind := range kinds {
		for _, b := range r.backends {
			if b.kind == kind && b.provider != nil {
				ordered = append(ordered, b)
			}
		}
	}
	return ordered, nil
}

// Share the remaining request budget so a slow first backend leaves time for
// siblings. The API additionally bounds the whole query to 35 seconds.
func attemptTimeout(ctx context.Context, remaining int) time.Duration {
	timeout := 12 * time.Second
	if deadline, ok := ctx.Deadline(); ok {
		share := time.Until(deadline) / time.Duration(remaining)
		if share < timeout {
			timeout = share
		}
	}
	return timeout
}

func providerReason(err error) string {
	for _, known := range []error{errBudget, errAuthentication, errRateLimited} {
		if errors.Is(err, known) {
			return known.Error()
		}
	}
	return "provider_unavailable"
}

// This process-local cooldown prevents repeated failed requests. It is not a
// billing ledger: shared/monthly hard limits belong to the model gateway.
func (r *Router) cooldown(id, reason string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.blockedUntil == nil {
		r.blockedUntil = map[string]time.Time{}
		r.blockedReason = map[string]string{}
	}
	r.blockedUntil[id] = time.Now().Add(5 * time.Minute)
	r.blockedReason[id] = reason
}

func (r *Router) blocked(id string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if time.Now().Before(r.blockedUntil[id]) {
		return r.blockedReason[id]
	}
	return ""
}
