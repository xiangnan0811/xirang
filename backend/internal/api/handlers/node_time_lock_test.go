package handlers

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"xirang/backend/internal/model"
	gormrepo "xirang/backend/internal/repository/gorm"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/mattn/go-sqlite3"
	"gorm.io/gorm"
)

// TestNodeTimestampMergeLocksSQLitePostgres exercises the real repository with
// separate database connections while the first merge holds its transaction open.
func TestNodeTimestampMergeLocksSQLitePostgres(t *testing.T) {
	for _, engine := range []string{"sqlite", "postgres"} {
		t.Run(engine, func(t *testing.T) {
			t.Setenv("APP_ENV", "development")
			db := openMigratedRetryTransactionDB(t, engine)
			sqlDB, err := db.DB()
			if err != nil {
				t.Fatal(err)
			}
			// PostgreSQL migrations can retain a dedicated connection in this pool.
			sqlDB.SetMaxOpenConns(sqlDB.Stats().InUse + 2)

			initialStart := time.Date(2025, 1, 2, 1, 0, 0, 0, time.UTC)
			initialEnd := time.Date(2025, 1, 2, 5, 0, 0, 0, time.UTC)
			updatedStart := time.Date(2025, 1, 2, 2, 0, 0, 0, time.UTC)
			updatedEnd := time.Date(2025, 1, 2, 4, 0, 0, 0, time.UTC)
			node := model.Node{
				Name: "timestamp-lock-node", Host: "192.0.2.10", Username: "tester",
				AuthType: "key", BackupDir: "timestamp-lock-node",
				MaintenanceStart: &initialStart, MaintenanceEnd: &initialEnd,
			}
			if err := db.Create(&node).Error; err != nil {
				t.Fatalf("create node: %v", err)
			}

			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			// Pin the contender before starting the holder, guaranteeing that its
			// session settings and both transactions use different pool connections.
			err = db.WithContext(ctx).Connection(func(contenderDB *gorm.DB) error {
				// Keep the pinned connection while preventing session setup SQL from
				// carrying statement state into the repository transactions.
				contenderDB = contenderDB.Session(&gorm.Session{NewDB: true})
				if engine == "sqlite" {
					var original int
					if err := contenderDB.Raw("PRAGMA busy_timeout").Scan(&original).Error; err != nil {
						return fmt.Errorf("read SQLite busy timeout: %w", err)
					}
					if err := contenderDB.Exec("PRAGMA busy_timeout = 0").Error; err != nil {
						return fmt.Errorf("set SQLite busy timeout: %w", err)
					}
					defer func() {
						restoreCtx, restoreCancel := context.WithTimeout(context.Background(), 2*time.Second)
						defer restoreCancel()
						if err := contenderDB.WithContext(restoreCtx).Exec(fmt.Sprintf("PRAGMA busy_timeout = %d", original)).Error; err != nil {
							t.Errorf("restore SQLite busy timeout: %v", err)
						}
					}()
				} else {
					var original string
					if err := contenderDB.Raw("SHOW lock_timeout").Scan(&original).Error; err != nil {
						return fmt.Errorf("read PostgreSQL lock timeout: %w", err)
					}
					if err := contenderDB.Exec("SET lock_timeout = '200ms'").Error; err != nil {
						return fmt.Errorf("set PostgreSQL lock timeout: %w", err)
					}
					defer func() {
						restoreCtx, restoreCancel := context.WithTimeout(context.Background(), 2*time.Second)
						defer restoreCancel()
						if err := contenderDB.WithContext(restoreCtx).Exec("SELECT set_config('lock_timeout', ?, false)", original).Error; err != nil {
							t.Errorf("restore PostgreSQL lock timeout: %v", err)
						}
					}()
				}

				holderNode, contenderNode := node, node
				entered := make(chan model.Node, 1)
				release := make(chan struct{})
				holderDone := make(chan error, 1)
				var releaseOnce sync.Once
				releaseHolder := func() { releaseOnce.Do(func() { close(release) }) }
				holderFinished := false
				go func() {
					err := gormrepo.NewNodeRepository(db).Update(ctx, &holderNode, false, func(current *model.Node) error {
						entered <- *current
						select {
						case <-release:
							current.MaintenanceStart = &updatedStart
							return nil
						case <-ctx.Done():
							return ctx.Err()
						}
					})
					holderDone <- err
				}()
				defer func() {
					releaseHolder()
					if !holderFinished {
						cleanupTimer := time.NewTimer(5 * time.Second)
						defer cleanupTimer.Stop()
						select {
						case <-holderDone:
						case <-cleanupTimer.C:
							t.Error("holder did not finish during cleanup")
						}
					}
				}()

				assertPair := func(where string, current model.Node, start, end time.Time) {
					t.Helper()
					if current.MaintenanceStart == nil || !current.MaintenanceStart.Equal(start) || current.MaintenanceEnd == nil || !current.MaintenanceEnd.Equal(end) {
						t.Fatalf("%s: got maintenance pair (%v, %v), want (%v, %v)", where, current.MaintenanceStart, current.MaintenanceEnd, start, end)
					}
				}
				select {
				case current := <-entered:
					assertPair("holder callback", current, initialStart, initialEnd)
				case err := <-holderDone:
					holderFinished = true
					t.Fatalf("holder returned before its callback: %v", err)
				case <-ctx.Done():
					t.Fatal("holder did not reach its callback")
				}

				var callbackReached atomic.Bool
				contenderRepo := gormrepo.NewNodeRepository(contenderDB)
				err := contenderRepo.Update(ctx, &contenderNode, false, func(current *model.Node) error {
					callbackReached.Store(true)
					current.MaintenanceEnd = &updatedEnd
					return nil
				})
				if callbackReached.Load() {
					t.Fatal("contender reached its merge callback while the holder retained the lock")
				}
				if engine == "sqlite" {
					var sqliteErr sqlite3.Error
					if !errors.As(err, &sqliteErr) || (sqliteErr.Code != sqlite3.ErrBusy && sqliteErr.Code != sqlite3.ErrLocked) {
						t.Fatalf("contender should encounter SQLite lock contention, got %v", err)
					}
				} else {
					var pgErr *pgconn.PgError
					if !errors.As(err, &pgErr) || pgErr.Code != "55P03" {
						t.Fatalf("contender should encounter PostgreSQL lock timeout (55P03), got %v", err)
					}
				}

				releaseHolder()
				select {
				case err := <-holderDone:
					holderFinished = true
					if err != nil {
						t.Fatalf("holder update: %v", err)
					}
				case <-ctx.Done():
					t.Fatal("holder did not commit before deadline")
				}

				var retryCurrent model.Node
				retryCalled := false
				err = contenderRepo.Update(ctx, &contenderNode, false, func(current *model.Node) error {
					retryCalled = true
					retryCurrent = *current
					current.MaintenanceEnd = &updatedEnd
					return nil
				})
				if err != nil {
					t.Fatalf("retry contender update: %v", err)
				}
				if !retryCalled {
					t.Fatal("retry did not reach its merge callback")
				}
				assertPair("retry callback", retryCurrent, updatedStart, initialEnd)
				var persisted model.Node
				if err := contenderDB.First(&persisted, node.ID).Error; err != nil {
					t.Fatalf("load persisted node: %v", err)
				}
				assertPair("persisted node", persisted, updatedStart, updatedEnd)
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}
