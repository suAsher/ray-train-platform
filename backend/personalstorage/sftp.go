package personalstorage

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

type sftpFactory struct {
	address           string
	keys              []ssh.PublicKey
	hostKeyAlgorithms []string
}

// NewSFTPFactory accepts only administrator-configured fixed origins and pinned keys.
func NewSFTPFactory(e Endpoint) (Factory, error) {
	if e.Host == "" || strings.ContainsAny(e.Host, "/\\ \t\r\n") || e.Port < 1 || e.Port > 65535 || len(e.KnownHosts) == 0 {
		return nil, ErrInvalid
	}
	address := net.JoinHostPort(e.Host, strconv.Itoa(e.Port))
	normalized := knownhosts.Normalize(address)
	rest := e.KnownHosts
	var keys []ssh.PublicKey
	for len(rest) > 0 {
		marker, hosts, key, _, remaining, err := ssh.ParseKnownHosts(rest)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, ErrInvalid
		}
		rest = remaining
		if marker != "" {
			return nil, ErrInvalid
		}
		for _, host := range hosts {
			if host == normalized {
				keys = append(keys, key)
			}
		}
	}
	if len(keys) == 0 {
		return nil, ErrInvalid
	}
	algorithms, err := trustedHostKeyAlgorithms(keys)
	if err != nil {
		return nil, err
	}
	return &sftpFactory{address: address, keys: keys, hostKeyAlgorithms: algorithms}, nil
}

// Host public key formats and negotiated signature algorithms differ for RSA.
// Intersect the trusted pin types with the library's safe algorithms so other
// server keys cannot be selected, and SHA-1/DSA never become a fallback.
func trustedHostKeyAlgorithms(keys []ssh.PublicKey) ([]string, error) {
	keyType := func(algorithm string) string {
		switch algorithm {
		case ssh.KeyAlgoRSASHA256, ssh.KeyAlgoRSASHA512:
			return ssh.KeyAlgoRSA
		case ssh.CertAlgoRSASHA256v01, ssh.CertAlgoRSASHA512v01:
			return ssh.CertAlgoRSAv01
		default:
			return algorithm
		}
	}
	supported := ssh.SupportedAlgorithms().HostKeys
	safeTypes := make(map[string]bool)
	for _, algorithm := range supported {
		safeTypes[keyType(algorithm)] = true
	}
	pinnedTypes := make(map[string]bool)
	for _, key := range keys {
		if !safeTypes[key.Type()] {
			return nil, ErrInvalid
		}
		pinnedTypes[key.Type()] = true
	}
	var algorithms []string
	for _, algorithm := range supported {
		if pinnedTypes[keyType(algorithm)] {
			algorithms = append(algorithms, algorithm)
		}
	}
	if len(algorithms) == 0 {
		return nil, ErrInvalid
	}
	return algorithms, nil
}
func (f *sftpFactory) Open(ctx context.Context, c Credential) (Store, error) {
	if ValidateOwner(Owner{ID: c.OwnerID, Username: c.Username}) != nil || c.BindingID == "" || c.Generation != 1 {
		return nil, ErrInvalid
	}
	signer, err := ssh.ParsePrivateKey(c.PrivateKey)
	if err != nil || signer.PublicKey().Type() != ssh.KeyAlgoED25519 {
		return nil, ErrConflict
	}
	c.PrivateKey = append([]byte(nil), c.PrivateKey...)
	cfg := &ssh.ClientConfig{User: c.Username, HostKeyAlgorithms: append([]string(nil), f.hostKeyAlgorithms...), Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)}, HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
		for _, pin := range f.keys {
			if bytes.Equal(pin.Marshal(), key.Marshal()) {
				return nil
			}
		}
		return ErrForbidden
	}, Timeout: 15 * time.Second}
	conn, err := (&net.Dialer{Timeout: 15 * time.Second}).DialContext(ctx, "tcp", f.address)
	if err != nil {
		return nil, ErrUnavailable
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	_ = conn.SetDeadline(time.Now().Add(15 * time.Second))
	sshConn, chans, reqs, err := ssh.NewClientConn(conn, f.address, cfg)
	if err != nil {
		_ = conn.Close()
		return nil, ErrUnavailable
	}
	sshClient := ssh.NewClient(sshConn, chans, reqs)
	session, err := sshClient.NewSession()
	if err != nil {
		_ = sshClient.Close()
		return nil, ErrUnavailable
	}
	stdin, err := session.StdinPipe()
	if err != nil {
		_ = sshClient.Close()
		return nil, ErrUnavailable
	}
	stdout, err := session.StdoutPipe()
	if err != nil {
		_ = sshClient.Close()
		return nil, ErrUnavailable
	}
	if err = session.RequestSubsystem("sftp"); err != nil {
		_ = sshClient.Close()
		return nil, ErrUnavailable
	}
	budget := &responseBudget{reader: stdout, close: func() { _ = conn.Close() }}
	client, err := sftp.NewClientPipe(budget, stdin)
	if err != nil {
		_ = sshClient.Close()
		return nil, ErrUnavailable
	}
	_ = conn.SetDeadline(time.Time{})
	store := &sftpStore{client: client, ssh: sshClient, conn: conn, budget: budget, session: session, reconnect: func(cleanupCtx context.Context) (Store, error) { return f.Open(cleanupCtx, c) }}
	store.mu.Lock()
	store.stop = context.AfterFunc(ctx, func() { _ = store.Close() })
	store.mu.Unlock()
	if _, err := store.Stat(ctx, ""); err != nil {
		_ = store.Close()
		return nil, err
	}
	return store, nil
}

// responseBudget bounds bytes before pkg/sftp can allocate an unbounded directory
// listing. Exceeding the budget terminates this session; callers must reconnect.
// A concurrent file operation shares this budget and can fail closed during List.
type responseBudget struct {
	reader    io.Reader
	close     func()
	mu        sync.Mutex
	remaining int64
	limited   bool
}

func (b *responseBudget) Read(p []byte) (int, error) {
	n, err := b.reader.Read(p)
	b.mu.Lock()
	exceeded := false
	if b.limited {
		b.remaining -= int64(n)
		exceeded = b.remaining < 0
	}
	b.mu.Unlock()
	if exceeded {
		b.close()
		return 0, ErrTooLarge
	}
	return n, err
}
func (b *responseBudget) set(limit int64) {
	b.mu.Lock()
	b.remaining = limit
	b.limited = limit > 0
	b.mu.Unlock()
}

type sftpStore struct {
	client    *sftp.Client
	ssh       *ssh.Client
	session   *ssh.Session
	conn      net.Conn
	once      sync.Once
	mu        sync.Mutex
	stop      func() bool
	budget    *responseBudget
	listMu    sync.Mutex
	reconnect func(context.Context) (Store, error)
}

func (s *sftpStore) Close() error {
	s.once.Do(func() {
		s.mu.Lock()
		stop := s.stop
		s.mu.Unlock()
		if stop != nil {
			stop()
		}
		_ = s.conn.Close()
		_ = s.client.Close()
		_ = s.session.Close()
		_ = s.ssh.Close()
	})
	return nil
}
func (s *sftpStore) operation(ctx context.Context) func() {
	stop := context.AfterFunc(ctx, func() { _ = s.Close() })
	timer := time.AfterFunc(30*time.Second, func() { _ = s.Close() })
	return func() { stop(); timer.Stop() }
}
func entry(info os.FileInfo) Entry {
	return Entry{Name: info.Name(), Size: info.Size(), Mode: info.Mode(), ModTime: info.ModTime()}
}
func (s *sftpStore) checked(p string, missingLeaf bool) (string, error) {
	clean, err := ValidatePath(p)
	if err != nil {
		return "", err
	}
	remote := "/" + clean
	root, err := s.client.Lstat("/")
	if err != nil {
		return "", safeError(err)
	}
	if !root.IsDir() || root.Mode()&os.ModeSymlink != 0 {
		return "", ErrForbidden
	}
	current := ""
	parts := strings.Split(clean, "/")
	if clean != "" {
		for i, part := range parts {
			current += "/" + part
			info, err := s.client.Lstat(current)
			if err != nil {
				if os.IsNotExist(err) && missingLeaf && i == len(parts)-1 {
					return remote, nil
				}
				return "", safeError(err)
			}
			if err = ValidateEntry(entry(info)); err != nil {
				return "", err
			}
			if i < len(parts)-1 && !info.IsDir() {
				return "", ErrInvalid
			}
		}
	}
	resolved, err := s.client.RealPath(remote)
	if err != nil {
		return "", safeError(err)
	}
	if resolved != remote {
		return "", ErrForbidden
	}
	return remote, nil
}
func (s *sftpStore) List(ctx context.Context, p string) ([]Entry, error) {
	s.listMu.Lock()
	defer s.listMu.Unlock()
	s.budget.set(8 * 1024 * 1024)
	defer s.budget.set(0)
	done := s.operation(ctx)
	defer done()
	remote, err := s.checked(p, false)
	if err != nil {
		return nil, err
	}
	infos, err := s.client.ReadDirContext(ctx, remote)
	if err != nil {
		return nil, safeError(err)
	}
	if len(infos) > 10000 {
		return nil, ErrTooLarge
	}
	out := make([]Entry, 0, len(infos))
	for _, info := range infos {
		e := entry(info)
		if ValidateEntry(e) != nil {
			continue
		}
		if _, err := ValidatePath(path.Join(p, e.Name)); err != nil {
			continue
		}
		if _, err := s.checked(path.Join(p, e.Name), false); err != nil {
			continue
		}
		out = append(out, e)
	}
	return out, nil
}
func (s *sftpStore) Stat(ctx context.Context, p string) (Entry, error) {
	done := s.operation(ctx)
	defer done()
	remote, err := s.checked(p, false)
	if err != nil {
		return Entry{}, err
	}
	info, err := s.client.Lstat(remote)
	if err != nil {
		return Entry{}, safeError(err)
	}
	return entry(info), nil
}
func (s *sftpStore) Open(ctx context.Context, p string, flags int) (File, error) {
	done := s.operation(ctx)
	defer done()
	if p == "" || flags & ^(os.O_WRONLY|os.O_RDWR|os.O_APPEND|os.O_CREATE|os.O_EXCL|os.O_SYNC|os.O_TRUNC) != 0 {
		return nil, ErrInvalid
	}
	remote, err := s.checked(p, flags&os.O_CREATE != 0)
	if err != nil {
		return nil, err
	}
	file, err := s.client.OpenFile(remote, flags)
	if err != nil {
		return nil, safeError(err)
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = file.Close()
		return nil, ErrForbidden
	}
	return &sftpFile{file: file, store: s}, nil
}
func (s *sftpStore) Mkdir(ctx context.Context, p string) error {
	done := s.operation(ctx)
	defer done()
	if p == "" {
		return ErrInvalid
	}
	remote, err := s.checked(p, true)
	if err != nil {
		return err
	}
	return safeError(s.client.Mkdir(remote))
}
func (s *sftpStore) Remove(ctx context.Context, p string) error {
	done := s.operation(ctx)
	defer done()
	if p == "" {
		return ErrForbidden
	}
	remote, err := s.checked(p, false)
	if err != nil {
		return err
	}
	return safeError(s.client.Remove(remote))
}
func (s *sftpStore) Rename(ctx context.Context, old, new string) error {
	done := s.operation(ctx)
	defer done()
	if old == "" || new == "" {
		return ErrForbidden
	}
	from, err := s.checked(old, false)
	if err != nil {
		return err
	}
	to, err := s.checked(new, true)
	if err != nil {
		return err
	}
	return safeError(s.client.Rename(from, to))
}

type sftpFile struct {
	file  *sftp.File
	store *sftpStore
}

func (f *sftpFile) op() func() {
	timer := time.AfterFunc(30*time.Second, func() { _ = f.store.Close() })
	return func() { timer.Stop() }
}
func (f *sftpFile) ReadAt(p []byte, off int64) (int, error) {
	done := f.op()
	defer done()
	n, err := f.file.ReadAt(p, off)
	if errors.Is(err, io.EOF) {
		return n, io.EOF
	}
	return n, safeError(err)
}
func (f *sftpFile) WriteAt(p []byte, off int64) (int, error) {
	done := f.op()
	defer done()
	n, err := f.file.WriteAt(p, off)
	return n, safeError(err)
}
func (f *sftpFile) Stat() (os.FileInfo, error) {
	done := f.op()
	defer done()
	info, err := f.file.Stat()
	return info, safeError(err)
}
func (f *sftpFile) Sync() error { done := f.op(); defer done(); return safeError(f.file.Sync()) }
func (f *sftpFile) Truncate(size int64) error {
	if size < 0 {
		return ErrInvalid
	}
	done := f.op()
	defer done()
	return safeError(f.file.Truncate(size))
}
func (f *sftpFile) Close() error { done := f.op(); defer done(); return safeError(f.file.Close()) }

// Cleanup uses a fresh owner-authorized session because request cancellation
// closes the original connection. Its caller supplies a bounded cleanup context.
func (s *sftpStore) Cleanup(ctx context.Context, name string) error {
	if s.reconnect == nil {
		return ErrUnavailable
	}
	store, err := s.reconnect(ctx)
	if err != nil {
		return err
	}
	defer store.Close()
	err = store.Remove(ctx, name)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	return err
}
