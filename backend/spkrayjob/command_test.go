package spkrayjob

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ray-train-platform-backend/domain"
)

func TestRunHelpUsesProductNeutralTitle(t *testing.T) {
	var stdout bytes.Buffer
	if err := Run(context.Background(), []string{"--help"}, &stdout, &bytes.Buffer{}, func(string) string { return "" }); err != nil {
		t.Fatal(err)
	}
	help := stdout.String()
	if !strings.Contains(help, "spk-rayjob — 分布式训练任务命令行客户端") {
		t.Fatalf("help title is not product neutral: %q", help)
	}
	if strings.Contains(help, "西井") {
		t.Fatalf("help must not expose an internal brand: %q", help)
	}
	for _, expected := range []string{"--engine ray-ddp", "Actor + torchrun", "--engine ray-train", "--max-failures", "--checkpoint-every-epochs", "--checkpoint-keep-latest", "--checkpoint-keep-best", "workers", "Checkpoint", "平台开启后可用"} {
		if !strings.Contains(help, expected) {
			t.Fatalf("help must explain engine semantics and availability using %q: %s", expected, help)
		}
	}
	if strings.Contains(help, "--ray-version") {
		t.Fatalf("the client must not expose a Ray version override: %s", help)
	}
}

func TestRunLoginWritesOwnerOnlyConfigWithoutEchoingToken(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer login-secret" {
			t.Fatal("login did not validate the supplied token")
		}
		writeClientSuccess(t, writer, http.StatusOK, map[string]any{"items": []any{}})
	}))
	defer server.Close()

	configPath := filepath.Join(t.TempDir(), "nested", "config.json")
	caFile := writeTestCA(t, server)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	err := RunWithInput(context.Background(), []string{
		"login", "--server", server.URL, "--ca-file", caFile,
		"--token-stdin", "--config", configPath,
	}, strings.NewReader("login-secret\n"), &stdout, &stderr, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(configPath)
	if err != nil {
		t.Fatal(err)
	}
	config, err := loadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 || config.Token != "login-secret" || config.Server != server.URL {
		t.Fatalf("mode=%o config=%+v", info.Mode().Perm(), config)
	}
	if !strings.Contains(stdout.String(), "登录成功") || strings.Contains(stdout.String(), "login-secret") || stderr.Len() != 0 {
		t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestRunLoginRejectsInsecurePlatformURL(t *testing.T) {
	err := RunWithInput(context.Background(), []string{
		"login", "--server", "http://train.xx.com", "--token-stdin",
	}, strings.NewReader("login-secret\n"), &bytes.Buffer{}, &bytes.Buffer{}, func(string) string { return "" })
	if err == nil {
		t.Fatal("HTTP login server was accepted")
	}
}

func TestRunLoginWithPlatformCredentialsStoresOnlyTheIssuedSession(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v1/auth/login":
			if request.Method != http.MethodPost {
				t.Fatalf("login method=%s", request.Method)
			}
			var body map[string]string
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["username"] != "alice" || body["password"] != "correct-horse" {
				t.Fatalf("unexpected login body: %#v", body)
			}
			writeClientSuccess(t, writer, http.StatusOK, map[string]any{"token": "local-session-token", "username": "alice"})
		case "/api/v1/jobs":
			if request.Header.Get("Authorization") != "Bearer local-session-token" {
				t.Fatalf("authorization=%q", request.Header.Get("Authorization"))
			}
			writeClientSuccess(t, writer, http.StatusOK, map[string]any{"items": []any{}})
		default:
			t.Fatalf("unexpected request: %s", request.URL.Path)
		}
	}))
	defer server.Close()

	configPath := filepath.Join(t.TempDir(), "rayctl.json")
	caFile := writeTestCA(t, server)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	err := RunWithInput(context.Background(), []string{
		"login", "--server", server.URL, "--ca-file", caFile, "--username", "alice", "--password-stdin", "--config", configPath,
	}, strings.NewReader("correct-horse\n"), &stdout, &stderr, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(contents, []byte("correct-horse")) || bytes.Contains(stdout.Bytes(), []byte("correct-horse")) || bytes.Contains(stderr.Bytes(), []byte("correct-horse")) {
		t.Fatalf("password leaked: config=%q stdout=%q stderr=%q", contents, stdout.String(), stderr.String())
	}
	if !bytes.Contains(contents, []byte("local-session-token")) || !bytes.Contains(stdout.Bytes(), []byte("登录成功：alice")) {
		t.Fatalf("unexpected login persistence: config=%q stdout=%q", contents, stdout.String())
	}
}

func TestRunSubmitOmitsQueueAndLetsPlatformResolveIt(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "train.py"), []byte("print('train')\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v1/source-artifacts":
			var create struct {
				SHA256    string `json:"sha256"`
				SizeBytes int64  `json:"sizeBytes"`
			}
			if err := json.NewDecoder(request.Body).Decode(&create); err != nil {
				t.Fatal(err)
			}
			writeClientSuccess(t, writer, http.StatusCreated, map[string]any{
				"artifactId": "artifact-test", "state": "PENDING", "sha256": create.SHA256, "sizeBytes": create.SizeBytes,
				"uploadUrl": serverURL(request) + "/upload", "contentLength": create.SizeBytes, "uploadRequired": true,
			})
		case "/upload":
			writer.WriteHeader(http.StatusOK)
		case "/api/v1/source-artifacts/artifact-test/complete":
			writeClientSuccess(t, writer, http.StatusOK, map[string]any{"artifactId": "artifact-test", "state": "READY", "uploadRequired": false})
		case "/api/v1/jobs":
			var body struct {
				Spec domain.JobSpec `json:"spec"`
			}
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body.Spec.Queue != "" {
				t.Fatalf("CLI sent a user-selected queue %q", body.Spec.Queue)
			}
			writeClientSuccess(t, writer, http.StatusAccepted, map[string]any{"id": "job-test"})
		default:
			t.Fatalf("unexpected request %s", request.URL.Path)
		}
	}))
	defer server.Close()

	caFile := writeTestCA(t, server)
	var stdout bytes.Buffer
	err := Run(context.Background(), []string{
		"submit", "--server", server.URL, "--ca-file", caFile, "--dir", root,
		"--name", "external-submit", "--image", "harbor.example/train@sha256:" + strings.Repeat("a", 64),
		"--entrypoint", "python train.py",
	}, &stdout, &bytes.Buffer{}, func(key string) string {
		if key == "RAY_PLATFORM_TOKEN" {
			return "test-token"
		}
		return ""
	})
	if err != nil || !strings.Contains(stdout.String(), "job-test") {
		t.Fatalf("err=%v stdout=%q", err, stdout.String())
	}
}

func TestRunSubmitPropagatesCallerSourceArtifactID(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "train.py"), []byte("print('train')\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	const artifactID = "artifact-0123456789abcdef01234567"
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v1/source-artifacts":
			var create struct {
				ArtifactID string `json:"artifactId"`
			}
			if err := json.NewDecoder(request.Body).Decode(&create); err != nil {
				t.Fatal(err)
			}
			if create.ArtifactID != artifactID {
				t.Fatalf("artifactID=%q", create.ArtifactID)
			}
			writeClientSuccess(t, writer, http.StatusOK, map[string]any{"artifactId": artifactID, "state": "READY", "uploadRequired": false})
		case "/api/v1/jobs":
			writeClientSuccess(t, writer, http.StatusAccepted, map[string]any{"id": "job-test"})
		default:
			t.Fatalf("unexpected request %s", request.URL.Path)
		}
	}))
	defer server.Close()
	err := Run(context.Background(), []string{
		"submit", "--server", server.URL, "--ca-file", writeTestCA(t, server), "--dir", root,
		"--name", "external-submit", "--image", "harbor.example/train@sha256:" + strings.Repeat("a", 64),
		"--entrypoint", "python train.py", "--source-artifact-id", artifactID,
	}, &bytes.Buffer{}, &bytes.Buffer{}, testEnvironment)
	if err != nil {
		t.Fatal(err)
	}
}

func TestRunSubmitMapsLogicalDataSpacesWithoutObjectStorePaths(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "train.py"), []byte("print('train')\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v1/source-artifacts":
			var create struct {
				SHA256    string `json:"sha256"`
				SizeBytes int64  `json:"sizeBytes"`
			}
			if err := json.NewDecoder(request.Body).Decode(&create); err != nil {
				t.Fatal(err)
			}
			writeClientSuccess(t, writer, http.StatusCreated, map[string]any{
				"artifactId": "artifact-data", "state": "PENDING", "sha256": create.SHA256, "sizeBytes": create.SizeBytes,
				"uploadUrl": serverURL(request) + "/upload", "contentLength": create.SizeBytes, "uploadRequired": true,
			})
		case "/upload":
			writer.WriteHeader(http.StatusOK)
		case "/api/v1/source-artifacts/artifact-data/complete":
			writeClientSuccess(t, writer, http.StatusOK, map[string]any{"artifactId": "artifact-data", "state": "READY", "uploadRequired": false})
		case "/api/v1/jobs":
			var body struct {
				Spec domain.JobSpec `json:"spec"`
			}
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body.Spec.Input != (domain.DataLocation{Space: domain.DataSpaceMyFiles, RelativePath: "datasets/demo"}) {
				t.Fatalf("input=%+v", body.Spec.Input)
			}
			if body.Spec.Output != (domain.DataLocation{Space: domain.DataSpaceMyRuns, RelativePath: "experiments/cli"}) {
				t.Fatalf("output=%+v", body.Spec.Output)
			}
			writeClientSuccess(t, writer, http.StatusAccepted, map[string]any{"id": "job-data"})
		default:
			t.Fatalf("unexpected request %s", request.URL.Path)
		}
	}))
	defer server.Close()

	caFile := writeTestCA(t, server)
	err := Run(context.Background(), []string{
		"submit", "--server", server.URL, "--ca-file", caFile, "--dir", root,
		"--name", "external-data", "--image", "harbor.example/train@sha256:" + strings.Repeat("a", 64),
		"--entrypoint", "python train.py", "--input-space", "my-files", "--input-path", "datasets/demo", "--output-path", "experiments/cli",
	}, &bytes.Buffer{}, &bytes.Buffer{}, func(key string) string {
		if key == "RAY_PLATFORM_TOKEN" {
			return "test-token"
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestRunVersionPrintsAReleaseIdentifier(t *testing.T) {
	var stdout bytes.Buffer
	if err := Run(context.Background(), []string{"version"}, &stdout, &bytes.Buffer{}, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(stdout.String(), "spk-rayjob ") {
		t.Fatalf("version output=%q", stdout.String())
	}
}

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

func TestForwardCursorStartsStrictlyAfterTheNewestLineInAnInitialTail(t *testing.T) {
	cursor, err := forwardCursorAfterEntries([]LogEntry{
		{Timestamp: "2026-08-22T16:00:10Z", Line: "oldest"},
		{Timestamp: "2026-08-22T16:00:12Z", Line: "newest-a"},
		{Timestamp: "2026-08-22T16:00:12Z", Line: "newest-b"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if cursor != "2026-08-22T16:00:12.000000001Z" {
		t.Fatalf("forward cursor=%q, want the instant after the newest tail entry", cursor)
	}
}
