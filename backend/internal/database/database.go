package database

import (
	"fmt"
	"strings"
	"time"

	"xirang/backend/internal/config"

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

// sqlitePragmas are appended to the SQLite DSN so each pooled connection
// gets them at open time (they are per-connection in mattn/go-sqlite3).
// _journal_mode=WAL enables reader/writer concurrency; _txlock=immediate
// makes BEGIN take a write lock up front so concurrent writers serialize
// via _busy_timeout (5s) instead of failing at COMMIT with SQLITE_BUSY.
// _loc=UTC tells the driver to interpret stored DATETIME strings as UTC
// when scanning into time.Time, complementing GORM's UTC NowFunc so
// timestamps round-trip without timezone drift. See migration 000050 and
// docs/migration-utc-cutover.md.
const sqlitePragmas = "_journal_mode=WAL&_busy_timeout=5000&_foreign_keys=ON&_synchronous=NORMAL&_txlock=immediate&_loc=UTC"

func buildSQLiteDSN(path string) string {
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	return path + sep + sqlitePragmas
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
	// data cutover and docs/migration-utc-cutover.md for the runbook.
	gormCfg := &gorm.Config{
		Logger:  newCtxAwareLogger(logger.Default),
		NowFunc: func() time.Time { return time.Now().UTC() },
	}
	switch cfg.DBType {
	case "sqlite":
		db, err := gorm.Open(sqlite.Open(buildSQLiteDSN(cfg.SQLitePath)), gormCfg)
		if err != nil {
			return nil, fmt.Errorf("连接 sqlite 失败: %w", err)
		}
		if err := configureSQLitePool(db); err != nil {
			return nil, fmt.Errorf("配置连接池失败: %w", err)
		}
		RegisterMetricsCallbacks(db)
		return db, nil
	case "postgres":
		db, err := gorm.Open(postgres.Open(buildPostgresDSN(cfg.PostgresDSN)), gormCfg)
		if err != nil {
			return nil, fmt.Errorf("连接 postgres 失败: %w", err)
		}
		if err := configurePool(db); err != nil {
			return nil, fmt.Errorf("配置连接池失败: %w", err)
		}
		RegisterMetricsCallbacks(db)
		return db, nil
	default:
		return nil, fmt.Errorf("不支持的 DB 类型: %s", cfg.DBType)
	}
}
