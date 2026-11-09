package processing

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"
)

// These tests execute the shipped down migration against an isolated migrated
// schema. The snapshot uses the production Coordinator and a transactional
// policy reader; the reader's channel gives the test a lock-holding barrier.
func TestProcessingBackfillSnapshotDirectDownPostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		if strings.TrimSpace(os.Getenv("REQUIRE_POSTGRES_PROCESSING_TEST")) == "1" {
			t.Fatal("TEST_POSTGRES_DSN is required")
		}
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	t.Run("AdmissionSetVersion", func(t *testing.T) { quotaAdmissionSetVersionRaces(t, dsn) })
	open := func(t *testing.T) processingBehaviorFixture { return openProcessingBehaviorPostgres(t, dsn) }
	t.Run("SnapshotFirst", func(t *testing.T) {
		f, p := quotaBehaviorFixture(t, open)
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		reached, release := make(chan int, 1), make(chan struct{})
		c, err := NewCoordinator(f.db, f.lease, f.clock.Now, CoordinatorConfig{
			QueueMax: 100, InteractiveReservedSlots: 2, BackgroundSlots: 2, PullLease: 30 * time.Second, AttemptTimeout: 2 * time.Hour, RetryMax: 5,
			BackfillPolicySource: func(ctx context.Context, tx *gorm.DB) (BackfillPolicy, error) {
				var pid int
				if err := tx.Raw("SELECT pg_backend_pid()").Scan(&pid).Error; err != nil {
					return BackfillPolicy{}, err
				}
				reached <- pid
				select {
				case <-release:
					return p.read(ctx, tx)
				case <-ctx.Done():
					return BackfillPolicy{}, ctx.Err()
				}
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		snapshot := make(chan error, 1)
		go func() { _, _, err := c.BackfillSnapshot(ctx); snapshot <- err }()
		var snapshotPID int
		select {
		case snapshotPID = <-reached:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
		db, err := f.db.DB()
		if err != nil {
			t.Fatal(err)
		}
		conn, err := db.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := conn.Close(); err != nil {
				t.Errorf("close snapshot-first connection: %v", err)
			}
		}()
		var downPID int
		if err := conn.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&downPID); err != nil {
			t.Fatal(err)
		}
		down := make(chan error, 1)
		sqlText := quotaDownSQL(t)
		go func() { _, err := conn.ExecContext(ctx, sqlText); down <- err }()
		quotaWaitPGBlocked(t, ctx, db, downPID, snapshotPID)
		close(release)
		select {
		case err := <-snapshot:
			if err != nil {
				t.Fatalf("snapshot: %v", err)
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
		select {
		case err := <-down:
			if err != nil {
				t.Fatalf("direct down: %v", err)
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
		if _, _, err := c.BackfillSnapshot(ctx); err == nil || errors.Is(err, ErrBackfillDenied) {
			t.Fatalf("missing schema snapshot error=%v", err)
		}
	})
	t.Run("DirectDownFirst", func(t *testing.T) {
		f, _ := quotaBehaviorFixture(t, open)
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		db, err := f.db.DB()
		if err != nil {
			t.Fatal(err)
		}
		conn, err := db.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := conn.Close(); err != nil {
				t.Errorf("close direct-down connection: %v", err)
			}
		}()
		var downPID int
		if err := conn.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&downPID); err != nil {
			t.Fatal(err)
		}
		tx, err := conn.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		commitAttempted := false
		defer func() {
			if err := tx.Rollback(); err != nil {
				if errors.Is(err, sql.ErrTxDone) && (commitAttempted || ctx.Err() != nil) {
					return
				}
				t.Errorf("rollback direct-down transaction: %v", err)
			}
		}()
		if _, err := tx.ExecContext(ctx, "LOCK TABLE backup_asset_processing_jobs IN ACCESS EXCLUSIVE MODE"); err != nil {
			t.Fatal(err)
		}
		result := make(chan error, 1)
		go func() { _, _, err := f.coordinator.BackfillSnapshot(ctx); result <- err }()
		quotaWaitAnyPGBlocked(t, ctx, db, downPID)
		if _, err := tx.ExecContext(ctx, quotaDownBody(t)); err != nil {
			t.Fatalf("direct down: %v", err)
		}
		commitAttempted = true
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		select {
		case err := <-result:
			if err == nil || errors.Is(err, ErrBackfillDenied) {
				t.Fatalf("snapshot after down error=%v", err)
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	})
	t.Run("UsedGuardRollback", func(t *testing.T) {
		f, _ := quotaBehaviorFixture(t, open)
		quotaRequireWork(t, f, validWorkDescriptor(), "charged", PriorityBackground)
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		db, err := f.db.DB()
		if err != nil {
			t.Fatal(err)
		}
		conn, err := db.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := conn.Close(); err != nil {
				t.Errorf("close used-guard connection: %v", err)
			}
		}()
		if _, err := conn.ExecContext(ctx, quotaDownSQL(t)); err == nil {
			t.Fatal("used direct down succeeded")
		}
		if _, err := conn.ExecContext(ctx, "ROLLBACK"); err != nil {
			t.Fatal(err)
		}
		_, usage, err := f.coordinator.BackfillSnapshot(ctx)
		if err != nil || usage.Jobs != 1 {
			t.Fatalf("snapshot after guard rollback=%+v err=%v", usage, err)
		}
	})
	t.Run("MissingSeedRollback", func(t *testing.T) {
		f, _ := quotaBehaviorFixture(t, open)
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if err := f.db.Exec("DELETE FROM backup_asset_processing_backfill_ledger WHERE id = 1").Error; err != nil {
			t.Fatal(err)
		}
		db, err := f.db.DB()
		if err != nil {
			t.Fatal(err)
		}
		conn, err := db.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := conn.Close(); err != nil {
				t.Errorf("close missing-seed connection: %v", err)
			}
		}()
		if _, err := conn.ExecContext(ctx, quotaDownSQL(t)); err == nil {
			t.Fatal("direct down accepted missing seed")
		}
		if _, err := conn.ExecContext(ctx, "ROLLBACK"); err != nil {
			t.Fatal(err)
		}
		if _, _, err := f.coordinator.BackfillSnapshot(ctx); err == nil || errors.Is(err, ErrBackfillDenied) {
			t.Fatalf("missing seed snapshot error=%v", err)
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := tx.Rollback(); err != nil {
				if errors.Is(err, sql.ErrTxDone) && ctx.Err() != nil {
					return
				}
				t.Errorf("rollback missing-seed lock check: %v", err)
			}
		}()
		if _, err := tx.ExecContext(ctx, "LOCK TABLE backup_asset_processing_jobs IN ACCESS EXCLUSIVE MODE"); err != nil {
			t.Fatalf("snapshot leaked jobs lock: %v", err)
		}
	})
}

func quotaDownSQL(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("../../database/migrations/postgres/000099_processing_backfill_quota.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func quotaDownBody(t *testing.T) string {
	t.Helper()
	text := strings.TrimSpace(quotaDownSQL(t))
	if !strings.HasPrefix(text, "BEGIN;") || !strings.HasSuffix(text, "COMMIT;") {
		t.Fatal("unexpected direct down transaction wrapper")
	}
	return strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(text, "BEGIN;"), "COMMIT;"))
}

func quotaWaitPGBlocked(t *testing.T, ctx context.Context, db *sql.DB, waiter, blocker int) {
	t.Helper()
	for {
		var blocked bool
		if err := db.QueryRowContext(ctx, "SELECT $1 = ANY(pg_blocking_pids($2))", blocker, waiter).Scan(&blocked); err != nil {
			t.Fatalf("lock probe: %v", err)
		}
		if blocked {
			return
		}
		if ctx.Err() != nil {
			t.Fatalf("waiting for PostgreSQL lock: %v", ctx.Err())
		}
	}
}

func quotaWaitAnyPGBlocked(t *testing.T, ctx context.Context, db *sql.DB, blocker int) {
	t.Helper()
	for {
		var blocked bool
		if err := db.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM pg_stat_activity WHERE $1 = ANY(pg_blocking_pids(pid)))", blocker).Scan(&blocked); err != nil {
			t.Fatalf("lock probe: %v", err)
		}
		if blocked {
			return
		}
		if ctx.Err() != nil {
			t.Fatalf("waiting for PostgreSQL lock: %v", ctx.Err())
		}
	}
}
