package functionwarehouse

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"hash"
	"io"
	"mime/multipart"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	MaxUploadSize int64 = 20 << 30
	simpleUploadLimit int64 = 500 << 20
	defaultChunkSize int64 = 10 << 20
	defaultUploadTimeout = 30 * time.Minute
)

var uploadPrefixPattern = regexp.MustCompile(`^raytrain/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}(?:/[A-Za-z0-9_-]+)*/?$`)

// Upload opens an immutable source at most twice. Large files are hashed in a
// streaming first pass, then uploaded as bounded sequential chunks. Neither
// chunk POSTs nor merge POSTs are retried, and shared MD5 chunks are never cleaned.
func (c *Client) Upload(ctx context.Context, token, name, prefix string, size int64, digest string, open func(context.Context) (io.ReadCloser, error)) (UploadedFile, error) {
	if !validUploadInput(name, prefix, size, digest) || open == nil { return UploadedFile{}, ErrInvalid }
	if !validToken(token) { return UploadedFile{}, ErrUnauthorized }
	ctx, cancel := context.WithTimeout(ctx, defaultUploadTimeout)
	defer cancel()
	if size <= simpleUploadLimit {
		reader, err := open(ctx)
		if err != nil || reader == nil { return UploadedFile{}, ErrUnavailable }
		defer reader.Close()
		return c.UploadFile(ctx, token, name, prefix, size, digest, reader)
	}
	return c.uploadChunks(ctx, token, name, prefix, size, digest, defaultChunkSize, open)
}

// UploadFile streams the confirmed multipart file contract. The input reader
// must be context-aware or closable; a closable reader is closed on cancellation.
// It validates bytes actually read and then verifies the server's size/hash.
func (c *Client) UploadFile(ctx context.Context, token, name, prefix string, size int64, digest string, reader io.Reader) (UploadedFile, error) {
	if !validUploadInput(name, prefix, size, digest) || size > simpleUploadLimit || reader == nil { return UploadedFile{}, ErrInvalid }
	if !validToken(token) { return UploadedFile{}, ErrUnauthorized }
	ctx, cancel := context.WithTimeout(ctx, defaultUploadTimeout)
	defer cancel()
	stopClose := closeOnCancel(ctx, reader)
	defer stopClose()
	checked := &integrityReader{source: reader, remaining: size, digest: digest, hash: sha256.New()}
	body, contentType, length, err := multipartStream(name, checked, size, nil)
	if err != nil { return UploadedFile{}, ErrInvalid }
	var response json.RawMessage
	err = c.mutate(ctx, token, "/system/api/file/minio/upload", url.Values{"prefix": {prefix}}, contentType, body, length, &response)
	if err != nil { return UploadedFile{}, err }
	if !checked.verified { return UploadedFile{}, ErrUnknownOutcome }
	if file, ok := completedFile(response, name, size, digest); ok { return file, nil }
	return c.awaitUploadedFile(ctx, token, name, size, digest)
}

func (c *Client) uploadChunks(ctx context.Context, token, name, prefix string, size int64, digest string, chunkSize int64, open func(context.Context) (io.ReadCloser, error)) (UploadedFile, error) {
	first, err := open(ctx)
	if err != nil || first == nil { return UploadedFile{}, ErrUnavailable }
	stopClose := closeOnCancel(ctx, first)
	fileMD5, err := hashSource(ctx, first, size, digest)
	stopClose()
	_ = first.Close()
	if err != nil { return UploadedFile{}, err }
	second, err := open(ctx)
	if err != nil || second == nil { return UploadedFile{}, ErrUnavailable }
	defer second.Close()
	stopClose = closeOnCancel(ctx, second)
	defer stopClose()
	checked := &integrityReader{source: second, remaining: size, digest: digest, hash: sha256.New()}
	chunks := (size + chunkSize - 1) / chunkSize
	buffer := make([]byte, chunkSize)
	for index := int64(0); index < chunks; index++ {
		if ctx.Err() != nil { return UploadedFile{}, ErrUnknownOutcome }
		length := min(chunkSize, size-index*chunkSize)
		if _, err := io.ReadFull(checked, buffer[:length]); err != nil { return UploadedFile{}, ErrUnknownOutcome }
		chunk := buffer[:length]
		md5sum := md5.Sum(chunk) // Required by the existing chunk transport contract.
		fields := map[string]string{
			"chunkSize": strconv.FormatInt(length, 10), "chunks": strconv.FormatInt(chunks, 10),
			"currentChunk": strconv.FormatInt(index, 10), "currentChunkMd5": hex.EncodeToString(md5sum[:]),
			"fileMd5": fileMD5, "size": strconv.FormatInt(size, 10), "path": prefix,
		}
		body, contentType, total, err := multipartStream(name, bytes.NewReader(chunk), length, fields)
		if err != nil { return UploadedFile{}, ErrInvalid }
		chunkCtx, cancel := context.WithTimeout(ctx, 5 * time.Minute)
		err = c.mutate(chunkCtx, token, "/system/api/file/minio/chunk/upload", nil, contentType, body, total, nil)
		cancel()
		if err != nil { return UploadedFile{}, err }
	}
	if !checked.verified { return UploadedFile{}, ErrUnknownOutcome }
	fields := map[string]string{
		"chunkCount": strconv.FormatInt(chunks, 10), "fileName": name,
		"fileMd5": fileMD5, "sha256": digest, "prefix": prefix,
	}
	body, contentType, length, err := multipartStream("", nil, 0, fields)
	if err != nil { return UploadedFile{}, ErrInvalid }
	var response json.RawMessage
	mergeCtx, cancel := context.WithTimeout(ctx, 10 * time.Minute)
	err = c.mutate(mergeCtx, token, "/system/api/file/minio/chunk/merge", nil, contentType, body, length, &response)
	cancel()
	if err != nil { return UploadedFile{}, err }
	if file, ok := completedFile(response, name, size, digest); ok { return file, nil }
	return c.awaitUploadedFile(ctx, token, name, size, digest)
}

func hashSource(ctx context.Context, reader io.Reader, size int64, digest string) (string, error) {
	sha := sha256.New()
	md5hash := md5.New() // Compatibility fingerprint; SHA256 establishes integrity.
	buffer := make([]byte, 128 << 10)
	limited := io.LimitReader(reader, size+1)
	var count int64
	emptyReads := 0
	for {
		if ctx.Err() != nil { return "", ErrUnavailable }
		n, err := limited.Read(buffer)
		if n > 0 {
			emptyReads = 0
			count += int64(n)
			_, _ = sha.Write(buffer[:n])
			_, _ = md5hash.Write(buffer[:n])
		}
		if n == 0 && err == nil { emptyReads++; if emptyReads >= 100 { return "", ErrUnavailable } }
		if err == io.EOF { break }
		if err != nil { return "", ErrUnavailable }
	}
	if count != size || hex.EncodeToString(sha.Sum(nil)) != digest { return "", ErrInvalid }
	return hex.EncodeToString(md5hash.Sum(nil)), nil
}

type integrityReader struct {
	source io.Reader
	remaining int64
	digest string
	hash hash.Hash
	verified bool
}

func (r *integrityReader) Read(p []byte) (int, error) {
	if len(p) == 0 { return 0, nil }
	if r.verified { return 0, io.EOF }
	if r.remaining < int64(len(p)) { p = p[:r.remaining] }
	n, err := r.source.Read(p)
	if n > 0 { _, _ = r.hash.Write(p[:n]); r.remaining -= int64(n) }
	if err != nil && err != io.EOF { return n, ErrInvalid }
	if r.remaining == 0 {
		var extra [1]byte
		extraN, extraErr := io.ReadFull(r.source, extra[:])
		if extraN != 0 || extraErr != io.EOF || hex.EncodeToString(r.hash.Sum(nil)) != r.digest { return n, ErrInvalid }
		r.verified = true
		return n, io.EOF
	}
	if err == io.EOF { return n, ErrInvalid }
	return n, nil
}

func multipartStream(name string, reader io.Reader, size int64, fields map[string]string) (io.Reader, string, int64, error) {
	var header bytes.Buffer
	writer := multipart.NewWriter(&header)
	for key, value := range fields { if err := writer.WriteField(key, value); err != nil { return nil, "", 0, err } }
	if reader != nil { if _, err := writer.CreateFormFile("file", name); err != nil { return nil, "", 0, err } }
	prefix := append([]byte(nil), header.Bytes()...)
	header.Reset()
	if err := writer.Close(); err != nil { return nil, "", 0, err }
	suffix := append([]byte(nil), header.Bytes()...)
	if reader == nil { reader = strings.NewReader("") }
	return io.MultiReader(bytes.NewReader(prefix), reader, bytes.NewReader(suffix)), writer.FormDataContentType(), int64(len(prefix)+len(suffix))+size, nil
}

func closeOnCancel(ctx context.Context, reader io.Reader) func() {
	if closer, ok := reader.(io.Closer); ok {
		stop := context.AfterFunc(ctx, func() { _ = closer.Close() })
		return func() { stop() }
	}
	return func() {}
}

func validUploadInput(name, prefix string, size int64, digest string) bool {
	return validFilename(name) && len(prefix) <= 512 && uploadPrefixPattern.MatchString(prefix) && size > 0 && size <= MaxUploadSize && validSHA256(digest)
}

func validFilename(name string) bool {
	return name != "" && name != "." && name != ".." && len(name) <= 255 && utf8.ValidString(name) && path.Base(name) == name && !strings.ContainsAny(name, "\\\x00\r\n")
}

func validSHA256(value string) bool {
	if len(value) != 64 || strings.ToLower(value) != value { return false }
	_, err := hex.DecodeString(value)
	return err == nil
}

func validUploadedFile(file UploadedFile) bool {
	if !validFilename(file.Filename) || !validSHA256(file.FileSHA256) || file.FileSize <= 0 || file.FileSize > MaxUploadSize || len(file.URL) > 8192 || len(file.FilePath) > 4096 || strings.ContainsAny(file.URL+file.FilePath, "\x00\r\n") { return false }
	// The URL remains data and is never fetched. Reject credentials or executable
	// schemes even when the upstream responds with unexpected metadata.
	parsed, err := url.Parse(file.URL)
	if err != nil || parsed.User != nil { return false }
	if parsed.Scheme == "" && parsed.Host == "" {
		const previewPrefix = "/system/api/file/preview/"
		if !strings.HasPrefix(file.URL, previewPrefix) || !strings.HasPrefix(parsed.Path, previewPrefix) || len(parsed.Path) <= len(previewPrefix) || strings.ContainsAny(file.URL, "?#") || strings.ContainsAny(parsed.Path, "\\\x00\r\n%") { return false }
		for _, segment := range strings.Split(parsed.Path, "/") { if segment == "." || segment == ".." { return false } }
		return true
	}
	return parsed.Host != "" && (parsed.Scheme == "https" || parsed.Scheme == "http")
}

// completedFile recognizes the frontend's documented camel/snake case file
// responses and process result object/array, while requiring exact integrity.
func completedFile(data json.RawMessage, name string, size int64, digest string) (UploadedFile, bool) {
	return completedFileDepth(data, name, size, digest, 0)
}

func completedFileDepth(data json.RawMessage, name string, size int64, digest string, depth int) (UploadedFile, bool) {
	if depth > 3 { return UploadedFile{}, false }
	var object map[string]json.RawMessage
	if json.Unmarshal(data, &object) != nil { return UploadedFile{}, false }
	if status := stringField(object, "status"); status == "error" || status == "uploading" { return UploadedFile{}, false }
	if raw, ok := object["result"]; ok && string(raw) != "null" {
		if len(raw) > 0 && raw[0] == '[' {
			var results []json.RawMessage
			if json.Unmarshal(raw, &results) != nil || len(results) != 1 { return UploadedFile{}, false }
			raw = results[0]
		}
		return completedFileDepth(raw, name, size, digest, depth+1)
	}
	file := UploadedFile{
		URL: stringField(object, "url"), FilePath: stringField(object, "filePath", "file_path"),
		Filename: stringField(object, "filename", "fileName"), FileSHA256: stringField(object, "fileSha256", "file_sha256"),
	}
	for _, key := range []string{"fileSize", "file_size"} {
		if raw, ok := object[key]; ok {
			if json.Unmarshal(raw, &file.FileSize) != nil {
				var text string
				if json.Unmarshal(raw, &text) == nil { file.FileSize, _ = strconv.ParseInt(text, 10, 64) }
			}
			break
		}
	}
	return file, file.Filename == name && file.FileSize == size && file.FileSHA256 == digest && validUploadedFile(file)
}

func stringField(object map[string]json.RawMessage, keys ...string) string {
	for _, key := range keys {
		var value string
		if json.Unmarshal(object[key], &value) == nil && value != "" { return value }
	}
	return ""
}

func (c *Client) awaitUploadedFile(ctx context.Context, token, name string, size int64, digest string) (UploadedFile, error) {
	for attempt := 0; attempt < 60; attempt++ {
		var data json.RawMessage
		if err := c.get(ctx, token, "/system/api/file/minio/upload/process", url.Values{"sha256": {digest}}, &data); err != nil { return UploadedFile{}, ErrUnknownOutcome }
		var status struct { Status string `json:"status"` }
		if json.Unmarshal(data, &status) != nil || status.Status == "error" { return UploadedFile{}, ErrUnknownOutcome }
		if file, ok := completedFile(data, name, size, digest); ok { return file, nil }
		if attempt == 59 { break }
		timer := time.NewTimer(3 * time.Second)
		select {
		case <-ctx.Done(): timer.Stop(); return UploadedFile{}, ErrUnknownOutcome
		case <-timer.C:
		}
	}
	return UploadedFile{}, ErrUnknownOutcome
}
