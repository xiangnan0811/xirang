package handlers

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/mattn/go-sqlite3"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func openReadonlySnapshotTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "snapshot.sqlite")+"?_journal_mode=WAL&_txlock=immediate&_busy_timeout=1000"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	pool, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	pool.SetMaxOpenConns(1)
	pool.SetMaxIdleConns(1)
	t.Cleanup(func() { _ = pool.Close() })
	if err := db.Exec("CREATE TABLE snapshot_fixture (id INTEGER PRIMARY KEY, value TEXT NOT NULL)").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("INSERT INTO snapshot_fixture VALUES (1, 'original')").Error; err != nil {
		t.Fatal(err)
	}
	return db
}

func assertReadonlySnapshotPoolClean(t *testing.T, db *gorm.DB) {
	t.Helper()
	// BEGIN fails if the raw transaction leaked into the next pool borrower.
	if err := db.Exec("BEGIN IMMEDIATE").Error; err != nil {
		t.Fatalf("dirty pool after preview: %v", err)
	}
	if err := db.Exec("ROLLBACK").Error; err != nil {
		t.Fatalf("pool rollback: %v", err)
	}
	if err := db.Exec("UPDATE snapshot_fixture SET value = 'usable' WHERE id = 1").Error; err != nil {
		t.Fatalf("pool cannot write after preview: %v", err)
	}
}

func TestEscalationPreviewReadonlySnapshotCallbackFailureAndCancellation(t *testing.T) {
	for _, mode := range []string{"success", "callback-error", "cancel-inside", "cancel-before"} {
		t.Run(mode, func(t *testing.T) {
			db := openReadonlySnapshotTestDB(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "cancel-before" {
				cancel()
			}
			injected := errors.New("FAKE_READONLY_CALLBACK_ERROR_FOR_TEST_ONLY")
			called := false
			err := withReadonlySnapshot(ctx, db, func(tx *gorm.DB) error {
				called = true
				var value string
				if err := tx.Raw("SELECT value FROM snapshot_fixture WHERE id = 1").Scan(&value).Error; err != nil {
					return err
				}
				if value != "original" {
					return fmt.Errorf("snapshot value=%q", value)
				}
				if mode == "callback-error" {
					return injected
				}
				if mode == "cancel-inside" {
					cancel()
				}
				return nil
			})
			switch mode {
			case "success":
				if err != nil || !called {
					t.Fatalf("success called=%v err=%v", called, err)
				}
			case "callback-error":
				if !errors.Is(err, injected) {
					t.Fatalf("callback cause lost: %v", err)
				}
			case "cancel-before":
				if called || !errors.Is(err, context.Canceled) {
					t.Fatalf("pre-cancel called=%v err=%v", called, err)
				}
			case "cancel-inside":
				if !called || !errors.Is(err, context.Canceled) {
					t.Fatalf("active cancellation called=%v err=%v", called, err)
				}
			}
			assertReadonlySnapshotPoolClean(t, db)
		})
	}
}

func TestEscalationPreviewReadonlySnapshotCommitRollbackFailuresDiscardConnection(t *testing.T) {
	for _, mode := range []string{"commit-failure", "rollback-failure", "commit-and-rollback-failure"} {
		t.Run(mode, func(t *testing.T) {
			db := openReadonlySnapshotTestDB(t)
			pool, err := db.DB()
			if err != nil {
				t.Fatal(err)
			}
			connection, err := pool.Conn(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			var commits, rollbacks atomic.Int64
			err = connection.Raw(func(raw any) error {
				physical, ok := raw.(*sqlite3.SQLiteConn)
				if !ok {
					return fmt.Errorf("SQLite raw connection is %T", raw)
				}
				physical.RegisterAuthorizer(func(op int, arg1, arg2, arg3 string) int {
					if op == sqlite3.SQLITE_TRANSACTION {
						switch arg1 {
						case "COMMIT":
							commits.Add(1)
							if mode != "rollback-failure" {
								return sqlite3.SQLITE_DENY
							}
						case "ROLLBACK":
							rollbacks.Add(1)
							if mode != "commit-failure" {
								return sqlite3.SQLITE_DENY
							}
						}
					}
					return sqlite3.SQLITE_OK
				})
				return nil
			})
			if closeErr := connection.Close(); closeErr != nil {
				t.Fatal(closeErr)
			}
			if err != nil {
				t.Fatal(err)
			}
			injected := errors.New("FAKE_READONLY_CALLBACK_FAILURE_FOR_TEST_ONLY")
			err = withReadonlySnapshot(context.Background(), db, func(tx *gorm.DB) error {
				var count int64
				if err := tx.Raw("SELECT COUNT(*) FROM snapshot_fixture").Scan(&count).Error; err != nil {
					return err
				}
				if mode == "rollback-failure" {
					return injected
				}
				return nil
			})
			if err == nil || rollbacks.Load() == 0 || (mode != "rollback-failure" && commits.Load() == 0) {
				t.Fatalf("failure boundary not exercised: error=%v commits=%d rollbacks=%d", err, commits.Load(), rollbacks.Load())
			}
			if mode == "rollback-failure" && !errors.Is(err, injected) {
				t.Fatalf("callback cause not retained: %v", err)
			}
			// If rollback was denied, cleanup MUST discard the contaminated physical
			// connection. Removing an authorizer on that connection cannot rescue it.
			// A healthy pool borrower must already be outside any transaction.
			if mode == "commit-failure" {
				conn, err := pool.Conn(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				err = conn.Raw(func(raw any) error {
					physical, ok := raw.(*sqlite3.SQLiteConn)
					if !ok {
						return fmt.Errorf("raw type=%T", raw)
					}
					physical.RegisterAuthorizer(nil)
					return nil
				})
				closeErr := conn.Close()
				if err != nil {
					t.Fatal(err)
				}
				if closeErr != nil {
					t.Fatal(closeErr)
				}
			}
			assertReadonlySnapshotPoolClean(t, db)
		})
	}
}
