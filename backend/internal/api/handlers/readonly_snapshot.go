package handlers

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
)

const readonlySnapshotCleanupTimeout = 2 * time.Second

var errReadonlySnapshotUnsupportedDatabase = errors.New("readonly snapshot database dialect is unsupported")

// withReadonlySnapshot binds GORM to one dedicated connection and
// issues the transaction controls explicitly. This is important for the
// production SQLite DSN, whose default _txlock=immediate would otherwise turn
// a normal GORM Transaction into a writer reservation. PostgreSQL gets a
// repeatable-read, read-only snapshot. The callback is only given SELECT-safe
// shared decision functions; no persistence path is reachable here.
func withReadonlySnapshot(ctx context.Context, db *gorm.DB, fn func(*gorm.DB) error) (retErr error) {
	if db == nil || fn == nil {
		return errors.New("readonly snapshot database is unavailable")
	}
	sqlDB, err := db.DB()
	if err != nil {
		return fmt.Errorf("readonly snapshot database is unavailable: %w", err)
	}
	conn, err := sqlDB.Conn(ctx)
	if err != nil {
		return fmt.Errorf("readonly snapshot database connection is unavailable: %w", err)
	}
	defer func() { retErr = errors.Join(retErr, conn.Close()) }()

	dialect := strings.ToLower(strings.TrimSpace(db.Name()))
	beginSQL := "BEGIN"
	switch dialect {
	case "sqlite", "sqlite3":
		// Explicit BEGIN, rather than database/sql BeginTx, is deliberate:
		// go-sqlite3's _txlock=immediate driver setting must not promote this
		// read transaction to a writer reservation.
	case "postgres", "postgresql":
		beginSQL = "BEGIN ISOLATION LEVEL REPEATABLE READ READ ONLY"
	default:
		return errReadonlySnapshotUnsupportedDatabase
	}

	transactionOpen := false
	rollback := func() error {
		if !transactionOpen {
			return nil
		}
		cleanupCtx, cancel := context.WithTimeout(context.Background(), readonlySnapshotCleanupTimeout)
		_, rollbackErr := conn.ExecContext(cleanupCtx, "ROLLBACK")
		cancel()
		transactionOpen = false
		if rollbackErr == nil {
			return nil
		}

		// A failed rollback may leave the raw transaction open. Mark the
		// database/sql connection bad so it cannot return to the pool carrying
		// that transaction state.
		discardErr := conn.Raw(func(any) error {
			return driver.ErrBadConn
		})
		rollbackFailure := fmt.Errorf("readonly snapshot rollback failed: %w", rollbackErr)
		if discardErr == nil {
			return errors.Join(rollbackFailure, errors.New("readonly snapshot connection discard was not confirmed"))
		}
		if !errors.Is(discardErr, driver.ErrBadConn) {
			return errors.Join(rollbackFailure, fmt.Errorf("readonly snapshot connection discard failed: %w", discardErr))
		}
		return rollbackFailure
	}
	defer func() {
		if cleanupErr := rollback(); cleanupErr != nil {
			retErr = errors.Join(retErr, cleanupErr)
		}
	}()

	// A canceled BEGIN may have reached the server even when Exec reports an
	// error. Always clean up that ambiguous outcome before returning the pool
	// connection.
	transactionOpen = true
	if _, err := conn.ExecContext(ctx, beginSQL); err != nil {
		return fmt.Errorf("readonly snapshot transaction is unavailable: %w", err)
	}

	readDB := db.Session(&gorm.Session{NewDB: true, Context: ctx})
	readDB.Statement.ConnPool = conn
	if err := fn(readDB); err != nil {
		return errors.Join(err, rollback())
	}
	if err := ctx.Err(); err != nil {
		return errors.Join(err, rollback())
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return errors.Join(fmt.Errorf("readonly snapshot transaction commit failed: %w", err), rollback())
	}
	transactionOpen = false
	return nil
}
