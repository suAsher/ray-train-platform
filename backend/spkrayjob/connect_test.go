package spkrayjob

import (
	"bytes"
	"context"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"golang.org/x/net/websocket"
)

func TestConnectWorkerUsesAuthenticatedWebSocketAndStreamsBytes(t *testing.T) {
	server := httptest.NewTLSServer(websocket.Handler(func(connection *websocket.Conn) {
		if connection.Request().Header.Get("Authorization") != "Bearer test-token" {
			t.Errorf("missing bearer token")
			return
		}
		_, _ = io.Copy(connection, connection)
	}))
	defer server.Close()
	client, err := NewClient(ClientOptions{ServerURL: server.URL, Token: "test-token", HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}

	var stdout bytes.Buffer
	if err := client.ConnectWorker(context.Background(), "job-a", 2, strings.NewReader("echo-ready\n"), &stdout); err != nil {
		t.Fatalf("connect worker: %v", err)
	}
	if stdout.String() != "echo-ready\n" {
		t.Fatalf("unexpected stream: %q", stdout.String())
	}
}

func TestRunConnectRejectsNegativeWorkerOrdinal(t *testing.T) {
	err := RunWithInput(context.Background(), []string{"connect", "--worker", "-1", "job-a"}, strings.NewReader(""), io.Discard, io.Discard, func(string) string { return "" })
	if err == nil || !strings.Contains(err.Error(), "worker") {
		t.Fatalf("expected worker validation error, got %v", err)
	}
}
