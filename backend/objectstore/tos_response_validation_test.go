package objectstore

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

type fakeTOSClient struct {
	presignResponse *tosPresignResponse
	presignErr      error
	lastPresign     tosPresignRequest
	headInfo        ObjectInfo
	headErr         error
}

func (client *fakeTOSClient) Presign(_ context.Context, request tosPresignRequest) (*tosPresignResponse, error) {
	client.lastPresign = request
	return client.presignResponse, client.presignErr
}

func (client *fakeTOSClient) Head(_ context.Context, _, _ string) (ObjectInfo, error) {
	return client.headInfo, client.headErr
}

func TestTOSStoreValidatesPresignResponseAndReturnsBrowserContract(t *testing.T) {
	now := time.Date(2026, 8, 10, 4, 0, 0, 0, time.UTC)
	client := &fakeTOSClient{}
	store, err := newTOSStoreWithClient(TOSConfig{
		Endpoint: "https://tos.example.com", Region: "cn", Bucket: "private-bucket",
		AccessKey: "ak", SecretKey: "sk", Now: func() time.Time { return now },
	}, client)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	client.presignResponse = validPresignResponse("https://private-bucket.tos.example.com/object.zip", fullUploadHeaders(1234, testDigest))

	result, err := store.PresignPut(context.Background(), "object.zip", testDigest, 1234, 15*time.Minute)
	if err != nil {
		t.Fatalf("presign: %v", err)
	}
	if result.ContentLength != 1234 || result.ExpiresAt != now.Add(15*time.Minute) {
		t.Fatalf("unexpected browser contract: length=%d expiry=%s", result.ContentLength, result.ExpiresAt)
	}
	if _, ok := result.RequiredHeaders["Content-Length"]; ok {
		t.Fatal("browser must not be instructed to set Content-Length")
	}
	for key, want := range browserUploadHeaders(testDigest) {
		if result.RequiredHeaders[key] != want {
			t.Fatalf("browser header %q=%q, want %q", key, result.RequiredHeaders[key], want)
		}
	}
	if client.lastPresign.Headers["Content-Length"] != "1234" {
		t.Fatal("Content-Length must remain part of the signed request")
	}
	if client.lastPresign.ExpiresSeconds != 900 {
		t.Fatalf("signed expiry=%d, want 900", client.lastPresign.ExpiresSeconds)
	}
}

func TestTOSStoreRejectsMalformedPresignResponsesWithoutLeakingURL(t *testing.T) {
	validHeaders := fullUploadHeaders(1234, testDigest)
	valid := validPresignResponse("https://tos.example.com/object.zip", validHeaders)
	tests := []struct {
		name     string
		response *tosPresignResponse
	}{
		{name: "nil response", response: nil},
		{name: "http URL", response: validPresignResponse("http://tos.example.com/object.zip", validHeaders)},
		{name: "relative URL", response: validPresignResponse("/object.zip", validHeaders)},
		{name: "foreign host", response: validPresignResponse("https://attacker.example/object.zip", validHeaders)},
		{name: "empty path", response: validPresignResponse("https://tos.example.com", validHeaders)},
		{name: "userinfo", response: validPresignResponse("https://user@tos.example.com/object.zip", validHeaders)},
		{name: "fragment", response: validPresignResponse("https://tos.example.com/object.zip#fragment", validHeaders)},
		{name: "missing core query", response: withoutQuery(valid, "X-Tos-Signature")},
		{name: "duplicate core query", response: withDuplicateQuery(valid, "X-Tos-Signature")},
		{name: "case duplicate core query", response: withCaseDuplicateQuery(valid, "x-tos-signature")},
		{name: "missing signed header query", response: withoutSignedHeader(valid, "content-length")},
		{name: "missing exact header", response: withoutResponseHeader(valid, "If-None-Match")},
		{name: "wrong exact header", response: withResponseHeader(valid, "x-tos-forbid-overwrite", "false")},
		{name: "duplicate exact header values", response: withDuplicateResponseHeader(valid, "Content-Type", "application/zip")},
		{name: "case duplicate exact header", response: withCaseDuplicateResponseHeader(valid, "content-type", "application/zip")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := &fakeTOSClient{presignResponse: test.response}
			store, err := newTOSStoreWithClient(TOSConfig{
				Endpoint: "https://tos.example.com", Region: "cn", Bucket: "private-bucket",
				AccessKey: "ak", SecretKey: "sk",
			}, client)
			if err != nil {
				t.Fatal(err)
			}
			_, err = store.PresignPut(context.Background(), "secret-object-key.zip", testDigest, 1234, 15*time.Minute)
			if !errors.Is(err, ErrUnavailable) {
				t.Fatalf("error=%v, want ErrUnavailable", err)
			}
			if strings.Contains(err.Error(), "secret-object-key") || strings.Contains(err.Error(), "attacker.example") || strings.Contains(err.Error(), "X-Tos-") {
				t.Fatalf("presign validation error leaked response details: %v", err)
			}
		})
	}
}

func validPresignResponse(base string, headers map[string]string) *tosPresignResponse {
	parsed, _ := url.Parse(base)
	query := parsed.Query()
	query.Set("X-Tos-Algorithm", "TOS4-HMAC-SHA256")
	query.Set("X-Tos-Credential", "test/credential")
	query.Set("X-Tos-Date", "20260810T040000Z")
	query.Set("X-Tos-Expires", "900")
	query.Set("X-Tos-Signature", "signature")
	names := make([]string, 0, len(headers))
	for key := range headers {
		names = append(names, strings.ToLower(key))
	}
	query.Set("X-Tos-SignedHeaders", strings.Join(names, ";"))
	parsed.RawQuery = query.Encode()
	return &tosPresignResponse{URL: parsed.String(), SignedHeaders: httpHeaderFromStrings(headers)}
}

func withDuplicateQuery(input *tosPresignResponse, name string) *tosPresignResponse {
	clone := clonePresignResponse(input)
	parsed, _ := url.Parse(clone.URL)
	query := parsed.Query()
	query.Add(name, "duplicate")
	parsed.RawQuery = query.Encode()
	clone.URL = parsed.String()
	return clone
}

func withCaseDuplicateQuery(input *tosPresignResponse, name string) *tosPresignResponse {
	clone := clonePresignResponse(input)
	parsed, _ := url.Parse(clone.URL)
	query := parsed.Query()
	query[name] = []string{"duplicate"}
	parsed.RawQuery = query.Encode()
	clone.URL = parsed.String()
	return clone
}

func withoutQuery(input *tosPresignResponse, name string) *tosPresignResponse {
	clone := clonePresignResponse(input)
	parsed, _ := url.Parse(clone.URL)
	query := parsed.Query()
	query.Del(name)
	parsed.RawQuery = query.Encode()
	clone.URL = parsed.String()
	return clone
}

func withoutSignedHeader(input *tosPresignResponse, name string) *tosPresignResponse {
	clone := clonePresignResponse(input)
	parsed, _ := url.Parse(clone.URL)
	query := parsed.Query()
	parts := strings.Split(query.Get("X-Tos-SignedHeaders"), ";")
	kept := parts[:0]
	for _, part := range parts {
		if part != name {
			kept = append(kept, part)
		}
	}
	query.Set("X-Tos-SignedHeaders", strings.Join(kept, ";"))
	parsed.RawQuery = query.Encode()
	clone.URL = parsed.String()
	return clone
}

func withoutResponseHeader(input *tosPresignResponse, name string) *tosPresignResponse {
	clone := clonePresignResponse(input)
	clone.SignedHeaders.Del(name)
	return clone
}

func withResponseHeader(input *tosPresignResponse, name, value string) *tosPresignResponse {
	clone := clonePresignResponse(input)
	clone.SignedHeaders.Set(name, value)
	return clone
}

func withDuplicateResponseHeader(input *tosPresignResponse, name, value string) *tosPresignResponse {
	clone := clonePresignResponse(input)
	clone.SignedHeaders.Add(name, value)
	return clone
}

func withCaseDuplicateResponseHeader(input *tosPresignResponse, name, value string) *tosPresignResponse {
	clone := clonePresignResponse(input)
	clone.SignedHeaders[name] = []string{value}
	return clone
}

func clonePresignResponse(input *tosPresignResponse) *tosPresignResponse {
	if input == nil {
		return nil
	}
	return &tosPresignResponse{URL: input.URL, SignedHeaders: input.SignedHeaders.Clone()}
}

func httpHeaderFromStrings(input map[string]string) http.Header {
	headers := make(http.Header, len(input))
	for name, value := range input {
		headers[name] = []string{value}
	}
	return headers
}
