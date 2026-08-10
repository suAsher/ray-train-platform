package objectstore

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"ray-train-platform-backend/domain"
)

type TOSConfig struct {
	Endpoint      string
	Region        string
	Bucket        string
	AccessKey     string
	SecretKey     string
	SecurityToken string
	Transport     http.RoundTripper
	Now           func() time.Time
}

type tosPresignRequest struct {
	Bucket         string
	Key            string
	ExpiresSeconds int64
	Headers        map[string]string
}

type tosPresignResponse struct {
	URL           string
	SignedHeaders http.Header
}

type tosClient interface {
	Presign(context.Context, tosPresignRequest) (*tosPresignResponse, error)
	Head(context.Context, string, string) (ObjectInfo, error)
}

type TOSStore struct {
	client   tosClient
	endpoint *url.URL
	bucket   string
	now      func() time.Time
}

func newTOSStoreWithClient(config TOSConfig, client tosClient) (*TOSStore, error) {
	endpoint, err := validateTOSConfig(config)
	if err != nil {
		return nil, err
	}
	if client == nil {
		return nil, fmt.Errorf("TOS client is required")
	}
	now := config.Now
	if now == nil {
		now = time.Now
	}
	return &TOSStore{client: client, endpoint: endpoint, bucket: config.Bucket, now: now}, nil
}

func validateTOSConfig(config TOSConfig) (*url.URL, error) {
	endpoint, err := url.Parse(config.Endpoint)
	if err != nil || endpoint.Scheme != "https" || endpoint.Host == "" || endpoint.User != nil || (endpoint.Path != "" && endpoint.Path != "/") || endpoint.RawQuery != "" || endpoint.Fragment != "" {
		return nil, fmt.Errorf("TOS endpoint must be an HTTPS origin")
	}
	if strings.TrimSpace(config.Region) == "" || strings.TrimSpace(config.Bucket) == "" || strings.TrimSpace(config.AccessKey) == "" || strings.TrimSpace(config.SecretKey) == "" {
		return nil, fmt.Errorf("TOS region, bucket, access key and secret key are required")
	}
	return endpoint, nil
}

func (store *TOSStore) PresignPut(ctx context.Context, objectKey, digest string, sizeBytes int64, ttl time.Duration) (PresignedPut, error) {
	if err := ctx.Err(); err != nil {
		return PresignedPut{}, err
	}
	if objectKey == "" || sizeBytes < 1 || sizeBytes > domain.MaxSourceArtifactSize || ttl <= 0 || ttl > 7*24*time.Hour || ttl%time.Second != 0 {
		return PresignedPut{}, fmt.Errorf("invalid presign request")
	}
	if err := domain.ValidateSourceArtifactSHA256(digest); err != nil {
		return PresignedPut{}, fmt.Errorf("invalid presign digest")
	}
	fullHeaders := fullUploadHeaders(sizeBytes, digest)
	response, err := store.client.Presign(ctx, tosPresignRequest{
		Bucket: store.bucket, Key: objectKey, ExpiresSeconds: int64(ttl / time.Second),
		Headers: cloneHeaders(fullHeaders),
	})
	if err != nil {
		return PresignedPut{}, fmt.Errorf("%w: presign request failed", ErrUnavailable)
	}
	if err := store.validatePresignResponse(response, objectKey, fullHeaders, ttl); err != nil {
		return PresignedPut{}, fmt.Errorf("%w: invalid presign response", ErrUnavailable)
	}
	return PresignedPut{
		URL: response.URL, RequiredHeaders: browserUploadHeaders(digest), ContentLength: sizeBytes,
		ExpiresAt: store.now().UTC().Add(ttl),
	}, nil
}

func (store *TOSStore) validatePresignResponse(response *tosPresignResponse, objectKey string, required map[string]string, ttl time.Duration) error {
	if response == nil {
		return fmt.Errorf("missing response")
	}
	parsed, err := url.Parse(response.URL)
	if err != nil || !parsed.IsAbs() || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" || parsed.Path == "" || parsed.Path == "/" {
		return fmt.Errorf("invalid URL")
	}
	allowedBucketHost := store.bucket + "." + store.endpoint.Host
	pathStyle := strings.EqualFold(parsed.Host, store.endpoint.Host)
	virtualHost := strings.EqualFold(parsed.Host, allowedBucketHost)
	if !pathStyle && !virtualHost {
		return fmt.Errorf("invalid host")
	}
	if parsed.RawPath != "" {
		return fmt.Errorf("ambiguous path")
	}
	decodedPath, err := url.PathUnescape(parsed.EscapedPath())
	if err != nil {
		return fmt.Errorf("invalid path")
	}
	expectedPath := "/" + objectKey
	if pathStyle {
		expectedPath = "/" + store.bucket + "/" + objectKey
	}
	if decodedPath != expectedPath {
		return fmt.Errorf("invalid path")
	}
	query := parsed.Query()
	if !unambiguousValues(query) {
		return fmt.Errorf("ambiguous signing query")
	}
	for _, name := range []string{"X-Tos-Signature", "X-Tos-Algorithm", "X-Tos-Credential", "X-Tos-Date", "X-Tos-Expires", "X-Tos-SignedHeaders"} {
		if queryValue(query, name) == "" {
			return fmt.Errorf("missing signing query")
		}
	}
	if queryValue(query, "X-Tos-Expires") != strconv.FormatInt(int64(ttl/time.Second), 10) {
		return fmt.Errorf("wrong signing expiry")
	}
	signed, ok := splitSignedHeaders(queryValue(query, "X-Tos-SignedHeaders"))
	if !ok || !unambiguousValues(response.SignedHeaders) {
		return fmt.Errorf("ambiguous signed header")
	}
	for name, value := range required {
		if _, ok := signed[strings.ToLower(name)]; !ok {
			return fmt.Errorf("missing signed header")
		}
		actual, ok := headerValue(response.SignedHeaders, name)
		if !ok || actual != value {
			return fmt.Errorf("wrong signed header")
		}
	}
	return nil
}

func (store *TOSStore) Head(ctx context.Context, objectKey string) (ObjectInfo, error) {
	if objectKey == "" {
		return ObjectInfo{}, fmt.Errorf("object key is required")
	}
	return store.client.Head(ctx, store.bucket, objectKey)
}

func fullUploadHeaders(sizeBytes int64, digest string) map[string]string {
	headers := browserUploadHeaders(digest)
	headers["Content-Length"] = strconv.FormatInt(sizeBytes, 10)
	return headers
}

func browserUploadHeaders(digest string) map[string]string {
	return map[string]string{
		"Content-Type":           "application/zip",
		"If-None-Match":          "*",
		"x-tos-forbid-overwrite": "true",
		"x-tos-meta-sha256":      digest,
	}
}

func queryValue(query url.Values, name string) string {
	for key, values := range query {
		if strings.EqualFold(key, name) && len(values) > 0 {
			return values[0]
		}
	}
	return ""
}

func splitSignedHeaders(value string) (map[string]struct{}, bool) {
	result := make(map[string]struct{})
	for _, header := range strings.Split(strings.ToLower(value), ";") {
		if header == "" {
			continue
		}
		if _, exists := result[header]; exists {
			return nil, false
		}
		result[header] = struct{}{}
	}
	return result, true
}

func unambiguousValues(values map[string][]string) bool {
	seen := make(map[string]struct{}, len(values))
	for key, entries := range values {
		normalized := strings.ToLower(key)
		if _, duplicate := seen[normalized]; duplicate || len(entries) != 1 {
			return false
		}
		seen[normalized] = struct{}{}
	}
	return true
}

func headerValue(headers http.Header, name string) (string, bool) {
	for key, values := range headers {
		if strings.EqualFold(key, name) {
			if len(values) != 1 {
				return "", false
			}
			return values[0], true
		}
	}
	return "", false
}

func cloneHeaders(headers map[string]string) map[string]string {
	clone := make(map[string]string, len(headers))
	for key, value := range headers {
		clone[key] = value
	}
	return clone
}
