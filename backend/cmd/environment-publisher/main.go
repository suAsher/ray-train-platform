// environment-publisher is a trusted, non-interactive publish worker. It only
// reads a sealed OCI layout and projected credential files; it executes no user
// scripts and does not modify the shared build artifact volume.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/google/go-containerregistry/pkg/logs"
	"ray-train-platform-backend/registryauth"
)

func main() {
	var request registryauth.PublishRequest
	var credentialsDirectory, resultPath string
	flag.StringVar(&request.LayoutPath, "layout", "/artifacts/oci", "sealed OCI layout directory")
	flag.StringVar(&request.Digest, "digest", "", "frozen OCI image manifest digest")
	flag.StringVar(&request.Project, "project", "", "Harbor project")
	flag.StringVar(&request.Repository, "repository", "", "repository within the project")
	flag.StringVar(&request.Tag, "tag", "", "frozen version tag")
	flag.StringVar(&credentialsDirectory, "credentials-dir", "/registry-credentials", "projected credential directory")
	flag.StringVar(&resultPath, "result", "/dev/termination-log", "small sanitized result file")
	flag.Parse()
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer cancel()
	ctx, deadline := context.WithTimeout(ctx, 45*time.Minute)
	defer deadline()
	// The registry library's raw retry/debug logs may contain signed upload
	// locations. Only the allowlisted diagnostic events below are emitted.
	logs.Warn.SetOutput(io.Discard)
	logs.Debug.SetOutput(io.Discard)
	logs.Progress.SetOutput(io.Discard)
	diagnostics := json.NewEncoder(os.Stderr)
	ctx = registryauth.WithPublishDiagnostics(ctx, func(event registryauth.PublishDiagnostic) {
		_ = diagnostics.Encode(event)
	})
	result, err := run(ctx, request, credentialsDirectory)
	if err != nil {
		result = registryauth.PublishResult{ErrorCode: errorCode(err)}
	}
	data, _ := json.Marshal(result)
	if writeErr := os.WriteFile(resultPath, data, 0600); writeErr != nil {
		os.Stderr.WriteString("PUBLISH_RESULT_WRITE_FAILED\n")
		os.Exit(1)
	}
	if err != nil {
		os.Stderr.WriteString(result.ErrorCode + "\n")
		os.Exit(1)
	}
	os.Stdout.Write(data)
	os.Stdout.WriteString("\n")
}

func readCredential(directory, name string, limit int64) (string, error) {
	file, err := os.Open(filepath.Join(directory, name))
	if err != nil {
		return "", registryauth.ErrCredentials
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > limit {
		return "", registryauth.ErrCredentials
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || int64(len(data)) > limit {
		return "", registryauth.ErrCredentials
	}
	return string(data), nil
}

func run(ctx context.Context, request registryauth.PublishRequest, directory string) (registryauth.PublishResult, error) {
	username, err := readCredential(directory, "username", 256)
	if err != nil {
		return registryauth.PublishResult{}, err
	}
	secret, err := readCredential(directory, "secret", 8192)
	if err != nil {
		return registryauth.PublishResult{}, err
	}
	return registryauth.NewClient().Publish(ctx, registryauth.Credentials{Username: username, Secret: secret}, request)
}

func errorCode(err error) string {
	switch {
	case errors.Is(err, registryauth.ErrCredentials):
		return "REGISTRY_AUTH_REQUIRED"
	case errors.Is(err, registryauth.ErrForbidden):
		return "REGISTRY_PUSH_DENIED"
	case errors.Is(err, registryauth.ErrArtifact):
		return "OCI_ARTIFACT_INVALID"
	case errors.Is(err, registryauth.ErrInvalidTarget):
		return "REGISTRY_TARGET_INVALID"
	default:
		return "REGISTRY_PUBLISH_FAILED"
	}
}
