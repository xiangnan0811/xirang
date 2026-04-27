package database

import (
	"database/sql"
	"fmt"
	"strings"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

// openMemoryDB opens a uniquely-named shared in-memory SQLite DB for the test.
// The shared cache + named file are required because multiple QueryRow/Exec
// calls open separate connections in the database/sql pool.
func openMemoryDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared",
		strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// TestFixupLegacyPolicyBwlimit_RenamesLegacyColumn verifies the fixup renames
// bw_limit -> bwlimit on a legacy DB.
func TestFixupLegacyPolicyBwlimit_RenamesLegacyColumn(t *testing.T) {
	db := openMemoryDB(t)
	if _, err := db.Exec(`CREATE TABLE policies (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL,
		bw_limit INTEGER NOT NULL DEFAULT 0
	)`); err != nil {
		t.Fatalf("create legacy policies: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO policies (name, bw_limit) VALUES ('p1', 42)`); err != nil {
		t.Fatalf("seed row: %v", err)
	}

	if err := fixupLegacyPolicyBwlimit(db, "sqlite"); err != nil {
		t.Fatalf("fixup error: %v", err)
	}

	// New column must exist with the migrated data.
	var got int
	if err := db.QueryRow(`SELECT bwlimit FROM policies WHERE name='p1'`).Scan(&got); err != nil {
		t.Fatalf("select bwlimit: %v", err)
	}
	if got != 42 {
		t.Fatalf("expected 42, got %d", got)
	}

	// Old column must be gone.
	var oldCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('policies') WHERE name='bw_limit'`).Scan(&oldCount); err != nil {
		t.Fatalf("count bw_limit: %v", err)
	}
	if oldCount != 0 {
		t.Fatalf("expected bw_limit gone, still present")
	}
}

// TestFixupLegacyPolicyBwlimit_NoopOnFreshDB verifies the fixup is a no-op
// when the canonical column is already in place.
func TestFixupLegacyPolicyBwlimit_NoopOnFreshDB(t *testing.T) {
	db := openMemoryDB(t)
	if _, err := db.Exec(`CREATE TABLE policies (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL,
		bwlimit INTEGER NOT NULL DEFAULT 0
	)`); err != nil {
		t.Fatalf("create fresh policies: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO policies (name, bwlimit) VALUES ('p1', 7)`); err != nil {
		t.Fatalf("seed row: %v", err)
	}

	if err := fixupLegacyPolicyBwlimit(db, "sqlite"); err != nil {
		t.Fatalf("fixup error on fresh DB: %v", err)
	}

	var got int
	if err := db.QueryRow(`SELECT bwlimit FROM policies WHERE name='p1'`).Scan(&got); err != nil {
		t.Fatalf("select bwlimit: %v", err)
	}
	if got != 7 {
		t.Fatalf("expected 7, got %d", got)
	}
}

// TestFixupLegacyPolicyBwlimit_Idempotent verifies running the fixup twice is
// safe (the second run sees the canonical state and does nothing).
func TestFixupLegacyPolicyBwlimit_Idempotent(t *testing.T) {
	db := openMemoryDB(t)
	if _, err := db.Exec(`CREATE TABLE policies (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL,
		bw_limit INTEGER NOT NULL DEFAULT 0
	)`); err != nil {
		t.Fatalf("create legacy policies: %v", err)
	}
	if err := fixupLegacyPolicyBwlimit(db, "sqlite"); err != nil {
		t.Fatalf("first run: %v", err)
	}
	if err := fixupLegacyPolicyBwlimit(db, "sqlite"); err != nil {
		t.Fatalf("second run: %v", err)
	}
}

