package database

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
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

// TestRunMigrationsRejectsDirtyForEveryLegacyEnvValue locks the permanent
// fail-closed contract: absent, false, and formerly-enabled legacy
// configuration all reject before metadata or schema mutation.
func TestRunMigrationsRejectsDirtyForEveryLegacyEnvValue(t *testing.T) {
	testCases := []struct {
		name  string
		value *string
	}{
		{name: "unset"},
		{name: "empty", value: stringPointer("")},
		{name: "false", value: stringPointer("false")},
		{name: "true", value: stringPointer("true")},
		{name: "one", value: stringPointer("1")},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			if testCase.value == nil {
				oldValue, wasSet := os.LookupEnv("ALLOW_DIRTY_STARTUP")
				if err := os.Unsetenv("ALLOW_DIRTY_STARTUP"); err != nil {
					t.Fatalf("unset legacy dirty env: %v", err)
				}
				t.Cleanup(func() {
					if wasSet {
						_ = os.Setenv("ALLOW_DIRTY_STARTUP", oldValue)
						return
					}
					_ = os.Unsetenv("ALLOW_DIRTY_STARTUP")
				})
			} else {
				t.Setenv("ALLOW_DIRTY_STARTUP", *testCase.value)
			}
			sqlDB, gdb, _ := openDirtyTestDB(t, true, 50)
			beforeSchema := snapshotSQLiteSchema(t, sqlDB)

			err := RunMigrations(gdb, "sqlite")
			if !errors.Is(err, ErrMigrationDirty) {
				t.Fatalf("dirty startup legacy env case %s returned %v, want ErrMigrationDirty", testCase.name, err)
			}
			afterSchema := snapshotSQLiteSchema(t, sqlDB)
			if afterSchema != beforeSchema {
				t.Fatalf("dirty rejection mutated schema: before=%q after=%q", beforeSchema, afterSchema)
			}
			dirty, version, checkErr := checkMigrationDirty(sqlDB, "sqlite")
			if checkErr != nil {
				t.Fatalf("check dirty state after rejection: %v", checkErr)
			}
			if !dirty || version != 50 {
				t.Fatalf("dirty rejection mutated migration metadata: version=%d dirty=%v", version, dirty)
			}
		})
	}
}

func stringPointer(value string) *string {
	return &value
}

func snapshotSQLiteSchema(t *testing.T, db *sql.DB) string {
	t.Helper()
	rows, err := db.Query(`
		SELECT type, name, COALESCE(sql, '')
		FROM sqlite_master
		WHERE name NOT LIKE 'sqlite_%'
		ORDER BY type, name`)
	if err != nil {
		t.Fatalf("query sqlite schema: %v", err)
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			t.Errorf("close sqlite schema rows: %v", closeErr)
		}
	}()
	var snapshot strings.Builder
	for rows.Next() {
		var kind, name, definition string
		if err := rows.Scan(&kind, &name, &definition); err != nil {
			t.Fatalf("scan sqlite schema: %v", err)
		}
		fmt.Fprintf(&snapshot, "%s\x00%s\x00%s\n", kind, name, definition)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate sqlite schema: %v", err)
	}
	return snapshot.String()
}

// TestRunMigrationsRejectsCleanVersionSchemaDriftBeforeFixups proves that a
// falsely-clean post-69 version cannot be mutated or accepted when the minimum
// recovery schema is absent.
func TestRunMigrationsRejectsCleanVersionSchemaDriftBeforeFixups(t *testing.T) {
	_, gdb, _ := openDirtyTestDB(t, false, 71)
	sqlDB, err := gdb.DB()
	if err != nil {
		t.Fatalf("gorm DB: %v", err)
	}
	if _, err := sqlDB.Exec(`CREATE TABLE policies (id INTEGER PRIMARY KEY, bw_limit INTEGER NOT NULL)`); err != nil {
		t.Fatalf("create legacy policies fixture: %v", err)
	}
	if _, err := sqlDB.Exec(`INSERT INTO policies (id, bw_limit) VALUES (1, 23)`); err != nil {
		t.Fatalf("seed legacy policies fixture: %v", err)
	}

	var before string
	if err := sqlDB.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name='policies'`).Scan(&before); err != nil {
		t.Fatalf("snapshot schema before startup: %v", err)
	}
	err = RunMigrations(gdb, "sqlite")
	var after string
	if scanErr := sqlDB.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name='policies'`).Scan(&after); scanErr != nil {
		t.Fatalf("snapshot schema after startup: %v", scanErr)
	}
	if after != before {
		t.Fatalf("clean-version schema drift was mutated before rejection: before=%q after=%q", before, after)
	}
	if !errors.Is(err, ErrMigrationSchemaDrift) || !strings.Contains(strings.ToLower(err.Error()), "schema") || !strings.Contains(strings.ToLower(err.Error()), "drift") {
		t.Fatalf("clean version 71 without minimum migration-69 schema returned %v, want typed schema-drift rejection", err)
	}
	dirty, version, checkErr := checkMigrationDirty(sqlDB, "sqlite")
	if checkErr != nil {
		t.Fatalf("check migration metadata after rejection: %v", checkErr)
	}
	if dirty || version != 71 {
		t.Fatalf("schema-drift rejection mutated migration metadata: version=%d dirty=%v", version, dirty)
	}
	var bwLimit int
	if scanErr := sqlDB.QueryRow(`SELECT bw_limit FROM policies WHERE id = 1`).Scan(&bwLimit); scanErr != nil {
		t.Fatalf("read legacy policies data after rejection: %v", scanErr)
	}
	if bwLimit != 23 {
		t.Fatalf("schema-drift rejection mutated policies data: got %d want 23", bwLimit)
	}
}
