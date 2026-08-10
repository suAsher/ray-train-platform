package db

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"ray-train-platform-backend/config"
)

const (
	migrationLockID            int64 = 728119034
	migrationLockTimeout             = 30 * time.Second
	migrationLockRetryInterval       = 250 * time.Millisecond
)

var migrationNamePattern = regexp.MustCompile(`^([0-9]{4})_[A-Za-z0-9][A-Za-z0-9._-]*\.up\.sql$`)

//go:embed migrations/*.up.sql
var migrationFiles embed.FS

type migrationFile struct {
	version int
	path    string
}

func Open(cfg config.Config) (*gorm.DB, error) {
	if cfg.DatabaseURL == "" {
		return nil, fmt.Errorf("DATABASE_URL is required")
	}
	database, err := gorm.Open(postgres.Open(cfg.DatabaseURL), &gorm.Config{})
	if err != nil {
		return nil, fmt.Errorf("open postgres: %w", err)
	}
	return database, nil
}

func ApplyMigrations(database *gorm.DB) error {
	if database == nil {
		return fmt.Errorf("database is required")
	}
	if database.Dialector.Name() != "postgres" {
		return applyMigrations(database, migrationFiles)
	}

	lockContext, cancel := context.WithTimeout(context.Background(), migrationLockTimeout)
	defer cancel()
	return database.WithContext(lockContext).Connection(func(contextConnection *gorm.DB) error {
		connection := contextConnection.WithContext(context.Background())
		return withMigrationLock(
			func() error {
				return acquireMigrationLock(lockContext, migrationLockRetryInterval, func(ctx context.Context) (bool, error) {
					var acquired bool
					if err := connection.WithContext(ctx).Raw("SELECT pg_try_advisory_lock(?)", migrationLockID).Scan(&acquired).Error; err != nil {
						return false, err
					}
					return acquired, nil
				})
			},
			func() error {
				return applyMigrations(connection, migrationFiles)
			},
			func() (bool, error) {
				var unlocked bool
				if err := connection.Raw("SELECT pg_advisory_unlock(?)", migrationLockID).Scan(&unlocked).Error; err != nil {
					return false, err
				}
				return unlocked, nil
			},
		)
	})
}

func acquireMigrationLock(
	ctx context.Context,
	retryInterval time.Duration,
	tryAcquire func(context.Context) (bool, error),
) error {
	if retryInterval <= 0 {
		return fmt.Errorf("acquire migration lock: retry interval must be positive")
	}
	for {
		if err := ctx.Err(); err != nil {
			return migrationLockContextError(err)
		}
		acquired, err := tryAcquire(ctx)
		if err != nil {
			if contextErr := ctx.Err(); contextErr != nil {
				return migrationLockContextError(contextErr)
			}
			return fmt.Errorf("acquire migration lock: try advisory lock: %w", err)
		}
		if acquired {
			return nil
		}

		timer := time.NewTimer(retryInterval)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return migrationLockContextError(ctx.Err())
		case <-timer.C:
		}
	}
}

func migrationLockContextError(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("acquire migration lock: timed out waiting for advisory lock: %w", err)
	}
	return fmt.Errorf("acquire migration lock: canceled while waiting for advisory lock: %w", err)
}

func withMigrationLock(
	acquire func() error,
	migrate func() error,
	release func() (bool, error),
) error {
	if err := acquire(); err != nil {
		return fmt.Errorf("acquire migration lock: %w", err)
	}

	migrationErr := migrate()
	unlocked, unlockErr := release()
	var releaseErr error
	if unlockErr != nil {
		releaseErr = fmt.Errorf("release migration lock: %w", unlockErr)
	} else if !unlocked {
		releaseErr = fmt.Errorf("release migration lock: lock was not held by this session")
	}

	if migrationErr != nil && releaseErr != nil {
		return errors.Join(migrationErr, releaseErr)
	}
	if migrationErr != nil {
		return migrationErr
	}
	return releaseErr
}

func migrationVersions(files fs.FS) ([]int, error) {
	migrations, err := discoverMigrations(files)
	if err != nil {
		return nil, err
	}
	versions := make([]int, len(migrations))
	for index, migration := range migrations {
		versions[index] = migration.version
	}
	return versions, nil
}

func discoverMigrations(files fs.FS) ([]migrationFile, error) {
	entries, err := fs.ReadDir(files, "migrations")
	if errors.Is(err, fs.ErrNotExist) {
		return []migrationFile{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read migrations directory: %w", err)
	}

	migrations := make([]migrationFile, 0, len(entries))
	seenVersions := make(map[int]string, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".up.sql") {
			continue
		}

		matches := migrationNamePattern.FindStringSubmatch(entry.Name())
		if matches == nil {
			return nil, fmt.Errorf("invalid up migration filename %q", entry.Name())
		}
		version, err := strconv.Atoi(matches[1])
		if err != nil || version <= 0 {
			return nil, fmt.Errorf("invalid migration version in %q", entry.Name())
		}
		if previous, exists := seenVersions[version]; exists {
			return nil, fmt.Errorf("duplicate migration version %04d in %q and %q", version, previous, entry.Name())
		}

		seenVersions[version] = entry.Name()
		migrations = append(migrations, migrationFile{
			version: version,
			path:    "migrations/" + entry.Name(),
		})
	}

	sort.Slice(migrations, func(left, right int) bool {
		return migrations[left].version < migrations[right].version
	})
	return migrations, nil
}

func applyMigrations(database *gorm.DB, files fs.FS) error {
	if database == nil {
		return fmt.Errorf("database is required")
	}
	migrations, err := discoverMigrations(files)
	if err != nil {
		return err
	}
	if err := database.Exec(`
CREATE TABLE IF NOT EXISTS schema_migrations (
  version BIGINT PRIMARY KEY,
  applied_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
)`).Error; err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	for _, migration := range migrations {
		var appliedCount int64
		if err := database.Raw("SELECT COUNT(*) FROM schema_migrations WHERE version = ?", migration.version).Scan(&appliedCount).Error; err != nil {
			return fmt.Errorf("check migration %04d: %w", migration.version, err)
		}
		if appliedCount > 0 {
			continue
		}

		contents, err := fs.ReadFile(files, migration.path)
		if err != nil {
			return fmt.Errorf("read migration %04d: %w", migration.version, err)
		}
		if err := applyMigration(database, migration.version, string(contents)); err != nil {
			return err
		}
	}
	return nil
}

func applyMigration(database *gorm.DB, version int, statement string) error {
	tx := database.Begin()
	if tx.Error != nil {
		return fmt.Errorf("begin migration %04d: %w", version, tx.Error)
	}
	rollback := func() {
		_ = tx.Rollback().Error
	}

	if err := tx.Exec(statement).Error; err != nil {
		rollback()
		return fmt.Errorf("apply migration %04d: %w", version, err)
	}
	if err := tx.Exec("INSERT INTO schema_migrations(version) VALUES (?)", version).Error; err != nil {
		rollback()
		return fmt.Errorf("record migration %04d: %w", version, err)
	}
	if err := tx.Commit().Error; err != nil {
		rollback()
		return fmt.Errorf("commit migration %04d: %w", version, err)
	}
	return nil
}
