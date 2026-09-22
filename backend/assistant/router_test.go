package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

type fakeProvider struct {
	calls int
	answer string
	err error
	run func(context.Context) error
}

func (p *fakeProvider) complete(ctx context.Context, _ Input) (string, error) {
	p.calls++
	if p.run != nil {
		return "", p.run(ctx)
	}
	return p.answer, p.err
}

func testRouter(providers ...*fakeProvider) *Router {
	kinds := []string{"api", "api", "local", "local"}
	r := &Router{}
	for i, p := range providers {
		r.backends = append(r.backends, backend{id: string(rune('a' + i)), kind: kinds[i], provider: p})
	}
	return r
}

func TestRoutingModesAndFallback(t *testing.T) {
	for _, tc := range []struct {
		mode string
		want string
		calls [3]int
	}{
		{"docs", "docs", [3]int{0, 0, 0}},
		{"api", "api", [3]int{1, 1, 0}},
		{"local", "local", [3]int{0, 0, 1}},
		{"auto", "api", [3]int{1, 1, 0}},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			a := &fakeProvider{err: errBudget}
			b := &fakeProvider{answer: "second company"}
			c := &fakeProvider{answer: "local"}
			got, err := testRouter(a, b, c).Answer(context.Background(), tc.mode, Input{Question: "question"})
			if err != nil || got.Mode != tc.want || [3]int{a.calls, b.calls, c.calls} != tc.calls {
				t.Fatalf("result=%+v error=%v calls=%d/%d/%d", got, err, a.calls, b.calls, c.calls)
			}
			if tc.mode == "api" || tc.mode == "auto" {
				if got.BackendID != "b" || got.Reason != "fallback_budget_exhausted" {
					t.Fatal(got)
				}
			}
		})
	}
}

func TestExplicitModeNeverCrossesKind(t *testing.T) {
	for _, mode := range []string{"api", "local"} {
		a, b, c := &fakeProvider{err: errUnavailable}, &fakeProvider{err: errBudget}, &fakeProvider{err: errAuthentication}
		r := testRouter(a, b, c)
		got, err := r.Answer(context.Background(), mode, Input{})
		if err != nil || got.Mode != "docs" {
			t.Fatal(got, err)
		}
		if mode == "api" && c.calls != 0 || mode == "local" && a.calls+b.calls != 0 {
			t.Fatal("cross-kind request", a.calls, b.calls, c.calls)
		}
	}
}

func TestNoProviderAndLocalFirst(t *testing.T) {
	r, err := NewRouter(Config{})
	if err != nil {
		t.Fatal(err)
	}
	got, err := r.Answer(context.Background(), "auto", Input{})
	if err != nil || got.Mode != "docs" || got.Reason != "not_configured" {
		t.Fatal(got, err)
	}
	a, b, c := &fakeProvider{answer: "api"}, &fakeProvider{answer: "api2"}, &fakeProvider{answer: "local"}
	r = testRouter(a, b, c)
	r.localFirst = true
	got, err = r.Answer(context.Background(), "auto", Input{})
	if err != nil || got.Mode != "local" || a.calls+b.calls != 0 {
		t.Fatal(got, err, a.calls, b.calls)
	}
}

func TestCanceledRequestDoesNotCallFallback(t *testing.T) {
	for _, preCanceled := range []bool{true, false} {
		a, b := &fakeProvider{err: context.Canceled}, &fakeProvider{answer: "fallback"}
		ctx, cancel := context.WithCancel(context.Background())
		if preCanceled {
			cancel()
		}
		_, err := testRouter(a, b).Answer(ctx, "auto", Input{})
		cancel()
		if !errors.Is(err, context.Canceled) || b.calls != 0 || preCanceled && a.calls != 0 {
			t.Fatal(err, a.calls, b.calls)
		}
	}
}

func TestCooldownIsPerBackendAndExpires(t *testing.T) {
	for _, failure := range []error{errBudget, errAuthentication, errRateLimited, errUnavailable} {
		a, b := &fakeProvider{err: failure}, &fakeProvider{answer: "fallback"}
		r := testRouter(a, b)
		for i := 0; i < 2; i++ {
			got, err := r.Answer(context.Background(), "api", Input{})
			if err != nil || got.BackendID != "b" || got.Reason != "fallback_"+failure.Error() {
				t.Fatal(got, err)
			}
		}
		if a.calls != 1 || b.calls != 2 {
			t.Fatal("cooldown affected a healthy sibling", a.calls, b.calls)
		}
		if remaining := time.Until(r.blockedUntil["a"]); remaining < 4*time.Minute || remaining > 5*time.Minute {
			t.Fatal("expected five-minute suppression", remaining)
		}
		r.blockedUntil["a"] = time.Now().Add(-time.Second)
		_, _ = r.Answer(context.Background(), "api", Input{})
		if a.calls != 2 {
			t.Fatal("cooldown never expired")
		}
	}
}

func TestAttemptsDivideRemainingTimeForFallback(t *testing.T) {
	a := &fakeProvider{run: func(ctx context.Context) error {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 12*time.Second {
			t.Error("unbounded attempt")
		}
		<-ctx.Done()
		return ctx.Err()
	}}
	b := &fakeProvider{answer: "fallback"}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	got, err := testRouter(a, b).Answer(ctx, "api", Input{})
	if err != nil || got.BackendID != "b" || b.calls != 1 {
		t.Fatal(got, err, b.calls)
	}
}

func TestInvalidModeAndInputNeverCallProvider(t *testing.T) {
	p := &fakeProvider{answer: "answer"}
	r := testRouter(p)
	if _, err := r.Answer(context.Background(), "unknown", Input{}); err == nil {
		t.Fatal("accepted unknown mode")
	}
	if _, err := r.Answer(context.Background(), "api", Input{Question: strings.Repeat("字", 4001)}); err == nil {
		t.Fatal("accepted oversized input")
	}
	if p.calls != 0 {
		t.Fatal("invalid request reached model")
	}
}

func TestCapabilitiesDoNotExposeCredentialsOrEndpoints(t *testing.T) {
	r, err := NewRouter(Config{Providers: []ProviderConfig{
		{ID: "company-a", Kind: "api", BaseURL: "https://example.com/v1", Model: "model-a", APIKey: "secret"},
		{ID: "company-b", Kind: "api", BaseURL: "https://example.org/v1", Model: "model-b", APIKey: "secret"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	caps := r.Capabilities()
	encoded, err := json.Marshal(caps)
	if err != nil || strings.Contains(string(encoded), "secret") || strings.Contains(string(encoded), "example.com") {
		t.Fatal("capabilities exposed provider configuration")
	}
	if !caps.Providers["api"].Configured || caps.Providers["local"].Configured || len(caps.Backends) != 2 || caps.Backends[1].ID != "company-b" {
		t.Fatal(caps)
	}
}

func TestProviderConfigurationValidation(t *testing.T) {
	valid := ProviderConfig{ID: "company", Kind: "api", BaseURL: "https://example.com/v1", Model: "test", APIKey: "test"}
	for _, address := range []string{"http://example.com/v1", "https://user:pass@example.com/v1", "https://example.com/v1?key=secret", "https://example.com/v1#fragment", "https://example.com/v1/../secret", "https://example.com/%2e%2e/secret", "https://example.com/v1?", "https://example.com//v1"} {
		cfg := valid
		cfg.BaseURL = address
		if err := ValidateConfig(Config{Providers: []ProviderConfig{cfg}}); err == nil {
			t.Fatal("accepted unsafe URL", address)
		}
	}
	for _, mutation := range []func(*ProviderConfig){
		func(p *ProviderConfig) { p.APIKey = "" },
		func(p *ProviderConfig) { p.ID = "" },
		func(p *ProviderConfig) { p.ID = "bad id" },
		func(p *ProviderConfig) { p.Kind = "anthropic" },
		func(p *ProviderConfig) { p.Model = "" },
		func(p *ProviderConfig) { p.APIKey = "key\r\nX: injected" },
	} {
		cfg := valid
		mutation(&cfg)
		if err := ValidateConfig(Config{Providers: []ProviderConfig{cfg}}); err == nil {
			t.Fatal("accepted invalid provider")
		}
	}
	if err := ValidateConfig(Config{Providers: []ProviderConfig{valid, valid}}); err == nil {
		t.Fatal("accepted duplicate ID")
	}
	if err := ValidateConfig(Config{Providers: make([]ProviderConfig, 5)}); err == nil {
		t.Fatal("accepted more than four backends")
	}
	local := ProviderConfig{ID: "local", Kind: "local", BaseURL: "http://model.ns.svc.cluster.local:8000/v1", Model: "local-model"}
	if err := ValidateConfig(Config{Providers: []ProviderConfig{local}}); err != nil {
		t.Fatal(err)
	}
	local.BaseURL = "http://model.ns.svc.cluster.local.evil.test/v1"
	if err := ValidateConfig(Config{Providers: []ProviderConfig{local}}); err == nil {
		t.Fatal("accepted external plaintext local backend")
	}
}

func TestAutoKeepsConfiguredOrderWithinEachKind(t *testing.T) {
	for _, localFirst := range []bool{false, true} {
		api1 := &fakeProvider{err: errBudget}
		api2 := &fakeProvider{err: errRateLimited}
		local1 := &fakeProvider{err: errAuthentication}
		local2 := &fakeProvider{answer: "local second"}
		r := &Router{localFirst: localFirst, backends: []backend{
			{id: "l1", kind: "local", provider: local1},
			{id: "a1", kind: "api", provider: api1},
			{id: "l2", kind: "local", provider: local2},
			{id: "a2", kind: "api", provider: api2},
		}}
		got, err := r.Answer(context.Background(), "auto", Input{})
		if err != nil || got.BackendID != "l2" || local1.calls != 1 || local2.calls != 1 {
			t.Fatal(got, err)
		}
		wantAPICalls := 1
		if localFirst {
			wantAPICalls = 0
		}
		if api1.calls != wantAPICalls || api2.calls != wantAPICalls {
			t.Fatal("incorrect kind preference", api1.calls, api2.calls)
		}
	}
}
