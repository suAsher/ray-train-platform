// Injected only into isolated RED/GREEN test workspaces. This accessor preserves
// New's actual server, handlers, and TLS config while allowing an ephemeral port.
package webserver

import "net/http"

func ObservabilityTestHTTPServer(server *Webserver) *http.Server {
	return server.server
}
