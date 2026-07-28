package database

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"xirang/backend/internal/config"

	"github.com/mattn/go-sqlite3"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestOpenSQLiteEnablesWALPragmas(t *testing.T) {
	tempDir := t.TempDir()
	sqlitePath := filepath.Join(tempDir, "xirang-test.db")

	cfg := config.Config{
		DBType:     "sqlite",
		SQLitePath: sqlitePath,
	}
	db, err := Open(cfg)
	if err != nil {
		t.Fatalf("打开 sqlite 数据库失败: %v", err)
	}

	t.Cleanup(func() {
		sqlDB, dbErr := db.DB()
		if dbErr == nil {
			_ = sqlDB.Close()
		}
		_ = os.Remove(sqlitePath)
	})

	var foreignKeys int
	if err := db.Raw("PRAGMA foreign_keys;").Scan(&foreignKeys).Error; err != nil {
		t.Fatalf("查询 foreign_keys 失败: %v", err)
	}
	if foreignKeys != 1 {
		t.Fatalf("期望 foreign_keys=ON(1)，实际: %d", foreignKeys)
	}

	var journalMode string
	if err := db.Raw("PRAGMA journal_mode;").Scan(&journalMode).Error; err != nil {
		t.Fatalf("查询 journal_mode 失败: %v", err)
	}
	if strings.ToLower(strings.TrimSpace(journalMode)) != "wal" {
		t.Fatalf("期望 SQLite journal_mode=wal，实际: %s", journalMode)
	}

	var busyTimeout int
	if err := db.Raw("PRAGMA busy_timeout;").Scan(&busyTimeout).Error; err != nil {
		t.Fatalf("查询 busy_timeout 失败: %v", err)
	}
	if busyTimeout < 5000 {
		t.Fatalf("期望 busy_timeout >= 5000ms，实际: %d", busyTimeout)
	}

	var synchronous int
	if err := db.Raw("PRAGMA synchronous;").Scan(&synchronous).Error; err != nil {
		t.Fatalf("查询 synchronous 失败: %v", err)
	}
	if synchronous != 1 {
		t.Fatalf("期望 synchronous=NORMAL(1)，实际: %d", synchronous)
	}
}

func TestOpenSQLiteOverridesCallerLockPragmas(t *testing.T) {
	tempDir := t.TempDir()
	sqlitePath := filepath.Join(tempDir, "caller-pragmas.db")
	callerDSN := sqlitePath + "?_txlock=deferred&_busy_timeout=0&_timeout=0&_timeout=1&_journal=DELETE&_fk=0&_sync=OFF&cache=shared"

	dsn := buildSQLiteDSN(callerDSN)
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("解析 SQLite DSN 失败: %v", err)
	}

	query := parsed.Query()
	for key, want := range map[string]string{
		"_journal_mode": "WAL",
		"_busy_timeout": "5000",
		"_foreign_keys": "ON",
		"_synchronous":  "NORMAL",
		"_txlock":       "immediate",
		"_loc":          "UTC",
	} {
		got := query[key]
		if len(got) != 1 || got[0] != want {
			t.Fatalf("期望 %s 仅为 %q，实际: %q", key, want, got)
		}
	}
	for _, unsafeAlias := range []string{"_timeout", "_fk", "_journal", "_sync"} {
		if got, exists := query[unsafeAlias]; exists {
			t.Fatalf("期望移除不安全别名 %s，实际: %q", unsafeAlias, got)
		}
	}
	if got := query.Get("cache"); got != "shared" {
		t.Fatalf("期望保留无关 query 参数 cache=shared，实际: %q", got)
	}

	db, err := Open(config.Config{
		DBType:     "sqlite",
		SQLitePath: callerDSN,
	})
	if err != nil {
		t.Fatalf("打开 caller-pragmas SQLite 数据库失败: %v", err)
	}

	t.Cleanup(func() {
		sqlDB, dbErr := db.DB()
		if dbErr == nil {
			_ = sqlDB.Close()
		}
		_ = os.Remove(sqlitePath)
	})

	var busyTimeout int
	if err := db.Raw("PRAGMA busy_timeout;").Scan(&busyTimeout).Error; err != nil {
		t.Fatalf("查询 busy_timeout 失败: %v", err)
	}
	if busyTimeout != 5000 {
		t.Fatalf("期望 caller DSN 不能降低 busy_timeout (=5000ms)，实际: %d", busyTimeout)
	}

	var journalMode string
	if err := db.Raw("PRAGMA journal_mode;").Scan(&journalMode).Error; err != nil {
		t.Fatalf("查询 journal_mode 失败: %v", err)
	}
	if strings.ToLower(strings.TrimSpace(journalMode)) != "wal" {
		t.Fatalf("期望 caller DSN 不能降低 journal_mode=wal，实际: %s", journalMode)
	}

	var foreignKeys int
	if err := db.Raw("PRAGMA foreign_keys;").Scan(&foreignKeys).Error; err != nil {
		t.Fatalf("查询 foreign_keys 失败: %v", err)
	}
	if foreignKeys != 1 {
		t.Fatalf("期望 caller DSN 不能降低 foreign_keys=1，实际: %d", foreignKeys)
	}

	var synchronous int
	if err := db.Raw("PRAGMA synchronous;").Scan(&synchronous).Error; err != nil {
		t.Fatalf("查询 synchronous 失败: %v", err)
	}
	if synchronous != 1 {
		t.Fatalf("期望 caller DSN 不能降低 synchronous=NORMAL(1)，实际: %d", synchronous)
	}
}

func TestOpenSQLiteRejectsMalformedQuery(t *testing.T) {
	_, err := Open(config.Config{
		DBType:     "sqlite",
		SQLitePath: filepath.Join(t.TempDir(), "malformed-query.db") + "?cache=%ZZ",
	})
	if err == nil {
		t.Fatal("期望 malformed SQLite query 被拒绝")
	}
}

func TestOpenSQLitePreservesFileURIFragment(t *testing.T) {
	sqlitePath := filepath.Join(t.TempDir(), "fragment.db")
	callerDSN := "file:" + sqlitePath + "#fragment"

	dsn, err := buildSQLiteDSNWithError(callerDSN)
	if err != nil {
		t.Fatalf("构建 file URI SQLite DSN 失败: %v", err)
	}
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("解析 file URI SQLite DSN 失败: %v", err)
	}
	if parsed.Path != sqlitePath {
		t.Fatalf("期望保留 file URI path %q，实际: %q", sqlitePath, parsed.Path)
	}
	if parsed.Fragment != "fragment" {
		t.Fatalf("期望保留 file URI fragment，实际: %q", parsed.Fragment)
	}

	db, err := Open(config.Config{
		DBType:     "sqlite",
		SQLitePath: callerDSN,
	})
	if err != nil {
		t.Fatalf("打开带 fragment 的 SQLite file URI 失败: %v", err)
	}
	t.Cleanup(func() {
		sqlDB, dbErr := db.DB()
		if dbErr == nil {
			_ = sqlDB.Close()
		}
		_ = os.Remove(sqlitePath)
	})

	var busyTimeout int
	if err := db.Raw("PRAGMA busy_timeout;").Scan(&busyTimeout).Error; err != nil {
		t.Fatalf("查询 busy_timeout 失败: %v", err)
	}
	if busyTimeout != 5000 {
		t.Fatalf("期望 file URI 保持 busy_timeout=5000ms，实际: %d", busyTimeout)
	}
}

func TestOpenSQLitePreservesHashInFilesystemPath(t *testing.T) {
	prefixPath := filepath.Join(t.TempDir(), "backup")
	literalPath := prefixPath + "#2026.db"

	prefixDB, err := Open(config.Config{
		DBType:     "sqlite",
		SQLitePath: prefixPath,
	})
	if err != nil {
		t.Fatalf("打开 prefix SQLite 数据库失败: %v", err)
	}
	prefixSQLDB, err := prefixDB.DB()
	if err != nil {
		t.Fatalf("获取 prefix SQLite 连接失败: %v", err)
	}
	if err := prefixDB.Exec("CREATE TABLE prefix_sentinel (value TEXT NOT NULL)").Error; err != nil {
		t.Fatalf("创建 prefix sentinel 失败: %v", err)
	}
	if err := prefixDB.Exec("INSERT INTO prefix_sentinel (value) VALUES ('preserved')").Error; err != nil {
		t.Fatalf("写入 prefix sentinel 失败: %v", err)
	}
	if err := prefixSQLDB.Close(); err != nil {
		t.Fatalf("关闭 prefix SQLite 连接失败: %v", err)
	}

	literalDB, err := Open(config.Config{
		DBType:     "sqlite",
		SQLitePath: literalPath,
	})
	if err != nil {
		t.Fatalf("打开带 hash 的 SQLite path 失败: %v", err)
	}
	literalSQLDB, err := literalDB.DB()
	if err != nil {
		t.Fatalf("获取带 hash 的 SQLite 连接失败: %v", err)
	}
	t.Cleanup(func() {
		_ = literalSQLDB.Close()
	})
	if err := literalDB.Exec("CREATE TABLE literal_path_marker (value TEXT NOT NULL)").Error; err != nil {
		t.Fatalf("写入带 hash 的 SQLite path 失败: %v", err)
	}

	if _, err := os.Stat(literalPath); err != nil {
		t.Fatalf("期望创建 literal hash path %q: %v", literalPath, err)
	}

	prefixDB, err = Open(config.Config{
		DBType:     "sqlite",
		SQLitePath: prefixPath,
	})
	if err != nil {
		t.Fatalf("重新打开 prefix SQLite 数据库失败: %v", err)
	}
	prefixSQLDB, err = prefixDB.DB()
	if err != nil {
		t.Fatalf("获取重新打开的 prefix SQLite 连接失败: %v", err)
	}
	t.Cleanup(func() {
		_ = prefixSQLDB.Close()
	})

	var sentinel string
	if err := prefixDB.Raw("SELECT value FROM prefix_sentinel").Scan(&sentinel).Error; err != nil {
		t.Fatalf("读取 prefix sentinel 失败: %v", err)
	}
	if sentinel != "preserved" {
		t.Fatalf("期望 prefix sentinel 保持不变，实际: %q", sentinel)
	}

	var markerCount int
	if err := prefixDB.Raw("SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'literal_path_marker'").Scan(&markerCount).Error; err != nil {
		t.Fatalf("检查 prefix marker 失败: %v", err)
	}
	if markerCount != 0 {
		t.Fatalf("literal hash path 不应写入 prefix SQLite 数据库，实际 marker count: %d", markerCount)
	}
}

func TestWithSQLiteBusyRetryTxSetsAndRestoresBusyTimeout(t *testing.T) {
	db, primary, _ := newSQLiteBusyRetryTestDB(t)
	if err := db.Exec("PRAGMA busy_timeout = 123").Error; err != nil {
		t.Fatalf("configure SQLite busy timeout: %v", err)
	}

	var timeoutInside int64
	err := WithSQLiteBusyRetryTx(context.Background(), db, func(tx *gorm.DB) error {
		return tx.Raw("PRAGMA busy_timeout").Scan(&timeoutInside).Error
	})
	if err != nil {
		t.Fatalf("run SQLite busy retry transaction: %v", err)
	}
	if timeoutInside != 0 {
		t.Fatalf("SQLite busy timeout inside transaction=%d, want 0", timeoutInside)
	}
	assertSQLiteBusyRetryConnectionReleased(t, primary)
	if got := sqliteBusyRetryTimeout(t, db); got != 123 {
		t.Fatalf("SQLite busy timeout after transaction=%d, want restored 123", got)
	}
}

func TestWithSQLiteBusyRetryTxRetriesTypedSQLiteErrors(t *testing.T) {
	for _, testCase := range []struct {
		name string
		err  error
	}{
		{name: "wrapped sqlite error", err: fmt.Errorf("begin immediate: %w", sqlite3.Error{Code: sqlite3.ErrBusy})},
		{name: "wrapped sqlite errno", err: fmt.Errorf("begin immediate: %w", sqlite3.ErrLocked)},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			db, _, _ := newSQLiteBusyRetryTestDB(t)
			if err := db.Exec("CREATE TABLE busy_retry_values (value INTEGER NOT NULL)").Error; err != nil {
				t.Fatalf("create retry test table: %v", err)
			}

			calls := 0
			err := WithSQLiteBusyRetryTx(context.Background(), db, func(tx *gorm.DB) error {
				calls++
				if err := tx.Exec("INSERT INTO busy_retry_values (value) VALUES (?)", calls).Error; err != nil {
					return err
				}
				if calls == 1 {
					return testCase.err
				}
				return nil
			})
			if err != nil {
				t.Fatalf("retry SQLite transaction: %v", err)
			}
			if calls != 2 {
				t.Fatalf("transaction body calls=%d, want 2", calls)
			}
			var values int64
			if err := db.Raw("SELECT COUNT(*) FROM busy_retry_values").Scan(&values).Error; err != nil {
				t.Fatalf("count retry values: %v", err)
			}
			if values != 1 {
				t.Fatalf("committed retry values=%d, want exactly one", values)
			}
		})
	}
}

func TestWithSQLiteBusyRetryTxRetainsTypedBusyErrorAfterRetryExhaustion(t *testing.T) {
	db, _, _ := newSQLiteBusyRetryTestDB(t)
	terminalErr := fmt.Errorf("terminal SQLite busy: %w", sqlite3.ErrBusy)
	calls := 0

	err := WithSQLiteBusyRetryTx(context.Background(), db, func(*gorm.DB) error {
		calls++
		return terminalErr
	})
	if calls != sqliteBusyRetryAttempts {
		t.Fatalf("terminal SQLite busy body calls=%d, want %d", calls, sqliteBusyRetryAttempts)
	}
	if !errors.Is(err, sqlite3.ErrBusy) {
		t.Fatalf("terminal SQLite busy error=%v, want errors.Is(ErrBusy)", err)
	}
	var sqliteCode sqlite3.ErrNo
	if !errors.As(err, &sqliteCode) || sqliteCode != sqlite3.ErrBusy {
		t.Fatalf("terminal SQLite busy error=%v, want errors.As(ErrBusy)", err)
	}
}

func TestWithSQLiteBusyRetryTxHonorsBusyContextDeadline(t *testing.T) {
	db, primary, path := newSQLiteBusyRetryTestDB(t)
	locker, err := Open(config.Config{DBType: "sqlite", SQLitePath: path})
	if err != nil {
		t.Fatalf("open SQLite contention holder: %v", err)
	}
	lockerDB, err := locker.DB()
	if err != nil {
		t.Fatalf("get SQLite contention holder database: %v", err)
	}
	lockerDB.SetMaxOpenConns(1)
	t.Cleanup(func() {
		if err := lockerDB.Close(); err != nil {
			t.Errorf("close SQLite contention holder: %v", err)
		}
	})

	holder, err := lockerDB.Conn(context.Background())
	if err != nil {
		t.Fatalf("pin SQLite contention holder: %v", err)
	}
	if _, err := holder.ExecContext(context.Background(), "BEGIN IMMEDIATE"); err != nil {
		t.Fatalf("acquire SQLite immediate lock: %v", err)
	}
	t.Cleanup(func() {
		if _, err := holder.ExecContext(context.Background(), "ROLLBACK"); err != nil && !errors.Is(err, sql.ErrConnDone) {
			t.Errorf("release SQLite immediate lock: %v", err)
		}
		if err := holder.Close(); err != nil && !errors.Is(err, sql.ErrConnDone) {
			t.Errorf("close SQLite contention holder connection: %v", err)
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	calls := 0
	go func() {
		done <- WithSQLiteBusyRetryTx(ctx, db, func(*gorm.DB) error {
			calls++
			return nil
		})
	}()
	waitForSQLiteBusyRetryConnection(t, primary, 1)

	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("SQLite busy retry error=%v, want context deadline exceeded", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("SQLite busy retry did not honor context deadline")
	}
	if calls != 0 {
		t.Fatalf("SQLite busy retry body calls=%d, want 0 while BEGIN IMMEDIATE is held", calls)
	}
	assertSQLiteBusyRetryConnectionReleased(t, primary)
}

func TestWithSQLiteBusyRetryTxPreservesNonRetryableErrors(t *testing.T) {
	for _, testCase := range []struct {
		name  string
		cause error
	}{
		{name: "wrapped sentinel", cause: errors.New("transaction failed")},
		{name: "wrapped context deadline", cause: context.DeadlineExceeded},
		{name: "wrapped sqlite constraint", cause: sqlite3.ErrConstraint},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			db, _, _ := newSQLiteBusyRetryTestDB(t)
			returned := fmt.Errorf("transaction body: %w", testCase.cause)
			calls := 0
			err := WithSQLiteBusyRetryTx(context.Background(), db, func(*gorm.DB) error {
				calls++
				return returned
			})
			if !errors.Is(err, testCase.cause) {
				t.Fatalf("transaction error=%v, want errors.Is(%v)", err, testCase.cause)
			}
			if calls != 1 {
				t.Fatalf("transaction body calls=%d, want 1", calls)
			}
		})
	}
}

func TestWithSQLiteBusyRetryTxDiscardsConnectionWhenRestoreFails(t *testing.T) {
	db, primary, state := newSQLiteBusyRetryInstrumentedDB(t)
	state.failRestore.Store(true)

	err := WithSQLiteBusyRetryTx(context.Background(), db, func(*gorm.DB) error { return nil })
	if !errors.Is(err, errSQLiteBusyRetryRestore) {
		t.Fatalf("restore failure error=%v, want injected restore error", err)
	}
	if got := state.opens.Load(); got != 1 {
		t.Fatalf("connections opened before reuse=%d, want 1", got)
	}
	if got := state.closes.Load(); got != 1 {
		t.Fatalf("connections closed after failed restore=%d, want discarded connection", got)
	}
	if err := db.Exec("SELECT 1").Error; err != nil {
		t.Fatalf("query after failed restore: %v", err)
	}
	if got := state.opens.Load(); got != 2 {
		t.Fatalf("connections opened after reuse=%d, want fresh connection", got)
	}
	assertSQLiteBusyRetryConnectionReleased(t, primary)
	if got := sqliteBusyRetryTimeout(t, db); got != 321 {
		t.Fatalf("fresh SQLite connection busy timeout=%d, want configured 321", got)
	}
}

func TestWithSQLiteBusyRetryTxRestoresTimeoutAfterAppliedZeroMutationError(t *testing.T) {
	db, primary, state := newSQLiteBusyRetryInstrumentedDB(t)
	state.failZeroAfterApply.Store(true)
	calls := 0

	err := WithSQLiteBusyRetryTx(context.Background(), db, func(*gorm.DB) error {
		calls++
		return nil
	})
	if !errors.Is(err, errSQLiteBusyRetryZeroAfterApply) {
		t.Fatalf("applied zero-timeout error=%v, want injected error", err)
	}
	if calls != 0 {
		t.Fatalf("transaction body calls=%d, want 0 after zero-timeout mutation error", calls)
	}
	assertSQLiteBusyRetryConnectionReleased(t, primary)
	if got := sqliteBusyRetryTimeout(t, db); got != 321 {
		t.Fatalf("SQLite busy timeout after applied zero-timeout error=%d, want restored 321", got)
	}
	if got := state.opens.Load(); got != 1 {
		t.Fatalf("connections opened after restored zero-timeout error=%d, want original connection reused", got)
	}
}

func TestWithSQLiteBusyRetryTxPreservesBodyErrorWhenRestoreFails(t *testing.T) {
	db, primary, state := newSQLiteBusyRetryInstrumentedDB(t)
	state.failRestore.Store(true)
	bodyCause := errors.New("body error")

	err := WithSQLiteBusyRetryTx(context.Background(), db, func(*gorm.DB) error {
		return fmt.Errorf("transaction body: %w", bodyCause)
	})
	if !errors.Is(err, bodyCause) {
		t.Fatalf("combined transaction error=%v, want errors.Is(body error)", err)
	}
	if !errors.Is(err, errSQLiteBusyRetryRestore) {
		t.Fatalf("combined transaction error=%v, want errors.Is(restore error)", err)
	}
	if got := state.closes.Load(); got != 1 {
		t.Fatalf("connections closed after body and restore errors=%d, want discarded connection", got)
	}
	if err := db.Exec("SELECT 1").Error; err != nil {
		t.Fatalf("query after body and restore errors: %v", err)
	}
	if got := state.opens.Load(); got != 2 {
		t.Fatalf("connections opened after body and restore errors=%d, want fresh connection", got)
	}
	assertSQLiteBusyRetryConnectionReleased(t, primary)
}

func TestWithSQLiteBusyRetryTxSkipsSQLiteSetupForOtherDialects(t *testing.T) {
	db, _, state := newSQLiteBusyRetryInstrumentedDB(t)
	db = db.Session(&gorm.Session{NewDB: true})
	db.Dialector = namedSQLiteBusyRetryDialector{Dialector: db.Dialector}
	if err := db.Exec("PRAGMA busy_timeout = 123").Error; err != nil {
		t.Fatalf("configure non-SQLite busy timeout: %v", err)
	}
	state.busyTimeoutStatements.Store(0)

	calls := 0
	err := WithSQLiteBusyRetryTx(context.Background(), db, func(*gorm.DB) error {
		calls++
		return nil
	})
	if err != nil {
		t.Fatalf("run non-SQLite transaction: %v", err)
	}
	if calls != 1 {
		t.Fatalf("non-SQLite transaction body calls=%d, want 1", calls)
	}
	if got := state.busyTimeoutStatements.Load(); got != 0 {
		t.Fatalf("non-SQLite transaction issued %d SQLite busy-timeout statements", got)
	}
}

func newSQLiteBusyRetryTestDB(t *testing.T) (*gorm.DB, *sql.DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sqlite-busy-retry.db")
	db, err := Open(config.Config{DBType: "sqlite", SQLitePath: path})
	if err != nil {
		t.Fatalf("open SQLite busy retry database: %v", err)
	}
	primary, err := db.DB()
	if err != nil {
		t.Fatalf("get SQLite busy retry database: %v", err)
	}
	primary.SetMaxOpenConns(1)
	primary.SetMaxIdleConns(1)
	t.Cleanup(func() {
		if err := primary.Close(); err != nil {
			t.Errorf("close SQLite busy retry database: %v", err)
		}
	})
	return db, primary, path
}

func sqliteBusyRetryTimeout(t *testing.T, db *gorm.DB) int64 {
	t.Helper()
	var timeout int64
	if err := db.Raw("PRAGMA busy_timeout").Scan(&timeout).Error; err != nil {
		t.Fatalf("read SQLite busy timeout: %v", err)
	}
	return timeout
}

func waitForSQLiteBusyRetryConnection(t *testing.T, db *sql.DB, want int) {
	t.Helper()
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	for db.Stats().InUse != want {
		select {
		case <-timer.C:
			t.Fatalf("SQLite busy retry connection in use=%d, want %d", db.Stats().InUse, want)
		default:
			time.Sleep(time.Millisecond)
		}
	}
}

func assertSQLiteBusyRetryConnectionReleased(t *testing.T, db *sql.DB) {
	t.Helper()
	waitForSQLiteBusyRetryConnection(t, db, 0)
}

var (
	errSQLiteBusyRetryRestore        = errors.New("injected SQLite busy-timeout restore failure")
	errSQLiteBusyRetryZeroAfterApply = errors.New("injected SQLite zero-timeout error after apply")
)

const sqliteBusyRetryTestDriverName = "xirang_sqlite_busy_retry_test"

var (
	sqliteBusyRetryTestDriverOnce  sync.Once
	sqliteBusyRetryTestDriverState atomic.Pointer[sqliteBusyRetryDriverState]
)

type sqliteBusyRetryDriverState struct {
	opens                 atomic.Int64
	closes                atomic.Int64
	busyTimeoutStatements atomic.Int64
	busyTimeoutSetToZero  atomic.Bool
	failZeroAfterApply    atomic.Bool
	failRestore           atomic.Bool
}

type sqliteBusyRetryTestDriver struct{}

func (sqliteBusyRetryTestDriver) Open(name string) (driver.Conn, error) {
	conn, err := (&sqlite3.SQLiteDriver{}).Open(name)
	if err != nil {
		return nil, err
	}
	state := sqliteBusyRetryTestDriverState.Load()
	if state != nil {
		state.opens.Add(1)
	}
	return &sqliteBusyRetryTestConn{Conn: conn, state: state}, nil
}

type sqliteBusyRetryTestConn struct {
	driver.Conn
	state *sqliteBusyRetryDriverState
}

func (conn *sqliteBusyRetryTestConn) Close() error {
	if conn.state != nil {
		conn.state.closes.Add(1)
	}
	return conn.Conn.Close()
}

func (conn *sqliteBusyRetryTestConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	return conn.Conn.(driver.ConnBeginTx).BeginTx(ctx, opts)
}

func (conn *sqliteBusyRetryTestConn) ExecContext(
	ctx context.Context,
	query string,
	args []driver.NamedValue,
) (driver.Result, error) {
	if err := conn.observeBusyTimeoutStatement(query); err != nil {
		return nil, err
	}
	result, err := conn.Conn.(driver.ExecerContext).ExecContext(ctx, query, args)
	if err == nil && conn.state != nil && conn.state.failZeroAfterApply.Load() && sqliteBusyTimeoutZeroStatement(query) {
		return result, errSQLiteBusyRetryZeroAfterApply
	}
	return result, err
}

func (conn *sqliteBusyRetryTestConn) QueryContext(
	ctx context.Context,
	query string,
	args []driver.NamedValue,
) (driver.Rows, error) {
	if err := conn.observeBusyTimeoutStatement(query); err != nil {
		return nil, err
	}
	return conn.Conn.(driver.QueryerContext).QueryContext(ctx, query, args)
}

func (conn *sqliteBusyRetryTestConn) observeBusyTimeoutStatement(query string) error {
	if conn.state == nil {
		return nil
	}
	normalized := strings.ToLower(strings.TrimSuffix(strings.Join(strings.Fields(query), " "), ";"))
	if !strings.HasPrefix(normalized, "pragma busy_timeout") {
		return nil
	}
	conn.state.busyTimeoutStatements.Add(1)
	if !strings.Contains(normalized, "=") {
		return nil
	}
	if sqliteBusyTimeoutZeroStatement(query) {
		conn.state.busyTimeoutSetToZero.Store(true)
		return nil
	}
	if conn.state.failRestore.Load() && conn.state.busyTimeoutSetToZero.Load() {
		return errSQLiteBusyRetryRestore
	}
	return nil
}

func sqliteBusyTimeoutZeroStatement(query string) bool {
	normalized := strings.ToLower(strings.TrimSuffix(strings.Join(strings.Fields(query), " "), ";"))
	return strings.HasPrefix(normalized, "pragma busy_timeout") &&
		(strings.HasSuffix(normalized, "= 0") || strings.HasSuffix(normalized, "=0"))
}

func newSQLiteBusyRetryInstrumentedDB(t *testing.T) (*gorm.DB, *sql.DB, *sqliteBusyRetryDriverState) {
	t.Helper()
	sqliteBusyRetryTestDriverOnce.Do(func() {
		sql.Register(sqliteBusyRetryTestDriverName, sqliteBusyRetryTestDriver{})
	})
	state := &sqliteBusyRetryDriverState{}
	sqliteBusyRetryTestDriverState.Store(state)

	path := filepath.Join(t.TempDir(), "sqlite-busy-retry-instrumented.db")
	db, err := gorm.Open(sqlite.New(sqlite.Config{
		DriverName: sqliteBusyRetryTestDriverName,
		DSN:        "file:" + path + "?_busy_timeout=321&_txlock=immediate",
	}), &gorm.Config{})
	if err != nil {
		t.Fatalf("open instrumented SQLite busy retry database: %v", err)
	}
	primary, err := db.DB()
	if err != nil {
		t.Fatalf("get instrumented SQLite busy retry database: %v", err)
	}
	primary.SetMaxOpenConns(1)
	primary.SetMaxIdleConns(1)
	t.Cleanup(func() {
		if err := primary.Close(); err != nil {
			t.Errorf("close instrumented SQLite busy retry database: %v", err)
		}
		sqliteBusyRetryTestDriverState.Store(nil)
	})
	return db, primary, state
}

type namedSQLiteBusyRetryDialector struct {
	gorm.Dialector
}

func (namedSQLiteBusyRetryDialector) Name() string {
	return "postgres"
}
