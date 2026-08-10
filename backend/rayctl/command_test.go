package rayctl

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestRunPackageWritesDeterministicArchiveWithoutCredentials(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "train.py"), []byte("print('train')\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(t.TempDir(), "package.zip")
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if err := Run(context.Background(), []string{"package", "--dir", root, "--output", output}, &stdout, &stderr, func(string) string { return "secret-token" }); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(output)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 || stdout.Len() == 0 || bytes.Contains(stdout.Bytes(), []byte("secret-token")) || stderr.Len() != 0 {
		t.Fatalf("output mode=%o stdout=%q stderr=%q", info.Mode().Perm(), stdout.String(), stderr.String())
	}
}

func TestRunLoginCheckDebugRedactsCredential(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writeClientSuccess(t, writer, http.StatusOK, map[string]any{"items": []any{}})
	}))
	defer server.Close()
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	caFile := writeTestCA(t, server)
	if err := Run(context.Background(), []string{"login-check", "--server", server.URL, "--ca-file", caFile, "--debug"}, &stdout, &stderr, func(key string) string {
		if key == "RAY_PLATFORM_TOKEN" {
			return "secret-token"
		}
		return ""
	}); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(stderr.Bytes(), []byte("GET")) || bytes.Contains(stderr.Bytes(), []byte("secret-token")) {
		t.Fatalf("debug output=%q", stderr.String())
	}
}

func TestRunRejectsUnknownAndTokenFlags(t *testing.T) {
	for _, arguments := range [][]string{
		{"unknown"},
		{"login-check", "--token", "secret-token"},
	} {
		if err := Run(context.Background(), arguments, &bytes.Buffer{}, &bytes.Buffer{}, func(string) string { return "" }); err == nil {
			t.Fatalf("arguments %q were accepted", arguments)
		}
	}
}
