package task

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/database"
	"xirang/backend/internal/model"

	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func openMigratedBatchCommandSkipSQLiteDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "batch-command-skip-migrated.db")), &gorm.Config{
		NowFunc: func() time.Time { return time.Now().UTC() },
	})
	if err != nil {
		t.Fatalf("open migrated SQLite database: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get migrated SQLite database: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := database.RunMigrations(db, "sqlite"); err != nil {
		t.Fatalf("run SQLite migrations: %v", err)
	}
	return db
}

func openMigratedBatchCommandSkipPostgresDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		if strings.TrimSpace(os.Getenv(requirePostgresTaskTerminalTestEnv)) == "1" {
			t.Fatalf("TEST_POSTGRES_DSN is required when %s=1", requirePostgresTaskTerminalTestEnv)
		}
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	parsed, err := url.Parse(dsn)
	if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") {
		t.Fatalf("TEST_POSTGRES_DSN must be a PostgreSQL URL: %v", err)
	}
	base, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open PostgreSQL base connection: %v", err)
	}
	baseSQL, err := base.DB()
	if err != nil {
		t.Fatalf("get PostgreSQL base connection: %v", err)
	}
	schema := fmt.Sprintf("xirang_batch_skip_migrated_%d", time.Now().UTC().UnixNano())
	if _, err := baseSQL.Exec("CREATE SCHEMA " + schema); err != nil {
		_ = baseSQL.Close()
		t.Fatalf("create isolated PostgreSQL schema: %v", err)
	}
	t.Cleanup(func() {
		if _, err := baseSQL.Exec("DROP SCHEMA IF EXISTS " + schema + " CASCADE"); err != nil {
			t.Errorf("drop isolated PostgreSQL schema: %v", err)
		}
		_ = baseSQL.Close()
	})
	scoped := *parsed
	query := scoped.Query()
	query.Set("search_path", schema)
	query.Set("timezone", "UTC")
	scoped.RawQuery = query.Encode()
	db, err := gorm.Open(postgres.Open(scoped.String()), &gorm.Config{
		NowFunc: func() time.Time { return time.Now().UTC() },
	})
	if err != nil {
		t.Fatalf("open isolated PostgreSQL connection: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get isolated PostgreSQL connection: %v", err)
	}
	sqlDB.SetMaxOpenConns(8)
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := database.RunMigrations(db, "postgres"); err != nil {
		t.Fatalf("run PostgreSQL migrations: %v", err)
	}
	return db
}

func assertMigratedBatchCommandSkip(t *testing.T, db *gorm.DB, taskEntity model.Task, label string) {
	t.Helper()
	manager := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, nil, 8, 90)
	shutdownManagerOnCleanup(t, manager)
	if err := manager.skipTask(taskEntity, "skip-migrated", nil, "前置任务失败，链式执行跳过"); err != nil {
		t.Fatalf("skip %s v1 batch command task: %v", label, err)
	}
	var run model.TaskRun
	if err := db.Where("task_id = ?", taskEntity.ID).Take(&run).Error; err != nil {
		t.Fatalf("load %s skipped run: %v", label, err)
	}
	if run.Status != model.TaskRunStatusSkipped || run.FinishedAt == nil {
		t.Fatalf("%s skipped run=%+v, want terminal skipped with finish time", label, run)
	}
	evidence := readBatchCommandEvidence(t, db, run.ID)
	if evidence.Outcome != "unknown" || evidence.FinishedAt == nil {
		t.Fatalf("%s skipped evidence=%+v, want unknown terminal evidence", label, evidence)
	}
}

func TestBatchCommandRetrySkipEvidenceUnderMigratedSQLiteGuards(t *testing.T) {
	db := openMigratedBatchCommandSkipSQLiteDB(t)
	taskEntity := seedBatchCommandSkipTask(t, db, "batch-skip-migrated-sqlite", 1, true)
	assertMigratedBatchCommandSkip(t, db, taskEntity, "SQLite")
}

func TestBatchCommandRetrySkipEvidenceUnderMigratedPostgresGuards(t *testing.T) {
	db := openMigratedBatchCommandSkipPostgresDB(t)
	taskEntity := seedBatchCommandSkipTask(t, db, "batch-skip-migrated-postgres", 1, true)
	assertMigratedBatchCommandSkip(t, db, taskEntity, "PostgreSQL")
}
