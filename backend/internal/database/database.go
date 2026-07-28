package database

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"xirang/backend/internal/config"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/mattn/go-sqlite3"
	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func configurePool(db *gorm.DB) error {
	sqlDB, err := db.DB()
	if err != nil {
		return err
	}
	sqlDB.SetMaxOpenConns(25)
	sqlDB.SetMaxIdleConns(10)
	sqlDB.SetConnMaxLifetime(5 * time.Minute)
	return nil
}

// sqlitePragmas are set on the SQLite DSN so each pooled connection
// gets them at open time (they are per-connection in mattn/go-sqlite3).
// _journal_mode=WAL enables reader/writer concurrency; _txlock=immediate
// makes BEGIN take a write lock up front so concurrent writers serialize
// via _busy_timeout (5s) instead of failing at COMMIT with SQLITE_BUSY.
// _loc=UTC tells the driver to interpret stored DATETIME strings as UTC
// when scanning into time.Time, complementing GORM's UTC NowFunc so
// timestamps round-trip without timezone drift. See migration 000050 and
// docs/deployment.md#utc-时间戳约定.
var sqlitePragmas = url.Values{
	"_journal_mode": {"WAL"},
	"_busy_timeout": {"5000"},
	"_foreign_keys": {"ON"},
	"_synchronous":  {"NORMAL"},
	"_txlock":       {"immediate"},
	"_loc":          {"UTC"},
}

func buildSQLiteDSN(path string) string {
	dsn, err := buildSQLiteDSNWithError(path)
	if err != nil {
		return path
	}
	return dsn
}

func buildSQLiteDSNWithError(path string) (string, error) {
	pathAndQuery := path
	fragment := ""
	hasFragment := false
	if strings.HasPrefix(path, "file:") {
		pathAndQuery, fragment, hasFragment = strings.Cut(path, "#")
	}
	base, rawQuery, _ := strings.Cut(pathAndQuery, "?")
	query, err := url.ParseQuery(rawQuery)
	if err != nil {
		return "", fmt.Errorf("解析 sqlite DSN query 失败: %w", err)
	}
	for _, alias := range []string{"_timeout", "_fk", "_journal", "_sync"} {
		query.Del(alias)
	}
	for key, values := range sqlitePragmas {
		query.Set(key, values[0])
	}

	dsn := base + "?" + query.Encode()
	if hasFragment {
		// go-sqlite3 parses everything after '?' as query parameters. Keep an
		// empty query field before the URI fragment so it cannot suffix the
		// final protected option while SQLite still ignores the fragment.
		dsn += "&#" + fragment
	}
	return dsn, nil
}

// buildPostgresDSN ensures the PostgreSQL DSN includes timezone=UTC so the
// session always interprets/returns TIMESTAMP values in UTC. If the caller
// already specified a timezone we leave it untouched (operators may have a
// reason, and overriding silently would be surprising).
func buildPostgresDSN(dsn string) string {
	if dsn == "" {
		return dsn
	}
	if strings.Contains(dsn, "timezone=") || strings.Contains(dsn, "TimeZone=") {
		return dsn
	}
	// Postgres accepts both URL ("postgres://...?key=val") and key/value
	// ("host=... port=... dbname=...") DSNs. We append using the matching
	// separator instead of trying to parse.
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		sep := "?"
		if strings.Contains(dsn, "?") {
			sep = "&"
		}
		return dsn + sep + "timezone=UTC"
	}
	sep := " "
	if strings.HasSuffix(dsn, " ") || dsn == "" {
		sep = ""
	}
	return dsn + sep + "timezone=UTC"
}

func openPostgresSQLDB(dsn string) (*sql.DB, error) {
	config, err := pgx.ParseConfig(buildPostgresDSN(dsn))
	if err != nil {
		return nil, fmt.Errorf("解析 postgres DSN 失败: %w", err)
	}

	timezoneName := config.RuntimeParams["timezone"]
	if timezoneName == "" {
		timezoneName = "UTC"
		config.RuntimeParams["timezone"] = timezoneName
	}
	scanLocation, err := time.LoadLocation(timezoneName)
	if err != nil {
		return nil, fmt.Errorf("加载 postgres 时区失败: %w", err)
	}

	return stdlib.OpenDB(*config, stdlib.OptionAfterConnect(func(_ context.Context, conn *pgx.Conn) error {
		conn.TypeMap().RegisterType(&pgtype.Type{
			Name:  "timestamp",
			OID:   pgtype.TimestampOID,
			Codec: &pgtype.TimestampCodec{ScanLocation: scanLocation},
		})
		conn.TypeMap().RegisterType(&pgtype.Type{
			Name:  "timestamptz",
			OID:   pgtype.TimestamptzOID,
			Codec: &pgtype.TimestamptzCodec{ScanLocation: scanLocation},
		})
		return nil
	})), nil
}

func configureSQLitePool(db *gorm.DB) error {
	sqlDB, err := db.DB()
	if err != nil {
		return err
	}
	// PRAGMAs are now embedded in the DSN, so each new connection in the
	// pool auto-applies WAL/busy_timeout/foreign_keys/synchronous on open.
	// WAL allows multiple concurrent readers + one writer; the pool size
	// reflects that. Writes serialize at the SQLite level via
	// _txlock=immediate + _busy_timeout, so multiple Go connections don't
	// race for the write lock.
	sqlDB.SetMaxOpenConns(10)
	sqlDB.SetMaxIdleConns(5)
	sqlDB.SetConnMaxLifetime(5 * time.Minute)
	return nil
}

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
		restoreNeeded := true
		defer func() {
			if !restoreNeeded {
				return
			}
			restoreCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), sqliteBusyRestoreTimeout)
			defer cancel()
			restoreErr := connDB.WithContext(restoreCtx).
				Exec(fmt.Sprintf("PRAGMA busy_timeout = %d", originalBusyTimeout)).Error
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
	for attempt := 0; attempt < sqliteBusyRetryAttempts; attempt++ {
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
	_ = conn.Raw(func(any) error {
		return driver.ErrBadConn
	})
}

// Open opens a database connection based on the provided config, applying the
// appropriate connection pool settings and PRAGMAs for each driver. It also
// registers GORM callback-driven Prometheus metrics.
func Open(cfg config.Config) (*gorm.DB, error) {
	// Wrap GORM's default logger so client-aborted queries (ctx canceled /
	// deadline exceeded) don't get logged at Error level. The panel-query
	// endpoint fires an AbortController on every keystroke by design; those
	// cancellations must not show up as server errors.
	//
	// NowFunc forces GORM-managed CreatedAt/UpdatedAt timestamps to UTC.
	// Combined with SQLite `_loc=UTC` and Postgres `timezone=UTC`, this gives
	// us end-to-end UTC storage so subsequent reads come back with
	// time.Time.Location()==UTC. See migration 000050 for the historical
	// data cutover and docs/deployment.md#utc-时间戳约定 for the runbook.
	gormCfg := &gorm.Config{
		Logger:  newCtxAwareLogger(logger.Default),
		NowFunc: func() time.Time { return time.Now().UTC() },
	}
	switch cfg.DBType {
	case "sqlite":
		dsn, err := buildSQLiteDSNWithError(cfg.SQLitePath)
		if err != nil {
			return nil, fmt.Errorf("构建 sqlite DSN 失败: %w", err)
		}
		db, err := gorm.Open(sqlite.Open(dsn), gormCfg)
		if err != nil {
			return nil, fmt.Errorf("连接 sqlite 失败: %w", err)
		}
		if err := configureSQLitePool(db); err != nil {
			return nil, fmt.Errorf("配置连接池失败: %w", err)
		}
		RegisterMetricsCallbacks(db)
		return db, nil
	case "postgres":
		sqlDB, err := openPostgresSQLDB(cfg.PostgresDSN)
		if err != nil {
			return nil, fmt.Errorf("连接 postgres 失败: %w", err)
		}
		db, err := gorm.Open(postgres.New(postgres.Config{
			DSN:  buildPostgresDSN(cfg.PostgresDSN),
			Conn: sqlDB,
		}), gormCfg)
		if err != nil {
			_ = sqlDB.Close()
			return nil, fmt.Errorf("连接 postgres 失败: %w", err)
		}
		if err := configurePool(db); err != nil {
			_ = sqlDB.Close()
			return nil, fmt.Errorf("配置连接池失败: %w", err)
		}
		RegisterMetricsCallbacks(db)
		return db, nil
	default:
		return nil, fmt.Errorf("不支持的 DB 类型: %s", cfg.DBType)
	}
}
