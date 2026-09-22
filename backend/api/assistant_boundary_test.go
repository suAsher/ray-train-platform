package api

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestAssistantRedactsCompleteQuotedCredentialValues(t *testing.T) {
	for _, input := range []string{
		`password="two words" next=visible`,
		`password='two words' next=visible`,
		`{"password":"two,words;more", "next":"visible"}`,
		`api_key="two\"words;more" next=visible`,
		`secret='two\'words,more' next=visible`,
		`token=two next=visible`,
		`token=two,next=visible`,
		`token=two;next=visible`,
	} {
		got := assistantRedact(input)
		for _, secret := range []string{"two", "words", "more"} {
			if strings.Contains(got, secret) {
				t.Fatalf("credential fragment %q survived redaction: %q", secret, got)
			}
		}
		if !strings.Contains(got, "visible") || !strings.Contains(got, "[已脱敏]") {
			t.Fatalf("redaction damaged unrelated fields: %q", got)
		}
	}
}

// Recorder requests have finite in-memory bodies. Production must instead
// enforce the deadline on the actual HTTP transport, or reject the request.
type assistantDeadlineRecorder struct{ *httptest.ResponseRecorder }

func (w assistantDeadlineRecorder) SetReadDeadline(time.Time) error { return nil }

type assistantBodyReadSignal struct {
	io.ReadCloser
	once    sync.Once
	started chan<- struct{}
}

func (b *assistantBodyReadSignal) Read(p []byte) (int, error) {
	b.once.Do(func() { b.started <- struct{}{} })
	return b.ReadCloser.Read(p)
}

func TestAssistantSlowBodiesReleaseQuerySlots(t *testing.T) {
	h := assistantTestHandler()
	started := make(chan struct{}, 2)
	finished := make(chan struct{}, 2)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("ray-platform-principal", *assistantTestPrincipal())
		if c.GetHeader("X-Test-Slow-Body") == "true" {
			c.Request.Body = &assistantBodyReadSignal{ReadCloser: c.Request.Body, started: started}
			c.Next()
			finished <- struct{}{}
		}
	})
	h.RegisterAssistantRoutes(r.Group("/api/v1"))
	server := httptest.NewServer(r)
	defer server.Close()
	server.Client().Timeout = 2 * time.Second
	var connections []net.Conn
	for i := 0; i < 2; i++ {
		conn, err := net.DialTimeout("tcp", strings.TrimPrefix(server.URL, "http://"), time.Second)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		connections = append(connections, conn)
		if _, err := fmt.Fprintf(conn, "POST /api/v1/assistant/query HTTP/1.1\r\nHost: test\r\nContent-Type: application/json\r\nContent-Length: 1000\r\nX-Test-Slow-Body: true\r\n\r\n{\"question\":\""); err != nil {
			t.Fatal(err)
		}
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("slow body did not reach query reader")
		}
	}
	post := func() int {
		response, err := server.Client().Post(server.URL+"/api/v1/assistant/query", "application/json", strings.NewReader(`{"question":"日志","mode":"docs"}`))
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		_, _ = io.Copy(io.Discard, response.Body)
		return response.StatusCode
	}
	if got := post(); got != http.StatusTooManyRequests {
		t.Fatalf("expected occupied slots, got %d", got)
	}
	for _, conn := range connections {
		if err := conn.SetReadDeadline(time.Now().Add(8 * time.Second)); err != nil {
			t.Fatal(err)
		}
		response, err := http.ReadResponse(bufio.NewReader(conn), nil)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, response.Body)
		response.Body.Close()
		if response.StatusCode != http.StatusRequestTimeout {
			t.Fatalf("expected slow-body timeout, got %d", response.StatusCode)
		}
	}
	for range connections {
		select {
		case <-finished:
		case <-time.After(time.Second):
			t.Fatal("timeout handler did not release its guards")
		}
	}
	if got := post(); got != http.StatusOK {
		t.Fatalf("body timeout did not release slots: %d", got)
	}
}

func TestAssistantRejectsUnsupportedReadDeadlineBeforeReading(t *testing.T) {
	r := assistantTestRouter(assistantTestHandler(), assistantTestPrincipal())
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/assistant/query", strings.NewReader(`{"question":"日志"}`))
	req.Header.Set("Content-Type", "application/json")
	if err := http.NewResponseController(w).SetReadDeadline(time.Now()); !errors.Is(err, http.ErrNotSupported) {
		t.Fatal("test recorder unexpectedly supports deadlines")
	}
	r.ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "ASSISTANT_TRANSPORT_UNAVAILABLE") {
		t.Fatalf("unsupported transport was accepted: %d %s", w.Code, w.Body.String())
	}
}

func TestAssistantRedactsTruncatedQuotedCredentialValues(t *testing.T) {
	for _, quote := range []string{`"`, `'`} {
		for _, tail := range []string{"", `\`, "\n", "\r\n", "\\\n", "\nremaining-secret", "\r\nremaining-secret", `\` + quote + "remaining-secret"} {
			input := "password=" + quote + "correct horse" + tail
			if got := assistantRedact(input); got != "password=[已脱敏]" {
				t.Fatalf("truncated quoted credential was not fully redacted: quote=%q tail=%q result=%q", quote, tail, got)
			}
		}
	}
}
