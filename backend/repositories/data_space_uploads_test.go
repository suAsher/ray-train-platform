package repositories

import (
	"context"
	"errors"
	"testing"
	"time"

	"ray-train-platform-backend/domain"
)

func multipartRepository(t *testing.T) *GormRepository {
	t.Helper()
	repo := testRepository(t)
	if err := repo.db.AutoMigrate(&DataSpaceUploadRecord{}, &DataSpaceUploadPartRecord{}); err != nil {
		t.Fatalf("migrate multipart records: %v", err)
	}
	return repo
}

func multipartSession(id string) domain.DataSpaceUploadSession {
	now := time.Now().UTC()
	return domain.DataSpaceUploadSession{ID: id, TenantID: "tenant-a", UserID: "user-a", SpaceID: domain.DataSpaceMyFiles, RootPrefix: "ray-train/tenants/tenant-a/users/user-a/files/", RelativePath: "model.pth", ContentType: "application/octet-stream", SizeBytes: 300 * 1024 * 1024, PartSizeBytes: 64 * 1024 * 1024, TotalParts: 5, ProviderID: "provider-secret", State: domain.DataSpaceUploadActive, ExpiresAt: now.Add(24 * time.Hour), CreatedAt: now, UpdatedAt: now}
}

func TestDataSpaceUploadCreateResumeAndOwnerIsolation(t *testing.T) {
	repo := multipartRepository(t)
	session := multipartSession("upload-1")
	got, parts, created, err := repo.CreateOrResumeDataSpaceUpload(context.Background(), session)
	if err != nil || !created || got.ID != session.ID || len(parts) != 0 {
		t.Fatalf("create = %+v parts=%+v created=%v err=%v", got, parts, created, err)
	}
	resumed, _, created, err := repo.CreateOrResumeDataSpaceUpload(context.Background(), multipartSession("upload-new-id"))
	if err != nil || created || resumed.ID != session.ID {
		t.Fatalf("resume = %+v created=%v err=%v", resumed, created, err)
	}
	if _, _, err := repo.GetDataSpaceUpload(context.Background(), session.ID, session.TenantID, "another-user"); !errors.Is(err, domain.ErrDataSpaceUploadNotFound) {
		t.Fatalf("wrong owner error = %v", err)
	}
}

func TestDataSpaceUploadPartsCompletionAndExpiry(t *testing.T) {
	repo := multipartRepository(t)
	session := multipartSession("upload-2")
	if _, _, _, err := repo.CreateOrResumeDataSpaceUpload(context.Background(), session); err != nil {
		t.Fatal(err)
	}
	if _, _, err := repo.StartDataSpaceUploadCompletion(context.Background(), session.ID, session.TenantID, session.UserID); !errors.Is(err, domain.ErrDataSpaceUploadIncomplete) {
		t.Fatalf("incomplete error = %v", err)
	}
	for number := 1; number <= session.TotalParts; number++ {
		size, _ := session.Plan().ExpectedPartSize(number)
		part := domain.DataSpaceUploadPart{SessionID: session.ID, PartNumber: number, SizeBytes: size, SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", ETag: "etag", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
		if err := repo.RecordDataSpaceUploadPart(context.Background(), session, part, time.Now().Add(24*time.Hour)); err != nil {
			t.Fatalf("record part %d: %v", number, err)
		}
	}
	started, parts, err := repo.StartDataSpaceUploadCompletion(context.Background(), session.ID, session.TenantID, session.UserID)
	if err != nil || started.State != domain.DataSpaceUploadCompleting || len(parts) != session.TotalParts {
		t.Fatalf("completion = %+v parts=%d err=%v", started, len(parts), err)
	}
	if err := repo.FinishDataSpaceUploadCompletion(context.Background(), session.ID, true); err != nil {
		t.Fatal(err)
	}
	stored, _, err := repo.GetDataSpaceUpload(context.Background(), session.ID, session.TenantID, session.UserID)
	if err != nil || stored.State != domain.DataSpaceUploadCompleted {
		t.Fatalf("stored = %+v err=%v", stored, err)
	}
}

func TestClaimExpiredDataSpaceUploads(t *testing.T) {
	repo := multipartRepository(t)
	now := time.Now().UTC()
	session := multipartSession("upload-expired")
	session.ExpiresAt = now.Add(-time.Minute)
	if _, _, _, err := repo.CreateOrResumeDataSpaceUpload(context.Background(), session); err != nil {
		t.Fatal(err)
	}
	claimed, err := repo.ClaimExpiredDataSpaceUploads(context.Background(), now, 10)
	if err != nil || len(claimed) != 1 || claimed[0].State != domain.DataSpaceUploadAborting {
		t.Fatalf("claimed = %+v err=%v", claimed, err)
	}
}
