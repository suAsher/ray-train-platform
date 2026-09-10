package spkrayjob

import (
	"bytes"
	"context"
	"io"
	"net/http/httptest"
	"path/filepath"
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
		buffer := make([]byte, len("echo-ready\n"))
		_, _ = io.ReadFull(connection, buffer)
		_, _ = connection.Write(buffer)
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

func TestRunConnectAcceptsDocumentedFlagsAfterJobID(t *testing.T) {
	err := RunWithInput(
		context.Background(),
		[]string{"connect", "job-a", "--worker", "2", "--config", filepath.Join(t.TempDir(), "missing.json")},
		strings.NewReader(""), io.Discard, io.Discard, func(string) string { return "" },
	)
	if err == nil {
		t.Fatal("expected the missing config to stop the command")
	}
	if strings.Contains(err.Error(), "connect requires") {
		t.Fatalf("documented job-first syntax was rejected before connection setup: %v", err)
	}
}

func TestRunConnectHelpExplainsWorkerSelection(t *testing.T) {
	var stdout bytes.Buffer
	if err := RunWithInput(context.Background(), []string{"connect", "--help"}, strings.NewReader(""), &stdout, io.Discard, func(string) string { return "" }); err != nil {
		t.Fatalf("connect help: %v", err)
	}
	for _, expected := range []string{"spk-rayjob connect JOB_ID", "--worker"} {
		if !strings.Contains(stdout.String(), expected) {
			t.Fatalf("help missing %q: %s", expected, stdout.String())
		}
	}
}
