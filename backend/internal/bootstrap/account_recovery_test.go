package bootstrap

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"xirang/backend/internal/auth"
	"xirang/backend/internal/database"
	"xirang/backend/internal/model"
	"xirang/backend/internal/secure"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const accountRecoveryTestConfirmation = "FAKE_OFFLINE_RECOVERY_CONFIRMATION_ONLY"
const accountRecoveryTestPassword = "  New-Recovery-Password123!  "
const accountRecoveryTestKey = "RkFLRV9BQ0NPVU5UX1JFQ09WRVJZX0tFWV9PTkxZXzEyMzQ1Ng=="

type accountRecoveryOpen func(*testing.T) *gorm.DB

func openAccountRecoverySQLite(t *testing.T) *gorm.DB {
	t.Helper()
	// Keep synthetic credential-bearing fixtures inside the checkout's ignored
	// scratch tree, never the system temporary directory.
	base := filepath.Join("..", "..", "..", ".tmp", "agent")
	if err := os.MkdirAll(base, 0700); err != nil {
		t.Fatal("create recovery fixture scratch directory")
	}
	directory, err := os.MkdirTemp(base, "account-recovery-")
	if err != nil {
		t.Fatal("create recovery fixture directory")
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	db, err := gorm.Open(sqlite.Open(filepath.Join(directory, "recovery.db")+"?_foreign_keys=on&_loc=UTC&_txlock=immediate&_busy_timeout=5000"), &gorm.Config{Logger: logger.Discard, NowFunc: func() time.Time { return time.Now().UTC().Truncate(time.Microsecond) }})
	if err != nil {
		t.Fatal("open isolated SQLite recovery database")
	}
	finishAccountRecoveryFixture(t, db)
	return db
}

func finishAccountRecoveryFixture(t *testing.T, db *gorm.DB) {
	t.Helper()
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal("get recovery fixture connection")
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := database.RunMigrations(db, db.Name()); err != nil {
		t.Fatalf("migrate isolated recovery fixture: %v", err)
	}
	t.Setenv("XIRANG_BREAK_GLASS_CONFIRMATION", accountRecoveryTestConfirmation)
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", accountRecoveryTestKey)
	t.Setenv("DATA_ENCRYPTION_LEGACY_KEY", "")
	secure.ResetForTesting()
	t.Cleanup(secure.ResetForTesting)
}

func accountRecoveryRequest(t *testing.T, user model.User) AccountRecoveryRequest {
	t.Helper()
	id, err := GenerateAccountRecoveryOperationID()
	if err != nil {
		t.Fatal("generate recovery operation ID")
	}
	return AccountRecoveryRequest{OperationID: id, Username: user.Username, Reason: "  controlled synthetic account recovery  ", Confirmation: accountRecoveryTestConfirmation, ResetTOTP: true}
}

func seedAccountRecoveryTarget(t *testing.T, db *gorm.DB, role string, enabled, required bool) model.User {
	t.Helper()
	expires := time.Now().UTC().Add(time.Minute).Truncate(time.Microsecond)
	user := model.User{Username: "recovery-target", PasswordHash: "original-password-hash", Role: role,
		TOTPEnabled: enabled, TOTPReenrollmentRequired: required, TOTPSecret: "FAKE-ORIGINAL-SECRET",
		RecoveryCodes: `["FAKE-ORIGINAL-RECOVERY-HASH"]`, TokenVersion: 7,
		TOTPEnrollmentID: "original-enrollment", TOTPEnrollmentExpiresAt: &expires, Onboarded: true}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal("seed recovery target")
	}
	pending := []model.PendingAuthToken{
		{JTI: "11111111111111111111111111111111", UserID: user.ID, TokenVersion: 7, TOTPBinding: "original-binding", ExpiresAt: expires, CreatedAt: expires.Add(-time.Minute)},
		{JTI: "22222222222222222222222222222222", UserID: user.ID, TokenVersion: 6, TOTPBinding: "historical-binding", ExpiresAt: expires.Add(-time.Hour), CreatedAt: expires.Add(-2 * time.Hour)},
	}
	if err := db.Create(&pending).Error; err != nil {
		t.Fatal("seed pending tokens")
	}
	return user
}

func recoveryIdentitySnapshot(t *testing.T, db *gorm.DB, id uint) accountRecoveryIdentity {
	t.Helper()
	var row accountRecoveryIdentity
	if err := db.Table("users").Where("id = ?", id).Take(&row).Error; err != nil {
		t.Fatal("read raw recovery identity")
	}
	return row
}

func recoveryCount(t *testing.T, db *gorm.DB, table string) int64 {
	t.Helper()
	var count int64
	if err := db.Table(table).Count(&count).Error; err != nil {
		t.Fatalf("count %s", table)
	}
	return count
}

func assertAccountRecoveryUnchanged(t *testing.T, db *gorm.DB, before accountRecoveryIdentity) {
	t.Helper()
	if got := recoveryIdentitySnapshot(t, db, before.ID); !reflect.DeepEqual(before, got) {
		t.Fatal("rejected recovery changed identity")
	}
	if recoveryCount(t, db, "auth_pending_tokens") != 2 || recoveryCount(t, db, "account_recovery_audits") != 0 || recoveryCount(t, db, "audit_logs") != 0 {
		t.Fatal("rejected recovery changed pending tokens or audit history")
	}
}

func TestAccountRecoverySQLite(t *testing.T) {
	testAccountRecovery(t, openAccountRecoverySQLite)
}

func testAccountRecovery(t *testing.T, open accountRecoveryOpen) {
	t.Helper()
	for _, mode := range []string{"password", "factor", "both"} {
		for _, role := range []string{"admin", "operator", "viewer"} {
			t.Run(mode+"/"+role, func(t *testing.T) {
				db := open(t)
				user := seedAccountRecoveryTarget(t, db, role, true, false)
				// admin is the sole admin; viewer/operator exercise zero-admin DBs.
				before := recoveryIdentitySnapshot(t, db, user.ID)
				request := accountRecoveryRequest(t, user)
				request.ResetPassword = mode != "factor"
				request.ResetTOTP = mode != "password"
				if request.ResetPassword {
					request.NewPassword = accountRecoveryTestPassword
				}
				if err := RecoverAccount(context.Background(), db, request); err != nil {
					t.Fatalf("recover selected modes: %v", err)
				}
				after := recoveryIdentitySnapshot(t, db, user.ID)
				if after.ID != before.ID || after.Username != before.Username || after.Role != before.Role || after.TokenVersion != before.TokenVersion+1 {
					t.Fatal("recovery changed identity/role or failed to bump version once")
				}
				if request.ResetPassword {
					if auth.CheckPassword(after.PasswordHash, accountRecoveryTestPassword) != nil || auth.CheckPassword(after.PasswordHash, strings.TrimSpace(accountRecoveryTestPassword)) == nil {
						t.Fatal("new password did not preserve original bytes")
					}
				} else if after.PasswordHash != before.PasswordHash {
					t.Fatal("factor reset changed password")
				}
				if request.ResetTOTP {
					if after.TOTPEnabled || !after.TOTPReenrollmentRequired || after.TOTPSecret.String != "" || after.RecoveryCodes.String != "" {
						t.Fatal("factor reset failed closed reenrollment state")
					}
				} else if !after.TOTPEnabled || after.TOTPReenrollmentRequired || after.TOTPSecret != before.TOTPSecret || after.RecoveryCodes != before.RecoveryCodes {
					t.Fatal("password-only reset changed the enabled factor or ciphertext inventory")
				}
				if after.TOTPEnrollmentID.String != "" || after.TOTPEnrollmentExpiresAt.Valid || recoveryCount(t, db, "auth_pending_tokens") != 0 {
					t.Fatal("unfinished enrollment or pending login survived")
				}
				var audit model.AccountRecoveryAudit
				if err := db.Take(&audit).Error; err != nil {
					t.Fatal("load dedicated audit")
				}
				if audit.OperationID != request.OperationID || audit.TargetUserID != user.ID || audit.TargetUsername != user.Username || audit.Event != model.AccountRecoveryEventReset || audit.ResetPassword != request.ResetPassword || audit.ResetTOTP != request.ResetTOTP || audit.Reason != strings.TrimSpace(request.Reason) || audit.OldTokenVersion != 7 || audit.NewTokenVersion != 8 || audit.CreatedAt.IsZero() {
					t.Fatal("dedicated audit contract mismatch")
				}
				var general model.AuditLog
				if err := db.Take(&general).Error; err != nil {
					t.Fatal("load general audit")
				}
				if general.UserID != user.ID || general.Role != role || general.Method != "CLI" || general.Path != "break-glass/account-recover" || general.ClientIP != "local" || general.UserAgent != "xirang-recover-account" || general.EntryHash == "" {
					t.Fatal("general hash-chain audit contract mismatch")
				}
				committed, err := ReadAccountRecoveryStatus(context.Background(), db, request.OperationID)
				if err != nil || !committed {
					t.Fatal("committed operation was not observed")
				}
				if err := RecoverAccount(context.Background(), db, request); !errors.Is(err, ErrAccountRecoveryDuplicateOperation) {
					t.Fatal("operation ID replay was not explicitly rejected")
				}
				if got := recoveryIdentitySnapshot(t, db, user.ID); !reflect.DeepEqual(got, after) || recoveryCount(t, db, "account_recovery_audits") != 1 {
					t.Fatal("duplicate ID repeated recovery")
				}
			})
		}
	}
	for _, required := range []bool{false, true} {
		t.Run(fmt.Sprintf("password-only clears pending factor preserves required=%v", required), func(t *testing.T) {
			db := open(t)
			user := seedAccountRecoveryTarget(t, db, "operator", false, required)
			before := recoveryIdentitySnapshot(t, db, user.ID)
			request := accountRecoveryRequest(t, user)
			request.ResetPassword, request.ResetTOTP, request.NewPassword = true, false, accountRecoveryTestPassword
			if err := RecoverAccount(context.Background(), db, request); err != nil {
				t.Fatal(err)
			}
			after := recoveryIdentitySnapshot(t, db, user.ID)
			if after.TOTPEnabled || after.TOTPReenrollmentRequired != required || after.TOTPSecret.String != "" || after.RecoveryCodes != before.RecoveryCodes {
				t.Fatal("password-only pending cleanup changed existing gate or inventory")
			}
		})
	}
	testAccountRecoveryRejections(t, open)
	testAccountRecoveryGrants(t, open)
	testAccountRecoveryRollback(t, open)
	testAccountRecoveryCommit(t, open)
	testAccountRecoverySecurity(t, open)
}

func testAccountRecoveryRejections(t *testing.T, open accountRecoveryOpen) {
	t.Helper()
	cases := []struct {
		name   string
		change func(*AccountRecoveryRequest)
		want   error
	}{
		{"no modes", func(r *AccountRecoveryRequest) { r.ResetTOTP = false }, ErrAccountRecoveryRequest},
		{"unknown target", func(r *AccountRecoveryRequest) { r.Username = "does-not-exist" }, ErrAccountRecoveryTarget},
		{"empty username", func(r *AccountRecoveryRequest) { r.Username = "  " }, ErrAccountRecoveryRequest},
		{"wrong confirmation", func(r *AccountRecoveryRequest) { r.Confirmation = "FAKE-WRONG-CONFIRMATION" }, ErrAccountRecoveryConfirmation},
		{"short reason", func(r *AccountRecoveryRequest) { r.Reason = "short" }, ErrAccountRecoveryReason},
		{"long reason", func(r *AccountRecoveryRequest) { r.Reason = strings.Repeat("x", 513) }, ErrAccountRecoveryReason},
		{"control reason", func(r *AccountRecoveryRequest) { r.Reason = "recovery\nreason" }, ErrAccountRecoveryReason},
		{"invalid ID", func(r *AccountRecoveryRequest) { r.OperationID = strings.Repeat("A", 32) }, ErrAccountRecoveryOperationID},
		{"weak password", func(r *AccountRecoveryRequest) { r.ResetPassword = true; r.NewPassword = "weak" }, ErrAccountRecoveryPassword},
		{"bcrypt length", func(r *AccountRecoveryRequest) {
			r.ResetPassword = true
			r.NewPassword = "Aa1!" + strings.Repeat("x", 73)
		}, ErrAccountRecoveryPassword},
		{"unselected password", func(r *AccountRecoveryRequest) { r.NewPassword = accountRecoveryTestPassword }, ErrAccountRecoveryRequest},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			db := open(t)
			user := seedAccountRecoveryTarget(t, db, "operator", true, false)
			before := recoveryIdentitySnapshot(t, db, user.ID)
			request := accountRecoveryRequest(t, user)
			test.change(&request)
			if err := RecoverAccount(context.Background(), db, request); !errors.Is(err, test.want) {
				t.Fatalf("expected safe rejection %v, got %v", test.want, err)
			}
			assertAccountRecoveryUnchanged(t, db, before)
		})
	}
	for _, field := range []string{"totp_secret", "recovery_codes"} {
		t.Run("wrong key "+field, func(t *testing.T) {
			db := open(t)
			user := seedAccountRecoveryTarget(t, db, "operator", true, false)
			// Isolate each encrypted-field check, including inventory-only users.
			other := "recovery_codes"
			if field == other {
				other = "totp_secret"
			}
			if err := db.Table("users").Where("id = ?", user.ID).Update(other, "").Error; err != nil {
				t.Fatal("clear other encrypted field")
			}
			before := recoveryIdentitySnapshot(t, db, user.ID)
			t.Setenv("DATA_ENCRYPTION_KEY", "RkFLRV9XUk9OR19FTkNSWVBUSU9OX0tFWV9PTkxZXzEyMzQ1Ng==")
			secure.ResetForTesting()
			if err := RecoverAccount(context.Background(), db, accountRecoveryRequest(t, user)); !errors.Is(err, ErrAccountRecoveryKey) {
				t.Fatal("wrong key did not fail closed")
			}
			assertAccountRecoveryUnchanged(t, db, before)
		})
	}
	for _, state := range []string{"dirty", "old", "future", "missing identity column"} {
		t.Run("schema "+state, func(t *testing.T) {
			db := open(t)
			user := seedAccountRecoveryTarget(t, db, "operator", true, false)
			var err error
			switch state {
			case "dirty":
				err = db.Exec("UPDATE schema_migrations SET dirty = ?", true).Error
			case "old":
				err = db.Exec("UPDATE schema_migrations SET version = version - 1").Error
			case "future":
				err = db.Exec("UPDATE schema_migrations SET version = version + 1").Error
			default:
				err = db.Exec("ALTER TABLE users DROP COLUMN password_hash").Error
			}
			if err != nil {
				t.Fatal("prepare schema rejection")
			}
			request := accountRecoveryRequest(t, user)
			if err := RecoverAccount(context.Background(), db, request); !errors.Is(err, ErrAccountRecoverySchema) {
				t.Fatal("recovery accepted invalid schema")
			}
			if _, err := ReadAccountRecoveryStatus(context.Background(), db, request.OperationID); !errors.Is(err, ErrAccountRecoverySchema) {
				t.Fatal("status accepted invalid schema")
			}
			if recoveryCount(t, db, "auth_pending_tokens") != 2 || recoveryCount(t, db, "account_recovery_audits") != 0 {
				t.Fatal("schema rejection mutated storage")
			}
		})
	}
	t.Run("unknown role", func(t *testing.T) {
		db := open(t)
		user := seedAccountRecoveryTarget(t, db, "future-role", true, false)
		before := recoveryIdentitySnapshot(t, db, user.ID)
		if err := RecoverAccount(context.Background(), db, accountRecoveryRequest(t, user)); !errors.Is(err, ErrAccountRecoveryTarget) {
			t.Fatal("unknown role accepted")
		}
		assertAccountRecoveryUnchanged(t, db, before)
	})
	t.Run("missing confirmation provision", func(t *testing.T) {
		db := open(t)
		user := seedAccountRecoveryTarget(t, db, "viewer", true, false)
		t.Setenv("XIRANG_BREAK_GLASS_CONFIRMATION", "")
		if err := RecoverAccount(context.Background(), db, accountRecoveryRequest(t, user)); !errors.Is(err, ErrAccountRecoveryConfirmation) {
			t.Fatal("unprovisioned confirmation accepted")
		}
	})
	t.Run("status not found and invalid ID", func(t *testing.T) {
		db := open(t)
		id, _ := GenerateAccountRecoveryOperationID()
		if found, err := ReadAccountRecoveryStatus(context.Background(), db, id); err != nil || found {
			t.Fatal("unobserved operation must return false without error")
		}
		if _, err := ReadAccountRecoveryStatus(context.Background(), db, "invalid"); !errors.Is(err, ErrAccountRecoveryOperationID) {
			t.Fatal("status accepted invalid ID")
		}
	})
}

func testAccountRecoveryGrants(t *testing.T, open accountRecoveryOpen) {
	t.Helper()
	t.Run("grant history and business relationships", func(t *testing.T) {
		db := open(t)
		user := seedAccountRecoveryTarget(t, db, "operator", true, false)
		admin := model.User{Username: "unaffected-admin", Role: "admin", PasswordHash: "unaffected-hash"}
		if err := db.Create(&admin).Error; err != nil {
			t.Fatal("seed other admin")
		}
		otherBefore := recoveryIdentitySnapshot(t, db, admin.ID)
		now := time.Now().UTC().Truncate(time.Microsecond)
		statuses := []string{"active", "approved", "pending", "future-state", "", "denied", "revoked", "expired"}
		var grants []model.CredentialAccessGrant
		for _, status := range statuses {
			grant := model.CredentialAccessGrant{RequesterUserID: user.ID, RequesterUsername: user.Username, RequesterRole: user.Role, Action: "fixture", Purpose: "fixture", Reason: "preserved grant history", Status: status, RequestedAt: now.Add(-time.Hour), ExpiresAt: now.Add(time.Hour), RevokedByUserID: &admin.ID}
			if status == "revoked" {
				old := now.Add(-time.Hour)
				grant.RevokedAt = &old
			}
			if err := db.Create(&grant).Error; err != nil {
				t.Fatal("seed grant history")
			}
			grants = append(grants, grant)
		}
		other := model.CredentialAccessGrant{RequesterUserID: admin.ID, Action: "fixture", Purpose: "fixture", Status: "active", RequestedAt: now, ExpiresAt: now.Add(time.Hour)}
		if err := db.Create(&other).Error; err != nil {
			t.Fatal("seed other user's grant")
		}
		unaffectedPending := model.PendingAuthToken{JTI: "33333333333333333333333333333333", UserID: admin.ID, TokenVersion: admin.TokenVersion, TOTPBinding: "other-binding", ExpiresAt: now.Add(time.Hour), CreatedAt: now}
		if err := db.Create(&unaffectedPending).Error; err != nil {
			t.Fatal("seed other user's pending login")
		}
		// Real existing business relation, not an auxiliary fixture-only table.
		if err := db.Exec("INSERT INTO nodes (id, name, host, username, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)", 501, "recovery-node", "127.0.0.1", "fixture", now, now).Error; err != nil {
			t.Fatal("seed business node")
		}
		if err := db.Exec("INSERT INTO tasks (id, name, node_id, command, executor_type, status, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)", 502, "recovery-task", 501, "true", "command", "pending", now, now).Error; err != nil {
			t.Fatal("seed business task")
		}
		owner := model.NodeOwner{NodeID: 501, UserID: user.ID, CreatedAt: now}
		if err := db.Create(&owner).Error; err != nil {
			t.Fatal("seed original node ownership")
		}
		if err := RecoverAccount(context.Background(), db, accountRecoveryRequest(t, user)); err != nil {
			t.Fatal(err)
		}
		for index, original := range grants {
			var got model.CredentialAccessGrant
			if err := db.First(&got, original.ID).Error; err != nil {
				t.Fatal("grant history deleted")
			}
			if index < 5 {
				if got.Status != "revoked" || got.RevokedAt == nil || got.RevokedByUserID != nil {
					t.Fatal("nonterminal/unknown grant was not locally revoked")
				}
				got.Status, got.RevokedAt, got.RevokedByUserID, got.UpdatedAt = original.Status, original.RevokedAt, original.RevokedByUserID, original.UpdatedAt
			}
			if !reflect.DeepEqual(normalizeAccountRecoveryGrant(got), normalizeAccountRecoveryGrant(original)) {
				t.Fatal("grant history fields were rewritten")
			}
		}
		var audit model.AccountRecoveryAudit
		if err := db.Take(&audit).Error; err != nil || audit.RevokedGrantCount != 5 {
			t.Fatal("revoked grant audit count mismatch")
		}
		var otherGot model.CredentialAccessGrant
		if err := db.First(&otherGot, other.ID).Error; err != nil || !reflect.DeepEqual(normalizeAccountRecoveryGrant(otherGot), normalizeAccountRecoveryGrant(other)) {
			t.Fatal("other user's grant changed")
		}
		if !reflect.DeepEqual(recoveryIdentitySnapshot(t, db, admin.ID), otherBefore) {
			t.Fatal("other user's identity changed")
		}
		var relation struct {
			ID      uint
			NodeID  uint
			Name    string
			Command string
		}
		if err := db.Table("tasks").Select("id", "node_id", "name", "command").Where("id = ?", 502).Take(&relation).Error; err != nil || relation.ID != 502 || relation.NodeID != 501 || relation.Name != "recovery-task" || relation.Command != "true" {
			t.Fatal("business relationship changed")
		}
		var onboarded bool
		if err := db.Table("users").Select("onboarded").Where("id = ?", user.ID).Scan(&onboarded).Error; err != nil || !onboarded {
			t.Fatal("user business metadata changed")
		}
		var ownerAfter model.NodeOwner
		if err := db.Where("node_id = ? AND user_id = ?", 501, user.ID).Take(&ownerAfter).Error; err != nil || ownerAfter.NodeID != owner.NodeID || ownerAfter.UserID != owner.UserID || !ownerAfter.CreatedAt.Equal(owner.CreatedAt) {
			t.Fatal("original account's node ownership changed")
		}
		var pendingAfter model.PendingAuthToken
		if err := db.Where("jti = ?", unaffectedPending.JTI).Take(&pendingAfter).Error; err != nil || pendingAfter.UserID != admin.ID || recoveryCount(t, db, "auth_pending_tokens") != 1 {
			t.Fatal("recovery removed another user's pending login")
		}
	})
}

func testAccountRecoveryRollback(t *testing.T, open accountRecoveryOpen) {
	t.Helper()
	for _, failure := range []string{"dedicated audit", "hash-chain audit", "ciphertext CAS"} {
		t.Run("rollback "+failure, func(t *testing.T) {
			db := open(t)
			user := seedAccountRecoveryTarget(t, db, "operator", true, false)
			before := recoveryIdentitySnapshot(t, db, user.ID)
			grant := model.CredentialAccessGrant{RequesterUserID: user.ID, Action: "fixture", Purpose: "fixture", Status: "active", RequestedAt: time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(time.Hour)}
			if err := db.Create(&grant).Error; err != nil {
				t.Fatal("seed rollback grant")
			}
			var calls atomic.Int64
			if failure == "ciphertext CAS" {
				if err := db.Callback().Update().Before("gorm:update").Register("account_recovery_cas_failure", func(tx *gorm.DB) {
					updates, ok := tx.Statement.Dest.(map[string]any)
					if tx.Statement.Table != "users" || !ok {
						return
					}
					if _, ok := updates["totp_enrollment_expires_at"]; !ok {
						return
					}
					calls.Add(1)
					if err := tx.Exec("UPDATE users SET recovery_codes = ? WHERE id = ?", "FAKE-CONCURRENT-CIPHERTEXT", user.ID).Error; err != nil {
						_ = tx.AddError(err)
					}
				}); err != nil {
					t.Fatal("register CAS failure")
				}
			} else {
				table := "account_recovery_audits"
				if failure == "hash-chain audit" {
					table = "audit_logs"
				}
				if err := db.Callback().Create().Before("gorm:create").Register("account_recovery_audit_failure", func(tx *gorm.DB) {
					if tx.Statement.Table == table {
						calls.Add(1)
						_ = tx.AddError(errors.New("FAKE-SENSITIVE-DB-FAILURE"))
					}
				}); err != nil {
					t.Fatal("register audit failure")
				}
			}
			if err := RecoverAccount(context.Background(), db, accountRecoveryRequest(t, user)); err != ErrAccountRecoveryUnknown {
				t.Fatal("transaction failure was not fixed unknown error")
			}
			if calls.Load() != 1 {
				t.Fatal("failed transaction was retried or failure not exercised")
			}
			assertAccountRecoveryUnchanged(t, db, before)
			var got model.CredentialAccessGrant
			if err := db.First(&got, grant.ID).Error; err != nil || got.Status != "active" || got.RevokedAt != nil {
				t.Fatal("audit/CAS failure left revoked grant")
			}
		})
	}
}

// Wrap the real engine transaction to model both uncommitted failure and a
// durable COMMIT whose acknowledgement is lost. No production hooks or retries.
type accountRecoveryCommitPool struct {
	gorm.ConnPool
	sqlDB *sql.DB
	after bool
	calls *atomic.Int64
}

func (p *accountRecoveryCommitPool) GetDBConn() (*sql.DB, error) { return p.sqlDB, nil }
func (p *accountRecoveryCommitPool) BeginTx(ctx context.Context, options *sql.TxOptions) (gorm.ConnPool, error) {
	tx, err := p.sqlDB.BeginTx(ctx, options)
	if err != nil {
		return nil, err
	}
	return &accountRecoveryCommitTx{ConnPool: tx, tx: tx, after: p.after, calls: p.calls}, nil
}

type accountRecoveryCommitTx struct {
	gorm.ConnPool
	tx    *sql.Tx
	after bool
	calls *atomic.Int64
}

func (tx *accountRecoveryCommitTx) Commit() error {
	tx.calls.Add(1)
	if tx.after {
		if err := tx.tx.Commit(); err != nil {
			return err
		}
	} else {
		_ = tx.tx.Rollback()
	}
	return errors.New("FAKE-SENSITIVE-COMMIT-ACKNOWLEDGEMENT-FAILURE")
}
func (tx *accountRecoveryCommitTx) Rollback() error { return tx.tx.Rollback() }

func testAccountRecoveryCommit(t *testing.T, open accountRecoveryOpen) {
	t.Helper()
	for _, after := range []bool{false, true} {
		t.Run(fmt.Sprintf("unknown commit durable=%v", after), func(t *testing.T) {
			db := open(t)
			user := seedAccountRecoveryTarget(t, db, "admin", true, false)
			before := recoveryIdentitySnapshot(t, db, user.ID)
			sqlDB, err := db.DB()
			if err != nil {
				t.Fatal("get commit fixture DB")
			}
			var commits atomic.Int64
			wrapped := db.Session(&gorm.Session{NewDB: true, Context: context.Background()})
			wrapped.Statement.ConnPool = &accountRecoveryCommitPool{ConnPool: sqlDB, sqlDB: sqlDB, after: after, calls: &commits}
			request := accountRecoveryRequest(t, user)
			if err := RecoverAccount(context.Background(), wrapped, request); err != ErrAccountRecoveryUnknown {
				t.Fatal("commit acknowledgement failure did not remain unknown")
			}
			if commits.Load() != 1 {
				t.Fatal("unknown COMMIT was automatically replayed")
			}
			observed, err := ReadAccountRecoveryStatus(context.Background(), db, request.OperationID)
			if err != nil || observed != after {
				t.Fatal("status did not reflect actual durability")
			}
			if !after {
				assertAccountRecoveryUnchanged(t, db, before)
			} else if got := recoveryIdentitySnapshot(t, db, user.ID); got.TokenVersion != before.TokenVersion+1 || !got.TOTPReenrollmentRequired || recoveryCount(t, db, "audit_logs") != 1 {
				t.Fatal("durable unknown operation lost atomic state")
			}
		})
	}
}

func TestAccountRecoveryOperationIDsAndNilDB(t *testing.T) {
	seen := make(map[string]bool)
	for range 64 {
		id, err := GenerateAccountRecoveryOperationID()
		if err != nil || !validAccountRecoveryOperationID(id) || seen[id] {
			t.Fatal("invalid or repeated random operation ID")
		}
		seen[id] = true
	}
	if err := RecoverAccount(context.Background(), nil, AccountRecoveryRequest{}); err != ErrAccountRecoveryRequest {
		t.Fatal("nil recovery database accepted")
	}
	if _, err := ReadAccountRecoveryStatus(context.Background(), nil, strings.Repeat("a", 32)); err != ErrAccountRecoveryStatus {
		t.Fatal("nil status database accepted")
	}
}

func normalizeAccountRecoveryGrant(grant model.CredentialAccessGrant) model.CredentialAccessGrant {
	grant.RequestedAt = grant.RequestedAt.UTC()
	grant.ExpiresAt = grant.ExpiresAt.UTC()
	grant.CreatedAt = grant.CreatedAt.UTC()
	grant.UpdatedAt = grant.UpdatedAt.UTC()
	if grant.ApprovedAt != nil {
		normalized := grant.ApprovedAt.UTC()
		grant.ApprovedAt = &normalized
	}
	if grant.RevokedAt != nil {
		normalized := grant.RevokedAt.UTC()
		grant.RevokedAt = &normalized
	}
	return grant
}
