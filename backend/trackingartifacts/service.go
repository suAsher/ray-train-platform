package trackingartifacts

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

var idPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)
var shaPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

func ValidID(id string) bool { return idPattern.MatchString(id) }
func validScope(scope Scope) bool {
	for _, s := range []string{scope.TenantID, scope.OwnerID} {
		if strings.TrimSpace(s) != s || s == "" || len(s) > 256 || strings.IndexFunc(s, unicode.IsControl) >= 0 {
			return false
		}
	}
	return ValidID(scope.RunID)
}
func validName(name string) bool {
	return name != "" && name != "." && name != ".." && len(name) <= 255 && utf8.ValidString(name) && strings.TrimSpace(name) == name && !strings.ContainsAny(name, "/\\") && strings.IndexFunc(name, unicode.IsControl) < 0
}
func randomID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", ErrUnavailable
	}
	return hex.EncodeToString(b[:]), nil
}
func partKey(id string, index int) string { return id + "/" + strconv.Itoa(index) }
func public(v Record) Artifact {
	a := v.Artifact
	a.UploadedParts = append([]Part{}, v.UploadedParts...)
	sort.Slice(a.UploadedParts, func(i, j int) bool { return a.UploadedParts[i].Index < a.UploadedParts[j].Index })
	return a
}
func failure(err error) error {
	for _, known := range []error{ErrInvalid, ErrNotFound, ErrConflict, ErrQuota, ErrUnavailable} {
		if errors.Is(err, known) {
			return known
		}
	}
	return ErrUnavailable
}
func (s *Service) available() bool { return s != nil && s.repo != nil && s.objects != nil }
func (s *Service) Init(ctx context.Context, scope Scope, key string, input InitInput) (Artifact, error) {
	if !s.available() {
		return Artifact{}, ErrUnavailable
	}
	if !validScope(scope) || !validName(input.Name) || input.SizeBytes < 1 || input.SizeBytes > MaxFileBytes || !shaPattern.MatchString(input.SHA256) || len(key) < 8 || len(key) > 256 || strings.TrimSpace(key) != key || strings.IndexFunc(key, unicode.IsControl) >= 0 {
		return Artifact{}, ErrInvalid
	}
	id, err := randomID()
	if err != nil {
		return Artifact{}, err
	}
	now := time.Now().UTC()
	hash := sha256.Sum256([]byte(key))
	v := Record{Artifact: Artifact{ID: id, RunID: scope.RunID, Name: input.Name, SizeBytes: input.SizeBytes, SHA256: input.SHA256, State: "PENDING", PartSizeBytes: PartSizeBytes, TotalParts: int((input.SizeBytes + PartSizeBytes - 1) / PartSizeBytes), UploadedParts: []Part{}, CreatedAt: now, ExpiresAt: now.Add(24 * time.Hour)}, Scope: scope, IdempotencyHash: hex.EncodeToString(hash[:])}
	ctx, cancel := context.WithTimeout(ctx, OperationTimeout)
	defer cancel()
	saved, err := s.repo.Reserve(ctx, v)
	if err != nil {
		return Artifact{}, failure(err)
	}
	return public(saved), nil
}
func (s *Service) Get(ctx context.Context, scope Scope, id string) (Artifact, error) {
	if !s.available() {
		return Artifact{}, ErrUnavailable
	}
	if !validScope(scope) || !ValidID(id) {
		return Artifact{}, ErrInvalid
	}
	v, err := s.repo.Get(ctx, scope, id)
	if err != nil {
		return Artifact{}, failure(err)
	}
	return public(v), nil
}
func (s *Service) List(ctx context.Context, scope Scope, cursor string, limit int) (Page, error) {
	if !s.available() {
		return Page{}, ErrUnavailable
	}
	if !validScope(scope) || (cursor != "" && !ValidID(cursor)) || limit < 1 || limit > 100 {
		return Page{}, ErrInvalid
	}
	rows, err := s.repo.List(ctx, scope, cursor, limit+1)
	if err != nil {
		return Page{}, failure(err)
	}
	page := Page{Items: []Artifact{}}
	if len(rows) > limit {
		rows = rows[:limit]
		page.NextCursor = rows[len(rows)-1].ID
	}
	for _, v := range rows {
		page.Items = append(page.Items, public(v))
	}
	return page, nil
}
func expectedSize(v Record, index int) int64 {
	if index < 1 || index > v.TotalParts {
		return 0
	}
	if index == v.TotalParts {
		return v.SizeBytes - int64(index-1)*PartSizeBytes
	}
	return PartSizeBytes
}
func writable(v Record) bool { return v.State == "PENDING" && time.Now().Before(v.ExpiresAt) }
func (s *Service) mutate(ctx context.Context, scope Scope, id string, fn func(context.Context, Record) (Record, error)) (Artifact, error) {
	if !s.available() {
		return Artifact{}, ErrUnavailable
	}
	if !validScope(scope) || !ValidID(id) {
		return Artifact{}, ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, OperationTimeout)
	defer cancel()
	v, err := s.repo.Mutate(ctx, scope, id, func(v Record) (Record, error) {
		if ctx.Err() != nil {
			return Record{}, ErrUnavailable
		}
		return fn(ctx, v)
	})
	if err != nil {
		return Artifact{}, failure(err)
	}
	return public(v), nil
}
func (s *Service) PutPart(ctx context.Context, scope Scope, id string, index int, sha string, body io.Reader) (Artifact, error) {
	if body == nil || !shaPattern.MatchString(sha) || index < 1 || index > MaxParts {
		return Artifact{}, ErrInvalid
	}
	a, err := s.Get(ctx, scope, id)
	if err != nil {
		return Artifact{}, err
	}
	v := Record{Artifact: a}
	if !writable(v) {
		return Artifact{}, ErrConflict
	}
	size := expectedSize(v, index)
	if size < 1 {
		return Artifact{}, ErrInvalid
	}
	// Buffer at most one fixed-size part before taking the DB lock. The HTTP
	// caller also bounds and deadlines request-body reads; no network client can
	// hold an artifact transaction while slowly sending its upload body.
	data, err := io.ReadAll(io.LimitReader(body, size+1))
	if err != nil || int64(len(data)) != size {
		return Artifact{}, ErrInvalid
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != sha {
		return Artifact{}, ErrInvalid
	}
	return s.mutate(ctx, scope, id, func(ctx context.Context, v Record) (Record, error) {
		if !writable(v) {
			return Record{}, ErrConflict
		}
		for _, part := range v.UploadedParts {
			if part.Index == index && (part.SHA256 != sha || part.SizeBytes != size) {
				return Record{}, ErrConflict
			}
		}
		// Re-Put on an identical retry also repairs an object deleted by an earlier
		// cancelled cleanup whose transaction rolled back after a transient error.
		if err := s.objects.Put(ctx, id, index, sha, data); err != nil {
			return Record{}, failure(err)
		}
		for _, part := range v.UploadedParts {
			if part.Index == index {
				return v, nil
			}
		}
		v.UploadedParts = append(append([]Part{}, v.UploadedParts...), Part{Index: index, SizeBytes: size, SHA256: sha})
		return v, nil
	})
}
func (s *Service) Complete(ctx context.Context, scope Scope, id string) (Artifact, error) {
	return s.mutate(ctx, scope, id, func(ctx context.Context, v Record) (Record, error) {
		if v.State == "READY" {
			return v, nil
		}
		if !writable(v) || len(v.UploadedParts) != v.TotalParts {
			return Record{}, ErrConflict
		}
		parts := public(v).UploadedParts
		whole := sha256.New()
		buffer := make([]byte, 64<<10)
		for i, part := range parts {
			if part.Index != i+1 || part.SizeBytes != expectedSize(v, part.Index) {
				return Record{}, ErrConflict
			}
			body, size, err := s.objects.Get(ctx, id, part.Index)
			if err != nil {
				return Record{}, failure(err)
			}
			if body == nil {
				return Record{}, ErrUnavailable
			}
			if size != part.SizeBytes {
				body.Close()
				return Record{}, ErrConflict
			}
			single := sha256.New()
			n, readErr := io.CopyBuffer(io.MultiWriter(whole, single), io.LimitReader(body, size+1), buffer)
			closeErr := body.Close()
			if readErr != nil || closeErr != nil || ctx.Err() != nil {
				return Record{}, ErrUnavailable
			}
			if n != size || hex.EncodeToString(single.Sum(nil)) != part.SHA256 {
				return Record{}, ErrConflict
			}
		}
		if hex.EncodeToString(whole.Sum(nil)) != v.SHA256 {
			return Record{}, ErrConflict
		}
		v.State = "READY"
		return v, nil
	})
}
func (s *Service) Cancel(ctx context.Context, scope Scope, id string) (Artifact, error) {
	return s.mutate(ctx, scope, id, func(ctx context.Context, v Record) (Record, error) {
		if v.State == "CANCELLED" {
			return v, nil
		}
		if v.State != "PENDING" {
			return Record{}, ErrConflict
		}
		// Include unregistered keys left by a process/DB failure after Put. Only
		// this generated session's finite indexes can be deleted. A cleanup error
		// rolls the state change back and retains the entire reservation.
		for index := 1; index <= v.TotalParts; index++ {
			if err := s.objects.Delete(ctx, id, index); err != nil {
				return Record{}, failure(err)
			}
		}
		v.State = "CANCELLED"
		return v, nil
	})
}
