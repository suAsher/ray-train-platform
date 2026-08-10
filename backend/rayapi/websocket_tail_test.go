package rayapi

import (
	"bufio"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/observability"
)

func websocketTailRequest(t *testing.T, router http.Handler, path string) (int, string) {
	t.Helper()
	server := httptest.NewServer(router)
	defer server.Close()
	endpoint, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	connection, err := net.Dial("tcp", endpoint.Host)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if _, err := fmt.Fprintf(connection, "GET %s HTTP/1.1\r\nHost: %s\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n\r\n", path, endpoint.Host); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(connection)
	response, err := http.ReadResponse(reader, &http.Request{Method: http.MethodGet})
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusSwitchingProtocols {
		return response.StatusCode, ""
	}
	return response.StatusCode, readWebSocketTextFrame(t, reader)
}

func readWebSocketTextFrame(t *testing.T, reader *bufio.Reader) string {
	t.Helper()
	first, err := reader.ReadByte()
	if err != nil {
		t.Fatal(err)
	}
	second, err := reader.ReadByte()
	if err != nil {
		t.Fatal(err)
	}
	if first&0x0f != 0x1 || second&0x80 != 0 {
		t.Fatalf("unexpected WebSocket frame: first=%x second=%x", first, second)
	}
	length := int64(second & 0x7f)
	if length == 126 {
		var value uint16
		if err := binary.Read(reader, binary.BigEndian, &value); err != nil {
			t.Fatal(err)
		}
		length = int64(value)
	}
	if length == 127 {
		var value uint64
		if err := binary.Read(reader, binary.BigEndian, &value); err != nil {
			t.Fatal(err)
		}
		length = int64(value)
	}
	if length < 0 || length > 1024*1024 {
		t.Fatalf("unexpected WebSocket payload size: %d", length)
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(reader, payload); err != nil {
		t.Fatal(err)
	}
	return string(payload)
}

func TestRayTailLogsUseOwnerScopedWebSocketTextFrames(t *testing.T) {
	principal := auth.Principal{Subject: "user-a", TenantID: "tenant-a", Roles: []string{"Engineer"}, AuthType: auth.AuthTypeOIDC}
	repository := &rayTestRepository{}
	store := &recoveryStore{}
	logs := &recoveryLogs{}
	router := recoveryRouter(t, repository, store, logs, principal)
	packageName := testPackageSHA256 + ".zip"
	if response := recoveryRequest(router, http.MethodPut, "/ray/api/packages/gcs/"+packageName, []byte("PK\x03\x04tail")); response.Code != http.StatusOK {
		t.Fatalf("upload status=%d body=%s", response.Code, response.Body.String())
	}
	if response := recoveryRequest(router, http.MethodPost, "/ray/api/jobs/", []byte(raySubmitBody(packageName))); response.Code != http.StatusOK {
		t.Fatalf("submit status=%d body=%s", response.Code, response.Body.String())
	}
	logs.lines = []observability.LogLine{{Line: "ray-tail-websocket-marker"}}
	status, payload := websocketTailRequest(t, router, "/ray/api/jobs/raysubmit_test/logs/tail")
	if status != http.StatusSwitchingProtocols || payload != "ray-tail-websocket-marker\n" || logs.queriedJobID != "job-ray" {
		t.Fatalf("owner tail status=%d payload=%q queried=%q", status, payload, logs.queriedJobID)
	}
	other := auth.Principal{Subject: "user-b", TenantID: "tenant-b", Roles: []string{"Engineer"}, AuthType: auth.AuthTypeOIDC}
	otherRouter := recoveryRouter(t, repository, store, logs, other)
	status, payload = websocketTailRequest(t, otherRouter, "/ray/api/jobs/raysubmit_test/logs/tail")
	if status != http.StatusNotFound || payload != "" {
		t.Fatalf("cross-tenant tail status=%d payload=%q", status, payload)
	}
}

type appendableTailLogs struct {
	mu    sync.RWMutex
	lines []observability.LogLine
}

func (logs *appendableTailLogs) QueryJobLogs(_ context.Context, _ string, _ int) ([]observability.LogLine, error) {
	logs.mu.RLock()
	defer logs.mu.RUnlock()
	return append([]observability.LogLine(nil), logs.lines...), nil
}

func (logs *appendableTailLogs) append(line string) {
	logs.mu.Lock()
	defer logs.mu.Unlock()
	logs.lines = append(logs.lines, observability.LogLine{Line: line})
}

func TestRayTailLogsStreamsSubsequentTextFramesUntilClientCloses(t *testing.T) {
	principal := auth.Principal{Subject: "user-a", TenantID: "tenant-a", Roles: []string{"Engineer"}, AuthType: auth.AuthTypeOIDC}
	repository := &rayTestRepository{}
	store := &recoveryStore{}
	logs := &appendableTailLogs{lines: []observability.LogLine{{Line: "first-log-line"}}}
	router := recoveryRouter(t, repository, store, logs, principal)
	packageName := testPackageSHA256 + ".zip"
	if response := recoveryRequest(router, http.MethodPut, "/ray/api/packages/gcs/"+packageName, []byte("PK\x03\x04tail-stream")); response.Code != http.StatusOK {
		t.Fatalf("upload status=%d body=%s", response.Code, response.Body.String())
	}
	if response := recoveryRequest(router, http.MethodPost, "/ray/api/jobs/", []byte(raySubmitBody(packageName))); response.Code != http.StatusOK {
		t.Fatalf("submit status=%d body=%s", response.Code, response.Body.String())
	}

	server := httptest.NewServer(router)
	defer server.Close()
	endpoint, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	connection, err := net.Dial("tcp", endpoint.Host)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if _, err := fmt.Fprintf(connection, "GET /ray/api/jobs/raysubmit_test/logs/tail HTTP/1.1\r\nHost: %s\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n\r\n", endpoint.Host); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(connection)
	response, err := http.ReadResponse(reader, &http.Request{Method: http.MethodGet})
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("tail upgrade status=%d", response.StatusCode)
	}
	if payload := readWebSocketTextFrame(t, reader); payload != "first-log-line\n" {
		t.Fatalf("first tail payload=%q", payload)
	}

	logs.append("second-log-line")
	if err := connection.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if payload := readWebSocketTextFrame(t, reader); payload != "second-log-line\n" {
		t.Fatalf("second tail payload=%q", payload)
	}
}

func TestRayTailLogsRepliesWithCloseFrameWhenClientCloses(t *testing.T) {
	principal := auth.Principal{Subject: "user-a", TenantID: "tenant-a", Roles: []string{"Engineer"}, AuthType: auth.AuthTypeOIDC}
	repository := &rayTestRepository{}
	store := &recoveryStore{}
	logs := &appendableTailLogs{lines: []observability.LogLine{{Line: "close-log-line"}}}
	router := recoveryRouter(t, repository, store, logs, principal)
	packageName := testPackageSHA256 + ".zip"
	if response := recoveryRequest(router, http.MethodPut, "/ray/api/packages/gcs/"+packageName, []byte("PK\x03\x04tail-close")); response.Code != http.StatusOK {
		t.Fatalf("upload status=%d body=%s", response.Code, response.Body.String())
	}
	if response := recoveryRequest(router, http.MethodPost, "/ray/api/jobs/", []byte(raySubmitBody(packageName))); response.Code != http.StatusOK {
		t.Fatalf("submit status=%d body=%s", response.Code, response.Body.String())
	}

	connection, reader, cleanup := openWebsocketTail(t, router, "/ray/api/jobs/raysubmit_test/logs/tail")
	defer cleanup()
	if payload := readWebSocketTextFrame(t, reader); payload != "close-log-line\n" {
		t.Fatalf("first tail payload=%q", payload)
	}
	if _, err := connection.Write([]byte{0x88, 0x80, 0, 0, 0, 0}); err != nil {
		t.Fatal(err)
	}
	if err := connection.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	readWebSocketCloseFrame(t, reader)
}

func openWebsocketTail(t *testing.T, router http.Handler, path string) (net.Conn, *bufio.Reader, func()) {
	t.Helper()
	server := httptest.NewServer(router)
	endpoint, err := url.Parse(server.URL)
	if err != nil {
		server.Close()
		t.Fatal(err)
	}
	connection, err := net.Dial("tcp", endpoint.Host)
	if err != nil {
		server.Close()
		t.Fatal(err)
	}
	if _, err := fmt.Fprintf(connection, "GET %s HTTP/1.1\r\nHost: %s\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n\r\n", path, endpoint.Host); err != nil {
		_ = connection.Close()
		server.Close()
		t.Fatal(err)
	}
	reader := bufio.NewReader(connection)
	response, err := http.ReadResponse(reader, &http.Request{Method: http.MethodGet})
	if err != nil {
		_ = connection.Close()
		server.Close()
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusSwitchingProtocols {
		_ = response.Body.Close()
		_ = connection.Close()
		server.Close()
		t.Fatalf("tail upgrade status=%d", response.StatusCode)
	}
	return connection, reader, func() {
		_ = connection.Close()
		server.Close()
	}
}

func readWebSocketCloseFrame(t *testing.T, reader *bufio.Reader) {
	t.Helper()
	first, err := reader.ReadByte()
	if err != nil {
		t.Fatal(err)
	}
	second, err := reader.ReadByte()
	if err != nil {
		t.Fatal(err)
	}
	if first&0x0f != 0x8 || second != 0 {
		t.Fatalf("unexpected WebSocket close frame: first=%x second=%x", first, second)
	}
}
