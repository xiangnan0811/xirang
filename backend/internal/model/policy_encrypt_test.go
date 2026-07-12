package model

import (
	"strings"
	"testing"

	"xirang/backend/internal/secure"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestPolicyEncryptsDrillVerifyScripts(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "FAKE_DATA_ENCRYPTION_KEY_FOR_TEST_ONLY")
	secure.ResetForTesting()

	db, err := gorm.Open(sqlite.Open("file:policy_encrypt?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(&Policy{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	p := Policy{
		Name:            "encrypt-drill",
		SourcePath:      "/src",
		TargetPath:      "/dst",
		CronSpec:        "0 * * * *",
		DrillPreVerify:  "echo pre-secret",
		DrillVerify:     "echo verify-secret",
		DrillPostVerify: "echo post-secret",
		RetentionMode:   "simple",
	}
	if err := db.Create(&p).Error; err != nil {
		t.Fatalf("create: %v", err)
	}

	// Read raw (skip hooks) — must be ciphertext at rest.
	var raw struct {
		Pre  string `gorm:"column:drill_pre_verify"`
		Mid  string `gorm:"column:drill_verify"`
		Post string `gorm:"column:drill_post_verify"`
	}
	if err := db.Session(&gorm.Session{SkipHooks: true}).Table("policies").
		Select("drill_pre_verify, drill_verify, drill_post_verify").
		Where("id = ?", p.ID).Scan(&raw).Error; err != nil {
		t.Fatalf("raw select: %v", err)
	}
	for _, v := range []string{raw.Pre, raw.Mid, raw.Post} {
		if !strings.HasPrefix(v, "enc:") {
			t.Fatalf("expected encrypted at rest, got %q", v)
		}
		if strings.Contains(v, "secret") {
			t.Fatalf("plaintext leaked into storage: %q", v)
		}
	}

	// AfterFind must decrypt for application use.
	var loaded Policy
	if err := db.First(&loaded, p.ID).Error; err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.DrillPreVerify != "echo pre-secret" || loaded.DrillVerify != "echo verify-secret" || loaded.DrillPostVerify != "echo post-secret" {
		t.Fatalf("decrypt mismatch: %+v", loaded)
	}
}
