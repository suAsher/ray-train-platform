// personal-storage-acceptance is a narrowly scoped operator tool, not a user API.
// Build on the builder from backend/: go build ../ops/personal-storage-acceptance/main.go
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"golang.org/x/sys/unix"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"ray-train-platform-backend/personalfs"
	"ray-train-platform-backend/personalstorage"
)

const (
	ownerID         = "5977834169122c6a964370132f5c0e62"
	username        = "guofeng.su"
	namespace       = "ray-train-platform"
	host            = "mount.wellspiking.ai"
	hostFingerprint = "SHA256:q62n6tQyLK3O/UaD25LKPP4KARLqyIslmNC0XVKvdnA"
	markerPrefix    = "rtp-personal-acceptance-"
)

var owner = personalstorage.Owner{ID: ownerID, Username: username}

type options struct {
	action, kubeconfig, knownHosts, stateDir string
	keep                                     bool
}

type markerFailure struct {
	cause  error
	marker string
}

type diagnosticFailure struct { class string }
func (e *diagnosticFailure) Error() string { return e.class }

func (e *markerFailure) Error() string { return "personal acceptance failed" }
func (e *markerFailure) Unwrap() error { return e.cause }

func tagMarkerFailure(marker string, err *error) {
	if marker != "" && *err != nil {
		*err = &markerFailure{cause: *err, marker: marker}
	}
}

// Repository data is public binding metadata only. The command holds an
// exclusive OS lock across the complete operation, including network calls.
type fileRepository struct{ directory string }

func (r fileRepository) Get(ctx context.Context, id string) (personalstorage.Binding, error) {
	if ctx.Err() != nil || id != ownerID {
		return personalstorage.Binding{}, personalstorage.ErrForbidden
	}
	p := filepath.Join(r.directory, "binding.json")
	info, err := os.Lstat(p)
	if os.IsNotExist(err) {
		return personalstorage.Binding{}, personalstorage.ErrNotFound
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > 16384 {
		return personalstorage.Binding{}, personalstorage.ErrConflict
	}
	b, err := os.ReadFile(p)
	if err != nil || len(b) > 16384 {
		return personalstorage.Binding{}, personalstorage.ErrConflict
	}
	var binding personalstorage.Binding
	decoder := json.NewDecoder(bytes.NewReader(b))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&binding) != nil || decoder.Decode(new(any)) != io.EOF {
		return personalstorage.Binding{}, personalstorage.ErrConflict
	}
	if binding.OwnerID != ownerID || binding.Username != username || binding.Generation != 1 {
		return personalstorage.Binding{}, personalstorage.ErrConflict
	}
	return binding, nil
}

func (r fileRepository) Create(ctx context.Context, b personalstorage.Binding) (personalstorage.Binding, error) {
	if existing, err := r.Get(ctx, b.OwnerID); err == nil {
		return existing, nil
	} else if !errors.Is(err, personalstorage.ErrNotFound) {
		return personalstorage.Binding{}, err
	}
	if b.OwnerID != ownerID || b.Username != username || b.Generation != 1 {
		return personalstorage.Binding{}, personalstorage.ErrForbidden
	}
	if err := r.save(b); err != nil {
		return personalstorage.Binding{}, err
	}
	return b, nil
}

func (r fileRepository) MarkVerified(ctx context.Context, id, bindingID string, generation int64) (personalstorage.Binding, error) {
	b, err := r.Get(ctx, id)
	if err != nil {
		return personalstorage.Binding{}, err
	}
	if b.ID != bindingID || b.Generation != generation {
		return personalstorage.Binding{}, personalstorage.ErrConflict
	}
	now := time.Now().UTC()
	b.State, b.VerifiedAt = personalstorage.StateReady, &now
	if err := r.save(b); err != nil {
		return personalstorage.Binding{}, err
	}
	return b, nil
}

func (r fileRepository) save(b personalstorage.Binding) error {
	encoded, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return personalstorage.ErrUnavailable
	}
	return atomicPublicFile(r.directory, "binding.json", append(encoded, '\n'))
}

func atomicPublicFile(directory, name string, data []byte) error {
	p := filepath.Join(directory, name)
	if info, err := os.Lstat(p); err == nil {
		if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
			return personalstorage.ErrConflict
		}
	} else if !os.IsNotExist(err) {
		return personalstorage.ErrUnavailable
	}
	f, err := os.CreateTemp(directory, ".metadata-*")
	if err != nil {
		return personalstorage.ErrUnavailable
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		return personalstorage.ErrUnavailable
	}
	if err = os.Rename(f.Name(), p); err != nil {
		return personalstorage.ErrUnavailable
	}
	d, err := os.Open(directory)
	if err != nil {
		return personalstorage.ErrUnavailable
	}
	defer d.Close()
	return d.Sync()
}

func secureStateDir(p string) (*os.File, error) {
	if !filepath.IsAbs(p) || filepath.Clean(p) != p {
		return nil, personalstorage.ErrInvalid
	}
	if err := os.Mkdir(p, 0700); err != nil && !os.IsExist(err) {
		return nil, personalstorage.ErrUnavailable
	}
	resolved, err := filepath.EvalSymlinks(p)
	if err != nil || resolved != p {
		return nil, personalstorage.ErrForbidden
	}
	info, err := os.Stat(p)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		return nil, personalstorage.ErrForbidden
	}
	fd, err := unix.Open(filepath.Join(p, ".operator.lock"), unix.O_CREAT|unix.O_RDWR|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, personalstorage.ErrUnavailable
	}
	f := os.NewFile(uintptr(fd), "operator-lock")
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		f.Close()
		return nil, personalstorage.ErrConflict
	}
	return f, nil
}

func pinnedFactory(p string) (personalstorage.Factory, error) {
	data, err := os.ReadFile(p)
	if err != nil || len(data) > 65536 {
		return nil, personalstorage.ErrInvalid
	}
	rest, found := data, false
	for len(rest) > 0 {
		marker, hosts, key, _, remaining, err := ssh.ParseKnownHosts(rest)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil || marker != "" {
			return nil, personalstorage.ErrInvalid
		}
		rest = remaining
		for _, h := range hosts {
			if h == host || h == "["+host+"]:22" {
				if key.Type() != ssh.KeyAlgoED25519 || ssh.FingerprintSHA256(key) != hostFingerprint {
					return nil, personalstorage.ErrForbidden
				}
				found = true
			}
		}
	}
	if !found {
		return nil, personalstorage.ErrForbidden
	}
	return personalstorage.NewSFTPFactory(personalstorage.Endpoint{Host: host, Port: 22, KnownHosts: data})
}

func emit(v any) { _ = json.NewEncoder(os.Stdout).Encode(v) }

func main() {
	var o options
	flag.StringVar(&o.action, "action", "", "enroll, diagnose, check or mount-check")
	flag.StringVar(&o.kubeconfig, "kubeconfig", "", "explicit operator kubeconfig")
	flag.StringVar(&o.knownHosts, "known-hosts", "", "previously trusted Wellspiking known_hosts")
	flag.StringVar(&o.stateDir, "state-dir", "", "absolute private operator state directory (0700)")
	flag.BoolVar(&o.keep, "keep-markers", false, "retain only this run's marker directory for user inspection")
	flag.Parse()
	if flag.NArg() != 0 || o.kubeconfig == "" || o.knownHosts == "" || o.stateDir == "" || (o.action != "enroll" && o.action != "diagnose" && o.action != "check" && o.action != "mount-check") {
		emit(map[string]any{"ok": false, "stage": "arguments"})
		os.Exit(2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if stage, err := run(ctx, o); err != nil {
		// Only our newly generated marker name may be exposed for recovery.
		// Never print upstream/Kubernetes error text or private file names.
		v := map[string]any{"ok": false, "stage": stage}
		var failure *markerFailure
		if errors.As(err, &failure) {
			v["markerDirectory"] = failure.marker
		}
		var diagnosis *diagnosticFailure
		if errors.As(err, &diagnosis) { v["errorClass"] = diagnosis.class }
		emit(v)
		os.Exit(1)
	}
}

func run(ctx context.Context, o options) (string, error) {
	lock, err := secureStateDir(o.stateDir)
	if err != nil {
		return "state-directory", err
	}
	defer lock.Close()
	factory, err := pinnedFactory(o.knownHosts)
	if err != nil {
		return "host-pin", err
	}
	cfg, err := clientcmd.BuildConfigFromFlags("", o.kubeconfig)
	if err != nil {
		return "kubeconfig", err
	}
	cfg.Timeout = 20 * time.Second
	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return "kubernetes-client", err
	}
	service := personalstorage.NewService(fileRepository{o.stateDir}, personalstorage.NewKubernetesVault(client, namespace), factory)
	if o.action == "diagnose" { return diagnose(ctx, fileRepository{o.stateDir}, personalstorage.NewKubernetesVault(client, namespace)) }
	if o.action == "enroll" {
		v, err := service.Enroll(ctx, owner)
		if err != nil {
			return "enroll", err
		}
		if err := atomicPublicFile(o.stateDir, "public-key.pub", []byte(v.PublicKey)); err != nil {
			return "public-key-file", err
		}
		emit(map[string]any{"ok": true, "action": "enroll", "fingerprint": v.Fingerprint})
		return "", nil
	}
	if _, err := service.Verify(ctx, owner); err != nil {
		return "verify", err
	}
	if err := rejectedPaths(); err != nil {
		return "policy", err
	}
	if o.action == "mount-check" {
		return mountCheck(ctx, service, o.keep)
	}
	return storeCheck(ctx, service, o.keep)
}

// diagnose performs no SFTP writes, list operations, binding updates or
// authentication fallback. It uses only the existing fixed owner's Secret.
func diagnose(ctx context.Context, repo fileRepository, vault *personalstorage.KubernetesVault) (string, error) {
	fail := func(stage string, err error) (string, error) { return stage, &diagnosticFailure{class: diagnosticClass(err)} }
	b, err := repo.Get(ctx, ownerID)
	if err != nil { return fail("diagnose-binding", err) }
	credential, err := vault.Load(ctx, b)
	if err != nil { return fail("diagnose-secret", err) }
	signer, err := ssh.ParsePrivateKey(credential.PrivateKey)
	if err != nil { return fail("diagnose-private-key", personalstorage.ErrConflict) }
	if string(ssh.MarshalAuthorizedKey(signer.PublicKey())) != b.PublicKey || ssh.FingerprintSHA256(signer.PublicKey()) != b.Fingerprint {
		return fail("diagnose-private-key", personalstorage.ErrConflict)
	}
	address := net.JoinHostPort(host, "22")
	conn, err := (&net.Dialer{Timeout: 15*time.Second}).DialContext(ctx, "tcp", address)
	if err != nil { return fail("diagnose-tcp", err) }
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	if err := conn.SetDeadline(time.Now().Add(30*time.Second)); err != nil { return fail("diagnose-deadline", err) }
	emit(map[string]any{"ok": true, "stage": "diagnose-tcp"})
	sshConfig := &ssh.ClientConfig{User: username, Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)}, Timeout: 15*time.Second, HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
		fingerprint := ssh.FingerprintSHA256(key)
		matches := key.Type() == ssh.KeyAlgoED25519 && fingerprint == hostFingerprint
		// SSH host-key metadata is public; no key material or private names.
		emit(map[string]any{"stage": "diagnose-host-key", "algorithm": key.Type(), "fingerprint": fingerprint, "pinMatches": matches})
		if !matches { return personalstorage.ErrForbidden }
		return nil
	}}
	sshConn, channels, requests, err := ssh.NewClientConn(conn, address, sshConfig)
	if err != nil { return fail("diagnose-ssh-publickey", err) }
	sshClient := ssh.NewClient(sshConn, channels, requests)
	defer sshClient.Close()
	emit(map[string]any{"ok": true, "stage": "diagnose-ssh-publickey"})
	session, err := sshClient.NewSession()
	if err != nil { return fail("diagnose-session", err) }
	defer session.Close()
	stdin, err := session.StdinPipe()
	if err != nil { return fail("diagnose-stdin", err) }
	stdout, err := session.StdoutPipe()
	if err != nil { return fail("diagnose-stdout", err) }
	if err := session.RequestSubsystem("sftp"); err != nil { return fail("diagnose-sftp-subsystem", err) }
	emit(map[string]any{"ok": true, "stage": "diagnose-sftp-subsystem"})
	client, err := sftp.NewClientPipe(stdout, stdin)
	if err != nil { return fail("diagnose-sftp-initialize", err) }
	defer client.Close()
	info, err := client.Lstat("/")
	if err != nil { return fail("diagnose-root-lstat", err) }
	emit(map[string]any{"ok": true, "stage": "diagnose-root-lstat", "isDir": info.IsDir(), "isSymlink": info.Mode()&os.ModeSymlink != 0})
	resolved, err := client.RealPath("/")
	if err != nil { return fail("diagnose-root-realpath", err) }
	emit(map[string]any{"ok": true, "stage": "diagnose-root-realpath", "realPathEqualsRoot": resolved == "/"})
	return "", nil
}

func diagnosticClass(err error) string {
	if errors.Is(err, context.DeadlineExceeded) { return "timeout" }
	var network net.Error
	if errors.As(err, &network) && network.Timeout() { return "timeout" }
	if errors.Is(err, io.EOF) { return "connection_closed" }
	if errors.Is(err, personalstorage.ErrConflict) { return "credential_conflict" }
	if errors.Is(err, personalstorage.ErrForbidden) { return "host_key_mismatch" }
	if errors.Is(err, personalstorage.ErrNotFound) { return "not_found" }
	var status *sftp.StatusError
	if errors.As(err, &status) {
		switch status.Code {
		case 2: return "not_found"
		case 3: return "permission_denied"
		case 8: return "operation_unsupported"
		default: return "sftp_failure"
		}
	}
	// Client authentication errors are not exported as a typed error by x/crypto.
	// Match only to select a fixed safe label; never output the original text.
	if strings.Contains(err.Error(), "unable to authenticate") { return "publickey_authentication_failed" }
	if strings.Contains(err.Error(), personalstorage.ErrForbidden.Error()) { return "host_key_mismatch" }
	return "transport_or_protocol_failure"
}

func rejectedPaths() error {
	for _, root := range []string{".minio", ".original", ".wellspiking", ".spk-hybrid", ".spk-ssd", ".shared", ".spiking-group-shared"} {
		if _, err := personalstorage.ValidatePath(root); err == nil {
			return personalstorage.ErrForbidden
		}
		if _, err := personalstorage.ValidatePath(root + "/test"); err == nil {
			return personalstorage.ErrForbidden
		}
	}
	for _, p := range []string{"../other-user", "/other-user", "a/../../other-user", "a\\other-user"} {
		if _, err := personalstorage.ValidatePath(p); err == nil {
			return personalstorage.ErrForbidden
		}
	}
	return nil
}

func markerID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6], b[8] = (b[6]&0x0f)|0x40, (b[8]&0x3f)|0x80
	h := hex.EncodeToString(b[:])
	return markerPrefix + h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:], nil
}

func evidence(action, marker string, keep bool) {
	v := map[string]any{"ok": true, "action": action, "owner": username, "hostFingerprint": hostFingerprint, "policyRejected": true, "readWriteRenameRemove": true, "retained": keep, "crossAccountIsolationVerified": false, "productionEnabled": false}
	if keep {
		v["markerDirectory"] = marker
	}
	emit(v)
}

func storeCheck(ctx context.Context, service *personalstorage.Service, keep bool) (stage string, retErr error) {
	var marker string
	defer func() { tagMarkerFailure(marker, &retErr) }()
	defer func() {
		if retErr == nil {
			evidence("check", marker, keep)
		}
	}()
	store, err := service.OpenStore(ctx, owner)
	if err != nil {
		return "sftp-open", err
	}
	defer store.Close()
	marker, err = markerID()
	if err != nil {
		return "marker", err
	}
	paths := []string{marker + "/probe.txt", marker + "/renamed.txt", marker + "/sacrificial.txt", marker + "/child", marker}
	defer func() {
		if keep && retErr == nil {
			return
		}
		if err := cleanupMarker(service, marker); err != nil {
			stage, retErr = "cleanup", err
		}
	}()
	if err := store.Mkdir(ctx, marker); err != nil {
		return "marker-mkdir", err
	}
	if err := store.Mkdir(ctx, marker+"/child"); err != nil {
		return "sftp-mkdir", err
	}
	payload := []byte("RayTrain personal storage functional acceptance\n")
	f, err := store.Open(ctx, paths[0], os.O_RDWR|os.O_CREATE|os.O_EXCL)
	if err != nil {
		return "sftp-create", err
	}
	n, err := f.WriteAt(payload, 0)
	if err != nil || n != len(payload) {
		f.Close()
		return "sftp-write", personalstorage.ErrUnavailable
	}
	buf := make([]byte, len(payload))
	n, err = f.ReadAt(buf, 0)
	closeErr := f.Close()
	if err != nil || closeErr != nil || n != len(payload) || !bytes.Equal(buf, payload) {
		return "sftp-read", personalstorage.ErrUnavailable
	}
	if err := store.Rename(ctx, paths[0], paths[1]); err != nil {
		return "sftp-rename", err
	}
	entries, err := store.List(ctx, marker)
	if err != nil || !hasEntry(entries, "renamed.txt") || !hasEntry(entries, "child") {
		return "sftp-list", personalstorage.ErrUnavailable
	}
	if !keep {
		if err := store.Remove(ctx, paths[1]); err != nil {
			return "sftp-remove", err
		}
		if _, err := store.Stat(ctx, paths[1]); !errors.Is(err, personalstorage.ErrNotFound) {
			return "sftp-remove-stat", personalstorage.ErrUnavailable
		}
	} else {
		sacrificial, err := store.Open(ctx, paths[2], os.O_WRONLY|os.O_CREATE|os.O_EXCL)
		if err != nil {
			return "sftp-sacrificial-create", err
		}
		if err := sacrificial.Close(); err != nil {
			return "sftp-sacrificial-close", err
		}
		if err := store.Remove(ctx, paths[2]); err != nil {
			return "sftp-remove", err
		}
		if _, err := store.Stat(ctx, paths[2]); !errors.Is(err, personalstorage.ErrNotFound) {
			return "sftp-remove-stat", personalstorage.ErrUnavailable
		}
	}
	return "", nil
}

func hasEntry(entries []personalstorage.Entry, name string) bool {
	for _, e := range entries {
		if e.Name == name {
			return true
		}
	}
	return false
}

// A lost creation acknowledgment may still have created the directory. Install
// this independent, bounded cleanup before attempting mkdir; do not rely on a
// canceled SFTP session or disconnected FUSE mount to perform recovery.
func cleanupMarker(service *personalstorage.Service, marker string) error {
	if !strings.HasPrefix(marker, markerPrefix) || len(marker) != len(markerPrefix)+36 || strings.ContainsAny(marker, "/\\") {
		return personalstorage.ErrForbidden
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	store, err := service.OpenStore(ctx, owner)
	if err != nil {
		return err
	}
	defer store.Close()
	var cleanupErr error
	for _, suffix := range []string{"/probe.txt", "/renamed.txt", "/sacrificial.txt", "/child", ""} {
		if err := store.Remove(ctx, marker+suffix); err != nil && !errors.Is(err, personalstorage.ErrNotFound) {
			cleanupErr = err
		}
	}
	return cleanupErr
}

func mountCheck(ctx context.Context, service *personalstorage.Service, keep bool) (stage string, retErr error) {
	var marker string
	defer func() { tagMarkerFailure(marker, &retErr) }()
	defer func() {
		if retErr == nil {
			evidence("mount-check", marker, keep)
		}
	}()
	var fs unix.Statfs_t
	if err := unix.Statfs("/run", &fs); err != nil || fs.Type != unix.TMPFS_MAGIC {
		return "mount-requires-private-tmpfs-run", personalstorage.ErrForbidden
	}
	outer, err := os.MkdirTemp("/run", markerPrefix+"mount-")
	if err != nil {
		return "mount-target", err
	}
	// The private outer directory keeps the mounted root inaccessible to other
	// local users even if its upstream permission bits are permissive.
	target := filepath.Join(outer, "mount")
	defer func() {
		// Remove only empty local directories, after the later unmount defer.
		// Never recursively remove a target that might still be mounted.
		if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
			stage, retErr = "mount-target-cleanup", err
		}
		if err := os.Remove(outer); err != nil && !os.IsNotExist(err) {
			stage, retErr = "mount-parent-cleanup", err
		}
	}()
	if err := os.Mkdir(target, 0700); err != nil {
		return "mount-target", err
	}
	store, err := service.OpenStore(ctx, owner)
	if err != nil {
		return "mount-open", err
	}
	mount, err := personalfs.NewMount(ctx, target, "operator-guofeng-functional", store)
	if err != nil {
		store.Close()
		return "mount", err
	}
	defer func() {
		if err := mount.Close(); err != nil {
			stage, retErr = "unmount", err
		}
	}()
	marker, err = markerID()
	if err != nil {
		return "marker", err
	}
	dir := filepath.Join(target, marker)
	defer func() {
		if keep && retErr == nil {
			return
		}
		if err := cleanupMarker(service, marker); err != nil {
			stage, retErr = "fuse-cleanup", err
		}
	}()
	if err := os.Mkdir(dir, 0700); err != nil {
		return "fuse-marker-mkdir", err
	}
	if err := os.Mkdir(filepath.Join(dir, "child"), 0700); err != nil {
		return "fuse-mkdir", err
	}
	payload := []byte("RayTrain personal storage FUSE acceptance\n")
	p := filepath.Join(dir, "probe.txt")
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return "fuse-create", err
	}
	n, err := f.Write(payload)
	closeErr := f.Close()
	if err != nil || closeErr != nil || n != len(payload) {
		return "fuse-write", personalstorage.ErrUnavailable
	}
	read, err := os.ReadFile(p)
	if err != nil || !bytes.Equal(read, payload) {
		return "fuse-read", personalstorage.ErrUnavailable
	}
	if err := os.Rename(p, filepath.Join(dir, "renamed.txt")); err != nil {
		return "fuse-rename", err
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 2 {
		return "fuse-readdir", personalstorage.ErrUnavailable
	}
	// Confirm the mount and service access the identical controlled SFTP root.
	other, err := service.OpenStore(ctx, owner)
	if err != nil {
		return "fuse-crosscheck-open", err
	}
	defer other.Close()
	remote, err := other.Open(ctx, marker+"/renamed.txt", os.O_RDONLY)
	if err != nil {
		return "fuse-crosscheck-file", err
	}
	buf := make([]byte, len(payload))
	n, err = remote.ReadAt(buf, 0)
	closeErr = remote.Close()
	if err != nil || closeErr != nil || n != len(payload) || !bytes.Equal(buf, payload) {
		return "fuse-crosscheck-content", personalstorage.ErrUnavailable
	}
	if !keep {
		if err := os.Remove(filepath.Join(dir, "renamed.txt")); err != nil {
			return "fuse-remove", err
		}
		if _, err := os.Stat(filepath.Join(dir, "renamed.txt")); !os.IsNotExist(err) {
			return "fuse-remove-stat", personalstorage.ErrUnavailable
		}
	} else {
		p := filepath.Join(dir, "sacrificial.txt")
		f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return "fuse-sacrificial-create", err
		}
		if err := f.Close(); err != nil {
			return "fuse-sacrificial-close", err
		}
		if err := os.Remove(p); err != nil {
			return "fuse-remove", err
		}
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			return "fuse-remove-stat", personalstorage.ErrUnavailable
		}
	}
	return "", nil
}
