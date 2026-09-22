package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// Decode protocol through the public configuration shape so these tests also
// produce behavioral failures against the former OpenAI-only implementation.
func protocolConfig(t *testing.T, protocol string) ProviderConfig {
	t.Helper()
	encoded, err := json.Marshal(map[string]string{
		"id": "vendor", "kind": "api", "baseUrl": "https://example.com/v1",
		"model": "configured-model", "protocol": protocol,
	})
	if err != nil {
		t.Fatal(err)
	}
	var cfg ProviderConfig
	if err := json.Unmarshal(encoded, &cfg); err != nil {
		t.Fatal(err)
	}
	cfg.APIKey = "test-vendor-key"
	return cfg
}

func protocolHTTPProvider(t *testing.T, server *httptest.Server, protocol string) *httpProvider {
	t.Helper()
	cfg := protocolConfig(t, protocol)
	cfg.BaseURL = server.URL
	p, err := newHTTPProvider(cfg)
	if err != nil {
		t.Fatal(err)
	}
	hp := p.(*httpProvider)
	hp.client.Transport.(*http.Transport).TLSClientConfig = server.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
	return hp
}

func TestAnthropicNativeRequestAndTextOnlyResponse(t *testing.T) {
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" || r.Method != http.MethodPost {
			t.Error("wrong Anthropic method or endpoint", r.Method, r.URL.Path)
		}
		if r.Header.Get("x-api-key") != "test-vendor-key" || r.Header.Get("anthropic-version") != "2023-06-01" || r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Error("Anthropic authentication/version contract violated")
		}
		var body struct {
			Model string `json:"model"`
			System string `json:"system"`
			Messages []struct { Role, Content string } `json:"messages"`
			MaxTokens int `json:"max_tokens"`
			Stream bool `json:"stream"`
			Thinking json.RawMessage `json:"thinking"`
			Tools json.RawMessage `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error("invalid request", err)
		}
		if body.Model != "configured-model" || body.System != systemPrompt || len(body.Messages) != 1 || body.MaxTokens != 1500 || body.Stream || len(body.Thinking) != 0 || len(body.Tools) != 0 {
			t.Error("wrong native Messages payload")
		}
		if len(body.Messages) == 1 && (body.Messages[0].Role != "user" || !strings.Contains(body.Messages[0].Content, `"id":"doc:42"`)) {
			t.Error("native Messages evidence/user-role contract violated")
		}
		_, _ = w.Write([]byte(`{"type":"message","role":"assistant","content":[{"type":"thinking","thinking":"hidden reasoning","text":"do not expose"},{"type":"text","text":"第一段 [doc:42]"},{"type":"tool_use","name":"shell","input":{"command":"danger"},"text":"do not expose tool"},{"type":"text","text":"第二段"},{"type":"redacted_thinking","data":"hidden"}],"usage":{"input_tokens":20,"output_tokens":10}}`))
	}))
	defer s.Close()
	answer, err := protocolHTTPProvider(t, s, "anthropic").complete(context.Background(), Input{Question: "why", Evidence: []Evidence{{ID: "doc:42", Excerpt: "evidence"}}})
	if err != nil || answer != "第一段 [doc:42]\n第二段" {
		t.Fatal(answer, err)
	}
}

func TestProtocolEndpointsAndValidation(t *testing.T) {
	for _, tc := range []struct { protocol, baseURL, want string }{
		{"", "https://example.com", "https://example.com/v1/chat/completions"},
		{"openai", "https://example.com/v1/", "https://example.com/v1/chat/completions"},
		{"anthropic", "https://example.com", "https://example.com/v1/messages"},
		{"anthropic", "https://example.com/v1/", "https://example.com/v1/messages"},
		{"anthropic", "https://example.com/gateway/v1", "https://example.com/gateway/v1/messages"},
	} {
		cfg := protocolConfig(t, tc.protocol)
		cfg.BaseURL = tc.baseURL
		got, err := providerEndpoint(cfg)
		if err != nil || got != tc.want {
			t.Fatalf("protocol=%q endpoint=%q err=%v", tc.protocol, got, err)
		}
	}
	for _, name := range []string{"gemini", "ANTHROPIC", "anthropic ", "responses"} {
		if err := ValidateConfig(Config{Providers: []ProviderConfig{protocolConfig(t, name)}}); err == nil {
			t.Error("accepted unsupported protocol", name)
		}
	}
	cfg := protocolConfig(t, "anthropic")
	cfg.ThinkingDisabled = true
	if err := ValidateConfig(Config{Providers: []ProviderConfig{cfg}}); err == nil {
		t.Error("accepted OpenAI thinking extension for Anthropic")
	}
	cfg.ThinkingDisabled = false
	cfg.BaseURL = "http://example.com/v1"
	if err := ValidateConfig(Config{Providers: []ProviderConfig{cfg}}); err == nil {
		t.Error("native protocol bypassed endpoint TLS boundary")
	}
}

func TestProtocolMetadataDefaultsToOpenAI(t *testing.T) {
	for _, protocol := range []string{"", "openai", "anthropic"} {
		r, err := NewRouter(Config{Providers: []ProviderConfig{protocolConfig(t, protocol)}})
		if err != nil {
			t.Fatal(err)
		}
		data, err := json.Marshal(r.Capabilities())
		want := protocol
		if want == "" {
			want = "openai"
		}
		if err != nil || !strings.Contains(string(data), `"protocol":"`+want+`"`) || strings.Contains(string(data), "test-vendor-key") || strings.Contains(string(data), "example.com") {
			t.Fatal("invalid protocol capability metadata", string(data), err)
		}
	}
}

func TestAnthropicRejectsUnusableResponseWithoutDisclosure(t *testing.T) {
	for _, tc := range []struct { name, body string; valid bool }{
		{"no-usage-needed", `{"type":"message","role":"assistant","content":[{"type":"text","text":"answer"}]}`, true},
		{"thinking-only", `{"type":"message","role":"assistant","content":[{"type":"thinking","thinking":"private","text":"private"}]}`, false},
		{"tool-only", `{"type":"message","role":"assistant","content":[{"type":"tool_use","text":"private","name":"shell"}]}`, false},
		{"empty", `{"type":"message","role":"assistant","content":[]}`, false},
		{"openai-envelope", `{"choices":[{"message":{"content":"wrong protocol"}}]}`, false},
		{"non-json", `private upstream error`, false},
		{"key-echo", `{"type":"message","role":"assistant","content":[{"type":"text","text":"test-vendor-key"}]}`, false},
		{"long-text", `{"type":"message","role":"assistant","content":[{"type":"text","text":"`+strings.Repeat("字", 12001)+`"}]}`, false},
		{"oversize", strings.Repeat("x", maxPayloadBytes+1), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(tc.body)) }))
			defer s.Close()
			answer, err := protocolHTTPProvider(t, s, "anthropic").complete(context.Background(), Input{})
			if tc.valid {
				if err != nil || answer != "answer" { t.Fatal(answer, err) }
			} else if answer != "" || !errors.Is(err, errUnavailable) {
				t.Fatal("unsafe or unusable native response was not rejected")
			}
		})
	}
}

func TestMixedProtocolFallbackPreservesAuthenticationIsolation(t *testing.T) {
	var anthropicCalls, openaiCalls atomic.Int32
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/anthropic/v1/messages":
			anthropicCalls.Add(1)
			if r.Header.Get("x-api-key") != "test-vendor-key" || r.Header.Get("Authorization") != "" { t.Error("native credential mixed") }
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"type":"error","error":{"type":"authentication_error","message":"test-vendor-key private"}}`))
		case "/openai/v1/chat/completions":
			openaiCalls.Add(1)
			if r.Header.Get("Authorization") != "Bearer sibling-key" || r.Header.Get("x-api-key") != "" || r.Header.Get("anthropic-version") != "" { t.Error("OpenAI credential mixed") }
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"fallback answer"}}]}`))
		default:
			t.Error("unexpected endpoint", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer s.Close()
	a, b := protocolConfig(t, "anthropic"), protocolConfig(t, "openai")
	a.BaseURL = s.URL + "/anthropic/v1"
	b.ID, b.APIKey, b.BaseURL = "sibling", "sibling-key", s.URL+"/openai/v1"
	r, err := NewRouter(Config{Providers: []ProviderConfig{a, b}})
	if err != nil { t.Fatal(err) }
	for _, backend := range r.backends {
		backend.provider.(*httpProvider).client.Transport.(*http.Transport).TLSClientConfig = s.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
	}
	for i := 0; i < 2; i++ {
		got, err := r.Answer(context.Background(), "api", Input{})
		if err != nil || got.Answer != "fallback answer" || got.BackendID != "sibling" || got.Reason != "fallback_authentication_error" { t.Fatal(got, err) }
	}
	if anthropicCalls.Load() != 1 || openaiCalls.Load() != 2 { t.Fatal("mixed-protocol cooldown failed") }
}

func TestAnthropicRedirectCannotForwardAPIKey(t *testing.T) {
	var forwarded atomic.Int32
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { forwarded.Add(1) }))
	defer target.Close()
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", target.URL)
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer s.Close()
	if _, err := protocolHTTPProvider(t, s, "anthropic").complete(context.Background(), Input{}); !errors.Is(err, errUnavailable) || forwarded.Load() != 0 {
		t.Fatal("native redirect was not safely rejected", err)
	}
}

func TestAnthropicSpendLimitErrorsAreNotOrdinaryBadRequests(t *testing.T) {
	for _, tc := range []struct { body string; want error }{
		{`{"type":"error","error":{"type":"invalid_request_error","message":"Your credit balance is too low to access the Anthropic API."}}`, errBudget},
		{`{"type":"error","error":{"type":"invalid_request_error","message":"You have reached your monthly spend limit."}}`, errBudget},
		{`{"type":"error","error":{"type":"invalid_request_error","message":"Your workspace spend limit has been exceeded."}}`, errBudget},
		{`{"type":"error","error":{"type":"invalid_request_error","message":"max_tokens must be greater than zero"}}`, errUnavailable},
		{`{"type":"error","error":{"type":"invalid_request_error","message":"Review spend limit documentation for this invalid parameter"}}`, errUnavailable},
	} {
		if got := classifyError(http.StatusBadRequest, []byte(tc.body)); got != tc.want {
			t.Fatalf("bad request classification=%v want=%v", got, tc.want)
		}
	}
}
