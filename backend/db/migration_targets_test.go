package db

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"testing"
)

var (
	createTablePattern = regexp.MustCompile(`(?i)CREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?([A-Za-z_][A-Za-z0-9_]*)`)
	alterTablePattern  = regexp.MustCompile(`(?i)ALTER\s+TABLE\s+(?:IF\s+EXISTS\s+)?(?:ONLY\s+)?([A-Za-z_][A-Za-z0-9_]*)`)
	createIndexPattern = regexp.MustCompile(`(?i)CREATE\s+(?:UNIQUE\s+)?INDEX\s+(?:CONCURRENTLY\s+)?(?:IF\s+NOT\s+EXISTS\s+)?[A-Za-z0-9_]+\s+ON\s+(?:ONLY\s+)?([A-Za-z_][A-Za-z0-9_]*)`)
)

// A migration naming a table that does not exist only fails at startup, in the
// target environment, after the image is built and rolled out: the backend
// crash-loops on ApplyMigrations. This catches the mistake at test time
// without needing a live PostgreSQL.
//
// It caught exactly that: 0014 was written against `jobs`, while the table is
// `training_jobs`.
func TestMigrationsOnlyTouchTablesEarlierMigrationsCreate(t *testing.T) {
	versions, err := migrationVersions(migrationFiles)
	if err != nil {
		t.Fatalf("migrationVersions() error = %v", err)
	}
	names, err := migrationFileNames()
	if err != nil {
		t.Fatalf("read migration names: %v", err)
	}
	if len(names) != len(versions) {
		t.Fatalf("expected one file per version, got %d files for %d versions", len(names), len(versions))
	}

	known := map[string]bool{}
	for _, name := range names {
		contents, err := migrationFiles.ReadFile("migrations/" + name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		statements := stripSQLComments(string(contents))
		for _, match := range createTablePattern.FindAllStringSubmatch(statements, -1) {
			known[strings.ToLower(match[1])] = true
		}
		for _, pattern := range []*regexp.Regexp{alterTablePattern, createIndexPattern} {
			for _, match := range pattern.FindAllStringSubmatch(statements, -1) {
				table := strings.ToLower(match[1])
				if !known[table] {
					t.Errorf("%s references table %q, which no migration up to and including this one creates (known: %s)",
						name, table, sortedKeys(known))
				}
			}
		}
	}
}

func migrationFileNames() ([]string, error) {
	entries, err := migrationFiles.ReadDir("migrations")
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".up.sql") {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	return names, nil
}

// stripSQLComments removes `--` line comments so prose describing a former
// table name is not mistaken for a statement.
func stripSQLComments(contents string) string {
	lines := strings.Split(contents, "\n")
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		if index := strings.Index(line, "--"); index >= 0 {
			line = line[:index]
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n")
}

func sortedKeys(values map[string]bool) string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return fmt.Sprint(keys)
}
