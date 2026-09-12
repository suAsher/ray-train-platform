package modellifecycle

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"path"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

var hashPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
var commitPattern = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)

func ValidID(id string) bool {
	parsed, err := uuid.Parse(id)
	return err == nil && parsed.String() == id
}

type Service struct {
	repo              Repository
	source            Source
	objects           Objects
	heartbeatInterval time.Duration
}

func NewService(repo Repository, source Source, objects Objects) *Service {
	return &Service{repo: repo, source: source, objects: objects, heartbeatInterval: 30 * time.Second}
}
func (s *Service) RequestVersion(ctx context.Context, r VersionRequest) (Version, error) {
	if s.repo == nil || s.source == nil || s.objects == nil {
		return Version{}, ErrNotReady
	}
	if !validRequest(r) {
		return Version{}, ErrInvalid
	}
	// The idempotency fingerprint describes the request, never mutable source bytes.
	encoded, err := json.Marshal(r)
	if err != nil {
		return Version{}, ErrInvalid
	}
	sum := sha256.Sum256(encoded)
	fingerprint := hex.EncodeToString(sum[:])
	previous, err := s.repo.FindVersionRequest(ctx, r.ModelID, r.CreatorID, r.IdempotencyKey)
	if err == nil {
		if previous.RequestSHA256 != fingerprint {
			return Version{}, ErrConflict
		}
		return previous, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return Version{}, err
	}
	source, size, etag, err := s.source.Read(ctx, r.SourceRoot, r.RelativePath)
	if err != nil {
		return Version{}, err
	}
	if source == nil {
		return Version{}, ErrUnavailable
	}
	if err = source.Close(); err != nil {
		return Version{}, ErrInvalid
	}
	if size < 1 || size > MaxFileSize {
		return Version{}, ErrInvalid
	}
	if strings.TrimSpace(etag) == "" {
		return Version{}, ErrUnavailable
	}
	return s.repo.ReserveVersion(ctx, Version{ID: uuid.NewString(), ModelID: r.ModelID, Description: r.Description, CreatorID: r.CreatorID, CreatorName: r.CreatorName, JobID: r.JobID, JobName: r.JobName, RunID: r.RunID, FileName: r.FileName, SourceRoot: r.SourceRoot, SourceETag: etag, RelativePath: r.RelativePath, CodeSHA256: r.CodeSHA256, CodeCommit: r.CodeCommit, RuntimeImage: r.RuntimeImage, DatasetID: r.DatasetID, DatasetVersionID: r.DatasetVersionID, DatasetName: r.DatasetName, DatasetManifestSHA256: r.DatasetManifestSHA256, DatasetAssociation: r.DatasetAssociation, IdempotencyKey: r.IdempotencyKey, RequestSHA256: fingerprint, SizeBytes: size})
}
func validRequest(r VersionRequest) bool {
	if r.ModelID == "" || r.CreatorID == "" || r.JobID == "" || r.SourceRoot == "" || r.FileName == "" || r.FileName != path.Base(r.FileName) || len(r.FileName) > 255 || utf8.RuneCountInString(r.Description) > 4000 || len(r.IdempotencyKey) < 1 || len(r.IdempotencyKey) > 128 {
		return false
	}
	if r.RelativePath == "" || path.IsAbs(r.RelativePath) || path.Clean(r.RelativePath) != r.RelativePath || strings.Contains(r.RelativePath, "\\") || r.RelativePath == ".." || strings.HasPrefix(r.RelativePath, "../") || strings.ContainsAny(r.RelativePath+r.FileName, "\x00\r\n") {
		return false
	}
	for _, c := range r.IdempotencyKey {
		if c < 33 || c > 126 {
			return false
		}
	}
	if r.CodeSHA256 != "" && !hashPattern.MatchString(r.CodeSHA256) || r.CodeCommit != "" && !commitPattern.MatchString(r.CodeCommit) || len(r.RuntimeImage) > 512 {
		return false
	}
	if r.DatasetAssociation == "" {
		return r.DatasetID == "" && r.DatasetVersionID == "" && r.DatasetManifestSHA256 == "" && r.DatasetName == ""
	}
	return (r.DatasetAssociation == "training-record" || r.DatasetAssociation == "user-declared") && r.DatasetID != "" && r.DatasetVersionID != "" && hashPattern.MatchString(r.DatasetManifestSHA256)
}

// Run drains persistent requests. An expired lease is failed, never reclaimed:
// a stale writer can neither publish READY nor overwrite a newer attempt.
func (s *Service) Run(ctx context.Context) {
	if s.repo == nil || s.source == nil || s.objects == nil {
		return
	}
	for ctx.Err() == nil {
		now := time.Now().UTC()
		v, err := s.repo.ClaimVersion(ctx, uuid.NewString(), now, now.Add(2*time.Minute))
		if err == nil {
			s.process(ctx, v)
			continue
		}
		if !errors.Is(err, ErrNotFound) && ctx.Err() == nil {
			slog.Error("model snapshot claim failed", "error", err)
		}
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}
func (s *Service) process(ctx context.Context, v Version) {
	work, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	done := make(chan struct{})
	renewed := make(chan struct{})
	go func() {
		defer close(renewed)
		ticker := time.NewTicker(s.heartbeatInterval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-work.Done():
				return
			case <-ticker.C:
				now := time.Now().UTC()
				if s.repo.RenewVersion(work, v.ID, v.LeaseID, now, now.Add(2*time.Minute)) != nil {
					cancel()
					return
				}
			}
		}
	}()
	parts, hash, err := s.copy(work, v)
	close(done)
	<-renewed
	state := Ready
	if err != nil || work.Err() != nil {
		state = Failed
		parts = nil
		hash = ""
		slog.Warn("model snapshot copy failed", "version_id", v.ID, "error", err)
	}
	finish, cancelFinish := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancelFinish()
	// Completion is lease-fenced; a failed update leaves recovery to lease expiry.
	if err := s.repo.FinishVersion(finish, v.ID, v.LeaseID, state, parts, hash, time.Now().UTC()); err != nil {
		slog.Warn("model snapshot completion rejected", "version_id", v.ID, "error", err)
	}
}
func (s *Service) copy(ctx context.Context, v Version) ([]Part, string, error) {
	source, size, etag, err := s.source.Read(ctx, v.SourceRoot, v.RelativePath)
	if err != nil {
		return nil, "", err
	}
	if source == nil {
		return nil, "", ErrUnavailable
	}
	defer source.Close()
	if strings.TrimSpace(etag) == "" || strings.TrimSpace(v.SourceETag) == "" {
		return nil, "", ErrUnavailable
	}
	if size != v.SizeBytes || etag != v.SourceETag || size < 1 || size > MaxFileSize {
		return nil, "", ErrInvalid
	}
	whole := sha256.New()
	parts := make([]Part, 0, (size+PartSize-1)/PartSize)
	remaining := size
	for index := 0; remaining > 0; index++ {
		if err := ctx.Err(); err != nil {
			return nil, "", err
		}
		count := int64(PartSize)
		if remaining < count {
			count = remaining
		}
		buf := make([]byte, int(count))
		if _, err := io.ReadFull(source, buf); err != nil {
			return nil, "", err
		}
		sum := sha256.Sum256(buf)
		hash := hex.EncodeToString(sum[:])
		_, _ = whole.Write(buf)
		if err := s.objects.Put(ctx, v.ID, index, hash, buf); err != nil {
			return nil, "", err
		}
		parts = append(parts, Part{Index: index, SizeBytes: count, SHA256: hash})
		remaining -= count
	}
	var extra [1]byte
	if n, err := io.ReadFull(source, extra[:]); n != 0 || err != io.EOF {
		return nil, "", ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	return parts, hex.EncodeToString(whole.Sum(nil)), nil
}

// Download buffers at most one part and validates it before exposing its bytes.
func (s *Service) Download(ctx context.Context, v Version, w io.Writer) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if v.State != Ready || s.objects == nil {
		return ErrNotReady
	}
	if v.SizeBytes < 1 || v.SizeBytes > MaxFileSize || !hashPattern.MatchString(v.SHA256) || len(v.Parts) != int((v.SizeBytes+PartSize-1)/PartSize) {
		return ErrInvalid
	}
	whole := sha256.New()
	var total int64
	for index, part := range v.Parts {
		if err := ctx.Err(); err != nil {
			return err
		}
		expected := int64(PartSize)
		if left := v.SizeBytes - total; left < expected {
			expected = left
		}
		if part.Index != index || part.SizeBytes != expected || !hashPattern.MatchString(part.SHA256) {
			return ErrInvalid
		}
		reader, size, err := s.objects.Get(ctx, v.ID, index)
		if err != nil {
			return err
		}
		if reader == nil {
			return ErrUnavailable
		}
		if size != expected {
			reader.Close()
			return ErrInvalid
		}
		buf, readErr := io.ReadAll(io.LimitReader(reader, expected+1))
		closeErr := reader.Close()
		if readErr != nil {
			return readErr
		}
		if closeErr != nil {
			return closeErr
		}
		if int64(len(buf)) != expected {
			return ErrInvalid
		}
		sum := sha256.Sum256(buf)
		if hex.EncodeToString(sum[:]) != part.SHA256 {
			return ErrInvalid
		}
		_, _ = whole.Write(buf)
		total += int64(len(buf))
		// Validate the complete digest before releasing the final part.
		if index == len(v.Parts)-1 && (total != v.SizeBytes || hex.EncodeToString(whole.Sum(nil)) != v.SHA256) {
			return ErrInvalid
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if n, err := w.Write(buf); err != nil {
			return err
		} else if n != len(buf) {
			return io.ErrShortWrite
		}
	}
	return nil
}
