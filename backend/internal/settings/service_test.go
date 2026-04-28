package settings

import (
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
	if len(defs) != 27 {
		t.Errorf("expected 27 definitions, got %d", len(defs))
	}
	// 确认返回副本，不影响全局 registry
	defs[0].Key = "mutated"
	if registry[0].Key == "mutated" {
		t.Error("Registry() should return a copy, not a reference")
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
	t.Setenv("LOGIN_RATE_LIMIT", "20")
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

func TestGetEffective_DBOverridesEnv(t *testing.T) {
	t.Setenv("LOGIN_RATE_LIMIT", "20")
	db := setupTestDB(t)
	svc := NewService(db)
	_ = svc.Update("login.rate_limit", "30")
	val := svc.GetEffective("login.rate_limit")
	if val != "30" {
		t.Errorf("expected DB value '30' to override env '20', got '%s'", val)
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

func TestUpdate_SecurityFloor(t *testing.T) {
	svc := NewService(setupTestDB(t))
	// login.rate_limit Min=5
	if err := svc.Update("login.rate_limit", "2"); err == nil {
		t.Error("expected error: rate_limit below security floor of 5")
	}
	// login.fail_lock_threshold Min=3
	if err := svc.Update("login.fail_lock_threshold", "1"); err == nil {
		t.Error("expected error: lock threshold below security floor of 3")
	}
	// login.rate_window MinDuration=10s
	if err := svc.Update("login.rate_window", "5s"); err == nil {
		t.Error("expected error: rate_window below 10s floor")
	}
	// login.fail_lock_duration MinDuration=1m
	if err := svc.Update("login.fail_lock_duration", "30s"); err == nil {
		t.Error("expected error: lock_duration below 1m floor")
	}
}

func TestUpdate_ValueTooLong(t *testing.T) {
	svc := NewService(setupTestDB(t))
	longVal := make([]byte, maxValueLength+1)
	for i := range longVal {
		longVal[i] = '1'
	}
	if err := svc.Update("login.rate_limit", string(longVal)); err == nil {
		t.Error("expected error for value exceeding max length")
	}
}

func TestValidate_Bool(t *testing.T) {
	svc := NewService(setupTestDB(t))
	if err := svc.Validate("login.captcha_enabled", "true"); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if err := svc.Validate("login.captcha_enabled", "false"); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if err := svc.Validate("login.captcha_enabled", "yes"); err == nil {
		t.Error("expected error for non-bool value")
	}
}

func TestValidate_Duration(t *testing.T) {
	svc := NewService(setupTestDB(t))
	if err := svc.Validate("alert.dedup_window", "5m"); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if err := svc.Validate("alert.dedup_window", "-1m"); err == nil {
		t.Error("expected error for negative duration")
	}
	if err := svc.Validate("alert.dedup_window", "invalid"); err == nil {
		t.Error("expected error for invalid duration")
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
	if len(all) != 27 {
		t.Errorf("expected 27 settings, got %d", len(all))
	}
	if all["login.rate_limit"].Source != "db" {
		t.Errorf("expected source 'db', got '%s'", all["login.rate_limit"].Source)
	}
	if all["login.rate_limit"].Value != "25" {
		t.Errorf("expected '25', got '%s'", all["login.rate_limit"].Value)
	}
}

func TestCache_InvalidatedOnUpdate(t *testing.T) {
	db := setupTestDB(t)
	svc := NewService(db)
	// Prime cache
	val := svc.GetEffective("login.rate_limit")
	if val != "10" {
		t.Fatalf("expected '10', got '%s'", val)
	}
	// Update should invalidate cache
	_ = svc.Update("login.rate_limit", "50")
	val = svc.GetEffective("login.rate_limit")
	if val != "50" {
		t.Errorf("expected '50' after update, got '%s' (cache not invalidated?)", val)
	}
}

func TestCache_InvalidatedOnDelete(t *testing.T) {
	db := setupTestDB(t)
	svc := NewService(db)
	_ = svc.Update("login.rate_limit", "50")
	// Prime cache with DB value
	_ = svc.GetEffective("login.rate_limit")
	// Delete should invalidate cache
	_ = svc.Delete("login.rate_limit")
	val := svc.GetEffective("login.rate_limit")
	if val != "10" {
		t.Errorf("expected default '10' after delete, got '%s' (cache not invalidated?)", val)
	}
}
