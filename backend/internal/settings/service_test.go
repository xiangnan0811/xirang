package settings

import (
	"os"
	"testing"

	"xirang/backend/internal/model"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func setupTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.SystemSetting{}); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestRegistry(t *testing.T) {
	svc := NewService(setupTestDB(t))
	defs := svc.Registry()
	if len(defs) != 14 {
		t.Errorf("expected 14 definitions, got %d", len(defs))
	}
}

func TestGetEffective_Default(t *testing.T) {
	svc := NewService(setupTestDB(t))
	val := svc.GetEffective("login.rate_limit")
	if val != "10" {
		t.Errorf("expected '10', got '%s'", val)
	}
}

func TestGetEffective_EnvOverride(t *testing.T) {
	os.Setenv("LOGIN_RATE_LIMIT", "20")
	defer os.Unsetenv("LOGIN_RATE_LIMIT")
	svc := NewService(setupTestDB(t))
	val := svc.GetEffective("login.rate_limit")
	if val != "20" {
		t.Errorf("expected '20', got '%s'", val)
	}
}

func TestGetEffective_DBOverride(t *testing.T) {
	db := setupTestDB(t)
	svc := NewService(db)
	if err := svc.Update("login.rate_limit", "30"); err != nil {
		t.Fatal(err)
	}
	val := svc.GetEffective("login.rate_limit")
	if val != "30" {
		t.Errorf("expected '30', got '%s'", val)
	}
}

func TestUpdate_Invalid(t *testing.T) {
	svc := NewService(setupTestDB(t))
	if err := svc.Update("login.rate_limit", "abc"); err == nil {
		t.Error("expected error for non-integer value")
	}
	if err := svc.Update("unknown.key", "1"); err == nil {
		t.Error("expected error for unknown key")
	}
}

func TestDelete(t *testing.T) {
	db := setupTestDB(t)
	svc := NewService(db)
	_ = svc.Update("login.rate_limit", "30")
	if err := svc.Delete("login.rate_limit"); err != nil {
		t.Fatal(err)
	}
	val := svc.GetEffective("login.rate_limit")
	if val != "10" {
		t.Errorf("expected default '10' after delete, got '%s'", val)
	}
}

func TestGetAll(t *testing.T) {
	db := setupTestDB(t)
	svc := NewService(db)
	_ = svc.Update("login.rate_limit", "25")
	all, err := svc.GetAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 14 {
		t.Errorf("expected 14 settings, got %d", len(all))
	}
	if all["login.rate_limit"].Source != "db" {
		t.Errorf("expected source 'db', got '%s'", all["login.rate_limit"].Source)
	}
	if all["login.rate_limit"].Value != "25" {
		t.Errorf("expected '25', got '%s'", all["login.rate_limit"].Value)
	}
}
