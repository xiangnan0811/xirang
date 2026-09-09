// Package dbtx provides transaction primitives without depending on application
// configuration, migrations, authentication, or database initialization.
package dbtx

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"time"

	"github.com/mattn/go-sqlite3"
	"gorm.io/gorm"
)

const (
	sqliteBusyRetryAttempts       = 8
	sqliteBusyRetryInitialBackoff = 5 * time.Millisecond
	sqliteBusyRetryMaxBackoff     = 50 * time.Millisecond
	sqliteBusyRestoreTimeout      = time.Second
)

// WithSQLiteBusyRetryTx runs body in one transaction per attempt. SQLite uses
// a pinned connection with its local busy timeout disabled so cancellation can
// bound lock contention; the original timeout is restored before release.
func WithSQLiteBusyRetryTx(ctx context.Context, db *gorm.DB, body func(tx *gorm.DB) error) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if db.Dialector == nil || db.Name() != "sqlite" {
		return db.WithContext(ctx).Transaction(body)
	}
	return db.WithContext(ctx).Connection(func(connDB *gorm.DB) (resultErr error) {
		conn, ok := connDB.Statement.ConnPool.(*sql.Conn)
		if !ok || conn == nil {
			return errors.New("sqlite busy retry did not receive a pinned SQL connection")
		}
		var originalBusyTimeout int64
		if err := connDB.WithContext(ctx).Raw("PRAGMA busy_timeout").Scan(&originalBusyTimeout).Error; err != nil {
			return err
		}
		defer func() {
			restoreCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), sqliteBusyRestoreTimeout)
			defer cancel()
			restoreErr := connDB.WithContext(restoreCtx).Exec(fmt.Sprintf("PRAGMA busy_timeout = %d", originalBusyTimeout)).Error
			if restoreErr == nil {
				return
			}
			discardSQLiteConnection(conn)
			restoreErr = fmt.Errorf("restore SQLite busy timeout: %w", restoreErr)
			if resultErr == nil {
				resultErr = restoreErr
				return
			}
			resultErr = errors.Join(resultErr, restoreErr)
		}()
		if err := connDB.WithContext(ctx).Exec("PRAGMA busy_timeout = 0").Error; err != nil {
			return err
		}
		return retrySQLiteBusyTransaction(ctx, connDB, body)
	})
}

func retrySQLiteBusyTransaction(ctx context.Context, db *gorm.DB, body func(tx *gorm.DB) error) error {
	var lastBusyErr error
	for attempt := range sqliteBusyRetryAttempts {
		err := db.WithContext(ctx).Transaction(body)
		if err == nil {
			return nil
		}
		if !isSQLiteBusyOrLocked(err) {
			return err
		}
		lastBusyErr = err
		if ctx.Err() != nil {
			return errors.Join(lastBusyErr, ctx.Err())
		}
		if attempt == sqliteBusyRetryAttempts-1 {
			return lastBusyErr
		}
		if err := waitForSQLiteBusyRetry(ctx, sqliteBusyRetryDelay(attempt)); err != nil {
			return errors.Join(lastBusyErr, err)
		}
	}
	return lastBusyErr
}

func isSQLiteBusyOrLocked(err error) bool {
	var sqliteError sqlite3.Error
	if errors.As(err, &sqliteError) {
		return sqliteError.Code == sqlite3.ErrBusy || sqliteError.Code == sqlite3.ErrLocked
	}
	var sqliteErrorPointer *sqlite3.Error
	if errors.As(err, &sqliteErrorPointer) && sqliteErrorPointer != nil {
		return sqliteErrorPointer.Code == sqlite3.ErrBusy || sqliteErrorPointer.Code == sqlite3.ErrLocked
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

func sqliteBusyRetryDelay(attempt int) time.Duration {
	delay := sqliteBusyRetryInitialBackoff
	for step := 0; step < attempt && delay < sqliteBusyRetryMaxBackoff; step++ {
		delay *= 2
	}
	if delay > sqliteBusyRetryMaxBackoff {
		return sqliteBusyRetryMaxBackoff
	}
	return delay
}

func waitForSQLiteBusyRetry(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func discardSQLiteConnection(conn *sql.Conn) {
	if conn == nil {
		return
	}
	_ = conn.Raw(func(any) error { return driver.ErrBadConn })
}
