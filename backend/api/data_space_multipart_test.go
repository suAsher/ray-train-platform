package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
)

type memoryUploadRepository struct {
	session domain.DataSpaceUploadSession
	parts   []domain.DataSpaceUploadPart
	claimed []domain.DataSpaceUploadSession
}

func (repo *memoryUploadRepository) FindActiveDataSpaceUpload(_ context.Context, tenant, user string, space domain.DataSpaceID, path string) (domain.DataSpaceUploadSession, []domain.DataSpaceUploadPart, error) {
	if repo.session.ID == "" || repo.session.TenantID != tenant || repo.session.UserID != user || repo.session.SpaceID != space || repo.session.RelativePath != path || repo.session.State != domain.DataSpaceUploadActive {
		return domain.DataSpaceUploadSession{}, nil, domain.ErrDataSpaceUploadNotFound
	}
	return repo.session, append([]domain.DataSpaceUploadPart(nil), repo.parts...), nil
}
func (repo *memoryUploadRepository) CreateOrResumeDataSpaceUpload(_ context.Context, session domain.DataSpaceUploadSession) (domain.DataSpaceUploadSession, []domain.DataSpaceUploadPart, bool, error) {
	if repo.session.ID != "" {
		return repo.session, repo.parts, false, nil
	}
	repo.session = session
	return session, nil, true, nil
}
func (repo *memoryUploadRepository) GetDataSpaceUpload(_ context.Context, id, tenant, user string) (domain.DataSpaceUploadSession, []domain.DataSpaceUploadPart, error) {
	if repo.session.ID != id || repo.session.TenantID != tenant || repo.session.UserID != user {
		return domain.DataSpaceUploadSession{}, nil, domain.ErrDataSpaceUploadNotFound
	}
	return repo.session, append([]domain.DataSpaceUploadPart(nil), repo.parts...), nil
}
func (repo *memoryUploadRepository) RecordDataSpaceUploadPart(_ context.Context, _ domain.DataSpaceUploadSession, part domain.DataSpaceUploadPart, expires time.Time) error {
	repo.parts = append(repo.parts, part)
	repo.session.ExpiresAt = expires
	return nil
}
func (repo *memoryUploadRepository) StartDataSpaceUploadCompletion(context.Context, string, string, string) (domain.DataSpaceUploadSession, []domain.DataSpaceUploadPart, error) {
	if len(repo.parts) != repo.session.TotalParts {
		return domain.DataSpaceUploadSession{}, nil, domain.ErrDataSpaceUploadIncomplete
	}
	repo.session.State = domain.DataSpaceUploadCompleting
	return repo.session, repo.parts, nil
}
func (repo *memoryUploadRepository) FinishDataSpaceUploadCompletion(_ context.Context, _ string, success bool) error {
	if success {
		repo.session.State = domain.DataSpaceUploadCompleted
	} else {
		repo.session.State = domain.DataSpaceUploadActive
	}
	return nil
}
func (repo *memoryUploadRepository) StartDataSpaceUploadAbort(context.Context, string, string, string) (domain.DataSpaceUploadSession, error) {
	if repo.session.ID == "" {
		return domain.DataSpaceUploadSession{}, domain.ErrDataSpaceUploadNotFound
	}
	repo.session.State = domain.DataSpaceUploadAborting
	return repo.session, nil
}
func (repo *memoryUploadRepository) FinishDataSpaceUploadAbort(_ context.Context, _ string, success bool) error {
	if success {
		repo.session.State = domain.DataSpaceUploadAborted
	} else {
		repo.session.State = domain.DataSpaceUploadActive
	}
	return nil
}
func (repo *memoryUploadRepository) ClaimExpiredDataSpaceUploads(context.Context, time.Time, int) ([]domain.DataSpaceUploadSession, error) {
	return append([]domain.DataSpaceUploadSession(nil), repo.claimed...), nil
}

func TestExpiredMultipartUploadIsAbortedByCleanup(t *testing.T) {
	session := domain.DataSpaceUploadSession{ID: "upload-expired", RootPrefix: "safe/root/", RelativePath: "model.pth", ProviderID: "secret", State: domain.DataSpaceUploadAborting}
	repo := &memoryUploadRepository{session: session, claimed: []domain.DataSpaceUploadSession{session}}
	store := &fakeDataSpaceObjectStore{}
	handler := multipartAPI(t, repo, store)
	if err := handler.cleanupExpiredDataSpaceUploads(context.Background(), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if !store.aborted || repo.session.State != domain.DataSpaceUploadAborted {
		t.Fatalf("cleanup store=%+v session=%+v", store, repo.session)
	}
}

func multipartAPI(t *testing.T, repo *memoryUploadRepository, store *fakeDataSpaceObjectStore) *Handler {
	t.Helper()
	handler := NewHandler(&fakeJobRepository{}, Options{DataSpaces: &fakeDataSpaceStore{}, DataObjectStore: store, DataSpaceUploads: repo})
	handler.newID = func() (string, error) { return "job-fixedmultipart", nil }
	return handler
}

func TestLargeDataSpaceUploadCreatesOpaqueMultipartTicketAndResumes(t *testing.T) {
	repo, store := &memoryUploadRepository{}, &fakeDataSpaceObjectStore{}
	router := dataSpaceRouter(multipartAPI(t, repo, store), auth.Principal{Subject: "user-a", TenantID: "tenant-a", Roles: []string{domain.RoleEngineer}, AuthType: auth.AuthTypeLocal})
	body := `{"path":"models/epoch.pth","contentType":"application/octet-stream","sizeBytes":6442450944}`
	first := postDataSpaceOperation(router, "/api/v1/data-spaces/my-files/uploads", body)
	if first.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", first.Code, first.Body.String())
	}
	for _, forbidden := range []string{"provider-secret", "ray-train/", "bucket", "tos"} {
		if strings.Contains(strings.ToLower(first.Body.String()), forbidden) {
			t.Fatalf("ticket leaked %q: %s", forbidden, first.Body.String())
		}
	}
	if !strings.Contains(first.Body.String(), `"mode":"multipart"`) || !strings.Contains(first.Body.String(), `"totalParts":96`) {
		t.Fatalf("wrong multipart ticket: %s", first.Body.String())
	}
	second := postDataSpaceOperation(router, "/api/v1/data-spaces/my-files/uploads", body)
	if second.Code != http.StatusOK || !strings.Contains(second.Body.String(), `"sessionId":"upload-fixedmultipart"`) {
		t.Fatalf("resume status=%d body=%s first=%s", second.Code, second.Body.String(), first.Body.String())
	}
}

func TestMultipartPartIntegrityCompletionAndAbort(t *testing.T) {
	payload := "payload"
	digest := sha256.Sum256([]byte(payload))
	now := time.Now().UTC()
	repo := &memoryUploadRepository{session: domain.DataSpaceUploadSession{ID: "upload-test", TenantID: "tenant-a", UserID: "user-a", SpaceID: domain.DataSpaceMyFiles, RootPrefix: "ray-train/tenants/tenant-a/users/user-a/files/", RelativePath: "model.pth", ContentType: "application/octet-stream", SizeBytes: int64(len(payload)), PartSizeBytes: int64(len(payload)), TotalParts: 1, ProviderID: "provider-secret", State: domain.DataSpaceUploadActive, ExpiresAt: now.Add(time.Hour)}}
	store := &fakeDataSpaceObjectStore{}
	router := dataSpaceRouter(multipartAPI(t, repo, store), auth.Principal{Subject: "user-a", TenantID: "tenant-a", Roles: []string{domain.RoleEngineer}, AuthType: auth.AuthTypeLocal})

	partRequest := httptest.NewRequest(http.MethodPut, "/api/v1/data-spaces/my-files/uploads/upload-test/parts/1", strings.NewReader(payload))
	partRequest.Header.Set("X-Part-SHA256", hex.EncodeToString(digest[:]))
	partResponse := httptest.NewRecorder()
	router.ServeHTTP(partResponse, partRequest)
	if partResponse.Code != http.StatusOK || store.partBody != payload || len(repo.parts) != 1 {
		t.Fatalf("part status=%d body=%s store=%q parts=%+v", partResponse.Code, partResponse.Body.String(), store.partBody, repo.parts)
	}

	complete := postDataSpaceOperation(router, "/api/v1/data-spaces/my-files/uploads/upload-test/complete", `{}`)
	if complete.Code != http.StatusOK || repo.session.State != domain.DataSpaceUploadCompleted || len(store.completed) != 1 {
		t.Fatalf("complete status=%d body=%s session=%+v parts=%+v", complete.Code, complete.Body.String(), repo.session, store.completed)
	}
}

func TestMultipartRejectsWrongPartSizeAndOwner(t *testing.T) {
	repo := &memoryUploadRepository{session: domain.DataSpaceUploadSession{ID: "upload-test", TenantID: "tenant-a", UserID: "user-a", SpaceID: domain.DataSpaceMyFiles, RootPrefix: "ray-train/tenants/tenant-a/users/user-a/files/", RelativePath: "model.pth", SizeBytes: 7, PartSizeBytes: 7, TotalParts: 1, ProviderID: "secret", State: domain.DataSpaceUploadActive}}
	store := &fakeDataSpaceObjectStore{}
	for _, principal := range []auth.Principal{{Subject: "user-a", TenantID: "tenant-a", Roles: []string{domain.RoleEngineer}, AuthType: auth.AuthTypeLocal}, {Subject: "user-b", TenantID: "tenant-a", Roles: []string{domain.RoleEngineer}, AuthType: auth.AuthTypeLocal}} {
		router := dataSpaceRouter(multipartAPI(t, repo, store), principal)
		request := httptest.NewRequest(http.MethodPut, "/api/v1/data-spaces/my-files/uploads/upload-test/parts/1", strings.NewReader("short"))
		request.Header.Set("X-Part-SHA256", strings.Repeat("a", 64))
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code < 400 {
			t.Fatalf("principal=%s unexpectedly accepted status=%d", principal.Subject, response.Code)
		}
	}
	if store.partBody != "" {
		t.Fatal("invalid request touched object storage")
	}
}
