package spkrayjob

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func completionQuery(t *testing.T, getenv func(string) string, words ...string) string {
	t.Helper()
	var stdout, stderr bytes.Buffer
	err := Run(context.Background(), append([]string{"__complete"}, words...), &stdout, &stderr, getenv)
	if err != nil || stderr.Len() != 0 {
		t.Fatalf("completion must be silent: err=%v stderr=%q", err, stderr.String())
	}
	return stdout.String()
}

func completionValues(output string) []string {
	var values []string
	for _, line := range strings.Split(strings.TrimSuffix(output, "\n"), "\n") {
		if line != "" {
			values = append(values, strings.SplitN(line, "\t", 2)[0])
		}
	}
	return values
}

func TestCompletionStaticOffline(t *testing.T) {
	offline := func(string) string { return "" }
	cases := []struct {
		words []string
		want  []string
	}{
		{[]string{""}, []string{"submit", "status", "logs", "diagnose", "completion"}},
		{[]string{"sta"}, []string{"status"}},
		{[]string{"dataset", ""}, []string{"versions"}},
		{[]string{"source-artifact", ""}, []string{"resolve"}},
		{[]string{"completion", ""}, []string{"bash", "zsh"}},
		{[]string{"help", ""}, []string{"submit", "diagnose", "completion"}},
		{[]string{"submit", "--eng"}, []string{"--engine"}},
		{[]string{"submit", "--engine", "ray-"}, []string{"ray-ddp", "ray-train"}},
		{[]string{"submit", "--engine=ray-"}, []string{"--engine=ray-ddp", "--engine=ray-train"}},
		{[]string{"submit", "--priority", ""}, []string{"production", "normal", "opportunistic"}},
		{[]string{"submit", "--data-mode", ""}, []string{"mount", "cache", "ray-data-stage", "ray-data", "streaming"}},
		{[]string{"submit", "--dataset-cache-policy", ""}, []string{"off", "auto", "bounded"}},
		{[]string{"submit", "--input-space", "my-"}, []string{"my-storage", "my-files", "my-runs"}},
		{[]string{"jobs", "--state", "RUN"}, []string{"RUNNING"}},
		{[]string{"status", "--output", ""}, []string{"text", "json"}},
		{[]string{"connect", "job-123456789012345678901234", "--wo"}, []string{"--worker"}},
		{[]string{"diagnose", "--out"}, []string{"--output"}},
		{[]string{"diagnose", "--help"}, []string{"--help"}},
	}
	for _, tc := range cases {
		t.Run(strings.Join(tc.words, "_"), func(t *testing.T) {
			got := completionValues(completionQuery(t, offline, tc.words...))
			for _, want := range tc.want {
				found := false
				for _, value := range got {
					found = found || value == want
				}
				if !found {
					t.Errorf("completion %q: want %q, got %q", tc.words, want, got)
				}
			}
		})
	}
}

func TestCompletionDoesNotSuggestCredentialsOrFlagsAfterPositionals(t *testing.T) {
	for _, words := range [][]string{
		{"login", "--username", ""}, {"login", "--token", ""},
		{"status", "job-123456789012345678901234", "--"},
		{"logs", "job-123456789012345678901234", ""},
		{"submit", "--entrypoint", ""}, {"unknown", ""},
		{"", ""}, {"status", "--unknown", "job-"},
	} {
		if got := completionQuery(t, func(string) string { return "credential-value" }, words...); got != "" {
			t.Errorf("completion %q must be empty, got %q", words, got)
		}
	}
}

func TestCompletionFilesPreserveSpacesAndRejectControlCharacters(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"project config.yaml", "bad\nname", "bad\tname"} {
		if err := os.WriteFile(filepath.Join(root, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(root, "source folder"), 0o700); err != nil {
		t.Fatal(err)
	}
	got := completionValues(completionQuery(t, func(string) string { return "" }, "status", "--config", root+"/"))
	if strings.Join(got, "|") != root+"/project config.yaml|"+root+"/source folder/" {
		t.Fatalf("file candidates lost boundaries: %q", got)
	}
	got = completionValues(completionQuery(t, func(string) string { return "" }, "submit", "--dir", root+"/"))
	if len(got) != 1 || got[0] != root+"/source folder/" {
		t.Fatalf("--dir must only complete directories: %q", got)
	}
}

func TestCompletionJobsUseAuthenticatedVisibleListAndSanitizeDescriptions(t *testing.T) {
	var calls atomic.Int32
	const jobID = "job-123456789012345678901234"
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		if request.Method != http.MethodGet || request.URL.Path != "/api/v1/jobs" || request.URL.Query().Get("limit") != "100" {
			t.Errorf("unexpected completion request %s %s", request.Method, request.URL)
		}
		if request.Header.Get("Authorization") != "Bearer completion-test-secret" {
			t.Error("completion must use the user's configured authentication")
		}
		writeClientSuccess(t, writer, http.StatusOK, map[string]any{"items": []any{
			map[string]any{"id": jobID, "observedState": "RUNNING", "spec": map[string]any{"name": "training\tname\n\x1b[31m $(touch marker) completion-test-secret"}},
			map[string]any{"id": "$(touch marker)", "observedState": "RUNNING"},
		}})
	}))
	defer server.Close()
	ca := writeTestCA(t, server)
	getenv := func(key string) string {
		if key == "SPK_RAYJOB_TOKEN" {
			return "completion-test-secret"
		}
		return ""
	}
	for _, command := range []string{"status", "logs", "cancel", "connect", "diagnose"} {
		got := completionQuery(t, getenv, command, "--server", server.URL, "--ca-file", ca, "--debug", "job-")
		if !strings.HasPrefix(got, jobID+"\tRUNNING ") || !strings.Contains(got, "training name") || strings.Count(got, "\n") != 1 || strings.Count(got, "\t") != 1 || strings.ContainsAny(got, "\x1b\r") || strings.Contains(got, "completion-test-secret") {
			t.Fatalf("unsafe or missing job candidate: %q", got)
		}
	}
	if calls.Load() != 5 {
		t.Fatalf("expected only one visible-list request per query, got %d", calls.Load())
	}
}

func TestCompletionAuthenticationChangeCannotReuseCandidates(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		id := "job-111111111111111111111111"
		if request.Header.Get("Authorization") == "Bearer second-token" {
			id = "job-222222222222222222222222"
		}
		writeClientSuccess(t, writer, http.StatusOK, map[string]any{"items": []any{map[string]any{"id": id}}})
	}))
	defer server.Close()
	config := filepath.Join(t.TempDir(), "config.json")
	ca := writeTestCA(t, server)
	for index, token := range []string{"first-token", "second-token"} {
		contents, err := json.Marshal(map[string]string{"server": server.URL, "token": token})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(config, contents, 0o600); err != nil {
			t.Fatal(err)
		}
		got := completionQuery(t, func(string) string { return "" }, "status", "--config", config, "--ca-file", ca, "job-")
		want := "job-" + strings.Repeat(fmt.Sprint(index+1), 24)
		if values := completionValues(got); len(values) != 1 || values[0] != want {
			t.Fatalf("identity switch reused candidates: %q", got)
		}
	}
}

func TestCompletionFailuresAreSilentAndBounded(t *testing.T) {
	for _, mode := range []string{"unauthorized", "malformed", "slow"} {
		t.Run(mode, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				switch mode {
				case "slow":
					select {
					case <-request.Context().Done():
					case <-time.After(3 * time.Second):
					}
				case "unauthorized":
					writer.WriteHeader(http.StatusUnauthorized)
					_, _ = writer.Write([]byte(`{"error":"secret-error"}`))
				default:
					_, _ = writer.Write([]byte(`{"success":true,"data":{"items":"bad"}}`))
				}
			}))
			defer server.Close()
			start := time.Now()
			got := completionQuery(t, func(key string) string {
				if key == "SPK_RAYJOB_TOKEN" {
					return "test-token"
				}
				return ""
			}, "status", "--server", server.URL, "--ca-file", writeTestCA(t, server), "job-")
			if got != "" || time.Since(start) > 1500*time.Millisecond {
				t.Fatalf("completion failure must be empty within 1.5s, got %q after %s", got, time.Since(start))
			}
		})
	}
}

func TestCompletionScriptGeneration(t *testing.T) {
	for _, shell := range []string{"bash", "zsh"} {
		var stdout, stderr bytes.Buffer
		if err := Run(context.Background(), []string{"completion", shell}, &stdout, &stderr, func(string) string { return "secret-must-not-appear" }); err != nil {
			t.Fatal(err)
		}
		if stderr.Len() != 0 || !strings.Contains(stdout.String(), "__complete") || strings.Contains(stdout.String(), "secret-must-not-appear") || strings.Contains(stdout.String(), "eval ") {
			t.Fatalf("unsafe %s script: %s", shell, stdout.String())
		}
		shellPath, err := exec.LookPath(shell)
		if err != nil {
			t.Fatalf("builder must install %s to verify completion scripts: %v", shell, err)
		}
		command := exec.Command(shellPath, "-n")
		command.Stdin = strings.NewReader(stdout.String())
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("%s script syntax: %v: %s", shell, err, output)
		}
	}
	for _, args := range [][]string{{"completion"}, {"completion", "fish"}, {"completion", "bash", "extra"}} {
		if err := Run(context.Background(), args, &bytes.Buffer{}, &bytes.Buffer{}, nil); err == nil {
			t.Errorf("invalid completion arguments accepted: %q", args)
		}
	}
}

// The shell adapter invokes this test process as a real CLI without compiling
// another binary. Only the child carrying this dedicated marker exits here.
func TestCompletionHelper(t *testing.T) {
	if os.Getenv("SPK_RAYJOB_COMPLETION_HELPER") != "1" {
		return
	}
	for i, argument := range os.Args {
		if argument == "--" {
			if err := Run(context.Background(), os.Args[i+1:], os.Stdout, os.Stderr, os.Getenv); err != nil {
				os.Exit(1)
			}
			os.Exit(0)
		}
	}
	os.Exit(2)
}

func completionShellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func TestCompletionShellAdaptersPreserveLiteralCandidates(t *testing.T) {
	root := t.TempDir()
	const visibleJob = "job-abcdef123456abcdef123456"
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/v1/jobs" || request.Header.Get("Authorization") != "Bearer shell-test-token" {
			t.Errorf("shell completion used unexpected authentication or path: %s", request.URL.Path)
		}
		writeClientSuccess(t, writer, http.StatusOK, map[string]any{"items": []any{map[string]any{"id": visibleJob}}})
	}))
	defer server.Close()
	ca := writeTestCA(t, server)
	bin := filepath.Join(root, "bin with spaces")
	if err := os.Mkdir(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	wrapper := "#!/bin/sh\nSPK_RAYJOB_COMPLETION_HELPER=1 exec " + completionShellQuote(executable) + " -test.run=^TestCompletionHelper$ -- \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "spk-rayjob"), []byte(wrapper), 0o700); err != nil {
		t.Fatal(err)
	}
	const hostileFile = "$(touch INJECTED) config.yaml"
	if err := os.WriteFile(filepath.Join(root, hostileFile), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, shell := range []string{"bash", "zsh"} {
		t.Run(shell, func(t *testing.T) {
			var script bytes.Buffer
			if err := Run(context.Background(), []string{"completion", shell}, &script, &bytes.Buffer{}, nil); err != nil {
				t.Fatal(err)
			}
			preamble := ""
			suffix := "\nCOMP_WORDS=(spk-rayjob status --config '$(touch'); COMP_CWORD=3\n_spk_rayjob\nprintf '%s\\n' \"${COMPREPLY[@]}\"\n"
			if shell == "zsh" {
				preamble = "compdef() { :; }\ncompadd() { while (( $# )); do if [[ $1 == -- ]]; then shift; print -rl -- \"$@\"; return; fi; shift; done; }\n"
				suffix = "\nwords=(spk-rayjob status --config '$(touch'); CURRENT=4\n_spk_rayjob\n"
			}
			command := exec.Command(shell, "-f", "-c", preamble+script.String()+suffix)
			command.Dir = root
			command.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			output, err := command.CombinedOutput()
			if err != nil || string(output) != hostileFile+"\n" {
				t.Fatalf("%s literal completion failed: %v %q", shell, err, output)
			}
			if _, err := os.Stat(filepath.Join(root, "INJECTED")); !os.IsNotExist(err) {
				t.Fatal("completion executed a candidate as shell code")
			}
			enumSuffix := "\nCOMP_WORDS=(spk-rayjob submit --engine = ray-); COMP_CWORD=4\n_spk_rayjob\nprintf '%s\\n' \"${COMPREPLY[@]}\"\n"
			expected := "ray-ddp\nray-train\n"
			if shell == "zsh" {
				enumSuffix = "\nwords=(spk-rayjob submit --engine=ray-); CURRENT=3\n_spk_rayjob\n"
				expected = "--engine=ray-ddp\n--engine=ray-train\n"
			}
			command = exec.Command(shell, "-f", "-c", preamble+script.String()+enumSuffix)
			command.Dir = root
			command.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			if output, err := command.CombinedOutput(); err != nil || string(output) != expected {
				t.Fatalf("%s equals completion failed: %v %q", shell, err, output)
			}
			urlWords := strings.Split(server.URL, ":")
			for index, word := range urlWords {
				urlWords[index] = completionShellQuote(word)
			}
			jobSuffix := "\nCOMP_WORDS=(spk-rayjob status --server " + strings.Join(urlWords, " : ") + " --ca-file " + completionShellQuote(ca) + " job-); COMP_CWORD=$((${#COMP_WORDS[@]} - 1))\n_spk_rayjob\nprintf '%s\\n' \"${COMPREPLY[@]}\"\n"
			if shell == "zsh" {
				jobSuffix = "\nwords=(spk-rayjob status --server " + completionShellQuote(server.URL) + " --ca-file " + completionShellQuote(ca) + " job-); CURRENT=${#words[@]}\n_spk_rayjob\n"
			}
			command = exec.Command(shell, "-f", "-c", preamble+script.String()+jobSuffix)
			command.Dir = root
			command.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "SPK_RAYJOB_TOKEN=shell-test-token")
			if output, err := command.CombinedOutput(); err != nil || string(output) != visibleJob+"\n" {
				t.Fatalf("%s explicit server completion failed: %v %q", shell, err, output)
			}
		})
	}
}
