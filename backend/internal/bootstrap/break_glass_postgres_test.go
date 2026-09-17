package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"sync"
	"testing"
	"time"

	"xirang/backend/internal/model"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func openBreakGlassPostgresTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	rawDSN := os.Getenv("TEST_POSTGRES_DSN")
	if rawDSN == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	parsed, err := url.Parse(rawDSN)
	if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") {
		t.Fatalf("parse TEST_POSTGRES_DSN: %v", err)
	}
	schema := fmt.Sprintf("breakglass_test_%d", time.Now().UnixNano())
	root, err := gorm.Open(postgres.Open(rawDSN), &gorm.Config{})
	if err != nil {
		t.Fatalf("open PostgreSQL root connection: %v", err)
	}
	rootSQL, err := root.DB()
	if err != nil {
		t.Fatalf("get PostgreSQL root connection: %v", err)
	}
	defer func() { _ = rootSQL.Close() }()
	if err := root.Exec(fmt.Sprintf(`CREATE SCHEMA %s`, schema)).Error; err != nil {
		t.Fatalf("create isolated schema: %v", err)
	}
	query := parsed.Query()
	query.Set("options", "-c search_path="+schema)
	parsed.RawQuery = query.Encode()
	db, err := gorm.Open(postgres.Open(parsed.String()), &gorm.Config{})
	if err != nil {
		t.Fatalf("open isolated PostgreSQL connection: %v", err)
	}
	if sqlDB, dbErr := db.DB(); dbErr == nil {
		sqlDB.SetMaxOpenConns(8)
		sqlDB.SetMaxIdleConns(8)
		t.Cleanup(func() { _ = sqlDB.Close() })
	}
	t.Cleanup(func() {
		cleanup, cleanupErr := gorm.Open(postgres.Open(rawDSN), &gorm.Config{})
		if cleanupErr == nil {
			_ = cleanup.Exec(fmt.Sprintf(`DROP SCHEMA %s CASCADE`, schema)).Error
			if cleanupSQL, err := cleanup.DB(); err == nil {
				_ = cleanupSQL.Close()
			}
		}
	})
	if err := db.AutoMigrate(&model.User{}, &model.PendingAuthToken{}, &model.AuditLog{}, &model.BreakGlassAudit{}); err != nil {
		t.Fatalf("migrate isolated PostgreSQL schema: %v", err)
	}
	return db
}

func TestBreakGlassOnlyOneConcurrentZeroAdminPromotionPostgres(t *testing.T) {
	t.Setenv("XIRANG_BREAK_GLASS_CONFIRMATION", "FAKE_BREAK_GLASS_CONFIRMATION_FOR_TEST_ONLY")
	db := openBreakGlassPostgresTestDB(t)
	targets := []model.User{
		{Username: "operator-one", Role: "operator", PasswordHash: "hash"},
		{Username: "operator-two", Role: "operator", PasswordHash: "hash"},
	}
	if err := db.Create(&targets).Error; err != nil {
		t.Fatalf("create targets: %v", err)
	}
	results := make(chan error, len(targets))
	var wg sync.WaitGroup
	for _, target := range targets {
		target := target
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- BreakGlassPromoteAdmin(context.Background(), db, BreakGlassRequest{
				Username:     target.Username,
				Reason:       "restore emergency administrator access",
				Confirmation: "FAKE_BREAK_GLASS_CONFIRMATION_FOR_TEST_ONLY",
			})
		}()
	}
	wg.Wait()
	close(results)

	var successes, existingAdminErrors int
	for err := range results {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrBreakGlassAdminExists):
			existingAdminErrors++
		default:
			t.Fatalf("unexpected concurrent break-glass error: %v", err)
		}
	}
	if successes != 1 || existingAdminErrors != 1 {
		t.Fatalf("expected one promotion and one existing-admin rejection, got successes=%d rejections=%d", successes, existingAdminErrors)
	}
	var adminCount int64
	if err := db.Model(&model.User{}).Where("role = ?", "admin").Count(&adminCount).Error; err != nil {
		t.Fatalf("count admins: %v", err)
	}
	if adminCount != 1 {
		t.Fatalf("expected one promoted admin, got %d", adminCount)
	}
}
