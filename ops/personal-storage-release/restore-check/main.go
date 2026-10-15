// Run only against the isolated, password-free production snapshot restore.
package main

import (
	"encoding/json"
	"os"
	"regexp"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"ray-train-platform-backend/db"
)

const restoreDSN = "host=127.0.0.1 port=5432 user=postgres dbname=raytrain_restore sslmode=disable"

func main() {
	if os.Getenv("RESTORE_CHECK_DSN") != restoreDSN {
		fail("restore-only-dsn")
	}
	database, err := gorm.Open(postgres.Open(restoreDSN), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		fail("open-restore")
	}
	var before int
	if database.Raw("SELECT max(version) FROM schema_migrations").Scan(&before).Error != nil || before != 58 {
		fail("expected-schema-58")
	}
	var tables []string
	if database.Raw("SELECT table_name FROM information_schema.tables WHERE table_schema='public' AND table_type='BASE TABLE' AND table_name <> 'schema_migrations'").Scan(&tables).Error != nil {
		fail("table-inventory")
	}
	counts := make(map[string]int64, len(tables))
	identifier := regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	for _, table := range tables {
		if !identifier.MatchString(table) {
			fail("table-identifier")
		}
		var count int64
		if database.Raw(`SELECT count(*) FROM "`+table+`"`).Scan(&count).Error != nil {
			fail("before-count")
		}
		counts[table] = count
	}
	for attempt := 0; attempt < 2; attempt++ {
		if db.ApplyMigrations(database) != nil {
			fail("apply-migrations")
		}
	}
	var after int
	if database.Raw("SELECT max(version) FROM schema_migrations").Scan(&after).Error != nil || after != 59 {
		fail("expected-schema-59")
	}
	for table, beforeCount := range counts {
		var afterCount int64
		if database.Raw(`SELECT count(*) FROM "`+table+`"`).Scan(&afterCount).Error != nil || afterCount != beforeCount {
			fail("old-row-count-changed")
		}
	}
	var bindings int64
	if database.Table("wellspiking_personal_bindings").Count(&bindings).Error != nil || bindings != 0 {
		fail("new-binding-table")
	}
	_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"ok": true, "schemaBefore": before, "schemaAfter": after, "repeatApply": true, "oldRowCountsPreserved": true, "newBindingsEmpty": true})
}

func fail(stage string) {
	_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"ok": false, "stage": stage})
	os.Exit(1)
}
