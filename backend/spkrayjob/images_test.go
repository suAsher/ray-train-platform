package spkrayjob

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestImagesCommand(t *testing.T) {
	for _, format := range []string{"text", "json"} {
		t.Run(format, func(t *testing.T) {
			calls := 0
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != http.MethodGet || r.URL.RequestURI() != "/api/v1/images?kind=training" {
					t.Errorf("unexpected request %s %s", r.Method, r.URL)
				}
				if r.Header.Get("Authorization") == "" {
					t.Error("missing authentication")
				}
				writeClientSuccess(t, w, http.StatusOK, []any{map[string]any{"name": "custom", "reference": "harbor.other.example/team/train:v1", "description": "适合分类", "rayVersion": "2.35.0", "supportedEngines": []string{"ray-ddp"}, "environment": map[string]string{"python": "3.11", "dependencies": "numpy==1.26.4", "validationNotes": "GPU 未验证"}}})
			}))
			defer server.Close()
			var out bytes.Buffer
			err := Run(context.Background(), []string{"images", "--server", server.URL, "--ca-file", writeTestCA(t, server), "--output", format}, &out, &bytes.Buffer{}, testEnvironment)
			if err != nil {
				t.Fatal(err)
			}
			if calls != 1 {
				t.Fatalf("calls=%d", calls)
			}
			for _, want := range []string{"custom", "harbor.other.example/team/train:v1", "适合分类", "2.35.0", "ray-ddp", "3.11", "numpy==1.26.4", "GPU 未验证"} {
				if !strings.Contains(out.String(), want) {
					t.Fatalf("missing %q: %s", want, out.String())
				}
			}
			if format == "json" {
				var data []map[string]any
				if err := json.Unmarshal(out.Bytes(), &data); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestImagesRejectsInvalidArgumentsBeforeConnecting(t *testing.T) {
	for _, args := range [][]string{{"images", "extra"}, {"images", "--output", "xml"}} {
		if err := Run(context.Background(), args, &bytes.Buffer{}, &bytes.Buffer{}, func(string) string { return "" }); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}

func TestImagesHonorsRequestCancellation(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("cancelled command reached server") }))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var out bytes.Buffer
	err := Run(ctx, []string{"images", "--server", server.URL, "--ca-file", writeTestCA(t, server)}, &out, &bytes.Buffer{}, testEnvironment)
	if err == nil || out.Len() != 0 {
		t.Fatalf("cancelled request output=%q err=%v", out.String(), err)
	}
}

func TestRenderImagesEmptyAndTerminalSafety(t *testing.T) {
	var out bytes.Buffer
	if err := renderImages(&out, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "管理员") {
		t.Fatal("empty list lacks next action")
	}
	out.Reset()
	if err := renderImages(&out, []catalogImage{{Name: "custom\x1b[2J", Description: "hello\nworld\x00"}}); err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(out.String(), "\x1b\x00") {
		t.Fatal("terminal control code leaked")
	}
}
