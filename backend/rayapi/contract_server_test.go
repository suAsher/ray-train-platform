package rayapi

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"sync"
	"testing"

	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/observability"
)

type contractTailLogs struct {
	mu      sync.Mutex
	queries int
}

func (logs *contractTailLogs) QueryJobLogs(_ context.Context, _ string, _ int) ([]observability.LogLine, error) {
	logs.mu.Lock()
	defer logs.mu.Unlock()
	logs.queries++
	lines := []observability.LogLine{{Line: "ray-sdk-log-marker"}}
	if logs.queries >= 3 {
		lines = append(lines, observability.LogLine{Line: "ray-sdk-log-second-marker"})
	}
	return lines, nil
}

// This is launched only by the manually orchestrated pinned-Ray SDK contract
// command. It keeps the SDK-facing surface on the host network without making
// a test-only server part of the production binary.
func TestRay235ContractServer(t *testing.T) {
	if os.Getenv("RAYAPI_CONTRACT_SERVER") != "1" {
		t.Skip("set RAYAPI_CONTRACT_SERVER=1 to launch the external SDK contract server")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:18080")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	principal := auth.Principal{Subject: "ray-sdk-user", TenantID: "ray-sdk-tenant", Roles: []string{"Engineer"}, AuthType: auth.AuthTypeOIDC}
	server := &http.Server{Handler: recoveryRouter(t, &rayTestRepository{}, &recoveryStore{}, &contractTailLogs{}, principal)}
	fmt.Println("RAYAPI_CONTRACT_SERVER_READY=http://127.0.0.1:18080/ray")
	if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
		t.Fatal(err)
	}
}
