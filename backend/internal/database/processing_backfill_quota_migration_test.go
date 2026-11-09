package database

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	pgxmigrate "github.com/golang-migrate/migrate/v4/database/pgx/v5"
)

func TestProcessingBackfillQuotaMigrationSQLite(t *testing.T) {
	testProcessingBackfillQuotaMigration(t, newSQLiteMigrationFixture(t))
}

func TestProcessingBackfillQuotaMigrationPostgres(t *testing.T) {
	testProcessingBackfillQuotaMigration(t, newRequiredPostgresMigrationFixture(t))
}

func seedBackfillQuotaJob(t *testing.T, f migrationFixture, db *sql.DB) string {
	t.Helper()
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	point, generation, entry := f.insertSearchMigrationCatalog(t, db, "9", now)
	id := strings.Repeat("a", 32)
	f.insertProcessingMigrationJob(t, db, id, point, generation, entry, now)
	return id
}

func testProcessingBackfillQuotaMigration(t *testing.T, f migrationFixture) {
	t.Helper()
	t.Run("UpgradePreservesAndBackfills", func(t *testing.T) {
		m, db := f.openAt(t, 98)
		job := seedBackfillQuotaJob(t, f, db)
		f.mustExec(t, db, `UPDATE catalog_entries SET size = 0`)
		f.mustExec(t, db, `UPDATE recovery_points SET source_fingerprint = 'changed-later'`)
		if err := m.Steps(1); err != nil {
			t.Fatal(err)
		}
		assertMigrationVersion(t, m, 99)
		if err := VerifyCurrentSchema(f.recoveryWorkerGorm(t, db), f.engine); err != nil {
			t.Fatal(err)
		}
		var provider, fingerprint, state string
		var size int64
		var charged bool
		if err := db.QueryRow(f.bind(`SELECT provider_kind, estimated_input_bytes, backfill_charged, source_fingerprint, state FROM backup_asset_processing_jobs WHERE id = ?`), job).Scan(&provider, &size, &charged, &fingerprint, &state); err != nil {
			t.Fatal(err)
		}
		if provider != "rsync" || size != 0 || charged || fingerprint != "source-v1" || state != "queued" {
			t.Fatalf("unexpected migrated job: %q %d %v %q %q", provider, size, charged, fingerprint, state)
		}
	})
	t.Run("PreserveDerivedRows", func(t *testing.T) {
		m, db := f.openAt(t, 98)
		seedProcessingSharedDerivedBlobs(t, f, db)
		before := processingSharedDerivedBlobsRows(t, db)
		if err := m.Steps(1); err != nil {
			t.Fatal(err)
		}
		assertProcessingSharedDerivedBlobsRows(t, db, before)
		if err := m.Steps(-1); err != nil {
			t.Fatal(err)
		}
		assertProcessingSharedDerivedBlobsRows(t, db, before)
		assertMigrationVersion(t, m, 98)
	})
	t.Run("UpgradeFrom97", func(t *testing.T) {
		m, db := f.openAt(t, 97)
		seedBackfillQuotaJob(t, f, db)
		if err := RunMigrations(f.recoveryWorkerGorm(t, db), f.engine); err != nil {
			t.Fatal(err)
		}
		assertMigrationVersion(t, m, 99)
	})
	t.Run("UnusedDownAndReupgrade", func(t *testing.T) {
		m, db := f.openAt(t, 98)
		seedBackfillQuotaJob(t, f, db)
		if err := m.Steps(1); err != nil {
			t.Fatal(err)
		}
		if err := m.Steps(-1); err != nil {
			t.Fatal(err)
		}
		assertMigrationVersion(t, m, 98)
		if err := m.Steps(1); err != nil {
			t.Fatal(err)
		}
		assertMigrationVersion(t, m, 99)
		if err := VerifyCurrentSchema(f.recoveryWorkerGorm(t, db), f.engine); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("UsedDownRejected", func(t *testing.T) {
		run := func(t *testing.T, statement string) {
			m, db := f.openAt(t, 98)
			seedBackfillQuotaJob(t, f, db)
			if err := m.Steps(1); err != nil {
				t.Fatal(err)
			}
			f.mustExec(t, db, statement)
			f.expectExecRejected(t, db, `UPDATE schema_migrations SET version = ?`, 98)
			if err := m.Steps(-1); err == nil {
				t.Fatal("used quota allowed downgrade")
			}
			assertMigrationVersion(t, m, 99)
			if err := validateMinimumRecoverySchema(db, f.engine, 99); err != nil {
				t.Fatal(err)
			}
		}
		t.Run("JobCount", func(t *testing.T) { run(t, `UPDATE backup_asset_processing_backfill_ledger SET jobs_count = 1`) })
		t.Run("ByteCount", func(t *testing.T) { run(t, `UPDATE backup_asset_processing_backfill_ledger SET bytes_count = 1`) })
		t.Run("ZeroByteCharged", func(t *testing.T) { run(t, `UPDATE backup_asset_processing_jobs SET backfill_charged = TRUE`) })
	})
	t.Run("FutureDownTo99", func(t *testing.T) {
		m, db := f.openAt(t, 99)
		f.mustExec(t, db, `UPDATE backup_asset_processing_backfill_ledger SET jobs_count = 1`)
		f.mustExec(t, db, `UPDATE schema_migrations SET version = ?`, 100)
		f.mustExec(t, db, `UPDATE schema_migrations SET version = ?`, 99)
		assertMigrationVersion(t, m, 99)
	})
	t.Run("DirectDownRejected", func(t *testing.T) {
		m, db := f.openAt(t, 98)
		seedBackfillQuotaJob(t, f, db)
		if err := m.Steps(1); err != nil {
			t.Fatal(err)
		}
		f.mustExec(t, db, `UPDATE backup_asset_processing_jobs SET backfill_charged = TRUE`)
		down, err := backfillQuotaDown(f.engine)
		if err != nil {
			t.Fatal(err)
		}
		tx, err := db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		_, downErr := tx.Exec(down)
		if err := tx.Rollback(); err != nil {
			t.Fatal(err)
		}
		if downErr == nil {
			t.Fatal("direct down accepted used quota")
		}
		assertMigrationVersion(t, m, 99)
	})
	t.Run("SchemaDrift", func(t *testing.T) {
		run := func(t *testing.T, sqlite, postgres string) {
			m, db := f.openAt(t, 99)
			statement := sqlite
			if f.engine == "postgres" {
				statement = postgres
			}
			f.mustExec(t, db, statement)
			if err := VerifyCurrentSchema(f.recoveryWorkerGorm(t, db), f.engine); !errors.Is(err, ErrMigrationSchemaDrift) {
				t.Fatalf("expected schema drift, got %v", err)
			}
			assertMigrationVersion(t, m, 99)
		}
		t.Run("Seed", func(t *testing.T) {
			run(t, `DELETE FROM backup_asset_processing_backfill_ledger`, `DELETE FROM backup_asset_processing_backfill_ledger`)
		})
		t.Run("Column", func(t *testing.T) {
			run(t, `ALTER TABLE backup_asset_processing_jobs RENAME COLUMN estimated_input_bytes TO drifted_bytes`, `ALTER TABLE backup_asset_processing_jobs RENAME COLUMN estimated_input_bytes TO drifted_bytes`)
		})
		t.Run("Index", func(t *testing.T) {
			run(t, `DROP INDEX idx_backup_asset_processing_jobs_backfill_active`, `DROP INDEX idx_backup_asset_processing_jobs_backfill_active`)
		})
		t.Run("Guard", func(t *testing.T) {
			run(t, `DROP TRIGGER trg_processing_backfill_quota_downgrade_admission`, `ALTER TABLE schema_migrations DISABLE TRIGGER trg_processing_backfill_quota_downgrade_admission`)
		})
		t.Run("GuardBody", func(t *testing.T) {
			run(t, `DROP TRIGGER trg_processing_backfill_quota_downgrade_admission; CREATE TRIGGER trg_processing_backfill_quota_downgrade_admission BEFORE INSERT ON schema_migrations BEGIN SELECT 1; END`, `CREATE OR REPLACE FUNCTION processing_backfill_quota_downgrade_admission() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NEW; END; $$`)
		})
		t.Run("IndexDefinition", func(t *testing.T) {
			run(t, `DROP INDEX idx_backup_asset_processing_jobs_backfill_active; CREATE INDEX idx_backup_asset_processing_jobs_backfill_active ON backup_asset_processing_jobs(is_current, priority_class, state, provider_kind DESC, capability)`, `DROP INDEX idx_backup_asset_processing_jobs_backfill_active; CREATE INDEX idx_backup_asset_processing_jobs_backfill_active ON backup_asset_processing_jobs(is_current, priority_class, state, provider_kind DESC, capability)`)
			if f.engine == "postgres" {
				run(t, `DROP INDEX idx_backup_asset_processing_jobs_backfill_active; CREATE INDEX idx_backup_asset_processing_jobs_backfill_active ON backup_asset_processing_jobs(is_current, priority_class, state, provider_kind COLLATE NOCASE, capability)`, `DROP INDEX idx_backup_asset_processing_jobs_backfill_active; CREATE INDEX idx_backup_asset_processing_jobs_backfill_active ON backup_asset_processing_jobs(is_current, priority_class, state, provider_kind varchar_pattern_ops, capability)`)
				run(t, `DROP INDEX idx_backup_asset_processing_jobs_backfill_active; CREATE INDEX idx_backup_asset_processing_jobs_backfill_active ON backup_asset_processing_jobs(is_current, priority_class, state, provider_kind COLLATE NOCASE, capability)`, `DROP INDEX idx_backup_asset_processing_jobs_backfill_active; CREATE INDEX idx_backup_asset_processing_jobs_backfill_active ON backup_asset_processing_jobs(is_current, priority_class, state, provider_kind COLLATE "C", capability)`)
				run(t, `DROP INDEX idx_backup_asset_processing_jobs_backfill_active; CREATE INDEX idx_backup_asset_processing_jobs_backfill_active ON backup_asset_processing_jobs(is_current, priority_class, state, provider_kind COLLATE NOCASE, capability)`, `DROP INDEX idx_backup_asset_processing_jobs_backfill_active; CREATE INDEX idx_backup_asset_processing_jobs_backfill_active ON backup_asset_processing_jobs(is_current, priority_class, state, provider_kind, capability) INCLUDE (estimated_input_bytes)`)
			}
		})
		t.Run("Window", func(t *testing.T) {
			run(t, `UPDATE backup_asset_processing_backfill_ledger SET window_started_at = '1970-01-01 00:01:00Z'`, `UPDATE backup_asset_processing_backfill_ledger SET window_started_at = '1970-01-01 00:01:00+00'`)
		})
	})
	t.Run("PartialSchemaPreflight", func(t *testing.T) {
		run := func(t *testing.T, statement string) {
			m, db := f.openAt(t, 98)
			f.mustExec(t, db, statement)
			if err := RunMigrations(f.recoveryWorkerGorm(t, db), f.engine); !errors.Is(err, ErrMigrationPrecondition) {
				t.Fatalf("partial schema accepted: %v", err)
			}
			assertMigrationVersion(t, m, 98)
		}
		t.Run("Column", func(t *testing.T) { run(t, `ALTER TABLE backup_asset_processing_jobs ADD COLUMN provider_kind TEXT`) })
		t.Run("Ledger", func(t *testing.T) { run(t, `CREATE TABLE backup_asset_processing_backfill_ledger (id INTEGER)`) })
		t.Run("Index", func(t *testing.T) {
			run(t, `CREATE INDEX idx_backup_asset_processing_jobs_backfill_active ON backup_asset_processing_jobs(state)`)
		})
	})
	t.Run("InvalidSourcePreflight", func(t *testing.T) {
		run := func(t *testing.T, scenario string) {
			m, db := f.openAt(t, 98)
			seedBackfillQuotaJob(t, f, db)
			if f.engine == "sqlite" {
				f.mustExec(t, db, `PRAGMA ignore_check_constraints = ON`)
				f.mustExec(t, db, `PRAGMA foreign_keys = OFF`)
			} else {
				switch scenario {
				case "negative_size":
					f.mustExec(t, db, `ALTER TABLE catalog_entries DROP CONSTRAINT catalog_entries_size_check`)
				case "unknown_provider":
					f.mustExec(t, db, `ALTER TABLE backup_repositories DROP CONSTRAINT backup_repositories_provider_kind_check`)
				case "missing_entry":
					var name string
					err := db.QueryRow(`SELECT quote_ident(conname) FROM pg_catalog.pg_constraint WHERE conrelid = 'backup_asset_processing_jobs'::regclass AND contype = 'f' AND confrelid = 'catalog_entries'::regclass`).Scan(&name)
					if err != nil {
						t.Fatal(err)
					}
					f.mustExec(t, db, `ALTER TABLE backup_asset_processing_jobs DROP CONSTRAINT `+name)
				}
			}
			switch scenario {
			case "negative_size":
				f.mustExec(t, db, `UPDATE catalog_entries SET size = -1`)
			case "unknown_provider":
				// Construct an invalid historical association without updating a repository's
				// immutable provider kind. The fixture's provider CHECK is disabled above.
				repositoryID := strings.Repeat("8", 32)
				f.insertRepository(t, db, repositoryID, "unknown", time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC))
				f.mustExec(t, db, `UPDATE recovery_points SET repository_id = ? WHERE id = ?`, repositoryID, strings.Repeat("9", 32))
			case "missing_entry":
				f.mustExec(t, db, `UPDATE backup_asset_processing_jobs SET entry_id = 'missing'`)
			case "ambiguous_repository":
				f.mustExec(t, db, `ALTER TABLE backup_repositories RENAME TO backfill_test_repositories`)
				f.mustExec(t, db, `CREATE VIEW backup_repositories AS SELECT * FROM backfill_test_repositories UNION ALL SELECT * FROM backfill_test_repositories`)
			}
			if f.engine == "sqlite" {
				f.mustExec(t, db, `PRAGMA foreign_keys = ON`)
				f.mustExec(t, db, `PRAGMA ignore_check_constraints = OFF`)
			}
			var preflightErr error
			if scenario == "ambiguous_repository" {
				preflightErr = preflightProcessingBackfillQuotaMigration(db, f.engine, 98)
			} else {
				preflightErr = RunMigrations(f.recoveryWorkerGorm(t, db), f.engine)
			}
			if !errors.Is(preflightErr, ErrMigrationPrecondition) {
				t.Fatalf("invalid source accepted: %v", preflightErr)
			}
			assertMigrationVersion(t, m, 98)
			fs := sqliteMigrationsFS
			if f.engine == "postgres" {
				fs = postgresMigrationsFS
			}
			up, err := fs.ReadFile("migrations/" + f.engine + "/000099_processing_backfill_quota.up.sql")
			if err != nil {
				t.Fatal(err)
			}
			tx, err := db.Begin()
			if err != nil {
				t.Fatal(err)
			}
			_, upErr := tx.Exec(string(up))
			if err := tx.Rollback(); err != nil {
				t.Fatal(err)
			}
			if upErr == nil {
				t.Fatal("direct upgrade accepted invalid source")
			}
			assertMigrationVersion(t, m, 98)
		}
		t.Run("NegativeSize", func(t *testing.T) { run(t, "negative_size") })
		t.Run("UnknownProvider", func(t *testing.T) { run(t, "unknown_provider") })
		t.Run("MissingEntry", func(t *testing.T) { run(t, "missing_entry") })
		t.Run("AmbiguousRepository", func(t *testing.T) { run(t, "ambiguous_repository") })
	})
	if f.engine == "sqlite" {
		t.Run("SQLiteExtraObjects", func(t *testing.T) {
			run := func(t *testing.T, statement string) {
				m, db := f.openAt(t, 99)
				f.mustExec(t, db, statement)
				if err := m.Steps(-1); err == nil {
					t.Fatal("extra dependency accepted")
				}
				assertMigrationVersion(t, m, 99)
			}
			t.Run("Index", func(t *testing.T) {
				run(t, `CREATE INDEX extra_quota_index ON backup_asset_processing_jobs(provider_kind)`)
			})
			t.Run("Trigger", func(t *testing.T) {
				run(t, `CREATE TRIGGER extra_quota_trigger AFTER UPDATE ON backup_asset_processing_jobs BEGIN SELECT 1; END`)
			})
			t.Run("ExternalTrigger", func(t *testing.T) {
				run := func(t *testing.T, dependency string, direct bool) {
					m, db := f.openAt(t, 98)
					job := seedBackfillQuotaJob(t, f, db)
					if err := m.Steps(1); err != nil {
						t.Fatal(err)
					}
					f.mustExec(t, db, `CREATE TABLE external_quota_owner (id INTEGER PRIMARY KEY, value TEXT NOT NULL)`)
					f.mustExec(t, db, `INSERT INTO external_quota_owner VALUES (1, 'preserve')`)
					f.mustExec(t, db, `CREATE TRIGGER external_quota_trigger AFTER UPDATE ON external_quota_owner BEGIN SELECT `+dependency+`; END`)
					schema := func() []string {
						t.Helper()
						rows, err := db.Query(`SELECT type || '|' || name || '|' || tbl_name || '|' || coalesce(sql, '') FROM sqlite_master ORDER BY type, name`)
						if err != nil {
							t.Fatal(err)
						}
						defer rows.Close()
						var result []string
						for rows.Next() {
							var row string
							if err := rows.Scan(&row); err != nil {
								t.Fatal(err)
							}
							result = append(result, row)
						}
						if err := rows.Err(); err != nil {
							t.Fatal(err)
						}
						return result
					}
					before := schema()
					if direct {
						down, err := backfillQuotaDown(f.engine)
						if err != nil {
							t.Fatal(err)
						}
						tx, err := db.Begin()
						if err != nil {
							t.Fatal(err)
						}
						_, downErr := tx.Exec(down)
						if err := tx.Rollback(); err != nil {
							t.Fatal(err)
						}
						if downErr == nil || !strings.Contains(downErr.Error(), "CHECK constraint failed") {
							t.Errorf("direct down must reject at its guard, got %v", downErr)
						}
					} else {
						err := m.Steps(-1)
						if err == nil || !strings.Contains(err.Error(), "000099 downgrade blocked") {
							t.Errorf("migrator must reject before dirty metadata, got %v", err)
						}
					}
					assertMigrationVersion(t, m, 99)
					if after := schema(); !reflect.DeepEqual(before, after) {
						t.Error("downgrade changed schema definitions")
					}
					var provider, value string
					var estimated, charged, jobs, bytes int64
					if err := db.QueryRow(`SELECT provider_kind, estimated_input_bytes, backfill_charged FROM backup_asset_processing_jobs WHERE id = ?`, job).Scan(&provider, &estimated, &charged); err != nil {
						t.Fatal(err)
					}
					if err := db.QueryRow(`SELECT jobs_count, bytes_count FROM backup_asset_processing_backfill_ledger WHERE id = 1`).Scan(&jobs, &bytes); err != nil {
						t.Fatal(err)
					}
					if err := db.QueryRow(`SELECT value FROM external_quota_owner WHERE id = 1`).Scan(&value); err != nil {
						t.Fatal(err)
					}
					if provider != "rsync" || estimated < 0 || charged != 0 || jobs != 0 || bytes != 0 || value != "preserve" {
						t.Fatalf("downgrade changed data: %q %d %d %d %d %q", provider, estimated, charged, jobs, bytes, value)
					}
				}
				for _, tc := range []struct{ name, dependency string }{
					{"ProviderKind", `provider_kind FROM backup_asset_processing_jobs`},
					{"EstimatedBytes", `estimated_input_bytes FROM backup_asset_processing_jobs`},
					{"Charged", `backfill_charged FROM backup_asset_processing_jobs`},
					{"Ledger", `jobs_count FROM backup_asset_processing_backfill_ledger`},
				} {
					t.Run(tc.name, func(t *testing.T) {
						t.Run("Migrator", func(t *testing.T) { run(t, tc.dependency, false) })
						t.Run("DirectDown", func(t *testing.T) { run(t, tc.dependency, true) })
					})
				}
			})
			t.Run("View", func(t *testing.T) {
				run(t, `CREATE VIEW extra_quota_view AS SELECT provider_kind FROM backup_asset_processing_jobs`)
			})
			t.Run("ForeignKey", func(t *testing.T) {
				run(t, `CREATE TABLE extra_quota_reference (value INTEGER REFERENCES backup_asset_processing_backfill_ledger(id))`)
			})
			t.Run("ReplacedIndex", func(t *testing.T) {
				run(t, `DROP INDEX idx_backup_asset_processing_jobs_queue; CREATE INDEX idx_backup_asset_processing_jobs_queue ON backup_asset_processing_jobs(state)`)
			})
		})
	}
	if f.engine == "postgres" {
		t.Run("AdmissionBeforeSetVersion", func(t *testing.T) { testBackfillQuotaAdmissionBeforeSetVersion(t, f) })
		t.Run("SetVersionBeforeAdmission", func(t *testing.T) { testBackfillQuotaSetVersionBeforeAdmission(t, f) })
		t.Run("DirectDownJobsFirst", func(t *testing.T) { testBackfillQuotaDirectDownJobsFirst(t, f) })
	}
}

func backfillQuotaDown(engine string) (string, error) {
	fs := sqliteMigrationsFS
	if engine == "postgres" {
		fs = postgresMigrationsFS
	}
	b, err := fs.ReadFile("migrations/" + engine + "/000099_processing_backfill_quota.down.sql")
	return string(b), err
}

func quotaPostgresTx(t *testing.T, db *sql.DB) (*sql.Tx, int) {
	t.Helper()
	tx, err := db.BeginTx(context.Background(), &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback() })
	var pid int
	if err := tx.QueryRow(`SELECT pg_backend_pid()`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	return tx, pid
}

func quotaWaitBlocked(t *testing.T, db *sql.DB, blocker int) int {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	for {
		var pid int
		err := db.QueryRowContext(ctx, `SELECT pid FROM pg_catalog.pg_stat_activity WHERE wait_event_type = 'Lock' AND $1 = ANY(pg_catalog.pg_blocking_pids(pid)) LIMIT 1`, blocker).Scan(&pid)
		if err == nil {
			return pid
		}
		if !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("observe blocked transaction: %v", err)
		}
		if ctx.Err() != nil {
			t.Fatalf("transaction did not block on %d: %v", blocker, ctx.Err())
		}
	}
}

func quotaAdmissionLocks(t *testing.T, tx *sql.Tx, job string) {
	t.Helper()
	if _, err := tx.Exec(`SELECT id FROM backup_asset_processing_jobs WHERE id = $1 FOR UPDATE`, job); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`SELECT version, dirty FROM schema_migrations FOR SHARE`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`SELECT id FROM backup_asset_processing_backfill_ledger WHERE id = 1 FOR UPDATE`); err != nil {
		t.Fatal(err)
	}
}

func quotaCharge(t *testing.T, tx *sql.Tx, job string) {
	t.Helper()
	if _, err := tx.Exec(`UPDATE backup_asset_processing_jobs SET backfill_charged = TRUE WHERE id = $1`, job); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`UPDATE backup_asset_processing_backfill_ledger SET jobs_count = 1 WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
}

func quotaMigrationDriver(t *testing.T, db *sql.DB) interface{ SetVersion(int, bool) error } {
	t.Helper()
	driver, err := pgxmigrate.WithInstance(db, &pgxmigrate.Config{})
	if err != nil {
		t.Fatal(err)
	}
	return driver
}

func testBackfillQuotaAdmissionBeforeSetVersion(t *testing.T, f migrationFixture) {
	m, db := f.openAt(t, 98)
	job := seedBackfillQuotaJob(t, f, db)
	if err := m.Steps(1); err != nil {
		t.Fatal(err)
	}
	driver := quotaMigrationDriver(t, db)
	tx, pid := quotaPostgresTx(t, db)
	quotaAdmissionLocks(t, tx, job)
	quotaCharge(t, tx, job)
	result := make(chan error, 1)
	go func() { result <- driver.SetVersion(98, true) }()
	quotaWaitBlocked(t, db, pid)
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := <-result; err == nil {
		t.Fatal("SetVersion accepted committed charge")
	}
	assertMigrationVersion(t, m, 99)
}

func testBackfillQuotaSetVersionBeforeAdmission(t *testing.T, f migrationFixture) {
	m, db := f.openAt(t, 98)
	seedBackfillQuotaJob(t, f, db)
	if err := m.Steps(1); err != nil {
		t.Fatal(err)
	}
	driver := quotaMigrationDriver(t, db)
	barrier, pid := quotaPostgresTx(t, db)
	if _, err := barrier.Exec(`SELECT id FROM backup_asset_processing_backfill_ledger WHERE id = 1 FOR UPDATE`); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() { result <- driver.SetVersion(98, true) }()
	migratorPID := quotaWaitBlocked(t, db, pid)
	admission, _ := quotaPostgresTx(t, db)
	read := make(chan error, 1)
	go func() {
		var version int
		var dirty bool
		err := admission.QueryRow(`SELECT version, dirty FROM schema_migrations FOR SHARE`).Scan(&version, &dirty)
		if err == nil || errors.Is(err, sql.ErrNoRows) {
			err = admission.QueryRow(`SELECT version, dirty FROM schema_migrations FOR SHARE`).Scan(&version, &dirty)
		}
		if err == nil && (version != 98 || !dirty) {
			err = errors.New("admission observed stale clean metadata")
		}
		read <- err
	}()
	quotaWaitBlocked(t, db, migratorPID)
	if err := barrier.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	if err := <-read; err != nil {
		t.Fatal(err)
	}
	if err := admission.Rollback(); err != nil {
		t.Fatal(err)
	}
	var charged bool
	var jobsCount, bytesCount int64
	if err := db.QueryRow(`SELECT backfill_charged FROM backup_asset_processing_jobs`).Scan(&charged); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT jobs_count, bytes_count FROM backup_asset_processing_backfill_ledger`).Scan(&jobsCount, &bytesCount); err != nil {
		t.Fatal(err)
	}
	if charged || jobsCount != 0 || bytesCount != 0 {
		t.Fatal("rejected admission changed quota state")
	}
	if err := driver.SetVersion(99, false); err != nil {
		t.Fatal(err)
	}
	assertMigrationVersion(t, m, 99)
}

func testBackfillQuotaDirectDownJobsFirst(t *testing.T, f migrationFixture) {
	m, db := f.openAt(t, 98)
	job := seedBackfillQuotaJob(t, f, db)
	if err := m.Steps(1); err != nil {
		t.Fatal(err)
	}
	tx, pid := quotaPostgresTx(t, db)
	quotaAdmissionLocks(t, tx, job)
	quotaCharge(t, tx, job)
	down, err := backfillQuotaDown("postgres")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	result := make(chan error, 1)
	go func() { _, err := conn.ExecContext(ctx, down); result <- err }()
	quotaWaitBlocked(t, db, pid)
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := <-result; err == nil {
		t.Fatal("direct down accepted committed charge")
	}
	if _, err := conn.ExecContext(ctx, `ROLLBACK`); err != nil {
		t.Fatal(err)
	}
	assertMigrationVersion(t, m, 99)
}
