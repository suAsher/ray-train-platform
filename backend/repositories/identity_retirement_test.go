package repositories

import (
	"context"
	"strings"
	"testing"
	"time"

	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
)

func TestRetiredTenantRejectsIdentityAndCredentials(t *testing.T) {
	ctx := context.Background()
	for _, operation := range []string{"identity", "local user", "session", "PAT", "login", "session lookup", "PAT lookup", "tenant exists"} {
		t.Run(operation, func(t *testing.T) {
			repo := testRepository(t)
			if err := repo.db.AutoMigrate(&LocalUserRecord{}, &LocalSessionRecord{}, &PersonalAccessTokenRecord{}); err != nil {
				t.Fatal(err)
			}
			principal := auth.Principal{Subject: "user-a", Username: "alice", TenantID: "tenant-a", Roles: []string{"Engineer"}}
			if err := repo.EnsureIdentity(ctx, principal); err != nil {
				t.Fatal(err)
			}
			user := domain.LocalUser{ID: "user-a", Username: "alice", TenantID: "tenant-a", Roles: []string{"Engineer"}, PasswordHash: "test-hash"}
			if err := repo.CreateLocalUser(ctx, user); err != nil {
				t.Fatal(err)
			}
			session := domain.LocalSession{ID: "s1", PublicID: "p1", UserID: "user-a", TenantID: "tenant-a", ExpiresAt: time.Now().Add(time.Hour)}
			if err := repo.CreateLocalSession(ctx, session, "digest"); err != nil {
				t.Fatal(err)
			}
			token := domain.PersonalAccessToken{ID: "t1", PublicID: "pt1", UserID: "user-a", TenantID: "tenant-a", Scopes: []string{"jobs:read"}, ExpiresAt: time.Now().Add(time.Hour)}
			if err := repo.CreatePersonalAccessToken(ctx, token, strings.Repeat("a", 64)); err != nil {
				t.Fatal(err)
			}
			if err := repo.db.Model(&TenantRecord{}).Where("id = ?", "tenant-a").Updates(map[string]any{"retired_at": time.Now(), "retired_by": "admin"}).Error; err != nil {
				t.Fatal(err)
			}
			var err error
			switch operation {
			case "identity":
				err = repo.EnsureIdentity(ctx, principal)
			case "local user":
				user.ID = "u2"
				user.Username = "bob"
				err = repo.CreateLocalUser(ctx, user)
			case "session":
				session.ID = "s2"
				session.PublicID = "p2"
				err = repo.CreateLocalSession(ctx, session, "digest")
			case "PAT":
				token.ID = "t2"
				token.PublicID = "pt2"
				err = repo.CreatePersonalAccessToken(ctx, token, strings.Repeat("b", 64))
			case "login":
				_, err = repo.FindLocalUserByUsername(ctx, "alice")
			case "session lookup":
				_, err = repo.FindLocalSessionByPublicID(ctx, "p1")
			case "PAT lookup":
				_, err = repo.FindPATByPublicID(ctx, "pt1")
			case "tenant exists":
				var exists bool
				exists, err = repo.TenantExists(ctx, "tenant-a")
				if err != nil {
					t.Fatal(err)
				}
				if exists {
					t.Fatal("retired tenant must not accept accounts")
				}
				return
			}
			if err == nil {
				t.Fatalf("retired tenant allowed %s", operation)
			}
		})
	}
}

func TestEnsureIdentityPreservesAdministrativeTenantMetadata(t *testing.T) {
	repo := testRepository(t)
	if err := repo.db.Model(&TenantRecord{}).Where("id = ?", "tenant-a").Updates(map[string]any{"name": "Research Team", "namespace": "custom-research", "local_queue": "research-gpu"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := repo.EnsureIdentity(context.Background(), auth.Principal{Subject: "user-a", TenantID: "tenant-a"}); err != nil {
		t.Fatal(err)
	}
	var tenant TenantRecord
	if err := repo.db.First(&tenant, "id = ?", "tenant-a").Error; err != nil {
		t.Fatal(err)
	}
	if tenant.Name != "Research Team" || tenant.Namespace != "custom-research" || tenant.LocalQueue != "research-gpu" {
		t.Fatalf("login overwrote tenant metadata: %#v", tenant)
	}
}
