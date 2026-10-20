package handlers

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
	"time"

	"xirang/backend/internal/auth"
	"xirang/backend/internal/middleware"
	"xirang/backend/internal/model"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/mattn/go-sqlite3"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	errSSHKeyMutationBusy           = errors.New("ssh key mutation lock busy")
	errSSHKeyMutationSessionInvalid = errors.New("ssh key mutation session invalid")
	errSSHKeyMutationUncertain      = errors.New("ssh key mutation outcome uncertain")
)

func isSSHKeyMutationLockError(err error) bool {
	if err == nil {
		return false
	}
	var postgresErr *pgconn.PgError
	if errors.As(err, &postgresErr) {
		return postgresErr.Code == "55P03" || postgresErr.Code == "40P01"
	}
	var sqliteErr sqlite3.Error
	if errors.As(err, &sqliteErr) {
		return sqliteErr.Code == sqlite3.ErrBusy || sqliteErr.Code == sqlite3.ErrLocked
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "database is locked") || strings.Contains(message, "database table is locked")
}

func isSSHKeyMutationLockErrorForTx(tx *gorm.DB, err error) bool {
	return isSSHKeyMutationLockError(err) || (tx != nil && tx.Name() == "sqlite" &&
		(errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled)))
}

// Only known, rolled-back outcomes may retain a business error identity.
func classifySSHKeyMutationError(db *gorm.DB, err error) error {
	if isSSHKeyMutationLockErrorForTx(db, err) {
		return fmt.Errorf("%w: %v", errSSHKeyMutationBusy, err)
	}
	return err
}

// withSSHKeyMutationTx never retries. A failed commit or cleanup masks all
// business identities: the caller must not invite an automatic resubmission.
func withSSHKeyMutationTx(ctx context.Context, db *gorm.DB, apply func(*gorm.DB) error) error {
	if db == nil || apply == nil {
		return errors.New("SSH key mutation dependency unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if db.Name() == "sqlite" {
		return withSSHKeyMutationSQLiteTx(ctx, db, apply)
	}
	tx := db.WithContext(ctx).Begin(&sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if tx.Error != nil {
		return classifySSHKeyMutationError(db, tx.Error)
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			_ = tx.Rollback().Error
			panic(recovered)
		}
	}()
	bodyErr := tx.Exec("SET LOCAL lock_timeout = '1500ms'").Error
	if bodyErr == nil {
		bodyErr = apply(tx)
	}
	if bodyErr != nil {
		if rollbackErr := tx.Rollback().Error; rollbackErr != nil {
			return fmt.Errorf("%w: rollback failed: %v; operation: %v", errSSHKeyMutationUncertain, rollbackErr, bodyErr)
		}
		return classifySSHKeyMutationError(db, bodyErr)
	}
	if err := tx.Commit().Error; err != nil {
		return fmt.Errorf("%w: commit failed: %v", errSSHKeyMutationUncertain, err)
	}
	return nil
}

func discardSSHKeyMutationSQLiteConnection(conn *sql.Conn) {
	if conn != nil {
		_ = conn.Raw(func(any) error { return driver.ErrBadConn })
	}
}

func withSSHKeyMutationSQLiteTx(ctx context.Context, db *gorm.DB, apply func(*gorm.DB) error) error {
	return db.WithContext(ctx).Connection(func(connDB *gorm.DB) (resultErr error) {
		conn, ok := connDB.Statement.ConnPool.(*sql.Conn)
		if !ok || conn == nil {
			return errors.New("SQLite mutation did not receive a pinned SQL connection")
		}
		// Raw BEGIN avoids database/sql's cancellation-triggered asynchronous
		// rollback; cleanup owns this connection until rollback and restoration.
		tx := connDB.Session(&gorm.Session{SkipDefaultTransaction: true})
		var originalBusyTimeout int64
		if err := tx.Raw("PRAGMA busy_timeout").Scan(&originalBusyTimeout).Error; err != nil {
			return err
		}
		defer func() {
			restoreCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
			defer cancel()
			if err := tx.WithContext(restoreCtx).Exec(fmt.Sprintf("PRAGMA busy_timeout = %d", originalBusyTimeout)).Error; err != nil {
				discardSSHKeyMutationSQLiteConnection(conn)
				resultErr = fmt.Errorf("%w: restore SQLite busy timeout: %v; operation: %v", errSSHKeyMutationUncertain, err, resultErr)
			}
		}()
		if err := tx.Exec("PRAGMA busy_timeout = 0").Error; err != nil {
			return err
		}
		if err := tx.Exec("BEGIN").Error; err != nil {
			return classifySSHKeyMutationError(tx, err)
		}
		rollback := func() error {
			cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
			defer cancel()
			return tx.WithContext(cleanupCtx).Exec("ROLLBACK").Error
		}
		defer func() {
			if recovered := recover(); recovered != nil {
				if err := rollback(); err != nil {
					discardSSHKeyMutationSQLiteConnection(conn)
				}
				panic(recovered)
			}
		}()
		bodyErr := tx.Exec("UPDATE ssh_keys SET id = id WHERE 1 = 0").Error
		if bodyErr == nil {
			bodyErr = apply(tx)
		}
		if bodyErr != nil {
			if err := rollback(); err != nil {
				discardSSHKeyMutationSQLiteConnection(conn)
				return fmt.Errorf("%w: SQLite rollback failed: %v; operation: %v", errSSHKeyMutationUncertain, err, bodyErr)
			}
			return classifySSHKeyMutationError(tx, bodyErr)
		}
		if err := tx.Exec("COMMIT").Error; err != nil {
			// A failed COMMIT may leave the transaction open. Even successful
			// rollback cannot turn an ambiguous commit into a safe rejection.
			if rollbackErr := rollback(); rollbackErr != nil {
				discardSSHKeyMutationSQLiteConnection(conn)
			}
			return fmt.Errorf("%w: SQLite commit failed: %v", errSSHKeyMutationUncertain, err)
		}
		return nil
	})
}

func validateSSHKeyMutationSession(ctx context.Context, db *gorm.DB, manager *auth.JWTManager, binding middleware.SessionBinding) error {
	return validateSSHKeyMutationSessionWithLock(ctx, db, manager, binding, false)
}

func lockAndValidateSSHKeyMutationSession(ctx context.Context, tx *gorm.DB, manager *auth.JWTManager, binding middleware.SessionBinding) error {
	return validateSSHKeyMutationSessionWithLock(ctx, tx, manager, binding, true)
}

func validateSSHKeyMutationSessionWithLock(ctx context.Context, db *gorm.DB, manager *auth.JWTManager, binding middleware.SessionBinding, lock bool) error {
	if db == nil || manager == nil {
		return errors.New("SSH key mutation session verifier unavailable")
	}
	if binding.UserID == 0 || !validStepUpProofID(binding.JTI) || binding.Role != "admin" || !binding.ExpiresAt.After(time.Now().UTC()) {
		return errSSHKeyMutationSessionInvalid
	}
	query := db.WithContext(ctx).Select("id", "role", "token_version", "totp_reenrollment_required")
	if lock {
		query = query.Clauses(clause.Locking{Strength: "SHARE"})
	}
	var user model.User
	if err := query.First(&user, binding.UserID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errSSHKeyMutationSessionInvalid
		}
		return err
	}
	if user.Role != "admin" || user.Role != binding.Role || user.TokenVersion != binding.TokenVersion || user.TOTPReenrollmentRequired {
		return errSSHKeyMutationSessionInvalid
	}
	if lock && db.Name() == "postgres" {
		if err := db.WithContext(ctx).Exec("LOCK TABLE token_revocations IN SHARE MODE").Error; err != nil {
			return err
		}
	}
	// This is a new statement after the revocation fence. The manager may
	// reject a cached revocation but must read durable validity through db.
	revoked, err := manager.IsSessionRevokedWithDB(ctx, db, binding.JTI)
	if err != nil {
		return err
	}
	if revoked || !binding.ExpiresAt.After(time.Now().UTC()) {
		return errSSHKeyMutationSessionInvalid
	}
	return nil
}
