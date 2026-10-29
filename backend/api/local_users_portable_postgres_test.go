package api

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	databasepkg "ray-train-platform-backend/db"
	"ray-train-platform-backend/domain"
	"ray-train-platform-backend/repositories"
)

// The tenant write fence and account transaction use separate connections.
// Pin search_path in the connection configuration, rather than on one pooled
// connection, so all writes remain inside the disposable test schema.
func portableAccountsPostgresStore(t *testing.T) *repositories.GormRepository {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("POSTGRES_TEST_DSN"))
	if dsn == "" {
		t.Skip("POSTGRES_TEST_DSN is not set")
	}
	open := func(schema string) *gorm.DB {
		cfg, err := pgx.ParseConfig(dsn)
		if err != nil {
			t.Fatal("invalid PostgreSQL test configuration")
		}
		if schema != "" {
			cfg.RuntimeParams["search_path"] = schema
		}
		pool := stdlib.OpenDB(*cfg)
		pool.SetMaxOpenConns(5)
		t.Cleanup(func() { _ = pool.Close() })
		db, err := gorm.Open(postgres.New(postgres.Config{Conn: pool}), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
		if err != nil {
			t.Fatal("cannot open isolated PostgreSQL test connection")
		}
		return db
	}
	admin := open("")
	schema := fmt.Sprintf("portable_accounts_%d", time.Now().UnixNano())
	if err := admin.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := admin.Exec("DROP SCHEMA " + schema + " CASCADE").Error; err != nil {
			t.Error(err)
		}
	})
	db := open(schema)
	if err := databasepkg.ApplyMigrations(db); err != nil {
		t.Fatal(err)
	}
	return repositories.NewGormRepository(db)
}

func TestPostgresLocalAccountCreationAndLoginWithoutObjectStorage(t *testing.T) {
	store := portableAccountsPostgresStore(t)
	admin := adminPrincipal()
	if err := store.EnsureIdentity(context.Background(), admin); err != nil {
		t.Fatal(err)
	}
	handler := localAuthHandlerWithInitializer(store, NewPersonalDataSpaceInitializer(nil))
	response := postUser(localUserAdminRouter(handler, admin),
		`{"username":"portable-user","password":"engineer-pass","roles":["Engineer"]}`)
	if response.Code != http.StatusCreated {
		t.Fatalf("PVC-only create: %d %s", response.Code, response.Body.String())
	}
	user, err := store.FindLocalUserByUsername(context.Background(), "portable-user")
	if err != nil || user.TenantID != admin.TenantID || len(user.Roles) != 1 || user.Roles[0] != domain.RoleEngineer {
		t.Fatalf("account or tenant membership was not persisted correctly: %v", err)
	}
	login := postLogin(handler, `{"username":"portable-user","password":"engineer-pass"}`)
	if login.Code != http.StatusOK {
		t.Fatalf("new PVC-only user cannot log in: %d %s", login.Code, login.Body.String())
	}
}
