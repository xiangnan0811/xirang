package auth

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
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
	"gorm.io/gorm/logger"
)

const (
	recoverySQLiteDriverName  = "xirang_auth_recovery_code_test"
	recoveryFailureDriverName = "xirang_auth_recovery_failure_test"
)

var (
	recoverySQLiteDriverOnce   sync.Once
	recoverySQLiteDriverState  atomic.Pointer[recoverySQLiteConcurrencyState]
	recoveryFailureDriverOnce  sync.Once
	recoveryFailureDriverState atomic.Pointer[recoveryFailureState]
)

type recoverySQLiteConcurrencyState struct {
	busyBegin chan struct{}
}

type recoveryFailureState struct {
	failUserQuery        atomic.Bool
	failRevocationQuery  atomic.Bool
	failRevocationAfter  atomic.Int64
	failUpdate           atomic.Bool
	failCommit           atomic.Bool
	updateCalls          atomic.Int64
	revocationQueryCalls atomic.Int64
	commitCalls          atomic.Int64
}

type recoveryTraceEntry struct {
	sql string
	err string
}

type recoveryTraceLogger struct {
	mu      sync.Mutex
	entries []recoveryTraceEntry
}

func (l *recoveryTraceLogger) LogMode(logger.LogLevel) logger.Interface {
	return l
}

func (l *recoveryTraceLogger) Info(context.Context, string, ...interface{}) {}

func (l *recoveryTraceLogger) Warn(context.Context, string, ...interface{}) {}

func (l *recoveryTraceLogger) Error(context.Context, string, ...interface{}) {}

func (l *recoveryTraceLogger) Trace(_ context.Context, _ time.Time, fc func() (string, int64), err error) {
	sqlText, _ := fc()
	entry := recoveryTraceEntry{sql: sqlText}
	if err != nil {
		entry.err = err.Error()
	}
	l.mu.Lock()
	l.entries = append(l.entries, entry)
	l.mu.Unlock()
}

func (l *recoveryTraceLogger) snapshot() []recoveryTraceEntry {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]recoveryTraceEntry(nil), l.entries...)
}

func (l *recoveryTraceLogger) reset() {
	l.mu.Lock()
	l.entries = nil
	l.mu.Unlock()
}

type recoveryFailureDriver struct{}

func (recoveryFailureDriver) Open(name string) (driver.Conn, error) {
	conn, err := (&sqlite3.SQLiteDriver{}).Open(name)
	if err != nil {
		return nil, err
	}
	return &recoveryFailureConn{
		Conn:  conn,
		state: recoveryFailureDriverState.Load(),
	}, nil
}

type recoveryFailureConn struct {
	driver.Conn
	state *recoveryFailureState
}

func (conn *recoveryFailureConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	tx, err := conn.Conn.(driver.ConnBeginTx).BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	return &recoveryFailureTx{Tx: tx, state: conn.state}, nil
}

func (conn *recoveryFailureConn) ExecContext(
	ctx context.Context,
	query string,
	args []driver.NamedValue,
) (driver.Result, error) {
	execer, ok := conn.Conn.(driver.ExecerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	upper := strings.ToUpper(query)
	if conn.state != nil && strings.Contains(upper, "UPDATE") && strings.Contains(upper, "USERS") {
		conn.state.updateCalls.Add(1)
		if conn.state.failUpdate.Load() {
			return nil, errors.New("FAKE_RECOVERY_UPDATE_FAILURE_FOR_TEST_ONLY")
		}
	}
	return execer.ExecContext(ctx, query, args)
}

func (conn *recoveryFailureConn) QueryContext(
	ctx context.Context,
	query string,
	args []driver.NamedValue,
) (driver.Rows, error) {
	queryer, ok := conn.Conn.(driver.QueryerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	upper := strings.ToUpper(query)
	if conn.state != nil && strings.Contains(upper, "TOKEN_REVOCATIONS") {
		calls := conn.state.revocationQueryCalls.Add(1)
		failAfter := conn.state.failRevocationAfter.Load()
		if conn.state.failRevocationQuery.Load() || (failAfter > 0 && calls > failAfter) {
			return nil, errors.New("FAKE_RECOVERY_REVOCATION_QUERY_FAILURE_FOR_TEST_ONLY")
		}
	}
	if conn.state != nil && strings.Contains(upper, "FROM") && strings.Contains(upper, "USERS") &&
		conn.state.failUserQuery.Load() {
		return nil, errors.New("FAKE_RECOVERY_USER_QUERY_FAILURE_FOR_TEST_ONLY")
	}
	return queryer.QueryContext(ctx, query, args)
}

type recoveryFailureTx struct {
	driver.Tx
	state *recoveryFailureState
}

func (tx *recoveryFailureTx) Commit() error {
	if tx.state != nil {
		tx.state.commitCalls.Add(1)
	}
	err := tx.Tx.Commit()
	if err != nil {
		return err
	}
	if tx.state != nil && tx.state.failCommit.Load() {
		return errors.New("FAKE_RECOVERY_COMMIT_FAILURE_FOR_TEST_ONLY")
	}
	return nil
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
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared&_loc=UTC"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
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
	}), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
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

func openIdentitySQLiteFailureTestDB(t *testing.T, capture logger.Interface) (*gorm.DB, *recoveryFailureState) {
	t.Helper()
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "FAKE_IDENTITY_DATA_ENCRYPTION_KEY_32_BYTES")
	secure.ResetForTesting()
	recoveryFailureDriverOnce.Do(func() {
		sql.Register(recoveryFailureDriverName, recoveryFailureDriver{})
	})
	state := &recoveryFailureState{}
	recoveryFailureDriverState.Store(state)
	dsn := fmt.Sprintf(
		"file:%s?_journal_mode=WAL&_busy_timeout=5000&_synchronous=NORMAL&_txlock=immediate&_loc=UTC",
		filepath.Join(t.TempDir(), "identity-failure.sqlite"),
	)
	db, err := gorm.Open(sqlite.New(sqlite.Config{
		DriverName: recoveryFailureDriverName,
		DSN:        dsn,
	}), &gorm.Config{Logger: capture})
	if err != nil {
		t.Fatalf("open SQLite failure-test database: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get SQLite failure-test database: %v", err)
	}
	sqlDB.SetMaxOpenConns(4)
	sqlDB.SetMaxIdleConns(4)
	t.Cleanup(func() {
		_ = sqlDB.Close()
		recoveryFailureDriverState.Store(nil)
	})
	if err := db.AutoMigrate(&model.User{}, &model.PendingAuthToken{}, &model.TokenRevocation{}); err != nil {
		t.Fatalf("migrate SQLite failure-test database: %v", err)
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

type recoveryCodeFixture struct {
	db         *gorm.DB
	service    *Service
	manager    *JWTManager
	user       model.User
	session    RecoveryCodeSession
	totpSecret string
}

func newRecoveryCodeFixture(t *testing.T, db *gorm.DB, role, inventory string, enabled bool) recoveryCodeFixture {
	t.Helper()
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "FAKE_IDENTITY_DATA_ENCRYPTION_KEY_32_BYTES")
	secure.ResetForTesting()
	key, err := GenerateTOTPSecret("test", "recovery-management")
	if err != nil {
		t.Fatalf("generate TOTP key: %v", err)
	}
	user := model.User{
		Username:      fmt.Sprintf("recovery-management-%s-%d", role, time.Now().UnixNano()),
		Role:          role,
		PasswordHash:  "FAKE_PASSWORD_HASH_FOR_TEST_ONLY",
		TOTPSecret:    key.Secret(),
		TOTPEnabled:   enabled,
		RecoveryCodes: inventory,
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create recovery-management user: %v", err)
	}
	var stored model.User
	if err := db.First(&stored, user.ID).Error; err != nil {
		t.Fatalf("reload recovery-management user: %v", err)
	}
	manager := NewJWTManager("FAKE_JWT_SECRET_FOR_TEST_ONLY", time.Hour)
	service := NewService(db, manager, nil, LoginSecurityConfig{})
	token, err := manager.GenerateToken(stored)
	if err != nil {
		t.Fatalf("generate recovery-management session: %v", err)
	}
	claims, err := manager.ParseToken(token)
	if err != nil || claims == nil || claims.ExpiresAt == nil {
		t.Fatalf("parse recovery-management session: claims=%+v err=%v", claims, err)
	}
	return recoveryCodeFixture{
		db:      db,
		service: service,
		manager: manager,
		user:    stored,
		session: RecoveryCodeSession{
			JTI:          claims.ID,
			UserID:       claims.UserID,
			Role:         claims.Role,
			TokenVersion: claims.TokenVersion,
			ExpiresAt:    claims.ExpiresAt.Time,
		},
		totpSecret: key.Secret(),
	}
}

func recoveryRawCodes(t *testing.T, db *gorm.DB, userID uint) string {
	t.Helper()
	var row struct {
		RecoveryCodes string `gorm:"column:recovery_codes"`
	}
	if err := db.Table("users").Select("recovery_codes").Where("id = ?", userID).Take(&row).Error; err != nil {
		t.Fatalf("load raw recovery inventory: %v", err)
	}
	return row.RecoveryCodes
}

func recoveryRawOperands(t *testing.T, db *gorm.DB, userID uint) (string, string) {
	t.Helper()
	var row struct {
		TOTPSecret    string `gorm:"column:totp_secret"`
		RecoveryCodes string `gorm:"column:recovery_codes"`
	}
	if err := db.Table("users").
		Select("totp_secret", "recovery_codes").
		Where("id = ?", userID).
		Take(&row).Error; err != nil {
		t.Fatalf("load raw recovery operands: %v", err)
	}
	return row.TOTPSecret, row.RecoveryCodes
}

func recoveryUser(t *testing.T, db *gorm.DB, userID uint) model.User {
	t.Helper()
	var user model.User
	if err := db.First(&user, userID).Error; err != nil {
		t.Fatalf("load recovery user: %v", err)
	}
	return user
}

func testTOTPCode(t *testing.T, secret string) string {
	t.Helper()
	code, err := totp.GenerateCode(secret, time.Now())
	if err != nil {
		t.Fatalf("generate TOTP code: %v", err)
	}
	return code
}

func differentTOTPCode(code string) string {
	if code == "" {
		return "000000"
	}
	if code[0] == '0' {
		return "1" + code[1:]
	}
	return "0" + code[1:]
}

func TestRecoveryCodesRemainingSQLite(t *testing.T) {
	db := openIdentitySQLiteTestDB(t)
	for _, role := range []string{"admin", "operator", "viewer"} {
		t.Run(role, func(t *testing.T) {
			fx := newRecoveryCodeFixture(t, db, role, `["FAKE_RECOVERY_CODE_ONE_FOR_TEST_ONLY"]`, true)
			rawBefore := recoveryRawCodes(t, db, fx.user.ID)
			remaining, err := fx.service.RecoveryCodesRemaining(context.Background(), fx.session)
			if err != nil || remaining != 1 {
				t.Fatalf("remaining=%d err=%v, want one code", remaining, err)
			}
			if rawAfter := recoveryRawCodes(t, db, fx.user.ID); rawAfter != rawBefore {
				t.Fatalf("remaining query mutated encrypted inventory")
			}
		})
	}
}

func TestRecoveryCodesRemainingInventorySQLite(t *testing.T) {
	db := openIdentitySQLiteTestDB(t)
	tests := []struct {
		name      string
		inventory string
		want      int
	}{
		{name: "empty storage", inventory: "", want: 0},
		{name: "empty JSON", inventory: `[]`, want: 0},
		{name: "historical plaintext", inventory: `["FAKE_RECOVERY_CODE_ONE_FOR_TEST_ONLY","FAKE_RECOVERY_CODE_TWO_FOR_TEST_ONLY"]`, want: 2},
		{name: "bcrypt", inventory: `["$2a$04$abcdefghijklmnopqrstuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuu"]`, want: 1},
		{name: "consumed inventory", inventory: `["FAKE_RECOVERY_CODE_ONE_FOR_TEST_ONLY","FAKE_RECOVERY_CODE_TWO_FOR_TEST_ONLY","FAKE_RECOVERY_CODE_THREE_FOR_TEST_ONLY","FAKE_RECOVERY_CODE_FOUR_FOR_TEST_ONLY","FAKE_RECOVERY_CODE_FIVE_FOR_TEST_ONLY","FAKE_RECOVERY_CODE_SIX_FOR_TEST_ONLY","FAKE_RECOVERY_CODE_SEVEN_FOR_TEST_ONLY"]`, want: 7},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fx := newRecoveryCodeFixture(t, db, "admin", tt.inventory, true)
			got, err := fx.service.RecoveryCodesRemaining(context.Background(), fx.session)
			if err != nil || got != tt.want {
				t.Fatalf("remaining=%d err=%v, want %d", got, err, tt.want)
			}
		})
	}
}

func TestRecoveryCodesRemainingRejectsInvalidStateSQLite(t *testing.T) {
	db := openIdentitySQLiteTestDB(t)
	t.Run("malformed JSON", func(t *testing.T) {
		fx := newRecoveryCodeFixture(t, db, "admin", `["FAKE_RECOVERY_CODE_ONE_FOR_TEST_ONLY"]`, true)
		if err := db.Table("users").Where("id = ?", fx.user.ID).Update("recovery_codes", "not-json").Error; err != nil {
			t.Fatalf("corrupt recovery inventory: %v", err)
		}
		if _, err := fx.service.RecoveryCodesRemaining(context.Background(), fx.session); !errors.Is(err, errRecoveryInventoryInvalid) {
			t.Fatalf("malformed inventory error=%v, want strict inventory error", err)
		}
	})
	t.Run("strict JSON shapes", func(t *testing.T) {
		for _, invalid := range []string{"null", "{}", `[""]`, "[null]", "[1]"} {
			t.Run(invalid, func(t *testing.T) {
				fx := newRecoveryCodeFixture(t, db, "admin", `["FAKE_RECOVERY_CODE_ONE_FOR_TEST_ONLY"]`, true)
				if err := db.Table("users").Where("id = ?", fx.user.ID).Update("recovery_codes", invalid).Error; err != nil {
					t.Fatalf("corrupt recovery inventory: %v", err)
				}
				if _, err := fx.service.RecoveryCodesRemaining(context.Background(), fx.session); !errors.Is(err, ErrRecoveryCodeInternal) {
					t.Fatalf("inventory %q error=%v, want safe internal sentinel", invalid, err)
				}
			})
		}
	})
	t.Run("decrypt failure", func(t *testing.T) {
		fx := newRecoveryCodeFixture(t, db, "admin", `["FAKE_RECOVERY_CODE_ONE_FOR_TEST_ONLY"]`, true)
		if err := db.Table("users").Where("id = ?", fx.user.ID).Update("recovery_codes", "enc:v2:corrupt").Error; err != nil {
			t.Fatalf("corrupt encrypted inventory: %v", err)
		}
		if _, err := fx.service.RecoveryCodesRemaining(context.Background(), fx.session); !errors.Is(err, ErrRecoveryCodeInternal) {
			t.Fatalf("decrypt failure error=%v, want safe internal sentinel", err)
		}
	})
	t.Run("disabled", func(t *testing.T) {
		fx := newRecoveryCodeFixture(t, db, "admin", `["FAKE_RECOVERY_CODE_ONE_FOR_TEST_ONLY"]`, false)
		if _, err := fx.service.RecoveryCodesRemaining(context.Background(), fx.session); !errors.Is(err, ErrRecoveryCodeTOTPDisabled) {
			t.Fatalf("disabled TOTP error=%v, want ErrRecoveryCodeTOTPDisabled", err)
		}
	})
}

func TestRecoveryCodesRemainingRejectsStaleAndRevokedSessionsSQLite(t *testing.T) {
	db := openIdentitySQLiteTestDB(t)
	fx := newRecoveryCodeFixture(t, db, "admin", `["FAKE_RECOVERY_CODE_ONE_FOR_TEST_ONLY"]`, true)
	stale := fx.session
	stale.TokenVersion++
	if _, err := fx.service.RecoveryCodesRemaining(context.Background(), stale); !errors.Is(err, ErrRecoveryCodeSessionInvalid) {
		t.Fatalf("stale token version error=%v", err)
	}
	revoked := fx.session
	if err := fx.manager.RevokeSession(revoked.JTI, revoked.UserID, revoked.ExpiresAt); err != nil {
		t.Fatalf("revoke session: %v", err)
	}
	if _, err := fx.service.RecoveryCodesRemaining(context.Background(), revoked); !errors.Is(err, ErrRecoveryCodeSessionInvalid) {
		t.Fatalf("revoked session error=%v", err)
	}
	unknownRole := fx.session
	unknownRole.Role = "unknown"
	if _, err := fx.service.RecoveryCodesRemaining(context.Background(), unknownRole); !errors.Is(err, ErrRecoveryCodeSessionInvalid) {
		t.Fatalf("unknown role error=%v", err)
	}
	expired := fx.session
	expired.ExpiresAt = time.Now().UTC().Add(-time.Second)
	if _, err := fx.service.RecoveryCodesRemaining(context.Background(), expired); !errors.Is(err, ErrRecoveryCodeSessionInvalid) {
		t.Fatalf("expired session error=%v", err)
	}
}

func TestRegenerateRecoveryCodesSQLite(t *testing.T) {
	db := openIdentitySQLiteTestDB(t)
	fx := newRecoveryCodeFixture(t, db, "admin", `["FAKE_RECOVERY_CODE_OLD_FOR_TEST_ONLY"]`, true)
	validCode := testTOTPCode(t, fx.totpSecret)
	rawBefore := recoveryRawCodes(t, db, fx.user.ID)
	codes, err := fx.service.RegenerateRecoveryCodes(context.Background(), fx.session, "  "+validCode+" ")
	if err != nil {
		t.Fatalf("regenerate recovery codes: %v", err)
	}
	if len(codes) != recoveryCodeCount {
		t.Fatalf("generated %d recovery codes, want %d", len(codes), recoveryCodeCount)
	}
	for _, code := range codes {
		if len(code) != recoveryCodeLength || code != strings.ToUpper(code) {
			t.Fatalf("generated code has unexpected format %q", code)
		}
	}
	rawAfter := recoveryRawCodes(t, db, fx.user.ID)
	if rawAfter == rawBefore || !secure.IsEncrypted(rawAfter) {
		t.Fatalf("regenerated inventory is not an encrypted replacement")
	}
	stored := recoveryUser(t, db, fx.user.ID)
	var hashed []string
	if err := json.Unmarshal([]byte(stored.RecoveryCodes), &hashed); err != nil {
		t.Fatalf("decode regenerated inventory: %v", err)
	}
	if len(hashed) != recoveryCodeCount {
		t.Fatalf("stored %d hashed codes, want %d", len(hashed), recoveryCodeCount)
	}
	for _, hash := range hashed {
		if !strings.HasPrefix(hash, "$2") {
			t.Fatalf("stored regenerated code is not bcrypt: %q", hash)
		}
	}
	if _, ok := ValidateAndConsumeRecoveryCode(stored.RecoveryCodes, "FAKE_RECOVERY_CODE_OLD_FOR_TEST_ONLY"); ok {
		t.Fatal("old recovery code remained usable after regeneration")
	}
	if _, ok := ValidateAndConsumeRecoveryCode(stored.RecoveryCodes, codes[0]); !ok {
		t.Fatal("new recovery code is not usable")
	}
	pending, err := fx.manager.Generate2FAPendingToken(stored)
	if err != nil {
		t.Fatalf("generate normal TOTP pending token: %v", err)
	}
	if result, err := fx.service.Complete2FALogin(context.Background(), pending, testTOTPCode(t, fx.totpSecret)); err != nil || result == nil {
		t.Fatalf("normal TOTP login after regeneration result=%v err=%v", result != nil, err)
	}
	stored = recoveryUser(t, db, fx.user.ID)
	recoveryPending, err := fx.manager.Generate2FAPendingToken(stored)
	if err != nil {
		t.Fatalf("generate recovery-code pending token: %v", err)
	}
	if result, err := fx.service.Complete2FALogin(context.Background(), recoveryPending, "  "+strings.ToLower(codes[0])+" "); err != nil || result == nil {
		t.Fatalf("new recovery-code login result=%v err=%v", result != nil, err)
	}
	if remaining, err := fx.service.RecoveryCodesRemaining(context.Background(), fx.session); err != nil || remaining != recoveryCodeCount-1 {
		t.Fatalf("remaining after recovery-code login=%d err=%v", remaining, err)
	}
	replayPending, err := fx.manager.Generate2FAPendingToken(stored)
	if err != nil {
		t.Fatalf("generate replay pending token: %v", err)
	}
	if _, err := fx.service.Complete2FALogin(context.Background(), replayPending, codes[0]); !errors.Is(err, ErrRecoveryCodeInvalid) {
		t.Fatalf("replayed regenerated code error=%v, want ErrRecoveryCodeInvalid", err)
	}
	if remaining, err := fx.service.RecoveryCodesRemaining(context.Background(), fx.session); err != nil || remaining != recoveryCodeCount-1 {
		t.Fatalf("remaining after replay attempt=%d err=%v", remaining, err)
	}
}

func TestRegenerateRecoveryCodesRejectsInvalidOTPWithoutMutationSQLite(t *testing.T) {
	db := openIdentitySQLiteTestDB(t)
	fx := newRecoveryCodeFixture(t, db, "admin", `["FAKE_RECOVERY_CODE_OLD_FOR_TEST_ONLY"]`, true)
	rawBefore := recoveryRawCodes(t, db, fx.user.ID)
	validCode := testTOTPCode(t, fx.totpSecret)
	if _, err := fx.service.RegenerateRecoveryCodes(context.Background(), fx.session, differentTOTPCode(validCode)); !errors.Is(err, ErrRecoveryCodeTOTPInvalid) {
		t.Fatalf("invalid TOTP error=%v, want ErrRecoveryCodeTOTPInvalid", err)
	}
	if rawAfter := recoveryRawCodes(t, db, fx.user.ID); rawAfter != rawBefore {
		t.Fatal("invalid TOTP changed recovery inventory")
	}
	if _, err := fx.service.RegenerateRecoveryCodes(context.Background(), fx.session, "  "); !errors.Is(err, ErrRecoveryCodeTOTPRequired) {
		t.Fatalf("missing TOTP error=%v, want ErrRecoveryCodeTOTPRequired", err)
	}
	if _, err := fx.service.RegenerateRecoveryCodes(context.Background(), fx.session, "FAKE_RECOVERY_CODE_OLD_FOR_TEST_ONLY"); !errors.Is(err, ErrRecoveryCodeTOTPInvalid) {
		t.Fatalf("recovery-code-as-TOTP error=%v, want ErrRecoveryCodeTOTPInvalid", err)
	}
	if rawAfter := recoveryRawCodes(t, db, fx.user.ID); rawAfter != rawBefore {
		t.Fatal("recovery-code-as-TOTP changed recovery inventory")
	}
}

func TestRegenerateRecoveryCodesWriteFailureRollsBackSQLite(t *testing.T) {
	db := openIdentitySQLiteTestDB(t)
	fx := newRecoveryCodeFixture(t, db, "admin", `["FAKE_RECOVERY_CODE_OLD_FOR_TEST_ONLY"]`, true)
	rawBefore := recoveryRawCodes(t, db, fx.user.ID)
	callbackName := fmt.Sprintf("test:recovery-regeneration-write-failure-%d", time.Now().UnixNano())
	if err := db.Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement != nil && tx.Statement.Table == "users" {
			_ = tx.AddError(errors.New("FAKE_RECOVERY_WRITE_FAILURE_FOR_TEST_ONLY"))
		}
	}); err != nil {
		t.Fatalf("register write failure callback: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Callback().Update().Remove(callbackName); err != nil {
			t.Errorf("remove write failure callback: %v", err)
		}
	})
	codes, err := fx.service.RegenerateRecoveryCodes(context.Background(), fx.session, testTOTPCode(t, fx.totpSecret))
	if !errors.Is(err, ErrRecoveryCodeInternal) || codes != nil {
		t.Fatalf("write failure code count=%d internal=%t, want nil codes and safe internal sentinel", len(codes), errors.Is(err, ErrRecoveryCodeInternal))
	}
	if strings.Contains(err.Error(), "FAKE_RECOVERY_WRITE_FAILURE_FOR_TEST_ONLY") || strings.Contains(err.Error(), "recovery_codes") {
		t.Fatalf("write failure leaked storage detail: %v", err)
	}
	if rawAfter := recoveryRawCodes(t, db, fx.user.ID); rawAfter != rawBefore {
		t.Fatal("write failure changed recovery inventory")
	}
}

func TestRecoveryCodesRemainingQueryFailureIsNotZeroSQLite(t *testing.T) {
	db, state := openIdentitySQLiteFailureTestDB(t, logger.Discard)
	fx := newRecoveryCodeFixture(t, db, "admin", `["FAKE_RECOVERY_CODE_QUERY_FAILURE_FOR_TEST_ONLY"]`, true)
	state.failUserQuery.Store(true)
	remaining, err := fx.service.RecoveryCodesRemaining(context.Background(), fx.session)
	if remaining != 0 || !errors.Is(err, ErrRecoveryCodeInternal) {
		t.Fatalf("query failure remaining=%d internal=%t, want zero and internal error", remaining, errors.Is(err, ErrRecoveryCodeInternal))
	}
}

func TestRegenerateRecoveryCodesRevocationQueryFailureSQLite(t *testing.T) {
	db, state := openIdentitySQLiteFailureTestDB(t, logger.Discard)
	fx := newRecoveryCodeFixture(t, db, "admin", `["FAKE_RECOVERY_CODE_REVOCATION_FAILURE_FOR_TEST_ONLY"]`, true)
	rawBefore := recoveryRawCodes(t, db, fx.user.ID)
	state.updateCalls.Store(0)
	state.revocationQueryCalls.Store(0)
	state.failRevocationAfter.Store(1)
	codes, err := fx.service.RegenerateRecoveryCodes(context.Background(), fx.session, testTOTPCode(t, fx.totpSecret))
	if codes != nil || !errors.Is(err, ErrRecoveryCodeInternal) {
		t.Fatalf("revocation query failure code count=%d internal=%t, want nil and internal error", len(codes), errors.Is(err, ErrRecoveryCodeInternal))
	}
	if state.revocationQueryCalls.Load() < 2 {
		t.Fatalf("revocation query calls=%d, want admission and in-transaction checks", state.revocationQueryCalls.Load())
	}
	if state.updateCalls.Load() != 0 {
		t.Fatalf("revocation query failure issued %d user updates, want zero", state.updateCalls.Load())
	}
	if rawAfter := recoveryRawCodes(t, db, fx.user.ID); rawAfter != rawBefore {
		t.Fatal("revocation query failure changed recovery inventory")
	}
}

func TestRegenerateRecoveryCodesCommitFailureReturnsNoSecretsSQLite(t *testing.T) {
	db, state := openIdentitySQLiteFailureTestDB(t, logger.Discard)
	fx := newRecoveryCodeFixture(t, db, "admin", `["FAKE_RECOVERY_CODE_COMMIT_FAILURE_FOR_TEST_ONLY"]`, true)
	rawBefore := recoveryRawCodes(t, db, fx.user.ID)
	state.updateCalls.Store(0)
	state.commitCalls.Store(0)
	state.failCommit.Store(true)
	codes, err := fx.service.RegenerateRecoveryCodes(context.Background(), fx.session, testTOTPCode(t, fx.totpSecret))
	if codes != nil || !errors.Is(err, ErrRecoveryCodeInternal) {
		t.Fatalf("commit failure code count=%d internal=%t, want nil and internal error", len(codes), errors.Is(err, ErrRecoveryCodeInternal))
	}
	if state.updateCalls.Load() != 1 || state.commitCalls.Load() != 1 {
		t.Fatalf("commit failure updates=%d commits=%d, want one each without replay", state.updateCalls.Load(), state.commitCalls.Load())
	}
	if rawAfter := recoveryRawCodes(t, db, fx.user.ID); rawAfter == rawBefore {
		t.Fatal("commit failure fixture did not preserve the committed-or-unknown state")
	}
}

func TestRegenerateRecoveryCodesBuiltUpdateFailureDoesNotLogOperandsSQLite(t *testing.T) {
	tests := []struct {
		name   string
		legacy bool
	}{
		{name: "encrypted operands"},
		{name: "legacy plaintext operands", legacy: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			capture := &recoveryTraceLogger{}
			db, state := openIdentitySQLiteFailureTestDB(t, capture)
			fx := newRecoveryCodeFixture(t, db, "admin", `["FAKE_RECOVERY_CODE_LOG_CAPTURE_FOR_TEST_ONLY"]`, true)
			rawSecret, rawInventory := recoveryRawOperands(t, db, fx.user.ID)
			validSecret := fx.totpSecret
			if tt.legacy {
				rawSecret = fx.totpSecret
				rawInventory = `["FAKE_LEGACY_RECOVERY_CODE_LOG_CAPTURE_FOR_TEST_ONLY"]`
				if err := db.Table("users").Where("id = ?", fx.user.ID).Update("totp_secret", rawSecret).Error; err != nil {
					t.Fatalf("seed legacy plaintext TOTP operand: %v", err)
				}
				if err := db.Table("users").Where("id = ?", fx.user.ID).Update("recovery_codes", rawInventory).Error; err != nil {
					t.Fatalf("seed legacy plaintext recovery operand: %v", err)
				}
				validSecret = rawSecret
			}
			capture.reset()
			state.updateCalls.Store(0)
			state.failUpdate.Store(true)
			codes, err := fx.service.RegenerateRecoveryCodes(context.Background(), fx.session, testTOTPCode(t, validSecret))
			if codes != nil || !errors.Is(err, ErrRecoveryCodeInternal) {
				t.Fatalf("built update failure code count=%d internal=%t, want nil and internal error", len(codes), errors.Is(err, ErrRecoveryCodeInternal))
			}
			if state.updateCalls.Load() != 1 {
				t.Fatalf("built update failure reached %d user updates, want one actual UPDATE", state.updateCalls.Load())
			}
			entries := capture.snapshot()
			if len(entries) != 0 {
				t.Fatalf("recovery operation emitted %d SQL/error traces", len(entries))
			}
			for _, entry := range entries {
				if strings.Contains(entry.sql, rawSecret) || strings.Contains(entry.sql, rawInventory) || entry.err != "" {
					t.Fatal("recovery operation trace contained sensitive operands or raw error")
				}
			}
		})
	}
}

type recoveryManagementRaceResult struct {
	kind          string
	regenerated   []string
	regenerateErr error
	login         *TOTPLoginResult
	loginErr      error
}

func recoveryRaceBarrier(t *testing.T, db *gorm.DB) (chan int, func(), func()) {
	t.Helper()
	winnerReached := make(chan int, 1)
	releaseWinner := make(chan struct{})
	var releaseOnce sync.Once
	release := func() {
		releaseOnce.Do(func() { close(releaseWinner) })
	}
	var winnerOnce sync.Once
	callbackName := fmt.Sprintf("test:recovery-management-race-barrier-%d", time.Now().UnixNano())
	if err := db.Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement == nil || tx.Statement.Table != "users" {
			return
		}
		winnerOnce.Do(func() {
			var winnerPID int
			if tx.Name() == "postgres" {
				if err := tx.Statement.ConnPool.QueryRowContext(tx.Statement.Context, "SELECT pg_backend_pid()").Scan(&winnerPID); err != nil {
					_ = tx.AddError(err)
					return
				}
			}
			winnerReached <- winnerPID
			<-releaseWinner
		})
	}); err != nil {
		t.Fatalf("register recovery race barrier: %v", err)
	}
	cleanup := func() {
		release()
		if err := db.Callback().Update().Remove(callbackName); err != nil {
			t.Errorf("remove recovery race barrier: %v", err)
		}
	}
	return winnerReached, release, cleanup
}

func waitForRecoveryRaceContender(t *testing.T, db *gorm.DB, sqliteState *recoverySQLiteConcurrencyState, winnerPID int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if sqliteState != nil {
		select {
		case <-sqliteState.busyBegin:
		case <-ctx.Done():
			t.Fatalf("timed out waiting for SQLite BUSY/LOCKED recovery contender: %v", ctx.Err())
		}
		return
	}
	if db.Name() != "postgres" {
		t.Fatalf("recovery race has no lock observer for %s", db.Name())
	}
	waiterPID := waitForIdentityPostgresLock(t, db, winnerPID)
	if waiterPID == winnerPID {
		t.Fatalf("PostgreSQL recovery waiter PID=%d is winning transaction PID", waiterPID)
	}
}

func runRecoveryRegenerateConsumeRace(t *testing.T, db *gorm.DB, sqliteState *recoverySQLiteConcurrencyState, first string) {
	t.Helper()
	oldCode := "FAKE_RECOVERY_CODE_CONCURRENT_FOR_TEST_ONLY"
	oldHash, err := bcrypt.GenerateFromPassword([]byte(oldCode), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("hash concurrent recovery code: %v", err)
	}
	inventoryJSON, err := json.Marshal([]string{string(oldHash)})
	if err != nil {
		t.Fatalf("marshal concurrent recovery inventory: %v", err)
	}
	fx := newRecoveryCodeFixture(t, db, "admin", string(inventoryJSON), true)
	pending, err := fx.manager.Generate2FAPendingToken(fx.user)
	if err != nil {
		t.Fatalf("generate concurrent pending token: %v", err)
	}
	validTOTP := testTOTPCode(t, fx.totpSecret)

	winnerReached, releaseWinner, cleanupBarrier := recoveryRaceBarrier(t, db)
	defer cleanupBarrier()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	outcomes := make(chan recoveryManagementRaceResult, 2)
	startRegenerate := func() {
		go func() {
			codes, err := fx.service.RegenerateRecoveryCodes(ctx, fx.session, validTOTP)
			outcomes <- recoveryManagementRaceResult{kind: "regenerate", regenerated: codes, regenerateErr: err}
		}()
	}
	startLogin := func() {
		go func() {
			result, err := fx.service.Complete2FALogin(ctx, pending, oldCode)
			outcomes <- recoveryManagementRaceResult{kind: "login", login: result, loginErr: err}
		}()
	}
	if first == "regenerate" {
		startRegenerate()
	} else {
		startLogin()
	}
	var winnerPID int
	select {
	case winnerPID = <-winnerReached:
	case <-ctx.Done():
		t.Fatalf("timed out waiting for first recovery mutation: %v", ctx.Err())
	}
	if first == "regenerate" {
		startLogin()
	} else {
		startRegenerate()
	}
	waitForRecoveryRaceContender(t, db, sqliteState, winnerPID)
	// The first mutation is released only after the contender has reached the
	// same database lock boundary.
	releaseWinner()

	var regenerateCodes []string
	var regenerateErr, loginErr error
	var loginResult *TOTPLoginResult
	for range 2 {
		select {
		case outcome := <-outcomes:
			if outcome.kind == "regenerate" {
				regenerateCodes, regenerateErr = outcome.regenerated, outcome.regenerateErr
			} else {
				loginResult, loginErr = outcome.login, outcome.loginErr
			}
		case <-ctx.Done():
			t.Fatalf("timed out waiting for recovery race outcomes: %v", ctx.Err())
		}
	}

	stored := recoveryUser(t, db, fx.user.ID)
	inventory, err := decodeRecoveryInventory(stored.RecoveryCodes)
	if err != nil {
		t.Fatalf("decode final recovery inventory: %v", err)
	}
	var pendingRows []model.PendingAuthToken
	if err := db.Where("user_id = ?", fx.user.ID).Find(&pendingRows).Error; err != nil {
		t.Fatalf("load final pending tokens: %v", err)
	}
	consumedPending := 0
	for _, row := range pendingRows {
		if row.ConsumedAt != nil {
			consumedPending++
		}
	}
	switch first {
	case "regenerate":
		if regenerateErr != nil || len(regenerateCodes) != recoveryCodeCount {
			t.Fatalf("regeneration-first outcome codes=%d err=%v", len(regenerateCodes), regenerateErr)
		}
		if !errors.Is(loginErr, ErrRecoveryCodeInvalid) || loginResult != nil {
			t.Fatalf("old-code login after regeneration result=%v err=%v", loginResult != nil, loginErr)
		}
		if len(inventory) != recoveryCodeCount || consumedPending != 0 {
			t.Fatalf("regeneration-first final inventory=%d consumed pending=%d", len(inventory), consumedPending)
		}
		if _, ok := ValidateAndConsumeRecoveryCode(stored.RecoveryCodes, oldCode); ok {
			t.Fatal("old recovery code remained usable after regeneration-first race")
		}
		for index, code := range regenerateCodes {
			if _, ok := ValidateAndConsumeRecoveryCode(stored.RecoveryCodes, code); !ok {
				t.Fatalf("winning regenerated code index %d is not usable", index)
			}
		}
	default:
		if loginErr != nil || loginResult == nil {
			t.Fatalf("consumption-first login result=%v err=%v", loginResult != nil, loginErr)
		}
		if db.Name() == "postgres" {
			if !errors.Is(regenerateErr, ErrRecoveryCodeConflict) {
				t.Fatalf("consumption-first PostgreSQL regeneration error=%v, want conflict", regenerateErr)
			}
		} else if !errors.Is(regenerateErr, ErrRecoveryCodeConflict) && !errors.Is(regenerateErr, ErrRecoveryCodeInternal) {
			t.Fatalf("consumption-first SQLite regeneration error=%v, want conflict or safe busy failure", regenerateErr)
		}
		if len(inventory) != 0 || consumedPending != 1 {
			t.Fatalf("consumption-first final inventory=%d consumed pending=%d", len(inventory), consumedPending)
		}
		if _, ok := ValidateAndConsumeRecoveryCode(stored.RecoveryCodes, oldCode); ok {
			t.Fatal("consumed recovery code remained usable")
		}
	}
}

func TestRegenerateRecoveryCodesConcurrentSQLiteBothOrders(t *testing.T) {
	for _, first := range []string{"regenerate", "consume"} {
		t.Run(first+"-first", func(t *testing.T) {
			db, sqliteState := openIdentitySQLiteConcurrentTestDB(t)
			runRecoveryRegenerateConsumeRace(t, db, sqliteState, first)
		})
	}
}

func runRecoveryRegenerateRegenerateRace(t *testing.T, db *gorm.DB, sqliteState *recoverySQLiteConcurrencyState, first int) {
	t.Helper()
	oldCode := "FAKE_RECOVERY_CODE_REGENERATION_RACE_FOR_TEST_ONLY"
	oldHash, err := bcrypt.GenerateFromPassword([]byte(oldCode), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("hash regeneration-race code: %v", err)
	}
	inventoryJSON, err := json.Marshal([]string{string(oldHash)})
	if err != nil {
		t.Fatalf("marshal regeneration-race inventory: %v", err)
	}
	fx := newRecoveryCodeFixture(t, db, "admin", string(inventoryJSON), true)
	validTOTP := testTOTPCode(t, fx.totpSecret)
	pending, err := fx.manager.Generate2FAPendingToken(fx.user)
	if err != nil {
		t.Fatalf("generate live pending token: %v", err)
	}
	pendingClaims, err := fx.manager.ParseToken(pending)
	if err != nil || pendingClaims == nil {
		t.Fatalf("parse live pending token: err=%v", err)
	}
	var pendingBefore model.PendingAuthToken
	if err := db.Where("jti = ?", pendingClaims.ID).Take(&pendingBefore).Error; err != nil {
		t.Fatalf("load live pending token before race: %v", err)
	}
	winnerReached, releaseWinner, cleanupBarrier := recoveryRaceBarrier(t, db)
	defer cleanupBarrier()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	outcomes := make(chan recoveryManagementRaceResult, 2)
	start := func(kind string) {
		go func() {
			codes, err := fx.service.RegenerateRecoveryCodes(ctx, fx.session, validTOTP)
			outcomes <- recoveryManagementRaceResult{kind: kind, regenerated: codes, regenerateErr: err}
		}()
	}
	if first == 0 {
		start("first")
	} else {
		start("second")
	}
	var winnerPID int
	select {
	case winnerPID = <-winnerReached:
	case <-ctx.Done():
		t.Fatalf("timed out waiting for first regeneration: %v", ctx.Err())
	}
	if first == 0 {
		start("second")
	} else {
		start("first")
	}
	waitForRecoveryRaceContender(t, db, sqliteState, winnerPID)
	releaseWinner()

	var successes, conflicts int
	var winningCodes []string
	for range 2 {
		select {
		case outcome := <-outcomes:
			if outcome.regenerateErr == nil && len(outcome.regenerated) == recoveryCodeCount {
				successes++
				winningCodes = outcome.regenerated
				continue
			}
			if db.Name() == "postgres" {
				if errors.Is(outcome.regenerateErr, ErrRecoveryCodeConflict) {
					conflicts++
					continue
				}
			} else if errors.Is(outcome.regenerateErr, ErrRecoveryCodeConflict) || errors.Is(outcome.regenerateErr, ErrRecoveryCodeInternal) {
				conflicts++
				continue
			}
			t.Fatalf("unexpected regeneration-race outcome kind=%s codes=%d err=%v", outcome.kind, len(outcome.regenerated), outcome.regenerateErr)
		case <-ctx.Done():
			t.Fatalf("timed out waiting for regeneration-race outcomes: %v", ctx.Err())
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("expected one regeneration winner and one conflict/busy loser, successes=%d conflicts=%d", successes, conflicts)
	}
	var pendingAfter model.PendingAuthToken
	if err := db.Where("jti = ?", pendingBefore.JTI).Take(&pendingAfter).Error; err != nil {
		t.Fatalf("load live pending token after race: %v", err)
	}
	if pendingAfter.JTI != pendingBefore.JTI ||
		pendingAfter.UserID != pendingBefore.UserID ||
		pendingAfter.TokenVersion != pendingBefore.TokenVersion ||
		pendingAfter.TOTPBinding != pendingBefore.TOTPBinding ||
		!pendingAfter.ExpiresAt.Equal(pendingBefore.ExpiresAt) ||
		!pendingAfter.CreatedAt.Equal(pendingBefore.CreatedAt) ||
		pendingBefore.ConsumedAt != nil || pendingAfter.ConsumedAt != nil {
		t.Fatal("live pending token changed during regeneration race")
	}
	stored := recoveryUser(t, db, fx.user.ID)
	inventory, err := decodeRecoveryInventory(stored.RecoveryCodes)
	if err != nil || len(inventory) != recoveryCodeCount {
		t.Fatalf("final regeneration-race inventory=%d err=%v", len(inventory), err)
	}
	if _, ok := ValidateAndConsumeRecoveryCode(stored.RecoveryCodes, oldCode); ok {
		t.Fatal("old recovery code remained usable after regeneration race")
	}
	for index, code := range winningCodes {
		if _, ok := ValidateAndConsumeRecoveryCode(stored.RecoveryCodes, code); !ok {
			t.Fatalf("winning regeneration code index %d is not usable after regeneration race", index)
		}
	}
}

func TestRegenerateRecoveryCodesConcurrentRegenerationSQLiteBothOrders(t *testing.T) {
	for _, first := range []int{0, 1} {
		t.Run(fmt.Sprintf("call-%d-first", first), func(t *testing.T) {
			db, sqliteState := openIdentitySQLiteConcurrentTestDB(t)
			runRecoveryRegenerateRegenerateRace(t, db, sqliteState, first)
		})
	}
}
