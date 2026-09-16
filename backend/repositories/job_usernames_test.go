package repositories

import (
	"context"
	"strings"
	"testing"

	"gorm.io/gorm"
)

type jobUsernameReader interface {
	JobUsernames(context.Context, []string) (map[string]string, error)
}

func requireJobUsernameReader(t *testing.T, repo *GormRepository) jobUsernameReader {
	t.Helper()
	reader, ok := any(repo).(jobUsernameReader)
	if !ok {
		t.Fatal("repository must provide an optional safe batch display-name lookup")
	}
	return reader
}

func TestJobUsernamesUsesOneSafeBoundedQuery(t *testing.T) {
	repo := testRepository(t)
	for _, user := range []UserRecord{
		{ID: "user-alice", OIDCSubject: "oidc-alice", Username: "alice", TenantID: "tenant-a", Email: "private-alice@example.com", RolesJSON: `["Engineer"]`},
		// Stable task ownership must survive the author's current-team change.
		{ID: "user-bob", OIDCSubject: "oidc-bob", Username: "bob", TenantID: "team-a", Email: "private-bob@example.com", RolesJSON: `["TenantAdmin"]`},
		{ID: "unrelated-user", OIDCSubject: "oidc-unrelated", Username: "unrelated", TenantID: "tenant-a"},
	} {
		seedGPUAllocationUser(t, repo, user)
	}
	queryLog := &gpuAllocationQueryLog{}
	reader := requireJobUsernameReader(t, NewGormRepository(repo.db.Session(&gorm.Session{Logger: queryLog})))
	names, err := reader.JobUsernames(context.Background(), []string{"user-bob", "", "user-alice", "user-alice", "missing-user"})
	if err != nil {
		t.Fatalf("read job usernames: %v", err)
	}
	if len(names) != 2 || names["user-alice"] != "alice" || names["user-bob"] != "bob" {
		t.Fatalf("unexpected display names or unrelated user disclosure: %+v", names)
	}
	if queryLog.selectCount("users") != 1 {
		t.Fatalf("expected one bounded batch query: %v", queryLog.statements)
	}
	for _, statement := range queryLog.statements {
		if strings.Contains(statement, "FROM `users`") {
			selectClause := strings.Split(statement, " FROM ")[0]
			if strings.Contains(selectClause, "*") || strings.Contains(selectClause, "email") || strings.Contains(selectClause, "roles") || !strings.Contains(selectClause, "username") {
				t.Fatalf("lookup reads more than public display fields: %s", statement)
			}
			if !strings.Contains(statement, "id IN") || strings.Contains(statement, "unrelated-user") || strings.Count(statement, "user-alice") != 1 {
				t.Fatalf("lookup must constrain unique IDs from returned jobs: %s", statement)
			}
		}
	}
}

func TestJobUsernamesEmptyResultDoesNotReadUserDirectory(t *testing.T) {
	repo := testRepository(t)
	queryLog := &gpuAllocationQueryLog{}
	reader := requireJobUsernameReader(t, NewGormRepository(repo.db.Session(&gorm.Session{Logger: queryLog})))
	names, err := reader.JobUsernames(context.Background(), []string{"", ""})
	if err != nil || len(names) != 0 || queryLog.selectCount("users") != 0 {
		t.Fatalf("empty result queried user directory: names=%v error=%v queries=%v", names, err, queryLog.statements)
	}
}

func TestJobUsernamesReturnsLookupErrorToDisplayCaller(t *testing.T) {
	repo := testRepository(t)
	reader := requireJobUsernameReader(t, repo)
	if err := repo.db.Migrator().DropTable(&UserRecord{}); err != nil {
		t.Fatal(err)
	}
	if _, err := reader.JobUsernames(context.Background(), []string{"user-alice"}); err == nil {
		t.Fatal("lookup must report failures for the API caller to log and degrade")
	}
}
