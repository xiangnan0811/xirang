package auth

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

	"github.com/pquerna/otp/totp"
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
	query.Set("search_path", schema)
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

func TestTOTPActivationSessionPostgres(t *testing.T) {
	db := openIdentityPostgresTestDB(t)
	user := model.User{
		Username:     "activation-postgres-admin",
		Role:         "admin",
		PasswordHash: "FAKE_PASSWORD_HASH_FOR_TEST_ONLY",
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create activation user: %v", err)
	}
	manager := NewJWTManager("FAKE_JWT_SECRET_FOR_TEST_ONLY", time.Hour)
	manager.SetDB(db)
	service := NewService(db, manager, nil, LoginSecurityConfig{})

	oldToken, err := manager.GenerateToken(user)
	if err != nil {
		t.Fatalf("generate original token: %v", err)
	}
	oldClaims, err := manager.ParseToken(oldToken)
	if err != nil || oldClaims.ExpiresAt == nil {
		t.Fatalf("parse original token: claims=%+v err=%v", oldClaims, err)
	}
	setup, err := service.SetupTOTP(context.Background(), user.ID, user.Username)
	if err != nil {
		t.Fatalf("setup TOTP: %v", err)
	}
	var pending model.User
	if err := db.First(&pending, user.ID).Error; err != nil {
		t.Fatalf("load pending user: %v", err)
	}
	code, err := totp.GenerateCode(pending.TOTPSecret, time.Now())
	if err != nil {
		t.Fatalf("generate TOTP code: %v", err)
	}
	session := TOTPActivationSession{
		JTI:          oldClaims.ID,
		UserID:       oldClaims.UserID,
		Role:         oldClaims.Role,
		TokenVersion: oldClaims.TokenVersion,
		ExpiresAt:    oldClaims.ExpiresAt.Time,
	}
	result, err := service.VerifyTOTP(context.Background(), session, code, setup.EnrollmentID)
	if err != nil || result == nil {
		t.Fatalf("activate TOTP: result=%+v err=%v", result, err)
	}
	if len(result.RecoveryCodes) == 0 || !result.User.TOTPEnabled {
		t.Fatalf("activation result missing safe user/recovery codes: %+v", result)
	}

	newClaims, err := manager.ParseToken(result.Token)
	if err != nil || newClaims.ExpiresAt == nil {
		t.Fatalf("parse replacement token: claims=%+v err=%v", newClaims, err)
	}
	if newClaims.ID != oldClaims.ID || !newClaims.ExpiresAt.Equal(oldClaims.ExpiresAt.Time) ||
		newClaims.TokenVersion != oldClaims.TokenVersion+1 ||
		newClaims.Purpose != "" || newClaims.StepUpAction != "" ||
		newClaims.SessionID != "" || newClaims.TOTPBinding != "" {
		t.Fatalf("replacement token claims mismatch: old=%+v new=%+v", oldClaims, newClaims)
	}
	var stored model.User
	if err := db.First(&stored, user.ID).Error; err != nil {
		t.Fatalf("load activated user: %v", err)
	}
	if !stored.TOTPEnabled || stored.TokenVersion != newClaims.TokenVersion {
		t.Fatalf("stored activation state mismatch: %+v claims=%+v", stored, newClaims)
	}
	// The old bearer has the pre-activation version and must fail middleware
	// validation; the replacement carries the committed version.
	if oldClaims.TokenVersion == stored.TokenVersion {
		t.Fatalf("old token version still matches committed user")
	}
	if _, err := service.VerifyTOTP(context.Background(), session, code, setup.EnrollmentID); !errors.Is(err, ErrSecurityConflict) {
		t.Fatalf("stale activation session should fail CAS, got %v", err)
	}

	if err := manager.RevokeSession(oldClaims.ID, oldClaims.UserID, oldClaims.ExpiresAt.Time); err != nil {
		t.Fatalf("revoke activation session: %v", err)
	}
	var revocation model.TokenRevocation
	if err := db.Where("token_hash = ?", "jti:"+oldClaims.ID).First(&revocation).Error; err != nil {
		t.Fatalf("load durable session revocation: %v", err)
	}
	if !revocation.ExpiresAt.Equal(oldClaims.ExpiresAt.Time) {
		t.Fatalf("session revocation expiry extended: got=%s want=%s", revocation.ExpiresAt, oldClaims.ExpiresAt.Time)
	}
	if revoked, err := manager.IsSessionRevoked(oldClaims.ID); err != nil || !revoked {
		t.Fatalf("activation session should remain revoked: revoked=%v err=%v", revoked, err)
	}
	if _, err := manager.ParseToken(oldToken); err == nil {
		t.Fatalf("logout should invalidate old token")
	}
	if _, err := manager.ParseToken(result.Token); err == nil {
		t.Fatalf("logout should invalidate replacement token sharing JTI")
	}
	reloaded := NewJWTManager("FAKE_JWT_SECRET_FOR_TEST_ONLY", time.Hour)
	reloaded.SetDB(db)
	if _, err := reloaded.ParseToken(result.Token); err == nil {
		t.Fatalf("durable logout should invalidate replacement token after manager reload")
	}
}

func TestTOTPActivationLogoutBeforeCommitPostgres(t *testing.T) {
	db := openIdentityPostgresTestDB(t)
	user := model.User{
		Username:     "activation-before-logout-postgres-admin",
		Role:         "admin",
		PasswordHash: "FAKE_PASSWORD_HASH_FOR_TEST_ONLY",
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create activation user: %v", err)
	}
	manager := NewJWTManager("FAKE_JWT_SECRET_FOR_TEST_ONLY", time.Hour)
	manager.SetDB(db)
	service := NewService(db, manager, nil, LoginSecurityConfig{})

	oldToken, err := manager.GenerateToken(user)
	if err != nil {
		t.Fatalf("generate original token: %v", err)
	}
	oldClaims, err := manager.ParseToken(oldToken)
	if err != nil || oldClaims.ExpiresAt == nil {
		t.Fatalf("parse original token: claims=%+v err=%v", oldClaims, err)
	}
	setup, err := service.SetupTOTP(context.Background(), user.ID, user.Username)
	if err != nil {
		t.Fatalf("setup TOTP: %v", err)
	}
	var pending model.User
	if err := db.First(&pending, user.ID).Error; err != nil {
		t.Fatalf("load pending user: %v", err)
	}
	code, err := totp.GenerateCode(pending.TOTPSecret, time.Now())
	if err != nil {
		t.Fatalf("generate TOTP code: %v", err)
	}
	session := TOTPActivationSession{
		JTI:          oldClaims.ID,
		UserID:       oldClaims.UserID,
		Role:         oldClaims.Role,
		TokenVersion: oldClaims.TokenVersion,
		ExpiresAt:    oldClaims.ExpiresAt.Time,
	}

	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseBarrier := func() {
		releaseOnce.Do(func() { close(release) })
	}
	const callbackName = "xirang:test_totp_activation_postgres_barrier"
	if err := db.Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement == nil || tx.Statement.Table != "users" {
			return
		}
		select {
		case entered <- struct{}{}:
		default:
		}
		<-release
	}); err != nil {
		t.Fatalf("register activation barrier: %v", err)
	}
	t.Cleanup(func() {
		releaseBarrier()
		if err := db.Callback().Update().Remove(callbackName); err != nil {
			t.Errorf("remove activation barrier: %v", err)
		}
	})

	activationDone := make(chan struct{})
	var activationResult *TOTPVerifyResult
	var activationErr error
	go func() {
		activationResult, activationErr = service.VerifyTOTP(context.Background(), session, code, setup.EnrollmentID)
		close(activationDone)
	}()

	waitCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	select {
	case <-entered:
	case <-waitCtx.Done():
		releaseBarrier()
		t.Fatalf("activation did not reach pre-commit barrier: %v", waitCtx.Err())
	}

	logoutDone := make(chan error, 1)
	go func() {
		logoutDone <- manager.RevokeSession(oldClaims.ID, oldClaims.UserID, oldClaims.ExpiresAt.Time)
	}()
	select {
	case logoutErr := <-logoutDone:
		if logoutErr != nil {
			releaseBarrier()
			t.Fatalf("logout before activation commit: %v", logoutErr)
		}
	case <-waitCtx.Done():
		releaseBarrier()
		t.Fatalf("logout did not finish before activation release: %v", waitCtx.Err())
	}
	releaseBarrier()

	select {
	case <-activationDone:
	case <-waitCtx.Done():
		t.Fatalf("activation did not finish after barrier release: %v", waitCtx.Err())
	}
	if activationErr != nil || activationResult == nil {
		t.Fatalf("activate TOTP after logout commit: result=%+v err=%v", activationResult, activationErr)
	}

	replacementClaims, err := manager.parseToken(activationResult.Token, false)
	if err != nil || replacementClaims.ExpiresAt == nil ||
		replacementClaims.ID != oldClaims.ID ||
		!replacementClaims.ExpiresAt.Equal(oldClaims.ExpiresAt.Time) ||
		replacementClaims.TokenVersion != oldClaims.TokenVersion+1 {
		t.Fatalf("replacement claims changed across pre-commit logout: old=%+v replacement=%+v err=%v", oldClaims, replacementClaims, err)
	}
	if _, err := manager.ParseToken(activationResult.Token); err == nil {
		t.Fatalf("replacement token resurrected revoked JTI")
	}
	reloaded := NewJWTManager("FAKE_JWT_SECRET_FOR_TEST_ONLY", time.Hour)
	reloaded.SetDB(db)
	if _, err := reloaded.ParseToken(activationResult.Token); err == nil {
		t.Fatalf("durable logout did not invalidate replacement token")
	}
	if _, err := manager.ParseToken(oldToken); err == nil {
		t.Fatalf("logout before activation commit should invalidate old token")
	}
	var stored model.User
	if err := db.First(&stored, user.ID).Error; err != nil {
		t.Fatalf("load activated user: %v", err)
	}
	if !stored.TOTPEnabled || stored.TokenVersion != oldClaims.TokenVersion+1 {
		t.Fatalf("activation should commit exactly once: user=%+v", stored)
	}
	if oldClaims.TokenVersion == stored.TokenVersion {
		t.Fatalf("old token version remained valid after activation")
	}
	var revocation model.TokenRevocation
	if err := db.Where("token_hash = ?", "jti:"+oldClaims.ID).First(&revocation).Error; err != nil {
		t.Fatalf("logout revocation did not commit before activation: %v", err)
	}
	if !revocation.ExpiresAt.Equal(oldClaims.ExpiresAt.Time) {
		t.Fatalf("pre-commit logout extended session expiry: got=%s want=%s", revocation.ExpiresAt, oldClaims.ExpiresAt.Time)
	}
}
