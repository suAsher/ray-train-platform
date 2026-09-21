package environmentbuild

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type lifecycleStore struct {
	Store
	build                 Build
	authorization         Authorization
	authorizationError    error
	saved                 []Build
	revoked               bool
	deletedMaterials      int
	retried               bool
	expiredAuthorizations []Authorization
}

func (s *lifecycleStore) EnvironmentBuild(_ context.Context, o Owner, id string) (Build, error) {
	if id != s.build.ID || o.UserID != s.build.OwnerID || o.TenantID != s.build.TenantID {
		return Build{}, ErrNotFound
	}
	return s.build, nil
}
func (s *lifecycleStore) EnvironmentAuthorization(_ context.Context, o Owner, id string) (Authorization, error) {
	if s.authorizationError != nil {
		return Authorization{}, s.authorizationError
	}
	a := s.authorization
	if id != a.ID || o.UserID != a.OwnerID || o.TenantID != a.TenantID || s.revoked {
		return Authorization{}, ErrNotFound
	}
	return a, nil
}
func (s *lifecycleStore) SaveEnvironmentBuild(_ context.Context, b Build, _ string) error {
	s.build = b
	s.saved = append(s.saved, b)
	return nil
}
func (s *lifecycleStore) EnvironmentCredentialMaterials(context.Context, string) ([]CredentialMaterial, error) {
	return []CredentialMaterial{{Ref: s.authorization.SecretRef}}, nil
}
func (s *lifecycleStore) DeleteEnvironmentCredentialMaterial(context.Context, string) error {
	s.deletedMaterials++
	return nil
}
func (s *lifecycleStore) DeleteEnvironmentAuthorization(context.Context, string) error {
	s.revoked = true
	return nil
}
func (s *lifecycleStore) ExpiredEnvironmentCredentialMaterials(context.Context, time.Time) ([]CredentialMaterial, error) {
	return nil, nil
}
func (s *lifecycleStore) ExpiredEnvironmentAuthorizations(context.Context, time.Time) ([]Authorization, error) {
	return s.expiredAuthorizations, nil
}
func (s *lifecycleStore) RetryEnvironmentBuild(_ context.Context, _ Owner, _ string, authID string, _ time.Time) (Build, error) {
	s.retried = true
	s.build.AuthID = authID
	s.build.Status = Queued
	return s.build, nil
}
func (s *lifecycleStore) ListEnvironmentBuilds(context.Context, Owner) ([]Build, error) {
	return []Build{s.build}, nil
}

type lifecycleRunner struct {
	Runner
	result       StepResult
	stepError    error
	cleanupError error
	steps        int
	cleanups     int
	retained     bool
}

func (r *lifecycleRunner) Step(_ context.Context, _ Build, _ *Credentials) (StepResult, error) {
	r.steps++
	return r.result, r.stepError
}
func (r *lifecycleRunner) Cleanup(_ context.Context, _ Build, retain bool) error {
	r.cleanups++
	r.retained = retain
	return r.cleanupError
}

type lifecycleRegistry struct {
	Registry
	deny bool
}

func (r *lifecycleRegistry) CheckPush(context.Context, Credentials, string) error {
	if r.deny {
		return ErrAuthorization
	}
	return nil
}
func (r *lifecycleRegistry) Projects(context.Context, Credentials, int) ([]Project, error) {
	return []Project{{Name: "project", ProjectID: 4, CanPush: true}}, nil
}

type lifecycleVault struct {
	Vault
	sealed                []byte
	getError, errorDelete error
}

func (v *lifecycleVault) Get(context.Context, string) ([]byte, error) { return v.sealed, v.getError }
func (v *lifecycleVault) Delete(context.Context, string) error        { return v.errorDelete }
func lifecycleFixture(t *testing.T) (*Service, *lifecycleStore, *lifecycleRunner, *lifecycleRegistry, *lifecycleVault) {
	t.Helper()
	now := time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC)
	store := &lifecycleStore{build: Build{ID: "build", TenantID: "team", OwnerID: "owner", AuthID: "auth", Project: "project", Repository: "image", Status: Pushing, Attempt: 1, ArtifactExpiresAt: now.Add(time.Hour)}}
	store.authorization = Authorization{ID: "auth", TenantID: "team", OwnerID: "owner", Username: "harbor-user", BuildID: "build", Target: "project/image", SecretRef: "material", ExpiresAt: now.Add(time.Hour)}
	runner := &lifecycleRunner{}
	registry := &lifecycleRegistry{}
	vault := &lifecycleVault{}
	s, err := NewService(store, runner, registry, vault, Config{Enabled: true, BaseImage: "harbor.wellspiking.ai/public/base@sha256:" + strings.Repeat("1", 64), WorkspaceImage: "harbor.wellspiking.ai/public/debug@sha256:" + strings.Repeat("2", 64), EncryptionKey: []byte(strings.Repeat("k", 32))})
	if err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return now }
	vault.sealed, err = s.encrypt(store.authorization, Credentials{Username: "harbor-user", Secret: "test-secret"})
	if err != nil {
		t.Fatal(err)
	}
	return s, store, runner, registry, vault
}
func TestPushingAuthorizationLossStopsBeforeRunner(t *testing.T) {
	for _, name := range []string{"missing", "expired", "changed build", "changed target", "missing material", "corrupt ciphertext", "permission revoked"} {
		t.Run(name, func(t *testing.T) {
			s, store, runner, registry, vault := lifecycleFixture(t)
			switch name {
			case "missing":
				store.authorizationError = ErrNotFound
			case "expired":
				store.authorization.ExpiresAt = s.now().Add(-time.Second)
			case "changed build":
				store.authorization.BuildID = "another"
			case "changed target":
				store.authorization.Target = "project/other"
			case "missing material":
				vault.getError = ErrNotFound
			case "corrupt ciphertext":
				vault.sealed = []byte("not-ciphertext")
			case "permission revoked":
				registry.deny = true
			}
			if err := s.reconcileBuild(context.Background(), store.build); err != nil {
				t.Fatal(err)
			}
			if store.build.Status != AwaitingAuth || store.build.ResumeStatus != Pushing || runner.steps != 0 {
				t.Fatalf("unsafe authorization transition: status=%s resume=%s runner=%d", store.build.Status, store.build.ResumeStatus, runner.steps)
			}
			if strings.Contains(store.build.Message, "test-secret") {
				t.Fatal("credential leaked")
			}
		})
	}
}
func TestPhaseErrorsAndInvalidResultsNeverPublish(t *testing.T) {
	for _, tc := range []struct {
		name, status string
		result       StepResult
		err          error
		want         string
	}{
		{name: "publisher auth refusal", status: Pushing, err: ErrAuthorization, want: AwaitingAuth},
		{name: "safe unsupported package", status: Capturing, err: &PhaseError{Code: "PACKAGE_MODIFIED"}, want: Failed},
		{name: "untrusted classification", status: Capturing, err: &PhaseError{Code: "raw test-secret diagnostic"}, want: Failed},
		{name: "missing capture", status: Capturing, result: StepResult{Done: true}, want: Failed},
		{name: "missing layer hash", status: Building, result: StepResult{Done: true, ChecksJSON: `{"layerDigest":"sha256:bad"}`}, want: Failed},
		{name: "missing OCI hash", status: Validating, result: StepResult{Done: true, ArtifactDigest: "bad"}, want: Failed},
		{name: "bad push digest", status: Pushing, result: StepResult{Done: true, ImageDigest: "bad"}, want: Failed},
		{name: "missing published digest", status: VerifyingPull, result: StepResult{Done: true}, want: Failed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, store, runner, _, _ := lifecycleFixture(t)
			store.build.Status = tc.status
			runner.result = tc.result
			runner.stepError = tc.err
			if err := s.reconcileBuild(context.Background(), store.build); err != nil {
				t.Fatal(err)
			}
			if store.build.Status != tc.want || store.build.ImageID != "" || strings.Contains(store.build.Message, "test-secret") {
				t.Fatalf("unsafe phase failure: %+v", store.build)
			}
			if tc.name == "safe unsupported package" && store.build.Message != phaseMessage("PACKAGE_MODIFIED") {
				t.Fatal("actionable safe diagnostic lost")
			}
		})
	}
}
func TestCleanupFailureReleasesLeaseAndKeepsCancellationPending(t *testing.T) {
	s, store, runner, _, vault := lifecycleFixture(t)
	store.build.Status = CancelRequested
	runner.cleanupError = errors.New("pod deletion pending")
	if err := s.reconcileBuild(context.Background(), store.build); err == nil {
		t.Fatal("pending cleanup reported complete")
	}
	if len(store.saved) != 1 || store.build.Status != CancelRequested || store.build.CleanedAt != nil || store.revoked {
		t.Fatal("premature cleanup completion or lease not released")
	}
	runner.cleanupError = nil
	vault.errorDelete = errors.New("credential store unavailable")
	if err := s.reconcileBuild(context.Background(), store.build); err == nil {
		t.Fatal("credential cleanup failure ignored")
	}
	if len(store.saved) != 2 || store.build.CleanedAt != nil || store.revoked {
		t.Fatal("credential deletion failure marked clean")
	}
	vault.errorDelete = nil
	store.build.ImageDigest = "sha256:" + strings.Repeat("3", 64)
	if err := s.reconcileBuild(context.Background(), store.build); err != nil {
		t.Fatal(err)
	}
	if store.build.Status != Canceled || store.build.CleanedAt == nil || !store.revoked || !strings.Contains(store.build.Message, "Harbor") {
		t.Fatal("cancel failed to preserve published image evidence and clean credentials")
	}
}
func TestExpiredBuildStopsAndExpiredAuthorizationIsCleaned(t *testing.T) {
	s, store, runner, _, _ := lifecycleFixture(t)
	store.build.Status = Building
	store.build.ArtifactExpiresAt = s.now().Add(-time.Second)
	if err := s.reconcileBuild(context.Background(), store.build); err != nil {
		t.Fatal(err)
	}
	if store.build.Status != Failed || runner.steps != 0 {
		t.Fatal("expired build still executed")
	}
	if err := s.reconcileBuild(context.Background(), store.build); err != nil {
		t.Fatal(err)
	}
	if runner.retained || store.build.CleanedAt == nil {
		t.Fatal("expired artifact retained")
	}
	s, store, _, _, _ = lifecycleFixture(t)
	store.expiredAuthorizations = []Authorization{store.authorization}
	if err := s.purgeExpiredCredentials(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !store.revoked || store.deletedMaterials != 1 {
		t.Fatal("expired authorization cleanup missing")
	}
}
func TestRetryBoundaryAndPublishedDigestAvoidsSecondPush(t *testing.T) {
	for _, name := range []string{"other owner", "active", "not cleaned", "attempt limit", "expired", "missing new auth", "different bound operation", "published digest"} {
		t.Run(name, func(t *testing.T) {
			s, store, _, _, _ := lifecycleFixture(t)
			now := s.now()
			store.build.Status = Failed
			store.build.CleanedAt = &now
			owner := Owner{TenantID: "team", UserID: "owner"}
			authID := "auth"
			switch name {
			case "other owner":
				owner.UserID = "other"
			case "active":
				store.build.Status = Building
			case "not cleaned":
				store.build.CleanedAt = nil
			case "attempt limit":
				store.build.Attempt = 5
			case "expired":
				store.build.ArtifactExpiresAt = now
			case "missing new auth":
				authID = "missing"
			case "different bound operation":
				store.authorization.BuildID = "other"
			case "published digest":
				store.build.ImageDigest = "sha256:" + strings.Repeat("3", 64)
				authID = ""
				store.authorizationError = ErrNotFound
			}
			_, err := s.Retry(context.Background(), owner, store.build.ID, authID)
			if name == "published digest" {
				if err != nil || !store.retried {
					t.Fatal("published image should resume verification without credentials")
				}
			} else if err == nil || store.retried {
				t.Fatalf("invalid retry accepted: %s", name)
			}
		})
	}
}
func TestAuthorizationProjectsTargetBindingAndRevokeOwnership(t *testing.T) {
	s, store, _, _, _ := lifecycleFixture(t)
	ctx := context.Background()
	owner := Owner{TenantID: "team", UserID: "owner"}
	projects, err := s.Projects(ctx, owner, "auth", 1)
	if err != nil || len(projects) != 1 || !projects[0].CanPush {
		t.Fatal("project listing lost verified metadata")
	}
	if _, err = s.Projects(ctx, owner, "auth", 0); !errors.Is(err, ErrInvalid) {
		t.Fatal("unbounded page accepted")
	}
	if err = s.CheckTarget(ctx, owner, "auth", "project", "other"); !errors.Is(err, ErrConflict) {
		t.Fatal("bound target changed")
	}
	if err = s.CheckTarget(ctx, owner, "auth", "project", "../other"); !errors.Is(err, ErrInvalid) {
		t.Fatal("invalid target reached registry")
	}
	if err = s.RevokeAuthorization(ctx, Owner{TenantID: "team", UserID: "other"}, "auth"); !errors.Is(err, ErrNotFound) || store.revoked {
		t.Fatal("cross-owner revocation")
	}
	if err = s.RevokeAuthorization(ctx, owner, "auth"); err != nil || !store.revoked {
		t.Fatal("owner revocation failed")
	}
}
func TestCapabilitiesAndDisabledLoopDoNotLaunchJobs(t *testing.T) {
	disabled, err := NewService(nil, nil, nil, nil, Config{})
	if err != nil {
		t.Fatal(err)
	}
	if disabled.Capabilities()["enabled"] != false || (*Service)(nil).Capabilities()["enabled"] != false {
		t.Fatal("disabled environment advertised")
	}
	if _, err = disabled.Create(context.Background(), Owner{}, "workspace", CreateRequest{}); !errors.Is(err, ErrUnavailable) {
		t.Fatal("disabled publication accepted")
	}
	if _, err = disabled.CreateAuthorization(context.Background(), Owner{}, Credentials{}); !errors.Is(err, ErrUnavailable) {
		t.Fatal("disabled credential creation accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan struct{})
	go func() { disabled.Run(ctx); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("shutdown did not stop reconciler")
	}
	s, store, _, _, _ := lifecycleFixture(t)
	if s.Capabilities()["registryHost"] != RegistryHost || s.Capabilities()["enabled"] != true {
		t.Fatal("wrong capabilities")
	}
	items, err := s.List(context.Background(), Owner{TenantID: "team", UserID: "owner"})
	if err != nil || len(items) != 1 || items[0].ID != store.build.ID {
		t.Fatal("owner list failed")
	}
}

func TestSafePreflightClassificationDoesNotExposeRunnerDetails(t *testing.T) {
	for _, code := range []string{"UNSUPPORTED_WORKSPACE", "ENVIRONMENT_CHANGED", "WHEEL_UNAVAILABLE", "PACKAGE_MODIFIED", "BUILD_TIMEOUT", "PULL_FAILED", "TEMP_STORAGE_FULL"} {
		error := &PhaseError{Code: code}
		if error.UserMessage() == "" || error.UserMessage() != phaseMessage(code) {
			t.Fatal("missing safe user action")
		}
	}
	var absent *PhaseError
	if absent.UserMessage() != "" || (&PhaseError{Code: "raw test-secret"}).UserMessage() != "" {
		t.Fatal("untrusted preflight text became user-visible")
	}
}
