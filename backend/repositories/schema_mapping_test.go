package repositories

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"

	"gorm.io/gorm/schema"
)

// The unit tests run against SQLite, which auto-creates whatever columns the
// GORM models declare. PostgreSQL does not: a model field that maps to a
// column the migrations never created fails at runtime with SQLSTATE 42703,
// and only in a real deployment. This test compares every model against the
// migration DDL so that mismatch is caught here instead.
func TestModelColumnsExistInMigrations(t *testing.T) {
	tables := loadMigrationTables(t)

	models := []any{
		&JobRecord{}, &OutboxRecord{}, &TenantRecord{}, &UserRecord{},
		&WorkspaceRecord{}, &IdempotencyRecord{}, &PersonalAccessTokenRecord{},
		&LocalUserRecord{}, &LocalSessionRecord{}, &AuditLogRecord{}, &MLflowDashboardTicketRecord{}, &PlatformImageRecord{},
		&DataMountBindingRecord{}, &GitCredentialRecord{}, &WorkspaceSnapshotRecord{},
		&IDCConnectionRecord{}, &DataTransferRecord{},
		&ManagedAttemptResourceRecord{}, &SourceArtifactRecord{}, &SourceArtifactRequestRecord{},
	}

	namer := schema.NamingStrategy{}
	for _, model := range models {
		parsed, err := schema.Parse(model, &sync.Map{}, namer)
		if err != nil {
			t.Fatalf("parse model %T: %v", model, err)
		}
		columns, ok := tables[parsed.Table]
		if !ok {
			t.Fatalf("model %T maps to table %q which no migration creates", model, parsed.Table)
		}
		for _, field := range parsed.Fields {
			if field.DBName == "" || field.IgnoreMigration {
				continue
			}
			if _, exists := columns[field.DBName]; !exists {
				t.Errorf("model %T field %s maps to column %q.%q, which the migrations do not create (have: %s)",
					model, field.Name, parsed.Table, field.DBName, strings.Join(sortedKeys(columns), ", "))
			}
		}
	}
}

func TestJobRecordDatasetProvenanceUsesExplicitNullableColumns(t *testing.T) {
	recordType := reflect.TypeOf(JobRecord{})
	parsed, err := schema.Parse(&JobRecord{}, &sync.Map{}, schema.NamingStrategy{})
	if err != nil {
		t.Fatalf("parse JobRecord: %v", err)
	}

	stringPointer := reflect.TypeOf((*string)(nil))
	for _, expected := range []struct {
		field  string
		column string
	}{
		{field: "DatasetID", column: "dataset_id"},
		{field: "DatasetVersionID", column: "dataset_version_id"},
		{field: "DatasetManifestDigest", column: "dataset_manifest_digest"},
		{field: "DatasetDataMode", column: "dataset_data_mode"},
		{field: "DatasetCachePolicy", column: "dataset_cache_policy"},
	} {
		field, exists := recordType.FieldByName(expected.field)
		if !exists {
			t.Errorf("JobRecord is missing %s", expected.field)
			continue
		}
		if field.Type != stringPointer {
			t.Errorf("JobRecord.%s type = %s, want *string", expected.field, field.Type)
		}
		if tag := field.Tag.Get("gorm"); tag != "column:"+expected.column {
			t.Errorf("JobRecord.%s gorm tag = %q, want explicit column:%s", expected.field, tag, expected.column)
		}
		parsedField := parsed.LookUpField(expected.field)
		if parsedField == nil || parsedField.DBName != expected.column {
			got := "<missing>"
			if parsedField != nil {
				got = parsedField.DBName
			}
			t.Errorf("JobRecord.%s database column = %q, want %q", expected.field, got, expected.column)
		}
	}
}

var (
	createTablePattern = regexp.MustCompile(`(?is)CREATE TABLE (?:IF NOT EXISTS )?([a-z_]+)\s*\((.*?)\n\);`)
	addColumnPattern   = regexp.MustCompile(`(?is)ALTER TABLE\s+([a-z_]+)\s+ADD COLUMN (?:IF NOT EXISTS )?([a-z_]+)`)
)

// loadMigrationTables builds table -> column set from the migration SQL. It is
// intentionally simple: the migrations are plain DDL with one column per line.
func loadMigrationTables(t *testing.T) map[string]map[string]struct{} {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join("..", "db", "migrations", "*.up.sql"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("no migrations found: %v", err)
	}
	sort.Strings(paths)

	tables := make(map[string]map[string]struct{})
	for _, path := range paths {
		contents, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatalf("read %s: %v", path, readErr)
		}
		sql := string(contents)

		for _, match := range createTablePattern.FindAllStringSubmatch(sql, -1) {
			table := match[1]
			if _, exists := tables[table]; !exists {
				tables[table] = make(map[string]struct{})
			}
			for _, line := range strings.Split(match[2], "\n") {
				line = strings.TrimSpace(line)
				if line == "" || strings.HasPrefix(line, "--") {
					continue
				}
				name := strings.ToLower(strings.Fields(line)[0])
				// Skip table-level constraint clauses.
				switch name {
				case "primary", "unique", "foreign", "constraint", "check":
					continue
				}
				tables[table][strings.Trim(name, `",`)] = struct{}{}
			}
		}

		for _, match := range addColumnPattern.FindAllStringSubmatch(sql, -1) {
			table, column := match[1], match[2]
			if _, exists := tables[table]; !exists {
				tables[table] = make(map[string]struct{})
			}
			tables[table][column] = struct{}{}
		}
	}
	return tables
}

func sortedKeys(values map[string]struct{}) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
