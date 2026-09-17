package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	"xirang/backend/internal/model"
	"xirang/backend/internal/secure"

	"github.com/pquerna/otp/totp"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func openIdentitySQLiteTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "FAKE_IDENTITY_DATA_ENCRYPTION_KEY_32_BYTES")
	secure.ResetForTesting()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared&_loc=UTC"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open SQLite test database: %v", err)
	}
	if err := db.AutoMigrate(&model.User{}, &model.PendingAuthToken{}, &model.TokenRevocation{}); err != nil {
		t.Fatalf("migrate SQLite test database: %v", err)
	}
	return db
}

func TestComplete2FALoginConsumesPendingJTIOnce(t *testing.T) {
	db := openIdentitySQLiteTestDB(t)
	key, err := GenerateTOTPSecret("test", "admin")
	if err != nil {
		t.Fatalf("generate TOTP key: %v", err)
	}
	user := model.User{Username: "admin", Role: "admin", PasswordHash: "hash", TOTPSecret: key.Secret(), TOTPEnabled: true}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create TOTP user: %v", err)
	}
	manager := NewJWTManager("test-secret", time.Hour)
	service := NewService(db, manager, nil, LoginSecurityConfig{})
	pending, err := manager.Generate2FAPendingToken(user)
	if err != nil {
		t.Fatalf("generate pending token: %v", err)
	}
	code, err := totp.GenerateCode(key.Secret(), time.Now())
	if err != nil {
		t.Fatalf("generate TOTP code: %v", err)
	}
	if _, err := service.Complete2FALogin(context.Background(), pending, code); err != nil {
		t.Fatalf("first pending completion: %v", err)
	}
	if _, err := service.Complete2FALogin(context.Background(), pending, code); !errors.Is(err, ErrPendingLoginInvalid) {
		t.Fatalf("replayed pending token should be rejected, got %v", err)
	}
	var row model.PendingAuthToken
	if err := db.Where("user_id = ?", user.ID).First(&row).Error; err != nil {
		t.Fatalf("load consumed pending token: %v", err)
	}
	if row.ConsumedAt == nil {
		t.Fatalf("successful completion must persist consumed_at")
	}
}
func TestDisableTOTPPreservesOpaquePasswordWhitespace(t *testing.T) {
	db := openIdentitySQLiteTestDB(t)
	key, err := GenerateTOTPSecret("test", "admin")
	if err != nil {
		t.Fatalf("generate TOTP key: %v", err)
	}
	password := " ValidPass#2026 "
	passwordHash, err := HashPassword(password)
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	user := model.User{
		Username:     "disable-totp-admin",
		Role:         "admin",
		PasswordHash: passwordHash,
		TOTPSecret:   key.Secret(),
		TOTPEnabled:  true,
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create TOTP user: %v", err)
	}
	service := NewService(db, NewJWTManager("test-secret", time.Hour), nil, LoginSecurityConfig{})
	code, err := totp.GenerateCode(key.Secret(), time.Now())
	if err != nil {
		t.Fatalf("generate TOTP code: %v", err)
	}
	if err := service.DisableTOTP(context.Background(), user.ID, password, code); err != nil {
		t.Fatalf("disable TOTP with exact password: %v", err)
	}
	var stored model.User
	if err := db.First(&stored, user.ID).Error; err != nil {
		t.Fatalf("load disabled user: %v", err)
	}
	if stored.TOTPEnabled {
		t.Fatal("DisableTOTP should clear TOTP state")
	}
}
