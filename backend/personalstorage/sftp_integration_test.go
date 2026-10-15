package personalstorage_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/dsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
	"io"
	"net"
	"os"
	ps "ray-train-platform-backend/personalstorage"
	"ray-train-platform-backend/personalstorage/testfixture"
	"strconv"
	"strings"
	"testing"
)

func TestPersonalStorageRealSFTPTwoUserIsolation(t *testing.T) {
	fixture, err := testfixture.Start()
	if err != nil {
		t.Fatal(err)
	}
	defer fixture.Close()
	ctx := context.Background()
	factory, err := ps.NewSFTPFactory(fixture.Endpoint)
	if err != nil {
		t.Fatal(err)
	}
	a, err := factory.Open(ctx, fixture.Credentials[0])
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := factory.Open(ctx, fixture.Credentials[1])
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if err := a.Mkdir(ctx, "models"); err != nil {
		t.Fatal(err)
	}
	file, err := a.Open(ctx, "models/only-a", os.O_CREATE|os.O_EXCL|os.O_RDWR)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = file.WriteAt([]byte("owner-a"), 0); err != nil {
		t.Fatal(err)
	}
	_ = file.Close()
	if _, err = b.Stat(ctx, "models/only-a"); !errors.Is(err, ps.ErrNotFound) {
		t.Fatalf("foreign owner file visible: %v", err)
	}
	wrong := fixture.Credentials[0]
	wrong.PrivateKey = fixture.Credentials[1].PrivateKey
	if _, err = factory.Open(ctx, wrong); !errors.Is(err, ps.ErrUnavailable) {
		t.Fatal("cross-user credential accepted")
	}
	raw, err := fixture.RawClient(0)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if err = raw.Mkdir("/.shared"); err != nil {
		t.Fatal(err)
	}
	if err = raw.Symlink("/models/only-a", "/escape"); err != nil {
		t.Fatal(err)
	}
	entries, err := a.List(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name == ".shared" || entry.Name == "escape" {
			t.Fatal("excluded entry listed")
		}
	}
	if _, err = a.Open(ctx, "escape", os.O_RDONLY); !errors.Is(err, ps.ErrForbidden) {
		t.Fatalf("symlink accepted: %v", err)
	}
	if _, err = a.Stat(ctx, "../user-b"); !errors.Is(err, ps.ErrInvalid) {
		t.Fatal("traversal accepted")
	}
	if err := ps.Upload(ctx, a, "complete", strings.NewReader("payload"), 7, 32); err != nil {
		t.Fatal(err)
	}
	_ = a.Close()
	a, err = factory.Open(ctx, fixture.Credentials[0])
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if _, err = a.Stat(ctx, "models/only-a"); err != nil {
		t.Fatal("reconnect lost owner data")
	}
}
func TestPersonalStorageHostKeyPinRequired(t *testing.T) {
	fixture, err := testfixture.Start()
	if err != nil {
		t.Fatal(err)
	}
	defer fixture.Close()
	other, err := testfixture.Start()
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if _, err := ps.NewSFTPFactory(ps.Endpoint{Host: "localhost", Port: 22}); err == nil {
		t.Fatal("missing pin accepted")
	}
	endpoint := fixture.Endpoint
	endpoint.KnownHosts = other.Endpoint.KnownHosts
	if _, err := ps.NewSFTPFactory(endpoint); err == nil {
		t.Fatal("foreign endpoint pin accepted")
	}
	_, _, wrongKey, _, _, err := ssh.ParseKnownHosts(other.Endpoint.KnownHosts)
	if err != nil {
		t.Fatal(err)
	}
	endpoint.KnownHosts = []byte(knownhosts.Line([]string{net.JoinHostPort(endpoint.Host, strconv.Itoa(endpoint.Port))}, wrongKey) + "\n")
	wrongFactory, err := ps.NewSFTPFactory(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	if store, err := wrongFactory.Open(context.Background(), fixture.Credentials[0]); err == nil {
		store.Close()
		t.Fatal("changed upstream host key accepted")
	}
}
func TestPersonalStorageSFTPCancellationAndDirectoryRemoval(t *testing.T) {
	fixture, err := testfixture.Start()
	if err != nil {
		t.Fatal(err)
	}
	defer fixture.Close()
	factory, err := ps.NewSFTPFactory(fixture.Endpoint)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	store, err := factory.Open(ctx, fixture.Credentials[0])
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err = store.Mkdir(ctx, "empty"); err != nil {
		t.Fatal(err)
	}
	if err = store.Remove(ctx, "empty"); err != nil {
		t.Fatal(err)
	}
	cancel()
	if _, err = store.Stat(ctx, ""); err == nil {
		t.Fatal("canceled session still readable")
	}
}

type cancelUploadReader struct {
	cancel context.CancelFunc
	sent   bool
}

func (r *cancelUploadReader) Read(p []byte) (int, error) {
	if !r.sent {
		r.sent = true
		return copy(p, []byte("ab")), nil
	}
	r.cancel()
	return 0, io.ErrUnexpectedEOF
}
func TestPersonalStorageCanceledUploadCleansThroughFreshOwnerSession(t *testing.T) {
	fixture, err := testfixture.Start()
	if err != nil {
		t.Fatal(err)
	}
	defer fixture.Close()
	factory, err := ps.NewSFTPFactory(fixture.Endpoint)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store, err := factory.Open(ctx, fixture.Credentials[0])
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err = ps.Upload(ctx, store, "incomplete", &cancelUploadReader{cancel: cancel}, 4, 16); err == nil {
		t.Fatal("canceled upload succeeded")
	}
	check, err := factory.Open(context.Background(), fixture.Credentials[0])
	if err != nil {
		t.Fatal(err)
	}
	defer check.Close()
	entries, err := check.List(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name, ".raytrain-upload-") || entry.Name == "incomplete" {
			t.Fatal("canceled upload leaked staging file")
		}
	}
}

func TestPersonalStorageFactoryRejectsNonEd25519Credential(t *testing.T) {
	fixture, err := testfixture.Start()
	if err != nil {
		t.Fatal(err)
	}
	defer fixture.Close()
	factory, err := ps.NewSFTPFactory(fixture.Endpoint)
	if err != nil {
		t.Fatal(err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(key, "")
	if err != nil {
		t.Fatal(err)
	}
	credential := fixture.Credentials[0]
	credential.PrivateKey = pem.EncodeToMemory(block)
	if _, err = factory.Open(context.Background(), credential); !errors.Is(err, ps.ErrConflict) {
		t.Fatal("non-Ed25519 personal credential accepted")
	}
}

func TestPersonalStorageRealSFTPFiltersUpstreamVirtualRoots(t *testing.T) {
	fixture, err := testfixture.Start()
	if err != nil {
		t.Fatal(err)
	}
	defer fixture.Close()
	raw, err := fixture.RawClient(0)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	roots := []string{".minio", ".original", ".wellspiking", ".spk-hybrid", ".spk-ssd", ".shared", ".spiking-group-shared"}
	for _, root := range roots {
		if err := raw.Mkdir("/" + root); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{".config", ".minio-backup", "private"} {
		if err := raw.Mkdir("/" + name); err != nil {
			t.Fatal(err)
		}
	}
	if err := raw.Mkdir("/private/.minio"); err != nil {
		t.Fatal(err)
	}
	file, err := raw.Create("/.private.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte("personal hidden file")); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	factory, err := ps.NewSFTPFactory(fixture.Endpoint)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	store, err := factory.Open(ctx, fixture.Credentials[0])
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	entries, err := store.List(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	visible := make(map[string]bool)
	for _, entry := range entries {
		visible[entry.Name] = true
	}
	for _, root := range roots {
		if visible[root] {
			t.Errorf("upstream virtual or internal root listed: %s", root)
		}
		if _, err := store.Stat(ctx, root); !errors.Is(err, ps.ErrInvalid) {
			t.Errorf("upstream virtual or internal root stat accepted: %s", root)
		}
		if _, err := store.Open(ctx, root+"/created", os.O_CREATE|os.O_WRONLY); !errors.Is(err, ps.ErrInvalid) {
			t.Errorf("upstream virtual or internal root write accepted: %s", root)
		}
	}
	for _, name := range []string{".config", ".minio-backup", ".private.txt", "private"} {
		if !visible[name] {
			t.Errorf("private hidden entry omitted: %s", name)
		}
	}
	nested, err := store.List(ctx, "private")
	if err != nil || len(nested) != 1 || nested[0].Name != ".minio" {
		t.Fatalf("private nested entry rejected: count=%d error=%v", len(nested), err)
	}
}

func TestPersonalStorageHostKeyNegotiationUsesOnlyTrustedPinAlgorithms(t *testing.T) {
	fixture, err := testfixture.StartWithECDSAHostKey()
	if err != nil {
		t.Fatal(err)
	}
	defer fixture.Close()
	_, _, pinned, _, _, err := ssh.ParseKnownHosts(fixture.Endpoint.KnownHosts)
	if err != nil || pinned.Type() != ssh.KeyAlgoED25519 {
		t.Fatal("fixture must pin only Ed25519 while also offering ECDSA")
	}
	t.Run("wrong Ed25519 pin remains rejected", func(t *testing.T) {
		other, err := testfixture.Start()
		if err != nil {
			t.Fatal(err)
		}
		defer other.Close()
		_, _, wrongKey, _, _, err := ssh.ParseKnownHosts(other.Endpoint.KnownHosts)
		if err != nil {
			t.Fatal(err)
		}
		endpoint := fixture.Endpoint
		endpoint.KnownHosts = []byte(knownhosts.Line([]string{net.JoinHostPort(endpoint.Host, strconv.Itoa(endpoint.Port))}, wrongKey) + "\n")
		factory, err := ps.NewSFTPFactory(endpoint)
		if err != nil {
			t.Fatal(err)
		}
		if store, err := factory.Open(context.Background(), fixture.Credentials[0]); err == nil {
			store.Close()
			t.Fatal("incorrect pinned Ed25519 key accepted")
		}
	})
	t.Run("trusted Ed25519 negotiates successfully", func(t *testing.T) {
		factory, err := ps.NewSFTPFactory(fixture.Endpoint)
		if err != nil {
			t.Fatal(err)
		}
		store, err := factory.Open(context.Background(), fixture.Credentials[0])
		if err != nil {
			t.Fatalf("server offering ECDSA and Ed25519 rejected trusted Ed25519 pin: %v", err)
		}
		defer store.Close()
		if _, err := store.Stat(context.Background(), ""); err != nil {
			t.Fatal(err)
		}
	})
}

func TestPersonalStorageRSAHostPinNegotiatesSHA2(t *testing.T) {
	fixture, err := testfixture.StartWithRSAHostKey()
	if err != nil {
		t.Fatal(err)
	}
	defer fixture.Close()
	factory, err := ps.NewSFTPFactory(fixture.Endpoint)
	if err != nil {
		t.Fatal(err)
	}
	store, err := factory.Open(context.Background(), fixture.Credentials[0])
	if err != nil {
		t.Fatalf("pinned RSA host supporting only SHA2 rejected: %v", err)
	}
	defer store.Close()
}

func TestPersonalStorageRejectsUnsupportedHostPinAlgorithm(t *testing.T) {
	var parameters dsa.Parameters
	if err := dsa.GenerateParameters(&parameters, rand.Reader, dsa.L1024N160); err != nil {
		t.Fatal(err)
	}
	key := &dsa.PrivateKey{PublicKey: dsa.PublicKey{Parameters: parameters}}
	if err := dsa.GenerateKey(key, rand.Reader); err != nil {
		t.Fatal(err)
	}
	public, err := ssh.NewPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	endpoint := ps.Endpoint{Host: "localhost", Port: 22, KnownHosts: []byte(knownhosts.Line([]string{"localhost"}, public) + "\n")}
	if _, err := ps.NewSFTPFactory(endpoint); !errors.Is(err, ps.ErrInvalid) {
		t.Fatalf("unsupported host pin algorithm accepted: %v", err)
	}
}
