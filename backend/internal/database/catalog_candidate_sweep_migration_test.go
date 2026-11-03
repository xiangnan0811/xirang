package database

import (
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/model"
)

func TestCatalogCandidateSweepMigrationSQLite(t *testing.T) {
	testCatalogCandidateSweepMigration(t, newSQLiteMigrationFixture(t))
}

func TestCatalogCandidateSweepMigrationPostgres(t *testing.T) {
	testCatalogCandidateSweepMigration(t, newRequiredPostgresMigrationFixture(t))
}

func testCatalogCandidateSweepMigration(t *testing.T, fixture migrationFixture) {
	t.Helper()
	t.Run("upgrade_preserves_catalog", func(t *testing.T) {
		migrator, db := fixture.openAt(t, uint(catalogCandidateSweepSchemaVersion-1))
		seedCatalogCandidateSweepHistory(t, fixture, db)
		before := catalogCandidateSweepHistory(t, db)
		if err := migrator.Steps(1); err != nil {
			t.Fatalf("upgrade catalog sweep: %v", err)
		}
		assertMigrationVersion(t, migrator, uint(catalogCandidateSweepSchemaVersion))
		assertCatalogCandidateSweepHistory(t, db, before)
		assertCatalogCandidateSweepSeed(t, db)
		index, err := migrationIndexContractOf(db, fixture.engine, "recovery_points", catalogCandidateSweepIndex)
		if err != nil || !migrationIndexUsable(index) || index.unique || strings.TrimSpace(index.predicate) != "" ||
			!sameMigrationIndexColumns(index.columns, []string{"created_at", "id"}) {
			t.Fatalf("unexpected catalog candidate index: %+v, %v", index, err)
		}
		if err := validateMinimumRecoverySchema(db, fixture.engine, catalogCandidateSweepSchemaVersion); err != nil {
			t.Fatalf("verify new schema: %v", err)
		}
		columns := gormColumnNames(t, model.CatalogCandidateSweep{})
		if !reflect.DeepEqual(columns, []string{"id", "revision", "after_created_at", "after_point_id", "high_water_created_at", "high_water_point_id"}) {
			t.Fatalf("model columns: %v", columns)
		}
		id := strings.Repeat("a", 32)
		now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
		encoded, err := json.Marshal(model.CatalogCandidateSweep{ID: 1, Revision: 2, AfterCreatedAt: &now, AfterPointID: &id, HighWaterCreatedAt: &now, HighWaterPointID: &id})
		if err != nil || string(encoded) != "{}" {
			t.Fatalf("private sweep state exposed in JSON: %s, %v", encoded, err)
		}
	})

	t.Run("singleton_and_boundary_constraints", func(t *testing.T) {
		_, db := fixture.openAt(t, uint(catalogCandidateSweepSchemaVersion))
		assertCatalogCandidateSweepSeed(t, db)
		fixture.expectExecRejected(t, db, `INSERT INTO catalog_candidate_sweeps (id, revision) VALUES (1, 1)`)
		for _, id := range []int{0, 2, -1} {
			fixture.expectExecRejected(t, db, `UPDATE catalog_candidate_sweeps SET id = ?`, id)
		}
		for _, revision := range []any{nil, 0, -1, "9223372036854775808"} {
			fixture.expectExecRejected(t, db, `UPDATE catalog_candidate_sweeps SET revision = ?`, revision)
		}
		if fixture.engine == "sqlite" {
			fixture.expectExecRejected(t, db, `UPDATE catalog_candidate_sweeps SET revision = ?`, 1.5)
			fixture.expectExecRejected(t, db, `UPDATE catalog_candidate_sweeps SET revision = 9223372036854775807 + 1`)
		}
		fixture.mustExec(t, db, `UPDATE catalog_candidate_sweeps SET revision = ?`, int64(math.MaxInt64))

		now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
		valid := strings.Repeat("a", 32)
		laterID := strings.Repeat("b", 32)
		update := `UPDATE catalog_candidate_sweeps SET after_created_at = ?, after_point_id = ?, high_water_created_at = ?, high_water_point_id = ?`
		values := []any{now, valid, now.Add(time.Hour), laterID}
		for mask := 1; mask < 15; mask++ {
			args := make([]any, len(values))
			for index := range values {
				if mask&(1<<index) != 0 {
					args[index] = values[index]
				}
			}
			if _, err := db.Exec(fixture.bind(update), args...); err == nil {
				t.Fatalf("partial NULL boundary mask %04b unexpectedly accepted", mask)
			}
		}
		for _, bad := range []string{
			strings.Repeat("a", 31), strings.Repeat("a", 33), strings.Repeat("A", 32),
			strings.Repeat("a", 31) + "g", strings.Repeat("a", 32) + "\x00suffix",
		} {
			fixture.expectExecRejected(t, db, update, now, bad, now.Add(time.Hour), laterID)
			fixture.expectExecRejected(t, db, update, now, valid, now.Add(time.Hour), bad)
		}
		if fixture.engine == "sqlite" {
			fixture.expectExecRejected(t, db, update, now, []byte(valid), now.Add(time.Hour), laterID)
			fixture.expectExecRejected(t, db, update, now, valid, now.Add(time.Hour), []byte(laterID))
		}
		fixture.expectExecRejected(t, db, update, now.Add(time.Hour), valid, now, laterID)
		fixture.expectExecRejected(t, db, update, now, laterID, now, valid)
		fixture.mustExec(t, db, update, now, valid, now, valid)
		if err := validateMinimumRecoverySchema(db, fixture.engine, catalogCandidateSweepSchemaVersion); err != nil {
			t.Fatalf("used equal boundary and maximum revision rejected: %v", err)
		}
		fixture.mustExec(t, db, update, nil, nil, nil, nil)
		if err := validateCatalogCandidateSweepSchema(db, fixture.engine); err != nil {
			t.Fatalf("used idle sweep rejected: %v", err)
		}
	})

	t.Run("downgrade_and_reupgrade", func(t *testing.T) {
		migrator, db := fixture.openAt(t, uint(catalogCandidateSweepSchemaVersion-1))
		seedCatalogCandidateSweepHistory(t, fixture, db)
		before := catalogCandidateSweepHistory(t, db)
		if err := migrator.Steps(1); err != nil {
			t.Fatalf("upgrade catalog sweep: %v", err)
		}
		now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
		fixture.mustExec(t, db, `UPDATE catalog_candidate_sweeps SET revision = ?, after_created_at = ?, after_point_id = ?, high_water_created_at = ?, high_water_point_id = ?`,
			7, now, strings.Repeat("a", 32), now.Add(time.Hour), strings.Repeat("b", 32))
		if err := validateMinimumRecoverySchema(db, fixture.engine, catalogCandidateSweepSchemaVersion); err != nil {
			t.Fatalf("verify used sweep before rollback: %v", err)
		}
		if err := migrator.Steps(-1); err != nil {
			t.Fatalf("downgrade used catalog sweep: %v", err)
		}
		assertMigrationVersion(t, migrator, uint(catalogCandidateSweepSchemaVersion-1))
		for _, relation := range []struct{ name, kind string }{{catalogCandidateSweepTable, "table"}, {catalogCandidateSweepIndex, "index"}} {
			if exists, err := migrationRelationExists(db, fixture.engine, relation.name, relation.kind); err != nil || exists {
				t.Fatalf("rollback retained %s: exists=%v error=%v", relation.name, exists, err)
			}
		}
		assertCatalogCandidateSweepHistory(t, db, before)
		if err := migrator.Steps(1); err != nil {
			t.Fatalf("reapply catalog sweep: %v", err)
		}
		assertMigrationVersion(t, migrator, uint(catalogCandidateSweepSchemaVersion))
		assertCatalogCandidateSweepSeed(t, db)
		assertCatalogCandidateSweepHistory(t, db, before)
		if err := validateMinimumRecoverySchema(db, fixture.engine, catalogCandidateSweepSchemaVersion); err != nil {
			t.Fatalf("verify reapplied schema: %v", err)
		}
	})

	t.Run("startup_schema_drift", func(t *testing.T) {
		testCatalogCandidateSweepSchemaDrift(t, fixture)
	})
}

func seedCatalogCandidateSweepHistory(t *testing.T, fixture migrationFixture, db *sql.DB) {
	t.Helper()
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	pointID, catalogID, entryID := fixture.insertSearchMigrationCatalog(t, db, "a", now)
	fixture.mustExec(t, db, `INSERT INTO catalog_generations
		(id, recovery_point_id, generation, state, is_active, source_fingerprint,
		 expected_entry_count, written_entry_count, expected_digest, written_digest,
		 error_code, correlation_id, started_at, finished_at, created_at, updated_at)
		VALUES (?, ?, 2, 'failed', ?, 'later-source', 1, 0, '', '', 'catalog_build_failed', '', ?, ?, ?, ?)`,
		strings.Repeat("b", 32), pointID, false, now, now, now, now)
	searchID := strings.Repeat("c", 32)
	fixture.insertSearchMigrationGeneration(t, db, searchID, pointID, catalogID, 1, now)
	fixture.insertSearchMigrationDocument(t, db, searchID, pointID, catalogID, entryID, strings.Repeat("d", 64), now)
}

func catalogCandidateSweepHistory(t *testing.T, db *sql.DB) map[string][][]any {
	t.Helper()
	snapshots := make(map[string][][]any)
	for _, table := range []struct{ name, order string }{
		{"backup_repositories", "id"},
		{"recovery_points", "id"},
		{"catalog_generations", "id"},
		{"catalog_entries", "generation_id, entry_id"},
		{"backup_asset_search_generations", "id"},
		{"backup_asset_search_documents", "search_generation_id, document_id"},
	} {
		rows := accountRecoveryMigrationSnapshot(t, db, `SELECT * FROM `+table.name+` ORDER BY `+table.order)
		if len(rows) == 0 {
			t.Fatalf("missing pre-existing %s evidence", table.name)
		}
		snapshots[table.name] = rows
	}
	return snapshots
}

func assertCatalogCandidateSweepHistory(t *testing.T, db *sql.DB, before map[string][][]any) {
	t.Helper()
	if after := catalogCandidateSweepHistory(t, db); !reflect.DeepEqual(before, after) {
		t.Fatalf("migration changed catalog history or references\nbefore: %#v\nafter: %#v", before, after)
	}
}

func assertCatalogCandidateSweepSeed(t *testing.T, db *sql.DB) {
	t.Helper()
	var count, idle int
	err := db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(CASE WHEN id = 1 AND revision = 1 AND
		after_created_at IS NULL AND after_point_id IS NULL AND high_water_created_at IS NULL AND high_water_point_id IS NULL
		THEN 1 ELSE 0 END), 0) FROM catalog_candidate_sweeps`).Scan(&count, &idle)
	if err != nil || count != 1 || idle != 1 {
		t.Fatalf("unexpected catalog sweep seed count=%d idle=%d error=%v", count, idle, err)
	}
}

type catalogCandidateSweepDriftCase struct {
	name   string
	reason string
}

func testCatalogCandidateSweepSchemaDrift(t *testing.T, fixture migrationFixture) {
	t.Helper()
	cases := []catalogCandidateSweepDriftCase{
		{"missing_table", "missing_catalog_candidate_sweep_table"},
		{"missing_seed", "invalid_catalog_candidate_sweep_state"},
		{"column_default", "invalid_catalog_candidate_sweep_columns"},
		{"column_nullability", "invalid_catalog_candidate_sweep_columns"},
		{"column_type", "invalid_catalog_candidate_sweep_columns"},
		{"extra_column", "invalid_catalog_candidate_sweep_columns"},
		{"wrong_primary_key", "invalid_catalog_candidate_sweep_primary_key"},
		{"extra_foreign_key", "invalid_catalog_candidate_sweep_foreign_keys"},
		{"missing_check", "invalid_catalog_candidate_sweep_checks"},
		{"extra_check", "invalid_catalog_candidate_sweep_checks"},
		{"loose_check", "invalid_catalog_candidate_sweep_checks"},
		{"regrouped_check", "invalid_catalog_candidate_sweep_checks"},
		{"uppercase_hex_check", "invalid_catalog_candidate_sweep_checks"},
		{"missing_index", "invalid_catalog_candidate_sweep_index"},
		{"wrong_index_order", "invalid_catalog_candidate_sweep_index"},
		{"unique_index", "invalid_catalog_candidate_sweep_index"},
		{"partial_index", "invalid_catalog_candidate_sweep_index"},
		{"wrong_index_table", "invalid_catalog_candidate_sweep_index"},
	}
	if fixture.engine == "sqlite" {
		cases = append(cases,
			catalogCandidateSweepDriftCase{"invalid_state", "invalid_catalog_candidate_sweep_state"},
			catalogCandidateSweepDriftCase{"invalid_state_partial", "invalid_catalog_candidate_sweep_state"},
			catalogCandidateSweepDriftCase{"invalid_state_reversed", "invalid_catalog_candidate_sweep_state"},
			catalogCandidateSweepDriftCase{"invalid_state_noncanonical", "invalid_catalog_candidate_sweep_state"},
		)
	} else {
		cases = append(cases,
			catalogCandidateSweepDriftCase{"invalid_state", "invalid_catalog_candidate_sweep_checks"},
			catalogCandidateSweepDriftCase{"not_valid_check", "invalid_catalog_candidate_sweep_checks"},
			catalogCandidateSweepDriftCase{"included_index_column", "invalid_catalog_candidate_sweep_index"},
			catalogCandidateSweepDriftCase{"narrowed_revision_cast", "invalid_catalog_candidate_sweep_checks"},
			catalogCandidateSweepDriftCase{"numeric_boundary_cast", "invalid_catalog_candidate_sweep_checks"},
		)
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			migrator, db := fixture.openAt(t, latestMigrationVersion)
			applyCatalogCandidateSweepDrift(t, fixture, db, tc.name)
			if strings.HasPrefix(tc.name, "invalid_state") {
				if err := validateCatalogCandidateSweepState(db, catalogCandidateSweepChecks(fixture.engine)); err == nil || err.Error() != "invalid_catalog_candidate_sweep_state" {
					t.Fatalf("state validation accepted %s: %v", tc.name, err)
				}
			}
			before := catalogCandidateSweepDriftSnapshot(t, fixture, db)
			gormDB := fixture.recoveryWorkerGorm(t, db)
			for _, verify := range []struct {
				name string
				run  func() error
			}{
				{"offline verifier", func() error { return VerifyCurrentSchema(gormDB, fixture.engine) }},
				{"startup", func() error { return RunMigrations(gormDB, fixture.engine) }},
			} {
				err := verify.run()
				if !errors.Is(err, ErrMigrationSchemaDrift) || !strings.Contains(err.Error(), "reason="+tc.reason) {
					t.Fatalf("%s accepted %s drift or returned wrong reason: %v", verify.name, tc.name, err)
				}
				if after := catalogCandidateSweepDriftSnapshot(t, fixture, db); !reflect.DeepEqual(before, after) {
					t.Fatalf("%s repaired or changed %s drift\nbefore: %#v\nafter: %#v", verify.name, tc.name, before, after)
				}
				assertMigrationVersion(t, migrator, latestMigrationVersion)
			}
		})
	}
}

func applyCatalogCandidateSweepDrift(t *testing.T, fixture migrationFixture, db *sql.DB, name string) {
	t.Helper()
	sqlite := fixture.engine == "sqlite"
	replaceSQLite := func(old, replacement string) {
		rebuildCatalogCandidateSweepSQLite(t, fixture, db, func(definition string) string {
			if strings.Count(definition, old) != 1 {
				t.Fatalf("cannot find unique sweep schema fragment %q", old)
			}
			return strings.Replace(definition, old, replacement, 1)
		})
	}
	switch name {
	case "missing_table":
		fixture.mustExec(t, db, `DROP TABLE catalog_candidate_sweeps`)
	case "missing_seed":
		fixture.mustExec(t, db, `DELETE FROM catalog_candidate_sweeps`)
	case "column_default":
		if sqlite {
			replaceSQLite("revision INTEGER NOT NULL", "revision INTEGER NOT NULL DEFAULT 1")
		} else {
			fixture.mustExec(t, db, `ALTER TABLE catalog_candidate_sweeps ALTER COLUMN revision SET DEFAULT 1`)
		}
	case "column_nullability":
		if sqlite {
			replaceSQLite("revision INTEGER NOT NULL", "revision INTEGER")
		} else {
			fixture.mustExec(t, db, `ALTER TABLE catalog_candidate_sweeps ALTER COLUMN revision DROP NOT NULL`)
		}
	case "column_type":
		if sqlite {
			replaceSQLite("revision INTEGER NOT NULL", "revision TEXT NOT NULL")
		} else {
			fixture.mustExec(t, db, `ALTER TABLE catalog_candidate_sweeps ALTER COLUMN revision TYPE INTEGER`)
		}
	case "extra_column":
		fixture.mustExec(t, db, `ALTER TABLE catalog_candidate_sweeps ADD COLUMN unexpected TEXT`)
	case "wrong_primary_key":
		if sqlite {
			rebuildCatalogCandidateSweepSQLite(t, fixture, db, func(definition string) string {
				definition = strings.Replace(definition, "id INTEGER NOT NULL PRIMARY KEY", "id INTEGER NOT NULL", 1)
				return catalogCandidateSweepAppendConstraint(t, definition, "PRIMARY KEY (revision)")
			})
		} else {
			fixture.mustExec(t, db, `ALTER TABLE catalog_candidate_sweeps DROP CONSTRAINT catalog_candidate_sweeps_pkey`)
			fixture.mustExec(t, db, `ALTER TABLE catalog_candidate_sweeps ADD PRIMARY KEY (revision)`)
		}
	case "extra_foreign_key":
		if sqlite {
			rebuildCatalogCandidateSweepSQLite(t, fixture, db, func(definition string) string {
				return catalogCandidateSweepAppendConstraint(t, definition, "FOREIGN KEY (after_point_id) REFERENCES recovery_points(id)")
			})
		} else {
			fixture.mustExec(t, db, `ALTER TABLE catalog_candidate_sweeps ADD FOREIGN KEY (after_point_id) REFERENCES recovery_points(id)`)
		}
	case "missing_check":
		if sqlite {
			replaceSQLite("    CONSTRAINT catalog_candidate_sweep_id_check CHECK (id = 1),\n", "")
		} else {
			fixture.mustExec(t, db, `ALTER TABLE catalog_candidate_sweeps DROP CONSTRAINT catalog_candidate_sweep_id_check`)
		}
	case "extra_check":
		if sqlite {
			rebuildCatalogCandidateSweepSQLite(t, fixture, db, func(definition string) string {
				return catalogCandidateSweepAppendConstraint(t, definition, "CONSTRAINT unexpected_check CHECK (revision >= 1)")
			})
		} else {
			fixture.mustExec(t, db, `ALTER TABLE catalog_candidate_sweeps ADD CONSTRAINT unexpected_check CHECK (revision >= 1)`)
		}
	case "loose_check":
		if sqlite {
			replaceSQLite("CHECK (id = 1)", "CHECK (id = 1 OR id = 2)")
		} else {
			replaceCatalogCandidateSweepPostgresCheck(t, fixture, db, "catalog_candidate_sweep_id_check", "id = 1 OR id = 2", false)
		}
	case "regrouped_check":
		// The ordered tokens match the intended predicate, but this grouping
		// weakens its boundary NULL guards. The seeded idle row remains valid.
		expression := `after_created_at IS NULL AND after_point_id IS NULL AND high_water_created_at IS NULL AND high_water_point_id IS NULL
			OR after_created_at IS NOT NULL AND after_point_id IS NOT NULL AND high_water_created_at IS NOT NULL AND high_water_point_id IS NOT NULL
			AND after_created_at < high_water_created_at OR after_created_at = high_water_created_at AND after_point_id <= high_water_point_id`
		if sqlite {
			rebuildCatalogCandidateSweepSQLite(t, fixture, db, func(definition string) string {
				return replaceCatalogCandidateSweepSQLiteCheck(t, definition, "catalog_candidate_sweep_boundary_check", expression)
			})
		} else {
			replaceCatalogCandidateSweepPostgresCheck(t, fixture, db, "catalog_candidate_sweep_boundary_check", expression, false)
		}
	case "uppercase_hex_check":
		if sqlite {
			replaceSQLite("after_point_id NOT GLOB '*[^0-9a-f]*'", "after_point_id NOT GLOB '*[^0-9A-F]*'")
		} else {
			replaceCatalogCandidateSweepPostgresCheck(t, fixture, db, "catalog_candidate_sweep_after_id_check", "after_point_id IS NULL OR after_point_id ~ '^[0-9A-F]{32}$'", false)
		}
	case "missing_index", "wrong_index_order", "unique_index", "partial_index", "wrong_index_table", "included_index_column":
		fixture.mustExec(t, db, `DROP INDEX idx_recovery_points_created_at_id`)
		switch name {
		case "wrong_index_order":
			fixture.mustExec(t, db, `CREATE INDEX idx_recovery_points_created_at_id ON recovery_points(id, created_at)`)
		case "unique_index":
			fixture.mustExec(t, db, `CREATE UNIQUE INDEX idx_recovery_points_created_at_id ON recovery_points(created_at, id)`)
		case "partial_index":
			fixture.mustExec(t, db, `CREATE INDEX idx_recovery_points_created_at_id ON recovery_points(created_at, id) WHERE id IS NOT NULL`)
		case "wrong_index_table":
			fixture.mustExec(t, db, `CREATE INDEX idx_recovery_points_created_at_id ON backup_repositories(created_at, id)`)
		case "included_index_column":
			fixture.mustExec(t, db, `CREATE INDEX idx_recovery_points_created_at_id ON recovery_points(created_at, id) INCLUDE (state)`)
		}
	case "invalid_state", "invalid_state_partial", "invalid_state_reversed", "invalid_state_noncanonical":
		if sqlite {
			fixture.mustExec(t, db, `PRAGMA ignore_check_constraints = ON`)
			now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
			switch name {
			case "invalid_state":
				fixture.mustExec(t, db, `UPDATE catalog_candidate_sweeps SET revision = 0`)
			case "invalid_state_partial":
				fixture.mustExec(t, db, `UPDATE catalog_candidate_sweeps SET after_created_at = ?`, now)
			case "invalid_state_reversed":
				fixture.mustExec(t, db, `UPDATE catalog_candidate_sweeps SET after_created_at = ?, after_point_id = ?, high_water_created_at = ?, high_water_point_id = ?`,
					now.Add(time.Hour), strings.Repeat("a", 32), now, strings.Repeat("b", 32))
			case "invalid_state_noncanonical":
				fixture.mustExec(t, db, `UPDATE catalog_candidate_sweeps SET after_created_at = ?, after_point_id = ?, high_water_created_at = ?, high_water_point_id = ?`,
					now, strings.Repeat("a", 32)+"\x00suffix", now.Add(time.Hour), strings.Repeat("b", 32))
			}
			fixture.mustExec(t, db, `PRAGMA ignore_check_constraints = OFF`)
		} else {
			// PostgreSQL cannot normally store a row violating a validated
			// CHECK. Model a damaged restore using an unvalidated constraint;
			// startup must reject that definition before inspecting its state.
			fixture.mustExec(t, db, `ALTER TABLE catalog_candidate_sweeps DROP CONSTRAINT catalog_candidate_sweep_revision_check`)
			fixture.mustExec(t, db, `UPDATE catalog_candidate_sweeps SET revision = 0`)
			fixture.mustExec(t, db, `ALTER TABLE catalog_candidate_sweeps ADD CONSTRAINT catalog_candidate_sweep_revision_check CHECK (revision >= 1 AND revision <= 9223372036854775807) NOT VALID`)
		}
	case "not_valid_check":
		replaceCatalogCandidateSweepPostgresCheck(t, fixture, db, "catalog_candidate_sweep_id_check", "id = 1", true)
	case "narrowed_revision_cast":
		replaceCatalogCandidateSweepPostgresCheck(t, fixture, db, "catalog_candidate_sweep_revision_check", "revision::integer >= 1 AND revision::integer <= 9223372036854775807", false)
	case "numeric_boundary_cast":
		expression := `((after_created_at IS NULL AND after_point_id IS NULL AND high_water_created_at IS NULL AND high_water_point_id IS NULL) OR
			(after_created_at IS NOT NULL AND after_point_id IS NOT NULL AND high_water_created_at IS NOT NULL AND high_water_point_id IS NOT NULL AND
			(after_created_at < high_water_created_at OR (after_created_at = high_water_created_at AND after_point_id::integer <= high_water_point_id::integer))))`
		replaceCatalogCandidateSweepPostgresCheck(t, fixture, db, "catalog_candidate_sweep_boundary_check", expression, false)
	default:
		t.Fatalf("unknown sweep schema drift %q", name)
	}
}

func rebuildCatalogCandidateSweepSQLite(t *testing.T, fixture migrationFixture, db *sql.DB, change func(string) string) {
	t.Helper()
	var definition string
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'catalog_candidate_sweeps'`).Scan(&definition); err != nil {
		t.Fatalf("read sweep table definition: %v", err)
	}
	changed := change(definition)
	if changed == definition {
		t.Fatal("sweep schema drift did not change the definition")
	}
	fixture.mustExec(t, db, `DROP TABLE catalog_candidate_sweeps`)
	fixture.mustExec(t, db, changed)
	// Some drifted types convert the seed before the original CHECK runs.
	fixture.mustExec(t, db, `PRAGMA ignore_check_constraints = ON`)
	fixture.mustExec(t, db, `INSERT INTO catalog_candidate_sweeps (id, revision) VALUES (1, 1)`)
	fixture.mustExec(t, db, `PRAGMA ignore_check_constraints = OFF`)
}

func catalogCandidateSweepAppendConstraint(t *testing.T, definition, constraint string) string {
	t.Helper()
	end := strings.LastIndex(definition, ")")
	if end < 0 {
		t.Fatal("malformed sweep table definition")
	}
	return definition[:end] + ",\n" + constraint + "\n" + definition[end:]
}

func replaceCatalogCandidateSweepSQLiteCheck(t *testing.T, definition, constraint, expression string) string {
	t.Helper()
	prefix := "CONSTRAINT " + constraint + " CHECK ("
	start := strings.Index(definition, prefix)
	if start < 0 {
		t.Fatalf("missing SQLite constraint %s", constraint)
	}
	open := start + len(prefix) - 1
	end, ok := migrationMatchingParenthesis(definition, open)
	if !ok {
		t.Fatalf("malformed SQLite constraint %s", constraint)
	}
	return definition[:open+1] + expression + definition[end:]
}

func replaceCatalogCandidateSweepPostgresCheck(t *testing.T, fixture migrationFixture, db *sql.DB, constraint, expression string, notValid bool) {
	t.Helper()
	fixture.mustExec(t, db, `ALTER TABLE catalog_candidate_sweeps DROP CONSTRAINT `+constraint)
	query := `ALTER TABLE catalog_candidate_sweeps ADD CONSTRAINT ` + constraint + ` CHECK (` + expression + `)`
	if notValid {
		query += ` NOT VALID`
	}
	fixture.mustExec(t, db, query)
}

func catalogCandidateSweepDriftSnapshot(t *testing.T, fixture migrationFixture, db *sql.DB) map[string][][]any {
	t.Helper()
	snapshot := map[string][][]any{
		"version": accountRecoveryMigrationSnapshot(t, db, `SELECT * FROM schema_migrations`),
	}
	if fixture.engine == "sqlite" {
		snapshot["relations"] = accountRecoveryMigrationSnapshot(t, db, `SELECT type, name, tbl_name, sql FROM sqlite_master
			WHERE name IN ('catalog_candidate_sweeps', 'idx_recovery_points_created_at_id') ORDER BY type, name`)
	} else {
		snapshot["relations"] = accountRecoveryMigrationSnapshot(t, db, `SELECT relation.relname, relation.relkind::text
			FROM pg_catalog.pg_class relation JOIN pg_catalog.pg_namespace ns ON ns.oid = relation.relnamespace
			WHERE ns.nspname = current_schema() AND relation.relname IN ('catalog_candidate_sweeps', 'idx_recovery_points_created_at_id') ORDER BY relation.relname`)
		snapshot["columns"] = accountRecoveryMigrationSnapshot(t, db, `SELECT column_name, data_type, is_nullable, COALESCE(column_default, ''), COALESCE(character_maximum_length, 0)
			FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = 'catalog_candidate_sweeps' ORDER BY ordinal_position`)
		snapshot["constraints"] = accountRecoveryMigrationSnapshot(t, db, `SELECT c.conname, c.contype::text, c.convalidated, pg_catalog.pg_get_constraintdef(c.oid)
			FROM pg_catalog.pg_constraint c JOIN pg_catalog.pg_class r ON r.oid = c.conrelid
			JOIN pg_catalog.pg_namespace ns ON ns.oid = r.relnamespace
			WHERE ns.nspname = current_schema() AND r.relname = 'catalog_candidate_sweeps' ORDER BY c.conname`)
		snapshot["index"] = accountRecoveryMigrationSnapshot(t, db, `SELECT tablename, indexdef FROM pg_catalog.pg_indexes
			WHERE schemaname = current_schema() AND indexname = 'idx_recovery_points_created_at_id'`)
	}
	if exists, err := migrationRelationExists(db, fixture.engine, catalogCandidateSweepTable, "table"); err != nil {
		t.Fatalf("inspect sweep table during drift snapshot: %v", err)
	} else if exists {
		snapshot["state"] = accountRecoveryMigrationSnapshot(t, db, `SELECT * FROM catalog_candidate_sweeps`)
	}
	return snapshot
}
