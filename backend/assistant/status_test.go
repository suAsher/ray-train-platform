package assistant

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestAdminBackendsReturnsSafeProcessLocalCooldown(t *testing.T) {
	r := &Router{backends: []backend{{id: "company", kind: "api", model: "model", protocol: "openai"}}}
	r.cooldown("company", "authentication_error")
	status := r.AdminBackends()
	if len(status) != 1 || status[0].Reason != "authentication_error" || status[0].CooldownUntil == nil || !status[0].CooldownUntil.After(time.Now()) {
		t.Fatalf("status=%+v", status)
	}
	raw, _ := json.Marshal(status)
	if strings.Contains(string(raw), "baseURL") || strings.Contains(string(raw), "apiKey") {
		t.Fatal("unsafe provider metadata")
	}
	status[0].Model = "mutated"
	if r.AdminBackends()[0].Model != "model" {
		t.Fatal("status aliases router")
	}
	r.blockedUntil["company"] = time.Now().Add(-time.Second)
	if s := r.AdminBackends()[0]; s.CooldownUntil != nil || s.Reason != "not_configured" {
		t.Fatalf("expired=%+v", s)
	}
}
