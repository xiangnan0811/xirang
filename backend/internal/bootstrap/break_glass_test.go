package bootstrap

import (
	"context"
	"errors"
	"testing"
	"time"

	"xirang/backend/internal/model"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func openBreakGlassTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	if err := db.AutoMigrate(&model.User{}, &model.PendingAuthToken{}, &model.AuditLog{}, &model.BreakGlassAudit{}); err != nil {
		t.Fatalf("migrate test database: %v", err)
	}
	return db
}

func TestBreakGlassPromotesAndAuditsAtomically(t *testing.T) {
	t.Setenv("XIRANG_BREAK_GLASS_CONFIRMATION", "FAKE_BREAK_GLASS_CONFIRMATION_FOR_TEST_ONLY")
	db := openBreakGlassTestDB(t)
	user := model.User{Username: "operator", Role: "operator", PasswordHash: "hash"}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create target user: %v", err)
	}
	if err := db.Create(&model.PendingAuthToken{
		JTI:          "0123456789abcdef0123456789abcdef",
		UserID:       user.ID,
		TokenVersion: user.TokenVersion,
		TOTPBinding:  "binding",
		ExpiresAt:    user.CreatedAt.Add(time.Hour),
		CreatedAt:    user.CreatedAt,
	}).Error; err != nil {
		t.Fatalf("create pending token: %v", err)
	}

	err := BreakGlassPromoteAdmin(context.Background(), db, BreakGlassRequest{
		Username:     "operator",
		Reason:       "restore emergency administrator access",
		Confirmation: "FAKE_BREAK_GLASS_CONFIRMATION_FOR_TEST_ONLY",
	})
	if err != nil {
		t.Fatalf("promote target: %v", err)
	}
	var got model.User
	if err := db.First(&got, user.ID).Error; err != nil {
		t.Fatalf("reload target: %v", err)
	}
	if got.Role != "admin" || got.TokenVersion != user.TokenVersion+1 {
		t.Fatalf("expected admin promotion and version bump, got role=%q version=%d", got.Role, got.TokenVersion)
	}
	var pendingCount int64
	if err := db.Model(&model.PendingAuthToken{}).Where("user_id = ?", user.ID).Count(&pendingCount).Error; err != nil {
		t.Fatalf("count pending tokens: %v", err)
	}
	if pendingCount != 0 {
		t.Fatalf("expected pending tokens invalidated, got %d", pendingCount)
	}
	var audit model.AuditLog
	if err := db.Where("path = ?", "break-glass/admin-promote").First(&audit).Error; err != nil {
		t.Fatalf("load audit record: %v", err)
	}
	if audit.UserID != user.ID || audit.Method != "CLI" || audit.EntryHash == "" {
		t.Fatalf("unexpected audit record: %+v", audit)
	}
	var breakGlass model.BreakGlassAudit
	if err := db.Where("target_user_id = ?", user.ID).First(&breakGlass).Error; err != nil {
		t.Fatalf("load break-glass reason: %v", err)
	}
	if breakGlass.Reason != "restore emergency administrator access" {
		t.Fatalf("unexpected break-glass reason: %q", breakGlass.Reason)
	}
}

func TestBreakGlassRequiresProvisionedConfirmation(t *testing.T) {
	t.Setenv("XIRANG_BREAK_GLASS_CONFIRMATION", "expected-confirmation")
	db := openBreakGlassTestDB(t)
	user := model.User{Username: "operator", Role: "operator", PasswordHash: "hash"}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create target user: %v", err)
	}
	err := BreakGlassPromoteAdmin(context.Background(), db, BreakGlassRequest{
		Username:     "operator",
		Reason:       "restore emergency administrator access",
		Confirmation: "wrong-confirmation",
	})
	if !errors.Is(err, ErrBreakGlassConfirmation) {
		t.Fatalf("expected confirmation error, got %v", err)
	}
	var got model.User
	if err := db.First(&got, user.ID).Error; err != nil {
		t.Fatalf("reload target: %v", err)
	}
	if got.Role != "operator" {
		t.Fatalf("invalid confirmation must not promote target")
	}
}

func TestBreakGlassRefusesWhenAnAdministratorExists(t *testing.T) {
	t.Setenv("XIRANG_BREAK_GLASS_CONFIRMATION", "FAKE_BREAK_GLASS_CONFIRMATION_FOR_TEST_ONLY")
	db := openBreakGlassTestDB(t)
	admin := model.User{Username: "existing-admin", Role: "admin", PasswordHash: "hash"}
	target := model.User{Username: "operator", Role: "operator", PasswordHash: "hash"}
	if err := db.Create(&admin).Error; err != nil {
		t.Fatalf("create existing admin: %v", err)
	}
	if err := db.Create(&target).Error; err != nil {
		t.Fatalf("create target user: %v", err)
	}
	err := BreakGlassPromoteAdmin(context.Background(), db, BreakGlassRequest{
		Username:     "operator",
		Reason:       "restore emergency administrator access",
		Confirmation: "FAKE_BREAK_GLASS_CONFIRMATION_FOR_TEST_ONLY",
	})
	if !errors.Is(err, ErrBreakGlassAdminExists) {
		t.Fatalf("expected existing-admin guard, got %v", err)
	}
}
