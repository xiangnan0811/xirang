package database

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// TestMigration045_BackfillSLOID verifies the 000045 migration:
//   - adds slo_id column
//   - creates the partial index
//   - backfills slo_id for well-formed XR-SLO-<n> rows
//   - leaves slo_id NULL for malformed XR-SLO-<garbage> rows
//   - leaves slo_id NULL for non-SLO error_codes
func TestMigration045_BackfillSLOID(t *testing.T) {
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared",
		strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}

	// Bootstrap a minimal alerts table that matches the pre-000045 schema
	// shape we care about for this test. Real migrations 000001..000044
	// build the full alerts table; for a focused unit test we only need the
	// columns the 000045 SQL touches.
	if err := db.Exec(`CREATE TABLE alerts (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		node_id INTEGER NOT NULL,
		node_name TEXT NOT NULL,
		error_code TEXT NOT NULL,
		message TEXT NOT NULL,
		severity TEXT NOT NULL,
		status TEXT NOT NULL,
		retryable INTEGER NOT NULL DEFAULT 0,
		triggered_at DATETIME,
		tags TEXT NOT NULL DEFAULT '[]',
		last_level_fired INTEGER NOT NULL DEFAULT -1,
		created_at DATETIME,
		updated_at DATETIME
	)`).Error; err != nil {
		t.Fatalf("create alerts: %v", err)
	}

	now := time.Now().UTC()
	insert := func(errorCode string) {
		if err := db.Exec(
			`INSERT INTO alerts (node_id, node_name, error_code, message, severity, status, triggered_at, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			0, "platform", errorCode, "msg", "warning", "open", now, now, now,
		).Error; err != nil {
			t.Fatalf("insert %q: %v", errorCode, err)
		}
	}
	insert("XR-SLO-7")   // well-formed, expect slo_id=7
	insert("XR-SLO-abc") // malformed, expect slo_id=NULL
	insert("XR-NODE-3")  // non-SLO, expect slo_id=NULL

	// Read and execute the 000045 up migration. The path is computed
	// relative to this test file so it works regardless of where `go test`
	// is invoked from.
	wd, _ := os.Getwd()
	upPath := filepath.Join(wd, "migrations", "sqlite", "000045_alert_slo_id.up.sql")
	upSQL, err := os.ReadFile(upPath)
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	for _, stmt := range splitSQLStatements(string(upSQL)) {
		if err := db.Exec(stmt).Error; err != nil {
			t.Fatalf("exec stmt %q: %v", stmt, err)
		}
	}

	type row struct {
		ErrorCode string
		SLOID     sql.NullInt64
	}
	var rows []row
	if err := db.Raw(`SELECT error_code, slo_id FROM alerts ORDER BY id`).Scan(&rows).Error; err != nil {
		t.Fatalf("select: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("expected 3 rows, got %d", len(rows))
	}
	if !rows[0].SLOID.Valid || rows[0].SLOID.Int64 != 7 {
		t.Fatalf("XR-SLO-7: expected slo_id=7, got valid=%v val=%d", rows[0].SLOID.Valid, rows[0].SLOID.Int64)
	}
	if rows[1].SLOID.Valid {
		t.Fatalf("XR-SLO-abc: expected slo_id=NULL, got %d", rows[1].SLOID.Int64)
	}
	if rows[2].SLOID.Valid {
		t.Fatalf("XR-NODE-3: expected slo_id=NULL, got %d", rows[2].SLOID.Int64)
	}
}

// splitSQLStatements is a deliberately simple semicolon splitter sufficient
// for this migration's plain DDL/DML; it does NOT handle quoted semicolons.
// Any future migration with embedded ';' would need a real parser.
func splitSQLStatements(s string) []string {
	// Strip line comments first so the splitter doesn't trip on '--' lines.
	var clean strings.Builder
	for _, line := range strings.Split(s, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "--") {
			continue
		}
		clean.WriteString(line)
		clean.WriteByte('\n')
	}
	var out []string
	for _, raw := range strings.Split(clean.String(), ";") {
		stmt := strings.TrimSpace(raw)
		if stmt != "" {
			out = append(out, stmt)
		}
	}
	return out
}
