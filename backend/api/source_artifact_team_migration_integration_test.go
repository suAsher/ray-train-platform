package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
	"ray-train-platform-backend/repositories"
)

func TestRequestScopedSourceUploadAfterTeamMigrationKeepsPersonalStorageHome(t *testing.T) {
	ctx := context.Background()
	database, err := gorm.Open(sqlite.Open("file:"+strings.ReplaceAll(t.Name(), "/", "-")+"?mode=memory&cache=shared"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	sqlDatabase, err := database.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDatabase.Close() })
	if err := database.AutoMigrate(&repositories.TenantRecord{}, &repositories.UserRecord{}, &repositories.LocalUserRecord{}, &repositories.TenantMembershipRecord{}, &repositories.SourceArtifactRecord{}, &repositories.SourceArtifactRequestRecord{}, &repositories.DataMountBindingRecord{}); err != nil {
		t.Fatal(err)
	}
	repository := repositories.NewGormRepository(database)
	for _, tenant := range []string{"team-a", "team-b"} {
		if err := repository.EnsureIdentity(ctx, auth.Principal{Subject: "seed-" + tenant, Username: "seed-" + tenant, TenantID: tenant, Roles: []string{domain.RoleEngineer}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := repository.EnsureIdentity(ctx, auth.Principal{Subject: "migrated-user", Username: "alice", TenantID: "team-a", Roles: []string{domain.RoleEngineer}}); err != nil {
		t.Fatal(err)
	}
	if err := repository.CreateLocalUser(ctx, domain.LocalUser{ID: "migrated-user", Username: "alice", StorageKey: "stable-alice", TenantID: "team-a", Roles: []string{domain.RoleEngineer}, PasswordHash: "!external-only"}); err != nil {
		t.Fatal(err)
	}
	if err := repository.ReassignActiveMembership(ctx, "migrated-user", "team-a", "team-b", []string{domain.RoleEngineer}, true); err != nil {
		t.Fatal(err)
	}
	account, err := repository.FindLocalUserByID(ctx, "migrated-user")
	if err != nil {
		t.Fatal(err)
	}
	principal := auth.Principal{Subject: account.ID, Username: account.Username, TenantID: account.TenantID, StorageTenantID: account.StorageTenantID, StorageKey: account.StorageKey, Roles: account.Roles, AuthType: auth.AuthTypePAT, Scopes: []string{domain.PATScopeSourcesWrite}}
	binding, err := domain.NewPersonalDataMountBindingForStorageHome("migrated-workspace", account.TenantID, account.StorageTenantID, account.ID, "data-stable-alice", `{"type":"TOS","bucket":"test","server":"tos.example.test","region":"test"}`, account.StorageKey)
	if err != nil {
		t.Fatal(err)
	}
	binding.Status = domain.DataMountBindingReady
	if err := repository.CreateDataMountBinding(ctx, binding); err != nil {
		t.Fatal(err)
	}
	store := newImmutableTOSMock(t)
	router := artifactTestRouter(t, repository, store, principal, time.Date(2026, 10, 12, 8, 0, 0, 0, time.UTC))
	body := `{"clientRequestId":"source-request-0123456789abcdef01234567","sha256":"` + apiArtifactDigest + `","sizeBytes":7}`
	var artifactID string
	for attempt := 0; attempt < 2; attempt++ {
		created := performArtifactRequest(router, http.MethodPost, "/api/v1/source-artifacts", body)
		if created.Code != http.StatusCreated {
			t.Fatalf("migrated request upload attempt %d: status=%d body=%s", attempt, created.Code, created.Body.String())
		}
		var envelope struct {
			Data sourceArtifactResponse `json:"data"`
		}
		if err := json.Unmarshal(created.Body.Bytes(), &envelope); err != nil {
			t.Fatal(err)
		}
		if attempt > 0 && envelope.Data.ArtifactID != artifactID {
			t.Fatalf("recovery changed the idempotent artifact: %q -> %q", artifactID, envelope.Data.ArtifactID)
		}
		artifactID = envelope.Data.ArtifactID
		artifact, err := repository.GetSourceArtifact(ctx, "team-b", account.ID, artifactID)
		if err != nil {
			t.Fatal(err)
		}
		root := "ray-train/tenants/team-a/users/stable-alice/"
		if artifact.StorageRoot != root || !strings.HasPrefix(artifact.ObjectKey, root+"workspace/.ray-train-archives/"+artifactID+"/") {
			t.Fatalf("migrated request artifact moved personal storage: %+v", artifact)
		}
		payload := []byte("new-zip")
		upload, err := http.NewRequest(http.MethodPut, envelope.Data.UploadURL, bytes.NewReader(payload))
		if err != nil {
			t.Fatal(err)
		}
		for key, value := range envelope.Data.RequiredHeaders {
			upload.Header.Set(key, value)
		}
		uploaded, err := store.server.Client().Do(upload)
		if err != nil {
			t.Fatal(err)
		}
		uploaded.Body.Close()
		if uploaded.StatusCode != http.StatusOK {
			t.Fatalf("migrated object upload: status=%d", uploaded.StatusCode)
		}
		completed := performArtifactRequest(router, http.MethodPost, "/api/v1/source-artifacts/"+artifactID+"/complete", "")
		if completed.Code != http.StatusOK {
			t.Fatalf("migrated complete: status=%d body=%s", completed.Code, completed.Body.String())
		}
		ready, err := repository.GetSourceArtifact(ctx, "team-b", account.ID, artifactID)
		if err != nil || ready.State != domain.SourceArtifactReady || !bytes.Equal(store.object(artifact.ObjectKey), payload) {
			t.Fatalf("migrated source not ready: artifact=%+v err=%v", ready, err)
		}
		if retry := performArtifactRequest(router, http.MethodPost, "/api/v1/source-artifacts", body); retry.Code != http.StatusOK || !strings.Contains(retry.Body.String(), `"artifactId":"`+artifactID+`"`) {
			t.Fatalf("migrated idempotent request: status=%d body=%s", retry.Code, retry.Body.String())
		}
		store.mu.Lock()
		delete(store.objects, artifact.ObjectKey)
		delete(store.digests, artifact.ObjectKey)
		store.mu.Unlock()
	}
	var legacy repositories.UserRecord
	if err := database.First(&legacy, "id = ?", account.ID).Error; err != nil {
		t.Fatal(err)
	}
	if legacy.TenantID != "team-a" {
		t.Fatalf("request upload rewrote stable storage team: %q", legacy.TenantID)
	}
}
