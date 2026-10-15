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
	"os"
	"path/filepath"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/sys/unix"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"ray-train-platform-backend/personalfs"
	"ray-train-platform-backend/personalstorage"
)

const (
	ownerID = "5977834169122c6a964370132f5c0e62"
	username = "guofeng.su"
	namespace = "ray-train-platform"
	host = "mount.wellspiking.ai"
	hostFingerprint = "SHA256:q62n6tQyLK3O/UaD25LKPP4KARLqyIslmNC0XVKvdnA"
	markerPrefix = "rtp-personal-acceptance-"
)

var owner = personalstorage.Owner{ID: ownerID, Username: username}

type options struct {
	action, kubeconfig, knownHosts, stateDir string
	keep bool
}

// Repository data is public binding metadata only. The command holds an
// exclusive OS lock across the complete operation, including network calls.
type fileRepository struct { directory string }

func (r fileRepository) Get(ctx context.Context, id string) (personalstorage.Binding, error) {
	if ctx.Err() != nil || id != ownerID { return personalstorage.Binding{}, personalstorage.ErrForbidden }
	p := filepath.Join(r.directory, "binding.json")
	info, err := os.Lstat(p)
	if os.IsNotExist(err) { return personalstorage.Binding{}, personalstorage.ErrNotFound }
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > 16384 { return personalstorage.Binding{}, personalstorage.ErrConflict }
	b, err := os.ReadFile(p)
	if err != nil || len(b) > 16384 { return personalstorage.Binding{}, personalstorage.ErrConflict }
	var binding personalstorage.Binding
	decoder := json.NewDecoder(bytes.NewReader(b))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&binding) != nil || decoder.Decode(new(any)) != io.EOF { return personalstorage.Binding{}, personalstorage.ErrConflict }
	if binding.OwnerID != ownerID || binding.Username != username || binding.Generation != 1 { return personalstorage.Binding{}, personalstorage.ErrConflict }
	return binding, nil
}

func (r fileRepository) Create(ctx context.Context, b personalstorage.Binding) (personalstorage.Binding, error) {
	if existing, err := r.Get(ctx, b.OwnerID); err == nil { return existing, nil } else if !errors.Is(err, personalstorage.ErrNotFound) { return personalstorage.Binding{}, err }
	if b.OwnerID != ownerID || b.Username != username || b.Generation != 1 { return personalstorage.Binding{}, personalstorage.ErrForbidden }
	if err := r.save(b); err != nil { return personalstorage.Binding{}, err }
	return b, nil
}

func (r fileRepository) MarkVerified(ctx context.Context, id, bindingID string, generation int64) (personalstorage.Binding, error) {
	b, err := r.Get(ctx, id)
	if err != nil { return personalstorage.Binding{}, err }
	if b.ID != bindingID || b.Generation != generation { return personalstorage.Binding{}, personalstorage.ErrConflict }
	now := time.Now().UTC()
	b.State, b.VerifiedAt = personalstorage.StateReady, &now
	if err := r.save(b); err != nil { return personalstorage.Binding{}, err }
	return b, nil
}

func (r fileRepository) save(b personalstorage.Binding) error {
	encoded, err := json.MarshalIndent(b, "", "  ")
	if err != nil { return personalstorage.ErrUnavailable }
	return atomicPublicFile(r.directory, "binding.json", append(encoded, '\n'))
}

func atomicPublicFile(directory, name string, data []byte) error {
	p := filepath.Join(directory, name)
	if info, err := os.Lstat(p); err == nil {
		if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 { return personalstorage.ErrConflict }
	} else if !os.IsNotExist(err) { return personalstorage.ErrUnavailable }
	f, err := os.CreateTemp(directory, ".metadata-*")
	if err != nil { return personalstorage.ErrUnavailable }
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err == nil { err = f.Sync() }
	closeErr := f.Close()
	if err != nil || closeErr != nil { return personalstorage.ErrUnavailable }
	if err = os.Rename(f.Name(), p); err != nil { return personalstorage.ErrUnavailable }
	d, err := os.Open(directory)
	if err != nil { return personalstorage.ErrUnavailable }
	defer d.Close()
	return d.Sync()
}

func secureStateDir(p string) (*os.File, error) {
	if !filepath.IsAbs(p) || filepath.Clean(p) != p { return nil, personalstorage.ErrInvalid }
	if err := os.Mkdir(p, 0700); err != nil && !os.IsExist(err) { return nil, personalstorage.ErrUnavailable }
	resolved, err := filepath.EvalSymlinks(p)
	if err != nil || resolved != p { return nil, personalstorage.ErrForbidden }
	info, err := os.Stat(p)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 { return nil, personalstorage.ErrForbidden }
	fd, err := unix.Open(filepath.Join(p, ".operator.lock"), unix.O_CREAT|unix.O_RDWR|unix.O_NOFOLLOW, 0600)
	if err != nil { return nil, personalstorage.ErrUnavailable }
	f := os.NewFile(uintptr(fd), "operator-lock")
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil { f.Close(); return nil, personalstorage.ErrConflict }
	return f, nil
}

func pinnedFactory(p string) (personalstorage.Factory, error) {
	data, err := os.ReadFile(p)
	if err != nil || len(data) > 65536 { return nil, personalstorage.ErrInvalid }
	rest, found := data, false
	for len(rest) > 0 {
		marker, hosts, key, _, remaining, err := ssh.ParseKnownHosts(rest)
		if errors.Is(err, io.EOF) { break }
		if err != nil || marker != "" { return nil, personalstorage.ErrInvalid }
		rest = remaining
		for _, h := range hosts {
			if h == host || h == "["+host+"]:22" {
				if key.Type() != ssh.KeyAlgoED25519 || ssh.FingerprintSHA256(key) != hostFingerprint { return nil, personalstorage.ErrForbidden }
				found = true
			}
		}
	}
	if !found { return nil, personalstorage.ErrForbidden }
	return personalstorage.NewSFTPFactory(personalstorage.Endpoint{Host: host, Port: 22, KnownHosts: data})
}

func emit(v any) { _ = json.NewEncoder(os.Stdout).Encode(v) }

func main() {
	var o options
	flag.StringVar(&o.action, "action", "", "enroll, check or mount-check")
	flag.StringVar(&o.kubeconfig, "kubeconfig", "", "explicit operator kubeconfig")
	flag.StringVar(&o.knownHosts, "known-hosts", "", "previously trusted Wellspiking known_hosts")
	flag.StringVar(&o.stateDir, "state-dir", "", "absolute private operator state directory (0700)")
	flag.BoolVar(&o.keep, "keep-markers", false, "retain only this run's marker directory for user inspection")
	flag.Parse()
	if flag.NArg() != 0 || o.kubeconfig == "" || o.knownHosts == "" || o.stateDir == "" || (o.action != "enroll" && o.action != "check" && o.action != "mount-check") {
		emit(map[string]any{"ok": false, "stage": "arguments"}); os.Exit(2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if stage, err := run(ctx, o); err != nil {
		// Never print upstream/Kubernetes error text, credentials or file names.
		emit(map[string]any{"ok": false, "stage": stage}); os.Exit(1)
	}
}

func run(ctx context.Context, o options) (string, error) {
	lock, err := secureStateDir(o.stateDir)
	if err != nil { return "state-directory", err }
	defer lock.Close()
	factory, err := pinnedFactory(o.knownHosts)
	if err != nil { return "host-pin", err }
	cfg, err := clientcmd.BuildConfigFromFlags("", o.kubeconfig)
	if err != nil { return "kubeconfig", err }
	cfg.Timeout = 20*time.Second
	client, err := kubernetes.NewForConfig(cfg)
	if err != nil { return "kubernetes-client", err }
	service := personalstorage.NewService(fileRepository{o.stateDir}, personalstorage.NewKubernetesVault(client, namespace), factory)
	if o.action == "enroll" {
		v, err := service.Enroll(ctx, owner)
		if err != nil { return "enroll", err }
		if err := atomicPublicFile(o.stateDir, "public-key.pub", []byte(v.PublicKey)); err != nil { return "public-key-file", err }
		emit(map[string]any{"ok": true, "action": "enroll", "fingerprint": v.Fingerprint})
		return "", nil
	}
	if _, err := service.Verify(ctx, owner); err != nil { return "verify", err }
	if err := rejectedPaths(); err != nil { return "policy", err }
	if o.action == "mount-check" { return mountCheck(ctx, service, o.keep) }
	return storeCheck(ctx, service, o.keep)
}

func rejectedPaths() error {
	for _, root := range []string{".minio", ".original", ".wellspiking", ".spk-hybrid", ".spk-ssd", ".shared", ".spiking-group-shared"} {
		if _, err := personalstorage.ValidatePath(root); err == nil { return personalstorage.ErrForbidden }
		if _, err := personalstorage.ValidatePath(root+"/test"); err == nil { return personalstorage.ErrForbidden }
	}
	for _, p := range []string{"../other-user", "/other-user", "a/../../other-user", "a\\other-user"} {
		if _, err := personalstorage.ValidatePath(p); err == nil { return personalstorage.ErrForbidden }
	}
	return nil
}

func markerID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil { return "", err }
	b[6], b[8] = (b[6]&0x0f)|0x40, (b[8]&0x3f)|0x80
	h := hex.EncodeToString(b[:])
	return markerPrefix+h[:8]+"-"+h[8:12]+"-"+h[12:16]+"-"+h[16:20]+"-"+h[20:], nil
}

func evidence(action, marker string, keep bool) {
	v := map[string]any{"ok": true, "action": action, "owner": username, "hostFingerprint": hostFingerprint, "policyRejected": true, "readWriteRenameRemove": true, "retained": keep, "crossAccountIsolationVerified": false, "productionEnabled": false}
	if keep { v["markerDirectory"] = marker }
	emit(v)
}

func storeCheck(ctx context.Context, service *personalstorage.Service, keep bool) (stage string, retErr error) {
	var marker string
	defer func() { if retErr == nil { evidence("check", marker, keep) } }()
	store, err := service.OpenStore(ctx, owner)
	if err != nil { return "sftp-open", err }
	defer store.Close()
	marker, err = markerID()
	if err != nil { return "marker", err }
	if err := store.Mkdir(ctx, marker); err != nil { return "marker-mkdir", err }
	paths := []string{marker+"/probe.txt", marker+"/renamed.txt", marker+"/sacrificial.txt", marker+"/child", marker}
	defer func() {
		if keep && retErr == nil { return }
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		cleanup, err := service.OpenStore(cleanupCtx, owner)
		if err != nil { stage, retErr = "cleanup-connect", err; return }
		defer cleanup.Close()
		for _, p := range paths {
			if err := cleanup.Remove(cleanupCtx, p); err != nil && !errors.Is(err, personalstorage.ErrNotFound) { stage, retErr = "cleanup", err }
		}
	}()
	if err := store.Mkdir(ctx, marker+"/child"); err != nil { return "sftp-mkdir", err }
	payload := []byte("RayTrain personal storage functional acceptance\n")
	f, err := store.Open(ctx, paths[0], os.O_RDWR|os.O_CREATE|os.O_EXCL)
	if err != nil { return "sftp-create", err }
	n, err := f.WriteAt(payload, 0)
	if err != nil || n != len(payload) { f.Close(); return "sftp-write", personalstorage.ErrUnavailable }
	buf := make([]byte, len(payload))
	n, err = f.ReadAt(buf, 0)
	closeErr := f.Close()
	if err != nil || closeErr != nil || n != len(payload) || !bytes.Equal(buf, payload) { return "sftp-read", personalstorage.ErrUnavailable }
	if err := store.Rename(ctx, paths[0], paths[1]); err != nil { return "sftp-rename", err }
	entries, err := store.List(ctx, marker)
	if err != nil || !hasEntry(entries, "renamed.txt") || !hasEntry(entries, "child") { return "sftp-list", personalstorage.ErrUnavailable }
	if !keep {
		if err := store.Remove(ctx, paths[1]); err != nil { return "sftp-remove", err }
		if _, err := store.Stat(ctx, paths[1]); !errors.Is(err, personalstorage.ErrNotFound) { return "sftp-remove-stat", personalstorage.ErrUnavailable }
	} else {
		sacrificial, err := store.Open(ctx, paths[2], os.O_WRONLY|os.O_CREATE|os.O_EXCL)
		if err != nil { return "sftp-sacrificial-create", err }
		if err := sacrificial.Close(); err != nil { return "sftp-sacrificial-close", err }
		if err := store.Remove(ctx, paths[2]); err != nil { return "sftp-remove", err }
		if _, err := store.Stat(ctx, paths[2]); !errors.Is(err, personalstorage.ErrNotFound) { return "sftp-remove-stat", personalstorage.ErrUnavailable }
	}
	return "", nil
}

func hasEntry(entries []personalstorage.Entry, name string) bool {
	for _, e := range entries { if e.Name == name { return true } }
	return false
}

func mountCheck(ctx context.Context, service *personalstorage.Service, keep bool) (stage string, retErr error) {
	var marker string
	defer func() { if retErr == nil { evidence("mount-check", marker, keep) } }()
	var fs unix.Statfs_t
	if err := unix.Statfs("/run", &fs); err != nil || fs.Type != unix.TMPFS_MAGIC { return "mount-requires-private-tmpfs-run", personalstorage.ErrForbidden }
	target, err := os.MkdirTemp("/run", markerPrefix+"mount-")
	if err != nil { return "mount-target", err }
	defer os.Remove(target)
	store, err := service.OpenStore(ctx, owner)
	if err != nil { return "mount-open", err }
	mount, err := personalfs.NewMount(ctx, target, "operator-guofeng-functional", store)
	if err != nil { store.Close(); return "mount", err }
	defer func() { if err := mount.Close(); err != nil { stage, retErr = "unmount", err } }()
	marker, err = markerID()
	if err != nil { return "marker", err }
	dir := filepath.Join(target, marker)
	if err := os.Mkdir(dir, 0700); err != nil { return "fuse-marker-mkdir", err }
	defer func() {
		if keep && retErr == nil { return }
		for _, p := range []string{"probe.txt", "renamed.txt", "sacrificial.txt", "child", ""} {
			if err := os.Remove(filepath.Join(dir, p)); err != nil && !os.IsNotExist(err) { stage, retErr = "fuse-cleanup", err }
		}
	}()
	if err := os.Mkdir(filepath.Join(dir, "child"), 0700); err != nil { return "fuse-mkdir", err }
	payload := []byte("RayTrain personal storage FUSE acceptance\n")
	p := filepath.Join(dir, "probe.txt")
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil { return "fuse-create", err }
	n, err := f.Write(payload)
	closeErr := f.Close()
	if err != nil || closeErr != nil || n != len(payload) { return "fuse-write", personalstorage.ErrUnavailable }
	read, err := os.ReadFile(p)
	if err != nil || !bytes.Equal(read, payload) { return "fuse-read", personalstorage.ErrUnavailable }
	if err := os.Rename(p, filepath.Join(dir, "renamed.txt")); err != nil { return "fuse-rename", err }
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 2 { return "fuse-readdir", personalstorage.ErrUnavailable }
	// Confirm the mount and service access the identical controlled SFTP root.
	other, err := service.OpenStore(ctx, owner)
	if err != nil { return "fuse-crosscheck-open", err }
	defer other.Close()
	remote, err := other.Open(ctx, marker+"/renamed.txt", os.O_RDONLY)
	if err != nil { return "fuse-crosscheck-file", err }
	buf := make([]byte, len(payload))
	n, err = remote.ReadAt(buf, 0)
	closeErr = remote.Close()
	if err != nil || closeErr != nil || n != len(payload) || !bytes.Equal(buf, payload) { return "fuse-crosscheck-content", personalstorage.ErrUnavailable }
	if !keep {
		if err := os.Remove(filepath.Join(dir, "renamed.txt")); err != nil { return "fuse-remove", err }
		if _, err := os.Stat(filepath.Join(dir, "renamed.txt")); !os.IsNotExist(err) { return "fuse-remove-stat", personalstorage.ErrUnavailable }
	} else {
		p := filepath.Join(dir, "sacrificial.txt")
		f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil { return "fuse-sacrificial-create", err }
		if err := f.Close(); err != nil { return "fuse-sacrificial-close", err }
		if err := os.Remove(p); err != nil { return "fuse-remove", err }
		if _, err := os.Stat(p); !os.IsNotExist(err) { return "fuse-remove-stat", personalstorage.ErrUnavailable }
	}
	return "", nil
}
