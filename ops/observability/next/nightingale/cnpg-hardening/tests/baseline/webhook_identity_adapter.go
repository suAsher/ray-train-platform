// This file is injected ONLY into the pristine upstream RED workspace.
// Upstream 1.30.1 has no source-auth helper: its webhook handler receives every
// source. This identity adapter makes that existing behavior testable without a
// missing-symbol compilation failure. It is forbidden in the GREEN workspace
// and in the image; GREEN must use the implementation from the reviewed patch.
package observabilityauth

import (
	"net/http"

	"sigs.k8s.io/controller-runtime/pkg/webhook"
)

func NewWebhookHandler(handler http.Handler, _ string) (http.Handler, error) {
	return handler, nil
}

// The baseline uses the upstream DefaultServer and exposes its unguarded mux.
// These names are adapters only; no allowlist behavior is introduced here.
type WebhookServer struct {
	*webhook.DefaultServer
	mux *http.ServeMux
}

func NewWebhookServer(options webhook.Options, _ string) (*WebhookServer, error) {
	mux := http.NewServeMux()
	options.WebhookMux = mux
	return &WebhookServer{DefaultServer: webhook.NewServer(options).(*webhook.DefaultServer), mux: mux}, nil
}

func (s *WebhookServer) WebhookMux() *http.ServeMux { return s.mux }
