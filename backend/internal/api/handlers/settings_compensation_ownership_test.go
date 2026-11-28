package handlers

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/ga"
	assetruntime "xirang/backend/internal/backupasset/runtime"
	"xirang/backend/internal/database"
	"xirang/backend/internal/model"
	"xirang/backend/internal/settings"

	"github.com/gin-gonic/gin"
	"github.com/mattn/go-sqlite3"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// ownershipTransition runs the handler's persistence and compensation callbacks
// in the same order as a runtime failure, exposing the competing commit points.
type ownershipTransition struct {
	settingsTransitionSpy
	beforePersist func() error
	afterPersist  func() error
	restoreErr    error
	persistErr    error
	restoreCalled bool
	persisted     bool
}

func (runtime *ownershipTransition) TransitionBackupAssetSettingsContextWithRestore(
	ctx context.Context, _, _, _ map[string]string, _ backupasset.ExportConfig,
	persist, restore func(context.Context) error,
) error {
	if runtime.beforePersist != nil {
		if err := runtime.beforePersist(); err != nil {
			return err
		}
	}
	if err := persist(ctx); err != nil {
		runtime.persistErr = err
		return err
	}
	runtime.persisted = true
	if runtime.afterPersist != nil {
		if err := runtime.afterPersist(); err != nil {
			return err
		}
	}
	runtime.restoreCalled = true
	runtime.restoreErr = restore(ctx)
	return errors.Join(errors.New("ownership test post-persist failure"), runtime.restoreErr)
}

type ownershipRow struct {
	exists    bool
	value     string
	updatedAt time.Time
}

func readOwnershipRow(t *testing.T, db *gorm.DB, key string) ownershipRow {
	t.Helper()
	var row model.SystemSetting
	err := db.Where("key = ?", key).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ownershipRow{}
	}
	if err != nil {
		t.Fatalf("read setting %s: %v", key, err)
	}
	return ownershipRow{exists: true, value: row.Value, updatedAt: row.UpdatedAt}
}

func assertOwnershipRow(t *testing.T, db *gorm.DB, key string, want ownershipRow) {
	t.Helper()
	got := readOwnershipRow(t, db, key)
	if got.exists != want.exists || got.value != want.value || !got.updatedAt.Equal(want.updatedAt) {
		t.Fatalf("setting %s = %+v, want %+v", key, got, want)
	}
}

func ownershipRequest(router *gin.Engine, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, req)
	return response
}

func ownershipRouter(db *gorm.DB, transitioner *ownershipTransition) *gin.Engine {
	handler := NewSettingsHandler(db, settings.NewService(db)).WithBackupAssetTransitioner(transitioner)
	router := gin.New()
	router.PUT("/settings", handler.BatchUpdate)
	router.DELETE("/settings/:key", handler.Delete)
	return router
}

func TestSettingsCompensationOwnershipMigrated(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, engine := range []string{"sqlite", "postgres"} {
		for _, scenario := range []string{"prior_writer", "stale_foundation", "later_writer", "later_same_value", "reinsert_after_delete", "unrelated_writer"} {
			t.Run(engine+"/"+scenario, func(t *testing.T) {
				t.Setenv("BACKUP_ASSETS_EXPORT_WORKER_CONCURRENCY", "")
				t.Setenv("LOGIN_CAPTCHA_ENABLED", "")
				t.Setenv("LOGIN_SECOND_CAPTCHA_ENABLED", "")
				db := openMigratedRetryTransactionDB(t, engine)
				prior := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
				for _, row := range []model.SystemSetting{
					{Key: "backup_assets.export.worker_concurrency", Value: "2", UpdatedAt: prior},
					{Key: "login.captcha_enabled", Value: "false", UpdatedAt: prior},
				} {
					if err := db.Create(&row).Error; err != nil {
						t.Fatal(err)
					}
				}
				// Exercise the representation used by older SQLite databases as well as
				// the logical time value when the compensation is uncontested.
				legacyTimestamp := "2024-01-02 03:04:05"
				if engine == "sqlite" && scenario == "unrelated_writer" {
					if err := db.Exec("UPDATE system_settings SET updated_at = ? WHERE key = ?", legacyTimestamp, "login.captcha_enabled").Error; err != nil {
						t.Fatal(err)
					}
				}
				runtime := &ownershipTransition{}
				router := ownershipRouter(db, runtime)
				// A separate service and handler model an ordinary concurrent writer;
				// requests occur after the previous transaction has committed.
				writer := ownershipRouter(db, &ownershipTransition{})
				put := func(body string) error {
					response := ownershipRequest(writer, http.MethodPut, "/settings", body)
					if response.Code != http.StatusOK {
						return fmt.Errorf("ordinary PUT status=%d body=%s", response.Code, response.Body.String())
					}
					return nil
				}
				keys := []string{"backup_assets.export.worker_concurrency", "login.captcha_enabled", "login.second_captcha_enabled"}
				before := make(map[string]ownershipRow)
				for _, key := range keys {
					before[key] = readOwnershipRow(t, db, key)
				}
				var committed map[string]ownershipRow
				var unrelated ownershipRow
				switch scenario {
				case "prior_writer":
					runtime.beforePersist = func() error {
						if err := put(`{"login.captcha_enabled":"true"}`); err != nil {
							return err
						}
						before["login.captcha_enabled"] = readOwnershipRow(t, db, "login.captcha_enabled")
						return nil
					}
				case "stale_foundation":
					runtime.beforePersist = func() error {
						stamp := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
						if err := db.Exec("UPDATE system_settings SET value = ?, updated_at = ? WHERE key = ?", "4", stamp, keys[0]).Error; err != nil {
							return err
						}
						before[keys[0]] = readOwnershipRow(t, db, keys[0])
						return nil
					}
				case "later_writer", "later_same_value":
					runtime.afterPersist = func() error {
						value := "false"
						if scenario == "later_same_value" {
							value = "true"
						}
						if err := put(fmt.Sprintf(`{"login.captcha_enabled":%q}`, value)); err != nil {
							return err
						}
						// Guarantee a distinct timestamp even on clocks with coarse resolution.
						stamp := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
						if err := db.Exec("UPDATE system_settings SET updated_at = ? WHERE key = ?", stamp, "login.captcha_enabled").Error; err != nil {
							return err
						}
						committed = make(map[string]ownershipRow)
						for _, key := range keys {
							committed[key] = readOwnershipRow(t, db, key)
						}
						return nil
					}
				case "reinsert_after_delete":
					runtime.afterPersist = func() error {
						if readOwnershipRow(t, db, keys[0]).exists {
							return errors.New("Foundation delete did not persist")
						}
						stamp := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
						if err := db.Create(&model.SystemSetting{Key: keys[0], Value: "4", UpdatedAt: stamp}).Error; err != nil {
							return err
						}
						committed = map[string]ownershipRow{keys[0]: readOwnershipRow(t, db, keys[0])}
						return nil
					}
				case "unrelated_writer":
					runtime.afterPersist = func() error {
						if err := put(`{"login.rate_limit":"23"}`); err != nil {
							return err
						}
						unrelated = readOwnershipRow(t, db, "login.rate_limit")
						return nil
					}
				}
				method, path, body := http.MethodPut, "/settings", `{"backup_assets.export.worker_concurrency":"3","login.captcha_enabled":"true","login.second_captcha_enabled":"true"}`
				if scenario == "reinsert_after_delete" {
					method, path, body = http.MethodDelete, "/settings/backup_assets.export.worker_concurrency", ""
				}
				response := ownershipRequest(router, method, path, body)
				if response.Code != http.StatusInternalServerError || strings.Contains(response.Body.String(), "ownership test") {
					t.Fatalf("status=%d body=%s, want generic 500", response.Code, response.Body.String())
				}
				if scenario == "stale_foundation" {
					if runtime.persisted || runtime.persistErr == nil || !strings.Contains(runtime.persistErr.Error(), "settings override conflict") || runtime.restoreCalled {
						t.Fatalf("stale snapshot persisted=%t persist error=%v restore called=%t", runtime.persisted, runtime.persistErr, runtime.restoreCalled)
					}
					for key, row := range before {
						assertOwnershipRow(t, db, key, row)
					}
					return
				}
				if !runtime.persisted {
					t.Fatal("transition did not reach post-persist failure")
				}
				conflict := scenario == "later_writer" || scenario == "later_same_value" || scenario == "reinsert_after_delete"
				if conflict {
					if runtime.restoreErr == nil || !strings.Contains(runtime.restoreErr.Error(), "settings override conflict") {
						t.Fatalf("restore error=%v, want settings override conflict", runtime.restoreErr)
					}
					for key, row := range committed {
						assertOwnershipRow(t, db, key, row)
					}
				} else {
					if runtime.restoreErr != nil {
						t.Fatalf("uncontested compensation: %v", runtime.restoreErr)
					}
					for key, row := range before {
						assertOwnershipRow(t, db, key, row)
					}
				}
				if scenario == "unrelated_writer" {
					if !unrelated.exists || unrelated.value != "23" {
						t.Fatalf("unrelated writer row=%+v", unrelated)
					}
					assertOwnershipRow(t, db, "login.rate_limit", unrelated)
					if engine == "sqlite" {
						var raw string
						if err := db.Raw("SELECT CAST(updated_at AS TEXT) FROM system_settings WHERE key = ?", "login.captcha_enabled").Scan(&raw).Error; err != nil {
							t.Fatal(err)
						}
						if raw != legacyTimestamp {
							t.Fatalf("legacy timestamp=%q, want %q", raw, legacyTimestamp)
						}
					}
				}
			})
		}
	}
}

// ownershipRealRuntime wraps only the callback boundary; the runtime itself
// performs the post-persist failure and handles compensation/fencing.
type ownershipRealRuntime struct {
	*assetruntime.Runtime
	afterPersist func() error
	restoreErr   error
}

func (runtime *ownershipRealRuntime) TransitionBackupAssetSettingsContextWithRestore(
	ctx context.Context, current, overlay, effective map[string]string, config backupasset.ExportConfig,
	persist, restore func(context.Context) error,
) error {
	return runtime.Runtime.TransitionBackupAssetSettingsContextWithRestore(ctx, current, overlay, effective, config,
		func(ctx context.Context) error {
			if err := persist(ctx); err != nil {
				return err
			}
			return runtime.afterPersist()
		},
		func(ctx context.Context) error {
			runtime.restoreErr = restore(ctx)
			return runtime.restoreErr
		})
}

func TestSettingsCompensationOwnershipRealRuntimeMigrated(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, engine := range []string{"sqlite", "postgres"} {
		t.Run(engine, func(t *testing.T) {
			t.Setenv("BACKUP_ASSETS_ENABLED", "")
			t.Setenv("LOGIN_CAPTCHA_ENABLED", "")
			db := openMigratedRetryTransactionDB(t, engine)
			now := time.Date(2026, 8, 24, 18, 0, 0, 0, time.UTC)
			digest := strings.Repeat("a", 64)
			if err := db.Create(&model.BackupAssetInstallation{ID: strings.Repeat("b", 32), Slot: 1, Class: string(ga.InstallationFresh), Readiness: string(ga.ReadinessUnknown), InventoryDigest: digest, CreatedAt: now, UpdatedAt: now}).Error; err != nil {
				t.Fatal(err)
			}
			svc := settings.NewService(db)
			snapshot := ga.ReadinessSnapshot{Class: ga.InstallationFresh, Status: ga.ReadinessReady, InventoryComplete: true, InventoryDigest: digest, ExportRootValid: true, KeyDomainsReady: true}
			inventory := ga.NewInventoryService(ga.InventoryDependencies{DB: db, Now: func() time.Time { return now }})
			realRuntime := buildThreeEntrypointRuntime(t, db, svc, inventory, snapshot, &settingsTransitionSpy{}, now)
			writer := ownershipRouter(db, &ownershipTransition{})
			var newer ownershipRow
			runtime := &ownershipRealRuntime{Runtime: realRuntime}
			runtime.afterPersist = func() error {
				response := ownershipRequest(writer, http.MethodPut, "/settings", `{"login.captcha_enabled":"false"}`)
				if response.Code != http.StatusOK {
					return fmt.Errorf("competing PUT status=%d body=%s", response.Code, response.Body.String())
				}
				newer = readOwnershipRow(t, db, "login.captcha_enabled")
				return nil
			}
			stampErr := fmt.Errorf("ownership injected stamp failure: %w", ga.ErrEnablementBlocked)
			const callbackName = "ownership_fail_enablement_stamp"
			if err := db.Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
				if tx.Statement != nil && tx.Statement.Table == "backup_asset_installations" {
					if _, ok := tx.Statement.Dest.(map[string]any)["enablement_succeeded_at"]; ok {
						_ = tx.AddError(stampErr)
					}
				}
			}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Callback().Update().Remove(callbackName) })
			handler := NewSettingsHandler(db, svc).WithBackupAssetTransitioner(runtime)
			router := gin.New()
			router.PUT("/settings", handler.BatchUpdate)
			response := ownershipRequest(router, http.MethodPut, "/settings", `{"backup_assets.enabled":"true","login.captcha_enabled":"true"}`)
			if response.Code != http.StatusInternalServerError || strings.Contains(response.Body.String(), stampErr.Error()) {
				t.Fatalf("status=%d body=%s, want generic 500", response.Code, response.Body.String())
			}
			if runtime.restoreErr == nil || !strings.Contains(runtime.restoreErr.Error(), "settings override conflict") {
				t.Fatalf("restore error=%v, want conflict", runtime.restoreErr)
			}
			if !newer.exists || newer.value != "false" {
				t.Fatalf("competing row=%+v", newer)
			}
			assertOwnershipRow(t, db, "login.captcha_enabled", newer)
			if row := readOwnershipRow(t, db, "backup_assets.enabled"); !row.exists || row.value != "true" {
				t.Fatalf("uncompensated enabled row=%+v", row)
			}
			if live, err := realRuntime.FeatureLive(); err != nil || live {
				t.Fatalf("fenced FeatureLive=%t err=%v", live, err)
			}
			retry := ownershipRequest(router, http.MethodPut, "/settings", `{"backup_assets.enabled":"true"}`)
			if retry.Code != http.StatusInternalServerError {
				t.Fatalf("fenced retry status=%d body=%s", retry.Code, retry.Body.String())
			}
			if live, err := realRuntime.FeatureLive(); err != nil || live {
				t.Fatalf("FeatureLive after retry=%t err=%v", live, err)
			}
		})
	}
}

// ownershipBusyDriver records a native BEGIN IMMEDIATE returning BUSY for the
// marked handler request. Holding that error until the writer commits makes
// the production retry deterministic without manufacturing a transaction.
type ownershipBusyDriver struct {
	busy    chan error
	release <-chan struct{}
}

type ownershipBusyConn struct {
	*sqlite3.SQLiteConn
	probe *ownershipBusyDriver
}

func (d *ownershipBusyDriver) Open(name string) (driver.Conn, error) {
	conn, err := (&sqlite3.SQLiteDriver{}).Open(name)
	if err != nil {
		return nil, err
	}
	return &ownershipBusyConn{SQLiteConn: conn.(*sqlite3.SQLiteConn), probe: d}, nil
}

func (conn *ownershipBusyConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	tx, err := conn.SQLiteConn.BeginTx(ctx, opts)
	var sqliteErr sqlite3.Error
	if retryTransactionContentionMarker(ctx) == "settings-waiter" && errors.As(err, &sqliteErr) &&
		(sqliteErr.Code == sqlite3.ErrBusy || sqliteErr.Code == sqlite3.ErrLocked) {
		select {
		case conn.probe.busy <- err:
			select {
			case <-conn.probe.release:
			case <-ctx.Done():
			}
		default:
		}
	}
	return tx, err
}

func openOwnershipBusyDB(t *testing.T, probe *ownershipBusyDriver) *gorm.DB {
	t.Helper()
	driverName := fmt.Sprintf("xirang_settings_ownership_%d", batchTestSequence.Add(1))
	sql.Register(driverName, probe)
	dsn := filepath.Join(t.TempDir(), "settings-ownership.sqlite") + "?_busy_timeout=5000&_journal_mode=WAL&_txlock=immediate&_loc=UTC"
	db, err := gorm.Open(sqlite.New(sqlite.Config{DriverName: driverName, DSN: dsn}), &gorm.Config{
		NowFunc: func() time.Time { return time.Now().UTC() },
	})
	if err != nil {
		t.Fatalf("open instrumented SQLite database: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(8)
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := database.RunMigrations(db, "sqlite"); err != nil {
		t.Fatalf("migrate SQLite database: %v", err)
	}
	return db
}

func TestSettingsCompensationWaitsForWriterMigrated(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, engine := range []string{"sqlite", "postgres"} {
		t.Run(engine, func(t *testing.T) {
			t.Setenv("BACKUP_ASSETS_EXPORT_WORKER_CONCURRENCY", "")
			t.Setenv("LOGIN_CAPTCHA_ENABLED", "")
			t.Setenv("LOGIN_SECOND_CAPTCHA_ENABLED", "")
			t.Setenv("LOGIN_RATE_LIMIT", "")
			release := make(chan struct{})
			var releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			probe := &ownershipBusyDriver{busy: make(chan error, 1), release: release}
			var db *gorm.DB
			if engine == "sqlite" {
				db = openOwnershipBusyDB(t, probe)
			} else {
				db = openMigratedRetryTransactionDB(t, engine)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			waiterDB := db
			var waiterPID int
			if engine == "postgres" {
				sqlDB, err := db.DB()
				if err != nil {
					t.Fatal(err)
				}
				conn, err := sqlDB.Conn(ctx)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = conn.Close() })
				if _, err := conn.ExecContext(ctx, "SET default_transaction_isolation = 'repeatable read'"); err != nil {
					t.Fatal(err)
				}
				var isolation string
				if err := conn.QueryRowContext(ctx, "SHOW default_transaction_isolation").Scan(&isolation); err != nil || isolation != "repeatable read" {
					t.Fatalf("waiter default isolation=%q err=%v", isolation, err)
				}
				if err := conn.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&waiterPID); err != nil {
					t.Fatal(err)
				}
				waiterDB = db.Session(&gorm.Session{NewDB: true, Context: ctx})
				waiterDB.Statement.ConnPool = conn
			}
			prior := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
			keys := []string{"backup_assets.export.worker_concurrency", "login.captcha_enabled", "login.second_captcha_enabled", "login.rate_limit"}
			for _, row := range []model.SystemSetting{
				{Key: keys[0], Value: "2", UpdatedAt: prior},
				{Key: keys[1], Value: "false", UpdatedAt: prior},
			} {
				if err := db.Create(&row).Error; err != nil {
					t.Fatal(err)
				}
			}
			writer := db.WithContext(ctx).Begin()
			if writer.Error != nil {
				t.Fatal(writer.Error)
			}
			finished := false
			responses := make(chan *httptest.ResponseRecorder, 1)
			started := false
			t.Cleanup(func() {
				if !finished {
					_ = writer.Rollback().Error
				}
				unblock()
				cancel()
				if started {
					select {
					case <-responses:
					case <-time.After(10 * time.Second):
						t.Error("timed out stopping settings waiter")
					}
				}
			})
			stamp := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
			if err := writer.Exec("UPDATE system_settings SET value = ?, updated_at = ? WHERE key = ?", "true", stamp, keys[1]).Error; err != nil {
				t.Fatal(err)
			}
			if err := writer.Create(&model.SystemSetting{Key: keys[2], Value: "true", UpdatedAt: stamp}).Error; err != nil {
				t.Fatal(err)
			}
			var holderPID int
			if engine == "postgres" {
				if err := writer.Raw("SELECT pg_backend_pid()").Scan(&holderPID).Error; err != nil {
					t.Fatal(err)
				}
			}
			transition := &ownershipTransition{}
			router := ownershipRouter(waiterDB, transition)
			req := httptest.NewRequest(http.MethodPut, "/settings", strings.NewReader(`{"backup_assets.export.worker_concurrency":"3","login.captcha_enabled":"false","login.second_captcha_enabled":"false","login.rate_limit":"23"}`))
			req = req.WithContext(context.WithValue(ctx, retryTransactionContentionContextKey{}, "settings-waiter"))
			req.Header.Set("Content-Type", "application/json")
			started = true
			go func() {
				response := httptest.NewRecorder()
				router.ServeHTTP(response, req)
				responses <- response
			}()
			if engine == "sqlite" {
				select {
				case <-probe.busy:
				case response := <-responses:
					started = false
					t.Fatalf("handler returned before SQLite contention: status=%d body=%s", response.Code, response.Body.String())
				case <-ctx.Done():
					t.Fatalf("waiting for SQLite BUSY: %v", ctx.Err())
				}
			} else {
				sqlDB, err := db.DB()
				if err != nil {
					t.Fatal(err)
				}
				for {
					var blocked bool
					err := sqlDB.QueryRowContext(ctx, `SELECT EXISTS (
						SELECT 1 FROM pg_stat_activity a JOIN pg_locks l ON l.pid = a.pid
						WHERE a.pid = $1 AND a.wait_event_type = 'Lock'
						  AND $2 = ANY(pg_blocking_pids(a.pid))
						  AND l.relation = 'system_settings'::regclass
						  AND l.mode = 'ShareRowExclusiveLock' AND NOT l.granted
					)`, waiterPID, holderPID).Scan(&blocked)
					if err != nil {
						t.Fatalf("inspect PostgreSQL waiter: %v", err)
					}
					if blocked {
						break
					}
					if ctx.Err() != nil {
						t.Fatalf("settings waiter PID %d did not block on writer PID %d: %v", waiterPID, holderPID, ctx.Err())
					}
					runtime.Gosched()
				}
			}
			winner := map[string]ownershipRow{
				keys[0]: {exists: true, value: "2", updatedAt: prior},
				keys[1]: {exists: true, value: "true", updatedAt: stamp},
				keys[2]: {exists: true, value: "true", updatedAt: stamp},
				keys[3]: {},
			}
			if err := writer.Commit().Error; err != nil {
				t.Fatalf("commit ordinary writer: %v", err)
			}
			finished = true
			unblock()
			var response *httptest.ResponseRecorder
			select {
			case response = <-responses:
				started = false
			case <-ctx.Done():
				t.Fatalf("waiting for handler completion: %v", ctx.Err())
			}
			if response.Code != http.StatusInternalServerError || strings.Contains(response.Body.String(), "ownership test") {
				t.Fatalf("status=%d body=%s, want generic 500", response.Code, response.Body.String())
			}
			if !transition.persisted || !transition.restoreCalled || transition.restoreErr != nil {
				t.Fatalf("persisted=%t restore called=%t restore error=%v persist error=%v", transition.persisted, transition.restoreCalled, transition.restoreErr, transition.persistErr)
			}
			for key, row := range winner {
				assertOwnershipRow(t, db, key, row)
			}
		})
	}
}
