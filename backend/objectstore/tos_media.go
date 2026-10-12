package objectstore

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/volcengine/ve-tos-golang-sdk/v2/tos"
)

type tosMediaReadRequest struct{ Bucket, Key, Range, ETag string }
type tosMediaReadResponse struct {
	Content            io.ReadCloser
	SizeBytes          int64
	ETag, ContentRange string
}
type tosMediaClient interface {
	HeadMedia(context.Context, string, string) (MediaInfo, error)
	ReadMedia(context.Context, tosMediaReadRequest) (tosMediaReadResponse, error)
}

func (s *TOSStore) HeadMedia(ctx context.Context, root, relative string) (MediaInfo, error) {
	if err := ctx.Err(); err != nil {
		return MediaInfo{}, err
	}
	key, err := scopedTOSDataFileKey(root, relative)
	if err != nil {
		return MediaInfo{}, err
	}
	client, ok := s.client.(tosMediaClient)
	if !ok {
		return MediaInfo{}, ErrUnavailable
	}
	info, err := client.HeadMedia(ctx, s.bucket, key)
	if err != nil {
		return MediaInfo{}, err
	}
	if info.SizeBytes < 0 || !validMediaETag(info.ETag) {
		return MediaInfo{}, ErrUnavailable
	}
	return info, nil
}

func (s *TOSStore) ReadMediaRange(ctx context.Context, root, relative string, info MediaInfo, start, end int64) (ArtifactRead, error) {
	if err := ctx.Err(); err != nil {
		return ArtifactRead{}, err
	}
	key, err := scopedTOSDataFileKey(root, relative)
	if err != nil {
		return ArtifactRead{}, err
	}
	if !validMediaETag(info.ETag) || start < 0 || end < start || end >= info.SizeBytes {
		return ArtifactRead{}, ErrUnavailable
	}
	client, ok := s.client.(tosMediaClient)
	if !ok {
		return ArtifactRead{}, ErrUnavailable
	}
	response, err := client.ReadMedia(ctx, tosMediaReadRequest{Bucket: s.bucket, Key: key, Range: fmt.Sprintf("bytes=%d-%d", start, end), ETag: info.ETag})
	if err != nil {
		if response.Content != nil {
			_ = response.Content.Close()
		}
		return ArtifactRead{}, err
	}
	if response.Content == nil || response.SizeBytes != end-start+1 || response.ContentRange != fmt.Sprintf("bytes %d-%d/%d", start, end, info.SizeBytes) || response.ETag != info.ETag {
		if response.Content != nil {
			_ = response.Content.Close()
		}
		if response.ETag != info.ETag {
			return ArtifactRead{}, ErrMediaChanged
		}
		return ArtifactRead{}, ErrUnavailable
	}
	return ArtifactRead{Content: response.Content, SizeBytes: response.SizeBytes, ETag: response.ETag}, nil
}

func validMediaETag(etag string) bool {
	if len(etag) < 3 || len(etag) > 1024 || etag[0] != '"' || etag[len(etag)-1] != '"' {
		return false
	}
	for _, b := range []byte(etag[1 : len(etag)-1]) {
		if b < 33 || b > 126 || b == '"' {
			return false
		}
	}
	return true
}
func mediaETag(raw string) string {
	if validMediaETag(raw) {
		return raw
	}
	quoted := "\"" + raw + "\""
	if validMediaETag(quoted) {
		return quoted
	}
	return ""
}

func (s *sdkTOSClient) HeadMedia(ctx context.Context, bucket, key string) (MediaInfo, error) {
	output, err := s.client.HeadObjectV2(ctx, &tos.HeadObjectV2Input{Bucket: bucket, Key: key})
	if err != nil {
		return MediaInfo{}, mediaTOSError(err)
	}
	if output == nil {
		return MediaInfo{}, ErrUnavailable
	}
	if strings.EqualFold(output.ObjectType, "Symlink") {
		return MediaInfo{}, ErrUnavailable
	}
	return MediaInfo{SizeBytes: output.ContentLength, ETag: mediaETag(output.ETag), LastModified: output.LastModified}, nil
}
func (s *sdkTOSClient) ReadMedia(ctx context.Context, r tosMediaReadRequest) (tosMediaReadResponse, error) {
	output, err := s.client.GetObjectV2(ctx, &tos.GetObjectV2Input{Bucket: r.Bucket, Key: r.Key, Range: r.Range, IfMatch: r.ETag})
	if err != nil {
		if output != nil && output.Content != nil {
			_ = output.Content.Close()
		}
		return tosMediaReadResponse{}, mediaTOSError(err)
	}
	if output == nil {
		return tosMediaReadResponse{}, ErrUnavailable
	}
	if output.StatusCode != http.StatusPartialContent || strings.EqualFold(output.ObjectType, "Symlink") {
		if output.Content != nil {
			_ = output.Content.Close()
		}
		return tosMediaReadResponse{}, ErrUnavailable
	}
	return tosMediaReadResponse{Content: output.Content, SizeBytes: output.ContentLength, ETag: mediaETag(output.ETag), ContentRange: strings.TrimSpace(output.ContentRange)}, nil
}
func mediaTOSError(err error) error {
	switch tos.StatusCode(err) {
	case http.StatusNotFound:
		return ErrNotFound
	case http.StatusPreconditionFailed:
		return ErrMediaChanged
	}
	return ErrUnavailable
}
