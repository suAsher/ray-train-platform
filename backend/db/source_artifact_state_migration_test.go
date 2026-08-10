package db

import (
	"strings"
	"testing"
)

func TestSourceArtifactV1StateMigrationRejectsLegacyRowsBeforeNarrowingConstraint(t *testing.T) {
	contents, err := migrationFiles.ReadFile("migrations/0003_source_artifact_v1_states.up.sql")
	if err != nil {
		t.Fatalf("read migration 3: %v", err)
	}
	sql := string(contents)
	required := []string{
		"state NOT IN ('PENDING', 'READY')",
		"RAISE EXCEPTION",
		"source_artifacts_state_check",
		"CHECK (state IN ('PENDING', 'READY'))",
	}
	for _, fragment := range required {
		if !strings.Contains(sql, fragment) {
			t.Errorf("migration 3 missing %q", fragment)
		}
	}
	if strings.Contains(strings.ToUpper(sql), "DELETE FROM SOURCE_ARTIFACTS") || strings.Contains(strings.ToUpper(sql), "UPDATE SOURCE_ARTIFACTS SET STATE") {
		t.Fatal("migration 3 must not silently delete or rewrite legacy states")
	}
	if strings.Index(sql, "RAISE EXCEPTION") > strings.Index(sql, "DROP CONSTRAINT") {
		t.Fatal("legacy state guard must run before dropping the old constraint")
	}
}
