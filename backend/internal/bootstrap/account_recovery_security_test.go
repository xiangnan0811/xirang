package bootstrap

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"xirang/backend/internal/model"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type accountRecoveryTraceLogger struct{ traces atomic.Int64 }

func (l *accountRecoveryTraceLogger) LogMode(logger.LogLevel) logger.Interface    { return l }
func (*accountRecoveryTraceLogger) Info(context.Context, string, ...interface{})  {}
func (*accountRecoveryTraceLogger) Warn(context.Context, string, ...interface{})  {}
func (*accountRecoveryTraceLogger) Error(context.Context, string, ...interface{}) {}
func (l *accountRecoveryTraceLogger) Trace(_ context.Context, _ time.Time, fc func() (string, int64), _ error) {
	l.traces.Add(1)
	// A malicious/noisy surrounding logger must never be allowed to evaluate
	// the SQL interpolation closure of a credential-bearing recovery query.
	_, _ = fc()
}

func testAccountRecoverySecurity(t *testing.T, open accountRecoveryOpen) {
	t.Helper()
	t.Run("scoped SQL silence and status read-only", func(t *testing.T) {
		db := open(t)
		user := seedAccountRecoveryTarget(t, db, "admin", true, false)
		var writes atomic.Int64
		if err := db.Callback().Create().Before("gorm:create").Register("recovery_count_create", func(*gorm.DB) { writes.Add(1) }); err != nil {
			t.Fatal("register create observer")
		}
		if err := db.Callback().Update().Before("gorm:update").Register("recovery_count_update", func(*gorm.DB) { writes.Add(1) }); err != nil {
			t.Fatal("register update observer")
		}
		if err := db.Callback().Delete().Before("gorm:delete").Register("recovery_count_delete", func(*gorm.DB) { writes.Add(1) }); err != nil {
			t.Fatal("register delete observer")
		}
		capture := &accountRecoveryTraceLogger{}
		noisy := db.Session(&gorm.Session{Logger: capture})
		request := accountRecoveryRequest(t, user)
		if err := RecoverAccount(context.Background(), noisy, request); err != nil {
			t.Fatal(err)
		}
		writes.Store(0)
		if found, err := ReadAccountRecoveryStatus(context.Background(), noisy, request.OperationID); err != nil || !found {
			t.Fatal("observe committed reset")
		}
		if writes.Load() != 0 {
			t.Fatal("status wrote application data")
		}
		if capture.traces.Load() != 0 {
			t.Fatal("recovery exposed SQL to the surrounding logger")
		}
		if err := db.Callback().Query().Before("gorm:query").Register("recovery_status_failure", func(tx *gorm.DB) {
			if tx.Statement.Table == "account_recovery_audits" {
				_ = tx.AddError(errors.New("FAKE-SECRET-QUERY-FAILURE"))
			}
		}); err != nil {
			t.Fatal("register status failure")
		}
		if _, err := ReadAccountRecoveryStatus(context.Background(), noisy, request.OperationID); err != ErrAccountRecoveryStatus {
			t.Fatal("status query failure leaked an underlying error or claimed not found")
		}
		if capture.traces.Load() != 0 || writes.Load() != 0 {
			t.Fatal("failed status logged SQL or wrote data")
		}
	})
	t.Run("ciphertext hash role version and enrollment CAS", func(t *testing.T) {
		// Each predicate is security-relevant even when normal callers obey the
		// identity lock; force a conflicting write immediately before the CAS.
		for _, column := range []string{"totp_secret", "password_hash", "role", "token_version", "totp_enrollment_id", "totp_enrollment_expires_at", "totp_reenrollment_required"} {
			t.Run(column, func(t *testing.T) {
				db := open(t)
				user := seedAccountRecoveryTarget(t, db, "operator", true, false)
				before := recoveryIdentitySnapshot(t, db, user.ID)
				var calls atomic.Int64
				if err := db.Callback().Update().Before("gorm:update").Register("recovery_change_baseline", func(tx *gorm.DB) {
					updates, ok := tx.Statement.Dest.(map[string]any)
					if !ok || tx.Statement.Table != "users" {
						return
					}
					if _, ok := updates["totp_enrollment_expires_at"]; !ok {
						return
					}
					calls.Add(1)
					var value any = "FAKE-CHANGED-BASELINE"
					switch column {
					case "role":
						value = "viewer"
					case "token_version":
						value = 8
					case "totp_enrollment_expires_at":
						value = time.Now().UTC().Add(2 * time.Hour)
					case "totp_reenrollment_required":
						value = true
					}
					// column is a closed fixture constant, never external input.
					if err := tx.Exec("UPDATE users SET "+column+" = ? WHERE id = ?", value, user.ID).Error; err != nil {
						_ = tx.AddError(err)
					}
				}); err != nil {
					t.Fatal("register identity baseline conflict")
				}
				if err := RecoverAccount(context.Background(), db, accountRecoveryRequest(t, user)); err != ErrAccountRecoveryUnknown {
					t.Fatal("identity baseline conflict was not unknown")
				}
				if calls.Load() != 1 {
					t.Fatal("identity CAS conflict was retried or not injected")
				}
				assertAccountRecoveryUnchanged(t, db, before)
			})
		}
	})
	t.Run("audit continuity preserves prior history", func(t *testing.T) {
		db := open(t)
		user := seedAccountRecoveryTarget(t, db, "operator", true, false)
		first := accountRecoveryRequest(t, user)
		if err := RecoverAccount(context.Background(), db, first); err != nil {
			t.Fatal(err)
		}
		var original model.AuditLog
		if err := db.Take(&original).Error; err != nil {
			t.Fatal("read original hash entry")
		}
		second := accountRecoveryRequest(t, user)
		second.ResetPassword, second.ResetTOTP, second.NewPassword = true, false, accountRecoveryTestPassword
		if err := RecoverAccount(context.Background(), db, second); err != nil {
			t.Fatal(err)
		}
		var last model.AuditLog
		if err := db.Order("id DESC").Take(&last).Error; err != nil {
			t.Fatal("read chained entry")
		}
		if last.PrevHash != original.EntryHash || last.EntryHash == "" || last.EntryHash == original.EntryHash || recoveryCount(t, db, "account_recovery_audits") != 2 {
			t.Fatal("repeated explicit recoveries did not preserve audit chain")
		}
		if found, err := ReadAccountRecoveryStatus(context.Background(), db, first.OperationID); err != nil || !found {
			t.Fatal("earlier committed operation lost observation")
		}
	})
}

func TestAccountRecoveryNeverInitializesDatabase(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal("open empty SQLite database")
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal("get empty SQLite pool")
	}
	defer func() { _ = sqlDB.Close() }()
	t.Setenv("XIRANG_BREAK_GLASS_CONFIRMATION", accountRecoveryTestConfirmation)
	request := AccountRecoveryRequest{OperationID: strings.Repeat("a", 32), Username: "absent", Reason: "controlled recovery", Confirmation: accountRecoveryTestConfirmation, ResetTOTP: true}
	if err := RecoverAccount(context.Background(), db, request); err != ErrAccountRecoverySchema {
		t.Fatal("empty database was accepted")
	}
	if _, err := ReadAccountRecoveryStatus(context.Background(), db, request.OperationID); err != ErrAccountRecoverySchema {
		t.Fatal("status accepted empty database")
	}
	var tables int
	if err := sqlDB.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type = 'table'").Scan(&tables); err != nil || tables != 0 {
		t.Fatal("exported recovery API initialized storage")
	}
}
