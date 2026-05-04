package database

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/config"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// TestNowFuncReturnsUTC verifies GORM's auto-managed CreatedAt is written in
// UTC after Open() configures NowFunc. This is the linchpin of the UTC
// cutover — without it, new rows would still be written in Local time even
// after running migration 000050.
func TestNowFuncReturnsUTC(t *testing.T) {
	// Force the process TZ to a non-UTC zone so we'd notice if NowFunc
	// silently fell back to Local.
	t.Setenv("TZ", "Asia/Shanghai")

	tempDir := t.TempDir()
	cfg := config.Config{
		DBType:     "sqlite",
		SQLitePath: filepath.Join(tempDir, "nowfunc.db"),
	}
	db, err := Open(cfg)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		sqlDB, _ := db.DB()
		if sqlDB != nil {
			_ = sqlDB.Close()
		}
	})

	type widget struct {
		ID        uint `gorm:"primaryKey"`
		Name      string
		CreatedAt time.Time
	}
	if err := db.AutoMigrate(&widget{}); err != nil {
		t.Fatalf("AutoMigrate: %v", err)
	}

	w := widget{Name: "first"}
	if err := db.Create(&w).Error; err != nil {
		t.Fatalf("Create: %v", err)
	}

	var got widget
	if err := db.First(&got, w.ID).Error; err != nil {
		t.Fatalf("First: %v", err)
	}

	// Both the in-memory value (set by NowFunc before INSERT) and the value
	// scanned back (via _loc=UTC) must report Location()==UTC.
	if w.CreatedAt.Location().String() != "UTC" {
		t.Fatalf("in-memory CreatedAt location = %s, want UTC", w.CreatedAt.Location())
	}
	if got.CreatedAt.Location().String() != "UTC" {
		t.Fatalf("scanned CreatedAt location = %s, want UTC", got.CreatedAt.Location())
	}
	// And the round-trip must preserve the absolute instant (no silent shift).
	if !w.CreatedAt.Equal(got.CreatedAt) {
		t.Fatalf("round-trip drift: wrote %v, read %v", w.CreatedAt, got.CreatedAt)
	}
}

// TestUTCCutoverSQLEquivalence simulates the migration 000050 scenario:
// 1. Pretend the server was running in Local (Asia/Shanghai = +8h) and wrote
//    a timestamp value that represents a specific absolute instant.
// 2. Run the SQL fragment from 000050 to subtract 8 hours (the cutover).
// 3. Read it back through a UTC-loc connection.
// 4. Assert the absolute instant is preserved.
//
// This is the core correctness guarantee of the cutover: nothing about the
// real-world moment recorded changes, only the storage representation
// switches from Local-string to UTC-string.
func TestUTCCutoverSQLEquivalence(t *testing.T) {
	// We deliberately do NOT use Open() here because we want to control
	// _loc precisely across the simulated old/new boundary.
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "cutover.db")

	// Phase 1: open in Local mode (no _loc=UTC), write an instant via the
	// Local-time string the legacy GORM would have produced.
	shanghai, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Skipf("LoadLocation: %v (skipping; Local TZ unavailable)", err)
	}
	// Pick a deterministic instant; 2025-06-15 12:00:00 +0800 == 2025-06-15 04:00:00 UTC
	originalLocal := time.Date(2025, 6, 15, 12, 0, 0, 0, shanghai)
	originalUTC := originalLocal.UTC()

	// Open as a "legacy" connection: no _loc=UTC. The driver will write
	// the time literal as-is in whatever location the time.Time has.
	legacyDSN := fmt.Sprintf("file:%s?_journal_mode=WAL&_busy_timeout=5000", dbPath)
	legacy, err := gorm.Open(sqlite.Open(legacyDSN), &gorm.Config{})
	if err != nil {
		t.Fatalf("open legacy: %v", err)
	}
	if err := legacy.Exec(`CREATE TABLE samples (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		event_at DATETIME NOT NULL
	)`).Error; err != nil {
		t.Fatalf("create samples: %v", err)
	}
	// Insert the literal Local-time string the legacy GORM (no NowFunc, no
	// loc=UTC) would have produced for this instant: "2025-06-15 12:00:00".
	legacyLiteral := originalLocal.Format("2006-01-02 15:04:05")
	if err := legacy.Exec(`INSERT INTO samples (event_at) VALUES (?)`, legacyLiteral).Error; err != nil {
		t.Fatalf("insert legacy: %v", err)
	}
	if sqlDB, _ := legacy.DB(); sqlDB != nil {
		_ = sqlDB.Close()
	}

	// Phase 2: run the cutover SQL (-8h on event_at) using a no-loc
	// connection so the driver doesn't try to interpret/rewrite literals.
	cutDB, err := gorm.Open(sqlite.Open(legacyDSN), &gorm.Config{})
	if err != nil {
		t.Fatalf("open for cutover: %v", err)
	}
	if err := cutDB.Exec(
		`UPDATE samples SET event_at = datetime(event_at, '-8 hours') WHERE event_at IS NOT NULL`,
	).Error; err != nil {
		t.Fatalf("cutover update: %v", err)
	}
	if sqlDB, _ := cutDB.DB(); sqlDB != nil {
		_ = sqlDB.Close()
	}

	// Phase 3: open in new UTC mode (_loc=UTC), read it back as time.Time.
	utcDSN := fmt.Sprintf("file:%s?_journal_mode=WAL&_busy_timeout=5000&_loc=UTC", dbPath)
	utcDB, err := gorm.Open(sqlite.Open(utcDSN), &gorm.Config{})
	if err != nil {
		t.Fatalf("open utc: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, _ := utcDB.DB(); sqlDB != nil {
			_ = sqlDB.Close()
		}
	})

	type sample struct {
		ID      uint
		EventAt time.Time
	}
	var got sample
	if err := utcDB.Raw(`SELECT id, event_at FROM samples ORDER BY id LIMIT 1`).Scan(&got).Error; err != nil {
		t.Fatalf("read back: %v", err)
	}

	// Phase 4: the read-back time.Time must equal the original UTC instant.
	if !got.EventAt.Equal(originalUTC) {
		t.Fatalf("cutover drift: original UTC = %v, after cutover read = %v (delta=%v)",
			originalUTC, got.EventAt, got.EventAt.Sub(originalUTC))
	}
	// Sanity: location should be UTC (driver now interprets stored string as UTC).
	if got.EventAt.Location().String() != "UTC" {
		t.Fatalf("read-back location = %s, want UTC", got.EventAt.Location())
	}
}

// TestSQLiteLocUTCRoundTrip is a tighter check on the DSN pragma alone: a
// time.Time in any location, written and read through a _loc=UTC connection,
// must come back at the same absolute instant in Location()==UTC.
func TestSQLiteLocUTCRoundTrip(t *testing.T) {
	tempDir := t.TempDir()
	cfg := config.Config{
		DBType:     "sqlite",
		SQLitePath: filepath.Join(tempDir, "loc.db"),
	}
	db, err := Open(cfg)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, _ := db.DB(); sqlDB != nil {
			_ = sqlDB.Close()
		}
	})

	type ev struct {
		ID      uint `gorm:"primaryKey"`
		EventAt time.Time
	}
	if err := db.AutoMigrate(&ev{}); err != nil {
		t.Fatalf("AutoMigrate: %v", err)
	}

	shanghai, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Skipf("LoadLocation: %v", err)
	}

	cases := []time.Time{
		time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2025, 6, 15, 12, 0, 0, 0, shanghai),       // +8 zone
		time.Date(2025, 12, 31, 23, 59, 59, 0, time.FixedZone("+09:30", 9*3600+30*60)),
	}
	for i, want := range cases {
		want := want
		t.Run(fmt.Sprintf("case_%d_%s", i, want.Location()), func(t *testing.T) {
			row := ev{EventAt: want}
			if err := db.Create(&row).Error; err != nil {
				t.Fatalf("create: %v", err)
			}
			var got ev
			if err := db.First(&got, row.ID).Error; err != nil {
				t.Fatalf("read: %v", err)
			}
			if !got.EventAt.Equal(want) {
				t.Fatalf("instant changed: wrote %v, read %v", want, got.EventAt)
			}
			if got.EventAt.Location().String() != "UTC" {
				t.Fatalf("location = %s, want UTC", got.EventAt.Location())
			}
		})
	}
}

// TestBuildPostgresDSN covers the three input shapes the helper must handle:
// URL form with existing query, URL form without query, and the keyword/value
// form. Plus a passthrough case when the caller already specified a timezone.
func TestBuildPostgresDSN(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "url with existing query",
			in:   "postgres://user:pass@host:5432/db?sslmode=disable",
			want: "postgres://user:pass@host:5432/db?sslmode=disable&timezone=UTC",
		},
		{
			name: "url without query",
			in:   "postgres://user:pass@host:5432/db",
			want: "postgres://user:pass@host:5432/db?timezone=UTC",
		},
		{
			name: "postgresql url scheme",
			in:   "postgresql://user@host/db",
			want: "postgresql://user@host/db?timezone=UTC",
		},
		{
			name: "keyword/value form",
			in:   "host=localhost port=5432 user=u dbname=d sslmode=disable",
			want: "host=localhost port=5432 user=u dbname=d sslmode=disable timezone=UTC",
		},
		{
			name: "already has timezone",
			in:   "postgres://user@host/db?timezone=Asia/Shanghai",
			want: "postgres://user@host/db?timezone=Asia/Shanghai",
		},
		{
			name: "already has TimeZone (PG canonical)",
			in:   "host=localhost TimeZone=UTC",
			want: "host=localhost TimeZone=UTC",
		},
		{
			name: "empty",
			in:   "",
			want: "",
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := buildPostgresDSN(tc.in)
			if got != tc.want {
				t.Fatalf("buildPostgresDSN(%q):\n  got:  %q\n  want: %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestMigration050_DryRun_AppliesAndRollsBack runs the actual 000050 SQL on
// a synthetic SQLite DB with seed rows, then runs the down migration, and
// asserts everything round-trips. This protects against typos in either
// direction (e.g., an UPDATE missing a column or a sign-flip mistake).
func TestMigration050_DryRun_AppliesAndRollsBack(t *testing.T) {
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared&_loc=UTC",
		strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, _ := db.DB(); sqlDB != nil {
			_ = sqlDB.Close()
		}
	})

	// Build a tiny table that mirrors one of the columns 000050 touches.
	// We use users.created_at as the canary; if 000050 forgets users it will
	// be obvious here. (Full coverage of every table would balloon this test;
	// the migration SQL is mechanical enough that one canary plus visual
	// review of the SQL file is sufficient.)
	if err := db.Exec(`CREATE TABLE users (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		username TEXT NOT NULL,
		password_hash TEXT NOT NULL DEFAULT '',
		role TEXT NOT NULL DEFAULT 'admin',
		created_at DATETIME,
		updated_at DATETIME
	)`).Error; err != nil {
		t.Fatalf("create users: %v", err)
	}
	// Seed with a Local-time literal "as if" written by old GORM.
	if err := db.Exec(
		`INSERT INTO users (username, created_at, updated_at) VALUES (?, ?, ?)`,
		"seed", "2025-06-15 12:00:00", "2025-06-15 12:00:00",
	).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Apply only the users portion of the up SQL (we read the whole file but
	// our schema only has `users`, so non-users statements would error). We
	// extract just the lines targeting `users`.
	wd, _ := os.Getwd()
	upPath := filepath.Join(wd, "migrations", "sqlite", "000050_utc_cutover.up.sql")
	upBytes, err := os.ReadFile(upPath)
	if err != nil {
		t.Fatalf("read up sql: %v", err)
	}
	for _, stmt := range splitSQLStatements(string(upBytes)) {
		if !strings.Contains(stmt, " users ") {
			continue
		}
		if err := db.Exec(stmt).Error; err != nil {
			t.Fatalf("up exec %q: %v", stmt, err)
		}
	}

	var afterUp string
	if err := db.Raw(`SELECT created_at FROM users WHERE id = 1`).Scan(&afterUp).Error; err != nil {
		t.Fatalf("read after up: %v", err)
	}
	// Normalize the read-back string: SQLite may return the value either as
	// stored ("YYYY-MM-DD HH:MM:SS") or normalized to ISO-8601
	// ("YYYY-MM-DDTHH:MM:SSZ") depending on driver/scanner. We strip the
	// trailing 'Z' and replace 'T' with space for a stable comparison.
	afterUpNorm := strings.TrimSuffix(strings.Replace(afterUp, "T", " ", 1), "Z")
	if afterUpNorm != "2025-06-15 04:00:00" {
		t.Fatalf("after up: got %q (normalized %q), want 2025-06-15 04:00:00 (12:00 - 8h)", afterUp, afterUpNorm)
	}

	// Now apply the down rollback.
	downPath := filepath.Join(wd, "migrations", "sqlite", "000050_utc_cutover.down.sql")
	downBytes, err := os.ReadFile(downPath)
	if err != nil {
		t.Fatalf("read down sql: %v", err)
	}
	for _, stmt := range splitSQLStatements(string(downBytes)) {
		if !strings.Contains(stmt, " users ") {
			continue
		}
		if err := db.Exec(stmt).Error; err != nil {
			t.Fatalf("down exec %q: %v", stmt, err)
		}
	}

	var afterDown string
	if err := db.Raw(`SELECT created_at FROM users WHERE id = 1`).Scan(&afterDown).Error; err != nil {
		t.Fatalf("read after down: %v", err)
	}
	afterDownNorm := strings.TrimSuffix(strings.Replace(afterDown, "T", " ", 1), "Z")
	if afterDownNorm != "2025-06-15 12:00:00" {
		t.Fatalf("after down: got %q (normalized %q), want 2025-06-15 12:00:00 (round-trip)", afterDown, afterDownNorm)
	}
}
