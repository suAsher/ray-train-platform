package objectstore

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
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

type tosMultipartCreateRequest struct {
	Bucket, Key, ContentType string
}

type tosMultipartPartRequest struct {
	Bucket, Key, UploadID string
	PartNumber            int
	SizeBytes             int64
	Body                  io.Reader
}

type tosMultipartCompleteRequest struct {
	Bucket, Key, UploadID string
	Parts                 []MultipartPart
}

type tosMultipartAbortRequest struct{ Bucket, Key, UploadID string }

type tosMultipartClient interface {
	CreateDataMultipart(context.Context, tosMultipartCreateRequest) (string, error)
	UploadDataPart(context.Context, tosMultipartPartRequest) (string, error)
	CompleteDataMultipart(context.Context, tosMultipartCompleteRequest) error
	AbortDataMultipart(context.Context, tosMultipartAbortRequest) error
}

type tosDirectoryListRequest struct {
	Bucket            string
	Prefix            string
	Delimiter         string
	ContinuationToken string
	MaxKeys           int
}

type tosDirectoryListResponse struct {
	Directories           []string
	NextContinuationToken string
}

type tosDirectoryClient interface {
	ListDirectories(context.Context, tosDirectoryListRequest) (tosDirectoryListResponse, error)
}

type tosDirectoryMarkerClient interface {
	PutDirectoryMarker(context.Context, string, string) error
}

type tosArtifactListRequest struct {
	Bucket            string
	Prefix            string
	Delimiter         string
	ContinuationToken string
	MaxKeys           int
}

type tosArtifactObject struct {
	Key          string
	SizeBytes    int64
	ETag         string
	LastModified time.Time
}

type tosArtifactListResponse struct {
	Directories           []string
	Objects               []tosArtifactObject
	NextContinuationToken string
}

type tosArtifactClient interface {
	ListArtifacts(context.Context, tosArtifactListRequest) (tosArtifactListResponse, error)
}

type tosCopyRequest struct {
	Bucket         string
	SourceKey      string
	DestinationKey string
}

type tosCopyClient interface {
	CopyObject(context.Context, tosCopyRequest) error
}

type tosArtifactReadRequest struct {
	Bucket string
	Key    string
}

type tosArtifactReadResponse struct {
	Content      io.ReadCloser
	SizeBytes    int64
	ContentType  string
	ETag         string
	LastModified time.Time
}

type tosArtifactReadClient interface {
	ReadArtifact(context.Context, tosArtifactReadRequest) (tosArtifactReadResponse, error)
}

const maxDirectoryPageSize = 100

const maxWorkspaceSnapshotObjects = 10000

const (
	maxDataUploadBytes int64 = 5 * 1024 * 1024 * 1024
	dataUploadTTL            = 15 * time.Minute
)

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

// ListDirectories lists direct child prefixes below a catalogue-controlled
// root. A caller can never change the bucket or request a parent/sibling
// prefix: relativePath is validated and joined only after rootPrefix has been
// canonicalised.
func (store *TOSStore) ListDirectories(ctx context.Context, rootPrefix, relativePath, cursor string, limit int) (DirectoryPage, error) {
	if err := ctx.Err(); err != nil {
		return DirectoryPage{}, err
	}
	prefix, err := scopedTOSDirectoryPrefix(rootPrefix, relativePath)
	if err != nil {
		return DirectoryPage{}, err
	}
	if len(cursor) > 4096 || strings.ContainsRune(cursor, '\x00') {
		return DirectoryPage{}, fmt.Errorf("invalid directory cursor")
	}
	if limit <= 0 || limit > maxDirectoryPageSize {
		limit = maxDirectoryPageSize
	}
	client, ok := store.client.(tosDirectoryClient)
	if !ok {
		return DirectoryPage{}, ErrUnavailable
	}
	response, err := client.ListDirectories(ctx, tosDirectoryListRequest{
		Bucket: store.bucket, Prefix: prefix, Delimiter: "/", ContinuationToken: cursor, MaxKeys: limit,
	})
	if err != nil {
		return DirectoryPage{}, ErrUnavailable
	}
	return directoryPageForPrefix(prefix, response), nil
}

// EnsurePersonalDataDirectories materializes the fixed prefixes used by a
// new user's persistent TOS-backed workspace. Kubernetes rejects a subPath
// mount when its target directory does not exist, so marker objects are
// created before the platform marks a personal PVC usable.
func (store *TOSStore) EnsurePersonalDataDirectories(ctx context.Context, rootPrefix string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	root, err := scopedTOSDirectoryPrefix(rootPrefix, "")
	if err != nil {
		return err
	}
	client, ok := store.client.(tosDirectoryMarkerClient)
	if !ok {
		return ErrUnavailable
	}
	for _, directory := range []string{"workspace", "files", "runs", "snapshots"} {
		if err := client.PutDirectoryMarker(ctx, store.bucket, root+directory+"/.ray-train-keep"); err != nil {
			return ErrUnavailable
		}
	}
	return nil
}

// EnsureDataDirectory makes an empty governed TOS prefix visible to FSX. A
// team/public binding is always derived by the control plane; this method does
// not accept a user-provided bucket or absolute object key.
func (store *TOSStore) EnsureDataDirectory(ctx context.Context, rootPrefix string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	root, err := scopedTOSDirectoryPrefix(rootPrefix, "")
	if err != nil {
		return err
	}
	client, ok := store.client.(tosDirectoryMarkerClient)
	if !ok {
		return ErrUnavailable
	}
	if err := client.PutDirectoryMarker(ctx, store.bucket, root+".ray-train-keep"); err != nil {
		return ErrUnavailable
	}
	return nil
}

func (store *TOSStore) ListDataEntries(ctx context.Context, rootPrefix, relativePath, cursor string, limit int) (DataEntryPage, error) {
	if err := ctx.Err(); err != nil {
		return DataEntryPage{}, err
	}
	prefix, err := scopedTOSDirectoryPrefix(rootPrefix, relativePath)
	if err != nil {
		return DataEntryPage{}, err
	}
	if len(cursor) > 4096 || strings.ContainsRune(cursor, '\x00') {
		return DataEntryPage{}, fmt.Errorf("invalid data entry cursor")
	}
	if limit <= 0 || limit > maxDirectoryPageSize {
		limit = maxDirectoryPageSize
	}
	client, ok := store.client.(tosArtifactClient)
	if !ok {
		return DataEntryPage{}, ErrUnavailable
	}
	response, err := client.ListArtifacts(ctx, tosArtifactListRequest{
		Bucket: store.bucket, Prefix: prefix, Delimiter: "/", ContinuationToken: cursor, MaxKeys: limit,
	})
	if err != nil {
		return DataEntryPage{}, ErrUnavailable
	}
	return dataEntryPageForPrefix(prefix, response), nil
}

func (store *TOSStore) PresignDataPut(ctx context.Context, rootPrefix, relativePath, contentType string, sizeBytes int64, ttl time.Duration) (PresignedPut, error) {
	if err := ctx.Err(); err != nil {
		return PresignedPut{}, err
	}
	key, err := scopedTOSDataFileKey(rootPrefix, relativePath)
	if err != nil || sizeBytes < 0 || sizeBytes > maxDataUploadBytes || ttl != dataUploadTTL || strings.TrimSpace(contentType) == "" || strings.ContainsAny(contentType, "\r\n") {
		return PresignedPut{}, fmt.Errorf("invalid data upload request")
	}
	response, err := store.client.Presign(ctx, tosPresignRequest{
		Bucket: store.bucket, Key: key, ExpiresSeconds: int64(ttl / time.Second), Headers: fullDataUploadHeaders(contentType, sizeBytes),
	})
	if err != nil || response == nil {
		return PresignedPut{}, ErrUnavailable
	}
	if err := store.validateDataUploadPresignResponse(response, key, contentType, sizeBytes, ttl); err != nil {
		return PresignedPut{}, ErrUnavailable
	}
	return PresignedPut{URL: response.URL, RequiredHeaders: dataUploadHeaders(contentType), ContentLength: sizeBytes, ExpiresAt: store.now().UTC().Add(ttl)}, nil
}

// PutData streams a data-space file into the caller's own root. The platform
// relays these bytes instead of handing out a presigned URL, because the object
// store is only reachable from inside the cluster: a browser told to upload
// directly to it can never connect.
func (store *TOSStore) PutData(ctx context.Context, rootPrefix, relativePath, contentType string, sizeBytes int64, body io.Reader) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	key, err := scopedTOSDataFileKey(rootPrefix, relativePath)
	if err != nil {
		return err
	}
	if body == nil || sizeBytes < 0 || sizeBytes > maxDataUploadBytes || strings.TrimSpace(contentType) == "" || strings.ContainsAny(contentType, "\r\n") {
		return fmt.Errorf("invalid data upload request")
	}
	client, ok := store.client.(interface {
		PutData(context.Context, tosDataPutRequest) error
	})
	if !ok {
		return ErrUnavailable
	}
	if err := client.PutData(ctx, tosDataPutRequest{Bucket: store.bucket, Key: key, ContentType: contentType, SizeBytes: sizeBytes, Body: body}); err != nil {
		return ErrUnavailable
	}
	return nil
}

func (store *TOSStore) CreateDataMultipart(ctx context.Context, rootPrefix, relativePath, contentType string) (string, error) {
	key, err := scopedTOSDataFileKey(rootPrefix, relativePath)
	if err != nil || strings.TrimSpace(contentType) == "" || strings.ContainsAny(contentType, "\r\n") {
		return "", fmt.Errorf("invalid multipart upload request")
	}
	client, ok := store.client.(tosMultipartClient)
	if !ok {
		return "", ErrUnavailable
	}
	uploadID, err := client.CreateDataMultipart(ctx, tosMultipartCreateRequest{Bucket: store.bucket, Key: key, ContentType: contentType})
	if err != nil || strings.TrimSpace(uploadID) == "" {
		return "", ErrUnavailable
	}
	return uploadID, nil
}

func (store *TOSStore) UploadDataPart(ctx context.Context, rootPrefix, relativePath, uploadID string, partNumber int, sizeBytes int64, body io.Reader) (string, error) {
	key, err := scopedTOSDataFileKey(rootPrefix, relativePath)
	if err != nil || strings.TrimSpace(uploadID) == "" || partNumber < 1 || partNumber > domain.DataSpaceMaxMultipartParts || sizeBytes < 1 || sizeBytes > domain.DataSpaceMaxPartBytes || body == nil {
		return "", fmt.Errorf("invalid multipart part request")
	}
	client, ok := store.client.(tosMultipartClient)
	if !ok {
		return "", ErrUnavailable
	}
	etag, err := client.UploadDataPart(ctx, tosMultipartPartRequest{Bucket: store.bucket, Key: key, UploadID: uploadID, PartNumber: partNumber, SizeBytes: sizeBytes, Body: body})
	if err != nil || strings.TrimSpace(etag) == "" {
		return "", ErrUnavailable
	}
	return etag, nil
}

func (store *TOSStore) CompleteDataMultipart(ctx context.Context, rootPrefix, relativePath, uploadID string, parts []MultipartPart) error {
	key, err := scopedTOSDataFileKey(rootPrefix, relativePath)
	if err != nil || strings.TrimSpace(uploadID) == "" || len(parts) < 1 || len(parts) > domain.DataSpaceMaxMultipartParts {
		return fmt.Errorf("invalid multipart completion request")
	}
	for index, part := range parts {
		if part.PartNumber != index+1 || part.SizeBytes < 1 || part.SizeBytes > domain.DataSpaceMaxPartBytes || strings.TrimSpace(part.ETag) == "" {
			return fmt.Errorf("invalid multipart completion part")
		}
	}
	client, ok := store.client.(tosMultipartClient)
	if !ok {
		return ErrUnavailable
	}
	if err := client.CompleteDataMultipart(ctx, tosMultipartCompleteRequest{Bucket: store.bucket, Key: key, UploadID: uploadID, Parts: append([]MultipartPart(nil), parts...)}); err != nil {
		return ErrUnavailable
	}
	return nil
}

func (store *TOSStore) AbortDataMultipart(ctx context.Context, rootPrefix, relativePath, uploadID string) error {
	key, err := scopedTOSDataFileKey(rootPrefix, relativePath)
	if err != nil || strings.TrimSpace(uploadID) == "" {
		return fmt.Errorf("invalid multipart abort request")
	}
	client, ok := store.client.(tosMultipartClient)
	if !ok {
		return ErrUnavailable
	}
	if err := client.AbortDataMultipart(ctx, tosMultipartAbortRequest{Bucket: store.bucket, Key: key, UploadID: uploadID}); err != nil {
		return ErrUnavailable
	}
	return nil
}

func (store *TOSStore) ReadData(ctx context.Context, rootPrefix, relativePath string) (ArtifactRead, error) {
	if err := ctx.Err(); err != nil {
		return ArtifactRead{}, err
	}
	key, err := scopedTOSDataFileKey(rootPrefix, relativePath)
	if err != nil {
		return ArtifactRead{}, err
	}
	client, ok := store.client.(tosArtifactReadClient)
	if !ok {
		return ArtifactRead{}, ErrUnavailable
	}
	response, err := client.ReadArtifact(ctx, tosArtifactReadRequest{Bucket: store.bucket, Key: key})
	if err != nil {
		if err == ErrNotFound {
			return ArtifactRead{}, ErrNotFound
		}
		return ArtifactRead{}, ErrUnavailable
	}
	if response.Content == nil || response.SizeBytes < 0 {
		if response.Content != nil {
			_ = response.Content.Close()
		}
		return ArtifactRead{}, ErrUnavailable
	}
	return ArtifactRead{Content: response.Content, SizeBytes: response.SizeBytes, ContentType: strings.TrimSpace(response.ContentType)}, nil
}

func (store *TOSStore) CreateDataDirectory(ctx context.Context, rootPrefix, relativePath string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	prefix, err := scopedTOSDirectoryPrefix(rootPrefix, relativePath)
	if err != nil || relativePath == "" {
		return fmt.Errorf("invalid data directory path")
	}
	client, ok := store.client.(tosDirectoryMarkerClient)
	if !ok {
		return ErrUnavailable
	}
	if err := client.PutDirectoryMarker(ctx, store.bucket, prefix+".ray-train-keep"); err != nil {
		return ErrUnavailable
	}
	return nil
}

// SnapshotWorkspace makes an immutable server-side copy of one personal
// workspace directory. Empty directories are not meaningful in TOS, so a
// marker is copied/created only after at least one ordinary source object was
// found. It excludes platform markers and never accepts an arbitrary bucket,
// source object, or destination from an HTTP caller.
func (store *TOSStore) SnapshotWorkspace(ctx context.Context, workspaceRoot, sourcePath, snapshotRoot string) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	sourcePrefix, err := scopedTOSDirectoryPrefix(workspaceRoot, sourcePath)
	if err != nil {
		return 0, fmt.Errorf("invalid workspace source")
	}
	destinationPrefix, err := scopedTOSDirectoryPrefix(snapshotRoot, "")
	if err != nil || sourcePrefix == destinationPrefix {
		return 0, fmt.Errorf("invalid workspace snapshot destination")
	}
	client, ok := store.client.(interface {
		tosArtifactClient
		tosCopyClient
		tosDirectoryMarkerClient
	})
	if !ok {
		return 0, ErrUnavailable
	}
	continuation := ""
	copied := 0
	for {
		response, listErr := client.ListArtifacts(ctx, tosArtifactListRequest{
			Bucket: store.bucket, Prefix: sourcePrefix, Delimiter: "", ContinuationToken: continuation, MaxKeys: maxDirectoryPageSize,
		})
		if listErr != nil {
			return 0, ErrUnavailable
		}
		for _, object := range response.Objects {
			if !strings.HasPrefix(object.Key, sourcePrefix) {
				continue
			}
			relative := strings.TrimPrefix(object.Key, sourcePrefix)
			if relative == "" || hasPlatformWorkspacePath(relative) {
				continue
			}
			if copied >= maxWorkspaceSnapshotObjects {
				return 0, fmt.Errorf("workspace snapshot has too many files")
			}
			if err := client.CopyObject(ctx, tosCopyRequest{Bucket: store.bucket, SourceKey: object.Key, DestinationKey: destinationPrefix + relative}); err != nil {
				return 0, ErrUnavailable
			}
			copied++
		}
		if response.NextContinuationToken == "" {
			break
		}
		if response.NextContinuationToken == continuation {
			return 0, ErrUnavailable
		}
		continuation = response.NextContinuationToken
	}
	if copied == 0 {
		return 0, fmt.Errorf("workspace source does not contain files")
	}
	if err := client.PutDirectoryMarker(ctx, store.bucket, destinationPrefix+".ray-train-snapshot"); err != nil {
		return 0, ErrUnavailable
	}
	return copied, nil
}

func hasPlatformWorkspacePath(relative string) bool {
	for _, segment := range strings.Split(relative, "/") {
		if strings.HasPrefix(segment, ".ray-train-") {
			return true
		}
	}
	return false
}

func scopedTOSDataFileKey(rootPrefix, relativePath string) (string, error) {
	root, err := scopedTOSDirectoryPrefix(rootPrefix, "")
	if err != nil {
		return "", err
	}
	path, err := domain.NormalizeStorageRelativePath(relativePath)
	if err != nil || path == "" || strings.HasPrefix(path, ".ray-train-") {
		return "", fmt.Errorf("invalid data file path")
	}
	return root + path, nil
}

func dataUploadHeaders(contentType string) map[string]string {
	return map[string]string{"Content-Type": contentType}
}

func fullDataUploadHeaders(contentType string, sizeBytes int64) map[string]string {
	headers := dataUploadHeaders(contentType)
	headers["Content-Length"] = strconv.FormatInt(sizeBytes, 10)
	return headers
}

func (store *TOSStore) validateDataUploadPresignResponse(response *tosPresignResponse, objectKey, contentType string, sizeBytes int64, ttl time.Duration) error {
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
	if !pathStyle && !virtualHost || parsed.RawPath != "" {
		return fmt.Errorf("invalid URL host")
	}
	decodedPath, err := url.PathUnescape(parsed.EscapedPath())
	if err != nil {
		return fmt.Errorf("invalid URL path")
	}
	expectedPath := "/" + objectKey
	if pathStyle {
		expectedPath = "/" + store.bucket + "/" + objectKey
	}
	if decodedPath != expectedPath {
		return fmt.Errorf("invalid URL path")
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
	for name, value := range fullDataUploadHeaders(contentType, sizeBytes) {
		if _, ok := signed[strings.ToLower(name)]; !ok {
			return fmt.Errorf("missing signed upload header")
		}
		actual, ok := headerValue(response.SignedHeaders, name)
		if !ok || actual != value {
			return fmt.Errorf("wrong upload header")
		}
	}
	return nil
}

// ListArtifactEntries returns direct children below one task output root. The
// client receives only a name and a relative path; the TOS prefix remains an
// internal detail and cannot escape to a sibling task output.
func (store *TOSStore) ListArtifactEntries(ctx context.Context, taskRoot, relativePath, cursor string, limit int) (ArtifactPage, error) {
	if err := ctx.Err(); err != nil {
		return ArtifactPage{}, err
	}
	prefix, err := scopedTOSDirectoryPrefix(taskRoot, relativePath)
	if err != nil {
		return ArtifactPage{}, err
	}
	if len(cursor) > 4096 || strings.ContainsRune(cursor, '\x00') {
		return ArtifactPage{}, fmt.Errorf("invalid artifact cursor")
	}
	if limit <= 0 || limit > maxDirectoryPageSize {
		limit = maxDirectoryPageSize
	}
	client, ok := store.client.(tosArtifactClient)
	if !ok {
		return ArtifactPage{}, ErrUnavailable
	}
	response, err := client.ListArtifacts(ctx, tosArtifactListRequest{
		Bucket: store.bucket, Prefix: prefix, Delimiter: "/", ContinuationToken: cursor, MaxKeys: limit,
	})
	if err != nil {
		return ArtifactPage{}, ErrUnavailable
	}
	return artifactPageForPrefix(prefix, response), nil
}

// ReadArtifact opens a single file below the server-computed task root. It
// does not accept a raw TOS key and refuses root/directory reads.
func (store *TOSStore) ReadArtifact(ctx context.Context, taskRoot, relativePath string) (ArtifactRead, error) {
	if err := ctx.Err(); err != nil {
		return ArtifactRead{}, err
	}
	root, err := scopedTOSDirectoryPrefix(taskRoot, "")
	if err != nil {
		return ArtifactRead{}, err
	}
	path, err := domain.NormalizeStorageRelativePath(relativePath)
	if err != nil || path == "" {
		return ArtifactRead{}, fmt.Errorf("invalid artifact path")
	}
	key := root + path
	client, ok := store.client.(tosArtifactReadClient)
	if !ok {
		return ArtifactRead{}, ErrUnavailable
	}
	response, err := client.ReadArtifact(ctx, tosArtifactReadRequest{Bucket: store.bucket, Key: key})
	if err != nil {
		if err == ErrNotFound {
			return ArtifactRead{}, ErrNotFound
		}
		return ArtifactRead{}, ErrUnavailable
	}
	if response.Content == nil || response.SizeBytes < 0 {
		if response.Content != nil {
			_ = response.Content.Close()
		}
		return ArtifactRead{}, ErrUnavailable
	}
	return ArtifactRead{Content: response.Content, SizeBytes: response.SizeBytes, ContentType: strings.TrimSpace(response.ContentType)}, nil
}

func scopedTOSDirectoryPrefix(rootPrefix, relativePath string) (string, error) {
	root, err := domain.NormalizeStorageRelativePath(strings.TrimSuffix(strings.TrimSpace(rootPrefix), "/"))
	if err != nil || root == "" {
		return "", fmt.Errorf("invalid storage root prefix")
	}
	relative, err := domain.NormalizeStorageRelativePath(relativePath)
	if err != nil {
		return "", err
	}
	if relative == "" {
		return root + "/", nil
	}
	return root + "/" + relative + "/", nil
}

func directoryPageForPrefix(requestPrefix string, response tosDirectoryListResponse) DirectoryPage {
	directories := make([]string, 0, len(response.Directories))
	seen := make(map[string]struct{}, len(response.Directories))
	for _, fullPrefix := range response.Directories {
		// CommonPrefixes from TOS are directory-like prefixes and always end in
		// a slash. Keep this check even though the SDK adapter already draws
		// from CommonPrefixes so a faulty adapter can never turn an object key
		// into a browser result.
		if !strings.HasPrefix(fullPrefix, requestPrefix) || !strings.HasSuffix(fullPrefix, "/") {
			continue
		}
		child := strings.TrimSuffix(strings.TrimPrefix(fullPrefix, requestPrefix), "/")
		if child == "" || strings.Contains(child, "/") {
			continue
		}
		if _, err := domain.NormalizeStorageRelativePath(child); err != nil {
			continue
		}
		if _, exists := seen[child]; exists {
			continue
		}
		seen[child] = struct{}{}
		directories = append(directories, child)
	}
	sort.Strings(directories)
	return DirectoryPage{Directories: directories, NextCursor: response.NextContinuationToken}
}

func dataEntryPageForPrefix(requestPrefix string, response tosArtifactListResponse) DataEntryPage {
	entries := make([]DataEntry, 0, len(response.Directories)+len(response.Objects))
	seen := make(map[string]struct{}, len(response.Directories)+len(response.Objects))
	for _, fullPrefix := range response.Directories {
		name, ok := directArtifactChild(requestPrefix, fullPrefix, true)
		if !ok || name == ".ray-train-keep" {
			continue
		}
		seen[name] = struct{}{}
		entries = append(entries, DataEntry{Name: name, Type: DataEntryDirectory})
	}
	for _, object := range response.Objects {
		name, ok := directArtifactChild(requestPrefix, object.Key, false)
		if !ok || name == ".ray-train-keep" {
			continue
		}
		if _, exists := seen[name]; exists {
			continue
		}
		seen[name] = struct{}{}
		entries = append(entries, DataEntry{Name: name, Type: DataEntryFile, SizeBytes: object.SizeBytes, LastModified: object.LastModified.UTC()})
	}
	sort.Slice(entries, func(left, right int) bool {
		if entries[left].Type != entries[right].Type {
			return entries[left].Type == DataEntryDirectory
		}
		return entries[left].Name < entries[right].Name
	})
	return DataEntryPage{Entries: entries, NextCursor: response.NextContinuationToken}
}

func artifactPageForPrefix(requestPrefix string, response tosArtifactListResponse) ArtifactPage {
	entries := make([]ArtifactEntry, 0, len(response.Directories)+len(response.Objects))
	seen := make(map[string]struct{}, len(response.Directories)+len(response.Objects))
	for _, fullPrefix := range response.Directories {
		name, ok := directArtifactChild(requestPrefix, fullPrefix, true)
		if !ok {
			continue
		}
		seen[name] = struct{}{}
		entries = append(entries, ArtifactEntry{Name: name, Type: ArtifactDirectory})
	}
	for _, object := range response.Objects {
		name, ok := directArtifactChild(requestPrefix, object.Key, false)
		if !ok {
			continue
		}
		if _, exists := seen[name]; exists {
			continue
		}
		seen[name] = struct{}{}
		entries = append(entries, ArtifactEntry{Name: name, Type: ArtifactFile, SizeBytes: object.SizeBytes, LastModified: object.LastModified.UTC()})
	}
	sort.Slice(entries, func(left, right int) bool {
		if entries[left].Type != entries[right].Type {
			return entries[left].Type == ArtifactDirectory
		}
		return entries[left].Name < entries[right].Name
	})
	return ArtifactPage{Entries: entries, NextCursor: response.NextContinuationToken}
}

func directArtifactChild(requestPrefix, candidate string, directory bool) (string, bool) {
	if !strings.HasPrefix(candidate, requestPrefix) {
		return "", false
	}
	relative := strings.TrimPrefix(candidate, requestPrefix)
	if directory {
		if !strings.HasSuffix(relative, "/") {
			return "", false
		}
		relative = strings.TrimSuffix(relative, "/")
	}
	if relative == "" || strings.Contains(relative, "/") {
		return "", false
	}
	if _, err := domain.NormalizeStorageRelativePath(relative); err != nil {
		return "", false
	}
	return relative, true
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
