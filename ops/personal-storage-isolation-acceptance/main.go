// Operator-only acceptance for the two explicitly authorized storage accounts.
// Build on the builder from backend/: go build ../ops/personal-storage-isolation-acceptance/main.go
// This tool never changes production database rows, Helm values or membership.
package main

import (
	"bytes"
	"context"
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
	"ray-train-platform-backend/personalstorage"
)

const (
	namespace       = "ray-train-platform"
	host            = "mount.wellspiking.ai"
	hostFingerprint = "SHA256:q62n6tQyLK3O/UaD25LKPP4KARLqyIslmNC0XVKvdnA"
)

var (
	ownerA = personalstorage.Owner{ID: "5977834169122c6a964370132f5c0e62", Username: "guofeng.su"}
	ownerB = personalstorage.Owner{ID: "oauth2-555eaeca5a097d648e10b956", Username: "raytrain-test"}
)

type fileRepository struct {
	directory string
	owner     personalstorage.Owner
}

func (r fileRepository) Get(ctx context.Context, id string) (personalstorage.Binding, error) {
	if ctx.Err() != nil || id != r.owner.ID {
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
	data, err := os.ReadFile(p)
	if err != nil || len(data) > 16384 {
		return personalstorage.Binding{}, personalstorage.ErrConflict
	}
	var binding personalstorage.Binding
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if d.Decode(&binding) != nil || d.Decode(new(any)) != io.EOF {
		return personalstorage.Binding{}, personalstorage.ErrConflict
	}
	if binding.OwnerID != r.owner.ID || binding.Username != r.owner.Username || binding.Generation != 1 {
		return personalstorage.Binding{}, personalstorage.ErrConflict
	}
	return binding, nil
}

func (r fileRepository) Create(ctx context.Context, b personalstorage.Binding) (personalstorage.Binding, error) {
	if current, err := r.Get(ctx, b.OwnerID); err == nil {
		return current, nil
	} else if !errors.Is(err, personalstorage.ErrNotFound) {
		return personalstorage.Binding{}, err
	}
	if b.OwnerID != r.owner.ID || b.Username != r.owner.Username || b.Generation != 1 {
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
	data, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return personalstorage.ErrUnavailable
	}
	return atomicPublicFile(r.directory, "binding.json", append(data, '\n'))
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
	if err := os.Rename(f.Name(), p); err != nil {
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
	var stat unix.Stat_t
	if err := unix.Lstat(p, &stat); err != nil || int(stat.Uid) != os.Geteuid() {
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

func emit(value any) { _ = json.NewEncoder(os.Stdout).Encode(value) }

func main() {
	var action, kubeconfig, knownHosts, stateDir string
	flag.StringVar(&action, "action", "", "enroll-test")
	flag.StringVar(&kubeconfig, "kubeconfig", "", "explicit operator kubeconfig")
	flag.StringVar(&knownHosts, "known-hosts", "", "previously trusted fixed-origin known_hosts")
	flag.StringVar(&stateDir, "state-dir", "", "absolute private raytrain-test operator state directory (0700)")
	flag.Parse()
	if flag.NArg() != 0 || action != "enroll-test" || kubeconfig == "" || knownHosts == "" || stateDir == "" {
		emit(map[string]any{"ok": false, "stage": "arguments"})
		os.Exit(2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if stage, err := enrollTest(ctx, kubeconfig, knownHosts, stateDir); err != nil {
		emit(map[string]any{"ok": false, "stage": stage})
		os.Exit(1)
	}
}

func enrollTest(ctx context.Context, kubeconfig, knownHosts, stateDir string) (string, error) {
	lock, err := secureStateDir(stateDir)
	if err != nil {
		return "state-directory", err
	}
	defer lock.Close()
	factory, err := pinnedFactory(knownHosts)
	if err != nil {
		return "host-pin", err
	}
	cfg, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		return "kubeconfig", err
	}
	cfg.Timeout = 20 * time.Second
	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return "kubernetes-client", err
	}
	service := personalstorage.NewService(fileRepository{directory: stateDir, owner: ownerB}, personalstorage.NewKubernetesVault(client, namespace), factory)
	v, err := service.Enroll(ctx, ownerB)
	if err != nil {
		return "enroll-test", err
	}
	if err := atomicPublicFile(stateDir, "public-key.pub", []byte(v.PublicKey)); err != nil {
		return "public-key-file", err
	}
	if err := atomicPublicFile(stateDir, "fingerprint.txt", []byte(v.Fingerprint+"\n")); err != nil {
		return "fingerprint-file", err
	}
	emit(map[string]any{"ok": true, "action": "enroll-test", "owner": ownerB.Username, "fingerprint": v.Fingerprint, "productionDatabaseChanged": false, "helmChanged": false})
	return "", nil
}
