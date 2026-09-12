package task

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"xirang/backend/internal/model"
	gormrepo "xirang/backend/internal/repository/gorm"

	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestTaskTargetOwnershipConcurrencySQLite(t *testing.T) {
	dbA, dbB := openTargetOwnershipSQLite(t)
	exerciseTargetOwnershipConcurrency(t, dbA, dbB)
}

func TestTaskTargetOwnershipConcurrencyPostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	dbA, dbB := openTargetOwnershipPostgres(t, dsn)
	exerciseTargetOwnershipConcurrency(t, dbA, dbB)
}

func exerciseTargetOwnershipConcurrency(t *testing.T, dbA, dbB *gorm.DB) {
	t.Helper()
	nodeA := model.Node{Name: "target-ownership-node-a", Host: "127.0.0.1", Username: "root", AuthType: "key", BackupDir: "target-ownership-node-a"}
	nodeB := model.Node{Name: "target-ownership-node-b", Host: "127.0.0.1", Username: "root", AuthType: "key", BackupDir: "target-ownership-node-b"}
	if err := dbA.Create(&nodeA).Error; err != nil {
		t.Fatalf("create node A: %v", err)
	}
	if err := dbA.Create(&nodeB).Error; err != nil {
		t.Fatalf("create node B: %v", err)
	}

	t.Run("same target is single-owner", func(t *testing.T) {
		target := filepath.Join(t.TempDir(), "shared-target")
		results := runConcurrentTaskCreates(t, dbA, dbB,
			model.Node{ID: nodeA.ID}, model.Node{ID: nodeB.ID}, target, target)
		if (results[0].err == nil) == (results[1].err == nil) {
			t.Fatalf("same-target creates returned errors %v and %v, want exactly one success", results[0].err, results[1].err)
		}
		var count int64
		if err := dbA.Model(&model.Task{}).Where("rsync_target = ?", target).Count(&count).Error; err != nil {
			t.Fatalf("count same-target tasks: %v", err)
		}
		if count != 1 {
			t.Fatalf("same-target task count=%d, want 1", count)
		}
	})

	t.Run("different targets both persist", func(t *testing.T) {
		targetA := filepath.Join(t.TempDir(), "target-a")
		targetB := filepath.Join(t.TempDir(), "target-b")
		results := runConcurrentTaskCreates(t, dbA, dbB,
			model.Node{ID: nodeA.ID}, model.Node{ID: nodeB.ID}, targetA, targetB)
		if results[0].err != nil || results[1].err != nil {
			t.Fatalf("non-overlapping creates returned errors %v and %v", results[0].err, results[1].err)
		}
		var count int64
		if err := dbA.Model(&model.Task{}).Where("rsync_target IN ?", []string{targetA, targetB}).Count(&count).Error; err != nil {
			t.Fatalf("count non-overlapping tasks: %v", err)
		}
		if count != 2 {
			t.Fatalf("non-overlapping task count=%d, want 2", count)
		}
	})
}

type concurrentTaskCreateResult struct {
	task model.Task
	err  error
}

func runConcurrentTaskCreates(t *testing.T, dbA, dbB *gorm.DB, nodeA, nodeB model.Node, targetA, targetB string) [2]concurrentTaskCreateResult {
	t.Helper()
	services := [2]*TaskApiService{
		NewTaskApiService(gormrepo.NewTaskRepository(dbA), gormrepo.NewNodeRepository(dbA), gormrepo.NewPolicyRepository(dbA), nil),
		NewTaskApiService(gormrepo.NewTaskRepository(dbB), gormrepo.NewNodeRepository(dbB), gormrepo.NewPolicyRepository(dbB), nil),
	}
	nodes := [2]model.Node{nodeA, nodeB}
	targets := [2]string{targetA, targetB}
	start := make(chan struct{})
	results := [2]concurrentTaskCreateResult{}
	var wg sync.WaitGroup
	wg.Add(2)
	for i := range services {
		go func(i int) {
			defer wg.Done()
			<-start
			results[i].task, results[i].err = services[i].CreateTask(context.Background(), CreateTaskInput{
				Name:         fmt.Sprintf("target-ownership-task-%d", i),
				NodeID:       nodes[i].ID,
				ExecutorType: "rsync",
				RsyncSource:  "/source",
				RsyncTarget:  targets[i],
			})
		}(i)
	}
	close(start)
	wg.Wait()
	return results
}

func openTargetOwnershipSQLite(t *testing.T) (*gorm.DB, *gorm.DB) {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "target-ownership.db") + "?_busy_timeout=5000&_foreign_keys=on"
	dbA, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open SQLite connection A: %v", err)
	}
	sqlA, err := dbA.DB()
	if err != nil {
		t.Fatalf("get SQLite connection A: %v", err)
	}
	sqlA.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlA.Close() })
	if err := dbA.AutoMigrate(&model.Node{}, &model.Policy{}, &model.Task{}); err != nil {
		t.Fatalf("migrate SQLite target ownership tables: %v", err)
	}

	dbB, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open SQLite connection B: %v", err)
	}
	sqlB, err := dbB.DB()
	if err != nil {
		t.Fatalf("get SQLite connection B: %v", err)
	}
	sqlB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlB.Close() })
	return dbA, dbB
}

func openTargetOwnershipPostgres(t *testing.T, dsn string) (*gorm.DB, *gorm.DB) {
	t.Helper()
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
	schema := fmt.Sprintf("xirang_target_ownership_%d", time.Now().UTC().UnixNano())
	if _, err := baseSQL.Exec("CREATE SCHEMA " + schema); err != nil {
		_ = baseSQL.Close()
		t.Fatalf("create PostgreSQL target ownership schema: %v", err)
	}
	t.Cleanup(func() {
		if _, err := baseSQL.Exec("DROP SCHEMA IF EXISTS " + schema + " CASCADE"); err != nil {
			t.Errorf("drop PostgreSQL target ownership schema: %v", err)
		}
		_ = baseSQL.Close()
	})

	scoped := *parsed
	query := scoped.Query()
	query.Set("search_path", schema)
	query.Set("timezone", "UTC")
	scoped.RawQuery = query.Encode()
	dbA, err := gorm.Open(postgres.Open(scoped.String()), &gorm.Config{})
	if err != nil {
		t.Fatalf("open PostgreSQL connection A: %v", err)
	}
	dbB, err := gorm.Open(postgres.Open(scoped.String()), &gorm.Config{})
	if err != nil {
		t.Fatalf("open PostgreSQL connection B: %v", err)
	}
	sqlA, err := dbA.DB()
	if err != nil {
		t.Fatalf("get PostgreSQL connection A: %v", err)
	}
	sqlB, err := dbB.DB()
	if err != nil {
		t.Fatalf("get PostgreSQL connection B: %v", err)
	}
	sqlA.SetMaxOpenConns(1)
	sqlB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlA.Close() })
	t.Cleanup(func() { _ = sqlB.Close() })
	if err := dbA.AutoMigrate(&model.Node{}, &model.Policy{}, &model.Task{}); err != nil {
		t.Fatalf("migrate PostgreSQL target ownership tables: %v", err)
	}
	return dbA, dbB
}
