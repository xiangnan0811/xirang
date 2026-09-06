package database

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"
	"xirang/backend/internal/model"
)

const lifecycleEffectClaimAuditSlotMigrationVersion uint = 77

func TestLifecycleEffectClaimAuditSlotMigrationSQLitePristineDown(t *testing.T) {
	runLifecycleEffectClaimAuditSlotMigrationTests(t, newSQLiteMigrationFixture(t), "PristineDown")
}

func TestLifecycleEffectClaimAuditSlotMigrationSQLiteClaimUsedDown(t *testing.T) {
	runLifecycleEffectClaimAuditSlotMigrationTests(t, newSQLiteMigrationFixture(t), "ClaimUsedDown")
}

func TestLifecycleEffectClaimAuditSlotMigrationSQLiteSlotUsedDown(t *testing.T) {
	runLifecycleEffectClaimAuditSlotMigrationTests(t, newSQLiteMigrationFixture(t), "SlotUsedDown")
}

func TestLifecycleEffectClaimAuditSlotMigrationSQLiteConstraints(t *testing.T) {
	runLifecycleEffectClaimAuditSlotMigrationTests(t, newSQLiteMigrationFixture(t), "Constraints")
}

func TestLifecycleEffectClaimAuditSlotMigrationSQLiteClaimTransitionRebinding(t *testing.T) {
	runLifecycleEffectClaimAuditSlotMigrationTests(t, newSQLiteMigrationFixture(t), "ClaimTransitionRebinding")
}

func TestLifecycleEffectClaimAuditSlotMigrationSQLiteUpgradeCutover(t *testing.T) {
	runLifecycleEffectClaimAuditSlotMigrationTests(t, newSQLiteMigrationFixture(t), "UpgradeCutover")
}

func TestLifecycleEffectClaimAuditSlotMigrationPostgresPristineDown(t *testing.T) {
	runLifecycleEffectClaimAuditSlotMigrationTests(t, newRequiredPostgresMigrationFixture(t), "PristineDown")
}

func TestLifecycleEffectClaimAuditSlotMigrationPostgresClaimUsedDown(t *testing.T) {
	runLifecycleEffectClaimAuditSlotMigrationTests(t, newRequiredPostgresMigrationFixture(t), "ClaimUsedDown")
}

func TestLifecycleEffectClaimAuditSlotMigrationPostgresSlotUsedDown(t *testing.T) {
	runLifecycleEffectClaimAuditSlotMigrationTests(t, newRequiredPostgresMigrationFixture(t), "SlotUsedDown")
}

func TestLifecycleEffectClaimAuditSlotMigrationPostgresConstraints(t *testing.T) {
	runLifecycleEffectClaimAuditSlotMigrationTests(t, newRequiredPostgresMigrationFixture(t), "Constraints")
}

func TestLifecycleEffectClaimAuditSlotMigrationPostgresClaimTransitionRebinding(t *testing.T) {
	runLifecycleEffectClaimAuditSlotMigrationTests(t, newRequiredPostgresMigrationFixture(t), "ClaimTransitionRebinding")
}

func TestLifecycleEffectClaimAuditSlotMigrationPostgresUpgradeCutover(t *testing.T) {
	runLifecycleEffectClaimAuditSlotMigrationTests(t, newRequiredPostgresMigrationFixture(t), "UpgradeCutover")
}

func runLifecycleEffectClaimAuditSlotMigrationTests(t *testing.T, fixture migrationFixture, scenario string) {
	t.Helper()

	if scenario == "" || scenario == "PristineDown" {
		t.Run("PristineDown", func(t *testing.T) {
			migrator, db := fixture.openAt(t, providerNativeVersionReferenceReasonMigrationVersion)
			lifecycleAdmissionBefore := fixture.recoveryTriggerDefinition(t, db, "schema_migrations", "trg_backup_asset_lifecycle_downgrade_admission")
			providerAdmissionBefore := fixture.recoveryTriggerDefinition(t, db, "schema_migrations", "trg_provider_native_version_reference_reason_downgrade_admission")
			migrateToBackupAssetVersion(t, migrator, lifecycleEffectClaimAuditSlotMigrationVersion)
			assertMigrationVersion(t, migrator, lifecycleEffectClaimAuditSlotMigrationVersion)
			if err := validateMinimumRecoverySchema(db, fixture.engine, int64(lifecycleEffectClaimAuditSlotMigrationVersion)); err != nil {
				t.Fatalf("validate clean v77 schema: %v", err)
			}
			if err := migrator.Steps(-1); err != nil {
				t.Fatalf("pristine v77 down: %v", err)
			}
			assertMigrationVersion(t, migrator, providerNativeVersionReferenceReasonMigrationVersion)
			for _, table := range []string{lifecycleEffectClaimAuditSlotClaimsTable, lifecycleEffectClaimAuditSlotSlotsTable} {
				if databaseTableExists(t, db, fixture.engine, table) {
					t.Fatalf("pristine down retained %s", table)
				}
			}
			assertLifecycleEffectClaimAuditSlotObjectsAbsent(t, fixture, db)
			if got := fixture.recoveryTriggerDefinition(t, db, "schema_migrations", "trg_backup_asset_lifecycle_downgrade_admission"); got != lifecycleAdmissionBefore {
				t.Fatalf("pristine down changed v70 admission trigger\n got: %s\nwant: %s", got, lifecycleAdmissionBefore)
			}
			if got := fixture.recoveryTriggerDefinition(t, db, "schema_migrations", "trg_provider_native_version_reference_reason_downgrade_admission"); got != providerAdmissionBefore {
				t.Fatalf("pristine down changed v76 admission trigger\n got: %s\nwant: %s", got, providerAdmissionBefore)
			}
		})
	}

	if scenario == "" || scenario == "ClaimUsedDown" {
		t.Run("ClaimUsedDown", func(t *testing.T) {
			t.Run("AdmissionIntact", func(t *testing.T) {
				migrator, db := fixture.openAt(t, providerNativeVersionReferenceReasonMigrationVersion)
				seed := fixture.seedLifecycleMigrationBase(t, db, "a", 77101)
				attemptID := strings.Repeat("b", 32)
				insertLifecycleAttempt(t, fixture, db, attemptID, seed.PointID, "retention_expire", "selected", "")
				migrateToBackupAssetVersion(t, migrator, lifecycleEffectClaimAuditSlotMigrationVersion)
				insertLifecycleEffectClaim(t, fixture, db, attemptID, seed.Now)
				before := fixture.recoveryTriggerDefinition(t, db, "schema_migrations", lifecycleEffectClaimAuditSlotAdmissionTrigger)
				if err := migrator.Steps(-1); err == nil {
					t.Fatalf("used claim down unexpectedly succeeded")
				}
				assertMigrationVersion(t, migrator, lifecycleEffectClaimAuditSlotMigrationVersion)
				if got := fixture.recoveryTriggerDefinition(t, db, "schema_migrations", lifecycleEffectClaimAuditSlotAdmissionTrigger); got != before {
					t.Fatalf("admission rejection changed v77 trigger\n got: %s\nwant: %s", got, before)
				}
				if count := lifecycleCount(t, fixture, db, lifecycleEffectClaimAuditSlotClaimsTable); count != 1 {
					t.Fatalf("claim rows after admission rejection=%d, want 1", count)
				}
			})
			t.Run("AdmissionBypassed", func(t *testing.T) {
				migrator, db := fixture.openAt(t, providerNativeVersionReferenceReasonMigrationVersion)
				seed := fixture.seedLifecycleMigrationBase(t, db, "c", 77102)
				attemptID := strings.Repeat("d", 32)
				insertLifecycleAttempt(t, fixture, db, attemptID, seed.PointID, "retention_expire", "selected", "")
				migrateToBackupAssetVersion(t, migrator, lifecycleEffectClaimAuditSlotMigrationVersion)
				insertLifecycleEffectClaim(t, fixture, db, attemptID, seed.Now)
				dropLifecycleEffectClaimAuditSlotAdmission(t, fixture, db)
				if err := executeLifecycleEffectClaimAuditSlotDown(t, fixture, db); err == nil {
					t.Fatalf("direct down with claim unexpectedly succeeded")
				}
				assertMigrationVersion(t, migrator, lifecycleEffectClaimAuditSlotMigrationVersion)
				if count := lifecycleCount(t, fixture, db, lifecycleEffectClaimAuditSlotClaimsTable); count != 1 {
					t.Fatalf("claim rows after direct guard rejection=%d, want 1", count)
				}
				if !databaseTableExists(t, db, fixture.engine, lifecycleEffectClaimAuditSlotSlotsTable) {
					t.Fatal("direct claim guard rejection dropped audit-slot table")
				}
			})
		})
	}

	if scenario == "" || scenario == "SlotUsedDown" {
		t.Run("SlotUsedDown", func(t *testing.T) {
			t.Run("AdmissionIntact", func(t *testing.T) {
				migrator, db := fixture.openAt(t, providerNativeVersionReferenceReasonMigrationVersion)
				seed := fixture.seedLifecycleMigrationBase(t, db, "e", 77103)
				attemptID := strings.Repeat("f", 32)
				insertLifecycleAttempt(t, fixture, db, attemptID, seed.PointID, "retention_expire", "selected", "")
				migrateToBackupAssetVersion(t, migrator, lifecycleEffectClaimAuditSlotMigrationVersion)
				insertLifecycleAuditSlot(t, fixture, db, attemptID, "blocked", seed.Now)
				before := fixture.recoveryTriggerDefinition(t, db, "schema_migrations", lifecycleEffectClaimAuditSlotAdmissionTrigger)
				if err := migrator.Steps(-1); err == nil {
					t.Fatalf("used slot down unexpectedly succeeded")
				}
				assertMigrationVersion(t, migrator, lifecycleEffectClaimAuditSlotMigrationVersion)
				if got := fixture.recoveryTriggerDefinition(t, db, "schema_migrations", lifecycleEffectClaimAuditSlotAdmissionTrigger); got != before {
					t.Fatalf("admission rejection changed v77 trigger\n got: %s\nwant: %s", got, before)
				}
				if count := lifecycleCount(t, fixture, db, lifecycleEffectClaimAuditSlotSlotsTable); count != 1 {
					t.Fatalf("slot rows after admission rejection=%d, want 1", count)
				}
			})
			t.Run("AdmissionBypassed", func(t *testing.T) {
				migrator, db := fixture.openAt(t, providerNativeVersionReferenceReasonMigrationVersion)
				seed := fixture.seedLifecycleMigrationBase(t, db, "1", 77104)
				attemptID := strings.Repeat("2", 32)
				insertLifecycleAttempt(t, fixture, db, attemptID, seed.PointID, "retention_expire", "selected", "")
				migrateToBackupAssetVersion(t, migrator, lifecycleEffectClaimAuditSlotMigrationVersion)
				insertLifecycleAuditSlot(t, fixture, db, attemptID, "blocked", seed.Now)
				dropLifecycleEffectClaimAuditSlotAdmission(t, fixture, db)
				if err := executeLifecycleEffectClaimAuditSlotDown(t, fixture, db); err == nil {
					t.Fatalf("direct down with slot unexpectedly succeeded")
				}
				assertMigrationVersion(t, migrator, lifecycleEffectClaimAuditSlotMigrationVersion)
				if count := lifecycleCount(t, fixture, db, lifecycleEffectClaimAuditSlotSlotsTable); count != 1 {
					t.Fatalf("slot rows after direct guard rejection=%d, want 1", count)
				}
				if !databaseTableExists(t, db, fixture.engine, lifecycleEffectClaimAuditSlotClaimsTable) {
					t.Fatal("direct slot guard rejection dropped claim table")
				}
			})
		})
	}

	if scenario == "" || scenario == "Constraints" {
		t.Run("Constraints", func(t *testing.T) {
			migrator, db := fixture.openAt(t, providerNativeVersionReferenceReasonMigrationVersion)
			seed := fixture.seedLifecycleMigrationBase(t, db, "3", 77105)
			attemptID := strings.Repeat("4", 32)
			insertLifecycleAttempt(t, fixture, db, attemptID, seed.PointID, "retention_expire", "selected", "")
			migrateToBackupAssetVersion(t, migrator, lifecycleEffectClaimAuditSlotMigrationVersion)
			if err := validateMinimumRecoverySchema(db, fixture.engine, int64(lifecycleEffectClaimAuditSlotMigrationVersion)); err != nil {
				t.Fatalf("validate v77 schema: %v", err)
			}
			wantClaimColumns := gormColumnNames(t, model.RecoveryPointLifecycleEffectClaim{})
			gotClaimColumns := migrationColumnNames(t, fixture, db, lifecycleEffectClaimAuditSlotClaimsTable)
			if !sameStringSet(gotClaimColumns, wantClaimColumns) {
				t.Fatalf("claim model/schema columns mismatch\n got: %v\nwant: %v", gotClaimColumns, wantClaimColumns)
			}
			wantSlotColumns := gormColumnNames(t, model.RecoveryPointLifecycleAuditSlot{})
			gotSlotColumns := migrationColumnNames(t, fixture, db, lifecycleEffectClaimAuditSlotSlotsTable)
			if !sameStringSet(gotSlotColumns, wantSlotColumns) {
				t.Fatalf("slot model/schema columns mismatch\n got: %v\nwant: %v", gotSlotColumns, wantSlotColumns)
			}
			if fixture.engine == "sqlite" {
				assertSQLiteForeignKeyAction(t, db, lifecycleEffectClaimAuditSlotClaimsTable, "attempt_id", "recovery_point_lifecycle_attempts", "RESTRICT")
				assertSQLiteForeignKeyAction(t, db, lifecycleEffectClaimAuditSlotSlotsTable, "attempt_id", "recovery_point_lifecycle_attempts", "RESTRICT")
			} else {
				assertPostgresForeignKeyAction(t, db, lifecycleEffectClaimAuditSlotClaimsTable, "attempt_id", "recovery_point_lifecycle_attempts", "RESTRICT")
				assertPostgresForeignKeyAction(t, db, lifecycleEffectClaimAuditSlotSlotsTable, "attempt_id", "recovery_point_lifecycle_attempts", "RESTRICT")
			}
			invalidClaim := `INSERT INTO recovery_point_lifecycle_effect_claims
			(id, attempt_id, executor_id, execution_id, transition_revision, lease_id, lease_attempt_id,
			 lease_fence_token_hash, target_identity_digest, state, deadline_at, heartbeat_at, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
			for name, args := range map[string][]any{
				"bad id":       {strings.Repeat("A", 32), attemptID, strings.Repeat("b", 32), strings.Repeat("c", 32), 1, strings.Repeat("d", 32), strings.Repeat("e", 32), strings.Repeat("f", 64), strings.Repeat("0", 64), "in_flight", seed.Now, seed.Now, seed.Now, seed.Now},
				"bad revision": {strings.Repeat("a", 32), attemptID, strings.Repeat("b", 32), strings.Repeat("c", 32), 0, strings.Repeat("d", 32), strings.Repeat("e", 32), strings.Repeat("f", 64), strings.Repeat("0", 64), "in_flight", seed.Now, seed.Now, seed.Now, seed.Now},
				"bad state":    {strings.Repeat("a", 32), attemptID, strings.Repeat("b", 32), strings.Repeat("c", 32), 1, strings.Repeat("d", 32), strings.Repeat("e", 32), strings.Repeat("f", 64), strings.Repeat("0", 64), "released", seed.Now, seed.Now, seed.Now, seed.Now},
			} {
				t.Run(name, func(t *testing.T) { fixture.expectExecRejected(t, db, invalidClaim, args...) })
			}
			insertLifecycleEffectClaim(t, fixture, db, attemptID, seed.Now)
			fixture.expectExecRejected(t, db, `INSERT INTO recovery_point_lifecycle_audit_slots
			(id, attempt_id, status, emitted_at, created_at) VALUES (?, ?, 'unsupported', ?, ?)`, strings.Repeat("9", 32), attemptID, seed.Now, seed.Now)
			insertLifecycleAuditSlot(t, fixture, db, attemptID, "blocked", seed.Now)
			fixture.expectExecRejected(t, db, `INSERT INTO recovery_point_lifecycle_audit_slots
			(id, attempt_id, status, emitted_at, created_at) VALUES (?, ?, 'blocked', ?, ?)`, strings.Repeat("8", 32), attemptID, seed.Now, seed.Now)
			insertLifecycleAuditSlot(t, fixture, db, attemptID, "deleted", seed.Now.Add(time.Second))
			fixture.expectExecRejected(t, db, `INSERT INTO recovery_point_lifecycle_audit_slots
			(id, attempt_id, status, emitted_at, created_at) VALUES (?, ?, 'identity_conflict', ?, ?)`, strings.Repeat("7", 32), attemptID, seed.Now, seed.Now)
			for _, order := range []struct {
				name          string
				firstStatus   string
				secondStatus  string
				wantSecondErr bool
			}{
				{name: "ObservationThenTerminal", firstStatus: "blocked", secondStatus: "deleted"},
				{name: "TerminalThenObservation", firstStatus: "deleted", secondStatus: "blocked", wantSecondErr: true},
			} {
				order := order
				t.Run("Concurrent/"+order.name, func(t *testing.T) {
					runLifecycleAuditSlotConcurrentOrder(t, fixture, order.firstStatus, order.secondStatus, order.wantSecondErr)
				})
			}
		})
	}

	if scenario == "" || scenario == "ClaimTransitionRebinding" {
		t.Run("ClaimTransitionRebinding", func(t *testing.T) {
			migrator, db := fixture.openAt(t, providerNativeVersionReferenceReasonMigrationVersion)
			seed := fixture.seedLifecycleMigrationBase(t, db, "5", 77106)
			attemptID := strings.Repeat("6", 32)
			insertLifecycleAttempt(t, fixture, db, attemptID, seed.PointID, "retention_expire", "selected", "")
			migrateToBackupAssetVersion(t, migrator, lifecycleEffectClaimAuditSlotMigrationVersion)
			insertLifecycleEffectClaim(t, fixture, db, attemptID, seed.Now)
			fixture.mustExec(t, db, `UPDATE recovery_point_lifecycle_effect_claims
			SET heartbeat_at = ?, deadline_at = ?, updated_at = ? WHERE attempt_id = ?`, seed.Now.Add(time.Second), seed.Now.Add(2*time.Minute), seed.Now.Add(time.Second), attemptID)
			fixture.expectExecRejected(t, db, `UPDATE recovery_point_lifecycle_effect_claims SET executor_id = ? WHERE attempt_id = ?`, strings.Repeat("1", 32), attemptID)
			fixture.mustExec(t, db, `UPDATE recovery_point_lifecycle_effect_claims SET state = 'uncertain', updated_at = ? WHERE attempt_id = ?`, seed.Now.Add(2*time.Second), attemptID)
			for _, column := range []string{"deadline_at", "heartbeat_at", "updated_at"} {
				fixture.expectExecRejected(t, db, `UPDATE recovery_point_lifecycle_effect_claims SET `+column+` = ? WHERE attempt_id = ?`,
					seed.Now.Add(3*time.Second), attemptID)
			}
			fixture.expectExecRejected(t, db, `UPDATE recovery_point_lifecycle_effect_claims SET state = 'in_flight' WHERE attempt_id = ?`, attemptID)
			fixture.mustExec(t, db, `UPDATE recovery_point_lifecycle_effect_claims
			SET state = 'in_flight', executor_id = ?, execution_id = ?, transition_revision = 2,
			    lease_id = ?, lease_attempt_id = ?, lease_fence_token_hash = ?, heartbeat_at = ?, deadline_at = ?, updated_at = ?
			WHERE attempt_id = ?`, strings.Repeat("1", 32), strings.Repeat("2", 32), strings.Repeat("3", 32), strings.Repeat("4", 32), strings.Repeat("5", 64), seed.Now, seed.Now.Add(time.Minute), seed.Now, attemptID)
			fixture.mustExec(t, db, `UPDATE recovery_point_lifecycle_effect_claims SET state = 'proven', updated_at = ? WHERE attempt_id = ?`, seed.Now.Add(3*time.Second), attemptID)
			fixture.expectExecRejected(t, db, `UPDATE recovery_point_lifecycle_effect_claims SET heartbeat_at = ? WHERE attempt_id = ?`, seed.Now.Add(4*time.Second), attemptID)
			fixture.expectExecRejected(t, db, `DELETE FROM recovery_point_lifecycle_effect_claims WHERE attempt_id = ?`, attemptID)
		})
	}

	if scenario == "" || scenario == "UpgradeCutover" {
		t.Run("UpgradeCutover", func(t *testing.T) {
			t.Run("ExactBlockedEvent", func(t *testing.T) {
				migrator, db := fixture.openAt(t, providerNativeVersionReferenceReasonMigrationVersion)
				seed := fixture.seedLifecycleMigrationBase(t, db, "7", 77107)
				attemptID := strings.Repeat("8", 32)
				insertLifecycleAttempt(t, fixture, db, attemptID, seed.PointID, "retention_expire", "blocked", "provider_worm")
				insertAuditCheckpoint(t, fixture, db, seed.Now)
				insertSettledAuditEvent(t, fixture, db, 1, 1, seed.RepositoryID, seed.PointID, attemptID, "blocked", "blocked", seed.Now)
				migrateToBackupAssetVersion(t, migrator, lifecycleEffectClaimAuditSlotMigrationVersion)
				assertLifecycleSlotStatus(t, fixture, db, attemptID, "blocked")
			})
			t.Run("ExactTerminalEvent", func(t *testing.T) {
				migrator, db := fixture.openAt(t, providerNativeVersionReferenceReasonMigrationVersion)
				seed := fixture.seedLifecycleMigrationBase(t, db, "9", 77108)
				attemptID := strings.Repeat("a", 32)
				insertLifecycleAttempt(t, fixture, db, attemptID, seed.PointID, "explicit_purge", "complete", "")
				insertLifecycleTombstoneForOperation(t, fixture, db, seed, "explicit_purge", "provider_already_absent")
				insertAuditCheckpoint(t, fixture, db, seed.Now)
				insertSettledAuditEvent(t, fixture, db, 1, 1, seed.RepositoryID, seed.PointID, attemptID, "already_absent", "success", seed.Now)
				migrateToBackupAssetVersion(t, migrator, lifecycleEffectClaimAuditSlotMigrationVersion)
				assertLifecycleSlotStatus(t, fixture, db, attemptID, "already_absent")
			})
			t.Run("TerminalInference", func(t *testing.T) {
				migrator, db := fixture.openAt(t, providerNativeVersionReferenceReasonMigrationVersion)
				seed := fixture.seedLifecycleMigrationBase(t, db, "b", 77109)
				attemptID := strings.Repeat("c", 32)
				insertLifecycleAttempt(t, fixture, db, attemptID, seed.PointID, "retention_expire", "complete", "")
				insertLifecycleTombstoneForOperation(t, fixture, db, seed, "retention_expire", "provider_deleted")
				migrateToBackupAssetVersion(t, migrator, lifecycleEffectClaimAuditSlotMigrationVersion)
				assertLifecycleSlotStatus(t, fixture, db, attemptID, "deleted")
			})
			t.Run("EqualTimestampObservationWithInferredTerminalRejected", func(t *testing.T) {
				migrator, db := fixture.openAt(t, providerNativeVersionReferenceReasonMigrationVersion)
				seed := fixture.seedLifecycleMigrationBase(t, db, "a", 77121)
				attemptID := strings.Repeat("b", 32)
				insertLifecycleAttempt(t, fixture, db, attemptID, seed.PointID, "retention_expire", "complete", "")
				insertLifecycleTombstoneForOperation(t, fixture, db, seed, "retention_expire", "provider_deleted")
				insertAuditCheckpoint(t, fixture, db, seed.Now)
				insertSettledAuditEvent(t, fixture, db, 1, 1, seed.RepositoryID, seed.PointID, attemptID, "blocked", "blocked", seed.Now)
				if err := migrator.Migrate(lifecycleEffectClaimAuditSlotMigrationVersion); err == nil {
					t.Fatal("equal-time observation with inferred terminal unexpectedly migrated to v77")
				}
				assertDirtyLifecycleEffectClaimAuditSlotMigration(t, fixture, db)
				if databaseTableExists(t, db, fixture.engine, lifecycleEffectClaimAuditSlotSlotsTable) {
					t.Fatal("equal-time inferred-terminal rejection left audit-slot table")
				}
			})

			t.Run("EqualTimestampObservationThenTerminal", func(t *testing.T) {
				migrator, db := fixture.openAt(t, providerNativeVersionReferenceReasonMigrationVersion)
				seed := fixture.seedLifecycleMigrationBase(t, db, "a", 77130)
				attemptID := strings.Repeat("b", 32)
				insertLifecycleAttempt(t, fixture, db, attemptID, seed.PointID, "retention_expire", "complete", "")
				insertLifecycleTombstoneForOperation(t, fixture, db, seed, "retention_expire", "provider_deleted")
				insertAuditCheckpoint(t, fixture, db, seed.Now)
				insertSettledAuditEvent(t, fixture, db, 1, 1, seed.RepositoryID, seed.PointID, attemptID, "blocked", "blocked", seed.Now)
				insertSettledAuditEvent(t, fixture, db, 1, 2, seed.RepositoryID, seed.PointID, attemptID, "deleted", "success", seed.Now)
				if err := migrator.Migrate(lifecycleEffectClaimAuditSlotMigrationVersion); err != nil {
					t.Fatalf("equal-time observation-to-terminal migration: %v", err)
				}
				assertLifecycleSlotStatus(t, fixture, db, attemptID, "blocked")
				assertLifecycleSlotStatus(t, fixture, db, attemptID, "deleted")
			})

			t.Run("EqualTimestampTerminalThenObservationRejected", func(t *testing.T) {
				migrator, db := fixture.openAt(t, providerNativeVersionReferenceReasonMigrationVersion)
				seed := fixture.seedLifecycleMigrationBase(t, db, "c", 77131)
				attemptID := strings.Repeat("d", 32)
				insertLifecycleAttempt(t, fixture, db, attemptID, seed.PointID, "retention_expire", "complete", "")
				insertLifecycleTombstoneForOperation(t, fixture, db, seed, "retention_expire", "provider_deleted")
				insertAuditCheckpoint(t, fixture, db, seed.Now)
				insertSettledAuditEvent(t, fixture, db, 1, 1, seed.RepositoryID, seed.PointID, attemptID, "deleted", "success", seed.Now)
				insertSettledAuditEvent(t, fixture, db, 1, 2, seed.RepositoryID, seed.PointID, attemptID, "blocked", "blocked", seed.Now)
				if err := migrator.Migrate(lifecycleEffectClaimAuditSlotMigrationVersion); err == nil {
					t.Fatal("equal-time terminal-to-observation migration unexpectedly succeeded")
				}
				assertDirtyLifecycleEffectClaimAuditSlotMigration(t, fixture, db)
				if databaseTableExists(t, db, fixture.engine, lifecycleEffectClaimAuditSlotSlotsTable) {
					t.Fatal("equal-time terminal-to-observation rejection left audit-slot table")
				}
			})

			t.Run("ZeroCreatedAtReceiptRejected", func(t *testing.T) {
				migrator, db := fixture.openAt(t, providerNativeVersionReferenceReasonMigrationVersion)
				seed := fixture.seedLifecycleMigrationBase(t, db, "c", 77120)
				attemptID := strings.Repeat("d", 32)
				insertLifecycleAttempt(t, fixture, db, attemptID, seed.PointID, "retention_expire", "complete", "")
				fixture.mustExec(t, db, `UPDATE recovery_points SET state = 'expired' WHERE id = ?`, seed.PointID)
				zero := time.Time{}
				fixture.mustExec(t, db, `INSERT INTO recovery_point_lifecycle_tombstones
					(recovery_point_id, repository_id, original_semantics, terminal_operation, terminal_state,
					 managed_history, deletion_receipt_digest, purged_at, result_code, created_at)
					VALUES (?, ?, 'native_snapshot', 'retention_expire', 'expired', ?, ?, ?, 'provider_deleted', ?)`,
					seed.PointID, seed.RepositoryID, true, strings.Repeat("a", 64), zero, zero)
				if err := migrator.Migrate(lifecycleEffectClaimAuditSlotMigrationVersion); err == nil {
					t.Fatal("zero-created-at receipt unexpectedly migrated to v77")
				}
				assertDirtyLifecycleEffectClaimAuditSlotMigration(t, fixture, db)
				if databaseTableExists(t, db, fixture.engine, lifecycleEffectClaimAuditSlotSlotsTable) {
					t.Fatal("zero-created-at receipt rejection left audit-slot table")
				}
			})
			t.Run("NonCandidateIgnored", func(t *testing.T) {
				for index, candidate := range []struct {
					name      string
					operation string
					phase     string
					reason    string
				}{
					{name: "MutableRetire", operation: "mutable_retire", phase: "blocked", reason: "provider_worm"},
					{name: "Selected", operation: "retention_expire", phase: "selected"},
					{name: "ExplicitPurgeSelected", operation: "explicit_purge", phase: "selected"},
					{name: "UnsupportedBlockedReason", operation: "retention_expire", phase: "blocked", reason: "lease_live"},
					{name: "Revoking", operation: "retention_expire", phase: "revoking"},
					{name: "Draining", operation: "retention_expire", phase: "draining"},
					{name: "Cleaning", operation: "retention_expire", phase: "cleaning"},
				} {
					candidate := candidate
					t.Run(candidate.name, func(t *testing.T) {
						migrator, db := fixture.openAt(t, providerNativeVersionReferenceReasonMigrationVersion)
						seed := fixture.seedLifecycleMigrationBase(t, db, fmt.Sprintf("%x", (13+index)%16), int64(77110+index))
						attemptID := strings.Repeat(fmt.Sprintf("%x", index+1), 32)
						insertLifecycleAttempt(t, fixture, db, attemptID, seed.PointID, candidate.operation, candidate.phase, candidate.reason)
						migrateToBackupAssetVersion(t, migrator, lifecycleEffectClaimAuditSlotMigrationVersion)
						assertMigrationVersion(t, migrator, lifecycleEffectClaimAuditSlotMigrationVersion)
						if count := lifecycleCount(t, fixture, db, lifecycleEffectClaimAuditSlotClaimsTable); count != 0 {
							t.Fatalf("non-candidate %s generated %d claims", candidate.name, count)
						}
						if count := lifecycleCount(t, fixture, db, lifecycleEffectClaimAuditSlotSlotsTable); count != 0 {
							t.Fatalf("non-candidate %s generated %d slots", candidate.name, count)
						}
					})
				}
			})
			t.Run("ProviderDeleteWithoutReceiptRejected", func(t *testing.T) {
				migrator, db := fixture.openAt(t, providerNativeVersionReferenceReasonMigrationVersion)
				seed := fixture.seedLifecycleMigrationBase(t, db, "f", 77111)
				attemptID := strings.Repeat("1", 32)
				insertLifecycleAttempt(t, fixture, db, attemptID, seed.PointID, "retention_expire", "provider_delete", "")
				if err := migrator.Migrate(lifecycleEffectClaimAuditSlotMigrationVersion); err == nil {
					t.Fatal("provider_delete without receipt unexpectedly migrated to v77")
				}
				assertDirtyLifecycleEffectClaimAuditSlotMigration(t, fixture, db)
				if databaseTableExists(t, db, fixture.engine, lifecycleEffectClaimAuditSlotClaimsTable) || databaseTableExists(t, db, fixture.engine, lifecycleEffectClaimAuditSlotSlotsTable) {
					t.Fatal("rejected provider_delete cutover left v77 tables")
				}
			})
			t.Run("NearMissRollsBack", func(t *testing.T) {
				nearMisses := []struct {
					name   string
					mutate func(*lifecycleSettledAuditEvent, lifecycleMigrationSeed, string)
				}{
					{
						name: "Action",
						mutate: func(event *lifecycleSettledAuditEvent, _ lifecycleMigrationSeed, _ string) {
							event.action = "repository_purge_plan"
						},
					},
					{
						name: "Outcome",
						mutate: func(event *lifecycleSettledAuditEvent, _ lifecycleMigrationSeed, _ string) {
							event.outcome = "success"
						},
					},
					{
						name: "RepositoryRelationID",
						mutate: func(event *lifecycleSettledAuditEvent, _ lifecycleMigrationSeed, _ string) {
							event.repositoryID = strings.Repeat("0", 32)
						},
					},
					{
						name: "RecoveryPointRelationID",
						mutate: func(event *lifecycleSettledAuditEvent, _ lifecycleMigrationSeed, _ string) {
							event.recoveryPointID = strings.Repeat("f", 32)
						},
					},
					{
						name: "ItemCountColumn",
						mutate: func(event *lifecycleSettledAuditEvent, _ lifecycleMigrationSeed, _ string) {
							event.itemCount = 2
						},
					},
					{
						name: "PayloadObjectShape",
						mutate: func(event *lifecycleSettledAuditEvent, _ lifecycleMigrationSeed, _ string) {
							event.fields = `[]`
						},
					},
					{
						name: "PayloadKeyCount",
						mutate: func(event *lifecycleSettledAuditEvent, _ lifecycleMigrationSeed, _ string) {
							event.fields = `{"stage":"settled","status":"blocked","item_count":1}`
						},
					},
					{
						name: "PayloadStage",
						mutate: func(event *lifecycleSettledAuditEvent, _ lifecycleMigrationSeed, _ string) {
							event.fields = `{"stage":"completed","status":"blocked","item_count":1,"source":"` + event.source + `"}`
						},
					},
					{
						name: "PayloadStatus",
						mutate: func(event *lifecycleSettledAuditEvent, _ lifecycleMigrationSeed, _ string) {
							event.fields = `{"stage":"settled","status":"success","item_count":1,"source":"` + event.source + `"}`
						},
					},
					{
						name: "PayloadItemCount",
						mutate: func(event *lifecycleSettledAuditEvent, _ lifecycleMigrationSeed, _ string) {
							event.fields = `{"stage":"settled","status":"blocked","item_count":2,"source":"` + event.source + `"}`
						},
					},
					{
						name: "PayloadSourceRelationID",
						mutate: func(event *lifecycleSettledAuditEvent, _ lifecycleMigrationSeed, _ string) {
							event.fields = `{"stage":"settled","status":"blocked","item_count":1,"source":"` + strings.Repeat("e", 32) + `"}`
						},
					},
				}
				for index, nearMiss := range nearMisses {
					nearMiss := nearMiss
					t.Run(nearMiss.name, func(t *testing.T) {
						migrator, db := fixture.openAt(t, providerNativeVersionReferenceReasonMigrationVersion)
						seed := fixture.seedLifecycleMigrationBase(t, db, fmt.Sprintf("%x", 2+index), int64(77112+index))
						attemptID := strings.Repeat(fmt.Sprintf("%x", index+3), 32)
						insertLifecycleAttempt(t, fixture, db, attemptID, seed.PointID, "retention_expire", "blocked", "provider_worm")
						insertAuditCheckpoint(t, fixture, db, seed.Now)
						event := lifecycleSettledAuditEvent{
							action:          "repository_purge",
							outcome:         "blocked",
							repositoryID:    seed.RepositoryID,
							recoveryPointID: seed.PointID,
							itemCount:       1,
							source:          attemptID,
							fields:          `{"stage":"settled","status":"blocked","item_count":1,"source":"` + attemptID + `"}`,
							createdAt:       seed.Now,
						}
						nearMiss.mutate(&event, seed, attemptID)
						insertLifecycleAuditEvent(t, fixture, db, 1, 1, event)
						if err := migrator.Migrate(lifecycleEffectClaimAuditSlotMigrationVersion); err == nil {
							t.Fatalf("%s near-miss settled event unexpectedly migrated to v77", nearMiss.name)
						}
						assertDirtyLifecycleEffectClaimAuditSlotMigration(t, fixture, db)
						for _, table := range []string{lifecycleEffectClaimAuditSlotClaimsTable, lifecycleEffectClaimAuditSlotSlotsTable} {
							if databaseTableExists(t, db, fixture.engine, table) {
								t.Fatalf("%s near-miss rollback left v77 table %s", nearMiss.name, table)
							}
						}
					})
				}
				t.Run("ExactCandidateWinsOverEveryNearMiss", func(t *testing.T) {
					for index, nearMiss := range nearMisses {
						nearMiss := nearMiss
						t.Run(nearMiss.name, func(t *testing.T) {
							migrator, db := fixture.openAt(t, providerNativeVersionReferenceReasonMigrationVersion)
							seed := fixture.seedLifecycleMigrationBase(t, db, fmt.Sprintf("%x", 2+index), int64(77122+index))
							attemptID := strings.Repeat(fmt.Sprintf("%x", index+3), 32)
							insertLifecycleAttempt(t, fixture, db, attemptID, seed.PointID, "retention_expire", "blocked", "provider_worm")
							insertAuditCheckpoint(t, fixture, db, seed.Now)

							insertSettledAuditEvent(t, fixture, db, 1, 1, seed.RepositoryID, seed.PointID, attemptID, "blocked", "blocked", seed.Now)
							nearEvent := lifecycleSettledAuditEvent{
								action:          "repository_purge",
								outcome:         "blocked",
								repositoryID:    seed.RepositoryID,
								recoveryPointID: seed.PointID,
								itemCount:       1,
								source:          attemptID,
								fields:          `{"stage":"settled","status":"blocked","item_count":1,"source":"` + attemptID + `"}`,
								createdAt:       seed.Now.Add(time.Second),
							}
							nearMiss.mutate(&nearEvent, seed, attemptID)
							insertLifecycleAuditEvent(t, fixture, db, 1, 2, nearEvent)

							if err := migrator.Migrate(lifecycleEffectClaimAuditSlotMigrationVersion); err != nil {
								t.Fatalf("exact event plus %s near-miss migration: %v", nearMiss.name, err)
							}
							assertLifecycleSlotStatus(t, fixture, db, attemptID, "blocked")
							if count := lifecycleCount(t, fixture, db, lifecycleEffectClaimAuditSlotSlotsTable); count != 1 {
								t.Fatalf("exact event plus %s near-miss created %d slots, want 1", nearMiss.name, count)
							}
						})
					}
				})
			})

			t.Run("BlockedThenDeleted", func(t *testing.T) {
				migrator, db := fixture.openAt(t, providerNativeVersionReferenceReasonMigrationVersion)
				seed := fixture.seedLifecycleMigrationBase(t, db, "4", 77113)
				attemptID := strings.Repeat("4", 32)
				insertLifecycleAttempt(t, fixture, db, attemptID, seed.PointID, "retention_expire", "complete", "")
				insertLifecycleTombstoneForOperation(t, fixture, db, seed, "retention_expire", "provider_deleted")
				insertAuditCheckpoint(t, fixture, db, seed.Now)
				insertSettledAuditEvent(t, fixture, db, 1, 1, seed.RepositoryID, seed.PointID, attemptID, "blocked", "blocked", seed.Now.Add(-time.Second))
				insertSettledAuditEvent(t, fixture, db, 1, 2, seed.RepositoryID, seed.PointID, attemptID, "deleted", "success", seed.Now)
				if err := migrator.Migrate(lifecycleEffectClaimAuditSlotMigrationVersion); err != nil {
					t.Fatalf("blocked-to-deleted migration: %v", err)
				}
				assertLifecycleSlotStatus(t, fixture, db, attemptID, "blocked")
				assertLifecycleSlotStatus(t, fixture, db, attemptID, "deleted")
			})

			t.Run("IdentityConflictThenAlreadyAbsent", func(t *testing.T) {
				migrator, db := fixture.openAt(t, providerNativeVersionReferenceReasonMigrationVersion)
				seed := fixture.seedLifecycleMigrationBase(t, db, "5", 77114)
				attemptID := strings.Repeat("5", 32)
				insertLifecycleAttempt(t, fixture, db, attemptID, seed.PointID, "explicit_purge", "complete", "")
				insertLifecycleTombstoneForOperation(t, fixture, db, seed, "explicit_purge", "provider_already_absent")
				insertAuditCheckpoint(t, fixture, db, seed.Now)
				insertSettledAuditEvent(t, fixture, db, 1, 1, seed.RepositoryID, seed.PointID, attemptID, "identity_conflict", "blocked", seed.Now.Add(-time.Second))
				insertSettledAuditEvent(t, fixture, db, 1, 2, seed.RepositoryID, seed.PointID, attemptID, "already_absent", "success", seed.Now)
				if err := migrator.Migrate(lifecycleEffectClaimAuditSlotMigrationVersion); err != nil {
					t.Fatalf("identity-conflict-to-already-absent migration: %v", err)
				}
				assertLifecycleSlotStatus(t, fixture, db, attemptID, "identity_conflict")
				assertLifecycleSlotStatus(t, fixture, db, attemptID, "already_absent")
			})

			for _, order := range []struct {
				name   string
				first  string
				second string
			}{
				{name: "BlockedThenIdentityConflict", first: "blocked", second: "identity_conflict"},
				{name: "IdentityConflictThenBlocked", first: "identity_conflict", second: "blocked"},
			} {
				order := order
				t.Run(order.name, func(t *testing.T) {
					migrator, db := fixture.openAt(t, providerNativeVersionReferenceReasonMigrationVersion)
					seed := fixture.seedLifecycleMigrationBase(t, db, "6", 77115)
					attemptID := strings.Repeat("6", 32)
					insertLifecycleAttempt(t, fixture, db, attemptID, seed.PointID, "retention_expire", "complete", "")
					insertLifecycleTombstoneForOperation(t, fixture, db, seed, "retention_expire", "provider_deleted")
					insertAuditCheckpoint(t, fixture, db, seed.Now)
					insertSettledAuditEvent(t, fixture, db, 1, 1, seed.RepositoryID, seed.PointID, attemptID, order.first, "blocked", seed.Now.Add(-2*time.Second))
					insertSettledAuditEvent(t, fixture, db, 1, 2, seed.RepositoryID, seed.PointID, attemptID, order.second, "blocked", seed.Now.Add(-time.Second))
					insertSettledAuditEvent(t, fixture, db, 1, 3, seed.RepositoryID, seed.PointID, attemptID, "deleted", "success", seed.Now)
					if err := migrator.Migrate(lifecycleEffectClaimAuditSlotMigrationVersion); err != nil {
						t.Fatalf("%s migration: %v", order.name, err)
					}
					assertLifecycleSlotStatus(t, fixture, db, attemptID, "blocked")
					assertLifecycleSlotStatus(t, fixture, db, attemptID, "identity_conflict")
					assertLifecycleSlotStatus(t, fixture, db, attemptID, "deleted")
				})
			}

			t.Run("ExactDuplicatesAtDifferentTimesDeduplicateToMinimum", func(t *testing.T) {
				migrator, db := fixture.openAt(t, providerNativeVersionReferenceReasonMigrationVersion)
				seed := fixture.seedLifecycleMigrationBase(t, db, "7", 77116)
				attemptID := strings.Repeat("7", 32)
				insertLifecycleAttempt(t, fixture, db, attemptID, seed.PointID, "explicit_purge", "complete", "")
				insertLifecycleTombstoneForOperation(t, fixture, db, seed, "explicit_purge", "provider_already_absent")
				insertAuditCheckpoint(t, fixture, db, seed.Now)
				observedAt := seed.Now.Add(-2 * time.Second)
				terminalAt := seed.Now
				insertSettledAuditEvent(t, fixture, db, 1, 1, seed.RepositoryID, seed.PointID, attemptID, "identity_conflict", "blocked", observedAt.Add(time.Second))
				insertSettledAuditEvent(t, fixture, db, 1, 2, seed.RepositoryID, seed.PointID, attemptID, "identity_conflict", "blocked", observedAt)
				insertSettledAuditEvent(t, fixture, db, 1, 3, seed.RepositoryID, seed.PointID, attemptID, "already_absent", "success", terminalAt.Add(time.Second))
				insertSettledAuditEvent(t, fixture, db, 1, 4, seed.RepositoryID, seed.PointID, attemptID, "already_absent", "success", terminalAt)
				if err := migrator.Migrate(lifecycleEffectClaimAuditSlotMigrationVersion); err != nil {
					t.Fatalf("duplicate event migration: %v", err)
				}
				assertLifecycleSlotStatus(t, fixture, db, attemptID, "identity_conflict")
				assertLifecycleSlotStatus(t, fixture, db, attemptID, "already_absent")
				if count := lifecycleCount(t, fixture, db, lifecycleEffectClaimAuditSlotSlotsTable); count != 2 {
					t.Fatalf("duplicate event backfill created %d slots, want 2", count)
				}
				var gotObservationAt, gotTerminalAt time.Time
				if err := db.QueryRow(fixture.bind(`SELECT emitted_at FROM recovery_point_lifecycle_audit_slots WHERE attempt_id = ? AND status = 'identity_conflict'`), attemptID).Scan(&gotObservationAt); err != nil {
					t.Fatalf("read deduplicated observation timestamp: %v", err)
				}
				if err := db.QueryRow(fixture.bind(`SELECT emitted_at FROM recovery_point_lifecycle_audit_slots WHERE attempt_id = ? AND status = 'already_absent'`), attemptID).Scan(&gotTerminalAt); err != nil {
					t.Fatalf("read deduplicated terminal timestamp: %v", err)
				}
				if !gotObservationAt.Equal(observedAt) || !gotTerminalAt.Equal(terminalAt) {
					t.Fatalf("deduplicated timestamps observation=%v terminal=%v, want observation=%v terminal=%v", gotObservationAt, gotTerminalAt, observedAt, terminalAt)
				}
			})

			t.Run("TerminalBeforeObservationRejected", func(t *testing.T) {
				migrator, db := fixture.openAt(t, providerNativeVersionReferenceReasonMigrationVersion)
				seed := fixture.seedLifecycleMigrationBase(t, db, "8", 77117)
				attemptID := strings.Repeat("8", 32)
				insertLifecycleAttempt(t, fixture, db, attemptID, seed.PointID, "retention_expire", "complete", "")
				insertLifecycleTombstoneForOperation(t, fixture, db, seed, "retention_expire", "provider_deleted")
				insertAuditCheckpoint(t, fixture, db, seed.Now)
				insertSettledAuditEvent(t, fixture, db, 1, 1, seed.RepositoryID, seed.PointID, attemptID, "deleted", "success", seed.Now)
				insertSettledAuditEvent(t, fixture, db, 1, 2, seed.RepositoryID, seed.PointID, attemptID, "blocked", "blocked", seed.Now.Add(time.Second))
				if err := migrator.Migrate(lifecycleEffectClaimAuditSlotMigrationVersion); err == nil {
					t.Fatal("post-terminal observation unexpectedly migrated to v77")
				}
				assertDirtyLifecycleEffectClaimAuditSlotMigration(t, fixture, db)
				if databaseTableExists(t, db, fixture.engine, lifecycleEffectClaimAuditSlotSlotsTable) {
					t.Fatal("chronology rejection left audit-slot table")
				}
			})

			t.Run("MalformedFieldsRejected", func(t *testing.T) {
				migrator, db := fixture.openAt(t, providerNativeVersionReferenceReasonMigrationVersion)
				seed := fixture.seedLifecycleMigrationBase(t, db, "9", 77118)
				attemptID := strings.Repeat("9", 32)
				insertLifecycleAttempt(t, fixture, db, attemptID, seed.PointID, "retention_expire", "blocked", "provider_worm")
				insertAuditCheckpoint(t, fixture, db, seed.Now)
				insertSettledAuditEventWithFields(t, fixture, db, 1, 1, seed.RepositoryID, seed.PointID, attemptID, "blocked", "blocked", 1,
					`{"stage":"settled","status":"blocked","item_count":1,"source":"`+attemptID+`","extra":"near-miss"}`, seed.Now)
				if err := migrator.Migrate(lifecycleEffectClaimAuditSlotMigrationVersion); err == nil {
					t.Fatal("malformed settled event unexpectedly migrated to v77")
				}
				assertDirtyLifecycleEffectClaimAuditSlotMigration(t, fixture, db)
				if databaseTableExists(t, db, fixture.engine, lifecycleEffectClaimAuditSlotSlotsTable) {
					t.Fatal("malformed event rejection left audit-slot table")
				}
			})

			t.Run("SlotIDsAreIndependentAndWellFormed", func(t *testing.T) {
				migrator, db := fixture.openAt(t, providerNativeVersionReferenceReasonMigrationVersion)
				attemptIDs := []string{strings.Repeat("a", 32), strings.Repeat("b", 32)}
				for index, attemptID := range attemptIDs {
					seed := fixture.seedLifecycleMigrationBase(t, db, fmt.Sprintf("%x", 10+index), int64(77119+index))
					insertLifecycleAttempt(t, fixture, db, attemptID, seed.PointID, "retention_expire", "blocked", "provider_worm")
					if index == 0 {
						insertAuditCheckpoint(t, fixture, db, seed.Now)
					}
					insertSettledAuditEvent(t, fixture, db, 1, int64(index+1), seed.RepositoryID, seed.PointID, attemptID, "blocked", "blocked", seed.Now)
				}
				if err := migrator.Migrate(lifecycleEffectClaimAuditSlotMigrationVersion); err != nil {
					t.Fatalf("slot ID migration: %v", err)
				}
				rows, err := db.Query(fixture.bind(`SELECT id FROM recovery_point_lifecycle_audit_slots ORDER BY attempt_id`))
				if err != nil {
					t.Fatalf("query generated slot IDs: %v", err)
				}
				t.Cleanup(func() {
					if err := rows.Close(); err != nil {
						t.Errorf("close generated slot ID rows: %v", err)
					}
				})
				ids := make([]string, 0, len(attemptIDs))
				for rows.Next() {
					var id string
					if err := rows.Scan(&id); err != nil {
						t.Fatalf("scan generated slot ID: %v", err)
					}
					ids = append(ids, id)
				}
				if err := rows.Err(); err != nil {
					t.Fatalf("iterate generated slot IDs: %v", err)
				}
				if len(ids) != len(attemptIDs) || ids[0] == ids[1] {
					t.Fatalf("generated slot IDs=%v, want two distinct IDs", ids)
				}
				slotIDPattern := regexp.MustCompile(`^[0-9a-f]{32}$`)
				for _, id := range ids {
					if !slotIDPattern.MatchString(id) {
						t.Fatalf("generated slot ID %q is not lowercase 32-hex", id)
					}
				}
			})
		})
	}
}

func runLifecycleAuditSlotConcurrentOrder(t *testing.T, fixture migrationFixture, firstStatus, secondStatus string, wantSecondErr bool) {
	t.Helper()
	_, db := fixture.openAt(t, lifecycleEffectClaimAuditSlotMigrationVersion)
	db.SetMaxOpenConns(4)
	seed := fixture.seedLifecycleMigrationBase(t, db, "c", 77201)
	attemptID := strings.Repeat("d", 32)
	insertLifecycleAttempt(t, fixture, db, attemptID, seed.PointID, "retention_expire", "selected", "")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	firstConn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("open first %s concurrency connection: %v", fixture.engine, err)
	}
	t.Cleanup(func() {
		if err := firstConn.Close(); err != nil {
			t.Errorf("close first %s concurrency connection: %v", fixture.engine, err)
		}
	})
	secondConn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("open second %s concurrency connection: %v", fixture.engine, err)
	}
	t.Cleanup(func() {
		if err := secondConn.Close(); err != nil {
			t.Errorf("close second %s concurrency connection: %v", fixture.engine, err)
		}
	})

	begin := "BEGIN"
	if fixture.engine == "sqlite" {
		begin = "BEGIN DEFERRED"
	}
	insert := `INSERT INTO recovery_point_lifecycle_audit_slots
		(id, attempt_id, status, emitted_at, created_at) VALUES (?, ?, ?, ?, ?)`
	slotID := func(status string) string {
		switch status {
		case "blocked":
			return strings.Repeat("e", 32)
		case "deleted":
			return strings.Repeat("f", 32)
		default:
			t.Fatalf("unsupported concurrency status %q", status)
			return ""
		}
	}
	if _, err := firstConn.ExecContext(ctx, fixture.bind(begin)); err != nil {
		t.Fatalf("begin first %s transaction: %v", fixture.engine, err)
	}
	if _, err := firstConn.ExecContext(ctx, fixture.bind(insert), slotID(firstStatus), attemptID, firstStatus, seed.Now, seed.Now); err != nil {
		t.Fatalf("insert first %s slot: %v", firstStatus, err)
	}

	secondStarted := make(chan struct{})
	secondResult := make(chan error, 1)
	go func() {
		if _, err := secondConn.ExecContext(ctx, fixture.bind(begin)); err != nil {
			close(secondStarted)
			secondResult <- fmt.Errorf("begin second transaction: %w", err)
			return
		}
		close(secondStarted)
		_, err := secondConn.ExecContext(ctx, fixture.bind(insert), slotID(secondStatus), attemptID, secondStatus, seed.Now.Add(time.Second), seed.Now.Add(time.Second))
		if err == nil {
			_, err = secondConn.ExecContext(ctx, fixture.bind("COMMIT"))
		} else {
			_, _ = secondConn.ExecContext(ctx, fixture.bind("ROLLBACK"))
		}
		secondResult <- err
	}()
	<-secondStarted

	if _, err := firstConn.ExecContext(ctx, fixture.bind("COMMIT")); err != nil {
		t.Fatalf("commit first %s transaction: %v", fixture.engine, err)
	}
	secondErr := <-secondResult
	if wantSecondErr {
		if secondErr == nil {
			t.Fatalf("%s terminal-first concurrency unexpectedly admitted observational slot", fixture.engine)
		}
	} else if secondErr != nil {
		t.Fatalf("%s observational-first concurrency rejected terminal slot: %v", fixture.engine, secondErr)
	}

	var slotCount int
	if err := db.QueryRow(fixture.bind(`SELECT COUNT(*) FROM recovery_point_lifecycle_audit_slots WHERE attempt_id = ?`), attemptID).Scan(&slotCount); err != nil {
		t.Fatalf("count %s concurrent audit slots: %v", fixture.engine, err)
	}
	wantCount := 2
	if wantSecondErr {
		wantCount = 1
	}
	if slotCount != wantCount {
		t.Fatalf("%s concurrent %s/%s slot count=%d, want %d", fixture.engine, firstStatus, secondStatus, slotCount, wantCount)
	}
}

func insertLifecycleEffectClaim(t *testing.T, fixture migrationFixture, db *sql.DB, attemptID string, now time.Time) {
	t.Helper()
	fixture.mustExec(t, db, `INSERT INTO recovery_point_lifecycle_effect_claims
		(id, attempt_id, executor_id, execution_id, transition_revision, lease_id, lease_attempt_id,
		 lease_fence_token_hash, target_identity_digest, state, deadline_at, heartbeat_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, 1, ?, ?, ?, ?, 'in_flight', ?, ?, ?, ?)`,
		strings.Repeat("a", 32), attemptID, strings.Repeat("b", 32), strings.Repeat("c", 32),
		strings.Repeat("d", 32), strings.Repeat("e", 32), strings.Repeat("f", 64), strings.Repeat("0", 64),
		now.Add(time.Minute), now, now, now)
}

func insertLifecycleAuditSlot(t *testing.T, fixture migrationFixture, db *sql.DB, attemptID, status string, now time.Time) {
	t.Helper()
	slotID := strings.Repeat("a", 32)
	switch status {
	case "blocked":
		slotID = strings.Repeat("b", 32)
	case "identity_conflict":
		slotID = strings.Repeat("c", 32)
	case "deleted":
		slotID = strings.Repeat("d", 32)
	case "already_absent":
		slotID = strings.Repeat("e", 32)
	}
	fixture.mustExec(t, db, `INSERT INTO recovery_point_lifecycle_audit_slots
		(id, attempt_id, status, emitted_at, created_at) VALUES (?, ?, ?, ?, ?)`,
		slotID, attemptID, status, now, now)
}

func insertLifecycleAttempt(t *testing.T, fixture migrationFixture, db *sql.DB, attemptID, pointID, operation, phase, reason string) {
	t.Helper()
	var completedAt any
	if phase == "complete" {
		completedAt = time.Date(2026, 9, 3, 1, 2, 4, 0, time.UTC)
	}
	fixture.mustExec(t, db, `INSERT INTO recovery_point_lifecycle_attempts
		(id, recovery_point_id, operation, phase, transition_revision, blocked_reason, completed_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, 1, ?, ?, ?, ?)`, attemptID, pointID, operation, phase, reason, completedAt, time.Date(2026, 9, 3, 1, 2, 3, 0, time.UTC), time.Date(2026, 9, 3, 1, 2, 3, 0, time.UTC))
}
func assertDirtyLifecycleEffectClaimAuditSlotMigration(t *testing.T, fixture migrationFixture, db *sql.DB) {
	t.Helper()
	dirty, version, err := checkMigrationDirty(db, fixture.engine)
	if err != nil {
		t.Fatalf("read dirty v77 migration state: %v", err)
	}
	if version != int64(lifecycleEffectClaimAuditSlotMigrationVersion) || !dirty {
		t.Fatalf("migration version got=%d dirty=%v, want v77 dirty", version, dirty)
	}
}
func insertLifecycleTombstoneForOperation(t *testing.T, fixture migrationFixture, db *sql.DB, seed lifecycleMigrationSeed, operation, resultCode string) {
	t.Helper()
	fixture.mustExec(t, db, `UPDATE recovery_points SET state = 'expired' WHERE id = ?`, seed.PointID)
	fixture.mustExec(t, db, `INSERT INTO recovery_point_lifecycle_tombstones
		(recovery_point_id, repository_id, original_semantics, terminal_operation, terminal_state,
		 managed_history, deletion_receipt_digest, purged_at, result_code, created_at)
		VALUES (?, ?, 'native_snapshot', ?, 'expired', ?, ?, ?, ?, ?)`,
		seed.PointID, seed.RepositoryID, operation, true, strings.Repeat("a", 64), seed.Now, resultCode, seed.Now)
}

func insertAuditCheckpoint(t *testing.T, fixture migrationFixture, db *sql.DB, now time.Time) {
	t.Helper()
	fixture.mustExec(t, db, `INSERT INTO backup_asset_audit_checkpoints
		(segment_no, status, previous_checkpoint_hash, first_entry_hash, last_entry_hash, entry_count,
		 opened_at, closed_at, checkpoint_hash)
		VALUES (1, 'closed', '', '', '', 1, ?, ?, '')`, now, now)
}

type lifecycleSettledAuditEvent struct {
	action          string
	outcome         string
	repositoryID    string
	recoveryPointID string
	itemCount       int64
	source          string
	fields          string
	createdAt       time.Time
}

func insertLifecycleAuditEvent(t *testing.T, fixture migrationFixture, db *sql.DB, segmentNo, sequence int64, event lifecycleSettledAuditEvent) {
	t.Helper()
	fixture.mustExec(t, db, `INSERT INTO backup_asset_audit_events
		(segment_no, segment_sequence, action, outcome, repository_id, recovery_point_id, item_count,
		 fields_json, prev_hash, entry_hash, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		segmentNo, sequence, event.action, event.outcome, event.repositoryID, event.recoveryPointID,
		event.itemCount, event.fields, strings.Repeat("0", 64), strings.Repeat("1", 64), event.createdAt)
}

func insertSettledAuditEvent(t *testing.T, fixture migrationFixture, db *sql.DB, segmentNo, sequence int64, repositoryID, pointID, attemptID, status, outcome string, createdAt time.Time) {
	t.Helper()
	fields := `{"stage":"settled","status":"` + status + `","item_count":1,"source":"` + attemptID + `"}`
	insertSettledAuditEventWithFields(t, fixture, db, segmentNo, sequence, repositoryID, pointID, attemptID, status, outcome, 1, fields, createdAt)
}

func insertSettledAuditEventWithFields(t *testing.T, fixture migrationFixture, db *sql.DB, segmentNo, sequence int64, repositoryID, pointID, attemptID, status, outcome string, itemCount int64, fields string, createdAt time.Time) {
	t.Helper()
	insertLifecycleAuditEvent(t, fixture, db, segmentNo, sequence, lifecycleSettledAuditEvent{
		action:          "repository_purge",
		outcome:         outcome,
		repositoryID:    repositoryID,
		recoveryPointID: pointID,
		itemCount:       itemCount,
		source:          attemptID,
		fields:          fields,
		createdAt:       createdAt,
	})
}

func assertLifecycleSlotStatus(t *testing.T, fixture migrationFixture, db *sql.DB, attemptID, status string) {
	t.Helper()
	var count int
	if err := db.QueryRow(fixture.bind(`SELECT COUNT(*) FROM recovery_point_lifecycle_audit_slots WHERE attempt_id = ? AND status = ?`), attemptID, status).Scan(&count); err != nil {
		t.Fatalf("count v77 %s slot: %v", status, err)
	}
	if count != 1 {
		t.Fatalf("v77 %s slot count=%d, want 1", status, count)
	}
}

func lifecycleCount(t *testing.T, fixture migrationFixture, db *sql.DB, table string) int {
	t.Helper()
	var count int
	if err := db.QueryRow(fixture.bind(`SELECT COUNT(*) FROM ` + table)).Scan(&count); err != nil {
		t.Fatalf("count %s rows: %v", table, err)
	}
	return count
}

func migrationColumnNames(t *testing.T, fixture migrationFixture, db *sql.DB, table string) []string {
	t.Helper()
	if fixture.engine == "sqlite" {
		return sqliteColumnNames(t, db, table)
	}
	return postgresColumnNames(t, db, table)
}

func sameStringSet(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	gotCopy := append([]string(nil), got...)
	wantCopy := append([]string(nil), want...)
	for _, values := range [][]string{gotCopy, wantCopy} {
		for index := range values {
			values[index] = strings.ToLower(values[index])
		}
		// Sorting is intentionally local so callers retain their catalog order.
		for i := 0; i < len(values); i++ {
			for j := i + 1; j < len(values); j++ {
				if values[j] < values[i] {
					values[i], values[j] = values[j], values[i]
				}
			}
		}
	}
	return fmt.Sprint(gotCopy) == fmt.Sprint(wantCopy)
}

func assertLifecycleEffectClaimAuditSlotObjectsAbsent(t *testing.T, fixture migrationFixture, db *sql.DB) {
	t.Helper()
	for _, relation := range []struct {
		name string
		kind string
	}{
		{lifecycleEffectClaimAuditSlotClaimsTable, "table"},
		{lifecycleEffectClaimAuditSlotSlotsTable, "table"},
		{"idx_recovery_point_lifecycle_effect_claims_attempt", "index"},
		{"idx_recovery_point_lifecycle_effect_claims_state_deadline", "index"},
		{"idx_recovery_point_lifecycle_audit_slots_attempt_status", "index"},
		{"idx_recovery_point_lifecycle_audit_slots_terminal", "index"},
	} {
		exists, err := migrationRelationExists(db, fixture.engine, relation.name, relation.kind)
		if err != nil {
			t.Fatalf("check absent v77 %s %s: %v", relation.kind, relation.name, err)
		}
		if exists {
			t.Fatalf("v77 down retained %s %s", relation.kind, relation.name)
		}
	}
	for _, trigger := range []struct {
		table string
		name  string
	}{
		{lifecycleEffectClaimAuditSlotClaimsTable, "trg_recovery_point_lifecycle_effect_claims_transition"},
		{lifecycleEffectClaimAuditSlotClaimsTable, "trg_recovery_point_lifecycle_effect_claims_no_delete"},
		{lifecycleEffectClaimAuditSlotSlotsTable, "trg_recovery_point_lifecycle_audit_slots_transition"},
		{lifecycleEffectClaimAuditSlotSlotsTable, "trg_recovery_point_lifecycle_audit_slots_immutable_update"},
		{lifecycleEffectClaimAuditSlotSlotsTable, "trg_recovery_point_lifecycle_audit_slots_immutable_delete"},
		{"schema_migrations", lifecycleEffectClaimAuditSlotAdmissionTrigger},
	} {
		if fixture.recoveryTriggerExists(t, db, trigger.table, trigger.name) {
			t.Fatalf("v77 down retained trigger %s", trigger.name)
		}
	}
	if fixture.engine == "postgres" {
		for _, function := range []string{
			"recovery_point_lifecycle_effect_claim_transition_guard",
			"recovery_point_lifecycle_effect_claim_delete_guard",
			"recovery_point_lifecycle_audit_slot_transition_guard",
			"recovery_point_lifecycle_audit_slot_immutable_guard",
			lifecycleEffectClaimAuditSlotAdmissionFunction,
		} {
			var count int
			if err := db.QueryRow(`SELECT COUNT(*) FROM pg_catalog.pg_proc AS procedure
				JOIN pg_catalog.pg_namespace AS namespace ON namespace.oid = procedure.pronamespace
				WHERE namespace.nspname = current_schema() AND procedure.proname = $1`, function).Scan(&count); err != nil {
				t.Fatalf("check absent v77 function %s: %v", function, err)
			}
			if count != 0 {
				t.Fatalf("v77 down retained function %s", function)
			}
		}
	}
}

func dropLifecycleEffectClaimAuditSlotAdmission(t *testing.T, fixture migrationFixture, db *sql.DB) {
	t.Helper()
	query := `DROP TRIGGER IF EXISTS trg_recovery_point_lifecycle_effect_claim_audit_slot_downgrade_admission`
	if fixture.engine == "postgres" {
		query += ` ON schema_migrations`
	}
	fixture.mustExec(t, db, query)
}

func executeLifecycleEffectClaimAuditSlotDown(t *testing.T, fixture migrationFixture, db *sql.DB) error {
	t.Helper()
	path := "migrations/sqlite/000077_lifecycle_effect_claim_audit_slot.down.sql"
	migrationFS := sqliteMigrationsFS
	if fixture.engine == "postgres" {
		path = "migrations/postgres/000077_lifecycle_effect_claim_audit_slot.down.sql"
		migrationFS = postgresMigrationsFS
	}
	script, err := migrationFS.ReadFile(path)
	if err != nil {
		return err
	}
	_, err = db.Exec(string(script))
	return err
}
