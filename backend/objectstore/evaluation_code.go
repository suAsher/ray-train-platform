package objectstore

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path"
	"strings"
	"unicode"

	"ray-train-platform-backend/domain"
	me "ray-train-platform-backend/modelevaluation"
)

const maxEvaluationCodeEntries = 10000
const maxEvaluationCodeExpandedSize uint64 = 256 << 20

type evaluationCodeStore struct{ store *TOSStore }

func (s *TOSStore) EvaluationCode() me.EvaluationCodeStore {
	return &evaluationCodeStore{store: s}
}

func evaluationCodeKey(id string) (string, error) {
	if !me.ValidCodeSnapshotID(id) {
		return "", me.ErrInvalid
	}
	return "raytrain-evaluation-code/" + id + "/source.zip", nil
}

func (s *evaluationCodeStore) Publish(ctx context.Context, id string, artifact domain.SourceArtifact) (me.CodeSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return me.CodeSnapshot{}, err
	}
	key, err := evaluationCodeKey(id)
	if err != nil {
		return me.CodeSnapshot{}, err
	}
	if artifact.Validate() != nil || artifact.SizeBytes > me.MaxEvaluationCodeSize {
		return me.CodeSnapshot{}, me.ErrInvalid
	}
	if artifact.State != domain.SourceArtifactReady {
		return me.CodeSnapshot{}, me.ErrNotReady
	}
	if s == nil || s.store == nil {
		return me.CodeSnapshot{}, me.ErrUnavailable
	}
	writer, ok := s.store.client.(interface {
		Put(context.Context, tosPutRequest) error
	})
	if !ok {
		return me.CodeSnapshot{}, me.ErrUnavailable
	}
	// Validate the complete source before exposing a durable immutable copy.
	// The SourceArtifact's canonical key came from an owner-scoped DB lookup.
	spool, err := s.readVerified(ctx, artifact.ObjectKey, artifact.SizeBytes, artifact.SHA256)
	if err != nil {
		return me.CodeSnapshot{}, err
	}
	defer spool.Close()
	if err := validateEvaluationCodeZIP(ctx, spool.file, artifact.SizeBytes); err != nil {
		return me.CodeSnapshot{}, err
	}
	err = writer.Put(ctx, tosPutRequest{Bucket: s.store.bucket, Key: key, SHA256: artifact.SHA256, SizeBytes: artifact.SizeBytes, ContentType: "application/zip", Body: spool.file})
	if err != nil {
		if !errors.Is(err, ErrAlreadyExists) {
			return me.CodeSnapshot{}, me.ErrUnavailable
		}
		head, headErr := s.store.client.Head(ctx, s.store.bucket, key)
		if headErr != nil {
			return me.CodeSnapshot{}, me.ErrUnavailable
		}
		if head.SizeBytes != artifact.SizeBytes || head.Metadata["sha256"] != artifact.SHA256 {
			return me.CodeSnapshot{}, me.ErrConflict
		}
	}
	return me.CodeSnapshot{ID: id, SHA256: artifact.SHA256, SizeBytes: artifact.SizeBytes, Format: "zip"}, nil
}

func (s *evaluationCodeStore) Open(ctx context.Context, snapshot me.CodeSnapshot) (io.ReadCloser, int64, error) {
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	if err := me.ValidateCodeSnapshot(snapshot); err != nil {
		return nil, 0, err
	}
	key, err := evaluationCodeKey(snapshot.ID)
	if err != nil {
		return nil, 0, err
	}
	spool, err := s.readVerified(ctx, key, snapshot.SizeBytes, snapshot.SHA256)
	if err != nil {
		if ctx.Err() != nil {
			return nil, 0, ctx.Err()
		}
		return nil, 0, me.ErrUnavailable
	}
	return spool, snapshot.SizeBytes, nil
}

// Only the declared bytes plus one overflow probe are read. Both source and
// download paths use 0600 temporary files instead of holding archives in RAM.
func (s *evaluationCodeStore) readVerified(ctx context.Context, key string, size int64, digest string) (*evaluationCodeSpool, error) {
	if s == nil || s.store == nil {
		return nil, me.ErrUnavailable
	}
	reader, ok := s.store.client.(tosArtifactReadClient)
	if !ok {
		return nil, me.ErrUnavailable
	}
	result, err := reader.ReadArtifact(ctx, tosArtifactReadRequest{Bucket: s.store.bucket, Key: key})
	if err != nil {
		if result.Content != nil {
			_ = result.Content.Close()
		}
		if errors.Is(err, ErrNotFound) {
			return nil, me.ErrNotFound
		}
		return nil, me.ErrUnavailable
	}
	if result.Content == nil {
		return nil, me.ErrUnavailable
	}
	defer result.Content.Close()
	if result.SizeBytes != size || size < 1 || size > me.MaxEvaluationCodeSize {
		return nil, me.ErrInvalid
	}
	file, err := os.CreateTemp("", "raytrain-evaluation-code-*")
	if err != nil {
		return nil, me.ErrUnavailable
	}
	spool := &evaluationCodeSpool{file: file, name: file.Name()}
	complete := false
	defer func() {
		if !complete {
			_ = spool.Close()
		}
	}()
	hash := sha256.New()
	bounded := io.LimitReader(evaluationCodeContextReader{ctx: ctx, reader: result.Content}, size+1)
	written, err := io.CopyBuffer(io.MultiWriter(file, hash), bounded, make([]byte, 32<<10))
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, me.ErrUnavailable
	}
	if written != size || hex.EncodeToString(hash.Sum(nil)) != digest {
		return nil, me.ErrInvalid
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, me.ErrUnavailable
	}
	complete = true
	return spool, nil
}

func validateEvaluationCodeZIP(ctx context.Context, file *os.File, size int64) error {
	archive, err := zip.NewReader(file, size)
	if err != nil || len(archive.File) < 1 || len(archive.File) > maxEvaluationCodeEntries {
		return me.ErrInvalid
	}
	seen := make(map[string]bool, len(archive.File))
	var expanded uint64
	buffer := make([]byte, 32<<10)
	for _, member := range archive.File {
		if err := ctx.Err(); err != nil {
			return err
		}
		name := strings.TrimSuffix(member.Name, "/")
		if name == "" || path.IsAbs(name) || name == "." || name == ".." || path.Clean(name) != name || strings.HasPrefix(name, "../") || strings.ContainsAny(name, "\\:%") || strings.IndexFunc(name, unicode.IsControl) >= 0 || seen[name] {
			return me.ErrInvalid
		}
		seen[name] = true
		if !member.Mode().IsRegular() && !member.Mode().IsDir() {
			return me.ErrInvalid
		}
		if member.UncompressedSize64 > maxEvaluationCodeExpandedSize-expanded {
			return me.ErrInvalid
		}
		expanded += member.UncompressedSize64
		if member.Mode().IsDir() {
			if member.UncompressedSize64 != 0 {
				return me.ErrInvalid
			}
			continue
		}
		// Read through EOF to verify compression, CRC, and the real expanded
		// length. A forged tiny header cannot make decompression unbounded.
		reader, err := member.Open()
		if err != nil {
			return me.ErrInvalid
		}
		bounded := io.LimitReader(evaluationCodeContextReader{ctx: ctx, reader: reader}, int64(member.UncompressedSize64)+1)
		written, readErr := io.CopyBuffer(io.Discard, bounded, buffer)
		closeErr := reader.Close()
		if err := ctx.Err(); err != nil {
			return err
		}
		if readErr != nil || closeErr != nil || written != int64(member.UncompressedSize64) {
			return me.ErrInvalid
		}
	}
	return nil
}

type evaluationCodeContextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r evaluationCodeContextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

type evaluationCodeSpool struct {
	file   *os.File
	name   string
	closed bool
}

func (s *evaluationCodeSpool) Read(p []byte) (int, error) { return s.file.Read(p) }
func (s *evaluationCodeSpool) Close() error {
	if s.closed {
		return nil
	}
	s.closed = true
	closeErr := s.file.Close()
	removeErr := os.Remove(s.name)
	if closeErr != nil || removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
		return me.ErrUnavailable
	}
	return nil
}
