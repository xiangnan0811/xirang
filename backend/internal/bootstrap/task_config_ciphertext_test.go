package bootstrap

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/mattn/go-sqlite3"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"xirang/backend/internal/model"
	"xirang/backend/internal/secure"
)

func TestEncryptPlaintextTaskExecutorConfigsSQLitePostgres(t *testing.T) {
	for _, engine := range []struct {
		name string
		open func(*testing.T) *gorm.DB
	}{
		{"sqlite", openAccountRecoverySQLite},
		{"postgres", openAccountRecoveryPostgres},
	} {
		t.Run(engine.name, func(t *testing.T) {
			db := engine.open(t)
			now := time.Date(2026, 1, 2, 3, 4, 5, 123000000, time.UTC)
			if err := db.Exec("INSERT INTO nodes (id, name, host, username, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)", 501, "config-node", "127.0.0.1", "fixture", now, now).Error; err != nil {
				t.Fatal("seed node")
			}
			encrypted, err := secure.EncryptString("existing encrypted config")
			if err != nil {
				t.Fatal("encrypt fixture")
			}
			configs := []struct {
				executor string
				config   string
				archived bool
			}{
				{"restic", "  {\"repository_password\":\"FAKE_CONFIG_SECRET\"}\n", false},
				{"restic", " { malformed restic\n", true},
				{"rclone", "\tmalformed non-restic: FAKE_CONFIG_SECRET\r\n", false},
				{"local", encrypted, false},
				{"local", " \t\n", false},
				{"local", "", false},
			}
			for index, fixture := range configs {
				var archivedAt any
				if fixture.archived {
					archivedAt = now
				}
				if err := db.Exec("INSERT INTO tasks (id, name, node_id, command, executor_type, executor_config, status, archived_at, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)", index+1, fmt.Sprintf("config-task-%d", index), 501, "preserve command", fixture.executor, fixture.config, "pending", archivedAt, now, now).Error; err != nil {
					t.Fatal("seed task")
				}
			}
			// Include a candidate beyond the first keyset page.
			for id := 7; id <= 258; id++ {
				if err := db.Exec("INSERT INTO tasks (id, name, node_id, executor_type, executor_config, status, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)", id, fmt.Sprintf("config-task-%d", id), 501, "local", "", "pending", now, now).Error; err != nil {
					t.Fatal("seed pagination task")
				}
			}
			if err := db.Exec("UPDATE tasks SET executor_config = ? WHERE id = ?", "last page secret", 258).Error; err != nil {
				t.Fatal("seed last page")
			}
			before := taskConfigCiphertextSnapshot(t, db)

			// Fail an update after one row commits, then prove a retry continues safely.
			var logOutput bytes.Buffer
			logDB := db.Session(&gorm.Session{Logger: logger.New(log.New(&logOutput, "", 0), logger.Config{LogLevel: logger.Info})})
			updates := 0
			if err := db.Callback().Update().Before("gorm:update").Register("task-config-test-failure", func(tx *gorm.DB) {
				updates++
				if updates == 2 {
					_ = tx.AddError(errors.New("FAKE_CONFIG_SECRET injected database error"))
				}
			}); err != nil {
				t.Fatal("register update failure")
			}
			err = EncryptPlaintextTaskExecutorConfigs(logDB)
			if err := db.Callback().Update().Remove("task-config-test-failure"); err != nil {
				t.Fatal("remove update failure")
			}
			if err == nil || strings.Contains(err.Error(), "FAKE_CONFIG_SECRET") {
				t.Fatalf("expected sanitized write failure, got %v", err)
			}
			if logOutput.Len() != 0 {
				t.Fatal("repair wrote SQL diagnostics to caller logger")
			}
			partial := taskConfigCiphertextSnapshot(t, db)
			if !secure.IsEncrypted(partial[0].ExecutorConfig) || partial[1].ExecutorConfig != before[1].ExecutorConfig {
				t.Fatal("write failure did not preserve partial committed state")
			}
			if err := EncryptPlaintextTaskExecutorConfigs(logDB); err != nil {
				t.Fatalf("retry encryption: %v", err)
			}
			if logOutput.Len() != 0 {
				t.Fatal("retry wrote SQL diagnostics to caller logger")
			}
			if err := logDB.Exec("SELECT 1").Error; err != nil || logOutput.Len() == 0 {
				t.Fatal("repair changed the caller's logger")
			}
			after := taskConfigCiphertextSnapshot(t, db)
			for index := range before {
				original := before[index].ExecutorConfig
				actual := after[index].ExecutorConfig
				if strings.TrimSpace(original) != "" && !secure.IsEncrypted(original) {
					if !secure.IsEncrypted(actual) {
						t.Fatalf("task %d remains plaintext", before[index].ID)
					}
					plain, err := secure.DecryptIfNeeded(actual)
					if err != nil || plain != original {
						t.Fatalf("task %d bytes changed after encryption", before[index].ID)
					}
				} else if actual != original {
					t.Fatalf("task %d blank or existing ciphertext changed", before[index].ID)
				}
				before[index].ExecutorConfig = actual
			}
			if !reflect.DeepEqual(before, after) {
				t.Fatal("repair changed unrelated task columns or revision")
			}
			if partial[0].ExecutorConfig != after[0].ExecutorConfig {
				t.Fatal("retry rewrote previously committed ciphertext")
			}
			if err := EncryptPlaintextTaskExecutorConfigs(db); err != nil {
				t.Fatalf("idempotent encryption: %v", err)
			}
			if !reflect.DeepEqual(after, taskConfigCiphertextSnapshot(t, db)) {
				t.Fatal("idempotent run changed tasks")
			}
			assertTaskConfigRepairLock(t, db)

			logOutput.Reset()
			if err := db.Callback().Query().Before("gorm:query").Register("task-config-test-read-failure", func(tx *gorm.DB) {
				_ = tx.AddError(errors.New("FAKE_CONFIG_SECRET injected read error"))
			}); err != nil {
				t.Fatal("register read failure")
			}
			err = EncryptPlaintextTaskExecutorConfigs(logDB)
			if err := db.Callback().Query().Remove("task-config-test-read-failure"); err != nil {
				t.Fatal("remove read failure")
			}
			if err == nil || strings.Contains(err.Error(), "FAKE_CONFIG_SECRET") {
				t.Fatalf("expected sanitized read failure, got %v", err)
			}
			if logOutput.Len() != 0 {
				t.Fatal("failed repair wrote SQL diagnostics to caller logger")
			}

			// Change the page's candidates before their transactions begin. Each
			// locked read must observe the new value rather than overwrite it.
			if err := db.Exec("UPDATE tasks SET executor_config = ? WHERE id IN ?", "page candidate", []int{2, 3, 4}).Error; err != nil {
				t.Fatal("seed changing candidates")
			}
			changed := false
			var changeErr error
			if err := db.Callback().Query().After("gorm:query").Register("task-config-test-concurrent-change", func(tx *gorm.DB) {
				if changed {
					return
				}
				changed = true
				changeErr = db.Exec("UPDATE tasks SET executor_config = ? WHERE id = ?", encrypted, 2).Error
				if changeErr == nil {
					changeErr = db.Exec("UPDATE tasks SET executor_config = ? WHERE id = ?", " \t", 3).Error
				}
				if changeErr == nil {
					changeErr = db.Exec("DELETE FROM tasks WHERE id = ?", 4).Error
				}
			}); err != nil {
				t.Fatal("register candidate change")
			}
			err = EncryptPlaintextTaskExecutorConfigs(db)
			if removeErr := db.Callback().Query().Remove("task-config-test-concurrent-change"); removeErr != nil {
				t.Fatal("remove candidate change")
			}
			if err != nil || changeErr != nil || !changed {
				t.Fatal("repair or candidate change failed")
			}
			changedTasks := taskConfigCiphertextSnapshot(t, db)
			if changedTasks[1].ExecutorConfig != encrypted || changedTasks[2].ExecutorConfig != " \t" || changedTasks[3].ID != 5 {
				t.Fatal("repair overwrote changed or deleted candidate")
			}

			if err := db.Exec("UPDATE tasks SET executor_config = ? WHERE id = ?", "key failure secret", 2).Error; err != nil {
				t.Fatal("seed missing key candidate")
			}
			t.Setenv("APP_ENV", "production")
			t.Setenv("DATA_ENCRYPTION_KEY", "")
			secure.ResetForTesting()
			err = EncryptPlaintextTaskExecutorConfigs(logDB)
			if err == nil || strings.Contains(err.Error(), "key failure secret") || strings.Contains(err.Error(), "DATA_ENCRYPTION_KEY") {
				t.Fatalf("expected sanitized key failure, got %v", err)
			}
			if got := taskConfigCiphertextSnapshot(t, db)[1].ExecutorConfig; got != "key failure secret" {
				t.Fatal("missing key changed plaintext")
			}
			t.Setenv("APP_ENV", "development")
			secure.ResetForTesting()
			keylessBefore := taskConfigCiphertextSnapshot(t, db)
			err = EncryptPlaintextTaskExecutorConfigs(db)
			if !errors.Is(err, errTaskConfigKey) || !reflect.DeepEqual(keylessBefore, taskConfigCiphertextSnapshot(t, db)) {
				t.Fatal("development without a configured key must reject plaintext without changes")
			}
			if err := db.Exec("UPDATE tasks SET executor_config = ? WHERE id = ?", encrypted, 2).Error; err != nil {
				t.Fatal("restore already-encrypted candidate")
			}
			keylessBefore = taskConfigCiphertextSnapshot(t, db)
			if err := EncryptPlaintextTaskExecutorConfigs(db); err != nil || !reflect.DeepEqual(keylessBefore, taskConfigCiphertextSnapshot(t, db)) {
				t.Fatal("keyless repair must allow existing ciphertext and blank configs")
			}
			if err := db.Exec("DELETE FROM tasks").Error; err != nil {
				t.Fatal("clear synthetic task fixture")
			}
			if err := EncryptPlaintextTaskExecutorConfigs(db); err != nil {
				t.Fatal("keyless repair must allow an empty table")
			}
		})
	}
}

func taskConfigCiphertextSnapshot(t *testing.T, db *gorm.DB) []model.Task {
	t.Helper()
	var tasks []model.Task
	if err := db.Session(&gorm.Session{SkipHooks: true}).Order("id").Find(&tasks).Error; err != nil {
		t.Fatal("read raw task snapshot")
	}
	return tasks
}

func assertTaskConfigRepairLock(t *testing.T, db *gorm.DB) {
	t.Helper()
	if err := db.Exec("UPDATE tasks SET executor_config = ? WHERE id = ?", "lock probe plaintext", 2).Error; err != nil {
		t.Fatal("seed lock probe")
	}
	pool, err := db.DB()
	if err != nil {
		t.Fatal("get lock probe pool")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	probe, err := pool.Conn(ctx)
	if err != nil {
		t.Fatal("acquire independent lock probe connection")
	}
	defer func() { _ = probe.Close() }()
	if db.Name() == "sqlite" {
		if _, err := probe.ExecContext(ctx, "PRAGMA busy_timeout=0"); err != nil {
			t.Fatal("configure SQLite lock probe")
		}
	}
	called := false
	var probeErr error
	if err := db.Callback().Update().Before("gorm:update").Register("task-config-test-lock-probe", func(tx *gorm.DB) {
		if called || tx.Statement.Table != "tasks" {
			return
		}
		called = true
		if db.Name() == "postgres" {
			var probePID, repairPID int
			if err := probe.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&probePID); err != nil {
				probeErr = err
				return
			}
			if err := tx.Statement.ConnPool.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&repairPID); err != nil {
				probeErr = err
				return
			}
			if probePID == repairPID {
				probeErr = errors.New("probe used repair connection")
				return
			}
			var id int
			err := probe.QueryRowContext(ctx, "SELECT id FROM tasks WHERE id = $1 FOR UPDATE NOWAIT", 2).Scan(&id)
			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) || pgErr.Code != "55P03" {
				probeErr = errors.New("PostgreSQL row lock was not held")
			}
			return
		}
		_, err := probe.ExecContext(ctx, "BEGIN IMMEDIATE")
		if err == nil {
			_, _ = probe.ExecContext(ctx, "ROLLBACK")
		}
		var sqliteErr sqlite3.Error
		if !errors.As(err, &sqliteErr) || sqliteErr.Code != sqlite3.ErrBusy {
			probeErr = errors.New("SQLite immediate writer lock was not held")
		}
	}); err != nil {
		t.Fatal("register lock probe")
	}
	err = EncryptPlaintextTaskExecutorConfigs(db)
	if removeErr := db.Callback().Update().Remove("task-config-test-lock-probe"); removeErr != nil {
		t.Fatal("remove lock probe")
	}
	if err != nil || !called || probeErr != nil {
		t.Fatal("repair did not hold the expected independent writer lock")
	}
	if db.Name() == "postgres" {
		var id int
		if err := probe.QueryRowContext(ctx, "SELECT id FROM tasks WHERE id = $1 FOR UPDATE NOWAIT", 2).Scan(&id); err != nil {
			t.Fatal("PostgreSQL row lock remained held after repair")
		}
	} else {
		if _, err := probe.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
			t.Fatal("SQLite writer lock remained held after repair")
		}
		if _, err := probe.ExecContext(ctx, "ROLLBACK"); err != nil {
			t.Fatal("release SQLite probe transaction")
		}
	}
}
