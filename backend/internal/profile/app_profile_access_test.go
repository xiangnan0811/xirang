package profile

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"xirang/backend/internal/model"
	"xirang/backend/internal/secure"
	"xirang/backend/internal/sshutil"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestResolveAppProfileAccessLoadsDecryptedConfig(t *testing.T) {
	db := setupAppProfileAccessTestDB(t)
	if err := db.Create(&model.AppCredential{Name: "mysql-access", Type: "mysql", Config: `{"host":"127.0.0.1","password":"FAKE_APP_PROFILE_PW_FOR_TEST_ONLY"}`}).Error; err != nil {
		t.Fatalf("create credential: %v", err)
	}

	var rawConfig string
	if err := db.Raw("SELECT config FROM app_credentials WHERE id = ?", 1).Scan(&rawConfig).Error; err != nil {
		t.Fatalf("raw config query: %v", err)
	}
	if rawConfig == "" || (!strings.HasPrefix(rawConfig, "enc:v1:") && !strings.HasPrefix(rawConfig, "enc:v2:")) {
		t.Fatalf("config should be encrypted at rest, got %q", rawConfig)
	}

	access, err := ResolveAppProfileAccess(db, 1)
	if err != nil {
		t.Fatalf("ResolveAppProfileAccess: %v", err)
	}
	cfg := access.Config()
	if cfg["password"] != "FAKE_APP_PROFILE_PW_FOR_TEST_ONLY" {
		t.Fatalf("resolved password mismatch: %v", cfg["password"])
	}
	if cfg["host"] != "127.0.0.1" {
		t.Fatalf("resolved host mismatch: %v", cfg["host"])
	}
}

func TestAppProfileAccessSafeMetadata(t *testing.T) {
	access := NewAppProfileAccess(map[string]interface{}{
		"password": "FAKE_METADATA_PW_FOR_TEST_ONLY",
		"host":     "example.internal",
	})

	metadata := access.SafeMetadata()
	if metadata["provider"] != sshutil.CredentialProviderLocal {
		t.Fatalf("provider metadata mismatch: %v", metadata)
	}
	encoded, err := json.Marshal(metadata)
	if err != nil {
		t.Fatalf("marshal metadata: %v", err)
	}
	for _, forbidden := range []string{"FAKE_METADATA_PW_FOR_TEST_ONLY", "example.internal", "password", "config", "credential", "command", "output", "payload"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("metadata exposed forbidden value %q in %s", forbidden, encoded)
		}
	}
}

func TestAppProfileAccessJSONDoesNotExposeConfig(t *testing.T) {
	access := NewAppProfileAccess(map[string]interface{}{
		"password": "FAKE_JSON_PW_FOR_TEST_ONLY",
		"host":     "json.internal",
	})

	encoded, err := json.Marshal(access)
	if err != nil {
		t.Fatalf("marshal access: %v", err)
	}
	for _, forbidden := range []string{"FAKE_JSON_PW_FOR_TEST_ONLY", "json.internal", "password", "host"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("access JSON exposed forbidden value %q in %s", forbidden, encoded)
		}
	}
}

func TestResolveAppProfileAccessInvalidConfigErrorIsSafe(t *testing.T) {
	raw := `{"password":"FAKE_INVALID_PW_FOR_TEST_ONLY","host":"bad.internal"`
	_, err := ResolveAppProfileAccessFromRaw(raw)
	if !errors.Is(err, ErrInvalidAppProfileAccess) {
		t.Fatalf("expected ErrInvalidAppProfileAccess, got %v", err)
	}
	for _, forbidden := range []string{"FAKE_INVALID_PW_FOR_TEST_ONLY", "bad.internal", raw, "password", "host"} {
		if strings.Contains(err.Error(), forbidden) {
			t.Fatalf("error exposed forbidden value %q in %q", forbidden, err.Error())
		}
	}
}

func TestAppProfileAccessReturnsConfigCopy(t *testing.T) {
	access := NewAppProfileAccess(map[string]interface{}{"password": "original"})
	cfg := access.Config()
	cfg["password"] = "mutated"

	cfg = access.Config()
	if cfg["password"] != "original" {
		t.Fatalf("Config should return a copy, got %v", cfg["password"])
	}
}

func TestAppProfileAccessHasPassword(t *testing.T) {
	tests := []struct {
		name   string
		config map[string]interface{}
		expect bool
	}{
		{name: "missing", config: map[string]interface{}{}, expect: false},
		{name: "empty", config: map[string]interface{}{"password": ""}, expect: false},
		{name: "string", config: map[string]interface{}{"password": "FAKE_HAS_PW_FOR_TEST_ONLY"}, expect: true},
		{name: "non string", config: map[string]interface{}{"password": true}, expect: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := NewAppProfileAccess(tc.config).HasPassword(); got != tc.expect {
				t.Fatalf("HasPassword() = %v, want %v", got, tc.expect)
			}
		})
	}
}

func setupAppProfileAccessTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	t.Setenv("APP_ENV", "development")
	secure.ResetForTesting()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get sql db: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	if err := db.AutoMigrate(&model.AppCredential{}); err != nil {
		t.Fatalf("migrate db: %v", err)
	}
	return db
}
