package rayapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
	"ray-train-platform-backend/httpapi"
	"ray-train-platform-backend/repositories"
)

func TestRayPackageUploadAfterTeamMigrationKeepsPersonalStorageHome(t *testing.T) {
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
	principal := auth.Principal{Subject: account.ID, Username: account.Username, TenantID: account.TenantID, StorageTenantID: account.StorageTenantID, StorageKey: account.StorageKey, Roles: account.Roles, AuthType: auth.AuthTypeOIDC}
	root := "ray-train/tenants/team-a/users/stable-alice/"
	binding, err := domain.NewPersonalDataMountBindingForStorageHome("migrated-workspace", account.TenantID, account.StorageTenantID, account.ID, "data-stable-alice", `{"type":"TOS","bucket":"test","server":"tos.example.test","region":"test"}`, account.StorageKey)
	if err != nil {
		t.Fatal(err)
	}
	binding.Status = domain.DataMountBindingReady
	if err := repository.CreateDataMountBinding(ctx, binding); err != nil {
		t.Fatal(err)
	}
	store := &rayTestStore{}
	router := rayRouterForRepository(t, repository, store, principal)
	payload := []byte("PK\x03\x04migrated-team-source")
	packageName := testPackageSHA256 + ".zip"
	for attempt := 0; attempt < 2; attempt++ {
		request := httptest.NewRequest(http.MethodPut, "/ray/api/packages/gcs/"+packageName, bytes.NewReader(payload))
		request.ContentLength = int64(len(payload))
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("migrated Ray upload attempt %d: status=%d body=%s", attempt, response.Code, response.Body.String())
		}
		artifact, err := repository.GetSourceArtifact(ctx, "team-b", account.ID, response.Header().Get(httpapi.SourceArtifactIDHeader))
		if err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(payload)
		wantKey := root + "workspace/.ray-train-archives/" + hex.EncodeToString(digest[:]) + ".zip"
		if artifact.State != domain.SourceArtifactReady || artifact.TenantID != "team-b" || artifact.StorageRoot != root || artifact.ObjectKey != wantKey || !bytes.Equal(store.putBody, payload) {
			t.Fatalf("migrated artifact did not preserve storage home: %+v", artifact)
		}
		if head := rayRequest(router, http.MethodHead, "/ray/api/packages/gcs/"+packageName, ""); head.Code != http.StatusOK || head.Header().Get(httpapi.SourceArtifactIDHeader) != artifact.ID {
			t.Fatalf("migrated package lookup: status=%d body=%s", head.Code, head.Body.String())
		}
		// Exercise missing-object recovery through the same user-facing PUT.
		delete(store.objects, artifact.ObjectKey)
	}
	var legacy repositories.UserRecord
	if err := database.First(&legacy, "id = ?", account.ID).Error; err != nil {
		t.Fatal(err)
	}
	if legacy.TenantID != "team-a" {
		t.Fatalf("upload rewrote the user's stable storage team: %q", legacy.TenantID)
	}
}
