package auth

import (
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

func openIdentityPostgresTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	rawDSN := os.Getenv("TEST_POSTGRES_DSN")
	if rawDSN == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	parsed, err := url.Parse(rawDSN)
	if err != nil || parsed.Scheme != "postgres" && parsed.Scheme != "postgresql" {
		t.Fatalf("parse TEST_POSTGRES_DSN: %v", err)
	}
	schema := fmt.Sprintf("identity_test_%d", time.Now().UnixNano())
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
	if err := db.AutoMigrate(&model.User{}, &model.LoginFailure{}, &model.PendingAuthToken{}, &model.TokenRevocation{}); err != nil {
		t.Fatalf("migrate isolated PostgreSQL schema: %v", err)
	}
	return db
}

func TestLastAdminInvariantConcurrentPostgres(t *testing.T) {
	db := openIdentityPostgresTestDB(t)
	passwordHash, err := HashPassword("Correct1!")
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	admins := []model.User{
		{Username: "admin-one", PasswordHash: passwordHash, Role: "admin"},
		{Username: "admin-two", PasswordHash: passwordHash, Role: "admin"},
	}
	if err := db.Create(&admins).Error; err != nil {
		t.Fatalf("create admins: %v", err)
	}
	service := NewService(db, NewJWTManager("test-secret", time.Hour), nil, LoginSecurityConfig{
		FailLockThreshold: 10,
		FailLockDuration:  time.Minute,
	})

	role := "operator"
	results := make(chan error, len(admins))
	var wg sync.WaitGroup
	for _, admin := range admins {
		admin := admin
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := service.UpdateUser(admin.ID, &role, nil)
			results <- err
		}()
	}
	wg.Wait()
	close(results)

	var successes, lastAdminErrors int
	for err := range results {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrLastAdmin):
			lastAdminErrors++
		default:
			t.Fatalf("unexpected concurrent mutation error: %v", err)
		}
	}
	if successes != 1 || lastAdminErrors != 1 {
		t.Fatalf("expected one successful demotion and one last-admin rejection, got successes=%d rejections=%d", successes, lastAdminErrors)
	}
	var adminCount int64
	if err := db.Model(&model.User{}).Where("role = ?", "admin").Count(&adminCount).Error; err != nil {
		t.Fatalf("count admins: %v", err)
	}
	if adminCount != 1 {
		t.Fatalf("expected one remaining admin, got %d", adminCount)
	}
}
