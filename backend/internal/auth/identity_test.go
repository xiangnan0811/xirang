package auth

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"xirang/backend/internal/model"
	"xirang/backend/internal/secure"

	"github.com/golang-jwt/jwt/v5"
	"github.com/mattn/go-sqlite3"
	"github.com/pquerna/otp/totp"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

const recoverySQLiteDriverName = "xirang_auth_recovery_code_test"

var (
	recoverySQLiteDriverOnce  sync.Once
	recoverySQLiteDriverState atomic.Pointer[recoverySQLiteConcurrencyState]
)

type recoverySQLiteConcurrencyState struct {
	busyBegin chan struct{}
}

type recoverySQLiteDriver struct{}

func (recoverySQLiteDriver) Open(name string) (driver.Conn, error) {
	conn, err := (&sqlite3.SQLiteDriver{}).Open(name)
	if err != nil {
		return nil, err
	}
	return &recoverySQLiteConn{
		Conn:  conn,
		state: recoverySQLiteDriverState.Load(),
	}, nil
}

type recoverySQLiteConn struct {
	driver.Conn
	state *recoverySQLiteConcurrencyState
}

func (conn *recoverySQLiteConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	tx, err := conn.Conn.(driver.ConnBeginTx).BeginTx(ctx, opts)
	if err != nil && recoverySQLiteBusyLocked(err) && conn.state != nil {
		select {
		case conn.state.busyBegin <- struct{}{}:
		default:
		}
	}
	return tx, err
}

func (conn *recoverySQLiteConn) ExecContext(
	ctx context.Context,
	query string,
	args []driver.NamedValue,
) (driver.Result, error) {
	execer, ok := conn.Conn.(driver.ExecerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	return execer.ExecContext(ctx, query, args)
}

func (conn *recoverySQLiteConn) QueryContext(
	ctx context.Context,
	query string,
	args []driver.NamedValue,
) (driver.Rows, error) {
	queryer, ok := conn.Conn.(driver.QueryerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	return queryer.QueryContext(ctx, query, args)
}

func recoverySQLiteBusyLocked(err error) bool {
	var sqliteErr sqlite3.Error
	if errors.As(err, &sqliteErr) {
		return sqliteErr.Code == sqlite3.ErrBusy || sqliteErr.Code == sqlite3.ErrLocked
	}
	var sqliteErrPointer *sqlite3.Error
	if errors.As(err, &sqliteErrPointer) && sqliteErrPointer != nil {
		return sqliteErrPointer.Code == sqlite3.ErrBusy || sqliteErrPointer.Code == sqlite3.ErrLocked
	}
	var sqliteCode sqlite3.ErrNo
	if errors.As(err, &sqliteCode) {
		return sqliteCode == sqlite3.ErrBusy || sqliteCode == sqlite3.ErrLocked
	}
	var sqliteCodePointer *sqlite3.ErrNo
	if errors.As(err, &sqliteCodePointer) && sqliteCodePointer != nil {
		return *sqliteCodePointer == sqlite3.ErrBusy || *sqliteCodePointer == sqlite3.ErrLocked
	}
	return false
}

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

func openIdentitySQLiteConcurrentTestDB(t *testing.T) (*gorm.DB, *recoverySQLiteConcurrencyState) {
	t.Helper()
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "FAKE_IDENTITY_DATA_ENCRYPTION_KEY_32_BYTES")
	secure.ResetForTesting()
	recoverySQLiteDriverOnce.Do(func() {
		sql.Register(recoverySQLiteDriverName, recoverySQLiteDriver{})
	})
	state := &recoverySQLiteConcurrencyState{busyBegin: make(chan struct{}, 1)}
	recoverySQLiteDriverState.Store(state)
	dsn := fmt.Sprintf(
		"file:%s?_journal_mode=WAL&_busy_timeout=5000&_synchronous=NORMAL&_txlock=immediate&_loc=UTC",
		filepath.Join(t.TempDir(), "identity-concurrent.sqlite"),
	)
	db, err := gorm.Open(sqlite.New(sqlite.Config{
		DriverName: recoverySQLiteDriverName,
		DSN:        dsn,
	}), &gorm.Config{})
	if err != nil {
		t.Fatalf("open concurrent SQLite test database: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get concurrent SQLite test database: %v", err)
	}
	sqlDB.SetMaxOpenConns(4)
	sqlDB.SetMaxIdleConns(4)
	t.Cleanup(func() {
		_ = sqlDB.Close()
		recoverySQLiteDriverState.Store(nil)
	})
	if err := db.AutoMigrate(&model.User{}, &model.PendingAuthToken{}, &model.TokenRevocation{}); err != nil {
		t.Fatalf("migrate concurrent SQLite test database: %v", err)
	}
	return db, state
}

func TestComplete2FALoginRecoveryCodeConcurrentSQLite(t *testing.T) {
	db, sqliteState := openIdentitySQLiteConcurrentTestDB(t)
	runConcurrentRecoveryCodeLoginTest(t, db, sqliteState)
}

func runConcurrentRecoveryCodeLoginTest(t *testing.T, db *gorm.DB, sqliteState *recoverySQLiteConcurrencyState) {
	t.Helper()
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "FAKE_IDENTITY_DATA_ENCRYPTION_KEY_32_BYTES")
	secure.ResetForTesting()

	key, err := GenerateTOTPSecret("test", "recovery-concurrent-admin")
	if err != nil {
		t.Fatalf("generate TOTP key: %v", err)
	}
	recoveryCodes := []string{"ABCD2345", "EFGH6789"}
	// Use real bcrypt hashes at MinCost so -race scheduling exercises the
	// transaction race without making a production bcrypt-cost or timeout guarantee.
	hashedRecoveryCodes := make([]string, len(recoveryCodes))
	for i, recoveryCode := range recoveryCodes {
		hash, err := bcrypt.GenerateFromPassword([]byte(recoveryCode), bcrypt.MinCost)
		if err != nil {
			t.Fatalf("hash recovery code: %v", err)
		}
		hashedRecoveryCodes[i] = string(hash)
	}
	recoveryJSON, err := json.Marshal(hashedRecoveryCodes)
	if err != nil {
		t.Fatalf("marshal recovery codes: %v", err)
	}
	user := model.User{
		Username:      "recovery-concurrent-admin",
		Role:          "admin",
		PasswordHash:  "FAKE_PASSWORD_HASH_FOR_TEST_ONLY",
		TOTPSecret:    key.Secret(),
		TOTPEnabled:   true,
		RecoveryCodes: string(recoveryJSON),
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create recovery-code user: %v", err)
	}
	manager := NewJWTManager("FAKE_JWT_SECRET_FOR_TEST_ONLY", time.Hour)
	service := NewService(db, manager, nil, LoginSecurityConfig{})
	firstPending, err := manager.Generate2FAPendingToken(user)
	if err != nil {
		t.Fatalf("generate first pending token: %v", err)
	}
	secondPending, err := manager.Generate2FAPendingToken(user)
	if err != nil {
		t.Fatalf("generate second pending token: %v", err)
	}
	if firstPending == secondPending {
		t.Fatal("concurrent recovery-code test requires distinct pending tokens")
	}

	attempts := []struct {
		pending string
		code    string
	}{
		{pending: firstPending, code: "abcd2345"},
		{pending: secondPending, code: "AbCd2345"},
	}
	winnerReached := make(chan int, 1)
	releaseWinner := make(chan struct{})
	var releaseWinnerOnce sync.Once
	releaseWinnerAndRemember := func() {
		releaseWinnerOnce.Do(func() { close(releaseWinner) })
	}
	var winnerOnce sync.Once
	callbackName := fmt.Sprintf("test:auth-recovery-code-barrier-%d", time.Now().UnixNano())
	if err := db.Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement == nil || tx.Statement.Table != "users" {
			return
		}
		winnerOnce.Do(func() {
			var winnerPID int
			if tx.Name() == "postgres" {
				if err := tx.Statement.ConnPool.QueryRowContext(tx.Statement.Context, "SELECT pg_backend_pid()").Scan(&winnerPID); err != nil {
					// GORM propagates the recorded error through the enclosing transaction.
					_ = tx.AddError(err)
					return
				}
			}
			winnerReached <- winnerPID
			<-releaseWinner
		})
	}); err != nil {
		t.Fatalf("register recovery-code transaction barrier: %v", err)
	}
	t.Cleanup(func() {
		releaseWinnerAndRemember()
		if err := db.Callback().Update().Remove(callbackName); err != nil {
			t.Errorf("remove recovery-code transaction barrier: %v", err)
		}
	})

	type recoveryLoginOutcome struct {
		result *TOTPLoginResult
		err    error
	}
	ready := make(chan struct{}, len(attempts))
	start := make(chan struct{})
	outcomes := make(chan recoveryLoginOutcome, len(attempts))
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	defer func() {
		releaseWinnerAndRemember()
		wg.Wait()
	}()
	for _, attempt := range attempts {
		attempt := attempt
		wg.Add(1)
		go func() {
			defer wg.Done()
			ready <- struct{}{}
			<-start
			result, err := service.Complete2FALogin(ctx, attempt.pending, attempt.code)
			outcomes <- recoveryLoginOutcome{result: result, err: err}
		}()
	}
	for range attempts {
		<-ready
	}
	close(start)

	evidenceCtx, evidenceCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer evidenceCancel()
	var winnerPID int
	select {
	case winnerPID = <-winnerReached:
	case <-evidenceCtx.Done():
		t.Fatalf("timed out waiting for winning recovery-code transaction: %v", evidenceCtx.Err())
	}
	if sqliteState != nil {
		select {
		case <-sqliteState.busyBegin:
		case <-evidenceCtx.Done():
			t.Fatalf("timed out waiting for SQLite BUSY/LOCKED contender evidence: %v", evidenceCtx.Err())
		}
	} else if db.Name() == "postgres" {
		waiterPID := waitForIdentityPostgresLock(t, db, winnerPID)
		if waiterPID == winnerPID {
			t.Fatalf("PostgreSQL waiter PID=%d is the winning transaction PID", waiterPID)
		}
	} else {
		t.Fatalf("recovery-code concurrency test has no lock observer for %s", db.Name())
	}
	releaseWinnerAndRemember()
	wg.Wait()
	close(outcomes)

	var successes, invalidCodes int
	for outcome := range outcomes {
		switch {
		case outcome.err == nil && outcome.result != nil && outcome.result.Token != "":
			successes++
		case errors.Is(outcome.err, ErrRecoveryCodeInvalid):
			invalidCodes++
		default:
			t.Fatalf("unexpected concurrent recovery-code result: result=%v err=%v", outcome.result != nil, outcome.err)
		}
	}
	if successes != 1 || invalidCodes != 1 {
		t.Fatalf("expected one successful login and one invalid recovery-code result, got successes=%d invalid=%d", successes, invalidCodes)
	}

	var stored model.User
	if err := db.First(&stored, user.ID).Error; err != nil {
		t.Fatalf("load recovery-code user after concurrent login: %v", err)
	}
	var storedRecoveryCodes []string
	if err := json.Unmarshal([]byte(stored.RecoveryCodes), &storedRecoveryCodes); err != nil {
		t.Fatalf("decode recovery-code inventory after concurrent login: %v", err)
	}
	if len(storedRecoveryCodes) != 1 || storedRecoveryCodes[0] != hashedRecoveryCodes[1] {
		t.Fatalf("control recovery code inventory changed: count=%d", len(storedRecoveryCodes))
	}

	var pendingRows []model.PendingAuthToken
	if err := db.Where("user_id = ?", user.ID).Find(&pendingRows).Error; err != nil {
		t.Fatalf("load pending tokens after concurrent login: %v", err)
	}
	if len(pendingRows) != len(attempts) {
		t.Fatalf("expected %d pending tokens, got %d", len(attempts), len(pendingRows))
	}
	var consumedPending int
	for _, pending := range pendingRows {
		if pending.ConsumedAt != nil {
			consumedPending++
		}
	}
	if consumedPending != 1 {
		t.Fatalf("expected exactly one consumed pending token, got %d", consumedPending)
	}
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

func TestTOTPActivationSigningFailureRollsBack(t *testing.T) {
	db := openIdentitySQLiteTestDB(t)
	user := model.User{
		Username:     "activation-admin",
		Role:         "admin",
		PasswordHash: "FAKE_PASSWORD_HASH_FOR_TEST_ONLY",
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create activation user: %v", err)
	}
	manager := NewJWTManager("FAKE_JWT_SECRET_FOR_TEST_ONLY", time.Hour)
	service := NewService(db, manager, nil, LoginSecurityConfig{})

	oldToken, err := manager.GenerateToken(user)
	if err != nil {
		t.Fatalf("generate original token: %v", err)
	}
	oldClaims, err := manager.ParseToken(oldToken)
	if err != nil {
		t.Fatalf("parse original token: %v", err)
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

	manager.signTokenFunc = func(*jwt.Token) (string, error) {
		return "", errors.New("FAKE_SIGNING_FAILURE_FOR_TEST_ONLY")
	}
	result, err := service.VerifyTOTP(context.Background(), session, code, setup.EnrollmentID)
	if err == nil || result != nil {
		t.Fatalf("signing failure must not return activation result: result=%+v err=%v", result, err)
	}
	var rolledBack model.User
	if err := db.First(&rolledBack, user.ID).Error; err != nil {
		t.Fatalf("reload user after signing failure: %v", err)
	}
	if rolledBack.TOTPEnabled || rolledBack.TokenVersion != oldClaims.TokenVersion ||
		rolledBack.TOTPEnrollmentID != setup.EnrollmentID || rolledBack.RecoveryCodes != "" {
		t.Fatalf("signing failure committed activation state: %+v", rolledBack)
	}

	manager.signTokenFunc = nil
	result, err = service.VerifyTOTP(context.Background(), session, code, setup.EnrollmentID)
	if err != nil || result == nil {
		t.Fatalf("activation after signing recovery failed: result=%+v err=%v", result, err)
	}
	newClaims, err := manager.ParseToken(result.Token)
	if err != nil {
		t.Fatalf("parse replacement token: %v", err)
	}
	if newClaims.ID != oldClaims.ID || newClaims.ExpiresAt == nil ||
		!newClaims.ExpiresAt.Equal(oldClaims.ExpiresAt.Time) ||
		newClaims.TokenVersion != oldClaims.TokenVersion+1 ||
		newClaims.Purpose != "" || newClaims.StepUpAction != "" ||
		newClaims.SessionID != "" || newClaims.TOTPBinding != "" {
		t.Fatalf("replacement token claims do not preserve primary session semantics: old=%+v new=%+v", oldClaims, newClaims)
	}
}
