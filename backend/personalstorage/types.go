// Package personalstorage provides owner-only, fixed-origin personal storage.
package personalstorage

import (
	"context"
	"errors"
	"io"
	"os"
	"time"
)

const (
	StateUnbound = "UNBOUND"
	StatePending = "PENDING"
	StateReady   = "READY"
	MountPath    = "/mnt/storage/wellspiking"
)

var (
	ErrInvalid     = errors.New("invalid personal storage request")
	ErrForbidden   = errors.New("personal storage access denied")
	ErrNotFound    = errors.New("personal storage not found")
	ErrNotReady    = errors.New("personal storage is not verified")
	ErrConflict    = errors.New("personal storage binding conflict")
	ErrUnavailable = errors.New("personal storage unavailable")
	ErrTooLarge    = errors.New("personal storage upload exceeds limit")
)

type Owner struct{ ID, Username string }
type Binding struct {
	ID, OwnerID, Username, SecretName, PublicKey, Fingerprint, State string
	Generation                                                       int64
	VerifiedAt                                                       *time.Time
}
type View struct {
	BindingID   string     `json:"bindingID,omitempty"`
	OwnerID     string     `json:"ownerID,omitempty"`
	Username    string     `json:"username,omitempty"`
	PublicKey   string     `json:"publicKey,omitempty"`
	Fingerprint string     `json:"fingerprint,omitempty"`
	State       string     `json:"state"`
	MountPath   string     `json:"mountPath"`
	Generation  int64      `json:"generation,omitempty"`
	VerifiedAt  *time.Time `json:"verifiedAt,omitempty"`
}
type MountReference struct {
	OwnerID, BindingID, Username, SecretName string
	Generation                               int64
}
type Credential struct {
	OwnerID, BindingID, Username string
	Generation                   int64
	PrivateKey                   []byte `json:"-"`
}
type Endpoint struct {
	Host       string
	Port       int
	KnownHosts []byte
}
type Entry struct {
	Name    string      `json:"name"`
	Size    int64       `json:"size"`
	Mode    os.FileMode `json:"mode"`
	ModTime time.Time   `json:"modTime"`
}

func (e Entry) IsDir() bool { return e.Mode.IsDir() }

type File interface {
	io.ReaderAt
	io.WriterAt
	Stat() (os.FileInfo, error)
	Sync() error
	Truncate(int64) error
	Close() error
}
type Store interface {
	List(context.Context, string) ([]Entry, error)
	Stat(context.Context, string) (Entry, error)
	Open(context.Context, string, int) (File, error)
	Mkdir(context.Context, string) error
	Remove(context.Context, string) error
	Rename(context.Context, string, string) error
	Close() error
}
type Factory interface {
	Open(context.Context, Credential) (Store, error)
}
type Repository interface {
	Get(context.Context, string) (Binding, error)
	Create(context.Context, Binding) (Binding, error)
	MarkVerified(context.Context, string, string, int64) (Binding, error)
}
type Vault interface {
	Ensure(context.Context, Binding, []byte) (Credential, error)
	Load(context.Context, Binding) (Credential, error)
}
