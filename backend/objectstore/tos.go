package objectstore

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/volcengine/ve-tos-golang-sdk/v2/tos"
	"github.com/volcengine/ve-tos-golang-sdk/v2/tos/enum"
	"ray-train-platform-backend/domain"
)

type sdkTOSClient struct {
	client *tos.ClientV2
}

func NewTOSStore(config TOSConfig) (*TOSStore, error) {
	if _, err := validateTOSConfig(config); err != nil {
		return nil, err
	}
	credentials := tos.NewStaticCredentials(config.AccessKey, config.SecretKey)
	if config.SecurityToken != "" {
		credentials.WithSecurityToken(config.SecurityToken)
	}
	options := []tos.ClientOption{tos.WithRegion(config.Region), tos.WithCredentials(credentials)}
	if config.Transport != nil {
		options = append(options, tos.WithHTTPTransport(config.Transport))
	}
	client, err := tos.NewClientV2(config.Endpoint, options...)
	if err != nil {
		return nil, fmt.Errorf("initialize TOS client")
	}
	return newTOSStoreWithClient(config, &sdkTOSClient{client: client})
}

func (client *sdkTOSClient) Presign(ctx context.Context, request tosPresignRequest) (*tosPresignResponse, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	output, err := client.client.PreSignedURL(&tos.PreSignedURLInput{
		HTTPMethod: enum.HttpMethodPut, Bucket: request.Bucket, Key: request.Key,
		Expires: request.ExpiresSeconds, Header: cloneHeaders(request.Headers), IsSignedAllHeaders: true,
	})
	if err != nil {
		return nil, err
	}
	if output == nil {
		return nil, nil
	}
	return &tosPresignResponse{URL: output.SignedUrl, SignedHeaders: httpHeaderFromMap(request.Headers)}, nil
}

func httpHeaderFromMap(input map[string]string) http.Header {
	headers := make(http.Header, len(input))
	for name, value := range input {
		headers[name] = []string{value}
	}
	return headers
}

func (client *sdkTOSClient) Head(ctx context.Context, bucket, objectKey string) (ObjectInfo, error) {
	output, err := client.client.HeadObjectV2(ctx, &tos.HeadObjectV2Input{Bucket: bucket, Key: objectKey})
	if err != nil {
		if tos.StatusCode(err) == http.StatusNotFound {
			return ObjectInfo{}, ErrNotFound
		}
		return ObjectInfo{}, ErrUnavailable
	}
	if output == nil {
		return ObjectInfo{}, ErrUnavailable
	}
	metadata := make(map[string]string)
	if output.Meta != nil {
		output.Meta.Range(func(key, value string) bool {
			metadata[strings.ToLower(key)] = value
			return true
		})
	}
	return ObjectInfo{SizeBytes: output.ContentLength, Metadata: metadata}, nil
}

func (client *sdkTOSClient) Put(ctx context.Context, request tosPutRequest) error {
	_, err := client.client.PutObjectV2(ctx, &tos.PutObjectV2Input{
		PutObjectBasicInput: tos.PutObjectBasicInput{
			Bucket: request.Bucket, Key: request.Key, ContentLength: request.SizeBytes,
			ContentType: "application/zip", ForbidOverwrite: true,
			Meta: map[string]string{"sha256": request.SHA256},
		},
		Content:      request.Body,
		GenericInput: tos.GenericInput{RequestHeader: map[string]string{"If-None-Match": "*"}},
	})
	if err == nil {
		return nil
	}
	if code := tos.StatusCode(err); code == http.StatusConflict || code == http.StatusPreconditionFailed {
		return ErrAlreadyExists
	}
	return ErrUnavailable
}

func (store *TOSStore) Put(ctx context.Context, objectKey, digest string, sizeBytes int64, body io.Reader) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if body == nil || !safePutObjectKey(objectKey) || sizeBytes < 1 || sizeBytes > domain.MaxSourceArtifactSize {
		return fmt.Errorf("invalid put request")
	}
	if err := domain.ValidateSourceArtifactSHA256(digest); err != nil {
		return fmt.Errorf("invalid put digest")
	}
	client, ok := store.client.(interface {
		Put(context.Context, tosPutRequest) error
	})
	if !ok {
		return ErrUnavailable
	}
	if err := client.Put(ctx, tosPutRequest{Bucket: store.bucket, Key: objectKey, SHA256: digest, SizeBytes: sizeBytes, Body: body}); err != nil {
		if err == ErrAlreadyExists {
			return ErrAlreadyExists
		}
		return ErrUnavailable
	}
	return nil
}

func safePutObjectKey(value string) bool {
	return strings.HasPrefix(value, "tenants/") && !strings.Contains(value, "..") && !strings.Contains(value, "\\") && strings.IndexByte(value, 0) < 0
}
