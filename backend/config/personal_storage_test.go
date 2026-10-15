package config
import "testing"
func TestPersonalStorageDisabledByDefault(t *testing.T) {
 t.Setenv("PERSONAL_STORAGE_ENABLED", "false")
 cfg, err := loadPersonalStorageConfig()
 if err != nil || cfg.Enabled { t.Fatalf("default personal storage must be disabled: %#v %v", cfg, err) }
}
func TestPersonalStorageRequiresPinnedHostAndDriver(t *testing.T) {
 t.Setenv("PERSONAL_STORAGE_ENABLED", "true")
 if _, err := loadPersonalStorageConfig(); err == nil { t.Fatal("enabled personal storage requires pinned upstream identity and driver") }
}
