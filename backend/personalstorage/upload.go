package personalstorage

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"os"
	"path"
	"time"
)

// Upload stages a bounded file and atomically publishes it only after exact-length IO.
func Upload(ctx context.Context, store Store, name string, reader io.Reader, size, max int64) (result error) {
	clean, err := ValidatePath(name)
	if err != nil || clean == "" || reader == nil || store == nil || size < 0 || max <= 0 {
		return ErrInvalid
	}
	if size > max {
		return ErrTooLarge
	}
	token := make([]byte, 12)
	if _, err = rand.Read(token); err != nil {
		return ErrUnavailable
	}
	tmp := path.Join(path.Dir(clean), ".raytrain-upload-"+hex.EncodeToString(token))
	var file File
	closed := false
	defer func() {
		if file != nil && !closed {
			_ = file.Close()
		}
		if result != nil {
			cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			defer cancel()
			var cleanupErr error
			if cleaner, ok := store.(interface {
				Cleanup(context.Context, string) error
			}); ok {
				cleanupErr = cleaner.Cleanup(cleanupCtx, tmp)
			} else {
				cleanupErr = store.Remove(cleanupCtx, tmp)
			}
			if cleanupErr != nil {
				result = ErrUnavailable
			}
		}
	}()
	file, err = store.Open(ctx, tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY)
	if err != nil {
		return err
	}
	buf := make([]byte, 64*1024)
	var offset int64
	for offset < size {
		if ctx.Err() != nil {
			return ErrUnavailable
		}
		amount := int64(len(buf))
		if remaining := size - offset; remaining < amount {
			amount = remaining
		}
		n, readErr := reader.Read(buf[:amount])
		if n > 0 {
			written, writeErr := file.WriteAt(buf[:n], offset)
			if writeErr != nil || written != n {
				return ErrUnavailable
			}
			offset += int64(n)
		}
		if readErr != nil {
			if readErr == io.EOF && offset == size {
				break
			}
			return ErrInvalid
		}
		if n == 0 {
			return ErrInvalid
		}
	}
	var extra [1]byte
	if n, err := reader.Read(extra[:]); n != 0 || err != io.EOF {
		return ErrInvalid
	}
	if err = file.Close(); err != nil {
		return ErrUnavailable
	}
	closed = true
	return store.Rename(ctx, tmp, clean)
}
