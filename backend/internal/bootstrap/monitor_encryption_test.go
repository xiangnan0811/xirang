package bootstrap

import (
	"fmt"
	"strings"
	"testing"

	"xirang/backend/internal/model"
	"xirang/backend/internal/secure"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func openMonitorEncryptionDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(&model.ServiceMonitor{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func TestEncryptServiceMonitorHeadersBackfillsAndIsIdempotent(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
	secure.ResetForTesting()
	db := openMonitorEncryptionDB(t)
	defer secure.ResetForTesting()

	raw := db.Session(&gorm.Session{SkipHooks: true})
	if err := raw.Create(&model.ServiceMonitor{
		ID: 1, Name: "FAKE_MONITOR_ENCRYPTION_FOR_TEST_ONLY", Type: "http",
		Target: "https://example.invalid", HTTPHeaders: `{"Authorization":"FAKE_SECRET_FOR_TEST_ONLY"}`,
	}).Error; err != nil {
		t.Fatalf("insert plaintext row: %v", err)
	}
	var before string
	if err := raw.Table("service_monitors").Select("http_headers").Where("id = 1").Scan(&before).Error; err != nil {
		t.Fatalf("read plaintext row: %v", err)
	}
	if !strings.Contains(before, "FAKE_SECRET_FOR_TEST_ONLY") {
		t.Fatalf("fixture should start plaintext: %q", before)
	}

	if err := EncryptServiceMonitorHeaders(db); err != nil {
		t.Fatalf("backfill: %v", err)
	}
	var encrypted string
	if err := raw.Table("service_monitors").Select("http_headers").Where("id = 1").Scan(&encrypted).Error; err != nil {
		t.Fatalf("read encrypted row: %v", err)
	}
	if !strings.HasPrefix(encrypted, "enc:v2:") || strings.Contains(encrypted, "FAKE_SECRET_FOR_TEST_ONLY") {
		t.Fatalf("header value not sealed: %q", encrypted)
	}
	if got, err := CountPlaintextServiceMonitorHeaders(db); err != nil || got != 0 {
		t.Fatalf("plaintext count got=%d err=%v", got, err)
	}
	if err := EncryptServiceMonitorHeaders(db); err != nil {
		t.Fatalf("second backfill: %v", err)
	}
	var loaded model.ServiceMonitor
	if err := db.First(&loaded, 1).Error; err != nil {
		t.Fatalf("load decrypted monitor: %v", err)
	}
	if loaded.HTTPHeaders != `{"Authorization":"FAKE_SECRET_FOR_TEST_ONLY"}` {
		t.Fatalf("decrypted header mismatch: %q", loaded.HTTPHeaders)
	}
}

func TestEncryptServiceMonitorHeadersFailsClosedOnUnreadableV1(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
	secure.ResetForTesting()
	db := openMonitorEncryptionDB(t)
	raw := db.Session(&gorm.Session{SkipHooks: true})
	if err := raw.Create(&model.ServiceMonitor{
		ID: 2, Name: "FAKE_MONITOR_BAD_CIPHERTEXT_FOR_TEST_ONLY", Type: "http",
		Target: "https://example.invalid", HTTPHeaders: "enc:v1:not-valid",
	}).Error; err != nil {
		t.Fatalf("insert bad ciphertext: %v", err)
	}
	if err := EncryptServiceMonitorHeaders(db); err == nil {
		t.Fatal("unreadable ciphertext must fail closed")
	}
	var stored string
	if err := raw.Table("service_monitors").Select("http_headers").Where("id = 2").Scan(&stored).Error; err != nil {
		t.Fatalf("read stored ciphertext: %v", err)
	}
	if stored != "enc:v1:not-valid" {
		t.Fatalf("failed transaction should preserve original value: %q", stored)
	}
	secure.ResetForTesting()
}
