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

func (client *sdkTOSClient) GetBucketObjectSetConfiguration(ctx context.Context, input *tos.GetBucketObjectSetConfigurationInput) (*tos.GetBucketObjectSetConfigurationOutput, error) {
	return client.client.GetBucketObjectSetConfiguration(ctx, input)
}

func (client *sdkTOSClient) PutBucketObjectSetConfiguration(ctx context.Context, input *tos.PutBucketObjectSetConfigurationInput) (*tos.PutBucketObjectSetConfigurationOutput, error) {
	return client.client.PutBucketObjectSetConfiguration(ctx, input)
}

func (client *sdkTOSClient) GetObjectSet(ctx context.Context, input *tos.GetObjectSetInput) (*tos.GetObjectSetOutput, error) {
	return client.client.GetObjectSet(ctx, input)
}

func (client *sdkTOSClient) PutObjectSet(ctx context.Context, input *tos.PutObjectSetInput) (*tos.PutObjectSetOutput, error) {
	return client.client.PutObjectSet(ctx, input)
}

func (client *sdkTOSClient) PutObjectSetQuota(ctx context.Context, input *tos.PutObjectSetQuotaInput) (*tos.PutObjectSetQuotaOutput, error) {
	return client.client.PutObjectSetQuota(ctx, input)
}

func (client *sdkTOSClient) GetObjectSetQuota(ctx context.Context, input *tos.GetObjectSetQuotaInput) (*tos.GetObjectSetQuotaOutput, error) {
	return client.client.GetObjectSetQuota(ctx, input)
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

func (client *sdkTOSClient) ListDirectories(ctx context.Context, request tosDirectoryListRequest) (tosDirectoryListResponse, error) {
	output, err := client.client.ListObjectsType2(ctx, &tos.ListObjectsType2Input{
		Bucket: request.Bucket, Prefix: request.Prefix, Delimiter: request.Delimiter,
		ContinuationToken: request.ContinuationToken, MaxKeys: request.MaxKeys,
	})
	if err != nil {
		return tosDirectoryListResponse{}, err
	}
	if output == nil {
		return tosDirectoryListResponse{}, fmt.Errorf("empty TOS directory response")
	}
	directories := make([]string, 0, len(output.CommonPrefixes))
	for _, prefix := range output.CommonPrefixes {
		directories = append(directories, prefix.Prefix)
	}
	return tosDirectoryListResponse{Directories: directories, NextContinuationToken: output.NextContinuationToken}, nil
}

func (client *sdkTOSClient) PutDirectoryMarker(ctx context.Context, bucket, key string) error {
	_, err := client.client.PutObjectV2(ctx, &tos.PutObjectV2Input{
		PutObjectBasicInput: tos.PutObjectBasicInput{
			Bucket: bucket, Key: key, ContentLength: 0, ContentType: "application/octet-stream",
		},
		Content: strings.NewReader(""),
	})
	if err != nil {
		return ErrUnavailable
	}
	return nil
}

func (client *sdkTOSClient) ListArtifacts(ctx context.Context, request tosArtifactListRequest) (tosArtifactListResponse, error) {
	output, err := client.client.ListObjectsType2(ctx, &tos.ListObjectsType2Input{
		Bucket: request.Bucket, Prefix: request.Prefix, Delimiter: request.Delimiter,
		ContinuationToken: request.ContinuationToken, MaxKeys: request.MaxKeys,
	})
	if err != nil {
		return tosArtifactListResponse{}, err
	}
	if output == nil {
		return tosArtifactListResponse{}, fmt.Errorf("empty TOS artifact response")
	}
	directories := make([]string, 0, len(output.CommonPrefixes))
	for _, prefix := range output.CommonPrefixes {
		directories = append(directories, prefix.Prefix)
	}
	objects := make([]tosArtifactObject, 0, len(output.Contents))
	for _, object := range output.Contents {
		objects = append(objects, tosArtifactObject{Key: object.Key, SizeBytes: object.Size, ETag: object.ETag, LastModified: object.LastModified})
	}
	return tosArtifactListResponse{Directories: directories, Objects: objects, NextContinuationToken: output.NextContinuationToken}, nil
}

func (client *sdkTOSClient) CopyObject(ctx context.Context, request tosCopyRequest) error {
	_, err := client.client.CopyObject(ctx, &tos.CopyObjectInput{
		Bucket: request.Bucket, Key: request.DestinationKey,
		SrcBucket: request.Bucket, SrcKey: request.SourceKey,
		ForbidOverwrite: true,
		GenericInput:    tos.GenericInput{RequestHeader: map[string]string{"If-None-Match": "*"}},
	})
	if err == nil {
		return nil
	}
	if code := tos.StatusCode(err); code == http.StatusConflict || code == http.StatusPreconditionFailed {
		return ErrAlreadyExists
	}
	return ErrUnavailable
}

func (client *sdkTOSClient) ReadArtifact(ctx context.Context, request tosArtifactReadRequest) (tosArtifactReadResponse, error) {
	output, err := client.client.GetObjectV2(ctx, &tos.GetObjectV2Input{Bucket: request.Bucket, Key: request.Key})
	if err != nil {
		if tos.StatusCode(err) == http.StatusNotFound {
			return tosArtifactReadResponse{}, ErrNotFound
		}
		return tosArtifactReadResponse{}, ErrUnavailable
	}
	if output == nil || output.Content == nil || output.ContentLength < 0 {
		if output != nil && output.Content != nil {
			_ = output.Content.Close()
		}
		return tosArtifactReadResponse{}, ErrUnavailable
	}
	return tosArtifactReadResponse{
		Content: output.Content, SizeBytes: output.ContentLength, ContentType: output.ContentType,
		ETag: output.ETag, LastModified: output.LastModified,
	}, nil
}

func (client *sdkTOSClient) Put(ctx context.Context, request tosPutRequest) error {
	contentType := request.ContentType
	if contentType == "" {
		contentType = "application/zip"
	}
	_, err := client.client.PutObjectV2(ctx, &tos.PutObjectV2Input{
		PutObjectBasicInput: tos.PutObjectBasicInput{
			Bucket: request.Bucket, Key: request.Key, ContentLength: request.SizeBytes,
			ContentType: contentType, ForbidOverwrite: true,
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

func (client *sdkTOSClient) DeleteObject(ctx context.Context, bucket, key string) error {
	_, err := client.client.DeleteObjectV2(ctx, &tos.DeleteObjectV2Input{Bucket: bucket, Key: key})
	if err != nil {
		if tos.StatusCode(err) == http.StatusNotFound {
			return ErrNotFound
		}
		return ErrUnavailable
	}
	return nil
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
	return strings.HasPrefix(value, "ray-train/tenants/") && !strings.Contains(value, "..") && !strings.Contains(value, "\\") && strings.IndexByte(value, 0) < 0
}
