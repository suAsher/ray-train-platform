package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/sys/unix"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"ray-train-platform-backend/personalfs"
	"ray-train-platform-backend/personalstorage"
)

const markerPrefix = "rtp-personal-acceptance-"

type isolationFailure struct {
	cause error
	markers map[string]string
}
func (e *isolationFailure) Error() string { return "personal isolation acceptance failed" }
func (e *isolationFailure) Unwrap() error { return e.cause }

func isolation(ctx context.Context, kubeconfig, knownHosts, stateDirA, stateDirB string, withFuse bool) (stage string, retErr error) {
	markers := map[string]string{}
	defer func() { if retErr != nil && len(markers) > 0 { retErr = &isolationFailure{cause: retErr, markers: markers} } }()
	// Registered before cleanup defers: success is emitted only after cleanup.
	defer func() {
		if retErr == nil { emit(map[string]any{"ok": true, "action": "isolation", "independentCredentials": true, "ownReadWriteVerified": true, "crossMarkerStatRejected": true, "crossMarkerReadRejected": true, "crossMarkerWriteRejected": true, "crossKeyAuthenticationRejected": true, "freshPositiveSessionsAfter": true, "rootMarkersMutuallyHidden": true, "adapterExcludedRootsHidden": true, "twoFuseVerified": withFuse, "productionDatabaseChanged": false, "helmChanged": false, "productionEnabled": false}) }
	}()
	if stateDirA == stateDirB { return "state-directories-conflict", personalstorage.ErrConflict }
	lockA, err := secureStateDir(stateDirA)
	if err != nil { return "state-directory-a", err }
	defer lockA.Close()
	lockB, err := secureStateDir(stateDirB)
	if err != nil { return "state-directory-b", err }
	defer lockB.Close()
	factory, err := pinnedFactory(knownHosts)
	if err != nil { return "host-pin", err }
	cfg, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil { return "kubeconfig", err }
	cfg.Timeout = 20*time.Second
	client, err := kubernetes.NewForConfig(cfg)
	if err != nil { return "kubernetes-client", err }
	vault := personalstorage.NewKubernetesVault(client, namespace)
	a := personalstorage.NewService(fileRepository{directory: stateDirA, owner: ownerA}, vault, factory)
	b := personalstorage.NewService(fileRepository{directory: stateDirB, owner: ownerB}, vault, factory)
	viewA, err := a.Verify(ctx, ownerA)
	if err != nil { return "verify-a-before", err }
	viewB, err := b.Verify(ctx, ownerB)
	if err != nil { return "verify-b-before", err }
	if viewA.BindingID == viewB.BindingID || viewA.Fingerprint == viewB.Fingerprint || viewA.PublicKey == viewB.PublicKey { return "independent-credentials", personalstorage.ErrConflict }
	storeA, err := a.OpenStore(ctx, ownerA)
	if err != nil { return "open-a", err }
	defer storeA.Close()
	storeB, err := b.OpenStore(ctx, ownerB)
	if err != nil { return "open-b", err }
	defer storeB.Close()
	markerA, err := markerID()
	if err != nil { return "marker-a", err }
	markers[ownerA.Username] = markerA
	defer func() { if err := cleanupMarker(a, ownerA, markerA); err != nil { stage, retErr = "cleanup-a", err } }()
	if err := storeA.Mkdir(ctx, markerA); err != nil { return "marker-mkdir-a", err }
	markerB, err := markerID()
	if err != nil { return "marker-b", err }
	markers[ownerB.Username] = markerB
	defer func() { if err := cleanupMarker(b, ownerB, markerB); err != nil { stage, retErr = "cleanup-b", err } }()
	if err := storeB.Mkdir(ctx, markerB); err != nil { return "marker-mkdir-b", err }
	payloadA, payloadB := []byte("authorized owner A: "+markerA+"\n"), []byte("authorized owner B: "+markerB+"\n")
	if err := ownReadWrite(ctx, storeA, markerA, payloadA); err != nil { return "own-read-write-a", err }
	if err := ownReadWrite(ctx, storeB, markerB, payloadB); err != nil { return "own-read-write-b", err }
	if err := foreignRejected(ctx, storeA, markerB); err != nil { return "foreign-access-a-to-b", err }
	if err := foreignRejected(ctx, storeB, markerA); err != nil { return "foreign-access-b-to-a", err }
	if err := rootVisibility(ctx, storeA, markerA, markerB); err != nil { return "root-visibility-a", err }
	if err := rootVisibility(ctx, storeB, markerB, markerA); err != nil { return "root-visibility-b", err }
	credentialA, err := a.Credential(ctx, ownerA)
	if err != nil { return "credential-a", err }
	credentialB, err := b.Credential(ctx, ownerB)
	if err != nil { return "credential-b", err }
	if err := crossKeyRejected(ctx, credentialA, ownerB); err != nil { return "cross-key-a-to-b", err }
	if err := crossKeyRejected(ctx, credentialB, ownerA); err != nil { return "cross-key-b-to-a", err }
	// Fresh successful sessions after both negative probes distinguish rejection
	// from a transport outage, unusable pin, server failure or account lockout.
	if err := freshOwnRead(ctx, a, ownerA, markerA, payloadA); err != nil { return "verify-a-after", err }
	if err := freshOwnRead(ctx, b, ownerB, markerB, payloadB); err != nil { return "verify-b-after", err }
	if withFuse {
		if err := dualFuseCheck(ctx, a, b, markerA, markerB, payloadA, payloadB); err != nil { return "dual-fuse", err }
	}
	return "", nil
}

func markerID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil { return "", err }
	raw[6], raw[8] = raw[6]&0x0f|0x40, raw[8]&0x3f|0x80
	h := hex.EncodeToString(raw[:])
	return markerPrefix+h[:8]+"-"+h[8:12]+"-"+h[12:16]+"-"+h[16:20]+"-"+h[20:], nil
}

func ownReadWrite(ctx context.Context, store personalstorage.Store, marker string, payload []byte) error {
	f, err := store.Open(ctx, marker+"/probe.txt", os.O_RDWR|os.O_CREATE|os.O_EXCL)
	if err != nil { return err }
	n, err := f.WriteAt(payload, 0)
	if err != nil || n != len(payload) { f.Close(); return personalstorage.ErrUnavailable }
	buf := make([]byte, len(payload))
	n, err = f.ReadAt(buf, 0)
	closeErr := f.Close()
	if err != nil || closeErr != nil || n != len(payload) || !bytes.Equal(buf, payload) { return personalstorage.ErrUnavailable }
	return nil
}

func denial(err error) bool { return errors.Is(err, personalstorage.ErrNotFound) || errors.Is(err, personalstorage.ErrForbidden) }

func foreignRejected(ctx context.Context, store personalstorage.Store, marker string) error {
	if _, err := store.Stat(ctx, marker); !denial(err) { return personalstorage.ErrForbidden }
	for _, flag := range []int{os.O_RDONLY, os.O_WRONLY} {
		f, err := store.Open(ctx, marker+"/probe.txt", flag)
		if f != nil { _ = f.Close() }
		// No CREATE/TRUNC and no read/write on unexpectedly opened foreign files.
		if !denial(err) { return personalstorage.ErrForbidden }
	}
	return nil
}

func excludedRoot(name string) bool {
	switch name {
	case ".minio", ".original", ".wellspiking", ".spk-hybrid", ".spk-ssd", ".shared", ".spiking-group-shared": return true
	default: return false
	}
}

func rootVisibility(ctx context.Context, store personalstorage.Store, own, foreign string) error {
	entries, err := store.List(ctx, "")
	if err != nil { return err }
	visible := false
	for _, e := range entries {
		// Private names exist only in memory and are never logged or persisted.
		if e.Name == foreign || excludedRoot(e.Name) { return personalstorage.ErrForbidden }
		if e.Name == own && e.IsDir() { visible = true }
	}
	if !visible { return personalstorage.ErrUnavailable }
	return nil
}

func crossKeyRejected(ctx context.Context, credential personalstorage.Credential, target personalstorage.Owner) error {
	if (credential.OwnerID != ownerA.ID || target != ownerB) && (credential.OwnerID != ownerB.ID || target != ownerA) { return personalstorage.ErrForbidden }
	signer, err := ssh.ParsePrivateKey(credential.PrivateKey)
	if err != nil || signer.PublicKey().Type() != ssh.KeyAlgoED25519 { return personalstorage.ErrConflict }
	address := net.JoinHostPort(host, "22")
	conn, err := (&net.Dialer{Timeout: 15*time.Second}).DialContext(ctx, "tcp", address)
	if err != nil { return personalstorage.ErrUnavailable }
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	if err := conn.SetDeadline(time.Now().Add(20*time.Second)); err != nil { return personalstorage.ErrUnavailable }
	pinMatched := false
	cfg := &ssh.ClientConfig{User: target.Username, Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)}, HostKeyAlgorithms: []string{ssh.KeyAlgoED25519}, Timeout: 15*time.Second, HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
		pinMatched = key.Type() == ssh.KeyAlgoED25519 && ssh.FingerprintSHA256(key) == hostFingerprint
		if !pinMatched { return personalstorage.ErrForbidden }
		return nil
	}}
	client, _, _, err := ssh.NewClientConn(conn, address, cfg)
	if err == nil { _ = client.Close(); return personalstorage.ErrForbidden }
	if !pinMatched { return personalstorage.ErrForbidden }
	// A generic unavailable/EOF/timeout/subsystem failure is never isolation
	// evidence. Accept only the SSH client's explicit authentication rejection.
	if !strings.Contains(err.Error(), "ssh: unable to authenticate,") { return personalstorage.ErrUnavailable }
	return nil
}

func freshOwnRead(ctx context.Context, service *personalstorage.Service, owner personalstorage.Owner, marker string, payload []byte) error {
	store, err := service.OpenStore(ctx, owner)
	if err != nil { return err }
	defer store.Close()
	f, err := store.Open(ctx, marker+"/probe.txt", os.O_RDONLY)
	if err != nil { return err }
	buf := make([]byte, len(payload))
	n, err := f.ReadAt(buf, 0)
	closeErr := f.Close()
	if err != nil || closeErr != nil || n != len(payload) || !bytes.Equal(buf, payload) { return personalstorage.ErrUnavailable }
	return nil
}

func cleanupMarker(service *personalstorage.Service, owner personalstorage.Owner, marker string) error {
	if !strings.HasPrefix(marker, markerPrefix) || len(marker) != len(markerPrefix)+36 || strings.ContainsAny(marker, "/\\") { return personalstorage.ErrForbidden }
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	store, err := service.OpenStore(ctx, owner)
	if err != nil { return err }
	defer store.Close()
	var result error
	for _, suffix := range []string{"/probe.txt", "/fuse-probe.txt", ""} {
		if err := store.Remove(ctx, marker+suffix); err != nil && !errors.Is(err, personalstorage.ErrNotFound) { result = err }
	}
	return result
}

func dualFuseCheck(ctx context.Context, a, b *personalstorage.Service, markerA, markerB string, payloadA, payloadB []byte) (retErr error) {
	var filesystem unix.Statfs_t
	if err := unix.Statfs("/run", &filesystem); err != nil || filesystem.Type != unix.TMPFS_MAGIC { return personalstorage.ErrForbidden }
	outer, err := os.MkdirTemp("/run", markerPrefix+"isolation-mount-")
	if err != nil { return personalstorage.ErrUnavailable }
	targetA, targetB := filepath.Join(outer, "a"), filepath.Join(outer, "b")
	defer func() {
		for _, p := range []string{targetB, targetA, outer} {
			if err := os.Remove(p); err != nil && !os.IsNotExist(err) { retErr = personalstorage.ErrUnavailable }
		}
	}()
	if err := os.Mkdir(targetA, 0700); err != nil { return personalstorage.ErrUnavailable }
	if err := os.Mkdir(targetB, 0700); err != nil { return personalstorage.ErrUnavailable }
	storeA, err := a.OpenStore(ctx, ownerA)
	if err != nil { return err }
	mountA, err := personalfs.NewMount(ctx, targetA, "operator-isolation-authorized-a", storeA)
	if err != nil { storeA.Close(); return err }
	defer func() { if err := mountA.Close(); err != nil { retErr = personalstorage.ErrUnavailable } }()
	storeB, err := b.OpenStore(ctx, ownerB)
	if err != nil { return err }
	mountB, err := personalfs.NewMount(ctx, targetB, "operator-isolation-authorized-b", storeB)
	if err != nil { storeB.Close(); return err }
	defer func() { if err := mountB.Close(); err != nil { retErr = personalstorage.ErrUnavailable } }()
	if err := fuseRootVisibility(targetA, markerA, markerB); err != nil { return err }
	if err := fuseRootVisibility(targetB, markerB, markerA); err != nil { return err }
	if err := fuseOwnReadWrite(targetA, markerA, payloadA); err != nil { return err }
	if err := fuseOwnReadWrite(targetB, markerB, payloadB); err != nil { return err }
	if err := fuseForeignRejected(targetA, markerB); err != nil { return err }
	if err := fuseForeignRejected(targetB, markerA); err != nil { return err }
	return nil
}

func fuseRootVisibility(target, own, foreign string) error {
	entries, err := os.ReadDir(target)
	if err != nil { return personalstorage.ErrUnavailable }
	visible := false
	for _, e := range entries {
		if e.Name() == foreign || excludedRoot(e.Name()) { return personalstorage.ErrForbidden }
		if e.Name() == own && e.IsDir() { visible = true }
	}
	if !visible { return personalstorage.ErrUnavailable }
	return nil
}

func fuseOwnReadWrite(target, marker string, payload []byte) error {
	// This file is the same UUID-scoped probe written via the owner's SFTP store.
	data, err := os.ReadFile(filepath.Join(target, marker, "probe.txt"))
	if err != nil || !bytes.Equal(data, payload) { return personalstorage.ErrUnavailable }
	p := filepath.Join(target, marker, "fuse-probe.txt")
	f, err := os.OpenFile(p, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil { return personalstorage.ErrUnavailable }
	n, err := f.Write(payload)
	closeErr := f.Close()
	if err != nil || closeErr != nil || n != len(payload) { return personalstorage.ErrUnavailable }
	data, err = os.ReadFile(p)
	if err != nil || !bytes.Equal(data, payload) { return personalstorage.ErrUnavailable }
	return nil
}

func fuseForeignRejected(target, foreign string) error {
	if _, err := os.Stat(filepath.Join(target, foreign)); !os.IsNotExist(err) && !os.IsPermission(err) { return personalstorage.ErrForbidden }
	for _, flag := range []int{os.O_RDONLY, os.O_WRONLY} {
		f, err := os.OpenFile(filepath.Join(target, foreign, "probe.txt"), flag, 0)
		if f != nil { _ = f.Close() }
		if !os.IsNotExist(err) && !os.IsPermission(err) { return personalstorage.ErrForbidden }
	}
	return nil
}
