package registryauth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	registrytransport "github.com/google/go-containerregistry/pkg/v1/remote/transport"
)

func TestPublishErrorsClassifyAuthFailuresWithoutLeakingRemoteDetails(t *testing.T) {
	for _, tc := range []struct {
		status int
		want   error
	}{{401, ErrCredentials}, {403, ErrForbidden}, {500, ErrUnavailable}, {429, ErrUnavailable}} {
		remoteError := fmt.Errorf("sensitive-password-and-upload-query: %w", &registrytransport.Error{StatusCode: tc.status})
		result := publishError(remoteError)
		if !errors.Is(result, tc.want) || strings.Contains(result.Error(), "sensitive") || errors.Is(result, remoteError) {
			t.Fatalf("unsafe error mapping: %v", result)
		}
	}
	if got := publishError(errors.New("private network address and secret")); !errors.Is(got, ErrUnavailable) || strings.Contains(got.Error(), "secret") {
		t.Fatalf("network details leaked: %v", got)
	}
}

func TestAssembleRegistryFailureDoesNotExposeUpstreamBody(t *testing.T) {
	request := preparedAssemblyRequest(t)
	request.Base = Host + "/fixtures/base@sha256:" + strings.Repeat("a", 64)
	client := testClient(func(request *http.Request) (*http.Response, error) {
		response := reply(403, `{"errors":[{"code":"DENIED","message":"fixture-private-secret"}]}`)
		response.Request = request
		return response, nil
	})
	result, err := client.Assemble(context.Background(), request)
	if err == nil || strings.Contains(err.Error(), "fixture-private-secret") || result.ArtifactDigest != "" {
		t.Fatalf("unsafe assembly failure: %+v %v", result, err)
	}
}

func TestPublishStopsBeforeUploadWhenCredentialsLackPush(t *testing.T) {
	directory, digest := testLayout(t)
	client := testClient(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != "/service/token" {
			t.Fatal("unauthorized publisher reached blob upload")
		}
		return reply(200, `{"token":"opaque-with-no-verified-grant"}`), nil
	})
	result, err := client.Publish(context.Background(), credentials, PublishRequest{LayoutPath: directory, Digest: digest, Project: "team", Repository: "model", Tag: "v1"})
	if !errors.Is(err, ErrForbidden) || result.ImageDigest != "" {
		t.Fatalf("unverified grant published an image: %+v %v", result, err)
	}
}

func TestPersonalAuthenticationRejectsMalformedCredentialsBeforeTransmission(t *testing.T) {
	client := testClient(func(*http.Request) (*http.Response, error) {
		t.Fatal("malformed credentials were transmitted")
		return nil, nil
	})
	for _, value := range []Credentials{{Username: "", Secret: "fixture"}, {Username: "user:other", Secret: "fixture"}, {Username: "user", Secret: ""}, {Username: "user", Secret: "fixture\nsecret"}, {Username: "user", Secret: strings.Repeat("x", 8193)}} {
		if _, err := client.Authenticate(context.Background(), value); !errors.Is(err, ErrCredentials) {
			t.Fatalf("malformed identity accepted: %v", err)
		}
	}
}

func TestPersonalAuthenticationRejectsMismatchedIdentityAndMalformedResponse(t *testing.T) {
	for _, body := range []string{`{"user_id":7,"username":"another-user"}`, `{"user_id":0,"username":"test-user"}`, `not-json`, strings.Repeat("x", maxResponseBytes+1)} {
		client := testClient(func(*http.Request) (*http.Response, error) { return reply(200, body), nil })
		if _, err := client.Authenticate(context.Background(), credentials); err == nil {
			t.Fatal("unverified identity accepted")
		}
	}
	client := testClient(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("private upstream endpoint?token=fixture-secret")
	})
	if _, err := client.Authenticate(context.Background(), credentials); !errors.Is(err, ErrUnavailable) || strings.Contains(err.Error(), "fixture-secret") {
		t.Fatalf("network error leaked: %v", err)
	}
}
