package personalstorage

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
)

func TestPersonalStoragePathPolicy(t *testing.T) {
	for _, p := range []string{"/etc/passwd", "../other", "a/../other", "a//b", "a/./b", `a\b`, ".shared", ".shared/a", ".spiking-group-shared/a", "a\x00b"} {
		if _, err := ValidatePath(p); !errors.Is(err, ErrInvalid) {
			t.Errorf("accepted unsafe path %q", p)
		}
	}
	for _, p := range []string{"", "data", "data/model.bin", ".hidden/model"} {
		if got, err := ValidatePath(p); err != nil || got != p {
			t.Errorf("valid path %q: %q %v", p, got, err)
		}
	}
}

type memoryRepo struct {
	mu   sync.Mutex
	rows map[string]Binding
}

func (r *memoryRepo) Get(_ context.Context, owner string) (Binding, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	b, ok := r.rows[owner]
	if !ok {
		return Binding{}, ErrNotFound
	}
	return b, nil
}
func (r *memoryRepo) Create(_ context.Context, b Binding) (Binding, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if old, ok := r.rows[b.OwnerID]; ok {
		return old, nil
	}
	r.rows[b.OwnerID] = b
	return b, nil
}
func (r *memoryRepo) MarkVerified(_ context.Context, owner, id string, g int64) (Binding, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	b, ok := r.rows[owner]
	if !ok || b.ID != id || b.Generation != g {
		return Binding{}, ErrConflict
	}
	b.State = StateReady
	r.rows[owner] = b
	return b, nil
}

type memoryVault struct {
	mu   sync.Mutex
	rows map[string]Credential
}

func (v *memoryVault) Ensure(_ context.Context, b Binding, key []byte) (Credential, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if c, ok := v.rows[b.OwnerID]; ok {
		if c.BindingID != b.ID || c.Username != b.Username || c.Generation != b.Generation {
			return Credential{}, ErrConflict
		}
		return c, nil
	}
	c := Credential{OwnerID: b.OwnerID, BindingID: b.ID, Generation: b.Generation, Username: b.Username, PrivateKey: append([]byte(nil), key...)}
	v.rows[b.OwnerID] = c
	return c, nil
}
func (v *memoryVault) Load(_ context.Context, b Binding) (Credential, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	c, ok := v.rows[b.OwnerID]
	if !ok {
		return Credential{}, ErrNotFound
	}
	if c.BindingID != b.ID || c.Username != b.Username || c.Generation != b.Generation {
		return Credential{}, ErrConflict
	}
	return c, nil
}

type denyingFactory struct{}

func (denyingFactory) Open(context.Context, Credential) (Store, error) {
	return nil, errors.New("secret password remote host failure")
}
func newTestService() *Service {
	return NewService(&memoryRepo{rows: map[string]Binding{}}, &memoryVault{rows: map[string]Credential{}}, denyingFactory{})
}
func TestPersonalStorageIndependentKeysAndStableOwner(t *testing.T) {
	s := newTestService()
	ctx := context.Background()
	a := Owner{ID: "user-a", Username: "guofeng.su"}
	b := Owner{ID: "user-b", Username: "another.user"}
	av, err := s.Enroll(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	bv, err := s.Enroll(ctx, b)
	if err != nil {
		t.Fatal(err)
	}
	if av.PublicKey == bv.PublicKey || av.BindingID == bv.BindingID || av.PublicKey == "" {
		t.Fatal("owners must have independent keys and bindings")
	}
	again, err := s.Enroll(ctx, a)
	if err != nil || again.PublicKey != av.PublicKey || again.BindingID != av.BindingID {
		t.Fatal("stable personal owner enrollment changed")
	}
	if _, err = s.View(ctx, Owner{ID: a.ID, Username: b.Username}); !errors.Is(err, ErrForbidden) {
		t.Fatal("renamed or foreign username accepted")
	}
	if _, err = s.OpenStore(ctx, a); !errors.Is(err, ErrNotReady) {
		t.Fatal("unverified binding opened")
	}
	if _, err = s.Verify(ctx, a); !errors.Is(err, ErrUnavailable) || err.Error() != "personal storage unavailable" {
		t.Fatal("verify leaked remote error")
	}
}
func TestPersonalStorageConcurrentEnrollment(t *testing.T) {
	s := newTestService()
	owner := Owner{ID: "owner", Username: "guofeng.su"}
	var wg sync.WaitGroup
	results := make(chan View, 16)
	fail := make(chan error, 16)
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, e := s.Enroll(context.Background(), owner)
			if e != nil {
				fail <- e
			} else {
				results <- v
			}
		}()
	}
	wg.Wait()
	close(results)
	close(fail)
	for e := range fail {
		t.Error(e)
	}
	var key string
	for v := range results {
		if key != "" && v.PublicKey != key {
			t.Fatal("HA enrollment returned multiple keys")
		}
		key = v.PublicKey
	}
}
func TestPersonalStorageRejectSpecialEntries(t *testing.T) {
	for _, m := range []os.FileMode{os.ModeSymlink, os.ModeNamedPipe, os.ModeSocket, os.ModeDevice} {
		if err := ValidateEntry(Entry{Name: "x", Mode: m}); !errors.Is(err, ErrForbidden) {
			t.Fatalf("accepted mode %v", m)
		}
	}
}

type recoveringRepo struct {
	*memoryRepo
	fail bool
}

func (r *recoveringRepo) Create(ctx context.Context, b Binding) (Binding, error) {
	if r.fail {
		r.fail = false
		return Binding{}, ErrUnavailable
	}
	return r.memoryRepo.Create(ctx, b)
}
func TestPersonalStorageEnrollmentRecoversSecretBeforeDatabase(t *testing.T) {
	repo := &recoveringRepo{memoryRepo: &memoryRepo{rows: map[string]Binding{}}, fail: true}
	vault := &memoryVault{rows: map[string]Credential{}}
	service := NewService(repo, vault, denyingFactory{})
	owner := Owner{ID: "durable-owner", Username: "guofeng.su"}
	if _, err := service.Enroll(context.Background(), owner); !errors.Is(err, ErrUnavailable) {
		t.Fatal("fixture must fail metadata write")
	}
	original := append([]byte(nil), vault.rows[owner.ID].PrivateKey...)
	enrolled, err := service.Enroll(context.Background(), owner)
	if err != nil {
		t.Fatal(err)
	}
	pub, _, err := publicKey(original)
	if err != nil || enrolled.PublicKey != pub {
		t.Fatal("retry replaced durable Secret winner")
	}
}

type readyStore struct{ uploadStore }

func (*readyStore) Stat(_ context.Context, name string) (Entry, error) {
	if name != "" {
		return Entry{}, ErrNotFound
	}
	return Entry{Name: "/", Mode: os.ModeDir | 0700}, nil
}

type readyFactory struct{ seen Credential }

func (f *readyFactory) Open(_ context.Context, c Credential) (Store, error) {
	f.seen = c
	return &readyStore{}, nil
}
func TestPersonalStorageVerifiedAccessUsesImmutablePersonalIdentity(t *testing.T) {
	ctx := context.Background()
	repo := &memoryRepo{rows: map[string]Binding{}}
	vault := &memoryVault{rows: map[string]Credential{}}
	factory := &readyFactory{}
	service := NewService(repo, vault, factory)
	owner := Owner{ID: "stable-personal-user", Username: "guofeng.su"}
	unbound, err := service.View(ctx, owner)
	if err != nil || unbound.State != StateUnbound || unbound.PublicKey != "" {
		t.Fatal("unbound state incorrect")
	}
	if _, err = service.Enroll(ctx, owner); err != nil {
		t.Fatal(err)
	}
	verified, err := service.Verify(ctx, owner)
	if err != nil || verified.State != StateReady {
		t.Fatal("verify failed")
	}
	c, err := service.Credential(ctx, owner)
	if err != nil || c.OwnerID != owner.ID || c.Username != owner.Username || len(c.PrivateKey) == 0 {
		t.Fatal("wrong credential")
	}
	ref, err := service.MountReference(ctx, owner)
	if err != nil || ref.OwnerID != owner.ID || ref.BindingID != verified.BindingID || ref.Generation != 1 || ref.SecretName == "" {
		t.Fatal("wrong mount identity")
	}
	store, err := service.OpenStore(ctx, owner)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if factory.seen.OwnerID != owner.ID {
		t.Fatal("wrong remote owner")
	}
	current, err := service.View(ctx, owner)
	if err != nil || current.PublicKey != verified.PublicKey || current.MountPath != MountPath {
		t.Fatal("verified view changed")
	}
}
func TestPersonalStorageRejectsSecretPublicKeyMismatch(t *testing.T) {
	service := newTestService()
	ctx := context.Background()
	a := Owner{ID: "owner-a", Username: "user-a"}
	b := Owner{ID: "owner-b", Username: "user-b"}
	if _, err := service.Enroll(ctx, a); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Enroll(ctx, b); err != nil {
		t.Fatal(err)
	}
	vault := service.vault.(*memoryVault)
	row := vault.rows[a.ID]
	row.PrivateKey = append([]byte(nil), vault.rows[b.ID].PrivateKey...)
	vault.rows[a.ID] = row
	if _, err := service.Enroll(ctx, a); !errors.Is(err, ErrConflict) {
		t.Fatal("Secret key mismatch accepted on enrollment")
	}
	if _, err := service.Verify(ctx, a); !errors.Is(err, ErrConflict) {
		t.Fatal("Secret key mismatch accepted on verify")
	}
}
func TestPersonalStorageInvalidAndUnreadyOwnersFailClosed(t *testing.T) {
	ctx := context.Background()
	service := newTestService()
	for _, o := range []Owner{{}, {ID: "a", Username: "../b"}, {ID: "a", Username: "user\n"}} {
		if _, err := service.Enroll(ctx, o); !errors.Is(err, ErrInvalid) {
			t.Fatal("invalid owner accepted")
		}
	}
	owner := Owner{ID: "pending", Username: "user"}
	if _, err := service.Enroll(ctx, owner); err != nil {
		t.Fatal(err)
	}
	if _, err := service.MountReference(ctx, owner); !errors.Is(err, ErrNotReady) {
		t.Fatal("unverified mount accepted")
	}
	if _, err := service.Credential(ctx, owner); !errors.Is(err, ErrNotReady) {
		t.Fatal("unverified credentials accepted")
	}
}
