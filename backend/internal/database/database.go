package database

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"xirang/backend/internal/config"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/stdlib"
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
// docs/deployment.md#utc-时间戳约定.
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
