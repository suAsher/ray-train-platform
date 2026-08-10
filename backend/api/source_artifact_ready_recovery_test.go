package api

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
	"ray-train-platform-backend/objectstore"
	"ray-train-platform-backend/repositories"
)

func (f *fakeSourceArtifactRepository) CreateOrReuseSourceArtifactWithLimits(ctx context.Context, artifact *domain.SourceArtifact, _ repositories.SourceArtifactLimits) (*domain.SourceArtifact, error) {
	return f.CreateOrReuseSourceArtifact(ctx, artifact)
}

func (f *fakeSourceArtifactRepository) ReopenSourceArtifactUploadWithLimits(_ context.Context, tenant, user, id string, expires time.Time, _ repositories.SourceArtifactLimits) (*domain.SourceArtifact, error) {
	if f.reopenErr != nil {
		return nil, f.reopenErr
	}
	artifact, err := f.GetSourceArtifact(context.Background(), tenant, user, id)
	if err != nil {
		return nil, err
	}
	artifact.State = domain.SourceArtifactPending
	artifact.CompletedAt = nil
	artifact.UploadExpiresAt = expires
	f.artifact = artifact
	return artifact, nil
}

func readyRecoveryFixture(t *testing.T, now time.Time) domain.SourceArtifact {
	t.Helper()
	pending, err := domain.NewSourceArtifact(domain.SourceArtifactInput{
		ID: "ready-artifact", TenantID: "tenant-a", UserID: "user-a", SHA256: apiArtifactDigest, SizeBytes: 100,
	}, now.Add(15*time.Minute), now)
	if err != nil {
		t.Fatal(err)
	}
	ready, err := pending.MarkReady(now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	return ready
}

func TestCreateSourceArtifactVerifiesAndRecoversReadyArtifact(t *testing.T) {
	now := time.Date(2026, 8, 10, 8, 0, 0, 0, time.UTC)
	principal := auth.Principal{Subject: "user-a", TenantID: "tenant-a", Roles: []string{"Engineer"}, AuthType: auth.AuthTypeOIDC}
	body := `{"sha256":"` + apiArtifactDigest + `","sizeBytes":100}`
	tests := []struct {
		name         string
		head         objectstore.ObjectInfo
		headErr      error
		wantStatus   int
		wantPresign  int
		wantPending  bool
		wantResponse string
	}{
		{name: "healthy ready", head: objectstore.ObjectInfo{SizeBytes: 100, Metadata: map[string]string{"sha256": apiArtifactDigest}}, wantStatus: http.StatusOK, wantResponse: `"uploadRequired":false`},
		{name: "missing object reopens upload", headErr: objectstore.ErrNotFound, wantStatus: http.StatusCreated, wantPresign: 1, wantPending: true, wantResponse: `"uploadRequired":true`},
		{name: "store unavailable", headErr: objectstore.ErrUnavailable, wantStatus: http.StatusServiceUnavailable},
		{name: "object mismatch", head: objectstore.ObjectInfo{SizeBytes: 99, Metadata: map[string]string{"sha256": apiArtifactDigest}}, wantStatus: http.StatusConflict},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ready := readyRecoveryFixture(t, now)
			repo := &fakeSourceArtifactRepository{artifact: &ready}
			store := &fakeArtifactStore{
				head: test.head, headErr: test.headErr,
				presign: objectstore.PresignedPut{URL: "https://private-bucket.tos.example/object", ContentLength: 100, ExpiresAt: now.Add(15 * time.Minute)},
			}
			response := performArtifactRequest(artifactTestRouter(t, repo, store, principal, now), http.MethodPost, "/api/v1/source-artifacts", body)
			if response.Code != test.wantStatus {
				t.Fatalf("status=%d, want %d", response.Code, test.wantStatus)
			}
			if store.headCalls != 1 || store.presignCalls != test.wantPresign {
				t.Fatalf("head=%d presign=%d, want 1/%d", store.headCalls, store.presignCalls, test.wantPresign)
			}
			if test.wantPending && repo.artifact.State != domain.SourceArtifactPending {
				t.Fatalf("missing READY object was not reopened: %q", repo.artifact.State)
			}
			if test.wantResponse != "" && !strings.Contains(response.Body.String(), test.wantResponse) {
				t.Fatalf("response missing %s", test.wantResponse)
			}
		})
	}
}

func TestCompleteReadySourceArtifactStillVerifiesObject(t *testing.T) {
	now := time.Date(2026, 8, 10, 8, 0, 0, 0, time.UTC)
	principal := auth.Principal{Subject: "user-a", TenantID: "tenant-a", Roles: []string{"Engineer"}, AuthType: auth.AuthTypeOIDC}
	tests := []struct {
		name       string
		head       objectstore.ObjectInfo
		headErr    error
		wantStatus int
	}{
		{name: "healthy", head: objectstore.ObjectInfo{SizeBytes: 100, Metadata: map[string]string{"sha256": apiArtifactDigest}}, wantStatus: http.StatusOK},
		{name: "missing", headErr: objectstore.ErrNotFound, wantStatus: http.StatusNotFound},
		{name: "mismatch", head: objectstore.ObjectInfo{SizeBytes: 99, Metadata: map[string]string{"sha256": apiArtifactDigest}}, wantStatus: http.StatusConflict},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ready := readyRecoveryFixture(t, now)
			repo := &fakeSourceArtifactRepository{artifact: &ready}
			store := &fakeArtifactStore{head: test.head, headErr: test.headErr}
			response := performArtifactRequest(artifactTestRouter(t, repo, store, principal, now), http.MethodPost, "/api/v1/source-artifacts/ready-artifact/complete", "")
			if response.Code != test.wantStatus || store.headCalls != 1 {
				t.Fatalf("status=%d head=%d, want %d/1", response.Code, store.headCalls, test.wantStatus)
			}
			if test.headErr == objectstore.ErrNotFound && repo.artifact.State != domain.SourceArtifactPending {
				t.Fatalf("missing completed object was not reopened: %q", repo.artifact.State)
			}
		})
	}
}

var _ SourceArtifactRepository = (*fakeSourceArtifactRepository)(nil)
var _ = repositories.ErrSourceArtifactNotFound
