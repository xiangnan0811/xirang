package database

import (
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	_ "github.com/mattn/go-sqlite3"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// openDirtyTestDB opens a fresh SQLite file with a manually-seeded
// schema_migrations row so we can drive checkMigrationDirty / RunMigrations
// without first replaying real migrations.
func openDirtyTestDB(t *testing.T, dirty bool, version int64) (*sql.DB, *gorm.DB, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, fmt.Sprintf("dirty-%s.db", strings.ReplaceAll(t.Name(), "/", "_")))
	dsn := buildSQLiteDSN(path)

	gdb, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("gorm open: %v", err)
	}
	sqlDB, err := gdb.DB()
	if err != nil {
		t.Fatalf("gorm.DB: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })

	// Build the same schema_migrations table golang-migrate creates so our
	// checkMigrationDirty path observes a realistic shape.
	if _, err := sqlDB.Exec(`CREATE TABLE schema_migrations (
		version BIGINT NOT NULL PRIMARY KEY,
		dirty BOOLEAN NOT NULL
	)`); err != nil {
		t.Fatalf("create schema_migrations: %v", err)
	}
	dirtyInt := 0
	if dirty {
		dirtyInt = 1
	}
	if _, err := sqlDB.Exec(`INSERT INTO schema_migrations (version, dirty) VALUES (?, ?)`, version, dirtyInt); err != nil {
		t.Fatalf("seed schema_migrations: %v", err)
	}
	return sqlDB, gdb, path
}

// TestCheckMigrationDirty_NoTable returns false when the table does not yet
// exist (fresh database scenario).
func TestCheckMigrationDirty_NoTable(t *testing.T) {
	dir := t.TempDir()
	dsn := buildSQLiteDSN(filepath.Join(dir, "fresh.db"))
	gdb, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	sqlDB, _ := gdb.DB()
	t.Cleanup(func() { _ = sqlDB.Close() })

	dirty, version, err := checkMigrationDirty(sqlDB, "sqlite")
	if err != nil {
		t.Fatalf("checkMigrationDirty: %v", err)
	}
	if dirty {
		t.Fatalf("fresh DB should not be dirty; got dirty=true")
	}
	if version != 0 {
		t.Fatalf("fresh DB should report version=0; got %d", version)
	}
}

// TestCheckMigrationDirty_CleanTable returns false when the table exists but
// dirty=0.
func TestCheckMigrationDirty_CleanTable(t *testing.T) {
	sqlDB, _, _ := openDirtyTestDB(t, false, 50)
	dirty, version, err := checkMigrationDirty(sqlDB, "sqlite")
	if err != nil {
		t.Fatalf("checkMigrationDirty: %v", err)
	}
	if dirty {
		t.Fatalf("clean migration row reported dirty=true")
	}
	if version != 50 {
		t.Fatalf("expected version=50, got %d", version)
	}
}

// TestCheckMigrationDirty_DirtyTable returns true when dirty=1.
func TestCheckMigrationDirty_DirtyTable(t *testing.T) {
	sqlDB, _, _ := openDirtyTestDB(t, true, 50)
	dirty, version, err := checkMigrationDirty(sqlDB, "sqlite")
	if err != nil {
		t.Fatalf("checkMigrationDirty: %v", err)
	}
	if !dirty {
		t.Fatalf("expected dirty=true")
	}
	if version != 50 {
		t.Fatalf("expected version=50, got %d", version)
	}
}

// TestRunMigrations_RejectsDirtyByDefault confirms RunMigrations short-circuits
// when schema_migrations.dirty=1 and ALLOW_DIRTY_STARTUP is unset. This is the
// production safety net that prevents continuing on a half-applied schema.
func TestRunMigrations_RejectsDirtyByDefault(t *testing.T) {
	t.Setenv("ALLOW_DIRTY_STARTUP", "")
	_, gdb, _ := openDirtyTestDB(t, true, 50)

	err := RunMigrations(gdb, "sqlite")
	if err == nil {
		t.Fatalf("expected error from dirty-state startup, got nil")
	}
	if !errors.Is(err, ErrMigrationDirty) {
		t.Fatalf("expected ErrMigrationDirty, got %v", err)
	}
}

// TestRunMigrations_AllowDirtyEscapeHatch confirms ALLOW_DIRTY_STARTUP=true
// bypasses the guard. We don't care that the actual migrate.Up() may itself
// fail (the embedded baseline migrations expect a virgin DB schema, not our
// hand-seeded schema_migrations row); we only care that the dirty-check did
// NOT short-circuit before reaching the migrator.
func TestRunMigrations_AllowDirtyEscapeHatch(t *testing.T) {
	t.Setenv("ALLOW_DIRTY_STARTUP", "true")
	_, gdb, _ := openDirtyTestDB(t, true, 50)

	err := RunMigrations(gdb, "sqlite")
	// We expect the migrator itself to fail (because our seeded version=50 is
	// inconsistent with the embedded baseline), but it must NOT be
	// ErrMigrationDirty — the escape hatch should have skipped the check.
	if err != nil && errors.Is(err, ErrMigrationDirty) {
		t.Fatalf("ALLOW_DIRTY_STARTUP=true should bypass dirty check; got ErrMigrationDirty: %v", err)
	}
}

// TestAllowDirtyStartup_ParsesEnvCorrectly covers the boolean parsing edge
// cases of the escape hatch.
func TestAllowDirtyStartup_ParsesEnvCorrectly(t *testing.T) {
	cases := []struct {
		env  string
		want bool
	}{
		{"", false},
		{"true", true},
		{"True", true},
		{"1", true},
		{"false", false},
		{"0", false},
		{"yes", false}, // strconv.ParseBool rejects yes; conservative default
		{"banana", false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(fmt.Sprintf("env=%q", tc.env), func(t *testing.T) {
			t.Setenv("ALLOW_DIRTY_STARTUP", tc.env)
			got := allowDirtyStartup()
			if got != tc.want {
				t.Fatalf("allowDirtyStartup() = %v, want %v", got, tc.want)
			}
		})
	}
}
