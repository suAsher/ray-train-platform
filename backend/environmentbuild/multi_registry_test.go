package environmentbuild

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"ray-train-platform-backend/registryauth"
)

const qomoloTestHost = "harbor.qomolo.com"

type multiRegistryRecorder struct {
	Registry
	hosts []string
}

func (r *multiRegistryRecorder) Authenticate(_ context.Context, host string, _ Credentials) error {
	r.hosts = append(r.hosts, host)
	return nil
}
func (r *multiRegistryRecorder) Projects(_ context.Context, host string, _ Credentials, _ int) ([]Project, error) {
	r.hosts = append(r.hosts, host)
	return []Project{{Name: "project", CanPush: true}}, nil
}
func (r *multiRegistryRecorder) CheckPush(_ context.Context, host string, _ Credentials, _ string) error {
	r.hosts = append(r.hosts, host)
	return nil
}

type multiRegistryStore struct{ *lifecycleStore }

func (s *multiRegistryStore) ReserveEnvironmentCredentialMaterial(context.Context, CredentialMaterial) error { return nil }
func (s *multiRegistryStore) SaveEnvironmentAuthorization(_ context.Context, a Authorization) error {
	s.authorization = a
	return nil
}
func (s *multiRegistryStore) EnvironmentWorkspace(_ context.Context, o Owner, id string) (Workspace, error) {
	return Workspace{ID: id, TenantID: o.TenantID, OwnerID: o.UserID, Namespace: "tenant", ResourceName: "workspace", State: "RUNNING"}, nil
}
func (s *multiRegistryStore) CreateEnvironmentBuild(_ context.Context, b Build) (Build, error) {
	s.build = b
	return b, nil
}

type multiRegistryRunner struct {
	*lifecycleRunner
	image string
}

func (r *multiRegistryRunner) InspectWorkspace(context.Context, Workspace) (WorkspaceSnapshot, error) {
	return WorkspaceSnapshot{UID: "workspace-uid", Image: r.image}, nil
}

type multiRegistryVault struct{ values map[string][]byte }

func (v *multiRegistryVault) Put(_ context.Context, ref string, sealed []byte, _ time.Time) error {
	v.values[ref] = append([]byte(nil), sealed...)
	return nil
}
func (v *multiRegistryVault) Get(_ context.Context, ref string) ([]byte, error) {
	sealed, ok := v.values[ref]
	if !ok { return nil, ErrNotFound }
	return sealed, nil
}
func (v *multiRegistryVault) Delete(_ context.Context, ref string) error {
	delete(v.values, ref)
	return nil
}

func multiRegistryFixture(t *testing.T) (*Service, *multiRegistryStore, *multiRegistryRecorder, *multiRegistryRunner) {
	t.Helper()
	base, original, runner, _, _ := lifecycleFixture(t)
	store := &multiRegistryStore{lifecycleStore: original}
	registry := &multiRegistryRecorder{}
	multiRunner := &multiRegistryRunner{lifecycleRunner: runner, image: base.config.WorkspaceImage}
	config := base.config
	config.RegistryHosts = []string{RegistryHost, qomoloTestHost}
	s, err := NewService(store, multiRunner, registry, &multiRegistryVault{values: map[string][]byte{}}, config)
	if err != nil { t.Fatal(err) }
	s.now = base.now
	return s, store, registry, multiRunner
}

func TestRegistryCapabilitiesAreExplicitAndDefaultCompatible(t *testing.T) {
	s, _, _, _ := multiRegistryFixture(t)
	raw, err := json.Marshal(s.Capabilities())
	if err != nil { t.Fatal(err) }
	var result struct {
		RegistryHost string `json:"registryHost"`
		Registries []struct {
			Host string `json:"host"`
			Label string `json:"label"`
			CredentialType string `json:"credentialType"`
			CredentialLabel string `json:"credentialLabel"`
			CredentialHint string `json:"credentialHint"`
		} `json:"registries"`
	}
	if json.Unmarshal(raw, &result) != nil || result.RegistryHost != RegistryHost || len(result.Registries) != 2 {
		t.Fatalf("unexpected capabilities: %s", raw)
	}
	for i, want := range []string{"cli_secret", "password"} {
		item := result.Registries[i]
		if item.CredentialType != want || item.Label == "" || item.CredentialLabel == "" || item.CredentialHint == "" { t.Fatalf("incomplete registry metadata: %+v", item) }
	}
	legacy, _, _, _, _ := lifecycleFixture(t)
	if len(legacy.config.RegistryHosts) != 1 || legacy.config.RegistryHosts[0] != RegistryHost { t.Fatal("legacy service enabled extra registry") }
	if (Build{}).Host() != RegistryHost || (Authorization{}).Host() != RegistryHost || (Build{Project: "p", Repository: "r", Tag: "t"}).Target() != RegistryHost+"/p/r:t" { t.Fatal("legacy host default changed") }
	if (Build{RegistryHost: qomoloTestHost, Project: "p", Repository: "r", Tag: "t"}).Target() != qomoloTestHost+"/p/r:t" { t.Fatal("selected registry missing from target") }
	for _, hosts := range [][]string{{"evil.invalid"}, {"https://"+qomoloTestHost}, {qomoloTestHost+":443"}, {RegistryHost, RegistryHost}, {""}} {
		config := s.config
		config.RegistryHosts = hosts
		if _, err := NewService(s.store, s.runner, s.registry, s.vault, config); !errors.Is(err, ErrInvalid) { t.Fatalf("invalid registry configuration accepted: %v", hosts) }
		if _, err := NewService(nil, nil, nil, nil, Config{RegistryHosts: hosts}); !errors.Is(err, ErrInvalid) { t.Fatalf("disabled service accepted invalid registry configuration: %v", hosts) }
	}
}

func TestAuthorizationSelectedHostRoutingAndInputRejection(t *testing.T) {
	ctx := context.Background()
	owner := Owner{TenantID: "team", UserID: "owner"}
	for _, host := range []string{"", RegistryHost, qomoloTestHost} {
		s, _, registry, _ := multiRegistryFixture(t)
		a, err := s.CreateAuthorizationForRegistry(ctx, owner, host, Credentials{Username: "person", Secret: "fixture-only"})
		if err != nil { t.Fatal(err) }
		want := host
		if want == "" { want = RegistryHost }
		if a.RegistryHost != want { t.Fatalf("wrong persisted host: %+v", a) }
		if _, err = s.Projects(ctx, owner, a.ID, 1); err != nil { t.Fatal(err) }
		if err = s.CheckTarget(ctx, owner, a.ID, "project", "image"); err != nil { t.Fatal(err) }
		if len(registry.hosts) != 3 { t.Fatalf("missing registry operations: %v", registry.hosts) }
		for _, got := range registry.hosts { if got != want { t.Fatalf("credential sent to %q instead of %q", got, want) } }
	}
	for _, host := range []string{"evil.invalid", "https://"+qomoloTestHost, qomoloTestHost+"/path", " " + qomoloTestHost, "HARBOR.QOMOLO.COM", qomoloTestHost+":443"} {
		s, _, registry, _ := multiRegistryFixture(t)
		if _, err := s.CreateAuthorizationForRegistry(ctx, owner, host, Credentials{Username: "person", Secret: "fixture"}); !errors.Is(err, ErrInvalid) || len(registry.hosts) != 0 { t.Fatalf("untrusted registry accepted: %q", host) }
	}
	s, _, registry, _ := multiRegistryFixture(t)
	s.config.RegistryHosts = []string{RegistryHost}
	if _, err := s.CreateAuthorizationForRegistry(ctx, owner, qomoloTestHost, Credentials{Username: "person", Secret: "fixture"}); !errors.Is(err, ErrInvalid) || len(registry.hosts) != 0 { t.Fatal("disabled registry received credentials") }
}

func TestCredentialCipherBindsRegistryAndPreservesLegacyAAD(t *testing.T) {
	s, store, _, _, _ := lifecycleFixture(t)
	legacy := store.authorization
	explicit := legacy
	explicit.RegistryHost = RegistryHost
	if !bytes.Equal(authorizationAAD(legacy), authorizationAAD(explicit)) { t.Fatal("legacy authorization AAD changed") }
	oldAAD, _ := json.Marshal([]string{"raytrain/environment-publish/v1", RegistryHost, legacy.ID, legacy.TenantID, legacy.OwnerID, legacy.Username, legacy.BuildID, legacy.Target, legacy.ExpiresAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00")})
	if !bytes.Equal(oldAAD, authorizationAAD(explicit)) { t.Fatal("persisted legacy cipher no longer decryptable") }
	for _, host := range []string{RegistryHost, qomoloTestHost} {
		a := legacy
		a.RegistryHost = host
		sealed, err := s.encrypt(a, Credentials{Username: a.Username, Secret: "fixture"})
		if err != nil { t.Fatal(err) }
		changed := a
		changed.RegistryHost = qomoloTestHost
		if host == qomoloTestHost { changed.RegistryHost = RegistryHost }
		if _, err = s.decryptCredential(changed, sealed); !errors.Is(err, ErrAuthorization) { t.Fatal("registry-tampered authorization decrypted") }
	}
}

func TestBuildCreationRequiresExplicitMatchingHostAndHostScopedIdempotency(t *testing.T) {
	ctx := context.Background()
	owner := Owner{TenantID: "team", UserID: "owner"}
	s, _, registry, _ := multiRegistryFixture(t)
	a, err := s.CreateAuthorizationForRegistry(ctx, owner, qomoloTestHost, Credentials{Username: "person", Secret: "fixture"})
	if err != nil { t.Fatal(err) }
	req := CreateRequest{AuthorizationID: a.ID, Project: "project", Repository: "image", Name: "environment", IdempotencyKey: "fixture-build"}
	if _, err = s.Create(ctx, owner, "workspace", req); !errors.Is(err, ErrConflict) || len(registry.hosts) != 1 { t.Fatal("Qomolo authorization silently selected target for legacy request") }
	req.RegistryHost = qomoloTestHost
	b, err := s.Create(ctx, owner, "workspace", req)
	if err != nil || b.RegistryHost != qomoloTestHost { t.Fatalf("selected registry build failed: %+v %v", b, err) }
	if again, err := s.Create(ctx, owner, "workspace", req); err != nil || again.ID != b.ID { t.Fatalf("same-host idempotency failed: %v", err) }
	req.RegistryHost = RegistryHost
	if _, err = s.Create(ctx, owner, "workspace", req); !errors.Is(err, ErrConflict) { t.Fatal("same idempotency key changed registry") }
	req.RegistryHost = "evil.invalid"
	if _, err = s.Create(ctx, owner, "workspace", req); !errors.Is(err, ErrInvalid) { t.Fatal("untrusted build target accepted") }
}

func TestRetryRejectsCrossRegistryAuthorizationBeforeAndAfterPush(t *testing.T) {
	for _, published := range []bool{false, true} {
		s, store, registry, _ := multiRegistryFixture(t)
		owner := Owner{TenantID: "team", UserID: "owner"}
		a, err := s.CreateAuthorizationForRegistry(context.Background(), owner, qomoloTestHost, Credentials{Username: "person", Secret: "fixture"})
		if err != nil { t.Fatal(err) }
		now := s.now()
		store.build.Status = Failed
		store.build.CleanedAt = &now
		if published { store.build.ImageDigest = "sha256:"+strings.Repeat("1", 64) }
		if _, err := s.Retry(context.Background(), owner, store.build.ID, a.ID); !errors.Is(err, ErrConflict) || store.retried || len(registry.hosts) != 1 { t.Fatalf("cross-registry retry accepted after push=%v", published) }
	}
}

func TestPushReconcileKeepsRegistryBindingAndImageReference(t *testing.T) {
	for _, mismatch := range []bool{false, true} {
		s, store, registry, runner := multiRegistryFixture(t)
		a, err := s.CreateAuthorizationForRegistry(context.Background(), Owner{TenantID: "team", UserID: "owner"}, qomoloTestHost, Credentials{Username: "person", Secret: "fixture"})
		if err != nil { t.Fatal(err) }
		store.build.RegistryHost = qomoloTestHost
		store.build.AuthID = a.ID
		if _, err = s.bindAuthorization(context.Background(), Owner{TenantID: "team", UserID: "owner"}, a.ID, store.build); err != nil { t.Fatal(err) }
		if mismatch { store.build.RegistryHost = RegistryHost }
		runner.result = StepResult{Done: true, ImageDigest: "sha256:"+strings.Repeat("2", 64)}
		before := len(registry.hosts)
		if err = s.reconcileBuild(context.Background(), store.build); err != nil { t.Fatal(err) }
		if mismatch {
			if store.build.Status != AwaitingAuth || runner.steps != 0 || len(registry.hosts) != before { t.Fatal("cross-registry push was attempted") }
		} else if store.build.Status != VerifyingPull || !strings.HasPrefix(store.build.ImageReference, qomoloTestHost+"/") || registry.hosts[len(registry.hosts)-1] != qomoloTestHost { t.Fatalf("wrong published reference: %+v", store.build) }
	}
}

func TestHarborRegistryClientSelectionKeepsLegacyClientBoundToItsHost(t *testing.T) {
	legacy := registryauth.NewClient()
	adapter := HarborRegistry{Client: legacy}
	selected, err := adapter.clientForHost("")
	if err != nil || selected != legacy || selected.Host() != RegistryHost { t.Fatal("legacy injected client was not reused") }
	selected, err = adapter.clientForHost(qomoloTestHost)
	if err != nil || selected == legacy || selected.Host() != qomoloTestHost { t.Fatal("Wellspiking client reused for Qomolo credentials") }
	qomolo := selected
	adapter.Client = qomolo
	selected, err = adapter.clientForHost(RegistryHost)
	if err != nil || selected == qomolo || selected.Host() != RegistryHost { t.Fatal("Qomolo client reused for Wellspiking credentials") }
	if _, err = adapter.clientForHost("evil.invalid"); err == nil { t.Fatal("adapter accepted arbitrary credential destination") }
}
