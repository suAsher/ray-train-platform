package repositories

import (
	"context"
	"testing"

	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
)

func gitCredentialRepo(t *testing.T) *GormRepository {
	t.Helper()
	repo := testRepository(t)
	if err := repo.db.AutoMigrate(&GitCredentialRecord{}); err != nil {
		t.Fatalf("migrate git credentials: %v", err)
	}
	if err := repo.EnsureIdentity(context.Background(), auth.Principal{TenantID: "team-a", Subject: "user-a", Username: "user-a", Roles: []string{domain.RoleEngineer}}); err != nil {
		t.Fatalf("ensure user-a: %v", err)
	}
	if err := repo.EnsureIdentity(context.Background(), auth.Principal{TenantID: "team-a", Subject: "user-b", Username: "user-b", Roles: []string{domain.RoleEngineer}}); err != nil {
		t.Fatalf("ensure user-b: %v", err)
	}
	return repo
}

func TestGitCredentialLookupPrefersPersonalCredentialAndKeepsUsersIsolated(t *testing.T) {
	repo := gitCredentialRepo(t)
	ctx := context.Background()
	team := domain.GitCredential{ID: "team", TenantID: "team-a", Scope: domain.GitCredentialScopeTeam, Name: "team Git", Host: "git.example.com", SecretName: "team-secret"}
	personal := domain.GitCredential{ID: "personal-a", TenantID: "team-a", Scope: domain.GitCredentialScopePersonal, OwnerUserID: "user-a", Name: "user-a Git", Host: "git.example.com", SecretName: "user-a-secret"}
	if err := repo.UpsertGitCredential(ctx, team); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpsertGitCredential(ctx, personal); err != nil {
		t.Fatal(err)
	}

	resolvedA, err := repo.GitCredentialForURL(ctx, "team-a", "user-a", "https://git.example.com/team/private-repo.git")
	if err != nil || resolvedA.SecretName != "user-a-secret" || resolvedA.Scope != domain.GitCredentialScopePersonal {
		t.Fatalf("user-a must receive their own credential first: credential=%+v err=%v", resolvedA, err)
	}
	resolvedB, err := repo.GitCredentialForURL(ctx, "team-a", "user-b", "https://git.example.com/team/private-repo.git")
	if err != nil || resolvedB.SecretName != "team-secret" || resolvedB.Scope != domain.GitCredentialScopeTeam {
		t.Fatalf("other users must fall back only to the team credential: credential=%+v err=%v", resolvedB, err)
	}
	personalForB, err := repo.ListGitCredentials(ctx, "team-a", "user-b", domain.GitCredentialScopePersonal)
	if err != nil || len(personalForB) != 0 {
		t.Fatalf("user-b must not list user-a's personal credential: credentials=%+v err=%v", personalForB, err)
	}
}
