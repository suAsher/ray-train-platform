package api

import (
	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"net/http/httptest"
	"path/filepath"
	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
	"ray-train-platform-backend/repositories"
	"testing"
)

func TestDatasetCleanupRealStoreTenantIsolationAndReadRemoval(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "cleanup.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&repositories.DatasetRecord{}, &repositories.DatasetVersionRecord{}, &repositories.DatasetPublicationRunRecord{}, &repositories.DatasetPublicationPartitionAttemptRecord{}, &repositories.JobRecord{}); err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	t.Cleanup(func() { sqlDB.Close() })
	owner := "team-a"
	for _, dataset := range []repositories.DatasetRecord{
		{ID: "team-data", Slug: "team-data", Name: "Team", OwnerTenantID: &owner, Visibility: "TEAM", SourceSpace: "team-shared", SourceRelativePath: "dataset", SchemaVersion: "s1h-v1"},
		{ID: "public-data", Slug: "public-data", Name: "Public", Visibility: "PUBLIC", SourceSpace: "public", SourceRelativePath: "dataset", SchemaVersion: "s1h-v1"},
	} {
		if err := db.Create(&dataset).Error; err != nil {
			t.Fatal(err)
		}
		version := repositories.DatasetVersionRecord{ID: dataset.ID + "-failed", DatasetID: dataset.ID, Version: "v1", State: "FAILED", SchemaVersion: "s1h-v1"}
		if err := db.Create(&version).Error; err != nil {
			t.Fatal(err)
		}
	}
	handler := NewHandler(nil, Options{Datasets: repositories.NewGormRepository(db)})
	serve := func(tenant, role, method, path string, want int) {
		t.Helper()
		router := gin.New()
		router.Use(func(c *gin.Context) {
			c.Set("ray-platform-principal", auth.Principal{Subject: "admin", TenantID: tenant, Roles: []string{role}, AuthType: auth.AuthTypeLocal})
		})
		handler.RegisterDatasetRoutes(router.Group("/api/v1"))
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(method, "/api/v1"+path, nil))
		if w.Code != want {
			t.Fatalf("%s %s %s %s got%d want%d: %s", tenant, role, method, path, w.Code, want, w.Body.String())
		}
	}
	serve("team-b", domain.RoleTenantAdmin, "DELETE", "/datasets/team-data/versions/team-data-failed", 404)
	serve("team-a", domain.RoleTenantAdmin, "DELETE", "/datasets/public-data/versions/public-data-failed", 404)
	serve("team-a", domain.RoleEngineer, "DELETE", "/datasets/team-data/versions/team-data-failed", 403)
	serve("team-a", domain.RoleTenantAdmin, "DELETE", "/datasets/team-data/versions/team-data-failed", 200)
	serve("team-a", domain.RoleTenantAdmin, "GET", "/datasets/team-data/versions/team-data-failed", 404)
	serve("team-b", domain.RoleSuperAdmin, "DELETE", "/datasets/public-data/versions/public-data-failed", 200)
}
