package policy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/model"

	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestLockTargetOwnershipSpaceSQLiteWithoutTasksTable(t *testing.T) {
	db := openTargetLockSQLite(t)
	if err := db.Transaction(func(tx *gorm.DB) error {
		return LockTargetOwnershipSpace(tx)
	}); err != nil {
		t.Fatalf("lock ownership space without tasks table: %v", err)
	}
}

func TestLockTargetOwnershipSpacePostgresSerializesTransactions(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	dbA, dbB := openTargetLockPostgresPair(t, dsn)
	locked := make(chan struct{})
	release := make(chan struct{})
	ownerErr := make(chan error, 1)
	go func() {
		ownerErr <- dbA.Transaction(func(tx *gorm.DB) error {
			if err := LockTargetOwnershipSpace(tx); err != nil {
				return err
			}
			close(locked)
			<-release
			return nil
		})
	}()
	<-locked

	waiterErr := make(chan error, 1)
	go func() {
		waiterErr <- dbB.Transaction(func(tx *gorm.DB) error {
			return LockTargetOwnershipSpace(tx)
		})
	}()
	select {
	case err := <-waiterErr:
		t.Fatalf("PostgreSQL waiter acquired lock before owner release: %v", err)
	case <-time.After(250 * time.Millisecond):
	}
	close(release)
	if err := <-ownerErr; err != nil {
		t.Fatalf("PostgreSQL owner transaction: %v", err)
	}
	if err := <-waiterErr; err != nil {
		t.Fatalf("PostgreSQL waiter transaction after release: %v", err)
	}
}

func TestLockTargetOwnershipSpaceSQLiteSerializesEmptyPolicyTable(t *testing.T) {
	dbA, dbB := openTargetLockSQLitePair(t)
	locked := make(chan struct{})
	release := make(chan struct{})
	ownerErr := make(chan error, 1)
	go func() {
		ownerErr <- dbA.Transaction(func(tx *gorm.DB) error {
			if err := LockTargetOwnershipSpace(tx); err != nil {
				return err
			}
			close(locked)
			<-release
			return nil
		})
	}()
	<-locked

	waiterErr := make(chan error, 1)
	go func() {
		waiterErr <- dbB.Transaction(func(tx *gorm.DB) error {
			return LockTargetOwnershipSpace(tx)
		})
	}()
	select {
	case err := <-waiterErr:
		t.Fatalf("waiter acquired empty-table lock before owner release: %v", err)
	case <-time.After(250 * time.Millisecond):
	}
	close(release)
	if err := <-ownerErr; err != nil {
		t.Fatalf("owner transaction: %v", err)
	}
	if err := <-waiterErr; err != nil {
		t.Fatalf("waiter transaction after release: %v", err)
	}
}

func openTargetLockPostgresPair(t *testing.T, dsn string) (*gorm.DB, *gorm.DB) {
	t.Helper()
	dbA, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open PostgreSQL lock connection A: %v", err)
	}
	sqlA, err := dbA.DB()
	if err != nil {
		t.Fatalf("get PostgreSQL lock connection A: %v", err)
	}
	sqlA.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlA.Close() })

	dbB, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open PostgreSQL lock connection B: %v", err)
	}
	sqlB, err := dbB.DB()
	if err != nil {
		t.Fatalf("get PostgreSQL lock connection B: %v", err)
	}
	sqlB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlB.Close() })
	return dbA, dbB
}

func openTargetLockSQLite(t *testing.T) *gorm.DB {
	t.Helper()
	dbA, _ := openTargetLockSQLitePair(t)
	return dbA
}

func openTargetLockSQLitePair(t *testing.T) (*gorm.DB, *gorm.DB) {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "target-lock.db") + "?_busy_timeout=5000&_foreign_keys=on"
	dbA, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open SQLite lock connection A: %v", err)
	}
	sqlA, err := dbA.DB()
	if err != nil {
		t.Fatalf("get SQLite lock connection A: %v", err)
	}
	sqlA.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlA.Close() })
	if err := dbA.AutoMigrate(&model.Policy{}); err != nil {
		t.Fatalf("migrate SQLite policy lock table: %v", err)
	}

	dbB, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open SQLite lock connection B: %v", err)
	}
	sqlB, err := dbB.DB()
	if err != nil {
		t.Fatalf("get SQLite lock connection B: %v", err)
	}
	sqlB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlB.Close() })
	return dbA, dbB
}
