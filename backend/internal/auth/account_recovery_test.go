package auth

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"xirang/backend/internal/model"
	"xirang/backend/internal/secure"
)

// All behavioral cases use the same suite against SQLite and a real isolated
// PostgreSQL schema; an absent DSN remains a visible skip, not engine evidence.
func accountRecoveryEngines(t *testing.T, run func(*testing.T, *gorm.DB)) {
	t.Helper()
	for _, engine := range []string{"sqlite", "postgres"} {
		t.Run(engine, func(t *testing.T) {
			var db *gorm.DB
			if engine == "sqlite" {
				db, _ = openIdentitySQLiteConcurrentTestDB(t)
			} else {
				t.Setenv("APP_ENV", "development")
				t.Setenv("DATA_ENCRYPTION_KEY", "FAKE_IDENTITY_DATA_ENCRYPTION_KEY_32_BYTES")
				secure.ResetForTesting()
				db = openIdentityPostgresTestDB(t)
			}
			if err := db.AutoMigrate(&model.LoginFailure{}, &model.AccountRecoveryAudit{}); err != nil {
				t.Fatal("migrate account recovery fixtures")
			}
			run(t, db)
		})
	}
}

type accountRecoveryFixture struct {
	db      *gorm.DB
	service *Service
	manager *JWTManager
	user    model.User
}

func newAccountRecoveryFixture(t *testing.T, db *gorm.DB) accountRecoveryFixture {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte("Recovery1!"), bcrypt.MinCost)
	if err != nil {
		t.Fatal("hash fixture password")
	}
	user := model.User{Username: "recovery-user", PasswordHash: string(hash), Role: "operator", TokenVersion: 7, TOTPReenrollmentRequired: true}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal("create recovery fixture")
	}
	manager := NewJWTManager("FAKE_ACCOUNT_RECOVERY_JWT_SECRET", time.Hour)
	service := NewService(db, manager, nil, LoginSecurityConfig{})
	return accountRecoveryFixture{db: db, service: service, manager: manager, user: user}
}

func (f accountRecoveryFixture) login(t *testing.T) *RecoveryEnrollmentResult {
	t.Helper()
	result, err := f.service.Login(f.user.Username, "Recovery1!", "127.0.0.1")
	if err != nil || result == nil {
		t.Fatal("restricted password login failed")
	}
	if result.RecoveryEnrollment == nil || result.Token != "" || result.LoginToken != "" || result.User != nil || result.Requires2FA {
		t.Fatal("recovery login leaked ordinary identity contract")
	}
	return result.RecoveryEnrollment
}

func accountRecoveryRow(t *testing.T, db *gorm.DB, id uint) storedIdentityUser {
	t.Helper()
	row, err := loadStoredIdentityUser(recoveryCodeSafeDB(db, context.Background()), id, false)
	if err != nil {
		t.Fatal("load recovery identity")
	}
	return row
}

func accountRecoveryAssertUnchanged(t *testing.T, before, after storedIdentityUser) {
	t.Helper()
	if before != after {
		t.Fatal("failed operation changed recovery identity")
	}
}

func TestAccountRecoveryEnrollmentLifecycle(t *testing.T) {
	accountRecoveryEngines(t, func(t *testing.T, db *gorm.DB) {
		f := newAccountRecoveryFixture(t, db)
		pending := model.PendingAuthToken{JTI: strings.Repeat("a", 32), UserID: f.user.ID, TokenVersion: 7, TOTPBinding: "old", ExpiresAt: time.Now().Add(time.Hour)}
		if err := db.Create(&pending).Error; err != nil {
			t.Fatal("seed pending token")
		}
		first := f.login(t)
		second := f.login(t)
		if first.Token == second.Token || first.Setup.Secret == second.Setup.Secret || first.Setup.EnrollmentID == second.Setup.EnrollmentID {
			t.Fatal("login did not replace enrollment")
		}
		claims, err := f.manager.ParseToken(second.Token)
		if err != nil {
			t.Fatal("parse restricted JWT")
		}
		if claims.Purpose != PurposeRecoveryEnrollment || claims.EnrollmentID != second.Setup.EnrollmentID || claims.ID == claims.EnrollmentID || claims.SessionID != "" || claims.StepUpAction != "" || claims.TOTPBinding != "" || claims.ExpiresAt == nil || claims.IssuedAt == nil ||
			!claims.ExpiresAt.Equal(second.Setup.ExpiresAt) || claims.ExpiresAt.Sub(claims.IssuedAt.Time) != TOTPEnrollmentTTL {
			t.Fatal("incorrect restricted JWT bounds")
		}
		var pendingCount int64
		if err := db.Model(&model.PendingAuthToken{}).Count(&pendingCount).Error; err != nil || pendingCount != 0 {
			t.Fatal("restricted login registered or retained pending login")
		}
		if codes, err := f.service.CompleteRecoveryEnrollment(context.Background(), first.Token, testTOTPCode(t, first.Setup.Secret)); codes != nil || !errors.Is(err, ErrRecoveryEnrollmentInvalid) {
			t.Fatal("replaced token accepted")
		}
		if codes, err := f.service.CompleteRecoveryEnrollment(context.Background(), second.Token, "ABCDEFGH"); codes != nil || !errors.Is(err, ErrTOTPCodeInvalid) {
			t.Fatal("recovery code used as enrollment TOTP")
		}
		before := accountRecoveryRow(t, db, f.user.ID)
		if !secure.IsEncrypted(before.TOTPSecret) {
			t.Fatal("setup secret not encrypted")
		}
		codes, err := f.service.CompleteRecoveryEnrollment(context.Background(), second.Token, testTOTPCode(t, second.Setup.Secret))
		if err != nil || len(codes) != recoveryCodeCount {
			t.Fatalf("complete restricted enrollment: %v", err)
		}
		seen := map[string]bool{}
		for _, code := range codes {
			if code == "" || seen[code] {
				t.Fatal("invalid recovery inventory")
			}
			seen[code] = true
		}
		after := accountRecoveryRow(t, db, f.user.ID)
		if after.ID != before.ID || after.Role != before.Role || after.PasswordHash != before.PasswordHash || !after.TOTPEnabled || after.TOTPReenrollmentRequired || after.TokenVersion != before.TokenVersion+1 || after.TOTPEnrollmentID != "" || after.TOTPEnrollmentExpiresAt.Valid || !secure.IsEncrypted(after.RecoveryCodes) {
			t.Fatal("incorrect completion identity")
		}
		inventory, err := secure.DecryptIfNeeded(after.RecoveryCodes)
		if err != nil {
			t.Fatal("decrypt test inventory")
		}
		var hashes []string
		if err := json.Unmarshal([]byte(inventory), &hashes); err != nil || len(hashes) != 8 {
			t.Fatal("invalid stored inventory")
		}
		for i, hash := range hashes {
			if bcrypt.CompareHashAndPassword([]byte(hash), []byte(codes[i])) != nil {
				t.Fatal("recovery code hash mismatch")
			}
		}
		var audits []model.AccountRecoveryAudit
		if err := db.Find(&audits).Error; err != nil || len(audits) != 1 {
			t.Fatal("missing completion audit")
		}
		audit := audits[0]
		if !lowerHexID(audit.OperationID) || audit.TargetUserID != f.user.ID || audit.TargetUsername != f.user.Username || audit.Event != model.AccountRecoveryEventReenrollmentCompleted || audit.OldTokenVersion != 7 || audit.NewTokenVersion != 8 || audit.Reason != "" || audit.ResetPassword || audit.ResetTOTP {
			t.Fatal("incorrect completion audit")
		}
		if again, err := f.service.CompleteRecoveryEnrollment(context.Background(), second.Token, testTOTPCode(t, second.Setup.Secret)); again != nil || !errors.Is(err, ErrRecoveryEnrollmentInvalid) {
			t.Fatal("consumed enrollment redisplayed recovery codes")
		}
		login, err := f.service.Login(f.user.Username, "Recovery1!", "127.0.0.1")
		if err != nil || login == nil || !login.Requires2FA || login.RecoveryEnrollment != nil || login.Token != "" {
			t.Fatal("normal login did not require new TOTP")
		}
		completed, err := f.service.Complete2FALogin(context.Background(), login.LoginToken, testTOTPCode(t, second.Setup.Secret))
		if err != nil || completed == nil || completed.Token == "" {
			t.Fatal("normal new-factor login failed")
		}
		primary, err := f.manager.ParseToken(completed.Token)
		if err != nil || primary.Purpose != "" || primary.TokenVersion != 8 {
			t.Fatal("normal primary token incorrect")
		}
	})
}

func TestAccountRecoveryClaimsAndCurrentIdentity(t *testing.T) {
	accountRecoveryEngines(t, func(t *testing.T, db *gorm.DB) {
		f := newAccountRecoveryFixture(t, db)
		enrollment := f.login(t)
		base, err := f.manager.ParseToken(enrollment.Token)
		if err != nil {
			t.Fatal("parse test JWT")
		}
		other := model.User{Username: "other-recovery-user", PasswordHash: f.user.PasswordHash, Role: f.user.Role, TokenVersion: f.user.TokenVersion, TOTPReenrollmentRequired: true}
		if err := db.Create(&other).Error; err != nil {
			t.Fatal("create second enrolled account")
		}
		if result, err := f.service.Login(other.Username, "Recovery1!", "127.0.0.1"); err != nil || result == nil || result.RecoveryEnrollment == nil {
			t.Fatal("enroll second account")
		}
		cases := []struct {
			name   string
			mutate func(*Claims)
		}{
			{"ordinary", func(c *Claims) { c.Purpose = "" }},
			{"pending", func(c *Claims) { c.Purpose = Purpose2FAPending }},
			{"step-up", func(c *Claims) { c.Purpose = PurposeStepUp }},
			{"subject", func(c *Claims) { c.Subject = "999999" }},
			{"other-account", func(c *Claims) { c.UserID = other.ID; c.Subject = fmt.Sprint(c.UserID) }},
			{"zero-id", func(c *Claims) { c.UserID = 0; c.Subject = "0" }},
			{"jti", func(c *Claims) { c.ID = "invalid" }},
			{"enrollment", func(c *Claims) { c.EnrollmentID = strings.Repeat("b", 32) }},
			{"version", func(c *Claims) { c.TokenVersion++ }},
			{"role", func(c *Claims) { c.Role = "admin" }},
			{"unknown-role", func(c *Claims) { c.Role = "unknown" }},
			{"missing-iat", func(c *Claims) { c.IssuedAt = nil }},
			{"missing-exp", func(c *Claims) { c.ExpiresAt = nil }},
			{"future-iat", func(c *Claims) { c.IssuedAt = jwt.NewNumericDate(time.Now().Add(time.Hour)) }},
			{"expired", func(c *Claims) {
				c.ExpiresAt = jwt.NewNumericDate(time.Now().Add(-time.Hour))
				c.IssuedAt = jwt.NewNumericDate(c.ExpiresAt.Add(-TOTPEnrollmentTTL))
			}},
			{"wrong-ttl", func(c *Claims) { c.ExpiresAt = jwt.NewNumericDate(c.ExpiresAt.Add(time.Minute)) }},
			{"session", func(c *Claims) { c.SessionID = strings.Repeat("a", 32) }},
			{"binding", func(c *Claims) { c.TOTPBinding = "not-sessionless" }},
			{"action", func(c *Claims) { c.StepUpAction = StepUpActionAssetSecretReveal }},
			{"nbf", func(c *Claims) { c.NotBefore = jwt.NewNumericDate(time.Now().Add(-time.Minute)) }},
		}
		before := accountRecoveryRow(t, db, f.user.ID)
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				claims := *base
				tc.mutate(&claims)
				token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(f.manager.secret)
				if err != nil {
					t.Fatal("sign invalid test claims")
				}
				codes, err := f.service.CompleteRecoveryEnrollment(context.Background(), token, testTOTPCode(t, enrollment.Setup.Secret))
				if codes != nil || !errors.Is(err, ErrRecoveryEnrollmentInvalid) {
					t.Fatal("invalid enrollment claims accepted")
				}
			})
		}
		for _, method := range []*jwt.SigningMethodHMAC{jwt.SigningMethodHS384, jwt.SigningMethodHS512} {
			token, err := jwt.NewWithClaims(method, *base).SignedString(f.manager.secret)
			if err != nil {
				t.Fatal("sign alternate algorithm")
			}
			if codes, err := f.service.CompleteRecoveryEnrollment(context.Background(), token, "123456"); codes != nil || !errors.Is(err, ErrRecoveryEnrollmentInvalid) {
				t.Fatal("wrong JWT algorithm accepted")
			}
		}
		if codes, err := f.service.CompleteRecoveryEnrollment(context.Background(), enrollment.Token+"x", "123456"); codes != nil || !errors.Is(err, ErrRecoveryEnrollmentInvalid) {
			t.Fatal("tampered signature accepted")
		}
		accountRecoveryAssertUnchanged(t, before, accountRecoveryRow(t, db, f.user.ID))
		for _, mutation := range []map[string]any{
			{"token_version": before.TokenVersion + 1}, {"role": "admin"}, {"totp_reenrollment_required": false},
			{"totp_enabled": true}, {"totp_enrollment_id": strings.Repeat("c", 32)}, {"totp_enrollment_expires_at": time.Now().Add(-time.Minute)},
		} {
			if err := db.Table("users").Where("id = ?", f.user.ID).Updates(mutation).Error; err != nil {
				t.Fatal("mutate current identity")
			}
			if codes, err := f.service.CompleteRecoveryEnrollment(context.Background(), enrollment.Token, testTOTPCode(t, enrollment.Setup.Secret)); codes != nil || !errors.Is(err, ErrRecoveryEnrollmentInvalid) {
				t.Fatal("stale current identity accepted")
			}
			if err := db.Table("users").Where("id = ?", f.user.ID).Updates(map[string]any{"token_version": before.TokenVersion, "role": before.Role, "totp_reenrollment_required": true, "totp_enabled": false, "totp_enrollment_id": before.TOTPEnrollmentID, "totp_enrollment_expires_at": before.TOTPEnrollmentExpiresAt.Time}).Error; err != nil {
				t.Fatal("restore identity")
			}
		}
		f.service.nowFunc = func() time.Time { return enrollment.Setup.ExpiresAt }
		if codes, err := f.service.CompleteRecoveryEnrollment(context.Background(), enrollment.Token, "123456"); codes != nil || !errors.Is(err, ErrRecoveryEnrollmentInvalid) {
			t.Fatal("expiry boundary accepted")
		}
	})
}

// A wrapper around the real engine transaction permits both definite rollback
// and successful COMMIT with a lost acknowledgement, without mocking SQL.
type accountRecoveryCommitPool struct {
	gorm.ConnPool
	mode   *atomic.Int32 // 1 = rollback before commit; 2 = commit then lose ack
	calls  *atomic.Int64
	begins chan struct{}
}

func (p accountRecoveryCommitPool) BeginTx(ctx context.Context, opts *sql.TxOptions) (gorm.ConnPool, error) {
	if p.begins != nil {
		select {
		case p.begins <- struct{}{}:
		default:
		}
	}
	var conn gorm.ConnPool
	var err error
	if b, ok := p.ConnPool.(gorm.ConnPoolBeginner); ok {
		conn, err = b.BeginTx(ctx, opts)
	} else {
		conn, err = p.ConnPool.(gorm.TxBeginner).BeginTx(ctx, opts)
	}
	if err != nil {
		return nil, err
	}
	return &accountRecoveryCommitTx{ConnPool: conn, committer: conn.(gorm.TxCommitter), mode: p.mode, calls: p.calls}, nil
}

type accountRecoveryCommitTx struct {
	gorm.ConnPool
	committer gorm.TxCommitter
	mode      *atomic.Int32
	calls     *atomic.Int64
}

func (tx *accountRecoveryCommitTx) Commit() error {
	tx.calls.Add(1)
	if tx.mode.Load() == 1 {
		_ = tx.committer.Rollback()
		return errors.New("FAKE_ACCOUNT_COMMIT_REJECTED")
	}
	if err := tx.committer.Commit(); err != nil {
		return err
	}
	if tx.mode.Load() == 2 {
		return errors.New("FAKE_ACCOUNT_COMMIT_ACK_LOST")
	}
	return nil
}
func (tx *accountRecoveryCommitTx) Rollback() error { return tx.committer.Rollback() }

func TestAccountRecoveryFailuresPublishNothing(t *testing.T) {
	for _, failure := range []string{"sign", "cas", "write", "audit", "commit-rollback", "commit-lost-ack"} {
		t.Run(failure, func(t *testing.T) {
			accountRecoveryEngines(t, func(t *testing.T, db *gorm.DB) {
				f := newAccountRecoveryFixture(t, db)
				enrollment := f.login(t)
				before := accountRecoveryRow(t, db, f.user.ID)
				var mode atomic.Int32
				var commits atomic.Int64
				wrapped := db.Session(&gorm.Session{NewDB: true})
				wrapped.Statement.ConnPool = accountRecoveryCommitPool{ConnPool: db.Statement.ConnPool, mode: &mode, calls: &commits}
				f.service.db = wrapped
				if failure == "commit-rollback" {
					mode.Store(1)
				}
				if failure == "commit-lost-ack" {
					mode.Store(2)
				}
				callback := "test:account-recovery-failure"
				if failure == "cas" || failure == "write" {
					if err := db.Callback().Update().Before("gorm:update").Register(callback, func(tx *gorm.DB) {
						if tx.Statement.Table != "users" {
							return
						}
						if failure == "cas" {
							tx.Statement.AddClause(clause.Where{Exprs: []clause.Expression{clause.Expr{SQL: "1 = 0"}}})
						} else {
							_ = tx.AddError(errors.New("FAKE_ACCOUNT_WRITE_FAILURE"))
						}
					}); err != nil {
						t.Fatal("install write failure")
					}
					t.Cleanup(func() { _ = db.Callback().Update().Remove(callback) })
				}
				if failure == "audit" {
					if err := db.Callback().Create().Before("gorm:create").Register(callback, func(tx *gorm.DB) {
						if tx.Statement.Table == "account_recovery_audits" {
							_ = tx.AddError(errors.New("FAKE_ACCOUNT_AUDIT_FAILURE"))
						}
					}); err != nil {
						t.Fatal("install audit failure")
					}
					t.Cleanup(func() { _ = db.Callback().Create().Remove(callback) })
				}
				if failure == "sign" {
					f.manager.signTokenFunc = func(*jwt.Token) (string, error) { return "", errors.New("FAKE_ACCOUNT_SIGN_FAILURE") }
					result, err := f.service.Login(f.user.Username, "Recovery1!", "127.0.0.1")
					if result != nil || !errors.Is(err, ErrRecoveryEnrollmentInternal) {
						t.Fatal("signing failure exposed enrollment")
					}
					accountRecoveryAssertUnchanged(t, before, accountRecoveryRow(t, db, f.user.ID))
					return
				}
				codes, err := f.service.CompleteRecoveryEnrollment(context.Background(), enrollment.Token, testTOTPCode(t, enrollment.Setup.Secret))
				expected := ErrRecoveryEnrollmentInternal
				if failure == "cas" {
					expected = ErrRecoveryEnrollmentInvalid
				}
				if codes != nil || !errors.Is(err, expected) {
					t.Fatal("failed completion exposed codes or unsafe error")
				}
				if failure == "commit-lost-ack" {
					after := accountRecoveryRow(t, db, f.user.ID)
					if after.TOTPReenrollmentRequired || !after.TOTPEnabled || after.TokenVersion != before.TokenVersion+1 || commits.Load() != 1 {
						t.Fatal("lost ack fixture did not commit exactly once")
					}
					mode.Store(0)
					if again, err := f.service.CompleteRecoveryEnrollment(context.Background(), enrollment.Token, testTOTPCode(t, enrollment.Setup.Secret)); again != nil || !errors.Is(err, ErrRecoveryEnrollmentInvalid) {
						t.Fatal("lost ack replay displayed codes")
					}
				} else {
					accountRecoveryAssertUnchanged(t, before, accountRecoveryRow(t, db, f.user.ID))
				}
				var count int64
				if err := db.Model(&model.AccountRecoveryAudit{}).Count(&count).Error; err != nil {
					t.Fatal("count audit")
				}
				if (failure == "commit-lost-ack" && count != 1) || (failure != "commit-lost-ack" && count != 0) {
					t.Fatal("audit not atomic")
				}
			})
		})
	}
}

func TestAccountRecoveryOrdinaryBypassGuards(t *testing.T) {
	accountRecoveryEngines(t, func(t *testing.T, db *gorm.DB) {
		f := newAccountRecoveryFixture(t, db)
		enrollment := f.login(t)
		code := testTOTPCode(t, enrollment.Setup.Secret)
		before := accountRecoveryRow(t, db, f.user.ID)
		if token, err := f.manager.GenerateToken(f.user); token != "" || !errors.Is(err, ErrRecoveryEnrollmentInvalid) {
			t.Fatal("primary token allowed")
		}
		if token, err := f.manager.Generate2FAPendingToken(f.user); token != "" || !errors.Is(err, ErrRecoveryEnrollmentInvalid) {
			t.Fatal("pending token allowed")
		}
		session := TOTPActivationSession{UserID: f.user.ID, Role: f.user.Role, TokenVersion: f.user.TokenVersion, JTI: strings.Repeat("a", 32), ExpiresAt: time.Now().Add(time.Hour)}
		if token, err := f.manager.generateActivationToken(f.user, session); token != "" || !errors.Is(err, ErrRecoveryEnrollmentInvalid) {
			t.Fatal("activation token allowed")
		}
		if setup, err := f.service.SetupTOTP(context.Background(), f.user.ID, f.user.Username); setup != nil || !errors.Is(err, ErrRecoveryEnrollmentInvalid) {
			t.Fatal("ordinary setup bypass")
		}
		if result, err := f.service.VerifyTOTP(context.Background(), session, code, enrollment.Setup.EnrollmentID); result != nil || !errors.Is(err, ErrSecurityConflict) {
			t.Fatal("ordinary verify bypass")
		}
		if err := f.service.DisableTOTP(context.Background(), f.user.ID, "Recovery1!", code); !errors.Is(err, ErrRecoveryEnrollmentInvalid) {
			t.Fatal("ordinary disable bypass")
		}
		recoverySession := RecoveryCodeSession(session)
		if codes, err := f.service.RegenerateRecoveryCodes(context.Background(), recoverySession, code); codes != nil || !errors.Is(err, ErrRecoveryCodeSessionInvalid) {
			t.Fatal("ordinary regenerate bypass")
		}
		if _, err := f.service.RecoveryCodesRemaining(context.Background(), recoverySession); !errors.Is(err, ErrRecoveryCodeSessionInvalid) {
			t.Fatal("restricted session inventory access")
		}
		ordinary := f.user
		ordinary.TOTPReenrollmentRequired = false
		ordinary.TOTPEnabled = true
		ordinary.TOTPSecret = enrollment.Setup.Secret
		pending, err := f.manager.Generate2FAPendingToken(ordinary)
		if err != nil {
			t.Fatal("prepare old pending challenge")
		}
		if result, err := f.service.Complete2FALogin(context.Background(), pending, code); result != nil || !errors.Is(err, ErrPendingLoginInvalid) {
			t.Fatal("pending completion bypass")
		}
		accountRecoveryAssertUnchanged(t, before, accountRecoveryRow(t, db, f.user.ID))
		// Contradictory required+enabled is rejected even with correct password.
		if err := db.Table("users").Where("id = ?", f.user.ID).Update("totp_enabled", true).Error; err != nil {
			t.Fatal("set contradictory flags")
		}
		if result, err := f.service.Login(f.user.Username, "Recovery1!", "127.0.0.1"); result != nil || !errors.Is(err, ErrRecoveryEnrollmentInvalid) {
			t.Fatal("contradictory account issued credentials")
		}
	})
}

// Pause after Login's successful-password bookkeeping has committed, but
// before its identity transaction begins. No engine lock is held at the gate.
func accountRecoveryQueryBarrier(t *testing.T, db *gorm.DB) (<-chan struct{}, func()) {
	t.Helper()
	entered, release := make(chan struct{}), make(chan struct{})
	var once atomic.Bool
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	name := "test:account-recovery-snapshot-barrier"
	if err := db.Callback().Delete().After("gorm:commit_or_rollback_transaction").Register(name, func(tx *gorm.DB) {
		if tx.Statement.Table == "login_failures" && once.CompareAndSwap(false, true) {
			close(entered)
			<-release
		}
	}); err != nil {
		t.Fatal("register snapshot barrier")
	}
	t.Cleanup(func() { unblock(); _ = db.Callback().Delete().Remove(name) })
	return entered, unblock
}

func accountRecoveryAwait(t *testing.T, entered <-chan struct{}) {
	t.Helper()
	select {
	case <-entered:
	case <-time.After(15 * time.Second):
		t.Fatal("recovery barrier timed out")
	}
}

func TestAccountRecoveryOldPasswordSnapshotCannotUpgrade(t *testing.T) {
	accountRecoveryEngines(t, func(t *testing.T, db *gorm.DB) {
		f := newAccountRecoveryFixture(t, db)
		entered, release := accountRecoveryQueryBarrier(t, db)
		type outcome struct {
			result *LoginResult
			err    error
		}
		result := make(chan outcome, 1)
		go func() { r, e := f.service.Login(f.user.Username, "Recovery1!", "127.0.0.1"); result <- outcome{r, e} }()
		accountRecoveryAwait(t, entered)
		if err := db.Table("users").Where("id = ?", f.user.ID).Updates(map[string]any{"token_version": 8, "password_hash": "FAKE_NEW_PASSWORD_HASH"}).Error; err != nil {
			t.Fatal("commit reset across password snapshot")
		}
		release()
		got := <-result
		if got.result != nil || !errors.Is(got.err, ErrRecoveryEnrollmentInvalid) {
			t.Fatal("old password snapshot upgraded to reset identity")
		}
	})
}

func TestAccountRecoveryConcurrentEnrollment(t *testing.T) {
	for _, scenario := range []string{"login-login", "verify-verify", "login-verify", "verify-login"} {
		t.Run(scenario, func(t *testing.T) {
			accountRecoveryEngines(t, func(t *testing.T, db *gorm.DB) {
				f := newAccountRecoveryFixture(t, db)
				enrollment := f.login(t)
				code := testTOTPCode(t, enrollment.Setup.Secret)
				entered, release := make(chan struct{}), make(chan struct{})
				var once atomic.Bool
				var releaseOnce sync.Once
				unblock := func() { releaseOnce.Do(func() { close(release) }) }
				name := "test:account-recovery-concurrent-write"
				if err := db.Callback().Update().Before("gorm:update").Register(name, func(tx *gorm.DB) {
					if tx.Statement.Table == "users" && once.CompareAndSwap(false, true) {
						close(entered)
						<-release
					}
				}); err != nil {
					t.Fatal("install concurrent write barrier")
				}
				t.Cleanup(func() { unblock(); _ = db.Callback().Update().Remove(name) })
				var mode atomic.Int32
				var commits atomic.Int64
				begins := make(chan struct{}, 32)
				wrapped := db.Session(&gorm.Session{NewDB: true})
				wrapped.Statement.ConnPool = accountRecoveryCommitPool{ConnPool: db.Statement.ConnPool, mode: &mode, calls: &commits, begins: begins}
				f.service.db = wrapped
				f.service.failureLocker.db = wrapped
				type outcome struct {
					login *LoginResult
					codes []string
					err   error
				}
				run := func(kind string, done chan<- outcome) {
					if kind == "login" {
						r, e := f.service.Login(f.user.Username, "Recovery1!", "127.0.0.1")
						done <- outcome{login: r, err: e}
					} else {
						c, e := f.service.CompleteRecoveryEnrollment(context.Background(), enrollment.Token, code)
						done <- outcome{codes: c, err: e}
					}
				}
				kinds := strings.Split(scenario, "-")
				first, second := make(chan outcome, 1), make(chan outcome, 1)
				go run(kinds[0], first)
				accountRecoveryAwait(t, entered)
				// Discard only events known to precede the winner's write barrier.
				draining := true
				for draining {
					select {
					case <-begins:
					default:
						draining = false
					}
				}
				go run(kinds[1], second)
				accountRecoveryAwait(t, begins)
				unblock()
				a, b := <-first, <-second
				if a.err != nil {
					t.Fatal("winning enrollment operation failed")
				}
				switch scenario {
				case "login-login":
					if b.err != nil || a.login == nil || b.login == nil || a.login.RecoveryEnrollment == nil || b.login.RecoveryEnrollment == nil {
						t.Fatal("concurrent logins failed")
					}
					winner := b.login.RecoveryEnrollment
					row := accountRecoveryRow(t, db, f.user.ID)
					if row.TOTPEnrollmentID != winner.Setup.EnrollmentID {
						t.Fatal("last login did not own enrollment")
					}
					if c, e := f.service.CompleteRecoveryEnrollment(context.Background(), a.login.RecoveryEnrollment.Token, testTOTPCode(t, a.login.RecoveryEnrollment.Setup.Secret)); c != nil || !errors.Is(e, ErrRecoveryEnrollmentInvalid) {
						t.Fatal("first concurrent login remained valid")
					}
				case "verify-verify":
					if len(a.codes) != 8 || b.codes != nil || !errors.Is(b.err, ErrRecoveryEnrollmentInvalid) {
						t.Fatal("duplicate verification did not have one winner")
					}
				case "login-verify":
					if b.codes != nil || !errors.Is(b.err, ErrRecoveryEnrollmentInvalid) {
						t.Fatal("verification accepted replaced enrollment")
					}
				case "verify-login":
					// The contender verified the old required snapshot before the
					// winner committed, so it must not issue a newer capability.
					if len(a.codes) != 8 || b.login != nil || !errors.Is(b.err, ErrRecoveryEnrollmentInvalid) {
						t.Fatal("racing login upgraded old snapshot after completion")
					}
				}
			})
		})
	}
}

func TestAccountRecoverySecretSafeLogging(t *testing.T) {
	accountRecoveryEngines(t, func(t *testing.T, db *gorm.DB) {
		f := newAccountRecoveryFixture(t, db)
		capture := &recoveryTraceLogger{}
		logged := db.Session(&gorm.Session{Logger: capture})
		f.service.db = logged
		f.service.failureLocker.db = logged
		enrollment := f.login(t)
		row := accountRecoveryRow(t, db, f.user.ID)
		code := testTOTPCode(t, enrollment.Setup.Secret)
		codes, err := f.service.CompleteRecoveryEnrollment(context.Background(), enrollment.Token, code)
		if err != nil {
			t.Fatal("logged recovery completion failed")
		}
		after := accountRecoveryRow(t, db, f.user.ID)
		forbidden := append([]string{enrollment.Token, enrollment.Setup.Secret, row.TOTPSecret, after.RecoveryCodes, code, f.user.PasswordHash}, codes...)
		for _, entry := range capture.snapshot() {
			for _, secret := range forbidden {
				if secret != "" && (strings.Contains(entry.sql, secret) || strings.Contains(entry.err, secret)) {
					t.Fatal("recovery secret appeared in SQL logs")
				}
			}
		}
	})
}

func TestAccountRecoveryPasswordFailureCreatesNoEnrollment(t *testing.T) {
	accountRecoveryEngines(t, func(t *testing.T, db *gorm.DB) {
		f := newAccountRecoveryFixture(t, db)
		before := accountRecoveryRow(t, db, f.user.ID)
		if r, e := f.service.Login(f.user.Username, "wrong-password", "127.0.0.1"); r != nil || !errors.Is(e, errInvalidCredentials) {
			t.Fatal("bad password accepted")
		}
		accountRecoveryAssertUnchanged(t, before, accountRecoveryRow(t, db, f.user.ID))
		for range 6 {
			f.service.failureLocker.RegisterFailure(f.user.Username, "127.0.0.1", time.Now())
		}
		if r, e := f.service.Login(f.user.Username, "Recovery1!", "127.0.0.1"); r != nil {
			t.Fatal("locked password login returned result")
		} else if _, ok := IsLoginLocked(e); !ok {
			t.Fatal("locked password login not rejected")
		}
		accountRecoveryAssertUnchanged(t, before, accountRecoveryRow(t, db, f.user.ID))
	})
}

func TestAccountRecoveryOrdinarySnapshotRetainsOriginalVersion(t *testing.T) {
	accountRecoveryEngines(t, func(t *testing.T, db *gorm.DB) {
		f := newAccountRecoveryFixture(t, db)
		if err := db.Table("users").Where("id = ?", f.user.ID).Update("totp_reenrollment_required", false).Error; err != nil {
			t.Fatal("prepare ordinary identity")
		}
		entered, release := accountRecoveryQueryBarrier(t, db)
		type outcome struct {
			result *LoginResult
			err    error
		}
		done := make(chan outcome, 1)
		go func() { r, e := f.service.Login(f.user.Username, "Recovery1!", "127.0.0.1"); done <- outcome{r, e} }()
		accountRecoveryAwait(t, entered)
		if err := db.Table("users").Where("id = ?", f.user.ID).Updates(map[string]any{"token_version": 8, "totp_reenrollment_required": true, "password_hash": "FAKE_REPLACEMENT_HASH"}).Error; err != nil {
			t.Fatal("reset ordinary identity")
		}
		release()
		got := <-done
		if got.err != nil || got.result == nil || got.result.Token == "" || got.result.RecoveryEnrollment != nil {
			t.Fatal("ordinary login contract changed")
		}
		claims, err := f.manager.ParseToken(got.result.Token)
		if err != nil || claims.TokenVersion != 7 || claims.Purpose != "" {
			t.Fatal("old password verification upgraded token version")
		}
		if claims.TokenVersion == accountRecoveryRow(t, db, f.user.ID).TokenVersion {
			t.Fatal("pre-reset snapshot produced current token")
		}
	})
}

func TestAccountRecoveryRegenerateRechecksRestrictedCurrentSession(t *testing.T) {
	accountRecoveryEngines(t, func(t *testing.T, db *gorm.DB) {
		f := newRecoveryCodeFixture(t, db, "operator", `["FAKE_OLD_RECOVERY_CODE"]`, true)
		before := accountRecoveryRow(t, db, f.user.ID)
		entered, release := make(chan struct{}), make(chan struct{})
		var once atomic.Bool
		var releaseOnce sync.Once
		unblock := func() { releaseOnce.Do(func() { close(release) }) }
		name := "test:account-recovery-regenerate-baseline"
		if err := db.Callback().Query().After("gorm:query").Register(name, func(tx *gorm.DB) {
			if _, ok := tx.Statement.Dest.(*storedIdentityUser); ok && once.CompareAndSwap(false, true) {
				close(entered)
				<-release
			}
		}); err != nil {
			t.Fatal("install regeneration baseline barrier")
		}
		t.Cleanup(func() { unblock(); _ = db.Callback().Query().Remove(name) })
		type outcome struct {
			codes []string
			err   error
		}
		done := make(chan outcome, 1)
		code := testTOTPCode(t, f.totpSecret)
		go func() {
			c, e := f.service.RegenerateRecoveryCodes(context.Background(), f.session, code)
			done <- outcome{c, e}
		}()
		accountRecoveryAwait(t, entered)
		// Leave role/version unchanged to prove the explicit current-row flag
		// restriction, not just the usual version mismatch, closes this path.
		if err := db.Table("users").Where("id = ?", f.user.ID).Update("totp_reenrollment_required", true).Error; err != nil {
			t.Fatal("restrict current session")
		}
		unblock()
		got := <-done
		if got.codes != nil || !errors.Is(got.err, ErrRecoveryCodeSessionInvalid) {
			t.Fatal("regeneration bypassed current-row reenrollment restriction")
		}
		after := accountRecoveryRow(t, db, f.user.ID)
		if after.RecoveryCodes != before.RecoveryCodes || after.TokenVersion != before.TokenVersion {
			t.Fatal("restricted regeneration changed inventory")
		}
	})
}

func TestAccountRecoveryLoginCommitAndWriteFailures(t *testing.T) {
	for _, failure := range []string{"cas", "write", "commit-rollback", "commit-lost-ack"} {
		t.Run(failure, func(t *testing.T) {
			accountRecoveryEngines(t, func(t *testing.T, db *gorm.DB) {
				f := newAccountRecoveryFixture(t, db)
				before := accountRecoveryRow(t, db, f.user.ID)
				var mode atomic.Int32
				var commits atomic.Int64
				wrapped := db.Session(&gorm.Session{NewDB: true})
				wrapped.Statement.ConnPool = accountRecoveryCommitPool{ConnPool: db.Statement.ConnPool, mode: &mode, calls: &commits}
				f.service.db = wrapped
				if failure == "commit-rollback" {
					mode.Store(1)
				}
				if failure == "commit-lost-ack" {
					mode.Store(2)
				}
				name := "test:account-recovery-login-write-failure"
				if failure == "cas" || failure == "write" {
					if err := db.Callback().Update().Before("gorm:update").Register(name, func(tx *gorm.DB) {
						if tx.Statement.Table != "users" {
							return
						}
						if failure == "cas" {
							tx.Statement.AddClause(clause.Where{Exprs: []clause.Expression{clause.Expr{SQL: "1 = 0"}}})
						} else {
							_ = tx.AddError(errors.New("FAKE_LOGIN_WRITE_FAILURE"))
						}
					}); err != nil {
						t.Fatal("install login write failure")
					}
					t.Cleanup(func() { _ = db.Callback().Update().Remove(name) })
				}
				// Invoke the password-verified seam directly so login-failure counter
				// cleanup transactions cannot consume the commit injection.
				result, err := f.service.beginRecoveryEnrollment(context.Background(), f.user)
				expected := ErrRecoveryEnrollmentInternal
				if failure == "cas" {
					expected = ErrRecoveryEnrollmentInvalid
				}
				if result != nil || !errors.Is(err, expected) {
					t.Fatal("failed login enrollment published secrets")
				}
				after := accountRecoveryRow(t, db, f.user.ID)
				if failure == "commit-lost-ack" {
					if commits.Load() != 1 || after.TOTPEnrollmentID == "" || after.TOTPEnrollmentID == before.TOTPEnrollmentID {
						t.Fatal("lost enrollment ack did not commit exactly once")
					}
					mode.Store(0)
					replacement, err := f.service.beginRecoveryEnrollment(context.Background(), f.user)
					if err != nil || replacement == nil || replacement.Setup.EnrollmentID == after.TOTPEnrollmentID {
						t.Fatal("new password login did not replace unknown enrollment")
					}
				} else {
					accountRecoveryAssertUnchanged(t, before, after)
				}
			})
		})
	}
}
