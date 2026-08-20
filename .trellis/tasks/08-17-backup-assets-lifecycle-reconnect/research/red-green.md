# RED/GREEN Evidence

## Task 1 — Managed Task retention delegates exact RecoveryPoint IDs

### RED

- UTC timestamp: `2026-08-17T04:10:38Z`
- Command: `cd backend && go test ./internal/task -run '^TestManagedTaskRetentionDelegatesExactRecoveryPointIDs$' -count=1`
- Exit status: `1`
- Expected failure category: compile-time contract RED caused only by the missing `ManagedRecoveryPointRetentionRequest` and `Manager.SetManagedRecoveryPointRetention` seam.
- Concise output:

  ```text
  internal/task/retention_test.go:22:10: undefined: ManagedRecoveryPointRetentionRequest
  internal/task/retention_test.go:25:99: undefined: ManagedRecoveryPointRetentionRequest
  internal/task/retention_test.go:119:10: manager.SetManagedRecoveryPointRetention undefined
  FAIL xirang/backend/internal/task [build failed]
  ```

### GREEN

- UTC timestamp: `2026-08-17T04:12:02Z`
- Command: `cd backend && go test ./internal/task -run '^TestManagedTaskRetentionDelegatesExactRecoveryPointIDs$' -count=1`
- Exit status: `0`
- Result: the exact selector passes after adding the narrow managed-retention port and deterministic opaque-ID delegation.
- Concise output: `ok xirang/backend/internal/task 0.012s`

### Adjacent retention selector

- UTC timestamp: `2026-08-17T04:12:19Z`
- Command: `cd backend && go test ./internal/task -run '^(TestManagedTaskRetentionDelegatesExactRecoveryPointIDs|TestRetention|TestPristine)' -count=1`
- Exit status: `0`
- Result: the new managed-delegation contract and adjacent retention/pristine compatibility coverage pass together.
- Concise output: `ok xirang/backend/internal/task 0.023s`

## Task 1 review fix — Unsafe exact authority inputs

### RED

- UTC timestamp: `2026-08-17T04:27:49Z`
- Command: `cd backend && go test ./internal/task -run '^TestManagedTaskRetentionRejectsUnsafeExactAuthorityInputs$' -count=1`
- Exit status: `1`
- Expected failure category: behavioral RED proving the current implementation invokes the managed authority for an empty exact set and reads the mutable `RepositoryID()` interface twice.
- Concise output:

  ```text
  empty_exact_set_is_a_managed_no-op: managed authority calls=1, want 0
  managed_authority_error_fails_closed: RepositoryID calls=2, want exactly 1
  validated_repository_ID_is_snapshotted_once: RepositoryID calls=2, want exactly 1
  FAIL xirang/backend/internal/task
  ```

### GREEN

- UTC timestamp: `2026-08-17T04:28:25Z`
- Command: `cd backend && go test ./internal/task -run '^TestManagedTaskRetentionRejectsUnsafeExactAuthorityInputs$' -count=1`
- Exit status: `0`
- Result: empty exact sets are managed no-ops, unsafe inputs stay fail-closed, and the validated repository ID is read and reused exactly once.
- Concise output: `ok xirang/backend/internal/task 0.033s`

### Review-fix verification

- UTC timestamp: `2026-08-17T04:29:38Z`
- Command: `cd backend && go test ./internal/task -run '^TestManagedTaskRetentionDelegatesExactRecoveryPointIDs$' -count=1`
- Exit status: `0`
- Result: the original exact-ID delegation selector passes with persisted RecoveryPoint IDs and one distinct, exactly-once-closed lineage session per executor.
- Concise output: `ok xirang/backend/internal/task 0.012s`

- UTC timestamp: `2026-08-17T04:29:47Z`
- Command: `cd backend && go test ./internal/task -run '^(TestManagedTaskRetentionDelegatesExactRecoveryPointIDs|TestRetention|TestPristine)' -count=1`
- Exit status: `0`
- Result: managed delegation and adjacent retention/pristine compatibility coverage pass together after the review fix.
- Concise output: `ok xirang/backend/internal/task 0.023s`

## Task 1 spec re-review — Missing authority must short-circuit exact session data

### RED

- UTC timestamp: `2026-08-17T04:33:08Z`
- Command: `cd backend && go test ./internal/task -run 'Retention|Pristine' -count=1`
- Exit status: `1`
- Expected failure category: existing regression panic proving a nil managed authority does not fail closed before dereferencing minimal exact-session data.
- Concise output:

  ```text
  FAIL: TestManagedResticRetentionBlocksForgetPruneBeforeCredentialAndSSH
  panic: runtime error: invalid memory address or nil pointer dereference
  legacyLineageSessionFake.RepositoryID
  Manager.delegateManagedRetention ... retention.go:288
  FAIL xirang/backend/internal/task
  ```

### GREEN

- UTC timestamp: `2026-08-17T04:33:43Z`
- Command: `cd backend && go test ./internal/task -run 'Retention|Pristine' -count=1`
- Exit status: `0`
- Result: missing managed authority fails closed before any exact-session data access; the broader retention/pristine selector passes without panic.
- Concise output: `ok xirang/backend/internal/task 0.082s`

### Spec re-review verification

- UTC timestamp: `2026-08-17T04:34:01Z`
- Command: `cd backend && go test ./internal/task -run '^TestManagedTaskRetentionDelegatesExactRecoveryPointIDs$' -count=1`
- Exit status: `0`
- Concise output: `ok xirang/backend/internal/task 0.011s`

- UTC timestamp: `2026-08-17T04:34:09Z`
- Command: `cd backend && go test ./internal/task -run '^TestManagedTaskRetentionRejectsUnsafeExactAuthorityInputs$' -count=1`
- Exit status: `0`
- Concise output: `ok xirang/backend/internal/task 0.032s`

- UTC timestamp: `2026-08-17T04:34:17Z`
- Command: `cd backend && go test ./internal/task -run '^(TestManagedTaskRetentionDelegatesExactRecoveryPointIDs|TestRetention|TestPristine)' -count=1`
- Exit status: `0`
- Concise output: `ok xirang/backend/internal/task 0.023s`

## Task 2 — Paired `000070_backup_asset_lifecycle` schema and models

### RED

- UTC timestamp: `2026-08-17T04:47:19Z`
- Command: `cd backend && go test ./internal/database -run '^TestBackupAssetMigration070(SQLite|PairedFiles|UsedDownAdmissionSQLite)$' -count=1`
- Exit status: `1`
- Expected failure category: compile-time lifecycle contract RED caused only by the missing centralized `000070` GORM models; the new migration tests also target the absent paired `000070_backup_asset_lifecycle` files and schema.
- Concise output:

  ```text
  internal/database/backup_asset_migrations_integration_test.go:3582:48: undefined: model.BackupRetentionPolicy
  internal/database/backup_asset_migrations_integration_test.go:3583:48: undefined: model.RecoveryPointHold
  internal/database/backup_asset_migrations_integration_test.go:3584:48: undefined: model.RecoveryPointLifecycleAttempt
  internal/database/backup_asset_migrations_integration_test.go:3585:48: undefined: model.RecoveryPointLifecycleTombstone
  internal/database/backup_asset_migrations_integration_test.go:3586:48: undefined: model.BackupRepositoryImportCandidate
  internal/database/backup_asset_migrations_integration_test.go:3587:48: undefined: model.BackupAssetPurgePlan
  internal/database/backup_asset_migrations_integration_test.go:3588:48: undefined: model.BackupAssetPurgePlanItem
  internal/database/backup_asset_migrations_integration_test.go:3589:48: undefined: model.BackupAssetConfigImportRef
  FAIL xirang/backend/internal/database [build failed]
  ```

### GREEN

- UTC timestamp: `2026-08-17T04:59:48Z`
- Command: `cd backend && go test ./internal/database -run '^TestBackupAssetMigration070(SQLite|PairedFiles|UsedDownAdmissionSQLite)$' -count=1`
- Exit status: `0`
- Result: the same exact selector passes on the final files with paired definitions, raw-fence exclusion, SQLite apply/model/constraint/tombstone/pristine-down coverage, preservation of live 000069 facts and lease references, and every lifecycle fact-family metadata admission guard.
- Concise output: `ok xirang/backend/internal/database 1.324s`

### Adjacent model/domain/lease and migration regression selectors

- UTC timestamp: `2026-08-17T05:00:00Z`
- Command: `cd backend && go test ./internal/model -run '^TestBackupAssetLifecycle' -count=1`
- Exit status: `0`
- Result: sensitive hold reasons and import locator/evidence encrypt at rest, decrypt on load, remain excluded from JSON, and fail closed on missing-key or malformed-ciphertext failures.
- Concise output: `ok xirang/backend/internal/model 0.046s`

- UTC timestamp: `2026-08-17T05:00:00Z`
- Command: `cd backend && go test ./internal/backupasset -run '^(TestLifecycleClosedEnumsAndValidators|TestRetentionWorkerLeaseHolderIsValid)$' -count=1`
- Exit status: `0`
- Result: lifecycle closed enums/validators and the reused `retention_worker` lease-holder contract pass.
- Concise output: `ok xirang/backend/internal/backupasset 0.005s`

- UTC timestamp: `2026-08-17T05:00:01Z`
- Command: `cd backend && go test ./internal/database -run '^TestBackupAssetMigration069(SQLite|PairedFiles)$' -count=1`
- Exit status: `0`
- Result: the prior paired 000069 SQLite apply/down-admission behavior remains green.
- Concise output: `ok xirang/backend/internal/database 7.570s`

### Required PostgreSQL fixture gate

- UTC timestamp: `2026-08-17T04:59:32Z`
- Command: `cd backend && REQUIRE_POSTGRES_MIGRATION_TEST=1 TEST_POSTGRES_DSN="$TEST_POSTGRES_DSN" go test ./internal/database -run '^TestBackupAssetMigration070(Postgres|UsedDownAdmissionPostgres)$' -count=1`
- Exit status: `1`
- Disposition: failed closed because the controller environment does not provide the required `TEST_POSTGRES_DSN`; no DSN was invented, no fixture was started or replaced, and PostgreSQL GREEN is not claimed.
- Concise output:

  ```text
  TestBackupAssetMigration070Postgres: TEST_POSTGRES_DSN is required when REQUIRE_POSTGRES_MIGRATION_TEST=1
  TestBackupAssetMigration070UsedDownAdmissionPostgres: TEST_POSTGRES_DSN is required when REQUIRE_POSTGRES_MIGRATION_TEST=1
  FAIL xirang/backend/internal/database
  ```

## Task 2 spec review fixes — Policy snapshots, enum reuse, and model isolation

### Policy revision snapshot RED

- UTC timestamp: `2026-08-17T05:12:42Z`
- Command: `cd backend && go test ./internal/database -run '^TestBackupAssetMigration070PolicyRevisionSnapshotSQLite$' -count=1`
- Exit status: `1`
- Expected failure category: schema-contract RED proving the composite `(policy_id, policy_revision)` foreign key pins the current policy row at the attempt's old snapshot revision.
- Concise output: `execute sqlite migration assertion query: FOREIGN KEY constraint failed`

### Closed enum/reuse RED

- UTC timestamp: `2026-08-17T05:13:09Z`
- Command: `cd backend && go test ./internal/backupasset -run '^TestLifecycleClosedEnumsAndValidators$' -count=1`
- Exit status: `1`
- Expected failure category: compile-time contract RED for missing purpose-exact status/reason validators before removing the duplicate hold-record state enum.
- Concise output:

  ```text
  undefined: ValidateRetentionPolicyStatus
  undefined: ValidateRecoveryPointHoldRecordState
  undefined: ValidateLifecycleBlockedReason
  undefined: ValidateImportReviewState
  undefined: ValidatePurgePlanStatus
  FAIL xirang/backend/internal/backupasset [build failed]
  ```

### Deterministic model-test RED

- UTC timestamp: `2026-08-17T05:13:13Z`
- Command: `cd backend && go test ./internal/model -run '^TestBackupAssetLifecycle' -count=2`
- Exit status: `1`
- Expected failure category: test-isolation RED proving the fixed shared-memory SQLite DSN leaks the first repetition's rows into the second.
- Concise output: `UNIQUE constraint failed: recovery_point_holds.id`

### Review-fix GREEN

- UTC timestamp: `2026-08-17T05:13:58Z`
- Command: `cd backend && go test ./internal/database -run '^TestBackupAssetMigration070PolicyRevisionSnapshotSQLite$' -count=1`
- Exit status: `0`
- Result: the current policy advances from revision 1 to 2 after an attempt snapshots revision 1, while the attempt's copied revision remains 1.
- Concise output: `ok xirang/backend/internal/database 0.150s`

- UTC timestamp: `2026-08-17T05:14:02Z`
- Command: `cd backend && go test ./internal/backupasset -run '^TestLifecycleClosedEnumsAndValidators$' -count=1`
- Exit status: `0`
- Result: hold records reuse `HoldState`, accept only `active`/`released`, reject `none`, and all requested lifecycle statuses/reasons have exhaustive closed validators.
- Concise output: `ok xirang/backend/internal/backupasset 0.005s`

- UTC timestamp: `2026-08-17T05:14:03Z`
- Command: `cd backend && go test ./internal/model -run '^TestBackupAssetLifecycle' -count=2`
- Exit status: `0`
- Result: each repetition uses and closes an isolated SQLite database.
- Concise output: `ok xirang/backend/internal/model 0.092s`

### Final review-fix verification

- UTC timestamp: `2026-08-17T05:14:08Z`
- Command: `cd backend && go test ./internal/database -run '^TestBackupAssetMigration070(SQLite|PairedFiles|UsedDownAdmissionSQLite)$' -count=1`
- Exit status: `0`
- Concise output: `ok xirang/backend/internal/database 1.445s`

- UTC timestamp: `2026-08-17T05:14:18Z`
- Command: `cd backend && go test ./internal/backupasset -run '^(TestLifecycleClosedEnumsAndValidators|TestRetentionWorkerLeaseHolderIsValid)$' -count=1`
- Exit status: `0`
- Concise output: `ok xirang/backend/internal/backupasset 0.005s`

- UTC timestamp: `2026-08-17T05:14:19Z`
- Command: `cd backend && go test ./internal/database -run '^TestBackupAssetMigration069(SQLite|PairedFiles)$' -count=1`
- Exit status: `0`
- Concise output: `ok xirang/backend/internal/database 7.552s`

- UTC timestamp: `2026-08-17T05:14:31Z`
- Command: `cd backend && go test ./internal/model ./internal/backupasset ./internal/database -count=1`
- Exit status: `0`
- Concise output: `ok` for model (`0.105s`), backupasset (`0.554s`), and database (`24.100s`).

### Required PostgreSQL fixture gate after review fixes

- UTC timestamp: `2026-08-17T05:15:01Z`
- Command: `cd backend && REQUIRE_POSTGRES_MIGRATION_TEST=1 TEST_POSTGRES_DSN="$TEST_POSTGRES_DSN" go test ./internal/database -run '^TestBackupAssetMigration070(Postgres|UsedDownAdmissionPostgres)$' -count=1`
- Exit status: `1`
- Disposition: failed closed because `TEST_POSTGRES_DSN` remains absent; PostgreSQL GREEN is not claimed.
- Concise output:

  ```text
  TestBackupAssetMigration070Postgres: TEST_POSTGRES_DSN is required when REQUIRE_POSTGRES_MIGRATION_TEST=1
  TestBackupAssetMigration070UsedDownAdmissionPostgres: TEST_POSTGRES_DSN is required when REQUIRE_POSTGRES_MIGRATION_TEST=1
  FAIL xirang/backend/internal/database
  ```

## Task 2 quality review fix — Exact tombstone terminal facts

### RED

- UTC timestamp: `2026-08-17T05:29:23Z`
- Command: `cd backend && go test ./internal/database -run '^TestBackupAssetMigration070TombstoneTerminalFactsSQLite$' -count=1`
- Exit status: `1`
- Expected failure category: schema-contract RED proving the tombstone CHECK admits an invalid member of the exhaustive operation/semantics/state/time/digest/result cross-product.
- Concise output: `sqlite invalid migration row unexpectedly succeeded`

### GREEN

- UTC timestamp: `2026-08-17T05:29:42Z`
- Command: `cd backend && go test ./internal/database -run '^TestBackupAssetMigration070TombstoneTerminalFactsSQLite$' -count=1`
- Exit status: `0`
- Result: every invalid terminal-fact product is rejected; mutable retirement requires `mutable_head`, while expiry/purge requires a non-null valid deletion-receipt digest and excludes retirement timestamps.
- Concise output: `ok xirang/backend/internal/database 0.163s`

### Final quality-review verification

- UTC timestamp: `2026-08-17T05:29:51Z`
- Command: `cd backend && go test ./internal/database -run '^TestBackupAssetMigration070(SQLite|PairedFiles|UsedDownAdmissionSQLite)$' -count=1`
- Exit status: `0`
- Concise output: `ok xirang/backend/internal/database 1.450s`

- UTC timestamp: `2026-08-17T05:29:54Z`
- Command: `cd backend && go test ./internal/model -run '^TestBackupAssetLifecycle' -count=2`
- Exit status: `0`
- Concise output: `ok xirang/backend/internal/model 0.108s`

- UTC timestamp: `2026-08-17T05:29:54Z`
- Command: `cd backend && go test ./internal/backupasset -run '^(TestLifecycleClosedEnumsAndValidators|TestRetentionWorkerLeaseHolderIsValid)$' -count=1`
- Exit status: `0`
- Concise output: `ok xirang/backend/internal/backupasset 0.005s`

- UTC timestamp: `2026-08-17T05:29:55Z`
- Command: `cd backend && go test ./internal/database -run '^TestBackupAssetMigration069(SQLite|PairedFiles)$' -count=1`
- Exit status: `0`
- Concise output: `ok xirang/backend/internal/database 7.657s`

- UTC timestamp: `2026-08-17T05:30:10Z`
- Command: `cd backend && go test ./internal/model ./internal/backupasset ./internal/database -count=1`
- Exit status: `0`
- Concise output: `ok` for model (`0.102s`), backupasset (`0.555s`), and database (`24.027s`).

### Required PostgreSQL fixture gate after tombstone fix

- UTC timestamp: `2026-08-17T05:30:41Z`
- Command: `cd backend && REQUIRE_POSTGRES_MIGRATION_TEST=1 TEST_POSTGRES_DSN="$TEST_POSTGRES_DSN" go test ./internal/database -run '^TestBackupAssetMigration070(Postgres|UsedDownAdmissionPostgres)$' -count=1`
- Exit status: `1`
- Disposition: failed closed because `TEST_POSTGRES_DSN` remains absent; PostgreSQL GREEN is not claimed.

## Task 2 quality re-review fix — Shared tombstone contract wiring

### RED

- UTC timestamp: `2026-08-17T05:37:05Z`
- Command: `cd backend && go test ./internal/database -run '^TestBackupAssetMigration070SharedContractRegistersTombstoneTerminalFacts$' -count=1`
- Exit status: `1`
- Expected failure category: compile-time runner-wiring RED proving no executable shared 000070 check registry exists for the tombstone terminal-fact contract.
- Concise output: `undefined: backupAssetMigration070SharedChecks`

### GREEN

- UTC timestamp: `2026-08-17T05:37:28Z`
- Command: `cd backend && go test ./internal/database -run '^TestBackupAssetMigration070SharedContractRegistersTombstoneTerminalFacts$' -count=1`
- Exit status: `0`
- Result: the non-brittle shared registry contains a non-nil `TombstoneTerminalFacts` runner, and `runBackupAssetMigration070Contract` executes every registered check for either dialect fixture.
- Concise output: `ok xirang/backend/internal/database 0.051s`

### Final shared-runner verification

- UTC timestamp: `2026-08-17T05:37:39Z`
- Command: `cd backend && go test ./internal/database -run '^TestBackupAssetMigration070(SQLite|PairedFiles|UsedDownAdmissionSQLite)$' -count=1`
- Exit status: `0`
- Concise output: `ok xirang/backend/internal/database 1.585s`

- UTC timestamp: `2026-08-17T05:37:41Z`
- Command: `cd backend && go test ./internal/database -run '^TestBackupAssetMigration070(PolicyRevisionSnapshot|TombstoneTerminalFacts)SQLite$' -count=1`
- Exit status: `0`
- Concise output: `ok xirang/backend/internal/database 0.268s`

- UTC timestamp: `2026-08-17T05:37:42Z`
- Command: `cd backend && go test ./internal/database -run '^TestBackupAssetMigration069(SQLite|PairedFiles)$' -count=1`
- Exit status: `0`
- Concise output: `ok xirang/backend/internal/database 7.985s`

- UTC timestamp: `2026-08-17T05:37:59Z`
- Command: `cd backend && go test ./internal/model ./internal/backupasset ./internal/database -count=1`
- Exit status: `0`
- Concise output: `ok` for model (`0.106s`), backupasset (`0.566s`), and database (`25.288s`).

### Required PostgreSQL fixture gate after shared wiring

- UTC timestamp: `2026-08-17T05:38:30Z`
- Command: `cd backend && REQUIRE_POSTGRES_MIGRATION_TEST=1 TEST_POSTGRES_DSN="$TEST_POSTGRES_DSN" go test ./internal/database -run '^TestBackupAssetMigration070(Postgres|UsedDownAdmissionPostgres)$' -count=1`
- Exit status: `1`
- Disposition: failed closed because `TEST_POSTGRES_DSN` remains absent; the executable registry proves the tombstone check is wired into the PostgreSQL contract, but PostgreSQL GREEN is not claimed.

## Task 2 approved disposable PostgreSQL fixture verification

### Fixture lifecycle

- Image: `postgres:17-alpine`, digest `sha256:18cfe3ef5e6815560c98237d6216d1e5119702fb0f3894c8785dd58b8bbe5d73`
- Container: `xirang-task2-pg-070-20260817-0539-17045`
- Isolation: loopback-only `127.0.0.1:32784`, no host volume, random task-local credentials, `--rm` automatic removal.
- Readiness: ready after 3 bounded one-second polls.
- Credential handling: password and DSN remained only in the live test shell and are not recorded here.

### Required PostgreSQL GREEN

- UTC timestamp: `2026-08-17T06:20:09Z`
- Command: `cd backend && REQUIRE_POSTGRES_MIGRATION_TEST=1 TEST_POSTGRES_DSN="$TEST_POSTGRES_DSN" go test ./internal/database -run '^TestBackupAssetMigration070(Postgres|UsedDownAdmissionPostgres)$' -count=1`
- Exit status: `0`
- Result: both the shared PostgreSQL 000070 contract and used-down admission contract pass against the approved disposable fixture, including policy snapshot and exhaustive tombstone terminal-fact wiring.
- Concise output: `ok xirang/backend/internal/database 36.454s`

### Adjacent regressions

- UTC timestamp: `2026-08-17T06:21:33Z`
- Command: `cd backend && go test ./internal/database -run '^TestBackupAssetMigration070(SQLite|PairedFiles|UsedDownAdmissionSQLite)$' -count=1`
- Exit status: `0`
- Concise output: `ok xirang/backend/internal/database 1.535s`

- UTC timestamp: `2026-08-17T06:21:36Z`
- Command: `cd backend && go test ./internal/database -run '^TestBackupAssetMigration069(SQLite|PairedFiles)$' -count=1`
- Exit status: `0`
- Concise output: `ok xirang/backend/internal/database 7.543s`

- UTC timestamp: `2026-08-17T06:22:10Z`
- Command: `cd backend && TEST_POSTGRES_DSN="$TEST_POSTGRES_DSN" go test ./internal/model ./internal/backupasset ./internal/database -count=1`
- Exit status: interrupted after the announced 5-minute optional broader-sweep bound (`database` elapsed `302.387s`).
- Disposition: model (`0.071s`) and backupasset (`0.483s`) passed; the PG-enabled full database sweep remained active and continued advancing through expected negative constraint/mutation cases but did not complete within the bounded window. This is recorded as incomplete, not GREEN and not a product failure; the exact required PostgreSQL gate above is complete and GREEN.

- UTC timestamp: `2026-08-17T06:27:31Z`
- Command: `cd backend && go test ./internal/model ./internal/backupasset ./internal/database -count=1`
- Exit status: `0`
- Concise output: `ok` for model (`0.064s`), backupasset (`0.481s`), and database (`23.958s`).

### Cleanup proof

- UTC timestamp: `2026-08-17T06:28:16Z`
- Action: stopped the unique fixture container, relied on `--rm`, and unset the task-local DSN and credential variables.
- Result: removal completed on the first bounded verification poll; an independent `docker inspect` check returned absent (`fixture_removal_verified=true`).

## Task 3 — Versioned policy and hold services

### Policy rule contract RED

- UTC timestamp: `2026-08-17T06:44:09Z`
- Command: `cd backend && go test ./internal/backupasset/retention -run '^TestPolicyRulesCanonicalizationAndDigest$' -count=1`
- Exit status: `1`
- Expected failure category: compile-time contract RED caused only by the absent v1 age/count/calendar policy-rule types, canonicalizer, parser, and digest API referenced by the newly added focused test.
- Concise output:

  ```text
  undefined: PolicyRules
  undefined: PolicyRulesVersion1
  undefined: AgeRule, CountRule, CalendarRule
  undefined: CanonicalizePolicyRules, ParsePolicyRules
  FAIL xirang/backend/internal/backupasset/retention [build failed]
  ```

### Policy rule contract GREEN

- UTC timestamp: `2026-08-17T06:45:10Z`
- Command: `cd backend && go test ./internal/backupasset/retention -run '^TestPolicyRulesCanonicalizationAndDigest$' -count=1`
- Exit status: `0`
- Result: the identical selector passes with a strict v1 age/count/calendar-only rule document, deterministic calendar ordering, exact canonical JSON, stable SHA-256 digest, and canonical parse round trip.
- Concise output: `ok xirang/backend/internal/backupasset/retention 0.004s`

### Policy CRUD/CAS RED

- UTC timestamp: `2026-08-17T06:46:19Z`
- Command: `cd backend && go test ./internal/backupasset/retention -run '^TestPolicyServiceVersionedCRUDUsesAdminAndRevisionCAS$' -count=1`
- Exit status: `1`
- Expected failure category: compile-time contract RED caused only by the absent policy service dependencies, create/update/delete requests, and service methods referenced by the focused CRUD/CAS test.
- Concise output:

  ```text
  undefined: NewPolicyService, PolicyServiceDependencies
  undefined: CreatePolicyRequest, UpdatePolicyRequest, DeletePolicyRequest
  FAIL xirang/backend/internal/backupasset/retention [build failed]
  ```

- Initial implementation rerun at `2026-08-17T06:47:41Z` remained RED because duplicate-scope rejection allocated a new opaque ID before the transaction proved the conflict; the same focused test exposed the ordering defect. ID allocation was moved after the locked active-scope check.

### Policy CRUD/CAS GREEN

- UTC timestamp: `2026-08-17T06:47:56Z`
- Command: `cd backend && go test ./internal/backupasset/retention -run '^TestPolicyServiceVersionedCRUDUsesAdminAndRevisionCAS$' -count=1`
- Exit status: `0`
- Result: the identical selector passes with Admin-only create/update/delete, one active exact scope, monotonic revisions, current-revision CAS, terminal delete, and replacement only after delete.
- Concise output: `ok xirang/backend/internal/backupasset/retention 0.007s`

### Policy exact selection RED

- UTC timestamp: `2026-08-17T06:49:40Z`
- Command: `cd backend && go test ./internal/backupasset/retention -run '^TestPolicySelectionDeterministicExactScopesAndExclusions$' -count=1`
- Exit status: `1`
- Expected failure category: compile-time contract RED caused only by the absent frozen selection request/result types and `PolicyService.Select` method referenced by the focused exact-scope test.
- Concise output:

  ```text
  service.Select undefined
  undefined: SelectionRequest, SelectedPoint
  FAIL xirang/backend/internal/backupasset/retention [build failed]
  ```

### Policy exact selection GREEN

- UTC timestamp: `2026-08-17T06:50:55Z`
- Command: `cd backend && go test ./internal/backupasset/retention -run '^TestPolicySelectionDeterministicExactScopesAndExclusions$' -count=1`
- Exit status: `0`
- Result: the identical selector passes with a frozen policy snapshot, current-revision CAS, exact repository/live Task-link scope, deterministic sorted ID/revision pairs, and fail-closed exclusion of mutable, terminal, unavailable, aggregate-held, and durable-held points.
- Concise output: `ok xirang/backend/internal/backupasset/retention 0.008s`

### Policy calendar selection RED

- UTC timestamp: `2026-08-17T06:51:35Z`
- Command: `cd backend && go test ./internal/backupasset/retention -run '^TestPolicySelectionCalendarKeepsOneRepresentativePerUTCPeriod$' -count=1`
- Exit status: `1`
- Expected failure category: behavioral RED proving the selector currently expires all four eligible points instead of keeping one newest representative in each of the two retained UTC day buckets.
- Concise output: `calendar selection=[01 02 03 04], want [02 04]`

### Policy calendar selection GREEN

- UTC timestamp: `2026-08-17T06:51:58Z`
- Command: `cd backend && go test ./internal/backupasset/retention -run '^TestPolicySelectionCalendarKeepsOneRepresentativePerUTCPeriod$' -count=1`
- Exit status: `0`
- Result: the identical selector passes; calendar retention keeps exactly the newest point in each bounded retained UTC day/week/month/year bucket and leaves other points eligible for deterministic expiration.
- Concise output: `ok xirang/backend/internal/backupasset/retention 0.004s`

### Hold create/release transaction RED

- UTC timestamp: `2026-08-17T06:53:15Z`
- Command: `cd backend && go test ./internal/backupasset/retention -run '^TestHoldCreateReleaseEncryptsReasonsAndProjectsAtomically$' -count=1`
- Exit status: `1`
- Expected failure category: compile-time contract RED caused only by the absent hold service dependencies, safe hold DTO, create/release requests, and methods referenced by the focused transaction/privacy test.
- Concise output:

  ```text
  undefined: NewHoldService, HoldServiceDependencies, HoldRecord
  undefined: CreateHoldRequest, ReleaseHoldRequest
  FAIL xirang/backend/internal/backupasset/retention [build failed]
  ```

### Hold create/release transaction GREEN

- UTC timestamp: `2026-08-17T06:55:02Z`
- Command: `cd backend && go test ./internal/backupasset/retention -run '^TestHoldCreateReleaseEncryptsReasonsAndProjectsAtomically$' -count=1`
- Exit status: `0`
- Result: the identical selector passes with Admin-only create/release, one active hold per type, one-way release, encrypted create/release reasons, metadata-only JSON, and same-transaction aggregate projection across overlapping operational/legal holds.
- Concise output: `ok xirang/backend/internal/backupasset/retention 0.018s`

### Operational hold expiry RED

- UTC timestamp: `2026-08-17T06:55:56Z`
- Command: `cd backend && go test ./internal/backupasset/retention -run '^TestHoldOperationalExpiryIsBoundedAdminOnlyAndNeverReleasesLegal$' -count=1`
- Exit status: `1`
- Expected failure category: compile-time contract RED caused only by the absent bounded `HoldService.ExpireOperational` method referenced by the focused expiry test.
- Concise output: `service.ExpireOperational undefined`

### Operational hold expiry GREEN

- UTC timestamp: `2026-08-17T06:56:45Z`
- Command: `cd backend && go test ./internal/backupasset/retention -run '^TestHoldOperationalExpiryIsBoundedAdminOnlyAndNeverReleasesLegal$' -count=1`
- Exit status: `0`
- Result: the identical selector passes with Admin-only deterministic bounded expiry, race-safe one-way transitions, encrypted internal release reasons, same-transaction point projection, and legal holds excluded from automatic release.
- Concise output: `ok xirang/backend/internal/backupasset/retention 0.022s`

### Retention hold-release backend step-up RED

- UTC timestamp: `2026-08-17T06:58:45Z`
- Command: `cd backend && go test ./internal/auth -run '^TestStepUpActionRetentionHoldRelease$' -count=1`
- Exit status: `1`
- Expected failure category: compile-time contract RED caused only by the absent purpose-exact `StepUpActionRetentionHoldRelease` registry constant referenced by the focused isolation test.
- Concise output: `undefined: StepUpActionRetentionHoldRelease; FAIL xirang/backend/internal/auth [build failed]`

### Retention hold-release backend step-up GREEN

- UTC timestamp: `2026-08-17T06:59:05Z`
- Command: `cd backend && go test ./internal/auth -run '^TestStepUpActionRetentionHoldRelease$' -count=1`
- Exit status: `0`
- Result: the identical selector passes with the exact `retention.hold_release` allowlisted purpose, strict parsing, proof claim propagation, and isolation from `repository.purge`.
- Concise output: `ok xirang/backend/internal/auth 0.050s`

### Retention hold-release frontend step-up RED

- UTC timestamp: `2026-08-17T06:59:43Z`
- Command: `cd web && npm run test -- src/lib/step-up-storage.test.ts`
- Exit status: `1`
- Expected failure category: behavioral RED; the focused registry/storage test received `undefined` instead of the absent `retention.hold_release` action.
- Concise output: `1 failed | 5 passed; expected undefined to be 'retention.hold_release'`
- Environment note: an earlier attempt found no installed `vitest` binary, so locked dependencies were restored with `npm ci`; that infrastructure failure is intentionally not counted as RED evidence.

### Retention hold-release frontend step-up GREEN

- UTC timestamp: `2026-08-17T06:59:59Z`
- Command: `cd web && npm run test -- src/lib/step-up-storage.test.ts`
- Exit status: `0`
- Result: the identical focused file passes with the exact frontend registry value and independent proof read/clear behavior for hold release versus repository purge.
- Concise output: `1 passed; 6 tests passed`

### Retention settings registry RED

- UTC timestamp: `2026-08-17T07:01:11Z`
- Command: `cd backend && go test ./internal/settings -run '^TestRetentionSettingsDefinitionsAndSafeDefaults$' -count=1`
- Exit status: `1`
- Expected failure category: behavioral RED caused by the absent bounded retention reconcile-interval, batch-size, and drain-timeout definitions.
- Concise output: `missing retention setting backup_assets.retention_reconcile_interval`

### Retention settings registry GREEN

- UTC timestamp: `2026-08-17T07:01:53Z`
- Command: `cd backend && go test ./internal/settings -run '^TestRetentionSettingsDefinitionsAndSafeDefaults$' -count=1`
- Exit status: `0`
- Result: the identical selector passes with dynamic bounded retention reconcile interval, batch size, and drain timeout definitions while `backup_assets.enabled` remains default false.
- Concise output: `ok xirang/backend/internal/settings 0.005s`

### Typed retention foundation config RED

- UTC timestamp: `2026-08-17T07:02:42Z`
- Command: `cd backend && go test ./internal/backupasset -run '^TestFoundationRetentionConfigUsesRegisteredSettingsAndBounds$' -count=1`
- Exit status: `1`
- Expected failure category: compile-time contract RED caused only by the absent typed `FoundationService.RetentionConfig` consumer.
- Concise output: `NewFoundationService(reader).RetentionConfig undefined; FAIL xirang/backend/internal/backupasset [build failed]`

### Typed retention foundation config GREEN

- UTC timestamp: `2026-08-17T07:03:13Z`
- Command: `cd backend && go test ./internal/backupasset -run '^TestFoundationRetentionConfigUsesRegisteredSettingsAndBounds$' -count=1`
- Exit status: `0`
- Result: the identical selector passes with a typed dynamic retention config, global feature flag propagation, and fail-closed interval/batch/drain bounds.
- Concise output: `ok xirang/backend/internal/backupasset 0.004s`

### Hold request JSON privacy RED

- UTC timestamp: `2026-08-17T07:05:24Z`
- Command: `cd backend && go test ./internal/backupasset/retention -run '^TestHoldCreateReleaseEncryptsReasonsAndProjectsAtomically$' -count=1`
- Exit status: `1`
- Expected failure category: behavioral privacy RED found during self-review; JSON marshaling a service create request exposed its plaintext reason.
- Concise output: `hold request JSON contains plaintext reason: ... "Reason":"FAKE_OPERATIONAL_HOLD_REASON_FOR_TEST_ONLY" ...`

### Hold request JSON privacy GREEN

- UTC timestamp: `2026-08-17T07:05:41Z`
- Command: `cd backend && go test ./internal/backupasset/retention -run '^TestHoldCreateReleaseEncryptsReasonsAndProjectsAtomically$' -count=1`
- Exit status: `0`
- Result: the identical selector passes after create/release service request reason fields were made JSON-ineligible in addition to encrypted at rest and absent from returned records.
- Concise output: `ok xirang/backend/internal/backupasset/retention 0.019s`

## Task 3 verification

- UTC timestamp: `2026-08-17T07:05:58Z`
- Required selector: `cd backend && go test ./internal/backupasset/retention ./internal/auth -run '^(TestPolicy|TestHold|TestStepUpActionRetentionHoldRelease)' -count=1`
- Exit status: `0`
- Result: `ok xirang/backend/internal/backupasset/retention 0.053s; ok xirang/backend/internal/auth 0.052s`
- Touched-package selector: `cd backend && go test ./internal/backupasset/retention ./internal/auth ./internal/settings ./internal/backupasset -count=1`
- Exit status: `0`
- Result: all four touched packages passed.
- Race selector: `cd backend && go test -race ./internal/backupasset/retention ./internal/auth -run '^(TestPolicy|TestHold|TestStepUpActionRetentionHoldRelease)' -count=1`
- Exit status: `0`
- Result: `ok xirang/backend/internal/backupasset/retention 1.162s; ok xirang/backend/internal/auth 1.530s`
- Frontend focused test at `2026-08-17T07:04:27Z`: `cd web && npm run test -- src/lib/step-up-storage.test.ts` — exit `0`, 6 tests passed.
- Frontend typecheck at `2026-08-17T07:04:27Z`: `cd web && npm run typecheck` — exit `0`.
- Frontend focused lint at `2026-08-17T07:04:27Z`: `cd web && npx eslint src/lib/step-up-storage.ts src/lib/step-up-storage.test.ts` — exit `0`.
- `git diff --check` — exit `0` with no output.

### Hold request log-format privacy RED

- UTC timestamp: `2026-08-17T07:07:08Z`
- Command: `cd backend && go test ./internal/backupasset/retention -run '^TestHoldCreateReleaseEncryptsReasonsAndProjectsAtomically$' -count=1`
- Exit status: `1`
- Expected failure category: behavioral privacy RED; default request formatting exposed the plaintext hold reason even though JSON was already protected.
- Concise output: `hold request formatting contains plaintext reason: ... FAKE_OPERATIONAL_HOLD_REASON_FOR_TEST_ONLY ...`

### Hold request log-format privacy GREEN

- UTC timestamp: `2026-08-17T07:07:32Z`
- Command: `cd backend && go test ./internal/backupasset/retention -run '^TestHoldCreateReleaseEncryptsReasonsAndProjectsAtomically$' -count=1`
- Exit status: `0`
- Result: the identical selector passes with redacted `String`/`GoString` request rendering; plaintext reasons are absent from default log formatting as well as JSON and returned records.
- Concise output: `ok xirang/backend/internal/backupasset/retention 0.019s`

## Task 3 final re-verification after privacy hardening

- UTC timestamp: `2026-08-17T07:07:52Z`
- Required selector: `cd backend && go test ./internal/backupasset/retention ./internal/auth -run '^(TestPolicy|TestHold|TestStepUpActionRetentionHoldRelease)' -count=1`
- Exit status: `0`
- Result: `ok xirang/backend/internal/backupasset/retention 0.059s; ok xirang/backend/internal/auth 0.054s`
- Touched-package selector: `cd backend && go test ./internal/backupasset/retention ./internal/auth ./internal/settings ./internal/backupasset -count=1`
- Exit status: `0`
- Result: all four touched packages passed.
- Race selector: `cd backend && go test -race ./internal/backupasset/retention ./internal/auth -run '^(TestPolicy|TestHold|TestStepUpActionRetentionHoldRelease)' -count=1`
- Exit status: `0`
- Result: `ok xirang/backend/internal/backupasset/retention 1.162s; ok xirang/backend/internal/auth 1.552s`

## Task 3 spec-review fix — Independent RecoveryPoint revision fencing

- Evidence recorded at UTC `2026-08-17T07:36:34Z` after the bounded correction and cleanup completed.

### Paired `000070` migration/model RED

- Command: `cd backend && go test ./internal/database -run '^TestBackupAssetMigration070PointRevisionSQLite$' -count=1`
- Exit status: `1`
- Expected failure category: focused schema-contract RED; the live `000070` migration completed but `recovery_points` had no independent `point_revision` column.
- Concise output: `SQLite 000070 recovery_points lacks point_revision; FAIL xirang/backend/internal/database 0.159s`

### Paired `000070` migration/model GREEN

- Same command exit status: `0`
- Result: SQLite backfills existing rows to positive revision 1; exact model parity passes; implicit updates and explicit old+1 updates advance exactly once; zero, skipped, and decreasing revisions are rejected; capability revision remains independent.
- Concise output: `ok xirang/backend/internal/database 0.157s`
- Exact final SQLite gate: `cd backend && go test ./internal/database -run '^TestBackupAssetMigration070(SQLite|PairedFiles|UsedDownAdmissionSQLite)$' -count=1` — exit `0`, `ok xirang/backend/internal/database 1.800s`.
- The shared contract also proves pristine down removes the owned triggers/column and restores the exact `000069` RecoveryPoint definition, while any `point_revision != 1` rejects down without changing the clean version, schema, triggers, or row.

### Policy selection revision/lock RED -> GREEN

- Command: `cd backend && go test ./internal/backupasset/retention -run '^TestPolicySelectionLocksRowsAndBindsIndependentRevision$' -count=1`
- RED exit status: `1`.
- Expected failure category: compile-time contract RED; `SelectedPoint` had no `PointRevision`, so selection could not bind the independent lifecycle token.
- Concise RED output: `unknown field PointRevision in struct literal of type SelectedPoint; FAIL [build failed]`.
- Same command GREEN exit status: `0`.
- Result: selection validates and returns positive point and capability revisions separately, and the RecoveryPoint query carries a `FOR UPDATE` lock clause.
- Concise GREEN output: `ok xirang/backend/internal/backupasset/retention 0.006s`.

### Hold projection revision RED -> GREEN

- Command: `cd backend && go test ./internal/backupasset/retention -run '^TestHoldProjectionAdvancesPointRevisionIndependently$' -count=1`
- RED exit status: `1`.
- Expected failure category: behavioral RED; an AutoMigrate-backed hold creation changed the projection without advancing the lifecycle point token.
- Concise RED output: `hold projection revisions=10/20, want 11/20; FAIL xirang/backend/internal/backupasset/retention 0.029s`.
- Same command GREEN exit status: `0`.
- Result: create, release, and operational expiry each explicitly advance `point_revision` once while leaving `capability_revision` unchanged; paired production triggers accept explicit old+1 without double advancement.
- Concise GREEN output: `ok xirang/backend/internal/backupasset/retention 0.028s`.

### Approved disposable PostgreSQL parity and lock evidence

- Image: `postgres:17-alpine`, local digest `sha256:18cfe3ef5e6815560c98237d6216d1e5119702fb0f3894c8785dd58b8bbe5d73`.
- Primary fixture: `xirang-task3-revision-20260817-072837-16263`, loopback-only port `32786`, no host volume, random shell-local database/user/password, ready after 3 bounded polls.
- Required command: `cd backend && REQUIRE_POSTGRES_MIGRATION_TEST=1 TEST_POSTGRES_DSN="$TEST_POSTGRES_DSN" go test ./internal/database -run '^TestBackupAssetMigration070(Postgres|UsedDownAdmissionPostgres)$' -count=1`.
- Exit status: `0`; concise output: `ok xirang/backend/internal/database 27.320s`.
- Focused live-lock command: `cd backend && TEST_POSTGRES_DSN="$TEST_POSTGRES_DSN" go test ./internal/backupasset/retention -run '^TestPolicySelectionPostgresLocksPointStateAndHoldDrift$' -count=1`.
- Exit status: `0`; concise output: `ok xirang/backend/internal/backupasset/retention 0.203s`.
- PostgreSQL `000069` regression: `cd backend && REQUIRE_POSTGRES_MIGRATION_TEST=1 TEST_POSTGRES_DSN="$TEST_POSTGRES_DSN" go test ./internal/database -run '^TestBackupAssetMigration069Postgres$' -count=1` — exit `0`, `ok xirang/backend/internal/database 118.899s`.
- Deterministic lock-observation re-run: isolated fixture `xirang-task3-lock-20260817-073620-29663`, loopback-only port `32787`, no host volume, random shell-local credentials, ready after 2 polls; the selector observed the concurrent writer in PostgreSQL `Lock` wait state and passed in `0.118s`.
- Cleanup: both fixtures stopped, auto-removed through `--rm`, credentials/DSNs were unset by the bounded shell cleanup, and independent `docker inspect` checks reported both names absent.

### Regression and final quality gates

- The first touched-package run correctly exposed pre-`000070` parity drift: the latest RecoveryPoint model was being compared directly to `000062`/`000063`. The versioned parity helper now excludes only `point_revision` for those historical schemas; `000070` retains exact model/schema parity.
- Touched packages: `cd backend && go test ./internal/model ./internal/backupasset/retention ./internal/database -count=1` — exit `0`; model `0.147s`, retention `0.098s`, database `24.841s`.
- Race: `cd backend && go test -race ./internal/backupasset/retention -run '^(TestPolicy|TestHold)' -count=1` — exit `0`, `1.229s`.
- Focused migration race: `cd backend && go test -race ./internal/database -run '^TestBackupAssetMigration070(SQLite|PairedFiles|UsedDownAdmissionSQLite|PointRevisionSQLite)$' -count=1` — exit `0`, `4.139s`.
- Required Task 3 selector: `cd backend && go test ./internal/backupasset/retention ./internal/auth -run '^(TestPolicy|TestHold|TestStepUpActionRetentionHoldRelease)' -count=1` — exit `0`; retention `0.092s`, auth `0.050s`.
- SQLite `000069` regression: `cd backend && go test ./internal/database -run '^TestBackupAssetMigration069(SQLite|PairedFiles)$' -count=1` — exit `0`, `7.520s`.
- `gofmt -d` over every authorized Go path produced no output; repository-root `git diff --check` exited `0`; the production retention privacy scan found no Provider locator, rollback locator, fence token, step-up proof, grant, ticket, private-key, or password flow.

## Task 3 final quality fixes — PostgreSQL scope fencing and clock sampling

### Task-link selection drift lock RED

- UTC timestamp: `2026-08-17T07:58:50Z`
- Command: `cd backend && TEST_POSTGRES_DSN="$TEST_POSTGRES_DSN" go test ./internal/backupasset/retention -run '^TestPolicySelectionPostgresLocksTaskLinkDrift$' -count=1`
- Exit status: `1`
- Expected failure category: behavioral PostgreSQL concurrency RED; the selection transaction returned the stable old exact scope, but a concurrent live Task-link unlink/rebind committed instead of waiting for that transaction.
- Concise output: `concurrent Task-link unlink/rebind was not fenced: <nil>; FAIL xirang/backend/internal/backupasset/retention 0.094s`

### Task-link selection drift lock GREEN

- UTC timestamp: `2026-08-17T08:02:07Z`
- Same command: `cd backend && TEST_POSTGRES_DSN="$TEST_POSTGRES_DSN" go test ./internal/backupasset/retention -run '^TestPolicySelectionPostgresLocksTaskLinkDrift$' -count=1`
- Exit status: `0`
- Result: selection now locks the resolved live TaskRepositoryLink row before reading its exact repository/task scope; the concurrent unlink/rebind reached a PostgreSQL `Lock` wait and completed only after the selection transaction ended.
- Concise output: `ok xirang/backend/internal/backupasset/retention 0.082s`

### Concurrent policy create conflict RED

- UTC timestamp: `2026-08-17T08:04:12Z`
- Command: `cd backend && TEST_POSTGRES_DSN="$TEST_POSTGRES_DSN" go test ./internal/backupasset/retention -run '^TestPolicyConcurrentCreatePostgresMapsActiveScopeConflict$' -count=1`
- Exit status: `1`
- Expected failure category: deterministic PostgreSQL concurrency RED; both transactions passed the active-policy count and allocated IDs, then the loser returned raw SQLSTATE `23505` after its mapper queried the already-aborted transaction and encountered SQLSTATE `25P02`.
- Concise output: `successes=1 conflicts=0 unexpected=[create retention policy: duplicate key / SQLSTATE 23505] active=1 ID allocations=2; want 1/1/none/1/1; FAIL xirang/backend/internal/backupasset/retention 0.072s`

### Concurrent policy create conflict GREEN

- UTC timestamp: `2026-08-17T08:05:22Z`
- Same command: `cd backend && TEST_POSTGRES_DSN="$TEST_POSTGRES_DSN" go test ./internal/backupasset/retention -run '^TestPolicyConcurrentCreatePostgresMapsActiveScopeConflict$' -count=1`
- Exit status: `0`
- Result: create locks the exact repository or live Task-link scope row before count/insert; the loser observes the committed active row before ID generation and returns `backupasset.ErrConflict`. PostgreSQL SQLSTATE `23505` is also mapped directly only for `idx_backup_retention_policies_active_scope`, without querying an aborted transaction; SQLite keeps its existing fallback mapper.
- Concise output: `ok xirang/backend/internal/backupasset/retention 0.579s`

### Hold create clock-sampling RED

- UTC timestamp: `2026-08-17T08:06:33Z`
- Command: `cd backend && go test ./internal/backupasset/retention -run '^TestHoldCreateSamplesClockOnceAcrossExpiryBoundary$' -count=1`
- Exit status: `1`
- Expected failure category: deterministic clock-boundary RED; validation sampled `09:30:00Z`, but creation sampled again at `09:30:02Z`, producing an active hold created after its `09:30:01Z` expiry.
- Concise output: `hold clock samples=2 ... CreatedAt=09:30:02Z ExpiresAt=09:30:01Z; want one sample at 09:30:00Z before expiry; FAIL xirang/backend/internal/backupasset/retention 0.039s`

### Hold create clock-sampling GREEN

- UTC timestamp: `2026-08-17T08:06:54Z`
- Same command: `cd backend && go test ./internal/backupasset/retention -run '^TestHoldCreateSamplesClockOnceAcrossExpiryBoundary$' -count=1`
- Exit status: `0`
- Result: hold create samples UTC time once and reuses it for expiry validation, `CreatedAt`, `UpdatedAt`, and the same-transaction RecoveryPoint projection.
- Concise output: `ok xirang/backend/internal/backupasset/retention 0.038s`

### Final PostgreSQL, SQLite, race, and cleanup gates

- PostgreSQL focused command at UTC `2026-08-17T08:07:59Z`: `cd backend && TEST_POSTGRES_DSN="$TEST_POSTGRES_DSN" go test ./internal/backupasset/retention -run '^(TestPolicySelectionPostgresLocksTaskLinkDrift|TestPolicyConcurrentCreatePostgresMapsActiveScopeConflict)$' -count=1` — exit `0`, `ok xirang/backend/internal/backupasset/retention 0.646s`.
- PostgreSQL race command at UTC `2026-08-17T08:08:35Z`: same selector with `go test -race` — exit `0`, `ok xirang/backend/internal/backupasset/retention 1.709s`.
- Required Task 3 selector with PostgreSQL enabled at UTC `2026-08-17T08:09:14Z`: `cd backend && go test ./internal/backupasset/retention ./internal/auth -run '^(TestPolicy|TestHold|TestStepUpActionRetentionHoldRelease)' -count=1` — exit `0`; retention `0.798s`, auth `0.051s`.
- No-DSN SQLite touched package at UTC `2026-08-17T08:10:04Z`: `cd backend && go test ./internal/backupasset/retention -count=1` — exit `0`, `0.111s`.
- No-DSN SQLite race at UTC `2026-08-17T08:10:05Z`: `cd backend && go test -race ./internal/backupasset/retention -run '^(TestPolicy|TestHold)' -count=1` — exit `0`, `1.286s`.
- Required Task 3 selector without PostgreSQL DSN at UTC `2026-08-17T08:10:07Z`: same required command — exit `0`; retention `0.117s`, auth `0.054s`.
- Disposable fixture: `xirang-task3-129a912cf778`, `postgres:17-alpine`, `--rm`, loopback-only publication, random shell-local credentials, and no bind mount. Docker's image-declared anonymous volume was identified exactly from its container event ID and removed after the container; independent inspection reports both exact container and volume absent. Credential/DSN variables were unset.
- Audit completed at UTC `2026-08-17T08:12:17Z`: `gofmt -d` produced no output; tracked and explicit untracked `git diff --check` gates exited `0`; production privacy scan found no locator, fence token, step-up proof, private-key, password, or ad hoc print/log flow. Lock order is policy -> live Task-link -> RecoveryPoint for selection and exact repository/Task-link scope -> policy count/insert for create; no new reverse acquisition was introduced.

## Task 4 — Repository reconnect, import review, and rebuild

### Retired mutable-head reconnect RED

- UTC timestamp: `2026-08-17T08:22:28Z`
- Command: `cd backend && go test ./internal/backupasset/repository -run '^TestLifecycleReconnectRetiredHeadDoesNotReactivate$' -count=1`
- Exit status: `1`
- Expected failure category: behavioral RED proving the existing probe-first reconnect transaction rejects a retired mutable head and rolls back repository access instead of reconnecting the repository while preserving the retired row unchanged.
- Concise output: `reconnect retired head: backup asset conflict: mutable point cannot be reactivated; FAIL xirang/backend/internal/backupasset/repository`

### Retired mutable-head reconnect GREEN

- UTC timestamp: `2026-08-17T08:23:13Z`
- Same command: `cd backend && go test ./internal/backupasset/repository -run '^TestLifecycleReconnectRetiredHeadDoesNotReactivate$' -count=1`
- Exit status: `0`
- Result: exact identity and live Task lineage reconnect repository access, while the retired mutable-head row, locator, last-good observation, retirement facts, and opaque ID remain unchanged; no replacement point is fabricated.
- Concise output: `ok xirang/backend/internal/backupasset/repository 0.098s`

### Import discovery persistence RED

- UTC timestamp: `2026-08-17T08:28:39Z`
- Command: `cd backend && go test ./internal/backupasset/repository -run '^TestImportDiscoveryPersistsEncryptedKeyedCandidateIdempotently$' -count=1`
- Exit status: `1`
- Expected failure category: compile-time contract RED caused only by the missing bounded import-discovery request/result and `Service.DiscoverImportCandidates` entry point.
- Concise output:

  ```text
  service.DiscoverImportCandidates undefined
  undefined: ImportDiscoveryRequest
  FAIL xirang/backend/internal/backupasset/repository [build failed]
  ```

### Import discovery persistence GREEN

- UTC timestamp: `2026-08-17T08:31:07Z`
- Same command: `cd backend && go test ./internal/backupasset/repository -run '^TestImportDiscoveryPersistsEncryptedKeyedCandidateIdempotently$' -count=1`
- Exit status: `0`
- Result: bounded provider discovery normalizes a complete Restic snapshot into one keyed source fingerprint, persists its private locator/evidence only through the encrypted model fields, returns a sanitized pending candidate, and re-discovery is idempotent without duplicating the candidate.
- Concise output: `ok xirang/backend/internal/backupasset/repository 0.089s`

### Import review terminal acceptance RED

- UTC timestamp: `2026-08-17T08:33:02Z`
- Command: `cd backend && go test ./internal/backupasset/repository -run '^TestImportReviewAcceptIsTerminalIdempotentAndCreatesOneRecoveryPoint$' -count=1`
- Exit status: `1`
- Expected failure category: compile-time contract RED caused only by the absent Admin import-review request and service entry point.
- Concise output: `undefined: ImportReviewRequest`; `service.ReviewImportCandidate undefined`; package build failed.

### Import review terminal acceptance GREEN

- UTC timestamp: `2026-08-17T08:35:59Z`
- Same command: `cd backend && go test ./internal/backupasset/repository -run '^TestImportReviewAcceptIsTerminalIdempotentAndCreatesOneRecoveryPoint$' -count=1`
- Exit status: `0`
- Result: Admin acceptance strictly revalidates decrypted private evidence, creates and terminally maps exactly one committed RecoveryPoint in the same transaction, encrypts its provider locator at rest, returns the same mapping on replay, and refuses a later contradictory rejection.
- Concise output: `ok xirang/backend/internal/backupasset/repository 0.091s`

### Import discovery Admin visibility RED

- UTC timestamp: `2026-08-17T08:36:41Z`
- Command: `cd backend && go test ./internal/backupasset/repository -run '^TestImportDiscoveryPersistsEncryptedKeyedCandidateIdempotently$' -count=1`
- Exit status: `1`
- Expected failure category: behavioral privacy RED; an Operator could invoke discovery and receive the pending unknown candidate instead of being denied before Provider access.
- Concise output: `operator discovered pending import candidates: <nil>`; package failed in `0.083s`.

### Import discovery Admin visibility GREEN

- UTC timestamp: `2026-08-17T08:37:07Z`
- Same command: `cd backend && go test ./internal/backupasset/repository -run '^TestImportDiscoveryPersistsEncryptedKeyedCandidateIdempotently$' -count=1`
- Exit status: `0`
- Result: discovery now rejects non-Admin actors before probing/listing Provider state; Admin discovery remains bounded, encrypted, sanitized, and idempotent.
- Concise output: `ok xirang/backend/internal/backupasset/repository 0.090s`

### Import pending-list and rejection RED

- UTC timestamp: `2026-08-17T08:38:04Z`
- Command: `cd backend && go test ./internal/backupasset/repository -run '^TestImportPendingListAndRejectAreAdminOnlyTerminalAndPointFree$' -count=1`
- Exit status: `1`
- Expected failure category: compile-time contract RED caused by the missing bounded Admin candidate-list request and service method.
- Concise output: `service.ListImportCandidates undefined`; `undefined: ImportCandidateListRequest`; package build failed.

### Import pending-list and rejection GREEN

- UTC timestamp: `2026-08-17T08:38:44Z`
- Same command: `cd backend && go test ./internal/backupasset/repository -run '^TestImportPendingListAndRejectAreAdminOnlyTerminalAndPointFree$' -count=1`
- Exit status: `0`
- Result: the bounded cursor list rejects Operators before querying candidates and returns only sanitized views to Admin; rejection is terminal/idempotent and never creates a RecoveryPoint.
- Concise output: `ok xirang/backend/internal/backupasset/repository 0.091s`

### Import mutable no-fake-history RED

- UTC timestamp: `2026-08-17T08:39:55Z`
- Command: `cd backend && go test ./internal/backupasset/repository -run '^TestImportMutableCandidateRequiresExplicitBaselineAndNeverReactivatesRetiredHead$' -count=1`
- Exit status: `1`
- Expected failure category: behavioral trust RED; explicit baseline review created a distinct point and preserved the retired head, but incorrectly copied the repository's `mutable` immutability onto the immutable `imported_baseline`.
- Concise output: accepted point had `Semantics:imported_baseline State:committed ImmutabilityLevel:mutable`; package failed in `0.095s`.

### Import mutable no-fake-history GREEN

- UTC timestamp: `2026-08-17T08:40:18Z`
- Same command: `cd backend && go test ./internal/backupasset/repository -run '^TestImportMutableCandidateRequiresExplicitBaselineAndNeverReactivatesRetiredHead$' -count=1`
- Exit status: `0`
- Result: an arbitrary mutable candidate remains pending without an explicit disposition; reviewed baseline acceptance creates one new committed `imported_baseline` with managed immutability while preserving the retired mutable row, its retirement facts, and its opaque ID unchanged.
- Concise output: `ok xirang/backend/internal/backupasset/repository 0.089s`

### Rebuild per-manifest truthful outcomes RED

- UTC timestamp: `2026-08-17T08:42:10Z`
- Command: `cd backend && go test ./internal/backupasset/repository -run '^TestRebuildAcceptedImportsReportsTruthfulPerManifestOutcomes$' -count=1`
- Exit status: `1`
- Expected failure category: compile-time contract RED caused by the absent narrow Catalog-generation/derived-backfill ports, rebuild request/result contracts, and `Service.RebuildAcceptedImports` method.
- Concise output: undefined `CatalogRebuildRequest`, `CatalogRebuildStart`, `DerivedBackfillRequest`, rebuild reason constants, dependency fields, and service method; package build failed.

### Rebuild per-manifest truthful outcomes GREEN

- UTC timestamp: `2026-08-17T08:43:24Z`
- Same command: `cd backend && go test ./internal/backupasset/repository -run '^TestRebuildAcceptedImportsReportsTruthfulPerManifestOutcomes$' -count=1`
- Exit status: `0`
- Result: rebuild validates every accepted candidate-to-point manifest mapping, starts a fresh Catalog generation only for valid mappings through an injected owner port, queues only low-priority derived work through a second owner port, continues after per-item failures, and returns safe exact accepted/catalog-started/derived-queued/partial/failed reason counts.
- Concise output: `ok xirang/backend/internal/backupasset/repository 0.087s`

### Import exact-point bind RED

- UTC timestamp: `2026-08-17T08:45:04Z`
- Command: `cd backend && go test ./internal/backupasset/repository -run '^TestImportReviewAcceptBindsExistingExactPointWithoutDuplicate$' -count=1`
- Exit status: `1`
- Expected failure category: behavioral idempotency RED; review always attempted a create instead of binding the exact already-published point, reaching the native-source unique index.
- Concise output: `create accepted import RecoveryPoint: UNIQUE constraint failed: recovery_points.repository_id, recovery_points.source_fingerprint`; package failed in `0.083s`.

### Import exact-point bind GREEN

- UTC timestamp: `2026-08-17T08:45:57Z`
- Same command: `cd backend && go test ./internal/backupasset/repository -run '^TestImportReviewAcceptBindsExistingExactPointWithoutDuplicate$' -count=1`
- Exit status: `0`
- Result: acceptance locks and binds an exact existing point by repository/keyed source, rejects mismatched or failed claimants without relabeling, creates only when absent, and verifies the pending-to-terminal candidate CAS updated exactly one row.
- Concise output: `ok xirang/backend/internal/backupasset/repository 0.081s`

### Task 4 repository verification gates

- At UTC `2026-08-17T08:47:09Z`, the required selector `cd backend && go test ./internal/backupasset/repository -run '^(TestDisconnectPreservesMutableEvidenceAndReconnectsWithRetainedSalt|TestLifecycleReconnectRetiredHeadDoesNotReactivate|TestImport|TestRebuild)' -count=1` exited `0` in `0.217s`.
- The same required selector with `-count=10` exited `0` in `1.394s`; the same selector under `go test -race` exited `0` in `2.063s`.
- The full repository package `cd backend && go test ./internal/backupasset/repository -count=1` exited `0` in `5.524s`.
- `gofmt -d` over every Task 4 Go path, tracked `git diff --check`, and explicit no-index whitespace checks for the four new repository files and Task evidence produced no output. The production privacy scan found no print/logger flow or JSON exposure of source fingerprints, encrypted evidence, or Provider locators; response/owner-port types contain only safe opaque IDs, digest/revision metadata, counts, and closed reason codes.
- Scope audit confirms Task 4 changed only the authorized repository reconnect/service files, created `import.go`, `import_test.go`, `rebuild.go`, `rebuild_test.go`, and appended this shared chronological evidence. The visible `repository/testutil_test.go` modification belongs to the independently approved Task 3 fixture repair and was preserved untouched.

### Task 4 review amendment — managed-manifest missing-proof RED

- UTC timestamp: `2026-08-17T08:57:09Z`
- Command: `cd backend && go test ./internal/backupasset/repository -run '^TestImportManagedManifestProofMatrixMissingVerifierFailsClosed$' -count=1`
- Exit status: `1`
- Expected failure category: behavioral provenance RED; both Rsync and Rclone candidates carrying only `xirang_manifest` semantics plus hexadecimal source revision were admitted without any authenticated marker or complete commit-digest proof owner.
- Concise output: `unverified rsync xirang_manifest admitted: <nil>` and `unverified rclone xirang_manifest admitted: <nil>`; package failed in `0.132s`.

### Task 4 review amendment — managed-manifest missing-proof GREEN

- UTC timestamp: `2026-08-17T08:58:09Z`
- Same command: `cd backend && go test ./internal/backupasset/repository -run '^TestImportManagedManifestProofMatrixMissingVerifierFailsClosed$' -count=1`
- Exit status: `0`
- Result: Rsync/Rclone managed-manifest discovery now requires an injected repository-local proof owner before normalization or persistence; Restic native discovery remains independent.
- Concise output: `ok xirang/backend/internal/backupasset/repository 0.126s`.

### Task 4 review amendment — managed-manifest proof matrix RED

- UTC timestamp: `2026-08-17T08:59:22Z`
- Command: `cd backend && go test ./internal/backupasset/repository -run '^TestImportManagedManifestProofMatrixValidAndInvalid$' -count=1`
- Exit status: `1`
- Expected failure category: behavioral provenance RED; the newly injected verifier was never called, so invalid marker, incomplete commit graph, and digest mismatch cases for both Rsync and Rclone were persisted as pending `xirang_manifest` candidates.
- Concise output: valid cases reported `proof requests=[]`; all six invalid cases reported `invalid proof admitted` with one persisted candidate; package failed in `0.221s`.

### Task 4 review amendment — managed-manifest proof matrix GREEN

- UTC timestamp: `2026-08-17T09:00:25Z`
- Same command: `cd backend && go test ./internal/backupasset/repository -run '^TestImportManagedManifestProofMatrixValidAndInvalid$' -count=1`
- Exit status: `0`
- Result: Rsync/Rclone discovery calls the proof owner with only repository ID, provider class, candidate digest, keyed marker digest, and commit-graph digest list; valid proof persists one encrypted candidate while invalid marker, incomplete graph, or digest mismatch returns a closed conflict and persists none.
- Concise output: `ok xirang/backend/internal/backupasset/repository 0.219s`.

### Task 4 review amendment — managed-manifest review revalidation RED

- UTC timestamp: `2026-08-17T09:01:32Z`
- Command: `cd backend && go test ./internal/backupasset/repository -run '^TestImportManagedManifestReviewRevalidatesProofBeforeAcceptance$' -count=1`
- Exit status: `1`
- Expected failure category: behavioral provenance RED; structurally valid encrypted stored evidence self-authorized acceptance because review never returned to the independent proof owner.
- Concise output: both Rsync and Rclone subtests reported `stored proof self-authorized acceptance: <nil>`; package failed in `0.128s`.

### Task 4 review amendment — managed-manifest review revalidation GREEN

- UTC timestamp: `2026-08-17T09:02:25Z`
- Same command: `cd backend && go test ./internal/backupasset/repository -run '^TestImportManagedManifestReviewRevalidatesProofBeforeAcceptance$' -count=1`
- Exit status: `0`
- Result: pending managed-manifest acceptance performs external proof verification from a private durable snapshot, then locks repository/candidate rows and rejects any provider, capability, kind, fingerprint, locator/evidence, or review-state drift before mutation; stored evidence cannot self-authorize.
- Concise output: `ok xirang/backend/internal/backupasset/repository 0.124s`.

## Task 3 follow-up — Repository foundation retention defaults

### Frozen repository settings fixture RED

- UTC timestamp: `2026-08-17T08:24:33Z`
- Command: `cd backend && go test ./internal/backupasset/repository -run '^TestRepositoryFoundationSettingsFixtureCoversSearchOverlayConfig$' -count=1`
- Exit status: `1`
- Expected failure category: fixture-contract RED; the frozen repository Foundation defaults omitted the three Task 3 retention settings, so a complete Foundation snapshot could not be constructed.
- Concise output: omitted `backup_assets.retention_reconcile_interval`, `backup_assets.retention_batch_size`, and `backup_assets.retention_drain_timeout`; `SearchOverlayConfig: incomplete backup asset settings snapshot`; package `FAIL` in `0.051s`.

### Frozen repository settings fixture GREEN

- UTC timestamp: `2026-08-17T08:25:08Z`
- Same command: `cd backend && go test ./internal/backupasset/repository -run '^TestRepositoryFoundationSettingsFixtureCoversSearchOverlayConfig$' -count=1`
- Exit status: `0`
- Result: the frozen repository Foundation fixture now carries the registry code defaults `5m`, `100`, and `30s` for the three retention settings; no production settings behavior changed.
- Concise output: `ok xirang/backend/internal/backupasset/repository 0.050s`.

### Repository settings fixture regression gates

- At UTC `2026-08-17T08:25:26Z`, `cd backend && go test ./internal/backupasset/repository -run '^TestDisconnectPreservesMutableEvidenceAndReconnectsWithRetainedSalt$' -count=1` exited `0` in `0.092s`.
- The Task 4 retired-head selector `cd backend && go test ./internal/backupasset/repository -run '^TestLifecycleReconnectRetiredHeadDoesNotReactivate$' -count=1` exited `0` in `0.085s`.
- At UTC `2026-08-17T08:25:28Z`, `cd backend && go test ./internal/backupasset/repository -count=1` exited `0` in `5.071s`.
- `gofmt -d backend/internal/backupasset/repository/testutil_test.go`, repository-root `git diff --check`, and the explicit untracked evidence whitespace check produced no output and exited `0`.

### Task 4 review amendment — Connect probe-to-commit Task archive RED

- UTC timestamp: `2026-08-17T09:04:46Z`
- Command: `cd backend && go test ./internal/backupasset/repository -run '^TestConnectRejectsTaskArchivedDuringProbeWithoutMutation$' -count=1`
- Exit status: `1`
- Expected failure category: behavioral stale-lineage RED; the Provider probe archived the Task after the initial load, but Connect committed the repository graph from the stale Task snapshot.
- Concise output: `Task archived during probe connect error=<nil>`; package failed in `0.076s`.

### Task 4 review amendment — Connect probe-to-commit Task archive GREEN

- UTC timestamp: `2026-08-17T09:06:26Z`
- Same command: `cd backend && go test ./internal/backupasset/repository -run '^TestConnectRejectsTaskArchivedDuringProbeWithoutMutation$' -count=1`
- Exit status: `0`
- Result: Connect now starts the commit transaction by locking and reloading the Task and its Node/credential lineage, rejects archive or access-lineage drift with typed `ErrConflict`, and performs no repository, binding, link, or point mutation.
- Concise output: `ok xirang/backend/internal/backupasset/repository 0.079s`.

### Task 4 review amendment — Connect probe-to-commit Task-link drift RED

- UTC timestamp: `2026-08-17T09:07:14Z`
- Command: `cd backend && go test ./internal/backupasset/repository -run '^TestConnectRejectsTaskLinkDriftDuringProbeWithoutMutation$' -count=1`
- Exit status: `1`
- Expected failure category: behavioral stale-lineage RED; the scripted second probe unlinked the exact retained Task link, but Connect resolved the repository from stale retained access and recreated an active link.
- Concise output: `Task link changed during probe connect error=<nil>`; package failed in `0.076s`.

### Task 4 review amendment — Connect probe-to-commit Task-link drift GREEN

- UTC timestamp: `2026-08-17T09:08:29Z`
- Same command: `cd backend && go test ./internal/backupasset/repository -run '^TestConnectRejectsTaskLinkDriftDuringProbeWithoutMutation$' -count=1`
- Exit status: `0`
- Result: Connect snapshots the active Task link before retained-access resolution, then locks and revalidates the exact absent/present link after Task/Node lineage and before repository resolution; unlink/replacement drift returns typed `ErrConflict` without recreating a link.
- Concise output: `ok xirang/backend/internal/backupasset/repository 0.091s`.

### Task 4 review amendment — Reconcile probe-to-commit Task archive RED

- UTC timestamp: `2026-08-17T09:10:02Z`
- Command: `cd backend && go test ./internal/backupasset/repository -run '^TestReconcileRejectsTaskArchivedDuringProbePreservingLastGoodFacts$' -count=1`
- Exit status: `1`
- Expected failure category: behavioral stale-lineage RED; the scripted reconcile probe archived the binding Task after runtime load, but Reconcile returned success and committed the new observation from the stale Task/runtime snapshot.
- Concise output: `Task archived during reconcile probe error=<nil>`; package failed in `0.092s`.

### Task 4 review amendment — Reconcile probe-to-commit Task archive GREEN

- UTC timestamp: `2026-08-17T09:11:02Z`
- Same command: `cd backend && go test ./internal/backupasset/repository -run '^TestReconcileRejectsTaskArchivedDuringProbePreservingLastGoodFacts$' -count=1`
- Exit status: `0`
- Result: Reconcile snapshots the exact binding Task link before the probe, then locks and revalidates Task, Node/credential, and Task-link lineage before repository and active-binding locks; archive drift returns typed `ErrConflict` and preserves repository, binding, and point last-good facts.
- Concise output: `ok xirang/backend/internal/backupasset/repository 0.091s`.

### Task 4 review amendment — final verification gates

- At UTC `2026-08-17T09:13:22Z`, the explicit Rsync/Rclone proof matrix plus stored-proof revalidation and mutable no-fake-history selector exited `0` in `0.353s`.
- At UTC `2026-08-17T09:13:23Z`, the Connect/Reconcile archive, Provider, Node, and Task-link probe-drift matrix exited `0` in `0.282s`; the retained-salt, retired-head, and failed-reconcile last-good selector exited `0` in `0.161s`.
- At UTC `2026-08-17T09:16:01Z`, the required Task 4 selector `cd backend && go test ./internal/backupasset/repository -run '^(TestDisconnectPreservesMutableEvidenceAndReconnectsWithRetainedSalt|TestLifecycleReconnectRetiredHeadDoesNotReactivate|TestImport|TestRebuild)' -count=1` exited `0` in `0.512s`.
- The same required selector with `-count=10` exited `0` in `0.548s`; the focused proof/drift race selector exited `0` in `2.783s`.
- At UTC `2026-08-17T09:16:01Z`, the full repository package exited `0` in `5.663s`; at UTC `2026-08-17T09:16:31Z`, the final full repository package under `go test -race` exited `0` in `16.134s`.
- `go vet ./internal/backupasset/repository`, `gofmt -d` over every Task 4 Go path, and tracked `git diff --check` exited `0` with no output.
- Privacy audit found no new print/logger flow and no raw locator, credential, or encrypted evidence in result/view or verifier-port shapes. The managed proof owner receives only opaque repository/provider/candidate/marker/commit digests; private locator/evidence remains confined to encrypted model fields and transaction-local validation.
- Scope audit confirms the amendment changed only authorized Task 4 repository import/connect/reconcile/service tests and production files plus this chronological evidence. Concurrent Task1-3/schema/retention/auth/web changes, including `repository/testutil_test.go`, were preserved untouched.

### Task 4 targeted re-review — retained probe-owner Node/SSH lineage RED

- UTC timestamp: `2026-08-17T09:26:00Z`
- Command: `cd backend && go test ./internal/backupasset/repository -run '^TestConnectRejectsRetainedProbeOwnerNodeAndSSHCredentialDriftWithoutMutation$' -count=1`
- Exit status: `1`
- Expected failure category: behavioral stale-probe-owner RED; shared Restic reconnect probed through the retained binding Task, then accepted both retained Node access drift and retained SSH private-key drift because the commit guard locked only the requested linked Task lineage.
- Concise output: both `node_access` and `ssh_private_key` reported `drift connect error=<nil>`; package failed in `0.130s`.

### Task 4 targeted re-review — retained probe-owner Node/SSH lineage GREEN

- UTC timestamp: `2026-08-17T09:27:57Z`
- Same command: `cd backend && go test ./internal/backupasset/repository -run '^TestConnectRejectsRetainedProbeOwnerNodeAndSSHCredentialDriftWithoutMutation$' -count=1`
- Exit status: `0`
- Result: Connect now retains the exact Task/Node/SSH snapshot used to build Provider access and, after requested Task/Node/credential/link locks, locks and exactly revalidates the distinct retained probe owner before any repository/binding/point mutation. Node access or private credential drift returns typed `ErrConflict` with the repository graph unchanged.
- Concise output: `ok xirang/backend/internal/backupasset/repository 0.129s`.

### Task 4 targeted re-review — final verification gates

- At UTC `2026-08-17T09:29:16Z`, the required Task 4 selector exited `0` in `0.579s`; the complete `^TestImport` selector exited `0` in `0.521s`.
- At the same timestamp, the complete Connect/Reconcile probe-lineage drift selector, including retained probe-owner Node/SSH drift and SSH usage-metadata compatibility, exited `0` in `0.444s`; retained-salt, retired-head, and failed-reconcile regressions exited `0` in `0.168s`.
- At UTC `2026-08-17T09:29:29Z`, the required Task 4 selector with `-count=10` exited `0` in `4.179s`, the full repository package exited `0` in `6.454s`, and the full repository package under `go test -race` exited `0` in `16.485s`.
- `go vet ./internal/backupasset/repository`, `gofmt -d` for the targeted Connect files, tracked `git diff --check`, and explicit trailing-whitespace scans exited cleanly.
- Privacy audit found no new print/logger/JSON boundary and no credential value in errors, DTOs, audit, or logs. Retained probe snapshots remain transaction-local; exact SSH authority comparison includes private key/type/fingerprint/disabled/expiry/scopes while intentionally excluding mutable `LastUsedAt` usage metadata.
- Targeted scope audit confirms this re-review changed only `repository/connect.go`, `repository/connect_test.go`, and this chronological evidence. All concurrent Task1-3/schema/retention/auth/web and existing Task4 import/rebuild/reconcile edits were preserved.

### Task 4 second targeted re-review — retained active-binding replacement RED

- UTC timestamp: `2026-08-17T09:35:09Z`
- Command: `cd backend && go test ./internal/backupasset/repository -run '^TestConnectRejectsActiveBindingReplacementDuringRetainedProbeWithoutMutation$' -count=1`
- Exit status: `1`
- Expected failure category: behavioral stale-binding RED; a real exact-repository `ReplaceAccess` committed while the retained Restic probe was in flight, but the stale reconnect returned success and refreshed the new active binding with its pre-probe document.
- Concise output: `active binding replaced during retained probe connect error=<nil>`; package failed in `0.092s`.

### Task 4 second targeted re-review — retained active-binding replacement GREEN

- UTC timestamp: `2026-08-17T09:36:30Z`
- Same command: `cd backend && go test ./internal/backupasset/repository -run '^TestConnectRejectsActiveBindingReplacementDuringRetainedProbeWithoutMutation$' -count=1`
- Exit status: `0`
- Result: retained access now carries the exact active binding used to construct probe access. After requested/retained Task lineage locks, Connect locks the repository and exact binding `FOR UPDATE`, compares binding identity/repository/kind/status/config/fingerprint/create-update/revocation lineage, and returns typed `ErrConflict` before mutation if an exact-repository replacement won the race.
- Concise output: `ok xirang/backend/internal/backupasset/repository 0.093s`.

### Task 4 second targeted re-review — final verification gates

- At UTC `2026-08-17T09:37:11Z`, the required Task 4 selector exited `0` in `0.541s`; the complete `^TestImport` selector exited `0` in `0.487s`.
- At the same timestamp, the complete Connect/Reconcile drift selector including active-binding replacement exited `0` in `0.452s`; retained-salt, retired-head, and failed-reconcile regressions exited `0` in `0.180s`.
- At UTC `2026-08-17T09:37:23Z`, the required selector with `-count=10` exited `0` in `4.164s`, the full repository package exited `0` in `6.467s`, and the full repository package under `go test -race` exited `0` in `16.321s`.
- `go vet ./internal/backupasset/repository`, `gofmt -d` for targeted Connect files, tracked `git diff --check`, and explicit trailing-whitespace scans exited cleanly.
- Privacy audit confirmed binding documents/fingerprints/credentials remain comparison-only or encrypted-storage inputs: no new print/logger/JSON/error/audit surface includes them, and conflict text remains generic.
- Scope audit confirms this second re-review changed only `repository/connect.go`, `repository/connect_test.go`, and this chronological evidence; concurrent work was preserved.

### Task 4 quality review — Reconcile repository/binding snapshot fence RED

- UTC timestamp: `2026-08-17T09:48:49Z`
- Command: `cd backend && go test ./internal/backupasset/repository -run '^TestReconcileRejectsRepositoryAndBindingRefreshDuringProbePreservingCommittedFacts$' -count=1`
- Exit status: `1`
- Expected failure category: behavioral stale-runtime RED; the probe independently advanced repository capability facts and the exact active binding adapter/config lineage, but stale Reconcile returned success and overwrote those committed facts.
- Concise output: `repository/binding refreshed during reconcile probe error=<nil>`; package failed in `0.094s`.

### Task 4 quality review — Reconcile repository/binding snapshot fence GREEN

- UTC timestamp: `2026-08-17T09:49:50Z`
- Same command: `cd backend && go test ./internal/backupasset/repository -run '^TestReconcileRejectsRepositoryAndBindingRefreshDuringProbePreservingCommittedFacts$' -count=1`
- Exit status: `0`
- Result: after Task/Node/credential/link locks, Reconcile locks and compares the exact repository identity/status/capability lineage and active binding identity/config/adapter/fingerprint/revocation lineage captured before the probe; drift returns generic typed `ErrConflict` before mutation and preserves the committed winner.
- Concise output: `ok xirang/backend/internal/backupasset/repository 0.092s`.

### Task 4 quality review — Restic trusted snapshot identity RED

- UTC timestamp: `2026-08-17T09:52:08Z`
- Command: `cd backend && go test ./internal/backupasset/repository -run '^TestImportResticTrustedCandidatesRequireFullNativeSnapshotIdentity$' -count=1`
- Exit status: `1`
- Expected failure category: behavioral trust-boundary RED; discovery and persisted-review validation admitted `imported_baseline` Restic candidates with arbitrary, uppercase, short, long, or locator-mismatched snapshot identities.
- Concise output: five invalid discovery cases reported `invalid Restic snapshot identity admitted: <nil>` and persisted arbitrary baseline review reported `accepted: <nil>`; package failed in `0.089s`.

### Task 4 quality review — Restic trusted snapshot identity GREEN

- UTC timestamp: `2026-08-17T09:53:02Z`
- Same command: `cd backend && go test ./internal/backupasset/repository -run '^TestImportResticTrustedCandidatesRequireFullNativeSnapshotIdentity$' -count=1`
- Exit status: `0`
- Result: both native and reviewed-baseline Restic candidates now require an exact lowercase 64-hex native snapshot identity shared by source revision and private locator; invalid persisted evidence fails closed while the valid boundary remains importable.
- Concise output: `ok xirang/backend/internal/backupasset/repository 0.084s`.

### Task 4 quality review — bounded rebuild page contract RED

- UTC timestamp: `2026-08-17T09:54:32Z`
- Command: `cd backend && go test ./internal/backupasset/repository -run '^TestRebuildAcceptedImportsIsBoundedAndCursorStable$' -count=1`
- Exit status: `1`
- Expected failure category: compile-time contract RED; rebuild exposed neither a bounded request/continuation nor a next cursor and therefore could only load and invoke owners for every accepted candidate in one call.
- Concise output: compiler reported undefined `RebuildRequest`, `maxRebuildPageSize`, and `RebuildResult.NextCursor`, plus the unbounded three-argument method signature.

### Task 4 quality review — bounded rebuild page contract GREEN

- UTC timestamp: `2026-08-17T09:55:07Z`
- Same command: `cd backend && go test ./internal/backupasset/repository -run '^TestRebuildAcceptedImportsIsBoundedAndCursorStable$' -count=1`
- Exit status: `0`
- Result: each invocation now validates a bounded limit, loads at most `limit+1` accepted candidates in deterministic `(created_at,id)` order, invokes Catalog/Derived owners only for the current page, and returns an accepted-candidate continuation with no duplicates across pages.
- Concise output: `ok xirang/backend/internal/backupasset/repository 0.088s`.

### Task 4 quality review — terminal accepted disposition replay RED

- UTC timestamp: `2026-08-17T09:55:52Z`
- Command: `cd backend && go test ./internal/backupasset/repository -run '^TestImportMutableCandidateRequiresExplicitBaselineAndNeverReactivatesRetiredHead$' -count=1`
- Exit status: `1`
- Expected failure category: behavioral terminal-replay RED; after exact imported-baseline replay succeeded idempotently, replaying the same accepted mutable candidate with the contradictory mutable-head disposition also returned success.
- Concise output: `contradictory accepted disposition replay error=<nil>`; package failed in `0.092s`.

### Task 4 quality review — terminal accepted disposition replay GREEN

- UTC timestamp: `2026-08-17T09:56:48Z`
- Same command: `cd backend && go test ./internal/backupasset/repository -run '^TestImportMutableCandidateRequiresExplicitBaselineAndNeverReactivatesRetiredHead$' -count=1`
- Exit status: `0`
- Result: acceptance and replay now share one disposition normalizer; exact imported-baseline replay remains idempotent, while a contradictory mutable-head replay returns typed `ErrConflict` and leaves both retired and accepted points unchanged.
- Concise output: `ok xirang/backend/internal/backupasset/repository 0.099s`.

### Task 4 quality review — concurrent discovery unique-collision RED

- UTC timestamp: `2026-08-17T09:58:42Z`
- Command: `cd backend && go test ./internal/backupasset/repository -run '^TestImportConcurrentDiscoveryUniqueCollisionRequiresExactWinner$' -count=1`
- Exit status: `1`
- Expected failure category: behavioral uniqueness-race RED; after the pre-create lookup missed, both an exact injected winner and a winner with mismatched private evidence surfaced the raw unique-constraint create error instead of exact idempotence or typed conflict.
- Concise output: both cases returned `create import candidate: UNIQUE constraint failed` and the package failed in `0.129s`; private evidence values were not printed.

### Task 4 quality review — concurrent discovery unique-collision GREEN

- UTC timestamp: `2026-08-17T09:59:14Z`
- Same command: `cd backend && go test ./internal/backupasset/repository -run '^TestImportConcurrentDiscoveryUniqueCollisionRequiresExactWinner$' -count=1`
- Exit status: `0`
- Result: the active source-fingerprint unique race now locks/reloads the winner and returns it idempotently only when repository, provider, kind, semantics, fingerprint, and decrypted private locator/evidence identity validate and match exactly; mismatched evidence returns generic typed `ErrConflict`.
- Concise output: `ok xirang/backend/internal/backupasset/repository 0.127s`.

### Task 4 quality review — combined and final verification gates

- At UTC `2026-08-17T09:59:38Z`, the combined `TestImport.*`, `TestRebuild.*`, and Reconcile repository/binding probe-fence selector exited `0` in `0.485s`.
- At UTC `2026-08-17T10:00:03Z`, the required Task 4 selector exited `0` in `0.504s`; the expanded import/rebuild plus Connect/Reconcile probe-drift matrix with `-count=10` exited `0` in `6.121s`.
- At UTC `2026-08-17T10:00:22Z`, the exact required Task 4 selector with `-count=10` exited `0` in `3.985s`; retained-salt, retired-head, failed-reconcile last-good, and the new Reconcile snapshot fence with `-count=10` exited `0` in `0.794s`.
- At UTC `2026-08-17T10:00:33Z`, the full repository package exited `0` in `5.716s`; at UTC `2026-08-17T10:00:44Z`, the full repository package under `go test -race` exited `0` in `16.721s`.
- At UTC `2026-08-17T10:01:15Z`, `go vet ./internal/backupasset/repository`, `gofmt -d` over all touched Task 4 Go files, and tracked `git diff --check` exited `0` with no output. At UTC `2026-08-17T10:01:21Z`, package-scoped `golangci-lint` exited `0` with `0 issues`.
- Privacy audit found no new print/logger/JSON/error/audit path carrying native locator, encrypted evidence/config, source fingerprint, or credential material. Rebuild exposes only counts/reasons plus an opaque candidate continuation; unique-race mismatch and Reconcile fence errors remain generic and typed.
- Scope audit confirms this review changed only Task 4 repository `import`, `rebuild`, and `reconcile` production/tests plus this chronological evidence. Concurrent Task1-3/schema/retention/auth/web changes and the shared `repository/testutil_test.go` fixture were preserved untouched.

### Task 4 cross-engine review — PostgreSQL import-collision RED

- UTC timestamp: `2026-08-17T10:11:54Z`
- Command: required real-PostgreSQL execution of `cd backend && go test ./internal/backupasset/repository -run '^TestImportConcurrentDiscoveryUniqueCollisionPostgres$' -count=1`; the ephemeral DSN and random credential were intentionally omitted.
- Exit status: `1`
- Expected failure category: behavioral cross-engine RED; after the concurrent unique winner committed, both exact and private-evidence-mismatch losers issued a failing INSERT and then attempted winner reload in PostgreSQL's aborted transaction.
- Concise output: both collision cases returned `reload import candidate race winner: current transaction is aborted (SQLSTATE 25P02)`; the independent unrelated CHECK-constraint case passed without being relabeled.
- Fixture evidence: PostgreSQL 17 ran in one disposable, loopback-only `--rm` container without a host volume. Bounded readiness succeeded; the container, its exact anonymous volume, and shell-local credential/DSN were removed or unset, and post-run cleanup verification passed. No credential, DSN, locator, or evidence payload was printed or recorded.

### Task 4 cross-engine review — PostgreSQL import-collision GREEN

- UTC timestamp: `2026-08-17T10:12:56Z`
- Same required real-PostgreSQL selector: `cd backend && go test ./internal/backupasset/repository -run '^TestImportConcurrentDiscoveryUniqueCollisionPostgres$' -count=1`; the ephemeral DSN and credential were intentionally omitted.
- Exit status: `0`
- Result: candidate creation now uses dialect-neutral `ON CONFLICT (repository_id, source_fingerprint) DO NOTHING`. A zero-row insert locks/reloads and strictly validates the winner without aborting PostgreSQL; exact contenders converge idempotently, private-evidence mismatch returns typed `ErrConflict`, and an unrelated CHECK failure still surfaces as an insert error.
- Concise output: `ok xirang/backend/internal/backupasset/repository 0.251s`.
- Fixture cleanup: the loopback-only PostgreSQL 17 container, its exact anonymous volume, and shell-local credential/DSN were again removed or unset; post-run cleanup verification passed with no private payload output.

### Task 4 cross-engine review — final verification gates

- At UTC `2026-08-17T10:13:20Z`, the existing injected SQLite exact/mismatched-winner selector exited `0` in `0.130s`.
- The first adjacent SQLite import run exposed that its AutoMigrate-only candidate fixtures omitted the production repository/source-fingerprint unique index required by an explicit conflict target. Test setup was corrected to install that exact production index in every import fixture; no production behavior was changed for the fixture correction.
- At UTC `2026-08-17T10:16:50Z`, the required Task 4 selector exited `0` in `0.475s`, the complete Import/Rebuild plus Connect/Reconcile probe-drift selector exited `0` in `0.661s`, and the required selector with `-count=10` exited `0` in `3.911s`.
- At UTC `2026-08-17T10:15:09Z`, the full repository package exited `0` in `5.604s`; at UTC `2026-08-17T10:15:23Z`, the full repository package under `go test -race` exited `0` in `16.308s`.
- At UTC `2026-08-17T10:15:58Z`, package `go vet`, `golangci-lint`, `gofmt -d`, and tracked `git diff --check` exited `0`; lint reported `0 issues`. Explicit untracked-file whitespace and private-value formatting scans were clean.
- Final fixture audit found no `xirang-import-*` container or PostgreSQL test environment variable. Both required PostgreSQL runs independently verified removal of their exact anonymous volume; the invalid preliminary harness run's exact volume was also identified by its matching creation timestamp and removed before the valid RED was captured.
- Privacy audit found no raw locator, evidence, credential, DSN, or random fixture credential in logs, errors, evidence, JSON, or test output. GORM fixture logging is silent; conflict and validation errors remain generic.
- Scope audit confirms this cross-engine correction changed only `repository/import.go`, `repository/import_test.go`, and this chronological Task 4 evidence. Provider, Catalog, runtime, migrations, Task5+, `.codex`, and concurrent shared edits were preserved untouched.

## Task 3 integration-fixture follow-up — Step-up matrix and snapshot Foundation

### Handler pairwise step-up matrix RED

- UTC timestamp: `2026-08-17T10:30:46Z`
- Command: `cd backend && go test ./internal/api/handlers -run '^TestStepUpProofPairwiseCrossPurposeRejection$' -count=1`
- Exit status: `1`
- Expected failure category: exhaustive expectation RED; all pairwise purpose-isolation checks ran against the live 18-action registry, but the final cardinality assertion remained frozen at 17 matching and 272 rejected pairs.
- Concise output: `pairwise matrix accepted=18 rejected=306, want 17/272`; package `FAIL` in `0.062s`.

### Snapshot Foundation retention defaults RED

- UTC timestamp: `2026-08-17T10:30:51Z`
- Command: `cd backend && go test ./internal/snapshot -run '^(TestIndexer|TestEnsureIndexed)' -count=1`
- Exit status: `1`
- Expected failure category: integration-fixture RED; the snapshot Foundation fixture omitted Task 3 retention settings, so exact indexing could not validate the complete Foundation snapshot and its scheduled build never reached provider listing.
- Concise output: three failures—two invalid/missing `backup_assets.retention_reconcile_interval` errors and one bounded scheduled-build timeout; package `FAIL` in `3.019s`.

### Task 4 stale reconcile-failure authority RED

- UTC timestamp: `2026-08-17T10:31:46Z`
- Command: `cd backend && go test ./internal/backupasset/repository -run '^TestReconcileStaleProbeFailureDoesNotDowngradeNewerAuthority$' -count=1`
- Exit status: `1`
- Expected failure category: behavioral stale-failure RED; both an old Provider error and an old invalid observation recorded failure after a newer repository/binding/mutable-point reconcile had committed.
- Concise output: Provider-error and invalid-observation cases returned the old probe/validation error, reported `preserved=false`, and emitted one stale failure audit each; package failed in `0.112s`.

### Handler pairwise step-up matrix GREEN

- UTC timestamp: `2026-08-17T10:31:29Z`
- Same command: `cd backend && go test ./internal/api/handlers -run '^TestStepUpProofPairwiseCrossPurposeRejection$' -count=1`
- Exit status: `0`
- Result: the test still executes every issued/expected action pair and rejects every cross-purpose proof; its completeness cardinality is derived from the live registry, yielding 18 matching and 306 rejected pairs without weakening isolation.
- Concise output: `ok xirang/backend/internal/api/handlers 0.064s`.

### Snapshot Foundation retention defaults GREEN

- UTC timestamp: `2026-08-17T10:31:36Z`
- Same command: `cd backend && go test ./internal/snapshot -run '^(TestIndexer|TestEnsureIndexed)' -count=1`
- Exit status: `0`
- Result: the snapshot Foundation fixture now includes retention reconcile interval `5m`, batch size `100`, and drain timeout `30s`; `backup_assets.enabled` remains `false` while all exact-index fixture paths complete.
- Concise output: `ok xirang/backend/internal/snapshot 0.016s`.

### Task 4 stale reconcile-failure authority GREEN

- UTC timestamp: `2026-08-17T10:33:16Z`
- Same command: `cd backend && go test ./internal/backupasset/repository -run '^TestReconcileStaleProbeFailureDoesNotDowngradeNewerAuthority$' -count=1`
- Exit status: `0`
- Result: Provider-error and invalid-observation recording now reuse the exact pre-probe Task/Node/credential/link/repository/binding snapshots. The failure transaction locks and revalidates that authority in deterministic order before repository or mutable-point mutation; stale work returns typed `ErrConflict`, preserves the newer graph exactly, and emits no stale failure audit.
- Concise output: `ok xirang/backend/internal/backupasset/repository 0.271s`.

### Task 3 integration-fixture final gates

- At UTC `2026-08-17T10:31:59Z`, `cd backend && go test ./internal/api/handlers ./internal/snapshot -count=1` exited `0`; handlers completed in `4.672s` and snapshot in `0.015s`.
- At UTC `2026-08-17T10:32:05Z`, the focused handler pairwise plus snapshot indexer/EnsureIndexed selector under `go test -race` exited `0`; handlers completed in `1.586s` and snapshot in `1.065s`.
- The final reviewed-count handler selector re-run exited `0` in `0.064s`; `go vet ./internal/api/handlers ./internal/snapshot` exited `0` with no output.
- The backend-wide gate was coordinated with the Task 4 implementer and ran from UTC `2026-08-17T10:33:18Z` through `2026-08-17T10:34:48Z`: `cd backend && go test ./...` exited `0`; all packages passed, including handlers `6.920s`, repository `9.794s`, retention `0.326s`, database `31.097s`, snapshot `0.037s`, and task `3.887s`.
- Final `gofmt -d`, tracked and explicit evidence whitespace checks exited `0` with no output. The snapshot fixture contains no enabled-true override, and the two-file diff introduces no locator, fence, encrypted-config, private-key, or credential payload.

### Task 4 stale reconcile-failure final gates

- At UTC `2026-08-17T10:33:35Z`, every `TestReconcile*` selector exited `0` in `0.636s`, including genuine current-authority Provider failure/invalid observation still marking repository and mutable point offline.
- At UTC `2026-08-17T10:33:51Z`, the complete Reconcile, Connect probe-drift, Import, and Rebuild matrix exited `0` in `0.969s`; the required Task 4 selector exited `0` in `0.547s`; and stale-failure, current-failure, and successful-reconcile-fence regressions with `-count=10` exited `0` in `0.801s`. The required Task 4 selector separately exited `0` with `-count=10` in `4.002s`.
- At UTC `2026-08-17T10:34:01Z`, the full repository package exited `0` in `5.810s`; at UTC `2026-08-17T10:34:11Z`, the full repository package under `go test -race` exited `0` in `16.495s`.
- The coordinated backend-wide `go test ./...` gate started after the focused Task 4 GREEN and exited `0`; its repository package completed in `9.794s`.
- At UTC `2026-08-17T10:34:37Z`, package `go vet`, `golangci-lint`, `gofmt -d`, and tracked `git diff --check` exited `0`; lint reported `0 issues`. Explicit whitespace and private-value formatting scans also exited cleanly.
- Privacy audit found no raw Task access, Node/SSH credential, binding config/fingerprint, locator, evidence, or Provider error detail in result, audit, log, or new error text. Stale authority conflicts are generic; no stale failure audit is emitted.
- Scope audit confirms this finding changed only `repository/reconcile.go`, `repository/reconcile_test.go`, and this coordinated chronological evidence. All Task3 fixture/evidence additions and other concurrent shared work were preserved without interleaving or reversion.

## Task 5 — lifecycle claim and private lease fence

### RED

- UTC timestamp: `2026-08-17T10:51:51Z`
- Command: `cd backend && go test ./internal/backupasset/retention -run '^TestLifecycleClaimMovesCommittedPointToExpiringWithLeaseFence$' -count=1`
- Exit status: `1`
- Expected failure category: compile-time contract RED caused only by the absent Task 5 lifecycle coordinator, dependency contract, and exact claim request.
- Concise output: compiler reported undefined `NewCoordinator`, `CoordinatorDependencies`, and `ClaimRequest`; package build failed.

### GREEN

- UTC timestamp: `2026-08-17T10:53:35Z`
- Same command: `cd backend && go test ./internal/backupasset/retention -run '^TestLifecycleClaimMovesCommittedPointToExpiringWithLeaseFence$' -count=1`
- Exit status: `0`
- Result: one transaction revalidates the exact selected point/revisions/hold projection, acquires a `retention_worker` lease, stores only the SHA-256 fence-token digest on the durable attempt, and advances `committed -> expiring` plus `point_revision` exactly once without changing capability revision.
- Concise output: `ok xirang/backend/internal/backupasset/retention 0.008s`.

## Task 5 — active-attempt late lease admission fence

### RED

- UTC timestamp: `2026-08-17T10:55:53Z`
- Command: `cd backend && go test ./internal/backupasset/retention -run '^TestLeaseAdmissionRejectsActiveLifecycleAttempt$' -count=1`
- Exit status: `1`
- Expected failure category: behavioral admission RED; after a lifecycle claim committed, a new content-session lease was still admitted for the exact point.
- Concise output: `late lifecycle admission error=<nil>, want ErrLeaseHeld`; package failed in `0.008s`. No lease fence or private token was printed or recorded.

### GREEN

- UTC timestamp: `2026-08-17T10:56:56Z`
- Same command: `cd backend && go test ./internal/backupasset/retention -run '^TestLeaseAdmissionRejectsActiveLifecycleAttempt$' -count=1`
- Exit status: `0`
- Result: coordinator composition installs a transaction-bound lease-admission fence; every ordinary lease acquisition locks the exact point and rejects an active lifecycle attempt or terminalizing point state before any lease row is written, while the coordinator's own `retention_worker` claim remains the sole bypass.
- Concise output: `ok xirang/backend/internal/backupasset/retention 0.008s`.

## Task 5 — live-lease block, durable retry, and bounded takeover

### RED

- UTC timestamp: `2026-08-17T10:58:28Z`
- Command: `cd backend && go test ./internal/backupasset/retention -run '^TestLifecycleLiveLeaseBlocksDrainingAndRetriesAfterBoundedTakeover$' -count=1`
- Exit status: `1`
- Expected failure category: compile-time lifecycle contract RED caused only by the absent injected admission port, bounded retry configuration, phase advance API, and opaque point request.
- Concise output: compiler reported missing `Admissions`, `RetryDelay`, `Advance`, and `LifecyclePointRequest`; package build failed.

### GREEN

- UTC timestamp: `2026-08-17T11:02:58Z`
- Same command: `cd backend && go test ./internal/backupasset/retention -run '^TestLifecycleLiveLeaseBlocksDrainingAndRetriesAfterBoundedTakeover$' -count=1`
- Exit status: `0`
- Result: the exact attempt fence advances through selection, revocation, and draining; a live ordinary lease durably closes the point as `purge_blocked/lease_live`. A bounded retry reacquires the lifecycle fence after its short deadline, restores `expiring`, and only after the ordinary lease's short deadline uses the lease service's fenced takeover and release path before entering cleanup. The former ordinary fence no longer validates.
- Concise output: `ok xirang/backend/internal/backupasset/retention 0.009s`.

## Task 5 — separately authorized exact mutable purge

### RED

- UTC timestamp: `2026-08-17T11:12:59Z`
- Command: `cd backend && go test ./internal/backupasset/retention -run '^TestPurgeExplicitMutableHeadRequiresExactPlanAndDeletesOnlyExactPoint$' -count=1`
- Exit status: `1`
- Expected failure category: compile-time lifecycle contract RED caused only by the absent exact purge-plan snapshot on lifecycle claim.
- Concise output: compiler reported missing `ClaimRequest.PurgePlan` and undefined `PurgePlanSnapshot`; package build failed.

### GREEN

- UTC timestamp: `2026-08-17T11:13:58Z`
- Same command: `cd backend && go test ./internal/backupasset/retention -run '^TestPurgeExplicitMutableHeadRequiresExactPlanAndDeletesOnlyExactPoint$' -count=1`
- Exit status: `0`
- Result: explicit purge requires an executing, unexpired exact plan/actor/revision/item snapshot and matching point/capability revisions; it separately moves a retired mutable head to `expiring`, deletes only the exact opaque point through the injected port, clears its protected rollback locator only after an absence receipt, and terminates as `expired` with an explicit-purge tombstone.
- Concise output: `ok xirang/backend/internal/backupasset/retention 0.011s`.

## Task 5 — post-claim hold block and safe resume phase

### RED

- UTC timestamp: `2026-08-17T11:15:36Z`
- Command: `cd backend && go test ./internal/backupasset/retention -run '^TestLifecycleHoldAfterClaimBlocksAndRetryRestartsAtRevocation$' -count=1`
- Exit status: `1`
- Expected failure category: behavioral safe-resume RED; a hold appearing after claim correctly produced durable `purge_blocked/active_hold`, but releasing it resumed at `draining` even though revocation had never run.
- Concise output: retry returned phase `draining`, want conservative `revoking`; package failed in `0.009s`.

### GREEN

- UTC timestamp: `2026-08-17T11:16:04Z`
- Same command: `cd backend && go test ./internal/backupasset/retention -run '^TestLifecycleHoldAfterClaimBlocksAndRetryRestartsAtRevocation$' -count=1`
- Exit status: `0`
- Result: hold state and exact active-hold rows are revalidated transactionally after claim; the attempt and point durably close as `blocked/purge_blocked`. After release and bounded fence reacquisition, `active_hold` conservatively restarts the idempotent revocation chain instead of guessing a later phase.
- Concise output: `ok xirang/backend/internal/backupasset/retention 0.010s`.

### Task 5 provider/fence durable-block regression GREEN

- UTC timestamp: `2026-08-17T11:17:30Z`
- Command: `cd backend && go test ./internal/backupasset/retention -run '^(TestLifecycleProviderFailuresRemainPurgeBlockedAndRetryFenced|TestLifecycleFenceLossIsDurableAndNeverExposesFenceMaterial)$' -count=1`
- Exit status: `0`
- Result: WORM, Provider unavailable, and Provider identity conflict each map to their closed durable `purge_blocked` reason; retry reacquires the lifecycle fence and resumes exact deletion, while a replaced lifecycle fence fails closed and its raw material remains absent from the public attempt payload.
- Concise output: `ok xirang/backend/internal/backupasset/retention 0.025s`.

## Task 5 — real PolicyService facade for Task 1 exact-ID handoff

### RED

- UTC timestamp: `2026-08-17T11:19:20Z`
- Command: `cd backend && go test ./internal/backupasset/retention -run '^TestLifecycleManagedTaskFacadeSelectsOnlyExactHandoffRecoveryPointIDs$' -count=1`
- Exit status: `1`
- Expected failure category: compile-time integration contract RED caused only by the absent real managed-Task retention facade and dependency contract.
- Concise output: compiler reported undefined `NewManagedTaskRetentionFacade` and `ManagedTaskRetentionFacadeDependencies`; package build failed.

### GREEN

- UTC timestamp: `2026-08-17T11:20:27Z`
- Same command: `cd backend && go test ./internal/backupasset/retention -run '^TestLifecycleManagedTaskFacadeSelectsOnlyExactHandoffRecoveryPointIDs$' -count=1`
- Exit status: `0`
- Result: the real facade resolves the exact active Task/repository link and Task-link policy, invokes `PolicyService` selection, revalidates every Task handoff ID against the same Task/repository scope, intersects selected points with that opaque-ID set, and creates lifecycle claims only for the exact subset; an otherwise eligible outside ID remains committed.
- Concise output: `ok xirang/backend/internal/backupasset/retention 0.011s`.

### Task 1 exact selector unchanged GREEN

- UTC timestamp: `2026-08-17T11:20:27Z`
- Command: `cd backend && go test ./internal/task -run '^TestManagedTaskRetentionDelegatesExactRecoveryPointIDs$' -count=1`
- Exit status: `0`
- Result: the original Task 1 regression remains unchanged and still hands only sorted, deduplicated opaque RecoveryPoint IDs to `Manager.SetManagedRecoveryPointRetention`; no path/age selector reaches the port.
- Concise output: `ok xirang/backend/internal/task 0.014s`.

## Task 5 — exact claim retry idempotency

### RED

- UTC timestamp: `2026-08-17T11:21:36Z`
- Command: `cd backend && go test ./internal/backupasset/retention -run '^TestLifecycleClaimMovesCommittedPointToExpiringWithLeaseFence$' -count=1`
- Exit status: `1`
- Expected failure category: behavioral idempotency RED; repeating the identical policy snapshot after the first claim revalidated the already-advanced point before recognizing the durable active attempt.
- Concise output: repeat claim returned `ErrConflict` (`recovery point no longer matches policy selection`) instead of the same attempt; package failed in `0.011s`.

### GREEN

- UTC timestamp: `2026-08-17T11:22:14Z`
- Same command: `cd backend && go test ./internal/backupasset/retention -run '^TestLifecycleClaimMovesCommittedPointToExpiringWithLeaseFence$' -count=1`
- Exit status: `0`
- Result: claim now locks and recognizes an exact active attempt before revalidating an already-advanced point; an identical frozen policy/operation snapshot returns the same safe attempt without another lease or point revision, while contradictory active claims remain conflicts.
- Concise output: `ok xirang/backend/internal/backupasset/retention 0.010s`.

## Task 5 — restart-safe non-destructive mutable retirement

### RED

- UTC timestamp: `2026-08-17T11:09:10Z`
- Command: `cd backend && go test ./internal/backupasset/retention -run '^TestMutableHeadRetirementStaysObservedUntilCleanupAndNeverDeletesProvider$' -count=1`
- Exit status: `1`
- Expected failure category: compile-time lifecycle contract RED caused only by the absent exact mutable-head revision snapshot on lifecycle claim.
- Concise output: compiler reported missing `ClaimRequest.MutablePoint` and undefined `MutableRetirementSnapshot`; package build failed.

### GREEN

- UTC timestamp: `2026-08-17T11:11:34Z`
- Same command: `cd backend && go test ./internal/backupasset/retention -run '^TestMutableHeadRetirementStaysObservedUntilCleanupAndNeverDeletesProvider$' -count=1`
- Exit status: `0`
- Result: an exact mutable-head revision snapshot creates an admission-fenced attempt without changing `observed`; reconstructing the coordinator before every phase resumes the durable attempt, cleanup is invoked once, Provider deletion is never invoked, and only proven tombstoning moves the stable point ID to `retired`, preserves encrypted bytes as the protected rollback locator, and records the permanent `mutable_retired` tombstone.
- Concise output: `ok xirang/backend/internal/backupasset/retention 0.009s`.

## Task 5 — immutable expiry, deletion receipt, tombstone, and terminal fence

### RED

- UTC timestamp: `2026-08-17T11:05:13Z`
- Command: `cd backend && go test ./internal/backupasset/retention -run '^TestLifecycleImmutableExpiryPersistsDeletionTombstoneBeforeExpired$' -count=1`
- Exit status: `1`
- Expected failure category: compile-time lifecycle contract RED caused only by the absent injected owner-cleanup and exact point-deletion ports/result plus the unimplemented cleaning, Provider-delete, tombstoning, and completion phases.
- Concise output: compiler reported undefined `PointDeletionResult`/`PointDeletionDeleted` and missing `Cleanup`/`Deleter` coordinator dependencies; package build failed.

### GREEN

- UTC timestamp: `2026-08-17T11:07:21Z`
- Same command: `cd backend && go test ./internal/backupasset/retention -run '^TestLifecycleImmutableExpiryPersistsDeletionTombstoneBeforeExpired$' -count=1`
- Exit status: `0`
- Result: the coordinator uses narrow injected point-only cleanup and deletion ports, revalidates the exact lifecycle fence and hold before committing effects, persists the safe deletion receipt tombstone before terminal state, then atomically clears private locators, marks physical bytes missing/point expired, completes the attempt, and releases the lifecycle lease. Repeating a complete attempt has no cleanup or deletion effect.
- Concise output: `ok xirang/backend/internal/backupasset/retention 0.009s`.

## Task 5 — final focused and backend gates

- At UTC `2026-08-17T11:22:52Z`, the required selector `cd backend && go test ./internal/backupasset/retention -run '^(TestLifecycle|TestExpiry|TestPurge|TestMutableHeadRetirement|TestLease)' -count=1` exited `0` in `0.071s` after the final idempotent-claim change.
- The lifecycle/purge/mutable/lease selector with `-count=10` exited `0` in `0.659s`; the same focused selector under `go test -race` exited `0` in `1.198s`.
- The unchanged Task 1 selector exited `0` in `0.014s`. Complete `retention`, `backupasset`, and `task` package tests had already exited `0`; the final `cd backend && go test ./...` re-run at UTC `2026-08-17T11:29:05Z` exited `0` for every backend package.
- Final `go vet ./internal/backupasset/retention ./internal/backupasset ./internal/task` exited `0`; focused `golangci-lint` reported `0 issues`.
- `gofmt`, `git diff --check`, `.codex` status, static error/log scans, and manifest scope checks exited cleanly. The exact Task 5 product/test paths are `lease.go`, `retention/coordinator.go`, `coordinator_test.go`, `task_facade.go`, and `task_facade_test.go`; all are present in the independently approved live manifest.
- Privacy audit confirms the lifecycle attempt exposes no lease ID, attempt ID, raw fence, or fence digest in JSON; injected cleanup/deletion requests contain only exact opaque point/attempt IDs; private Provider/rollback locators never cross those ports or error/log text. Terminal locator mutation runs through model encryption hooks.
- At UTC `2026-08-17T11:30:55Z`, the existing retention PostgreSQL lock/CAS selector `go test ./internal/backupasset/retention -run '^TestPolicy.*Postgres' -count=1` exited `0` in `0.700s` against a disposable `postgres:17-alpine` fixture. It was loopback-only, `--rm`, used a random shell-local credential and no host volume; post-test container absence proved cleanup.
- `backup_assets.enabled` remains `CodeDefault: "false"`; no default-enable, Docker/deploy/GA, dependency, API/UI, `000071`, Task 6 owner implementation, or Task 7 Provider implementation was added.

## Task 5 review fix — pre-effect lifecycle authority

### RED

- UTC timestamp: `2026-08-17T11:44:37Z`
- Command: `cd backend && go test ./internal/backupasset/retention -run '^TestLifecycleExternalEffectsRequireCurrentFence$' -count=1`
- Exit status: `1`
- Expected failure category: behavioral authority RED; cleaning and Provider deletion invoked their irreversible fake ports before re-locking the lifecycle attempt, point, and current lease fence.
- Concise output: taken-over and absolute-deadline cleanup cases each observed one forbidden cleanup call; stale Provider authority invoked deletion and then persisted `provider_delete_unproven` instead of blocking `fence_lost`; package failed in `0.020s`.

### Late-hold admission RED

- UTC timestamp: `2026-08-17T11:48:28Z`
- Command: `cd backend && go test ./internal/backupasset/retention -run '^TestHoldCreateRejectsLateLifecycleAdmissionBeforeWrite$' -count=1`
- Exit status: `1`
- Expected failure category: behavioral admission RED; a legal hold was inserted for a `purge_blocked` point whose non-terminal lifecycle attempt already owned destructive authority.
- Concise output: `HoldService.Create` returned nil and persisted the late hold instead of returning `ErrConflict`; package failed in `0.021s`.

### GREEN

- UTC timestamp: `2026-08-17T11:49:26Z`
- Same selectors combined: `cd backend && go test ./internal/backupasset/retention -run '^(TestLifecycleExternalEffectsRequireCurrentFence|TestHoldCreateRejectsLateLifecycleAdmissionBeforeWrite)$' -count=1`
- Exit status: `0`
- Result: every external lifecycle port is preceded by a point/attempt/current-fence/hold transaction preflight, receives only opaque point/attempt IDs plus unexported safe authority under a context bounded by the lease, and is followed by an exact authority CAS. Invalid or absolute-expired fences durably block before cleanup/delete. `HoldService` now invokes a transaction-bound coordinator admission after locking the point and before inserting a hold, so an active lifecycle attempt rejects late hold admission with no row written.
- Concise output: `ok xirang/backend/internal/backupasset/retention 0.042s`.

## Task 5 review fix — atomic Task selection and lifecycle claim

### RED

- UTC timestamp: `2026-08-17T11:51:05Z`
- Command: `cd backend && go test ./internal/backupasset/retention -run '^TestLifecycleManagedTaskFacadeRollsBackAllClaimsOnTaskPolicyOrLaterPointDrift$' -count=1`
- Exit status: `1`
- Expected failure category: behavioral transaction RED; the facade did not compare the request's legacy Task policy ID with the locked current Task, and committed each selected point claim in a separate transaction.
- Concise output: a stale Task policy request unexpectedly succeeded; an injected second selected-point claim failure left one partial lifecycle attempt and its point mutation committed; package failed in `0.017s`.

### GREEN

- UTC timestamp: `2026-08-17T11:52:41Z`
- Same focused selector plus unchanged Task 1 regression: `cd backend && go test ./internal/backupasset/retention -run '^(TestLifecycleManagedTaskFacadeRollsBackAllClaimsOnTaskPolicyOrLaterPointDrift|TestLifecycleManagedTaskFacadeSelectsOnlyExactHandoffRecoveryPointIDs)$' -count=1 && go test ./internal/task -run '^TestManagedTaskRetentionDelegatesExactRecoveryPointIDs$' -count=1`
- Exit status: `0`
- Result: one transaction locks the current Task policy binding, resolves and re-locks the exact active Task/repository link, locks the exact handoff scope, performs `PolicyService.SelectWithTx`, and creates every exact selected-point attempt through `Coordinator.ClaimTx`. Policy drift, link drift, or a later claim failure persists zero partial attempts/point changes. Public `Claim` retains its validation and transaction wrapper.
- Concise output: retention facade selectors passed in `0.025s`; the unchanged Task 1 selector passed in `0.014s`.

## Task 5 review fix — deterministic lifecycle lock order

### RED

- UTC timestamp: `2026-08-17T11:54:31Z`
- Command: focused `TestLifecycleClaimAndAdvanceUseOnePostgresLockOrder` against a disposable `postgres:17-alpine` container using a random shell-local credential, loopback-only ephemeral port, `--rm`, no host volume, and a bounded five-second test context.
- Exit status: `1`
- Expected failure category: real PostgreSQL concurrency RED; idempotent `Claim` locked point then attempt while `Advance` locked attempt then point.
- Concise output: PostgreSQL returned SQLSTATE `40P01` (`deadlock detected`) for the active-attempt `FOR UPDATE`; the focused package failed in `1.112s`. The trap removed the disposable container and a post-run ancestor check returned zero containers.

### GREEN

- UTC timestamp: `2026-08-17T11:55:56Z`
- Same PostgreSQL selector: `TestLifecycleClaimAndAdvanceUseOnePostgresLockOrder` under the same disposable PostgreSQL 17 fixture contract; exit status `0`, concise output `ok ... 0.358s`.
- SQLite/race companion: `cd backend && go test -race ./internal/backupasset/retention -run '^TestLifecycleClaimAndAdvanceUseOneSQLiteLockOrder$' -count=10`; exit status `0`, concise output `ok ... 1.126s`.
- Result: `Claim` and every `Advance` transaction now use deterministic point-before-attempt locking; Advance first resolves the attempt's immutable point identity without a lock, then re-locks and verifies the exact point/attempt binding. The forced former deadlock interleaving completes on PostgreSQL, and repeated SQLite race execution preserves one idempotent attempt.
- Fixture cleanup: the PostgreSQL container used `--rm`, a loopback ephemeral port, no host volume, a random shell-local credential, and a bounded context; post-run ancestor count remained zero.

## Task 5 review fix — explicit purge admitted under an initial hold

### RED

- UTC timestamp: `2026-08-17T11:57:03Z`
- Command: `cd backend && go test ./internal/backupasset/retention -run '^TestExplicitPurgeWithInitialHoldCreatesBlockedAttemptAndResumesAfterRelease$' -count=1`
- Exit status: `1`
- Expected failure category: behavioral durable-intent RED; a valid explicit purge plan against a held point was rejected before creating a retryable lifecycle attempt.
- Concise output: `Claim` returned `ErrConflict: recovery point has an active hold`; package failed in `0.010s` instead of persisting the exact `purge_blocked/active_hold` attempt with zero effects.

### GREEN

- UTC timestamp: `2026-08-17T11:57:53Z`
- Same focused selector plus ordinary-selection exclusion: `cd backend && go test ./internal/backupasset/retention -run '^(TestExplicitPurgeWithInitialHoldCreatesBlockedAttemptAndResumesAfterRelease|TestPolicySelectionDeterministicExactScopesAndExclusions)$' -count=1`
- Exit status: `0`
- Result: an exact authorized explicit purge under an initial hold atomically creates its lease and attempt, advances the point to `purge_blocked`, and records attempt revision 2 with `active_hold`, a retry time, and zero external effects. Exact replay returns the same attempt. Hold release and fenced retry restore `expiring` and restart at revocation, then complete once. Ordinary retention selection still excludes both hold-projection and durable-hold rows.
- Concise output: `ok xirang/backend/internal/backupasset/retention 0.017s`.

## Task 5 review fix — fresh lifecycle fence after absolute deadline

### RED

- UTC timestamp: `2026-08-17T11:58:47Z`
- Command: `cd backend && go test ./internal/backupasset/retention -run '^TestLifecycleBlockedAttemptAdoptsFreshFenceAfterAbsoluteDeadline$' -count=1`
- Exit status: `1`
- Expected failure category: behavioral long-outage RED; a durable Provider-blocked attempt whose old lifecycle lease crossed its absolute deadline could not acquire new bounded authority.
- Concise output: restart left the same lease binding on attempt revision 7 and merely rewrote the durable reason to `fence_lost`; package failed in `0.013s` instead of expiring the exact old lease, adopting a fresh fence, and resuming the saved Provider phase.

### GREEN

- UTC timestamp: `2026-08-17T12:00:55Z`
- Same long-outage selector: `cd backend && go test ./internal/backupasset/retention -run '^TestLifecycleBlockedAttemptAdoptsFreshFenceAfterAbsoluteDeadline$' -count=1`; exit status `0`, concise output `ok ... 0.015s`.
- PostgreSQL concurrency selector: `TestLifecycleBlockedAttemptHasExactlyOnePostgresFenceAdopter` under the disposable PostgreSQL 17 fixture; exit status `0`, concise output `ok ... 0.119s`.
- Result: only a locked blocked attempt whose exact old retention lease binding is still active/expired and past its absolute deadline may CAS the old lease to expired, acquire one new bounded retention-worker lease, and update the attempt binding/revision. The old raw fence fails validation. Restart resumes the saved idempotent phase and later completes. Two PostgreSQL adopters produce exactly one success, one conflict, and one active fresh lifecycle lease.
- Fixture cleanup: loopback-only ephemeral port, `--rm`, random shell-local credential, no host volume, bounded execution; post-run ancestor count was zero.

## Task 5 review fix — lease-bounded effect cancellation

### RED

- UTC timestamp: `2026-08-17T12:01:51Z`
- Command: `cd backend && go test ./internal/backupasset/retention -run '^TestLifecycleExternalEffectDeadlineCancelsBeforeAuthorityExpires$' -count=1`
- Exit status: `1`
- Expected failure category: behavioral deadline classification RED; the cleanup fake observed the coordinator-provided deadline and stopped on cancellation without completing an irreversible effect, but the coordinator classified the expired authority as an owner cleanup failure.
- Concise output: attempt became `blocked/owner_cleanup_unproven` instead of the fail-closed `blocked/fence_lost`; package failed in `0.033s`.

### GREEN

- UTC timestamp: `2026-08-17T12:02:50Z`
- Same selector plus pre-effect authority/late-hold regressions: `cd backend && go test ./internal/backupasset/retention -run '^(TestLifecycleExternalEffectDeadlineCancelsBeforeAuthorityExpires|TestLifecycleExternalEffectsRequireCurrentFence|TestHoldCreateRejectsLateLifecycleAdmissionBeforeWrite)$' -count=1`
- Exit status: `0`
- Result: the external child context expires no later than current lifecycle lease authority; parent cancellation is returned, lease-deadline cancellation is durably classified `fence_lost`, and no post-effect transition is accepted after child deadline expiry. The blocking fake observed the deadline and cancellation and completed zero irreversible effects.
- Concise output: `ok xirang/backend/internal/backupasset/retention 0.092s`.

## Task 5 review fixes — final verification

- At UTC `2026-08-17T12:05:52Z`, the unchanged Task 5 selector `cd backend && go test ./internal/backupasset/retention -run '^(TestLifecycle|TestExpiry|TestPurge|TestMutableHeadRetirement|TestLease)' -count=1` exited `0` in `0.145s`.
- The adjacent policy/hold/auth selector exited `0` (`retention 0.134s`, `auth 0.050s`), and the unchanged Task 1 exact selector exited `0` in `0.013s`.
- The expanded lifecycle/expiry/purge/explicit-purge/mutable/lease/hold selector exited `0` in `1.034s`; `-count=10` exited `0` in `6.774s`; focused `-race` exited `0` in `2.867s`.
- Full touched-package tests passed (`retention 0.346s`, `backupasset 0.651s`, `task 2.968s`). Full touched-package race tests passed (`retention 1.734s`, `backupasset 2.634s`, `task 5.885s`).
- `cd backend && go test ./...` and `go build ./...` exited `0`. `go vet` for retention/backupasset/task exited `0`; focused `golangci-lint` exited `0` with `0 issues`.
- A final disposable PostgreSQL 17 run combined the forced lock-order regression, exactly-one expired-fence adopter regression, and existing Policy PostgreSQL selectors; it exited `0` in `1.170s`. The container used `--rm`, a loopback-only ephemeral port, no host volume, a random shell-local credential, and bounded readiness/execution. The cleanup trap ran and the post-run ancestor count was zero.
- `gofmt -d`, `git diff --check`, explicit untracked-file trailing-whitespace scan, and `.codex` status/diff checks produced no output. No `.codex` file was touched.
- Exact review-fix paths are `retention/coordinator.go`, `coordinator_test.go`, `hold.go`, `hold_test.go`, `task_facade.go`, `task_facade_test.go`, plus this evidence file; all seven occur in the independently approved `implement.md` manifest. No additional outside-manifest path was modified.
- Privacy scan found no DSN, random fixture credential, private key, raw Provider locator, or raw lifecycle fence in evidence/log/error/JSON surfaces. The request's effect authority is unexported and contains only opaque identities, revision, fence digest, and deadline; raw fence material remains persistence-private. Test-only fake encryption values remain explicitly fake.
- `backup_assets.enabled` remains `CodeDefault: "false"`. No Task 6/7 production owner, API/UI/runtime composition, deploy/GA, dependency, `000071`, or default-enable work was added.
- At UTC `2026-08-17T12:07:15Z`, after naming the Hold admission interface for the final public contract, the four affected lifecycle/hold/facade review selectors exited `0` in `0.083s` and focused `go vet` exited `0`.

## Task 5 final review fix — uncertain effect at authority deadline

### RED

- UTC timestamp: `2026-08-17T12:18:37Z`
- Command: `cd backend && go test ./internal/backupasset/retention -run '^TestLifecycleUncertainEffect(DeadlineDurablyBlocksWithoutReplay|DoesNotOverwriteNewerAuthority)$' -count=1`
- Exit status: `1`
- Expected failure category: production-time behavioral RED using a moving domain clock synchronized with the external effect context deadline. The short-expiry post-effect path attempted fence takeover, invalidated the captured authority, and rolled the transaction back; the absolute-deadline path returned the deadline error directly.
- Concise output: both attempts remained in destructive `cleaning` instead of durable `blocked/fence_lost`; the short-expiry subtest then invoked the uncertain cleanup a second time under another deadline. Errors were generic `lifecycle effect authority changed` / `lifecycle lease absolute deadline reached`; no fence token or private payload was printed. The newer-authority preservation subtest already passed. Package failed in `0.132s`.

### GREEN

- UTC timestamp: `2026-08-17T12:19:57Z`
- Same command: `cd backend && go test ./internal/backupasset/retention -run '^TestLifecycleUncertainEffect(DeadlineDurablyBlocksWithoutReplay|DoesNotOverwriteNewerAuthority)$' -count=1`
- Exit status: `0`
- Result: deadline uncertainty now uses a dedicated point-before-attempt transaction that never renews or takes over the effect lease. It proves the exact phase, attempt revision, point binding, lease ID/owner/holder/attempt, fence digest, and captured effective deadline even when the lease is short-expired or absolute-expired, then CASes the same attempt/point to durable `purge_blocked/fence_lost`. Restart before retry invokes no effect; a retry after the safe retry time rotates/adopts fresh authority, restarts at revocation, and completes. If a newer fence/revision already won, the stale worker receives `ErrConflict` and preserves the newer attempt and point unchanged.
- Privacy result: public blocked JSON contains neither the raw fence nor a fence field; the moving-clock tests and errors print no secret/private material.
- Concise output: `ok xirang/backend/internal/backupasset/retention 0.114s`.

### Final verification

- At UTC `2026-08-17T12:21:44Z`, all prior Task 5 review-fix selectors plus the new uncertain-effect selectors exited `0` in `0.229s`; the unchanged Task 1 exact selector exited `0` in `0.014s`.
- The expanded lifecycle/expiry/purge/explicit-purge/mutable/lease/hold selector passed `-count=10` in `3.157s` and focused `-race` in `1.790s`.
- Full touched packages passed (`retention 0.423s`, `backupasset 0.576s`, `task 2.874s`) and their full race run passed (`retention 1.894s`, `backupasset 2.637s`, `task 5.840s`).
- `go vet`, focused `golangci-lint` (`0 issues`), `go test ./...`, and `go build ./...` exited `0`.
- The real PostgreSQL 17 suite combining lock-order, exactly-one absolute-deadline adopter, and existing Policy concurrency selectors exited `0` in `1.166s`. Its random credential/DSN were shell-local and omitted; the container was loopback-only, `--rm`, had no host volume, used bounded readiness/execution, and the post-run ancestor count was zero.
- `gofmt -d`, `git diff --check`, explicit trailing-whitespace scan, and `.codex` status produced no output. Privacy scans found no DSN/private key/raw fence in evidence or production output surfaces; test-only raw fence fixtures remain confined to assertions and are never printed.
- This final fix modified only manifested `retention/coordinator.go`, `coordinator_test.go`, and this evidence file. `backup_assets.enabled` remains `CodeDefault: "false"`; no Task 6+ owner/runtime/API/UI/deploy/GA/dependency/`000071` work was added.

## Task 2 pre-release amendment — Composite tombstone event identity

### RED

- UTC timestamp: `2026-08-17T12:40:24Z`
- Command: `cd backend && go test ./internal/database -run '^TestBackupAssetMigration070(TombstoneCompositeHistorySQLite|SharedContractRegistersTombstoneCompositeHistory)$' -count=1`
- Exit status: `1`
- Expected failure category: schema/model contract RED proving tombstones still use the single-column RecoveryPoint primary key, so a valid later explicit purge cannot coexist with the earlier mutable-retire event.
- Concise output:

  ```text
  model primary key = [recovery_point_id], want [recovery_point_id terminal_operation]
  sqlite primary key = [recovery_point_id], want [recovery_point_id terminal_operation]
  second valid event: UNIQUE constraint failed: recovery_point_lifecycle_tombstones.recovery_point_id
  FAIL xirang/backend/internal/database
  ```

### GREEN

- UTC timestamp: `2026-08-17T12:41:01Z`
- Same command: `cd backend && go test ./internal/database -run '^TestBackupAssetMigration070(TombstoneCompositeHistorySQLite|SharedContractRegistersTombstoneCompositeHistory)$' -count=1`
- Exit status: `0`
- Result: SQLite PRAGMA and GORM metadata expose the ordered composite primary key `(recovery_point_id, terminal_operation)`; one mutable-retire and one later explicit-purge event coexist for the same point; duplicate same-operation insertion is rejected. The executable shared 000070 registry contains this check for both dialects.
- Concise output: `ok xirang/backend/internal/database 0.167s`.

### SQLite/model regressions

- UTC timestamp: `2026-08-17T12:41:34Z`
- Command: `cd backend && go test ./internal/model -run '^TestBackupAssetLifecycle' -count=2`
- Exit status: `0`
- Concise output: `ok xirang/backend/internal/model 0.093s`.

- UTC timestamp: `2026-08-17T12:41:35Z`
- Command: `cd backend && go test ./internal/database -run '^TestBackupAssetMigration070(SQLite|PairedFiles|UsedDownAdmissionSQLite)$' -count=1`
- Exit status: `0`
- Result: paired files, model parity, both-event permanent tombstone behavior, pristine down, and all used-down admissions pass.
- Concise output: `ok xirang/backend/internal/database 1.992s`.

- UTC timestamp: `2026-08-17T12:41:38Z`
- Command: `cd backend && go test ./internal/database -run '^TestBackupAssetMigration069(SQLite|PairedFiles)$' -count=1`
- Exit status: `0`
- Concise output: `ok xirang/backend/internal/database 7.744s`.

### Real PostgreSQL 17 verification

- Fixture: `postgres:17-alpine` (`sha256:18cfe3ef5e6815560c98237d6216d1e5119702fb0f3894c8785dd58b8bbe5d73`), unique container `xirang-task2-tombstone-pg17-20260817-1242-48629`, loopback-only `127.0.0.1:32805`, no host volume, `--rm`, random shell-local credential omitted, ready after 2 bounded polls.
- UTC timestamp: `2026-08-17T12:42:56Z`
- Command: `cd backend && REQUIRE_POSTGRES_MIGRATION_TEST=1 TEST_POSTGRES_DSN="$TEST_POSTGRES_DSN" go test ./internal/database -run '^TestBackupAssetMigration070TombstoneCompositeHistoryPostgres$' -count=1`
- Exit status: `0`
- Result: PostgreSQL reports the ordered composite key, preserves both valid events, and rejects a duplicate operation.
- Concise output: `ok xirang/backend/internal/database 1.653s`.

- UTC timestamp: `2026-08-17T12:42:59Z`
- Command: `cd backend && REQUIRE_POSTGRES_MIGRATION_TEST=1 TEST_POSTGRES_DSN="$TEST_POSTGRES_DSN" go test ./internal/database -run '^TestBackupAssetMigration070(Postgres|UsedDownAdmissionPostgres)$' -count=1`
- Exit status: `0`
- Concise output: `ok xirang/backend/internal/database 29.118s`.

- Cleanup UTC timestamp: `2026-08-17T12:46:05Z`
- Result: the fixture stopped and `--rm` removed it on the first bounded poll; an independent `docker inspect` confirmed absence and shell-local credential/DSN variables were unset.

### Full quality gates

- UTC timestamp: `2026-08-17T12:44:21Z`
- Command: `cd backend && go test ./internal/model ./internal/backupasset ./internal/database -count=1`
- Exit status: `0`
- Concise output: model `0.134s`, backupasset `0.571s`, database `24.972s`.

- UTC timestamp: `2026-08-17T12:44:54Z`
- Command: `cd backend && go test -race ./internal/model ./internal/backupasset ./internal/database -count=1`
- Exit status: `0`
- Concise output: model `1.302s`, backupasset `2.702s`, database `37.828s`.

- `cd backend && go vet ./internal/model ./internal/backupasset ./internal/database` exited `0` with no output.
- `cd backend && golangci-lint run ./internal/model ./internal/backupasset ./internal/database` first reported one test-only unchecked `rows.Close`; the focused helper was corrected within the owned integration test. The same lint command then exited `0` with `0 issues`, and the focused composite selector remained GREEN in `0.160s`.
- `gofmt -d` for both modified Go files and `git diff --check` produced no output.

### Required Task 5 follow-up (not edited here)

- `retention/coordinator.go` currently loads a tombstone by `recovery_point_id` with `Limit(1)`. With two event rows this is ambiguous; Task 5 must bind `terminal_operation = attempt.Operation` in that query and update affected tests. This amendment intentionally did not edit paused Task 5 files.

## Task 5 mandatory hold admission — RED

- UTC timestamp: `2026-08-17T12:51:47Z`
- Command: `cd backend && go test ./internal/backupasset/retention -run '^TestLifecycleCoordinatorRequiresHoldServiceAndRejectsLateHoldsBeforeProviderEffect$' -count=1`
- Exit status: `1`
- Result: the construction subtest proved `NewCoordinator` accepted a missing `HoldService` (`error=<nil>`), so lifecycle hold admission could be omitted. The same focused selector also contains forced legal and operational hold timing between provider preflight and the fake external effect.
- Concise output: `NewCoordinator without HoldService error=<nil>, want ErrInvalidState`.

## Task 5 mandatory hold admission — GREEN

- UTC timestamp: `2026-08-17T12:52:28Z`
- Same command: `cd backend && go test ./internal/backupasset/retention -run '^TestLifecycleCoordinatorRequiresHoldServiceAndRejectsLateHoldsBeforeProviderEffect$' -count=1`
- Exit status: `0`
- Result: coordinator construction now fails closed without the exact `HoldService`, always installs lifecycle admission when constructed, and forced legal/operational hold attempts cannot commit while a provider effect is paused after preflight. The test observes zero hold rows and zero completed deletion before releasing the fake effect; the rejected hold cannot interleave an unsafe write, and the effect remains outside the database transaction.
- Concise output: `ok xirang/backend/internal/backupasset/retention 0.054s`.

## Task 5 mutable retirement followed by explicit purge — RED

- UTC timestamp: `2026-08-17T12:55:16Z`
- Command: `cd backend && go test ./internal/backupasset/retention -run '^TestMutableRetirementThenExplicitPurgePreservesOperationTombstones$' -count=1`
- Exit status: `1`
- Result: a real mutable retirement completed and a real explicit purge for the same point reached terminalization, but the completion read was observed binding only the point rather than the immutable event identity `(recovery_point_id, terminal_operation)`.
- Concise output: `explicit purge completion tombstone lookup seen/exact=true/false, want point+terminal_operation binding`.

## Task 5 mutable retirement followed by explicit purge — GREEN

- UTC timestamp: `2026-08-17T12:55:41Z`
- Same command: `cd backend && go test ./internal/backupasset/retention -run '^TestMutableRetirementThenExplicitPurgePreservesOperationTombstones$' -count=1`
- Exit status: `0`
- Result: lifecycle completion now reads the exact immutable event by point and attempt operation. The real service path preserves both `mutable_retire` and `explicit_purge` rows and their distinct retirement/deletion receipts, and replaying both completed attempts performs no additional cleanup or Provider deletion.
- Concise output: `ok xirang/backend/internal/backupasset/retention 0.016s`.

## Task 5 selected absolute-deadline restart — RED

- UTC timestamp: `2026-08-17T12:57:30Z`
- Command: `cd backend && go test ./internal/backupasset/retention -run '^TestSelectedAttemptAbsoluteDeadlineDurablyBlocksAndRestartsWithFreshFence$' -count=1`
- Exit status: `1`
- Result: advancing a selected attempt after its persisted absolute deadline returned `lifecycle lease absolute deadline reached` and no attempt, leaving the destructive lifecycle selected instead of durably recording `purge_blocked/fence_lost`.
- Concise output: `attempt={zero value} error=lifecycle lease absolute deadline reached, want blocked/fence_lost/revision 2`.

- UTC timestamp: `2026-08-17T12:58:46Z`
- Command: `cd backend && go test ./internal/backupasset/retention -run '^TestDrainingAttemptAbsoluteDeadlineDurablyBlocksFenceLost$' -count=1`
- Exit status: `1`
- Result: the existing durable block path handled a draining absolute deadline, but mislabeled the exact authority loss as `lease_drain_unproven` rather than `fence_lost`.
- Concise output: `phase/reason=blocked/lease_drain_unproven, want blocked/fence_lost` (private lease and fence fields omitted).

## Task 5 selected/pre-effect absolute-deadline restart — GREEN

- UTC timestamp: `2026-08-17T12:59:25Z`
- Command: `cd backend && go test ./internal/backupasset/retention -run '^(TestSelectedAttemptAbsoluteDeadlineDurablyBlocksAndRestartsWithFreshFence|TestDrainingAttemptAbsoluteDeadlineDurablyBlocksFenceLost)$' -count=1`
- Exit status: `0`
- Result: selected and draining phases now verify the exact old point/attempt/lease authority in deterministic lock order and durably CAS to `purge_blocked/fence_lost` without renewal or takeover. Selected replay remains blocked before `retry_at`; the approved fresh-fence adoption path expires the old lease, resumes from revocation, survives restarts, completes once, and exposes no raw fence material.
- Concise output: `ok xirang/backend/internal/backupasset/retention 0.017s`.

### Lifecycle attempt formatting privacy — RED

- UTC timestamp: `2026-08-17T13:00:59Z`
- Command: `cd backend && go test ./internal/backupasset/retention -run '^TestSelectedAttemptAbsoluteDeadlineDurablyBlocksAndRestartsWithFreshFence$' -count=1`
- Exit status: `1`
- Result: the selected-deadline behavior was GREEN, but ordinary Go formatting of its safe-facing attempt value still included private lease/fence authority fields.
- Concise output: `selected deadline lifecycle formatting exposed private lease or fence material`.

### Lifecycle attempt formatting privacy — GREEN

- UTC timestamp: `2026-08-17T13:01:19Z`
- Same command: `cd backend && go test ./internal/backupasset/retention -run '^TestSelectedAttemptAbsoluteDeadlineDurablyBlocksAndRestartsWithFreshFence$' -count=1`
- Exit status: `0`
- Result: `LifecycleAttempt` now has safe `String`/`GoString` representations containing only operation, phase, transition revision, and safe blocked reason; JSON and formatted diagnostics omit lease IDs, lease attempts, raw tokens, and fence hashes.
- Concise output: `ok xirang/backend/internal/backupasset/retention 0.014s`.

## Task 5 remaining-finding final gates — GREEN

- UTC timestamp: `2026-08-17T13:06:02Z`
- Required Task 5 selector: `cd backend && go test ./internal/backupasset/retention -run '^(TestLifecycle|TestExpiry|TestPurge|TestMutableHeadRetirement|TestLease)' -count=1`; exit `0`, concise output `ok ... 0.291s`.
- Policy/hold selector: `cd backend && go test ./internal/backupasset/retention ./internal/auth -run '^(TestPolicy|TestHold|TestStepUpActionRetentionHoldRelease)' -count=1`; exit `0`, concise output retention `0.131s`, auth `0.051s`.
- Unchanged Task 1 selector: `cd backend && go test ./internal/task -run '^TestManagedTaskRetentionDelegatesExactRecoveryPointIDs$' -count=1`; exit `0`, concise output `ok ... 0.014s`. Its adjacent Task retention/pristine selector also passed in `0.024s`.
- Corrections/new-selector repeat: focused mandatory-hold, atomic facade, external-authority, uncertain-effect, lock-order, initial-hold, retire-purge, selected/draining deadline, and adoption set with `-count=10`; exit `0`, concise output `ok ... 2.253s`.
- Same correction set under `go test -race`; exit `0`, concise output `ok ... 1.518s`. Full retention package passed in `0.436s`.
- Touched packages: `cd backend && go test ./internal/backupasset ./internal/backupasset/retention ./internal/task -count=1`; exit `0`, concise output `0.569s`, `0.470s`, `2.891s`. Full retention+Task race passed in `1.960s` and `5.770s`.
- Real PostgreSQL 17: disposable `postgres:17-alpine` container `xirang-task5-pg17-0334`, loopback-only ephemeral port, `--rm`, no host volume, random shell-local credential omitted, bounded readiness/cleanup. Policy locking/conflict, deterministic lifecycle lock order, Provider-blocked adopter, and selected-deadline adopter selectors all passed in `0.555s`; concurrent adoption produced exactly one success and one conflict. The container was stopped, `docker inspect` proved removal, and the DSN/credential variables were unset.
- Full backend: `cd backend && go test ./... && go build ./...`; exit `0`. `go vet ./...` exited `0`; `golangci-lint run ./...` exited `0` with `0 issues`.
- Formatting/static/privacy/default gates: `gofmt -d` for the three touched Go files, `git diff --check`, and exact manifest checks produced no findings; all four touched paths are listed in `implement.md`. `LifecycleAttempt` JSON and formatting omit private authority, and `settings/service.go` still declares `backup_assets.enabled` with `CodeDefault: "false"`.

## Task 5 exact terminal-event restart — RED

### Mutable retirement

- UTC timestamp: `2026-08-17T13:18:39Z`
- Command: `cd backend && go test ./internal/backupasset/retention -run '^TestLifecycleMutableRetireTerminalEventRestartSkipsEffects$' -count=1`
- Exit status: `1`
- Result: after the exact `mutable_retire` event was committed and tombstoning lost its absolute-deadline fence, fresh adoption resumed at revocation, replayed cleanup once, and hit the composite event uniqueness guard.
- Concise output: `resumed=revoking effect_deltas=1/0 duplicate_event=true, want tombstoning/0/0/false` (IDs and database statement omitted).

### Ordinary retention expiry

- UTC timestamp: `2026-08-17T13:18:47Z`
- Command: `cd backend && go test ./internal/backupasset/retention -run '^TestLifecycleRetentionExpireTerminalEventRestartSkipsEffects$' -count=1`
- Exit status: `1`
- Result: after the exact `retention_expire` event was committed, fresh adoption resumed at revocation, replayed both cleanup and Provider deletion, then attempted the same composite event again.
- Concise output: `resumed=revoking effect_deltas=1/1 duplicate_event=true, want tombstoning/0/0/false` (IDs, receipt, and database statement omitted).

### Explicit purge

- UTC timestamp: `2026-08-17T13:18:54Z`
- Command: `cd backend && go test ./internal/backupasset/retention -run '^TestLifecycleExplicitPurgeTerminalEventRestartSkipsEffects$' -count=1`
- Exit status: `1`
- Result: after the exact `explicit_purge` event was committed, fresh adoption resumed at revocation, replayed both cleanup and Provider deletion, then attempted the same composite event again.
- Concise output: `resumed=revoking effect_deltas=1/1 duplicate_event=true, want tombstoning/0/0/false` (IDs, receipt, and database statement omitted).

### Mismatched event validation

- UTC timestamp: `2026-08-17T13:19:11Z`
- Command: `cd backend && go test ./internal/backupasset/retention -run '^TestLifecycleTerminalEventMismatchFailsClosed$' -count=1`
- Exit status: `1`
- Result: corrupt mutable timestamp, retention receipt, and explicit-purge terminal-state variants all incorrectly resumed instead of returning the safe invalid-state conflict.
- Concise output: three subtests returned `error=<nil>, want ErrInvalidState`.

- Adjacent existing behavior: `TestLifecycleMissingTerminalEventRestartsEarliestSafePhase` passed in `0.014s`, proving a genuinely absent exact event already restarts from the earliest idempotent revocation phase.

## Task 5 exact terminal-event restart — GREEN

### Mutable retirement

- UTC timestamp: `2026-08-17T13:21:10Z`
- Same selector: `cd backend && go test ./internal/backupasset/retention -run '^TestLifecycleMutableRetireTerminalEventRestartSkipsEffects$' -count=1`
- Exit status: `0`
- Result: after fresh-fence adoption, the exact fully validated `mutable_retire` event resumes directly at tombstoning; cleanup/delete deltas remain `0/0`, one event remains, terminal completion and completed replay are idempotent.
- Concise output: `ok xirang/backend/internal/backupasset/retention 0.012s`.

### Ordinary retention expiry

- UTC timestamp: `2026-08-17T13:21:10Z`
- Same selector: `cd backend && go test ./internal/backupasset/retention -run '^TestLifecycleRetentionExpireTerminalEventRestartSkipsEffects$' -count=1`
- Exit status: `0`
- Result: the exact fully validated `retention_expire` event resumes at tombstoning with cleanup/delete deltas `0/0`; the prior deletion receipt is retained and no second event or Provider effect occurs.
- Concise output: `ok xirang/backend/internal/backupasset/retention 0.014s`.

### Explicit purge

- UTC timestamp: `2026-08-17T13:21:10Z`
- Same selector: `cd backend && go test ./internal/backupasset/retention -run '^TestLifecycleExplicitPurgeTerminalEventRestartSkipsEffects$' -count=1`
- Exit status: `0`
- Result: the exact fully validated `explicit_purge` event resumes at tombstoning with cleanup/delete deltas `0/0`; terminal completion and completed replay preserve the single event.
- Concise output: `ok xirang/backend/internal/backupasset/retention 0.013s`.

### Exact validation and missing-event fallback

- UTC timestamp: `2026-08-17T13:21:10Z`
- Commands: the exact `TestLifecycleTerminalEventMismatchFailsClosed` and `TestLifecycleMissingTerminalEventRestartsEarliestSafePhase` selectors.
- Exit status: `0` for both.
- Result: point, attempt, and lease are locked first; the exact composite event is then locked and validates repository, original semantics, operation, terminal state, managed-history bit, created/terminal timestamps, deletion receipt, and result class. All three corrupt variants remain blocked with zero effect replay and return `ErrInvalidState`; an absent event safely resumes earliest revocation.
- Concise output: mismatch `0.030s`; missing `0.015s`.

## Task 5 exact terminal-event restart final gates — GREEN

- UTC timestamp: `2026-08-17T13:25:27Z`.
- Focused repeat: all three exact operation selectors plus mismatch/missing recovery with `-count=10`; exit `0`, concise output `ok ... 0.451s`.
- Focused race: the same selector set under `go test -race`; exit `0`, concise output `ok ... 1.156s`.
- Required Task 5 selector: `cd backend && go test ./internal/backupasset/retention -run '^(TestLifecycle|TestExpiry|TestPurge|TestMutableHeadRetirement|TestLease)' -count=1`; exit `0`, concise output `ok ... 0.337s`.
- Policy/hold selector passed for retention in `0.135s` and auth in `0.050s`. The unchanged Task 1 exact selector passed in `0.013s`; its retention/pristine companion passed in `0.024s`.
- SQLite composite/schema selector: `TestBackupAssetMigration070(TombstoneCompositeHistorySQLite|SharedContractRegistersTombstoneCompositeHistory|SQLite|PairedFiles|UsedDownAdmissionSQLite)`; exit `0`, concise output `ok ... 2.075s`. Full retention passed in `0.474s`.
- Real PostgreSQL 17: disposable `postgres:17-alpine` container `xirang-task5-terminal-pg17-0334`, loopback-only ephemeral port, `--rm`, no host volume, random shell-local credential omitted, bounded readiness and cleanup. The composite-event schema selector passed in `1.735s`; the new real terminal-event restart plus existing policy locking, deterministic lock-order, Provider-blocked adopter, and selected-deadline adopter suite passed in `0.662s`. The container was stopped, `docker inspect` proved removal, and credential/DSN variables were unset.
- Touched packages: backupasset `0.593s`, retention `0.529s`, Task `2.924s`, database `25.518s`; retention/Task race passed in `2.114s` and `5.881s`. Focused vet exited `0`; focused lint returned `0 issues`.
- Full backend: `cd backend && go test ./... && go build ./...`; exit `0`. Full `go vet ./...` exited `0`; full `golangci-lint run ./...` exited `0` with `0 issues`.
- Static/security/scope gates: `gofmt -d` for the two changed Go files and `git diff --check` produced no output. All three changed paths are manifested; `.codex` is untouched. Private lease/fence fields remain JSON-excluded and safe-formatted, no new logging was added, and `backup_assets.enabled` remains `CodeDefault: "false"`.

## Task 6 Overlay late-output admission — RED

- UTC timestamp: `2026-08-17T14:12:04Z`
- Command: `cd backend && go test ./internal/backupasset/overlay -run '^TestLifecycleLateOutputRejectsOverlayResurrection$' -count=1`
- Exit status: `1`
- Expected failure category: genuine behavioral RED; an observed mutable point had an active `mutable_retire` attempt, existing saved-search/favorite/tag-assignment/recent overlays were reconciled, and every corresponding late point-bearing write was still admitted instead of returning `backupasset.ErrConflict`.
- Concise output: the `saved_search`, `favorite`, `tag_assignment`, and `recent` subtests each reported `late write error=<nil>, want ErrConflict`; package failed in `0.043s`.

## Task 6 Overlay late-output admission — GREEN

- UTC timestamp: `2026-08-17T14:14:52Z`
- Same command: `cd backend && go test ./internal/backupasset/overlay -run '^TestLifecycleLateOutputRejectsOverlayResurrection$' -count=1`
- Exit status: `0`
- Result: exact-point Overlay writes now lock and validate the RecoveryPoint before idempotency replay or mutation, reject any active lifecycle attempt or non-readable point state, and preserve reconciled broken/tombstone/deleted state across saved-search, favorite, tag-assignment, and recent paths.
- Concise output: `ok xirang/backend/internal/backupasset/overlay 0.022s`.

## Task 6 shared source-lifecycle stage contract — RED

- UTC timestamp: `2026-08-17T14:18:08Z`
- Command: `cd backend && go test ./internal/backupasset -run '^TestRecoveryPointSourceLifecycleRequestClosedContract$' -count=1`
- Exit status: `1`
- Expected failure category: contract compile RED; the shared exact-point request, closed `prepare|cleanup` stage, and validator did not exist.
- Concise output: compilation reported undefined `SourceLifecycleRequest`, `SourceLifecyclePrepare`, `SourceLifecycleCleanup`, and `ValidateSourceLifecycleRequest`.

## Task 6 shared source-lifecycle stage contract — GREEN

- UTC timestamp: `2026-08-17T14:18:38Z`
- Same command: `cd backend && go test ./internal/backupasset -run '^TestRecoveryPointSourceLifecycleRequestClosedContract$' -count=1`
- Exit status: `0`
- Result: the shared request carries only exact point/attempt IDs, a closed lifecycle operation, and the closed `prepare|cleanup` stage; malformed IDs and unknown operation/stage values fail with `ErrInvalidState`.
- Concise output: `ok xirang/backend/internal/backupasset 0.004s`.

## Task 6 shared source-lifecycle attempt stage fence — RED

- UTC timestamp: `2026-08-17T14:21:42Z`
- Command: `cd backend && go test ./internal/backupasset -run '^TestRecoveryPointSourceLifecycleAttemptStageFence$' -count=1`
- Exit status: `1`
- Expected failure category: contract compile RED; no transaction-bound validator existed to lock the exact point and prove the requested owner stage against the live lifecycle attempt.
- Concise output: compilation reported undefined `ValidateSourceLifecycleAttemptTx`.

## Task 6 shared source-lifecycle attempt stage fence — GREEN

- UTC timestamp: `2026-08-17T14:22:16Z`
- Same command: `cd backend && go test ./internal/backupasset -run '^TestRecoveryPointSourceLifecycleAttemptStageFence$' -count=1`
- Exit status: `0`
- Result: owner mutations can now share a point-first transaction fence that proves the exact attempt ID, point ID, operation, non-complete state, and `revoking` versus `cleaning` phase before mutation.
- Concise output: `ok xirang/backend/internal/backupasset 0.005s`.

## Task 6 Content source lifecycle — RED

- UTC timestamp: `2026-08-17T14:23:47Z`
- Command: `cd backend && go test ./internal/backupasset/content -run '^TestRecoveryPointSourceLifecycleContentRevokesAndDrainsExactPoint$' -count=1`
- Exit status: `1`
- Expected failure category: contract compile RED; Content had no exact-point source lifecycle owner to revoke delivery authority, prove reads drained, and release owner leases while retaining request history.
- Concise output: compilation reported undefined `NewSourceLifecycle`.

## Task 6 Content source lifecycle — GREEN

- UTC timestamp: `2026-08-17T14:24:44Z`
- Same command: `cd backend && go test ./internal/backupasset/content -run '^TestRecoveryPointSourceLifecycleContentRevokesAndDrainsExactPoint$' -count=1`
- Exit status: `0`
- Result: Content now validates the exact lifecycle attempt, drains in bounded batches, revokes only exact-point delivery grants, releases their live Content leases, and preserves delivery request history through prepare and idempotent cleanup.
- Concise output: `ok xirang/backend/internal/backupasset/content 0.052s`.

## Task 6 Catalog source lifecycle — RED

- UTC timestamp: `2026-08-17T14:26:01Z`
- Command: `cd backend && go test ./internal/backupasset/catalog -run '^TestRecoveryPointSourceLifecycleCatalogSeparatesPrepareFromCleanup$' -count=1`
- Exit status: `1`
- Expected failure category: contract compile RED; Catalog had no source owner separating builder terminalization and lease release during prepare from completed projection retirement during cleanup.
- Concise output: compilation reported undefined `NewSourceLifecycle`.

## Task 6 Catalog source lifecycle — GREEN

- UTC timestamp: `2026-08-17T14:26:56Z`
- Same command: `cd backend && go test ./internal/backupasset/catalog -run '^TestRecoveryPointSourceLifecycleCatalogSeparatesPrepareFromCleanup$' -count=1`
- Exit status: `0`
- Result: Catalog prepare now cancels and terminalizes exact-point builders and releases their leases while retaining completed active generation/entry evidence; cleanup alone supersedes the active generation and remains idempotent without deleting entry history.
- Concise output: `ok xirang/backend/internal/backupasset/catalog 0.008s`.

## Task 6 Search source lifecycle — RED

- UTC timestamp: `2026-08-17T14:29:00Z`
- Command: `cd backend && go test ./internal/backupasset/search -run '^TestRecoveryPointSourceLifecycleSearchSeparatesPrepareFromCleanup$' -count=1`
- Exit status: `1`
- Expected failure category: contract compile RED; Search had no exact-point owner to release builder leases during prepare, retain completed projections and the shared Search key until cleanup, then remove point-owned projection payloads with a cleanup proof.
- Concise output: compilation reported undefined `NewSourceLifecycle`.

## Task 6 Search source lifecycle — GREEN

- UTC timestamp: `2026-08-17T14:30:08Z`
- Same command: `cd backend && go test ./internal/backupasset/search -run '^TestRecoveryPointSourceLifecycleSearchSeparatesPrepareFromCleanup$' -count=1`
- Exit status: `0`
- Result: Search prepare now terminalizes exact-point builders and releases Search leases while preserving completed projections and the shared Search token key; cleanup alone removes point-owned documents/postings/fields, supersedes the generation, and exposes an exact-attempt zero-result proof for Processing.
- Concise output: `ok xirang/backend/internal/backupasset/search 0.009s`.

## Task 6 Search late-output admission and point-first seam — RED

- UTC timestamp: `2026-08-17T14:31:27Z`
- Command: `cd backend && go test ./internal/backupasset/search -run '^TestLifecycleLateOutputRejectsSearch' -count=1`
- Exit status: `1`
- Expected failure category: genuine behavioral RED; after an exact point entered `revoking`, Search still admitted a new generation and both Content and OCR late projections.
- Concise output: generation creation, Content ingest, and OCR ingest each returned `<nil>` instead of `backupasset.ErrConflict`; package failed in `0.047s`.

## Task 6 Search late-output admission and point-first seam — GREEN

- UTC timestamp: `2026-08-17T14:32:32Z`
- Same command: `cd backend && go test ./internal/backupasset/search -run '^TestLifecycleLateOutputRejectsSearch' -count=1`
- Exit status: `0`
- Result: Search generation creation, projection batch publication, activation, Content ingest, and OCR ingest now lock and validate the exact point lifecycle before any lease fence or output mutation, rejecting active lifecycle drift.
- Concise output: `ok xirang/backend/internal/backupasset/search 0.046s`.

## Task 6 Processing source lifecycle and Search proof order — RED

- UTC timestamp: `2026-08-17T14:34:51Z`
- Command: `cd backend && go test ./internal/backupasset/processing -run '^TestRecoveryPointSourceLifecycleProcessingDefersDestructionUntilSearchProof$' -count=1`
- Exit status: `1`
- Expected failure category: contract compile RED; Processing had no source lifecycle owner separating job/interest/grant/lease shutdown from derived reference, wrapped-DEK, and ciphertext destruction after a concrete Search cleanup proof.
- Concise output: compilation reported undefined `NewSourceLifecycle`.

## Task 6 Processing source lifecycle and Search proof order — GREEN

- UTC timestamp: `2026-08-17T14:37:11Z`
- Same command: `cd backend && go test ./internal/backupasset/processing -run '^TestRecoveryPointSourceLifecycleProcessingDefersDestructionUntilSearchProof$' -count=1`
- Exit status: `0`
- Result: Processing prepare now cancels exact-point jobs/attempts, removes interests, revokes drained grants, and releases owner leases while preserving Derived sets/references/wrapped DEKs/ciphertext; cleanup requires the concrete Search proof first, then revokes references/keys and removes unshared ciphertext idempotently.
- Concise output: `ok xirang/backend/internal/backupasset/processing 0.064s`.

## Task 6 Processing RequestWork late-admission lock order — RED

- UTC timestamp: `2026-08-17T14:38:42Z`
- Command: `cd backend && go test ./internal/backupasset/processing -run '^TestLifecycleLateOutputRejectsProcessingRequestWorkBeforeInterestMutation$' -count=1`
- Exit status: `1`
- Expected failure category: genuine behavioral RED; after lifecycle claim, the existing-job path still admitted a second interest instead of fencing at job-to-point admission before interest mutation.
- Concise output: existing-job `RequestWork` returned `<nil>` instead of `backupasset.ErrConflict`; package failed in `0.050s`.

## Task 6 Processing RequestWork late-admission lock order — GREEN

- UTC timestamp: `2026-08-17T14:39:15Z`
- Same command: `cd backend && go test ./internal/backupasset/processing -run '^TestLifecycleLateOutputRejectsProcessingRequestWorkBeforeInterestMutation$' -count=1`
- Exit status: `0`
- Result: existing-job RequestWork now preserves job-to-point admission before interest mutation, while new-job RequestWork locks and validates the point before job/interest insertion; both fail closed after lifecycle claim.
- Concise output: `ok xirang/backend/internal/backupasset/processing 0.052s`.

## Task 6 Export source lifecycle — RED

- UTC timestamp: `2026-08-17T14:43:16Z`
- Command: `cd backend && go test ./internal/backupasset/export -run '^TestRecoveryPointSourceLifecycleExportSeparatesPrepareFromCleanup$' -count=1`
- Exit status: `1`
- Expected failure category: contract compile RED; Export had no exact-point source owner to fence jobs and release source leases during prepare while deferring delivery/key/selection/ciphertext destruction to the existing ordered cleanup owner.
- Concise output: compilation reported undefined `NewSourceLifecycle`.

## Task 6 Export source lifecycle — GREEN

- UTC timestamp: `2026-08-17T14:44:46Z`
- Same command: `cd backend && go test ./internal/backupasset/export -run '^TestRecoveryPointSourceLifecycleExportSeparatesPrepareFromCleanup$' -count=1`
- Exit status: `0`
- Result: Export prepare now fences exact-point jobs, revokes delivery authority, drains streams, and releases source/ordinary leases while preserving selection, wrapped job key, and artifact ciphertext; cleanup alone routes each job through the existing ordered Export lifecycle and remains idempotent.
- Concise output: `ok xirang/backend/internal/backupasset/export 0.055s`.

## Task 6 Recovery source lifecycle — RED

- UTC timestamp: `2026-08-17T14:48:16Z`
- Command: `cd backend && go test ./internal/backupasset/recovery -run '^TestRecoveryPointSourceLifecycleRecoveryCancelsExactPointInterests$' -count=1`
- Exit status: `1`
- Expected failure category: contract compile RED; Recovery had no exact-point source owner routing active source jobs through the existing cancellation handoff while preserving RecoveryResult rows and result/workspace cleanup ownership.
- Concise output: compilation reported undefined `NewSourceLifecycle`.

## Task 6 Recovery source lifecycle — GREEN

- UTC timestamp: `2026-08-17T14:49:17Z`
- Same command: `cd backend && go test ./internal/backupasset/recovery -run '^TestRecoveryPointSourceLifecycleRecoveryCancelsExactPointInterests$' -count=1`
- Exit status: `0`
- Result: Recovery now scans exact-point source jobs in bounded batches, routes active work only through the existing `CancelJob` handoff, releases the exact Recovery source lease, and preserves RecoveryResult rows plus result/workspace cleanup ownership through prepare and idempotent cleanup.
- Concise output: `ok xirang/backend/internal/backupasset/recovery 0.071s`.

## Task 6 coordinator owner operation contract — RED

- UTC timestamp: `2026-08-17T14:50:57Z`
- Command: `cd backend && go test ./internal/backupasset/retention -run '^TestLifecyclePointRequestCarriesOperationToOwnerPorts$' -count=1`
- Exit status: `1`
- Expected failure category: contract compile RED; `LifecyclePointRequest` omitted the closed lifecycle operation, so admission, cleanup, and deletion owner ports could not receive operation semantics.
- Concise output: compilation reported undefined `request.Operation` at all three owner fakes.

## Task 6 coordinator owner operation contract — GREEN

- UTC timestamp: `2026-08-17T14:51:39Z`
- Same command: `cd backend && go test ./internal/backupasset/retention -run '^TestLifecyclePointRequestCarriesOperationToOwnerPorts$' -count=1`
- Exit status: `0`
- Result: lifecycle admission, cleanup, and deletion requests now carry the exact closed operation loaded from the durable attempt, without exposing effect authority or fence material.
- Concise output: `ok xirang/backend/internal/backupasset/retention 0.012s`.

## Task 6 Overlay lifecycle-only maintenance — RED

- UTC timestamp: `2026-08-17T14:52:33Z`
- Command: `cd backend && go test ./internal/backupasset/overlay -run '^TestLifecycleDependentCleanupOverlayMaintenanceUsesExactAttemptWhileDisabled$' -count=1`
- Exit status: `1`
- Expected failure category: contract compile RED; Overlay had no lifecycle-only maintenance entry that could verify the exact Cleaning attempt while the user-facing feature gate was disabled.
- Concise output: compilation reported undefined `ReconcileSourceLifecycle`.

## Task 6 Overlay lifecycle-only maintenance — GREEN

- UTC timestamp: `2026-08-17T14:53:31Z`
- Same command: `cd backend && go test ./internal/backupasset/overlay -run '^TestLifecycleDependentCleanupOverlayMaintenanceUsesExactAttemptWhileDisabled$' -count=1`
- Exit status: `0`
- Result: Overlay lifecycle maintenance now bypasses only the user feature gate, validates the exact Cleaning attempt inside the mutation transaction, and rejects a mismatched attempt while preserving normal feature-disabled behavior.
- Concise output: `ok xirang/backend/internal/backupasset/overlay 0.018s`.

## Task 6 pure runtime lifecycle aggregate — RED

- UTC timestamp: `2026-08-17T14:54:51Z`
- Command: `cd backend && go test ./internal/backupasset/runtime -run '^TestLifecycleDependentCleanupAggregate' -count=1`
- Exit status: `1`
- Expected failure category: contract compile RED; runtime had no pure aggregate requiring all six owners, enforcing prepare/cleanup order, and proving bounded Overlay zero-result completion.
- Concise output: compilation reported undefined `NewRetentionLifecycle` and `RetentionLifecycleDependencies`.

## Task 6 pure runtime lifecycle aggregate — GREEN

- UTC timestamp: `2026-08-17T14:55:55Z`
- Same command: `cd backend && go test ./internal/backupasset/runtime -run '^TestLifecycleDependentCleanupAggregate' -count=1`
- Exit status: `0`
- Result: the pure aggregate requires all seven ports, invokes prepare and cleanup in exact Content→Catalog→Search→Processing→Export→Recovery order, preserves payloads through prepare, prevents Processing cleanup before Search proof, and runs Overlay only after owners until a bounded zero-result pass.
- Concise output: `ok xirang/backend/internal/backupasset/runtime 0.054s`.

## Task 6 concrete Processing-to-Search cleanup proof bridge — RED

- UTC timestamp: `2026-08-17T14:57:00Z`
- Command: `cd backend && go test ./internal/backupasset/runtime -run '^TestLifecycleDependentCleanupProcessingSearchProofBridge$' -count=1`
- Exit status: `1`
- Expected failure category: contract compile RED; runtime had no concrete adapter binding Processing's proof port to the real Search source lifecycle owner.
- Concise output: compilation reported undefined `NewProcessingSearchRevocationProof`.

## Task 6 concrete Processing-to-Search cleanup proof bridge — GREEN

- UTC timestamp: `2026-08-17T14:57:40Z`
- Same command: `cd backend && go test ./internal/backupasset/runtime -run '^TestLifecycleDependentCleanupProcessingSearchProofBridge$' -count=1`
- Exit status: `0`
- Result: the concrete bridge delegates Processing's cleanup proof to the real Search source owner, rejects the proof while a real active Search projection remains, and succeeds only after Search removes that projection.
- Concise output: `ok xirang/backend/internal/backupasset/runtime 0.053s`.

## Task 6 managed Processing live-graph source-owner factory — RED

- UTC timestamp: `2026-08-17T15:01:17Z`
- Command: `cd backend && go test ./internal/backupasset/runtime -run '^TestLifecycleDependentCleanupProcessingRuntimeSourceOwnerUsesLiveGraph$' -count=1`
- Exit status: `1`
- Expected failure category: contract compile RED; `managedProcessingRuntime` exposed no factory that could construct the Task 6 source owner over its existing coordinator, DerivedStore, and DerivedLifecycle graph.
- Concise output: compilation reported undefined `managed.sourceLifecycle`.

## Task 6 managed Processing live-graph source-owner factory — GREEN

- UTC timestamp: `2026-08-17T15:01:58Z`
- Same command: `cd backend && go test ./internal/backupasset/runtime -run '^TestLifecycleDependentCleanupProcessingRuntimeSourceOwnerUsesLiveGraph$' -count=1`
- Exit status: `0`
- Result: `managedProcessingRuntime` now constructs the Processing source owner from its existing DB and exact DerivedLifecycle/DerivedStore graph, requires its live coordinator graph to be complete, and adds no Runtime installation or lifecycle wiring.
- Concise output: `ok xirang/backend/internal/backupasset/runtime 0.069s`.

## Task 6 Search active-builder cancel/join — RED

- UTC timestamp: `2026-08-17T15:03:37Z`
- Command: `cd backend && go test ./internal/backupasset/search -run '^TestRecoveryPointSourceLifecycleSearchCancelsAndJoinsActiveBuilder$' -count=1`
- Exit status: `1`
- Expected failure category: contract compile RED; Search Indexer exposed no exact-point in-process build registry, and the source owner accepted no Indexer with which to cancel and join an active builder.
- Concise output: compilation reported the missing builder registry methods and the missing Indexer constructor argument.

## Task 6 Search active-builder cancel/join — GREEN

- UTC timestamp: `2026-08-17T15:04:44Z`
- Same command: `cd backend && go test ./internal/backupasset/search -run '^TestRecoveryPointSourceLifecycleSearchCancelsAndJoinsActiveBuilder$' -count=1`
- Exit status: `0`
- Result: Search now registers each exact-point in-process build, source prepare cancels its context and releases its fence, waits for the registered builder to join, and refuses completion while any registered build remains.
- Concise output: `ok xirang/backend/internal/backupasset/search 0.007s`.

## Task 6 exact combined owner/aggregate/late-output selector — GREEN

- UTC timestamp: `2026-08-17T15:05:42Z`
- Command: `cd backend && go test ./internal/backupasset/{content,catalog,search,processing,export,recovery,overlay,retention,runtime} -run '^(TestRecoveryPointSourceLifecycle|TestLifecycleDependentCleanup|TestLifecycleLateOutput)' -count=1`
- Exit status: `0`
- Result: all six owners, Search cancel/join and late-output seams, Processing live-graph/derived-manifest/RequestWork seams, disabled Overlay maintenance, source boundary, concrete Search proof bridge, and pure aggregate passed together.
- Concise output: Content `0.056s`; Catalog `0.011s`; Search `0.109s`; Processing `0.139s`; Export `0.061s`; Recovery `0.089s`; Overlay `0.069s`; retention `0.008s`; runtime `0.073s`.

## Task 6 Search dual-engine fixture lifecycle schema — RED

- UTC evidence timestamp: `2026-08-17T15:12:21Z`
- Command: `cd backend && go test ./internal/backupasset ./internal/backupasset/{content,catalog,search,processing,export,recovery,overlay,retention,runtime} -count=1`
- Exit status: `1`
- Expected failure category: behavior fixture RED; the real Search indexer now enforces lifecycle admission, but the manually maintained Search SQLite behavior schema omitted `recovery_point_lifecycle_attempts`.
- Concise output: `TestSearchBehaviorSQLite` failed at Search generation admission with `check recovery point write lifecycle: no such table: recovery_point_lifecycle_attempts`.

## Task 6 Processing runtime fixture lifecycle authority — RED

- UTC evidence timestamp: `2026-08-17T15:16:59Z`
- Command: `cd backend && go test ./internal/backupasset/runtime -run '^(TestProcessingRuntimeSecretContinuationIsDefaultOffCompleteOnlyAndIdempotent|TestProcessingRuntimeTerminalPollDoesNotConsumeAcrossUpdaterActivation|TestProcessingRuntimeAtomicProjectionFailureLeavesNoPendingStateAndRetrySucceeds|TestProcessingRuntimeShutdownRetiresAttemptsGrantsAndRecoveryLease|TestRuntimeDerivedProjectionPortRealSQLiteSearchFailureRollsBackDerivedAndSearch)$' -count=1`
- Exit status: `1`
- Expected failure category: behavior fixture RED; the shared Processing runtime schema omitted RecoveryPoint lifecycle authority, and real RequestWork fixtures omitted their exact admitted point.
- Concise output: four RequestWork paths failed with `recovery point write source is unavailable`; the real Search projection path first exposed the missing lifecycle-attempt table.

## Task 6 Search and Processing runtime fixture lifecycle authority — GREEN

- UTC evidence timestamp: `2026-08-17T15:16:59Z`
- Commands: `cd backend && go test ./internal/backupasset/search -run '^TestSearchBehaviorSQLite$' -count=1`; `cd backend && go test ./internal/backupasset/runtime -run '^(TestProcessingRuntimeSecretContinuationIsDefaultOffCompleteOnlyAndIdempotent|TestProcessingRuntimeTerminalPollDoesNotConsumeAcrossUpdaterActivation|TestProcessingRuntimeAtomicProjectionFailureLeavesNoPendingStateAndRetrySucceeds|TestProcessingRuntimeShutdownRetiresAttemptsGrantsAndRecoveryLease|TestRuntimeDerivedProjectionPortRealSQLiteSearchFailureRollsBackDerivedAndSearch)$' -count=1`
- Exit status: `0` for both commands.
- Result: the manual Search behavior and shared Processing runtime schemas now include lifecycle-attempt authority, and only RequestWork fixtures seed their exact committed source point; production admission remains fail closed.
- Concise output: Search `0.031s`; runtime five-test selector `0.135s`.

## Task 6 full touched-package gate — GREEN

- UTC evidence timestamp: `2026-08-17T15:16:59Z`
- Command: `cd backend && go test ./internal/backupasset ./internal/backupasset/{content,catalog,search,processing,export,recovery,overlay,retention,runtime} -count=1`
- Exit status: `0`
- Result: every Task 6 touched backend package passed after the focused fixture repair.
- Concise output: backupasset `0.881s`; Content `1.123s`; Catalog `5.094s`; Search `1.146s`; Processing `3.550s`; Export `16.611s`; Recovery `34.947s`; Overlay `0.597s`; retention `0.800s`; runtime `7.449s`.

## Task 6 PostgreSQL 17 owner behavior and Processing lock order — GREEN

- UTC evidence timestamp: `2026-08-17T15:33:16Z`
- Command: a disposable loopback-only, `--rm`, no-volume `postgres:17-alpine` fixture ran `cd backend && go test ./internal/backupasset/{content,catalog,search,processing,export,recovery,overlay} -run '^(Test(Content|Catalog|Search|Processing|Export)Behavior(SQLite|Postgres)|Test(Recovery|Overlay)BehaviorPostgres|TestLifecycleLateOutputProcessingRequestWorkPullPostgresLockOrder)$' -count=1` with every package's `REQUIRE_POSTGRES_*_TEST=1` gate.
- Exit status: `0`
- Result: server major `17` was verified before execution; every owner behavior gate and the real RequestWork/Pull deadlock regression ran without skip.
- Concise output: Content `2.250s`; Catalog `2.326s`; Search `0.256s`; Processing `11.033s`; Export `36.200s`; Recovery `0.536s`; Overlay `0.413s`.
- Cleanup proof: no `xirang-task6-pg17-*` container and no `TEST_POSTGRES_DSN` or fixture-password environment remained after the trap; the fixture published no volume.

## Task 6 Child 13 unchanged lifecycle selector — GREEN

- UTC evidence timestamp: `2026-08-17T15:33:16Z`
- Command: `cd backend && go test ./internal/backupasset/recovery ./internal/backupasset/runtime -run 'RecoveryResult|RecoveryWorkspaceCleanup' -count=1`
- Exit status: `0`
- Result: Recovery source cleanup remains limited to the existing source-job cancel handoff and does not disturb RecoveryResult/workspace ownership.
- Concise output: Recovery `1.475s`; runtime `0.075s`.

## Task 6 repeat isolation — RED

- UTC evidence timestamp: `2026-08-17T15:33:16Z`
- Command: `cd backend && go test ./internal/backupasset/{content,catalog,search,processing,export,recovery,overlay,retention,runtime} -run '^(TestRecoveryPointSourceLifecycle|TestLifecycleDependentCleanup|TestLifecycleLateOutput)' -count=10`
- Exit status: `1`
- Expected failure category: repeatability fixture RED; new source-owner tests reused `t.Name()` as a process-lived shared-memory SQLite identity, so later count iterations collided on fixed opaque point IDs.
- Concise output: Content, Catalog, Search, Export, and the runtime Search-proof bridge failed with `UNIQUE constraint failed: recovery_points.id`.

## Task 6 repeat isolation and race — GREEN

- UTC evidence timestamp: `2026-08-17T15:33:16Z`
- Commands: the same combined selector with `-count=10`; the same combined selector with `go test -race ... -count=1`.
- Exit status: `0` for both commands.
- Result: affected tests now use unique per-invocation SQLite files; all owner, aggregate, and late-output seams repeat and race cleanly.
- Concise output (`-count=10`): Content `0.385s`; Catalog `0.214s`; Search `2.657s`; Processing `2.146s`; Export `0.543s`; Recovery `1.232s`; Overlay `1.500s`; retention `0.085s`; runtime `0.438s`.
- Concise output (`-race`): Content `1.597s`; Catalog `1.045s`; Search `1.251s`; Processing `1.797s`; Export `1.606s`; Recovery `1.672s`; Overlay `1.190s`; retention `1.049s`; runtime `1.608s`.

## Task 6 Processing Derived purge restart — RED

- UTC evidence timestamp: `2026-08-17T15:33:16Z`
- Command: `cd backend && go test ./internal/backupasset/processing -run '^TestRecoveryPointSourceLifecycleProcessingDefersDestructionUntilSearchProof$' -count=1`
- Exit status: `1`
- Expected failure category: behavioral restart RED; after a one-shot ciphertext removal failure, retry skipped the already-unavailable set and did not revisit its `purge_failed` blob.
- Concise output: retry recorded Search proof calls `2` but physical removal calls remained `1`, want `2`.

## Task 6 Processing Derived purge restart — GREEN

- UTC evidence timestamp: `2026-08-17T15:33:16Z`
- Same command: `cd backend && go test ./internal/backupasset/processing -run '^TestRecoveryPointSourceLifecycleProcessingDefersDestructionUntilSearchProof$' -count=1`
- Exit status: `0`
- Result: cleanup durably removes blob key authority before I/O, resumes exact-point `purge_failed` blobs first, treats already-absent ciphertext as the crash-safe completion case, and marks unavailable only after deletion proof.
- Concise output: `ok xirang/backend/internal/backupasset/processing 0.066s`.

## Task 6 Catalog live Indexer dependency — RED

- UTC evidence timestamp: `2026-08-17T15:33:16Z`
- Command: `cd backend && go test ./internal/backupasset/catalog -run '^TestRecoveryPointSourceLifecycleCatalogSeparatesPrepareFromCleanup$' -count=1`
- Exit status: `1`
- Expected failure category: behavioral dependency RED; Catalog accepted a nil Indexer and therefore could not prove in-process builder cancellation/join.
- Concise output: `nil Catalog Indexer error=<nil>, want invalid state`.

## Task 6 Catalog live Indexer dependency — GREEN

- UTC evidence timestamp: `2026-08-17T15:33:16Z`
- Same command: `cd backend && go test ./internal/backupasset/catalog -run '^TestRecoveryPointSourceLifecycleCatalogSeparatesPrepareFromCleanup$' -count=1`
- Exit status: `0`
- Result: Catalog source-owner construction now fails closed without its live Indexer graph.
- Concise output: `ok xirang/backend/internal/backupasset/catalog 0.010s`.

## Task 6 final touched-package and full backend gates — GREEN

- UTC evidence timestamp: `2026-08-17T15:33:16Z`
- Commands: `cd backend && go test ./internal/backupasset ./internal/backupasset/{content,catalog,search,processing,export,recovery,overlay,retention,runtime} -count=1`; `cd backend && go test ./...`; `cd backend && go build ./...`; `cd backend && go vet ./...`; `make lint-backend`.
- Exit status: `0` for every command.
- Touched-package output: backupasset `0.950s`; Content `1.175s`; Catalog `5.482s`; Search `1.125s`; Processing `3.763s`; Export `21.006s`; Recovery `42.572s`; Overlay `0.616s`; retention `0.829s`; runtime `7.613s`.
- Full-gate result: all backend packages passed; build and vet emitted no errors; golangci-lint reported `0 issues`.

## Task 6 static boundary, privacy, default-false, manifest, and diff gates — GREEN

- UTC evidence timestamp: `2026-08-17T15:33:16Z`
- Exit status: `0`
- Result: focused settings defaults and source-boundary selectors passed (`settings 0.005s`, `retention 0.006s`); `backup_assets.enabled` remains code-default false; all `35` Task 6 changed paths are in the amended live manifest; added-line/new-owner privacy review found no raw sensitive output flow; `derived_manifest.go` and its test are unchanged; `runtime.go` and its test are unchanged; no Task 7 Provider file, Task 8 installation, `.codex`, `000071`, deploy, dependency, or lockfile change exists; no path is staged; `git diff --check` is clean.
- Deterministic SHA-256 over the `34` Task 6 Go production/test paths: `e567c13893e7b9689ff9852b0ff249558b7827826ba0c463652064c3fa20925f`.

## Task 6 review fix: bounded exact-point Content cache eviction — RED

- UTC evidence timestamp: `2026-08-17T15:57:35Z`
- Command: `cd backend && go test ./internal/backupasset/content -run '^TestCacheEvictRecoveryPointBoundedZeroProofAndBusyFailClosed$' -count=1`
- Exit status: `1`
- Expected failure category: behavioral contract RED; the authenticated cache exposed only single-object eviction and had no bounded exact-RecoveryPoint eviction/zero-proof contract.
- Concise output: the test failed because `AuthenticatedCache` did not implement the recovery-point eviction interface.

## Task 6 review fix: bounded exact-point Content cache eviction — GREEN

- UTC evidence timestamp: `2026-08-17T15:58:44Z`
- Same command: `cd backend && go test ./internal/backupasset/content -run '^TestCacheEvictRecoveryPointBoundedZeroProofAndBusyFailClosed$' -count=1`
- Exit status: `0`
- Result: the cache now rejects canceled/invalid/busy cleanup, tracks exact-point pending materializations, evicts memory and disk entries in bounded deterministic batches, preserves other points, and requires a zero-result completion pass.
- Concise output: `ok xirang/backend/internal/backupasset/content 0.049s`.

## Task 6 review fix: Content Broker drain and cache ownership — RED

- UTC evidence timestamp: `2026-08-17T16:02:15Z`
- Command: `cd backend && go test ./internal/backupasset/content -run '^TestRecoveryPointSourceLifecycleContentJoinsPreclaimIssueAndEvictsExactCache$' -count=1`
- Exit status: `1`
- Expected failure category: behavioral race RED; the Content source owner completed while an issuance already admitted before the lifecycle claim remained in progress, so it could neither join the late asset/read registration nor own exact-point cache cleanup.
- Concise output: `prepare returned before pre-claim issue joined: <nil>`.

## Task 6 review fix: Content Broker drain and cache ownership — GREEN

- UTC evidence timestamp: `2026-08-17T16:03:35Z`
- Same command: `cd backend && go test ./internal/backupasset/content -run '^TestRecoveryPointSourceLifecycleContentJoinsPreclaimIssueAndEvictsExactCache$' -count=1`
- Exit status: `0`
- Result: the Content owner now requires the live Broker/cache graph, verifies the lifecycle claim before joining pre-claim issuance, cancels and joins exact-point reads, preserves independent RecoveryResult and other-point resources, keeps cache payload through prepare, and evicts exact memory/disk cache only during cleanup with busy retry and zero proof.
- Concise output: `ok xirang/backend/internal/backupasset/content 0.156s`.

## Task 6 review fix: Search historical-generation payload restart — RED

- UTC evidence timestamp: `2026-08-17T16:04:57Z`
- Command: `cd backend && go test ./internal/backupasset/search -run '^TestRecoveryPointSourceLifecycleSearchRemovesSupersededGenerationPayloadOnRestart$' -count=1`
- Exit status: `1`
- Expected failure category: behavioral restart RED; cleanup removed the current active projection but selected no already-superseded generation, leaving its exact-point documents behind while the final proof counted them.
- Concise output: `backup asset conflict: Search source revocation is incomplete`.

## Task 6 review fix: Search historical-generation payload restart — GREEN

- UTC evidence timestamp: `2026-08-17T16:05:42Z`
- Same command: `cd backend && go test ./internal/backupasset/search -run '^TestRecoveryPointSourceLifecycleSearchRemovesSupersededGenerationPayloadOnRestart$' -count=1`
- Exit status: `0`
- Result: cleanup now selects exact-point payload-bearing generations regardless of historical state, removes documents/postings/fields in bounded order, preserves generation rows and the shared Search key, preserves other points, and reaches an idempotent zero-payload proof after restart.
- Concise output: `ok xirang/backend/internal/backupasset/search 0.010s`.

## Task 6 review fix: Processing RequestWork readable-point admission — RED

- UTC evidence timestamp: `2026-08-17T16:07:17Z`
- Command: `cd backend && go test ./internal/backupasset/processing -run '^TestLifecycleLateOutputProcessingRequestWorkRejectsCompletedLifecycleNonReadablePoint$' -count=1`
- Exit status: `1`
- Expected failure category: behavioral admission RED; after a completed lifecycle attempt, existing-job RequestWork admitted retired, expired, failed, and preparing points and mutated interests because shared admission loaded only the point ID.
- Concise output: all four non-readable states returned `error=<nil>, want ErrConflict`.

## Task 6 review fix: Processing RequestWork readable-point admission — GREEN

- UTC evidence timestamp: `2026-08-17T16:07:48Z`
- Same command: `cd backend && go test ./internal/backupasset/processing -run '^TestLifecycleLateOutputProcessingRequestWorkRejectsCompletedLifecycleNonReadablePoint$' -count=1`
- Exit status: `0`
- Result: shared point admission now locks and validates state plus semantics, admits mutable observed and immutable committed/degraded points after completed attempts, and rejects non-readable points before either existing-job interest mutation or new-job insertion without changing RequestWork/Pull lock order.
- Concise output: `ok xirang/backend/internal/backupasset/processing 0.064s`.

## Task 6 review fix: Export item/source representation integrity — RED

- UTC evidence timestamp: `2026-08-17T16:10:12Z`
- Command: `cd backend && go test ./internal/backupasset/export -run '^TestRecoveryPointSourceLifecycleExportRejectsDivergentItemSourceRepresentations$' -count=1`
- Exit status: `1`
- Expected failure category: behavioral authority RED; item-only, source-only, mismatched point sets, and split-job representations were selected by the OR candidate query, allowing prepare/cleanup completion or invoking whole-job lifecycle effects.
- Concise output: prepare cases returned `error=<nil>, want ErrConflict`; cleanup invoked `7` or `14` lifecycle effects for divergent representations.

## Task 6 review fix: Export item/source representation integrity — GREEN

- UTC evidence timestamp: `2026-08-17T16:11:22Z`
- Same command: `cd backend && go test ./internal/backupasset/export -run '^TestRecoveryPointSourceLifecycleExportRejectsDivergentItemSourceRepresentations$' -count=1`
- Exit status: `0`
- Result: Export now preflights complete item/source point-set equality before any owner effect, selects only jobs that consistently include the exact point in both representations, and final-proves both representation integrity plus source-evidence and RecoveryPoint owner leases.
- Concise output: `ok xirang/backend/internal/backupasset/export 0.099s`.

## Task 6 review fix: Processing non-current live authority — RED

- UTC evidence timestamp: `2026-08-17T16:16:12Z`
- Command: `cd backend && go test ./internal/backupasset/processing -run '^TestRecoveryPointSourceLifecycleProcessingSettlesNonCurrentLiveAuthority$' -count=1`
- Exit status: `1`
- Expected failure category: behavioral authority RED; after the in-flight guard was cleared, prepare ignored the exact-point non-current job and left its active interest, current attempt, usable grant, and Processing lease live.
- Concise output: `backup asset conflict: Processing source lease remains live`.

## Task 6 review fix: Processing non-current live authority — GREEN

- UTC evidence timestamp: `2026-08-17T16:17:27Z`
- Same command: `cd backend && go test ./internal/backupasset/processing -run '^TestRecoveryPointSourceLifecycleProcessingSettlesNonCurrentLiveAuthority$' -count=1`
- Exit status: `0`
- Result: prepare now scans every exact-point job carrying live Processing authority regardless of current-job status, fails transactionally while any grant is in flight, settles interests/current attempts/usable grants/live leases, preserves terminal evidence, and final-proves all authority dimensions at zero without touching another point or released lease history.
- Concise output: `ok xirang/backend/internal/backupasset/processing 0.062s`.

## Task 6 review fix: Processing non-current authority PostgreSQL contract — RED

- UTC evidence timestamp: `2026-08-17T16:31:20Z`
- Command: fresh disposable loopback-only `postgres:17-alpine` (`server_version_num=170011`, `--rm`, tmpfs, no volume) plus `cd backend && REQUIRE_POSTGRES_PROCESSING_TEST=1 TEST_POSTGRES_DSN='<redacted loopback DSN>' go test ./internal/backupasset/processing -run '^(TestProcessingBehavior(SQLite|Postgres)|TestLifecycleLateOutputProcessingRequestWorkPullPostgresLockOrder|TestRecoveryPointSourceLifecycleProcessingSettlesNonCurrentLiveAuthorityPostgres)$' -count=1`
- Exit status: `1`
- Expected failure category: genuine cross-engine behavioral RED; the newly exercised non-current owner reached live PostgreSQL constraints and used a grant revocation reason that SQLite admitted but the authoritative PostgreSQL schema rejects.
- Concise output: `backup_asset_processing_grants_revocation_reason_check` rejected `source_expired`; the disposable fixture was removed by the exit trap.

## Task 6 review fix: Processing non-current authority PostgreSQL contract — continued RED

- UTC evidence timestamp: `2026-08-17T16:32:25Z`
- Same sanitized PostgreSQL 17 command and fixture contract.
- Exit status: `1`
- Expected failure category: genuine cross-engine behavioral RED; after correcting the grant reason, the same live schema proved that a canceled Processing attempt must preserve the closed canceled-outcome contract instead of recording an error outcome.
- Concise output: `backup_asset_processing_attempts_lifecycle_check` rejected canceled state with `source_expired`; the disposable fixture was removed by the exit trap.

## Task 6 review fix: Processing non-current authority PostgreSQL contract — GREEN

- UTC evidence timestamp: `2026-08-17T16:33:43Z`
- Same sanitized PostgreSQL 17 command and fixture contract.
- Exit status: `0`
- Result: the Processing owner now uses schema-valid expired grant evidence and the closed canceled-attempt outcome while preserving the non-current authority settlement, RequestWork/Pull lock order, and full SQLite/PostgreSQL Processing behavior contract.
- Concise output: `ok xirang/backend/internal/backupasset/processing 10.003s`.
- Cleanup proof: `fixture_inspect=absent`, exact-name `fixture_container_count=0`, and `credential_env_count=0` after the `--rm` exit trap; fixture binding was `127.0.0.1`, storage was tmpfs, and no volume mount existed.

## Task 6 review fix: final post-fix verification and static audit — GREEN

- UTC evidence timestamp: `2026-08-17T16:37:58Z`
- Exit status: `0` for every command.
- Exact combined selector: `cd backend && go test ./internal/backupasset/{content,catalog,search,processing,export,recovery,overlay,retention,runtime} -run '^(TestRecoveryPointSourceLifecycle|TestLifecycleDependentCleanup|TestLifecycleLateOutput)' -count=1` (`6.82s`).
- Repeat/race: the same selector with `-count=10` (`6.97s`) and `go test -race ... -count=1` (`11.76s`).
- Child 13 unchanged: `cd backend && go test ./internal/backupasset/recovery ./internal/backupasset/runtime -run 'RecoveryResult|RecoveryWorkspaceCleanup' -count=1` (`6.29s`).
- Full gates: touched packages GREEN (slowest Recovery `37.489s`); `cd backend && go test ./...` GREEN (`29.65s`); build `4.56s`; vet `1.33s`; golangci-lint `0 issues` in `2.98s`.
- Default/source boundary: retention settings safe-default selector GREEN (`0.005s`), source-boundary selector GREEN (`0.008s`), and `backup_assets.enabled` remains code-default `false`.
- Manifest/privacy/diff: all `37` Task 6 paths (the `36` Go paths plus this evidence file) are in the amended live manifest; owner output/JSON/raw-locator logging scans and unredacted-DSN scan returned zero; forbidden-scope, protected Derived-manifest, and staged-path counts are zero; `git diff --check` is clean.
- Deterministic SHA-256 over the `36` Task 6 Go production/test paths: `99434961b43b0f52ae276496e0dbe5325a355fffe08a1b3022198e6c23afe0b5`.

## Task 6 spec re-review fix: Content exact-point Issue join — RED

- UTC evidence timestamp: `2026-08-17T16:52:44Z`
- Command: `cd backend && go test ./internal/backupasset/content -run '^(TestRecoveryPointSourceLifecycleContentWaitsOnlyExactBackupAssetIssues|TestBrokerShutdownStillWaitsForRecoveryResultIssue)$' -count=1`
- Exit status: `1`
- Expected failure category: genuine behavioral RED through real `Issue` calls; after the target-point BackupAsset Issue exited, Content lifecycle prepare remained delayed by unrelated BackupAsset and RecoveryResult Issues because it waited the broker-global issuance group.
- Concise output: `prepare was delayed by unrelated backup-asset or RecoveryResult Issues`; the global Shutdown control in the same selector passed.

## Task 6 spec re-review fix: Content exact-point Issue join — GREEN

- UTC evidence timestamp: `2026-08-17T16:54:16Z`
- Same command: `cd backend && go test ./internal/backupasset/content -run '^(TestRecoveryPointSourceLifecycleContentWaitsOnlyExactBackupAssetIssues|TestBrokerShutdownStillWaitsForRecoveryResultIssue)$' -count=1`
- Exit status: `0`
- Result: validated BackupAsset issuance now registers an asset-only exact-RecoveryPoint join before lease acquisition and unregisters on error/cancel; lifecycle waits only that point with a context-aware channel and no waiter goroutine, unrelated BackupAsset/RecoveryResult Issues remain independent, and Shutdown retains its global issuance wait.
- Concise output: `ok xirang/backend/internal/backupasset/content 0.361s`.

## Task 6 spec re-review fix: write-admission semantics/state matrix — RED

- UTC evidence timestamp: `2026-08-17T16:55:21Z`
- Command: `cd backend && go test ./internal/backupasset -run '^TestValidateRecoveryPointWriteAdmissionTxEnforcesClosedSemanticsStateMatrix$' -count=1`
- Exit status: `1`
- Expected failure category: genuine behavioral RED; write admission accepted unknown semantics in committed/degraded state and mutable-head semantics in committed/degraded state instead of enforcing the closed readable matrix.
- Concise output: all four invalid combinations returned `error=<nil>, want ErrConflict`; valid mutable-observed and immutable committed/degraded combinations remained admitted.

## Task 6 spec re-review fix: write-admission semantics/state matrix — GREEN

- UTC evidence timestamp: `2026-08-17T16:55:57Z`
- Same command: `cd backend && go test ./internal/backupasset -run '^TestValidateRecoveryPointWriteAdmissionTxEnforcesClosedSemanticsStateMatrix$' -count=1`
- Exit status: `0`
- Result: the unchanged point-row lock now feeds a closed readable predicate that accepts only mutable-head observed or native-snapshot/xirang-manifest/imported-baseline committed/degraded, rejecting every tested cross/unknown combination before the active-attempt query.
- Concise output: `ok xirang/backend/internal/backupasset 0.005s`.

## Task 6 spec re-review fix: runtime canonical write-point fixtures — RED

- UTC evidence timestamp: `2026-08-17T17:00:57Z`
- Command: `cd backend && go test ./internal/backupasset/runtime -run '^(TestProcessingRuntimeSecretContinuationIsDefaultOffCompleteOnlyAndIdempotent|TestProcessingRuntimeTerminalPollDoesNotConsumeAcrossUpdaterActivation|TestProcessingRuntimeAtomicProjectionFailureLeavesNoPendingStateAndRetrySucceeds|TestProcessingRuntimeShutdownRetiresAttemptsGrantsAndRecoveryLease)$' -count=1`
- Exit status: `1`
- Expected failure category: genuine adjacent fixture RED; the four runtime tests modeled committed durable source points but omitted their version semantics, so the new closed admission contract correctly rejected the unknown-semantics/committed pairing.
- Concise output: all four tests failed at RequestWork admission with `recovery point write source is not readable` before their intended runtime behavior.

## Task 6 spec re-review fix: runtime canonical write-point fixtures — GREEN

- UTC evidence timestamp: `2026-08-17T17:02:17Z`
- Same command: `cd backend && go test ./internal/backupasset/runtime -run '^(TestProcessingRuntimeSecretContinuationIsDefaultOffCompleteOnlyAndIdempotent|TestProcessingRuntimeTerminalPollDoesNotConsumeAcrossUpdaterActivation|TestProcessingRuntimeAtomicProjectionFailureLeavesNoPendingStateAndRetrySucceeds|TestProcessingRuntimeShutdownRetiresAttemptsGrantsAndRecoveryLease)$' -count=1`
- Exit status: `0`
- Result: the shared runtime write-point fixture now models its durable catalog/source-fingerprint-backed authority as `xirang_manifest` plus `committed`; it does not mislabel these sources as mutable heads, provider-native snapshots, or imported baselines.
- Concise output: `ok xirang/backend/internal/backupasset/runtime 0.129s`.

## Task 6 spec re-review fix: runtime provider-semantic fixture fidelity — GREEN correction

- UTC evidence timestamp: `2026-08-17T17:03:18Z`
- Same four-test runtime command.
- Exit status: `0`
- Result: the fixture is now semantics-parameterized: explicit Restic malware-evidence points use `native_snapshot` plus `committed`, while provider-unspecified durable descriptor/continuation points and rsync manifest sources use `xirang_manifest` plus `committed`.
- Concise output: `ok xirang/backend/internal/backupasset/runtime 0.131s`.

## Task 6 spec re-review fix: final verification and static audit — GREEN

- UTC evidence timestamp: `2026-08-17T17:07:50Z`
- Exit status: `0` for every final required command.
- Exact combined selector: `cd backend && go test ./internal/backupasset/{content,catalog,search,processing,export,recovery,overlay,retention,runtime} -run '^(TestRecoveryPointSourceLifecycle|TestLifecycleDependentCleanup|TestLifecycleLateOutput)' -count=1` GREEN (`15.81s` wall); the same selector with `-count=10` GREEN (`16.09s` wall) and `go test -race ... -count=1` GREEN (`20.40s` wall).
- Review-fix focus: the exact Content Issue-join/global-Shutdown plus write-admission matrix selector passed at `-count=10`; the same selector passed under `-race` (`backupasset 1.024s`, Content `1.859s`). Adjacent full `backupasset` and Content packages passed.
- Child 13 unchanged: `cd backend && go test ./internal/backupasset/recovery ./internal/backupasset/runtime -run 'RecoveryResult|RecoveryWorkspaceCleanup' -count=1` GREEN (`2.77s` wall).
- Runtime fixture proof: the exact four-test selector passed after provider-semantic correction (`0.131s`), and full Runtime passed (`6.990s`).
- Full gates: the serial full touched-package command passed (slowest Recovery `34.975s`, Runtime `7.473s`); `cd backend && go test ./... -count=1` passed (slowest Recovery `38.557s`); `go build ./...` passed (`3.60s`); `go vet ./...` passed (`0.53s`); `golangci-lint run ./...` reported `0 issues` (`6.66s`).
- Default/source boundary: `TestRollbackSafeDisabledRetentionRemainsBlocked` passed (`0.011s`), `TestLifecycleDependentCleanupSourceBoundaryKeepsOwnerTablesAndRecoveryResultsOutOfRetention` passed (`0.006s`), and the settings code default for `backup_assets.enabled` remains `false`.
- PostgreSQL disposition: no fresh fixture was required because the two production fixes are an in-memory exact-point channel tracker and a Go predicate after the unchanged locked point select; the previously recorded fresh PostgreSQL 17 RequestWork/Pull and owner behavior gates remain applicable. Independent final inspection found no `xirang-task6-pg17-*` container and no PostgreSQL test credential/DSN environment.
- Manifest/privacy/diff: all `7` Go paths changed by this re-review cycle are present in the approved live manifest; the evidence path is explicitly assigned; added-production-line sensitive locator/credential scan returned zero; staged-path count is zero; `git diff --check` is clean; no Task 7 deletion or Task 8 production wiring was added.
- Deterministic SHA-256 over the `7` re-review-cycle Go production/test paths: `c2e07c2e6edee977618b741cddfbe8b02d3baff9a211452d36fc554cd9bbfee6`.

## Task 6 independent quality fix: Overlay closed readable matrix — RED

- UTC evidence timestamp: `2026-08-17T17:50:00Z`
- Command: `cd backend && go test ./internal/backupasset/overlay -run '^TestLifecycleOverlayClosedReadableMatrix$' -count=1`
- Exit status: `1`
- Expected failure category: genuine behavioral RED; unknown semantics paired with `committed` or `degraded` were admitted. The closed matrix must admit only mutable-head plus observed, or native-snapshot/xirang-manifest/imported-baseline plus committed/degraded.
- Only the focused test was changed before this RED; no production edit preceded it.

## Task 6 independent quality fix: Overlay PostgreSQL locked-recent zero proof — RED

- UTC evidence timestamp: `2026-08-17T17:50:41Z`
- Sanitized command: `REQUIRE_POSTGRES_OVERLAY_TEST=1 TEST_POSTGRES_DSN="$ephemeral_loopback_dsn" go test ./internal/backupasset/overlay -run '^TestLifecycleOverlayPostgresLockedRecentCannotFalseZero$' -count=1`
- Exit status: `1`
- Expected failure category: genuine PostgreSQL behavioral RED; reconciliation skipped the sole locked recent row and returned an empty successful result before the holder rolled back, creating a false zero-result proof.
- Fixture contract: disposable `postgres:17-alpine`, loopback-only random port, tmpfs data, no volume, `--rm`, random shell-local credential. Cleanup proof: exact fixture removed and fresh inspection absent; no DSN or credential is recorded here.
- Only the focused test was changed before this RED; no production edit preceded it.

## Task 6 independent quality fix: Catalog active-builder join — RED

- UTC evidence timestamp: `2026-08-17T17:50:44Z`
- Command: `cd backend && go test ./internal/backupasset/catalog -run '^TestRecoveryPointSourceLifecycleCatalogJoinsCanceledBuilderBeforeDurableProof$' -count=1`
- Exit status: `1`
- Expected failure category: genuine behavioral RED; prepare returned conflict before joining the canceled builder and prematurely marked the generation failed/released its lease instead of honoring the caller deadline. The unrelated point remained unchanged.
- Only `catalog/source_lifecycle_test.go` was changed before this RED; no production edit preceded it.

## Task 6 independent quality fix: Recovery typed source cancellation — RED

- UTC evidence timestamp: `2026-08-17T17:54:15Z`
- Command: `cd backend && go test ./internal/backupasset/recovery -run '^TestRecoveryWorkerCancelRecoveryPointPreservesQueuedRunningAndMutationArmedSemantics$' -count=1`
- Exit status: `1`
- Expected failure category: genuine contract RED; `WorkerCoordinator` lacked the typed source-scoped `CancelRecoveryPoint` handoff required to preserve queued, running, mutation-armed, idempotent, exact-lease, and Child 13 state semantics.
- Only the focused test was changed before this RED; no production edit preceded it.

## Task 6 independent quality fix: Recovery exact attempt/plan/point lease binding — RED

- UTC evidence timestamp: `2026-08-17T17:54:26Z`
- Command: `cd backend && go test ./internal/backupasset/recovery -run '^TestRecoveryPointSourceLifecycleRecoveryBindsAttemptPlanAndExactPointLease$' -count=1`
- Exit status: `1`
- Expected failure category: genuine behavioral RED; lifecycle attempt/phase drift was detected only after the old cancellation had committed, and a point-A plan/job carrying a point-B active source lease returned success and released B instead of failing closed.
- Only Recovery test files were changed before this RED; `source_lifecycle.go` and `worker.go` remained untouched.

## Task 6 independent quality fix: Processing publication drain and private-path redaction — RED

- UTC evidence timestamp: `2026-08-17T17:54:34Z`
- Command: `cd backend && go test ./internal/backupasset/processing -run '^(TestRecoveryPointSourceLifecycleProcessingRevokesPublicationBeforeInflightDrain|TestLifecycleDependentCleanupProcessingRedactsPrivatePath)$' -count=1`
- Exit status: `1`
- Expected failure category: genuine behavioral RED; an in-flight grant remained active and publishable after lifecycle claim, while a derived deletion failure exposed its private root, opaque locator, and canary instead of the closed `derived blob unavailable` error.
- Only focused Processing tests were changed before this RED; no production edit preceded it. The required-DSN lock-order test was strengthened but not run in this RED phase.

## Task 6 independent quality fix: Content exact owner authority/isolation/cache/privacy/boundedness — RED

- UTC evidence timestamp: `2026-08-17T17:56:24Z`
- Command: `cd backend && go test ./internal/backupasset/content ./internal/backupasset/retention -run '^(TestRecoveryPointSourceLifecycleContentExactLeaseAndRecoveryResultIsolation|TestCacheLifecycleEvictionSerializesConcurrentMaintenance|TestContentLifecycleErrorsRedactPrivatePaths|TestContentLifecycleBrokerMarkersAreBounded|TestLifecycleDependentCleanupDoesNotDrainRecoveryResultLease)$' -count=1`
- Exit status: `1`
- Expected failure category: genuine behavioral RED only: RecoveryResult content authority was misclassified as BackupAsset source authority; live and restart paths bypassed exact lease release/takeover and force-released mismatches; concurrent cache maintenance crossed the lifecycle owner; a raw path canary leaked; broker lifecycle markers remained unbounded; generic drain blocked on the preserved RecoveryResult lease.
- Only `content/source_lifecycle_test.go`, `content/cache_test.go`, and `retention/coordinator_test.go` were changed before this RED; no production edit preceded it.

## Task 6 independent quality fix: Content cache eviction serialization race — RED

- UTC evidence timestamp: `2026-08-17T17:56:35Z`
- Command: `cd backend && go test -race ./internal/backupasset/content -run '^TestCacheLifecycleEvictionSerializesConcurrentMaintenance$' -count=1`
- Exit status: `1`
- Expected failure category: genuine behavioral concurrency RED under the race gate; a second lifecycle eviction and Reconcile both crossed the same memory/disk eviction barrier rather than respecting one exact owner.
- No production edit preceded this RED.

## Task 6 independent quality fix: Export real-port prepare state matrix — RED

- UTC evidence timestamp: `2026-08-17T17:59:51Z`
- Command: `cd backend && go test ./internal/backupasset/export -run '^TestRecoveryPointSourceLifecycleExportRealPortPreparesQueuedRunningSealingReady$' -count=1`
- Exit status: `1`
- Expected failure category: genuine behavioral RED with the real persistent lifecycle graph: queued/ready were rejected by delivery revocation as invalid transitions, while running with an active attempt and sealing were rejected by fencing as attempt-fence loss.
- The matrix freezes the required source-expired/expiring prepare state, Fence→Revoke→Drain order, exact source/point lease release, preservation of payload/key/artifact/ciphertext/store reservation, and retry/revision idempotency.
- Only `export/source_lifecycle_test.go` was changed before this RED; no production edit preceded it.

## Task 6 independent quality fixes: consolidated local selectors — GREEN

- UTC evidence timestamp: `2026-08-17T18:22:15Z`
- Command: `cd backend && go test ./internal/backupasset/content ./internal/backupasset/catalog ./internal/backupasset/processing ./internal/backupasset/export ./internal/backupasset/recovery ./internal/backupasset/overlay ./internal/backupasset/retention -run '^(TestRecoveryPointSourceLifecycleContentExactLeaseAndRecoveryResultIsolation|TestCacheLifecycleEvictionSerializesConcurrentMaintenance|TestContentLifecycleErrorsRedactPrivatePaths|TestContentLifecycleBrokerMarkersAreBounded|TestLifecycleDependentCleanupDoesNotDrainRecoveryResultLease|TestRecoveryPointSourceLifecycleCatalogJoinsCanceledBuilderBeforeDurableProof|TestRecoveryPointSourceLifecycleProcessingRevokesPublicationBeforeInflightDrain|TestLifecycleDependentCleanupProcessingRedactsPrivatePath|TestRecoveryPointSourceLifecycleExportRealPortPreparesQueuedRunningSealingReady|TestRecoveryPointSourceLifecycleRecoveryBindsAttemptPlanAndExactPointLease|TestRecoveryWorkerCancelRecoveryPointPreservesQueuedRunningAndMutationArmedSemantics|TestLifecycleOverlayClosedReadableMatrix)$' -count=1`
- Exit status: `0`; all seven packages passed. Fresh `-count=10` and `-race -count=3` runs of the same closed finding matrix also passed.
- Result: Catalog joins before durable mutation; Processing revokes publication and redacts private paths; Export prepares every real active state through its real ports; Recovery binds the lifecycle/plan/job/attempt/exact point lease transaction; Overlay enforces the closed matrix; Content uses exact owner authority, RecoveryResult isolation, serialized cache maintenance, closed errors, and bounded markers.
- No Task 7/8/15, stage, commit, or push action occurred.

## Task 6 independent quality fix: Overlay PostgreSQL locked-recent zero proof — GREEN

- UTC evidence timestamp: `2026-08-17T18:23:02Z`
- Same required-DSN selector as the earlier RED passed at `-count=1` (`1.104s`) and fresh `-count=5` (`2.261s`). The sole held recent no longer yields a false empty success; caller context controls the blocking proof and the row is processed after rollback.
- Fixture contract: unique `postgres:17-alpine`, loopback-only random port, tmpfs, no volume, `--rm`, random shell-local credential, SQL-level readiness.
- Cleanup proof: `docker inspect` absent, exact-name count `0`, credential and DSN unset, and a fresh post-cleanup inspection passed.

## Task 6 independent quality fix: Processing PostgreSQL lock order and authority — GREEN

- UTC evidence timestamp: `2026-08-17T18:25:25Z`
- Required-DSN selector `TestLifecycleLateOutputProcessingRequestWorkPullPostgresLockOrder` passed (`1.353s`) with both deterministic barriers reached, observable retry count `0`, SQLSTATE list empty, and `40P01=0`.
- The same fixture also passed `TestProcessingBehaviorPostgres` and `TestRecoveryPointSourceLifecycleProcessingSettlesNonCurrentLiveAuthorityPostgres`; combined Go time was `8.260s`.
- Fixture contract: unique `postgres:17-alpine`, loopback-only random port, tmpfs, no volume, `--rm`, random shell-local credential, DB-level readiness.
- Cleanup proof at `2026-08-17T18:25:55Z`: exact-name count `0`, no remaining fixture image container, and credentials/DSN unset.

## Task 6 root review fix: unowned Content session fail-closed proof — RED

- UTC evidence timestamp: `2026-08-17T18:26:13Z`
- Command: `cd backend && go test ./internal/backupasset/content -run '^TestRecoveryPointSourceLifecycleContentRejectsUnownedContentSessionsAndPreservesRecoveryResult$' -count=1`
- Exit status: `1`
- Expected failure category: genuine behavioral RED; both an active `content_session` with no durable grant and one bound to an unknown resource kind were ignored by Content final proof while generic drain excluded all Content sessions, so each lifecycle request returned nil instead of a closed conflict.
- Each subtest simultaneously preserved a valid RecoveryResult grant/read/lease to freeze the Child 13 ownership boundary. The fixture compiled and reached only the intended assertions.
- Only the focused Content test was changed before this RED; no production edit preceded it.

## Task 6 root review fix: unowned Content session fail-closed proof — GREEN

- UTC evidence timestamp: `2026-08-17T18:29:06Z`
- Same command: `cd backend && go test ./internal/backupasset/content -run '^TestRecoveryPointSourceLifecycleContentRejectsUnownedContentSessionsAndPreservesRecoveryResult$' -count=1`
- Exit status: `0`; concise output: `ok xirang/backend/internal/backupasset/content 0.055s`.
- Result: Content final proof classifies every same-point active `content_session`: only an exact, shape-valid RecoveryResult grant/read/lease is preserved; any missing grant, unknown resource kind, or lease/grant binding mismatch fails closed; active BackupAsset authority must already have settled through the exact fenced owner path. Retention still does not query or mutate owner tables.
- The expanded Content/retention matrix passed at `-count=5` and `-race -count=3`; full Content/retention normal and race packages, Child 13 unchanged selectors, vet, gofmt, and diff-check passed.

## Task 6 final-gate fixture scheduling diagnosis — RED then GREEN

- UTC evidence window: after `2026-08-17T18:29:06Z` and before the final gate timestamp below; the shell start timestamp for the first repeat was not captured.
- RED command: the required nine-package Task 6 selector with `-count=10`; exit status `1` only in `TestRecoveryPointSourceLifecycleContentWaitsOnlyExactBackupAssetIssues/unrelated_asset_and_RecoveryResult_issues_do_not_delay_the_target` after one second with two of three Issue calls at the injected lease barrier.
- Root cause evidence: an exact `-count=20` reproduction captured the missing other-point Issue returning closed `content source unavailable` before lease acquisition because two asset goroutines contended on the short SQLite durable-admission transaction. Both points were readable and no lifecycle attempt existed; this did not exercise the exact-point wait assertion.
- Test-only correction: start each real Issue and wait for its exact lease-acquisition barrier before starting the next; all three remain concurrently suspended while lifecycle prepare runs. No production admission or owner behavior changed.
- GREEN: the exact subtest passed at `-count=20`; the required nine-package selector passed at `-count=10`; the same nine-package selector passed under `-race -count=1`; gofmt and diff-check passed.

## Task 6 independent quality fixes: final integrated verification — GREEN

- UTC evidence timestamp: `2026-08-17T18:39:21Z`
- Required selector: `cd backend && go test ./internal/backupasset/{content,catalog,search,processing,export,recovery,overlay,retention,runtime} -run '^(TestRecoveryPointSourceLifecycle|TestLifecycleDependentCleanup|TestLifecycleLateOutput)' -count=10` passed all nine packages; the same selector under `-race -count=1` passed all nine packages.
- Child 13 unchanged: `cd backend && go test ./internal/backupasset/recovery ./internal/backupasset/runtime -run 'RecoveryResult|RecoveryWorkspaceCleanup' -count=1` passed (`recovery 1.494s`, `runtime 0.075s`).
- Serial full touched packages passed: backupasset, Content, Catalog, Search, Processing, Export, Recovery, Overlay, retention, and Runtime; slowest were Recovery `32.971s`, Export `14.627s`, and Runtime `7.095s`.
- Uncached serial full backend `go test -p 1 ./... -count=1` passed; `go build ./...` passed; `go vet ./...` passed; `golangci-lint run ./...` reported `0 issues`.
- Formatting/diff/state: `gofmt -d` over every modified or untracked Go file produced no output; `git diff --check` passed; staged path count is zero.
- Safety boundary: `backup_assets.enabled` remains code-default `false`; protected `.codex/agents/trellis-research.toml` SHA-256 remains `4435ce73197ba1d29d40359a3279b6423f7e4f559a449f934c016808090066c4`; no `.codex`, Task 7 Provider deletion, Task 8 Runtime installation, Child 15/`000071`, deploy/GA, dependency, or lockfile path changed.
- Manifest rebaseline: root exact-path audit found and amended the one missing Catalog implementation path, `backend/internal/backupasset/catalog/indexer.go`; the resulting live manifest covers every second-cycle production/test/evidence path. Independent re-review remains required before Task 6 may be marked complete_checked.

## Task 6 final manifest re-review — APPROVED

- UTC evidence timestamp: `2026-08-17T18:40:31Z`
- Independent read-only verdict: no Critical, Important, or Minor finding; manifest/design approved.
- Result: adding only `backend/internal/backupasset/catalog/indexer.go` is exact and minimal because the active-build registry owns the completion channel and context-aware cancel/join seam. The already-created `catalog/source_lifecycle_test.go` covers the required behavior, so the unchanged `catalog/indexer_test.go` does not need a Modify entry.
- The reviewer expanded the live tracked/untracked production paths and found no other second-cycle path outside the manifest. No edit, stage, commit, or push occurred in the review.

## Task 6 final review fixes: Search pre-insert fence and migrated-schema owner — RED

- UTC evidence timestamp: `2026-08-17T19:09:19Z` to `2026-08-17T19:09:20Z`.
- Commands: `cd backend && go test ./internal/backupasset/search -run '^TestIndexerBeginGenerationRejectsStaleOrReleasedFenceWithoutDurableGeneration$' -count=1` and `cd backend && go test ./internal/backupasset/search -run '^TestSearchSourceLifecycleMigratedSchemaSQLite$' -count=1`.
- Exit status: `1` for both exact selectors. Stale and already-released fences each left one durable Search generation before fence validation; the real SQLite migration chain through 000070 rejected lifecycle prepare because `source_lifecycle` is not admitted by the paired 000065 Search error-code CHECK.
- Required PostgreSQL twin failed closed at `2026-08-17T19:09:27Z` only because `TEST_POSTGRES_DSN` was absent; no PostgreSQL behavioral pass is claimed here.
- Only Search test files were changed before these REDs; no production or migration file was edited.

## Task 6 final review fix: Export terminal/restart matrix — RED

- UTC evidence timestamp: `2026-08-17T19:09:48Z`.
- Command: `cd backend && go test ./internal/backupasset/export -run '^TestRecoveryPointSourceLifecycleExportFreshOwnerResumesClosedOutcomeCleanup$' -count=1`.
- Exit status: `1`; all six `failed|canceled × revoking|purge_failed|purged` fresh-owner cases returned `invalid export transition` instead of preserving the original outcome and converging through no-op or original cleanup.
- Only `export/source_lifecycle_test.go` changed before this RED.

## Task 6 final review fix: Overlay canonical usage-to-recent lock order — RED

- UTC evidence timestamp: `2026-08-17T19:10:09Z`.
- Sanitized command: `REQUIRE_POSTGRES_OVERLAY_TEST=1 TEST_POSTGRES_DSN="$ephemeral_loopback_dsn" go test ./internal/backupasset/overlay -run '^TestLifecycleOverlayPostgresClearRecentUsesCanonicalLockOrderWithoutRetry$' -count=1`.
- Exit status: `1`; the forced lifecycle-versus-`ClearRecent` interleaving produced exactly one PostgreSQL `40P01` deadlock with zero retry, proving the current recent-to-usage versus usage-to-recent inversion.
- Fixture contract: disposable PostgreSQL 17, loopback-only random port, tmpfs, no host volume, `--rm`, random shell-local credential. Cleanup proved the exact container absent and credentials unset. Only `overlay/lifecycle_test.go` changed before RED.

## Task 6 final review fixes: Content exact authority, retry, and cache proof — RED

- UTC evidence timestamp: `2026-08-17T19:10:11Z`.
- Command: `cd backend && go test ./internal/backupasset/content -run '^(TestRecoveryPointSourceLifecycleContentRequiresExactRecoveryResultLeaseBinding|TestRecoveryPointSourceLifecycleContentRejectsInvalidLiveBrokerSessionWithoutRelease|TestRecoveryPointSourceLifecycleContentRetriesTransientLiveSessionRelease|TestCacheLifecycleFailureFollowedByReconcileFailureRetainsRetryProof|TestContentLeaseSessionReleaseRetriesFailureAndMemoizesOnlySuccess)$' -count=1`.
- Exit status: `1`; behavioral failures only. Mismatched RecoveryResult attempt/fence hashes were accepted; invalid live lease/attempt/fence/controller bindings invoked release; transient release was permanently memoized; and Reconcile exposed a private path then discarded failed-chunk retry evidence.
- Only `content/source_lifecycle_test.go`, `cache_test.go`, and `lease_test.go` changed before RED.

## Task 6 final review fixes: Catalog lifecycle admission and unified teardown — RED

- UTC evidence timestamp: `2026-08-17T19:10:59Z`. Command: `cd backend && go test ./internal/backupasset/catalog -run '^TestLifecycleLateOutputCatalogRejectsGenerationAcrossAcquireRegisterWindow$' -count=1`; exit `1`. A builder acquired its lease before registry visibility, crossed a newly active lifecycle attempt, returned nil, and persisted one late generation.
- UTC evidence timestamp: `2026-08-17T19:11:04Z`. Command: `cd backend && go test ./internal/backupasset/catalog -run '^TestRecoveryPointSourceLifecycleCatalogWaitsForUnifiedBuilderTeardown$' -count=1`; exit `1`. Completion closed before final exact release, the owner returned while release remained blocked, release ran twice and unbounded, and one release began before Provider join/durable failure evidence.
- Only `catalog/source_lifecycle_test.go` changed before RED; existing full-struct failure diagnostics were simultaneously reduced to safe closed fields.

## Task 6 final review fix: Processing canonical lock order and zero hidden retry — RED

- UTC evidence timestamp: `2026-08-17T19:12:52Z` to `2026-08-17T19:12:55Z`.
- Command: `cd backend && go test ./internal/backupasset/processing -run '^TestLifecycleProcessingLocksPointThenValidatesAttemptBeforeJobMutation$' -count=1`.
- Exit status: `1`; RequestWork and Pull both observed `job_lock → point_lock → lifecycle_attempt_validation → mutation`, proving the owner/write paths do not share the required canonical point/attempt-before-job order.
- Only Processing tests changed before this RED.

## Task 6 final review fix: Recovery plan/job/point lock order — RED

- UTC evidence timestamp: `2026-08-17T19:13:31Z` to `2026-08-17T19:13:34Z`.
- Sanitized command: `REQUIRE_POSTGRES_RECOVERY_TEST=1 TEST_POSTGRES_DSN="$ephemeral_loopback_dsn" go test ./internal/backupasset/recovery -run '^TestRecoveryPointSourceLifecycleRecoveryPrepareFirstWritePostgresLockOrder$' -count=1 -v`.
- Exit status: `1`; the forced interleaving produced PostgreSQL SQLSTATEs `40P01` then `25P02` because PrepareFirstWrite held plan/job while lifecycle cancellation held point/attempt and waited for plan.
- The test also freezes no hidden retry, validation-before-mutation, unrelated-point preservation, exact source release, and unchanged Child 13 result/workspace ownership. The disposable loopback PostgreSQL 17 fixture used tmpfs, no host volume, `--rm`, and a random shell-local credential; cleanup proved zero matching containers and unset credentials. Only `recovery/source_lifecycle_test.go` changed before RED.

## Task 6 final review fix: Processing real PostgreSQL lock order — RED

- UTC evidence timestamp: `2026-08-17T19:15:07Z` to `2026-08-17T19:15:14Z`.
- Sanitized command: `REQUIRE_POSTGRES_PROCESSING_TEST=1 TEST_POSTGRES_DSN="$ephemeral_loopback_dsn" go test ./internal/backupasset/processing -run '^TestLifecycleLateOutputProcessingRequestWorkPullPostgresLockOrder$' -count=1`.
- Exit status: `1`; with the real retention lifecycle admission installed, RequestWork first locked the job and required one total transaction retry after query `40P01`; Pull first locked the job and lifecycle became the `40P01` victim. The observer covers Query/Create/Update/Delete/Raw/Row and transaction-entry counts so commit-triggered hidden retries cannot false-green.
- The unique PostgreSQL 17 fixture was loopback-only, tmpfs-backed, no host volume, `--rm`, and used an unprinted shell-local credential. All attempt containers were absent afterward and DSN/credential variables were unset. Only `processing/coordinator_test.go` and `behavior_integration_test.go` changed before RED.

## Task 6 final review-fix manifest expansion — APPROVED

- UTC evidence timestamp: `2026-08-17T19:16:25Z`.
- Root added only `content/lease.go`, `content/lease_test.go`, and `processing/behavior_integration_test.go` to the Task 6 Modify manifest before any corresponding product edit.
- Independent read-only re-review found the three additions necessary, sufficient, and minimal for transient Content session retry and real Processing lifecycle-admission/PostgreSQL proof. Every other final finding is closed by already-approved Catalog/Search/Content/Processing/Export/Recovery/Overlay paths; no migration, service, runtime-installation, Task 7/8/15, `000071`, deploy, dependency, or `.codex` path is needed.

## Task 6 final review fix: Catalog admission repository fixture parity — RED

- UTC evidence timestamp: `2026-08-17T19:27:48Z`.
- Command: `cd backend && go test ./internal/backupasset/repository -run '^TestManagedRsyncCatalogBuildCompletesWithFingerprintNone$' -count=1`.
- Exit status: `1`; the real managed-Rsync Catalog build reached the newly required lifecycle write admission but the shared Repository test schema lacked `recovery_point_lifecycle_attempts`, producing `no such table` instead of exercising the valid no-attempt path.
- The exact test had not yet been changed for this RED. The safe correction is fixture parity in the already-manifested Repository test database, not a production fallback around missing authority.

## Task 6 final review fixes: Search — GREEN

- UTC evidence timestamp: `2026-08-17T19:17:37Z` to `2026-08-17T19:17:38Z`.
- The identical pre-insert fence and migrated-SQLite selectors passed. Search now validates the exact lease fence immediately after point admission and before any generation query/insert; lifecycle prepare uses the closed, paired-schema-admitted `build_failed` reason.
- A disposable PostgreSQL 18 cross-version run of the required migrated-schema twin also passed at `2026-08-17T19:18:16Z` to `19:18:18Z` and was removed. This is supplemental only; the final required PostgreSQL 17 gate remains a root verification item.
- Adjacent/full Search, focused repeat/race, full Search race, vet, format, diff, and privacy checks passed. Only approved Search paths changed; migrations stayed untouched.

## Task 6 final review fix: Overlay canonical lock order — GREEN

- UTC evidence timestamp: `2026-08-17T19:18:31Z`.
- The identical required-PostgreSQL selector passed (`0.121s`) with zero deadlock and zero retry. Lifecycle now discovers owners without row locks, locks usage rows in sorted canonical order, requeries exact recents `FOR UPDATE`, and mutates only re-proven rows; no `40P01` retry masking was added.
- Adjacent/full PostgreSQL Overlay, `-count=20`, `-race -count=10`, vet, format, and diff checks passed. Both disposable PostgreSQL 17 fixtures were removed.

## Task 6 final review fix: Recovery canonical lock order — GREEN

- UTC evidence timestamp: `2026-08-17T19:19:30Z` to `2026-08-17T19:19:37Z`.
- The identical PostgreSQL selector passed with cancellation order plan→job→RecoveryPoint→lifecycle attempt→Recovery attempt, total retry `0`, and SQLSTATE list empty. Validation precedes every mutation and Child 13 result/workspace state remains untouched.
- Fresh PostgreSQL stress at `2026-08-17T19:21:50Z` to `19:22:11Z` passed `-count=10` and `-race -count=3`; focused/full Recovery, nine-package Task 6, Child 13, vet, format, and diff gates passed. Both fixtures were removed and credentials unset.

## Task 6 final review fix: Processing canonical lock order — GREEN

- UTC evidence timestamp: `2026-08-17T19:20:41Z` to `2026-08-17T19:20:47Z` for the identical PostgreSQL 17 selector; RequestWork and Pull first locked `recovery_points`, total transaction retries were `0`, and all observed error causes/SQLSTATEs were empty.
- UTC evidence timestamp: `2026-08-17T19:22:32Z` for the identical SQLite structural selector (`0.254s`). RequestWork and Pull now follow point→lifecycle attempt validation→exact job before mutation, and the shared behavior fixture installs the real Hold/Coordinator lifecycle admission.
- PostgreSQL adjacent behavior/authority, Processing adjacent/full/repeat/race, retention/runtime, vet, lint, format, and diff checks passed. The PostgreSQL fixture was removed and credentials unset.

## Task 6 final review fix: Export terminal/restart matrix — GREEN

- UTC evidence timestamp: `2026-08-17T19:22:15Z`.
- The identical fresh-owner selector passed. Closed failed/canceled jobs now require exact released-source proof, then no-op an already-purged cleanup or resume their original cleanup without reclassifying the execution result; unreleased source authority fails closed with zero effects.
- Adjacent, `-count=10`, full Export, focused race, vet, format, and diff gates passed. Only the approved Export source-lifecycle pair changed.

## Task 6 final review fix: Catalog admission repository fixture parity — GREEN

- UTC evidence timestamp: `2026-08-17T19:28:22Z`.
- The identical Repository selector and its adjacent reconnect/disconnect matrix passed after the shared, already-manifested Repository test DB added `RecoveryPointLifecycleAttempt` to AutoMigrate. No production fallback or Repository behavior changed.

## Task 6 final review fixes: Catalog lifecycle admission and unified teardown — GREEN

- UTC evidence timestamp: `2026-08-17T19:29:24Z`.
- The identical acquire/register selector passed (`0.107s`) and the identical unified-teardown selector passed (`0.220s`). Catalog generation creation/activation now performs transaction-bound lifecycle admission; Build alone owns Provider join, durable closed failure evidence, one bounded exact release, then unregister/close-done. `RevokeActiveBuilds` is cancel+join only.
- Catalog full, exact `-count=10`, full Catalog race, nine-package Task 6, full backend, build, vet, format, diff, and privacy checks passed.

## Task 6 final review fixes: local exact matrix — GREEN

- UTC evidence timestamp: `2026-08-17T19:31:02Z`.
- Root independently reran the complete non-PostgreSQL final-fix selector union across Content, Catalog, Search, Processing, Export, Recovery, and Overlay. Content (`0.071s`), Catalog (`0.346s`), Search (`0.183s`), Processing (`0.287s`), and Export (`0.258s`) executed and passed; Recovery and Overlay correctly reported no non-PostgreSQL tests for this exact union.
- This root run also supplies the receipt-time same-selector GREEN for Content: exact RecoveryResult attempt/hash proof, live Broker binding and transient retry, and cache lifecycle→Reconcile retry evidence all passed.

## Task 6 final Catalog test-manifest expansion — APPROVED

- UTC evidence timestamp: `2026-08-17T19:31:10Z`.
- Root added only `backend/internal/backupasset/catalog/indexer_test.go` after the unified teardown exposed its stale early-release assertion. Independent read-only re-review confirmed it necessary and minimal: production `RevokeActiveBuilds` and the old test both mandated release before Provider join, while the approved safety contract requires Build-owned join→evidence→release→done. No Runtime path or other manifest expansion is required.

## Task 6 final review fixes: root repeat/race and Child 13 gates — GREEN

- UTC evidence timestamp: `2026-08-17T19:31:47Z`.
- Required nine-package Task 6 selector passed at `-count=1`, `-count=10`, and `-race -count=1`; every Content, Catalog, Search, Processing, Export, Recovery, Overlay, retention, and Runtime package executed tests.
- UTC evidence timestamp: `2026-08-17T19:32:13Z`. Child 13 unchanged `RecoveryResult|RecoveryWorkspaceCleanup` passed (`recovery 1.475s`, `runtime 0.078s`), then serial full touched backupasset/owner/retention/runtime/repository packages passed; slowest were Recovery `32.936s`, Export `14.800s`, Runtime `7.063s`, and Repository `5.635s`.

## Task 6 final review fixes: unified PostgreSQL 17 gate — GREEN

- UTC evidence timestamp: `2026-08-17T19:39:12Z`; server version was `170011`.
- One fresh, unique PostgreSQL 17 fixture ran serial required selectors for Content, Catalog, Search behavior plus migrated-schema owner, Processing behavior/lock-order/non-current authority, Export behavior, Recovery behavior/lock-order, Overlay behavior/locked-row/canonical lock order, and retention lock-order/blocked-fence adopters/terminal restart.
- All eight packages passed: Content `1.394s`, Catalog `1.377s`, Search `1.373s`, Processing `10.687s`, Export `27.895s`, Recovery `0.469s`, Overlay `0.723s`, retention `0.546s`.
- Fixture contract: loopback-only random port, tmpfs, no host volume, `--rm`, random shell-local credential. Final proof reported `fixture_cleanup=absent remaining=0 credential_vars=0`.
- An immediately preceding run had the same all-package GREEN results but its post-test shell exited `1` because a zero-match credential scan inherited `rg`'s status under `pipefail`; the trap removed the container and independent checks found no residual container or environment. The fresh rerun above corrected only that harness check and is the authoritative gate.

## Task 6 final review fixes: uncached full backend/build/vet/lint — GREEN

- UTC evidence timestamp: `2026-08-17T19:40:33Z`.
- `cd backend && go test -p 1 ./... -count=1` passed every backend package, including Catalog `4.033s`, Content `1.299s`, Export `14.784s`, Processing `2.863s`, Recovery `32.899s`, Repository `5.588s`, Runtime `7.106s`, Search `0.777s`, and Database `23.481s`.
- `go build ./...` passed; `go vet ./...` passed; `golangci-lint run ./...` reported `0 issues`.
- A prior full run reached the same test/build/vet result and found only staticcheck `QF1008` in a Catalog test's explicit embedded-field selector. Root applied the equivalent promoted-method spelling, reran both Catalog review selectors, and reran lint clean before this authoritative full gate.

## Task 6 task-state reconciliation — UPDATED, NOT COMPLETE_CHECKED

- UTC evidence timestamp: `2026-08-17T19:43:50Z`.
- `task.json` no longer says the first Overlay RED is next. It records Tasks 1–5 as complete_checked and Task 6 as `in_progress_final_independent_rereview_pending`, with genuine review-fix RED→GREEN, full backend, and PostgreSQL 17 gates complete.
- This is bookkeeping only: Task 6 must still receive fresh independent spec and quality re-reviews with zero open findings before it may become complete_checked. Task 7/8, Child 15, `000071`, deploy/GA, dependency, and default-enable work remain unstarted.

## Task 6 final independent re-review fix: Recovery test-artifact privacy — RED

- UTC evidence timestamp: `2026-08-17T20:05:59Z`.
- Command: `cd backend && go test ./internal/backupasset/recovery -run '^TestRecoveryPointSourceLifecycleRecoveryDiagnosticsUseClosedFields$' -count=1 -v`.
- Exit status: `1`; the AST-based source regression found thirteen `%+v` diagnostics that format full Recovery claims, leases, attempts, or other authority-bearing structs instead of closed scalar fields.
- Only `recovery/source_lifecycle_test.go` changed before RED. Failure output listed source lines only and contained no fence token or private payload.

## Task 6 final independent re-review fix: expired-recent canonical lock order — RED

- UTC evidence timestamp: `2026-08-17T20:06:33Z`.
- Sanitized command: `REQUIRE_POSTGRES_OVERLAY_TEST=1 TEST_POSTGRES_DSN="$ephemeral_loopback_dsn" go test ./internal/backupasset/overlay -run '^TestLifecycleOverlayPostgresCleanupExpiredRecentUsesCanonicalLockOrderWithoutRetry$' -count=1`.
- Exit status: `1`; the deterministic `CleanupExpiredRecent` versus `ClearRecent` barriers produced one PostgreSQL `40P01`, with zero hidden retry, proving the remaining recent-to-usage versus usage-to-recent inversion.
- Fixture contract: PostgreSQL 17, loopback-only random port, tmpfs, no host volume, `--rm`, random shell-local credential. Cleanup proved the exact container absent. Only `overlay/lifecycle_test.go` changed before RED.

## Task 6 final independent re-review fix: Export test-artifact privacy — RED

- UTC evidence timestamp: `2026-08-17T20:07:26Z`.
- Command: `cd backend && go test ./internal/backupasset/export -run '^TestRecoveryPointSourceLifecycleTestDiagnosticsDoNotFormatRecoveryPointLease$' -count=1`.
- Exit status: `1`; the object-aware AST regression found seven actual `RecoveryPointLease` arguments passed to `%+v` across five Task 6 diagnostics.
- Only `export/source_lifecycle_test.go` changed before RED. The test reported source positions and variable names, not lease contents.

## Task 6 final independent re-review fix: Processing closed error chain — RED

- UTC evidence window: `2026-08-17T20:08:27Z` to `2026-08-17T20:08:43Z`.
- Command: `cd backend && go test ./internal/backupasset/processing -run '^(TestLifecycleDependentCleanupProcessingRedactsPrivatePath|TestRecoveryPointSourceLifecycleProcessingDefersDestructionUntilSearchProof)$' -count=1`.
- Exit status: `1`; direct Derived cleanup and SourceLifecycle both exposed the private filesystem cause through `errors.Unwrap`, `errors.As`, and `errors.Is`, while the public text and `ErrDerivedBlobUnavailable` sentinel remained closed. A static subtest also found the unsafe `Unwrap` accessor.
- Only `processing/derived_lifecycle_test.go` and `processing/source_lifecycle_test.go` changed before RED. The failure output itself contains no private canary, root, locator, fence, or lease.

## Task 6 final independent re-review fix: Search unified bounded teardown — RED

- UTC evidence timestamp: `2026-08-17T20:10:29Z`.
- Command: `cd backend && go test ./internal/backupasset/search -run '^TestIndexerCancellationWaitsForUnifiedBoundedTeardown$' -count=1`.
- Exit status: `1`; the behavioral regression proved failure evidence and final exact release used unbounded contexts, Release ran twice with the first call before projection join/failure evidence, `done` closed and the registry entry disappeared before final release, and another cancellation owner returned while final release remained blocked.
- The same regression proved the caller-facing cancellation remains bounded and durable failure evidence exists before final release. Only `search/indexer_test.go` changed before RED; baseline adjacent Search selectors stayed GREEN.

## Task 6 final independent re-review fix: expired-recent canonical lock order — GREEN

- UTC evidence timestamp: `2026-08-17T20:12:57Z`.
- The identical required-PostgreSQL selector passed (`0.113s`). `CleanupExpiredRecent` now freezes one cutoff, performs non-locking bounded owner discovery, locks usage rows in sorted order, requeries exact expired recents `FOR UPDATE`, and deletes/accounts only re-proven rows. No `40P01` retry masking was added.
- Adjacent and full local Overlay, full race, full PostgreSQL Overlay, exact `-count=20`, and exact PostgreSQL `-race -count=10` passed. Both PostgreSQL 17 fixtures were removed with zero matching containers.

## Task 6 final independent re-review fix: Recovery test-artifact privacy — GREEN

- UTC evidence timestamp: `2026-08-17T20:14:01Z`.
- The identical AST selector passed (`0.052s`) after all thirteen full authority diagnostics were replaced with closed scalar IDs, states, revisions, categories, and preservation booleans. Assertions were not weakened and no owner, fence token, or locator value is formatted.
- Focused/repeat/race/full Recovery, unchanged Child 13 RecoveryResult/workspace, vet, gofmt, diff, and privacy scans passed.

## Task 6 final independent re-review fix: Export test-artifact privacy — GREEN

- UTC evidence timestamp: `2026-08-17T20:14:17Z`; final lint-clean verification at `2026-08-17T20:21:17Z`.
- The identical privacy selector passed after five diagnostics were reduced to safe ID/state/status/released scalars. Its AST scan was then refactored away from deprecated `ast.Object` to lexical position-bounded binding resolution; mutation verification proved a restored lease dump still produces RED without false positives from shadowed locals.
- Adjacent/repeat/race/full Export, vet, lint, gofmt, diff, deprecated-API, and privacy scans passed.

## Task 6 final independent re-review fix: Search unified bounded teardown — GREEN

- UTC evidence timestamp: `2026-08-17T20:14:28Z`.
- The identical selector passed (`0.165s`). Search Build now uses one finite, at-most-30-second detached teardown context; persists and verifies durable closed failure evidence; performs exactly one exact lease release; then unregisters and closes `done`. Cancellation owners only cancel and join.
- Adjacent Search lifecycle/late-output selectors, exact `-count=20`, full Search, full Search race, vet, gofmt, diff, lint, and privacy scans passed. The regression's injected GORM error is explicitly checked.

## Task 6 final independent re-review fix: Processing closed error chain — GREEN

- UTC verification window: `2026-08-17T20:15:25Z` to `2026-08-17T20:20:44Z`.
- The identical selector passed (`0.119s`). Derived ciphertext removal now collapses every non-ENOENT filesystem failure directly to `ErrDerivedBlobUnavailable`; the private cause wrapper and `Unwrap` accessor no longer exist, and the SourceLifecycle path exposes the same closed sentinel only.
- An adjacent pre-existing Reconciler assertion required the private injected cause. Root added only `processing/reconciler_test.go` to the Modify manifest; independent read-only review approved it as necessary and minimal before that assertion was inverted to require `ErrDerivedBlobUnavailable` and reject the private cause while preserving all purge-failed/retry proofs.
- Adjacent/repeat/race/full Processing, package/full vet, focused lint, gofmt, diff, and privacy scans passed. Full repository lint later passed after peer-owned new-test lint findings were corrected.

## Task 6 final independent re-review fixes: root authoritative verification — GREEN

- UTC evidence timestamp: `2026-08-17T20:28:45Z`.
- Root exact Search/Processing/Export/Recovery fix union passed. The required nine-package Task 6 selector passed at `-count=1`, `-count=10`, and `-race -count=1`, with every package executing tests.
- A fresh PostgreSQL `17.11` (`server_version_num=170011`) fixture passed Overlay behavior, locked-recent proof, lifecycle-versus-ClearRecent canonical order, and the new CleanupExpiredRecent canonical-order selector. It used a unique name, loopback-only random port, tmpfs, no volume, `--rm`, and a random shell-local credential. Final proof reported `fixture_cleanup=absent remaining=0 credential_vars=0`. An earlier harness attempt executed no tests because host `psql` was absent; its trap also removed the fixture before the corrected container-internal readiness run.
- Serial full touched backupasset/owner/retention/runtime/repository packages passed. Fresh uncached `go test -p 1 ./... -count=1` passed every backend package; `go build ./...` and `go vet ./...` passed; `golangci-lint run ./...` reported `0 issues`.
- Task 6 remains in progress until a new independent spec review and a new independent quality review inspect this exact post-fix diff and report zero findings. Task 7/8, Child 15, `000071`, deploy/GA, dependency, and default-enable work remain unstarted.

## Task 6 fresh independent re-review — CHANGES REQUIRED

- UTC evidence timestamp: `2026-08-17T20:49:25Z`.
- The fresh spec review confirmed two separate Important boundedness defects: Recovery source cancellation can cross its pre-call cancellation check and enter `cancelJob`, which replaces an expired context with unbounded `context.WithoutCancel`; Export purge-failure persistence independently removes the owner deadline with unbounded `context.WithoutCancel`.
- The fresh quality review independently confirmed the Recovery defect and two further Important contract gaps: Task 6 tests still contain full authority/private-payload diagnostics (`RecoveryWorkerClaim`, Export payload/key/artifact/item), and the authoritative design/implementation artifacts still prescribe the obsolete Processing job-before-point order that the real PostgreSQL deadlock fix replaced with point-to-lifecycle-attempt-to-job.
- Both reviews found no Critical issue. Their focused, repeat, race, Child 13, full backend, and PostgreSQL gates were green, but those gates do not cover the confirmed contexts, diagnostics, or source-of-truth drift. Task 6 remains in progress; Task 7 is not started.

## Task 6 bounded owner-context and diagnostic privacy fixes — RED

- UTC evidence timestamp: `2026-08-17T20:51:56Z`.
- Command: `cd backend && go test ./internal/backupasset/recovery -run '^(TestRecoveryPointSourceLifecycleRecoveryCancellationAfterPrecheckUsesBoundedDetachedContext|TestWorkerCancelPersistsHandoffAfterCallerContextCancellation)$' -count=1 -v`.
- Exit status: `1`. A deterministic cancel-after-precheck adapter canceled the owner context exactly as source lifecycle entered `CancelRecoveryPoint`; the ensuing database query had no deadline and the source owner failed closed only after the test released it. The existing durable canceled-caller handoff control remained green.

- UTC evidence timestamp: `2026-08-17T20:52:08Z`.
- Command: `cd backend && go test ./internal/backupasset/recovery -run '^TestRecoveryPointSourceLifecycleRecoveryWorkerClaimDiagnosticsUseClosedFields$' -count=1 -v`.
- Exit status: `1`. The scoped AST regression found the Task 6 cancellation test formatting an entire `RecoveryWorkerClaim`, whose source fence contains private authority, at the then-current `worker_test.go:4748`.

- UTC evidence timestamp: `2026-08-17T20:53:14Z`.
- Command: `cd backend && go test ./internal/backupasset/export -run '^TestRecoveryPointSourceLifecycleTestDiagnosticsDoNotFormatRecoveryPointLease$' -count=1`.
- Exit status: `1`. The broadened lexical/type-aware AST guard found full aggregate payload and key/artifact/item diagnostics containing ciphertext, wrapped-key, or path-ciphertext fields. It reports source bindings and positions only, not private values.

- UTC evidence timestamp: `2026-08-17T20:53:22Z`.
- Command: `cd backend && go test ./internal/backupasset/export -run '^TestPersistentLifecyclePortPurgeFailureUsesBoundedDetachedSafePersistence$' -count=1`.
- Exit status: `1`. A real Store failure canceled the caller at purge; current failure persistence used an unbounded detached context, exposed the private Store cause, and failed to durably project `purge_failed` plus the exact pending reservation pair. The regression has a finite guard and requires a live bounded deadline, closed error graph, and durable retry state.
- Only manifested Recovery/Export tests changed before these REDs. Gofmt and whitespace checks were clean; no product, task-state, staging, commit, or push action occurred.

## Task 6 bounded owner-context and diagnostic privacy fixes — GREEN

- UTC evidence timestamp: `2026-08-17T20:55:00Z`.
- The identical Recovery bounded-handoff selector passed (`5.135s`), including the pre-existing canceled-caller durable-handoff control. Only an already-canceled/expired input is detached, with a named five-second timeout and guaranteed cancellation; live contexts remain attached. The identical scoped worker-claim AST selector passed at `20:55:17Z` after the single full-claim diagnostic was reduced to closed IDs/presence/error fields.
- Adjacent and repeated cancellation, race, full Recovery, unchanged Child 13, full vet/lint, gofmt, diff, and privacy checks passed.

- The identical Export bounded persistence and expanded diagnostic-privacy selectors passed together (`0.104s`; final fresh repeat `0.098s`). Purge-failure persistence now uses a named five-second `WithTimeout(WithoutCancel(ctx))` scope with guaranteed cancellation, durably records `purge_failed` and the exact `purge_pending` reservations, and returns only closed Export sentinels without the raw Store/private cause. Flagged payload/key/artifact/item diagnostics now expose only safe IDs, states, counts, and presence booleans.
- Adjacent/repeat/race/full Export, full Export race, vet, lint, gofmt, diff, and privacy checks passed.

## Task 6 bounded owner-context fixes: root authoritative verification — GREEN

- UTC evidence timestamp: `2026-08-17T21:08:32Z`.
- Root reran the exact Recovery and Export RED selectors; both packages passed (`5.146s` and `0.100s`). A static source-of-truth assertion proved the obsolete Processing job-before-point text is absent and the design now requires RecoveryPoint-to-lifecycle-attempt-to-exact-job ordering for both `RequestWork` and `Pull` with zero PostgreSQL retry.
- The required nine-package Task 6 selector passed at `-count=1`, `-count=10`, and `-race -count=1`, with all nine packages executing tests. The unchanged Child 13 RecoveryResult/workspace selector passed (`1.497s`/`0.078s`), and serial full touched packages passed.
- Fresh uncached `go test -p 1 ./... -count=1`, `go build ./...`, `go vet ./...`, and `golangci-lint run ./...` all passed; lint reported `0 issues`.
- Final static audit: JSON task artifacts valid, all changed Go files gofmt-clean, working/cached diff checks clean, staged count zero, feature default false, forbidden Task 7/8/Child 15/`000071`/deploy/dependency paths zero, and protected `.codex/agents/trellis-research.toml` SHA-256 remained `4435ce73197ba1d29d40359a3279b6423f7e4f559a449f934c016808090066c4`.
- One preliminary static command used a zsh array expansion that passed the full Go file list as one filename and exited before formatting; the corrected NUL-delimited command then executed the complete audit above. It changed no file and supplied no pass evidence by itself.
- Task 6 remains in progress pending a new independent spec review and new independent quality review of this exact post-fix diff. Task 7 is not started.

## Task 6 fresh bounded-context re-review — CHANGES REQUIRED

- UTC evidence timestamp: `2026-08-17T21:17:36Z`.
- The fresh read-only spec review found two Important residual test-artifact leaks: Recovery source-cancellation diagnostics format a complete workspace-cleanup tuple containing Child 13 cleanup owner/node-lease/fence authority, and Export diagnostics format complete `BackupAssetExportSourceLease` values containing lease/attempt identities plus the derived fence hash. Existing AST guards did not classify those types.
- The fresh read-only quality review independently confirmed the Recovery tuple and found the same class in Search: `assertSearchGeneration` formats a complete Search generation containing source fingerprint, lease/build-attempt IDs, and fence-token hash. It found no Critical or additional Minor issue.
- Both reviews confirmed the finite Recovery/Export contexts, canonical Processing lock order, owner/runtime/default/Child13/scope boundaries, and focused tests. Task 6 remains in progress until these three diagnostic classes are TDD-closed and a later fresh dual review reports zero findings.

## Task 6 residual private diagnostic closure — RED

- UTC evidence timestamp: `2026-08-17T21:18:11Z`.
- Command: `cd backend && go test ./internal/backupasset/export -run '^TestRecoveryPointSourceLifecycleTestDiagnosticsDoNotFormatRecoveryPointLease$' -count=1`.
- Exit status: `1`. Adding `BackupAssetExportSourceLease` to the existing lexical/type-aware private set made the guard non-vacuously flag the unrelated-source and idempotent-retry before/after bindings at the then-current lines 313 and 333. The diagnostics were not changed before RED.

- UTC evidence timestamp: `2026-08-17T21:18:32Z`.
- Command: `cd backend && go test ./internal/backupasset/recovery -run '^TestRecoveryPointSourceLifecycleRecoveryWorkerClaimDiagnosticsUseClosedFields$' -count=1 -v`.
- Exit status: `1`. The scoped guard now classifies both `RecoveryWorkerClaim` and helper-returned `recoveryWorkspaceCleanupTuple`; it isolated only the remaining tuple full-format at `worker_test.go:4800`, proving the earlier claim fix stayed closed.

- UTC evidence timestamp: `2026-08-17T21:18:49Z`.
- Command: `cd backend && go test ./internal/backupasset/search -run '^TestSearchSourceLifecycleDiagnosticsDoNotFormatFullGeneration$' -count=1`.
- Exit status: `1`. A narrow AST regression over `assertSearchGeneration` found exactly one full `BackupAssetSearchGeneration` diagnostic containing private source/build/fence authority. The helper remained unchanged before RED.
- Only manifested owner test files changed before these REDs. Gofmt and diff checks were clean; no production, task-state, staging, commit, or push action occurred.

## Task 6 residual private diagnostic closure — GREEN

- UTC evidence timestamp: `2026-08-17T21:20:14Z`.
- The identical Recovery AST selector passed (`0.058s`). The tuple inequality predicate is unchanged; failure output now contains only closed phases plus owner/expiry/fence/node-lease/node-fence/attempt presence or equality booleans, never the underlying authority values. Adjacent cancellation, Child 13, repeat, race, full Recovery, vet, lint, gofmt, diff, and privacy checks passed.
- The identical Export AST selector passed (`0.052s`). Both `BackupAssetExportSourceLease` `DeepEqual` predicates are unchanged; diagnostics now contain only source ID, state, and released-at presence. Adjacent/repeat/race/full Export, full Export race, vet, lint, gofmt, diff, and privacy checks passed.
- The identical Search AST selector passed (`0.020s`). `assertSearchGeneration` retains its state/active predicate and now reports only ID, state, active, expected/written counts, and query-error presence. Adjacent/repeat/full/race Search, vet, focused/global lint, gofmt, diff, and privacy checks passed.

## Task 6 residual private diagnostic closure: root authoritative verification — GREEN

- UTC evidence timestamp: `2026-08-17T21:27:05Z`.
- Root reran all three identical AST selectors; Recovery, Export, and Search passed (`0.059s`, `0.051s`, `0.007s`). The required nine-package Task 6 selector passed at `-count=10` and `-race -count=1`, with every package executing tests.
- Fresh uncached `go test -p 1 ./... -count=1`, `go build ./...`, `go vet ./...`, and `golangci-lint run ./...` passed; lint reported `0 issues`.
- Final static audit again proved valid task JSON, gofmt-clean changed Go files, clean working/cached diff checks, staged count zero, default false, forbidden-scope count zero, and protected `.codex/agents/trellis-research.toml` SHA-256 `4435ce73197ba1d29d40359a3279b6423f7e4f559a449f934c016808090066c4`.
- Task 6 remains in progress until a completely new independent spec and quality review inspect this exact snapshot and both report zero findings. Task 7 is not started.

## Task 6 fresh residual-privacy re-review — CHANGES REQUIRED

- UTC evidence timestamp: `2026-08-17T21:36:00Z`.
- Both fresh read-only reviews independently confirmed one Important functional defect: Search batches generation rows but `deleteProjectionTx` issues three whole-generation deletes for fields, postings, and documents in one transaction. A generation may contain millions of rows; a finite owner deadline can repeatedly roll the transaction back with zero restart progress, violating the bounded owner contract.
- The fresh spec review additionally found an Overlay Task 6 diagnostic formatting a complete decrypted favorite row, including owner/point/entry/private label, and found the new Search/Export/Recovery AST guards too narrow to durably enforce their claimed closure: they omit some formatting call families, plain `%v`, aliases/indirect expressions, and in Export do not prove required private bindings were inferred.
- No other owner, finite-context, lock-order, Child 13, default, runtime-installation, or scope finding was confirmed. Task 6 remains in progress; Task 7 is not started.

## Task 6 bounded Search payload and durable privacy guards — RED

- UTC evidence timestamp: `2026-08-17T21:38:24Z`.
- Command: `cd backend && go test ./internal/backupasset/recovery -run '^TestRecoveryPointSourceLifecycleRecoveryWorkerClaimDiagnosticsUseClosedFields$' -count=1 -v`.
- Exit status: `1`. An in-memory mutation canary with non-vacuous claim/tuple classifications exercised Errorf/Fatalf/Logf/Printf/Sprintf plus alias, selector, index, call, and plain/flagged `%v` paths; the existing scanner detected zero of five expected violations.

- UTC evidence timestamp: `2026-08-17T21:38:53Z`.
- Command: `cd backend && go test ./internal/backupasset/overlay -run '^TestLifecycleDisabledMaintenanceDiagnosticsDoNotFormatDecryptedFavorite$' -count=1`.
- Exit status: `1`. The non-vacuous scoped analyzer located the disabled lifecycle test's typed favorite binding, followed aliases, and caught its full `%+v` diagnostic of an AfterFind-decrypted private row.

- UTC evidence timestamp: `2026-08-17T21:39:02Z`.
- Command: `cd backend && go test ./internal/backupasset/export -run '^TestRecoveryPointSourceLifecycleTestDiagnosticsDoNotFormatRecoveryPointLease$' -count=1`.
- Exit status: `1`. The strengthened Export analyzer required all expected private loaders/binding classes and detected five intentional canary paths across the formatting call family, plain/flagged `%v`, aliases/selectors/indexes, and loader calls. No additional live residual was found.

- UTC evidence timestamp: `2026-08-17T21:41:40Z`.
- Command: `cd backend && go test ./internal/backupasset/search -run '^TestRecoveryPointSourceLifecycleSearchCleanupBoundsPayloadRowsPerTransactionAndRestarts$' -count=1`.
- Exit status: `1`. With row budget two and five rows in each payload table, current cleanup deleted five fields, five postings, and five documents in one transaction, leaving zero committed partial progress before the injected second-pass failure. The regression reconstructs the owner, requires bounded committed progress, FK-safe order, convergence/idempotence, and unrelated-generation preservation.
- The strengthened Search AST analyzer and its synthetic alias/full-verb mutation test were already green; the genuine product RED above remains the implementation gate. Only manifested owner tests changed before RED; gofmt and diff checks were clean.

## Task 6 bounded Search payload and durable privacy guards — GREEN

- UTC evidence timestamp: `2026-08-17T21:46:02Z`.
- The identical Recovery privacy selector passed (`0.057s`). Its shared analyzer now requires both private root classifications, follows value-preserving aliases/selector/index/composite/call expressions, maps exact plain/flagged/explicit-index `%v` arguments across Errorf/Fatalf/Logf/Printf/Sprintf, returns the exact five expected in-memory mutation violations, and returns zero for the live Task 6 cancellation test. Adjacent/Child13/repeat/race/full/vet/lint/static gates passed.
- The identical Export privacy selector passed (`0.054s`; final `0.055s`). Its shared analyzer proves required loaders and all six private binding classes, returns zero for the live file, and returns the exact ordered five violations for the in-memory mutation covering all required formatters/verbs/aliases/selectors/indexes/loader calls. Adjacent/repeat/race/full/vet/lint/static gates passed; an initial SQL-log-heavy full-race run exited 1 with truncated failure identity and was not counted, while focused race and the complete failure-filtered full-race rerun passed.
- The identical Overlay privacy selector passed (`0.007s`). The favorite predicate is unchanged; the diagnostic now exposes only opaque favorite ID, state, tombstone reason, version, and timestamp-presence booleans. Adjacent/repeat/race/full/vet/lint/static gates and a fresh full PostgreSQL Overlay package passed (`1.123s`); the disposable fixture was removed.

- The identical Search bounded/restart selector passed (`0.016s`). Each transaction now carries one total `owner.batchSize` payload-row budget; deterministic exact identities are selected and deleted fields-to-postings-to-documents with exact `RowsAffected` proof. Budget exhaustion commits progress; restart/requery converges idempotently while preserving unrelated generation payload and generation evidence. Adjacent/repeat/race/full/vet/lint/privacy gates passed.
- The migrated-schema shared SQLite/PostgreSQL contract was extended with three valid CatalogEntry-backed Search payload rows and batch size two. SQLite passed (`0.104s`). Fresh PostgreSQL `17.11` (`server_version_num=170011`) passed Search behavior plus migrated source cleanup (`1.371s`) using loopback random port, tmpfs, binds=null, no volume, and `--rm`; cleanup proof reported exact matching containers zero and DSN env absent.

## Task 6 bounded Search payload and durable privacy guards: root authoritative verification — GREEN

- UTC evidence timestamp: `2026-08-17T21:56:27Z`.
- Root reran the Search bounded/privacy union plus Overlay, Export, and Recovery privacy selectors; all passed. The required nine-package Task 6 selector passed at `-count=10` and `-race -count=1`, with every package executing tests.
- Fresh uncached `go test -p 1 ./... -count=1`, `go build ./...`, `go vet ./...`, and `golangci-lint run ./...` passed; lint reported `0 issues`.
- Final static audit proved valid task JSON, gofmt-clean changed Go files, clean working/cached diff checks, staged count zero, default false, forbidden-scope count zero, no Task 6 fixture containers, absent `TEST_POSTGRES_DSN`, and protected `.codex/agents/trellis-research.toml` SHA-256 `4435ce73197ba1d29d40359a3279b6423f7e4f559a449f934c016808090066c4`.
- Task 6 remains in progress until a completely new independent spec and quality review both report zero findings on this exact snapshot. Task 7 is not started.

## Task 6 final private-diagnostic re-review — CHANGES REQUIRED

- Evidence recorded at `2026-08-17T22:25:29Z` for the fresh reviews completed before the first repair RED at `22:13:12Z`.
- The independent spec and quality reviewers agreed that Search total payload-row batching, dual-engine migrated-schema coverage, all owner lifecycles, Child 13 isolation, finite contexts, lock order, default false, pure aggregate non-installation, and scope boundaries were correct.
- They found two remaining Important test-artifact classes: live whole-value diagnostics of private Retention leases/grants, Export jobs, and Overlay SavedSearch/Favorite rows; and privacy analyzers that could be bypassed through selector/index/index-list, identity calls, formatter or format aliases, dynamic formats, plain `%v`, or explicit-index `%[1]v`. No Critical or Minor finding remained.

## Task 6 final private-diagnostic closure — chronological RED to GREEN

- UTC `2026-08-17T22:13:12Z`: Export selector `go test ./internal/backupasset/export -run '^TestRecoveryPointSourceLifecycleTestDiagnosticsDoNotFormatRecoveryPointLease$' -count=1` failed genuinely. The strengthened non-vacuous guard safely identified eight live `BackupAssetExportJob` whole-value diagnostics and a dynamic-format plus identity-call mutation gap; no private value was printed.
- UTC `2026-08-17T22:13:32Z`: Search selector `go test ./internal/backupasset/search -run '^TestSearchGenerationDiagnosticPrivacyAnalyzerRejectsAliasesAndFullValueVerbs$' -count=1` failed genuinely because selector, index, index-list, explicit-index `%[1]v`, and formatter-alias mutations produced no violation.
- UTC `2026-08-17T22:13:59Z`: Recovery selector `go test ./internal/backupasset/recovery -run '^TestRecoveryPointSourceLifecycleRecoveryDiagnosticsUseClosedFields$' -count=1 -v` failed genuinely because the old full-file guard detected only two of five private mutations and missed plain `%v`, explicit-index `%[1]v`, and dynamic format paths.
- UTC `2026-08-17T22:15:20Z`: the identical Export selector passed (`0.053s`). The analyzer now requires live Export job/lease/payload/key/artifact/item roots and loader inference, tracks identity calls and aliased or dynamic formats, and returns exact nonempty mutation violations. All eight live job diagnostics use only opaque IDs, closed states/categories/cleanup, revisions, and attempt-presence facts; predicates are unchanged.
- UTC `2026-08-17T22:15:47Z`: the identical Search selector passed (`0.006s`). Private-expression propagation now covers selector/index/index-list/container/call/binary derivations, formatter aliases, and plain/flagged/indexed `%v`; only an explicit safe scalar field allowlist is exempt.
- UTC `2026-08-17T22:16:43Z`: the identical Recovery selector passed (`0.055s`). One shared analyzer now non-vacuously classifies the live Task 6 private roots, maps exact format arguments across Errorf/Fatalf/Logf/Printf/Sprintf, fails closed for dynamic formats, returns the exact five mutation violations, and returns zero for live diagnostics.
- UTC `2026-08-17T22:17:40Z`: Overlay selector `go test ./internal/backupasset/overlay -run '^TestLifecycleDisabledMaintenanceDiagnosticsDoNotFormatDecryptedFavorite$' -count=1` failed genuinely. The expanded all-`TestLifecycle*` guard safely found one SavedSearch and two AfterFind-decrypted Favorite whole-value diagnostics.
- UTC `2026-08-17T22:18:19Z`: Retention selector `go test ./internal/backupasset/retention -run '^TestLifecycleCoordinatorPrivateDiagnosticsUseClosedFields$' -count=1` failed genuinely. Its scoped guard found two whole `RecoveryPointLease` diagnostics and one whole `BackupAssetDeliveryGrant` diagnostic while preserving safe `LifecycleAttempt.String/GoString`; output contained only location/type/category.
- UTC `2026-08-17T22:18:57Z`: the identical Retention selector passed (`0.011s`). The private lease/grant guard is nonempty and mutation-tested across aliases, selectors, indexes, calls, the formatter family, plain/flagged/indexed `%v`, and dynamic formats. Live diagnostics expose only safe IDs, state/phase/revision, presence, and equality facts; predicates are unchanged.
- UTC `2026-08-17T22:19:37Z`: the identical Overlay selector passed (`0.009s`). The live file has zero violation and the mutation canary has exactly seven independent violations across alias, pointer, selector, index, index-list, conversion, and call paths. SavedSearch/Favorite diagnostics now omit query, owner, point/entry, label/ciphertext, and private payload while preserving assertions.
- Package-level adjacent, repeat, race, full-package, vet, lint, gofmt, and diff gates passed for every owned slice. Only the five manifested test files changed; no product, task-state, staging, commit, or push action occurred.

## Task 6 final private-diagnostic closure: root authoritative verification — GREEN

- UTC evidence timestamp: `2026-08-17T22:25:29Z`.
- Root reran the five-package exact privacy union at `-count=1`, `-count=10`, and `-race -count=1`; Search, Export, Overlay, Recovery, and Retention all passed.
- The required nine-package Task 6 selector passed at `-count=10` and `-race -count=1`; the unchanged Child 13 RecoveryResult/workspace selector passed (`1.500s`/`0.079s`).
- Fresh uncached `go test -p 1 ./... -count=1`, `go build ./...`, `go vet ./...`, and `golangci-lint run ./...` all passed; lint reported `0 issues`.
- JSON, working/cached diff, and gofmt checks passed; staged count is zero; `backup_assets.enabled` remains code-default false; forbidden Task 7/8/Child 15/`000071`/deploy/dependency scope remains absent; no Task 6 fixture container or `TEST_POSTGRES_DSN` remains; protected `.codex/agents/trellis-research.toml` SHA-256 remains `4435ce73197ba1d29d40359a3279b6423f7e4f559a449f934c016808090066c4`.
- One preliminary static wrapper was rejected before execution because it contained a disallowed cleanup form; it changed no file and supplied no pass evidence. The corrected no-cleanup static audit produced the results above.
- Task 6 remains in progress until a new independent spec and quality review inspect this exact snapshot and both report zero findings. Task 7 is not started.

## Task 6 dynamic-format and private-root re-review — CHANGES REQUIRED

- Evidence recorded at `2026-08-17T22:48:33Z` for the fresh reviews completed before the first repair RED at `22:38:44Z`.
- The fresh spec and quality reviews confirmed that all production owner lifecycles, Search row batching, migrated SQLite/PostgreSQL coverage, Child 13 ownership, finite contexts, lock order, default false, pure aggregate non-installation, and scope boundaries remained correct.
- They found no Critical or Minor issue, but confirmed privacy analyzers could still be bypassed by genuinely unknown dynamic formats, formatter aliases/reassignments, identity-call result aliases, binary/container derivations, and missing Overlay domain/model roots. Live Overlay TagAssignment/Recent/Usage diagnostics also still exposed owner-bearing whole rows.

## Task 6 dynamic-format and private-root closure — chronological RED to GREEN

- UTC `2026-08-17T22:38:44Z`: Search privacy mutation selector failed genuinely. Direct `makeFormat()`, dynamic reassignment invalidating stale static knowledge, formatter alias, and formatter reassignment each produced zero violation while the safe dynamic-scalar control stayed clean.
- UTC `2026-08-17T22:38:51Z`: Export privacy selector failed genuinely. The prior seven mutations still matched, but four new unknown-format, ordinary format reassignment, formatter alias, and formatter reassignment mutations were missed.
- UTC `2026-08-17T22:39:33Z`: Retention privacy selector failed genuinely. The existing nine mutation violations remained, while exactly seven new identity/container/generic-index/index-list/formatter-alias/reassignment/dynamic-format mutations were missed. Output contained only synthetic location/formatter/type/category.
- UTC `2026-08-17T22:39:53Z`: the identical Export selector passed (`0.054s`). Formats are tracked as known/unknown across declaration, `:=`, and ordinary `=`; private arguments with unknown formats fail closed; direct and aliased/reassigned formatter method values resolve. Live violations are zero and mutation violations are exactly eleven.
- UTC `2026-08-17T22:40:29Z`: Recovery full-file privacy selector failed genuinely. The old analyzer missed identity-call assignment, BinaryExpr, formatter alias/reassignment, and private unknown-format paths, while also exposing a safe dynamic-format false positive.
- UTC `2026-08-17T22:41:03Z`: the identical Search selector passed (`0.007s`). Source-order format assignments invalidate stale knowledge, recognized formatters with private generation-root arguments and unresolved formats fail closed, and unresolved formats carrying only allowlisted safe scalar fields stay clean.
- UTC `2026-08-17T22:41:35Z`: the identical Recovery selector passed. Formatter aliases/reassignments are fixed-point resolved; only named value-preserving identity call results and BinaryExpr roots propagate private provenance; ordinary error results remain untainted; unknown formats fail closed only with private arguments. Mutation violations are exactly nine and live violations zero.
- UTC `2026-08-17T22:41:38Z`: the identical Retention selector passed (`0.011s`). Identity passthrough, composites, `[]any` containers, index/index-list, formatter aliases/reassignments, and unknown dynamic formats preserve private lease/grant provenance. Safe `LifecycleAttempt.String/GoString` remains a deliberate non-violation through direct, identity, and container paths.
- UTC `2026-08-17T22:41:41Z`: Overlay privacy selector failed genuinely with exactly three live violations: owner-bearing TagAssignment, Recent, and Usage whole-value diagnostics. The expanded mutation set already returned all sixteen exact violations and classified domain Favorite plus model SavedSearch/Favorite/TagAssignment/Recent/Usage and their service return roots.
- UTC `2026-08-17T22:42:10Z`: the identical Overlay selector passed. Live violations are zero and mutation violations are exactly sixteen across identity/container, alias/pointer/selector/index/index-list/conversion/call, formatter alias/reassignment, literal indexed formats, and private unknown formats. The three live diagnostics now expose only closed IDs/state/reason/version/count/time-presence facts; predicates are unchanged.
- Search, Export, Overlay, Recovery, and Retention adjacent/repeat/race/full-package/vet/lint/gofmt/diff gates all passed. Only their manifested test files changed; no product, shared task-state, staging, commit, or push action occurred.

## Task 6 dynamic-format and private-root closure: root authoritative verification — GREEN

- UTC evidence timestamp: `2026-08-17T22:48:33Z`.
- Root reran the five-package exact privacy union at `-count=1`, `-count=10`, and `-race -count=1`; all packages passed.
- The required nine-package Task 6 selector passed at `-count=10` and `-race -count=1`; unchanged Child 13 RecoveryResult/workspace selectors passed (`1.493s`/`0.079s`).
- Fresh uncached `go test -p 1 ./... -count=1`, `go build ./...`, `go vet ./...`, and `golangci-lint run ./...` passed; lint reported `0 issues`.
- JSON, working/cached diff, and gofmt checks passed; staged count is `0`; `backup_assets.enabled` remains code-default false; forbidden changed path count is `0`; Task 6 fixture container count is `0`; `TEST_POSTGRES_DSN` is absent; protected `.codex/agents/trellis-research.toml` SHA-256 is unchanged at `4435ce73197ba1d29d40359a3279b6423f7e4f559a449f934c016808090066c4`.
- Task 6 remains in progress pending a new independent spec and quality review of this exact snapshot. Task 7 is not started.

## Task 6 systematic privacy and pre-purged Export re-review — CHANGES REQUIRED

- Evidence recorded at `2026-08-17T23:26:15Z` for the fresh reviews completed before the first repair RED at `23:08:18Z`.
- The fresh quality review found one production restart defect: a historical Export job already normally cleaned to `CleanupPurged`, with intentionally retained item/source evidence, was selected by the source owner and rejected before exact released-source proof, permanently blocking point lifecycle prepare.
- The spec and quality reviews also found the privacy guards still recognized only `%v`, missed some BinaryExpr/conversion/container/helper provenance, lacked complete file/helper scope in Search/Overlay, and lacked safe scalar allowlists in Export/Recovery/Overlay. Known `%s/%q/%x` formats could therefore expose private scalar fields, while safe state/ID/count fields could be falsely rejected.
- No Critical finding remained. Outside this production Export matrix and the test-artifact analyzers, Search row batching/dual-engine fixtures, all owners/contexts/locks, Child 13, default false, pure aggregate non-installation, and scope boundaries remained conformant.

## Task 6 systematic privacy and pre-purged Export closure — chronological RED to GREEN

- UTC `2026-08-17T23:08:18Z`: Retention privacy selector failed genuinely. The prior sixteen mutation violations remained, while seven new non-`v`, BinaryExpr, `any`/type conversion, explicit-index/flags/width/precision mutations were missed. `%T`, `%%`, safe grant fields, and safe `LifecycleAttempt` controls stayed clean.
- UTC `2026-08-17T23:09:10Z`: Search privacy mutation selector failed genuinely. Whole/private `%s`, flagged `%q`, explicit-index `%x`, and an ordinary test-helper/typed-loader/alias/private-selector path all produced zero violation, while `%T`/`%%`, safe fields, and dynamic-safe controls stayed clean.
- UTC `2026-08-17T23:09:16Z`: Overlay privacy selector failed genuinely. The old analyzer saw one function and roots `3/3/2/2/1` with sixteen violations; the required full-file/helper, private CompositeLit, non-`v`, and safe-control matrix expected two functions, roots `4/5/3/3/2`, and twenty-three violations.
- UTC `2026-08-17T23:10:01Z`: Recovery full-file plus worker-scoped privacy selector failed genuinely. The old analyzer returned nine versus fifteen full-file mutations and five versus ten worker mutations, missing non-`v` verbs, arbitrary AST-inferred value-preserving helpers, and typed loader roots.
- UTC `2026-08-17T23:10:47Z`: the identical Retention selector passed (`0.010s`). The parser maps every consuming verb plus explicit indexes/flags/width/precision to exact arguments, exempts `%T`/`%%`, preserves private Binary/conversion provenance, and treats equality/logical BinaryExpr results as safe booleans. Safe Attempt direct/identity/container paths remain clean.
- UTC `2026-08-17T23:11:39Z`: the identical Search selector passed (`0.006s`). The analyzer scans every real file function except its own implementation/canary, discovers typed value/pointer loaders and parameters/results, and propagates conversions/composites/aliases/selectors/indexes. Every consuming verb except `%T` is unsafe for private arguments; `%%` consumes none; unresolved private formats fail closed; the safe field allowlist prevents false positives. Whole-file live scan passed without assertion rewrite.
- UTC `2026-08-17T23:12:52Z`: Export production selector `TestRecoveryPointSourceLifecycleExportFreshOwnerNoopsPrePurgedClosedOutcome` failed genuinely. Failed/canceled/expired pre-purged jobs returned invalid transition, and the unreleased-source control returned invalid transition instead of `ErrConflict`.
- UTC `2026-08-17T23:13:58Z`: the identical Overlay privacy selector passed. The analyzer scans all top-level functions, fixed-point propagates private helper parameters, recognizes private domain/model CompositeLit roots and service returns, parses every consuming verb except `%T`, preserves formatter/dynamic/identity/container provenance, and uses a closed safe-selector allowlist. Mutation is exactly twenty-three, safe canary zero, and whole-file live scan zero.
- UTC `2026-08-17T23:14:01Z`: Export privacy selector failed genuinely. The existing eleven violations remained while seven new `%s/%q/%x`, BinaryExpr, generic container/index, conversion, and generic identity mutations were missed.
- UTC `2026-08-17T23:15:43Z`: the identical Recovery selectors passed. The shared analyzer reads same-file functions, maps typed multi-result positions, infers value-preserving one-parameter helpers by their return AST rather than name, parses all consuming verbs except `%T`, and preserves identity/Binary/composite/conversion/formatter/dynamic provenance without tainting ordinary error calls. Full mutation is exactly fifteen, worker mutation ten, and live scans zero. One raw cleanup-attempt diagnostic was reduced to a presence boolean without changing its assertion.
- UTC `2026-08-17T23:16:27Z`: the identical Export production selector passed (`0.207s`). `prepareJob` now inspects closed disposition before transition: only `CleanupPurged` failed/canceled/expired jobs with exact released source and RecoveryPoint lease proof no-op idempotently; unreleased proof returns wrapped `ErrConflict`; no effect or outcome reclassification occurs.
- UTC `2026-08-17T23:16:36Z`: the identical Export privacy selector passed (`0.052s`). Format arguments, all consuming verbs except `%T`, BinaryExpr/composites/index/conversions/generic identities, formatter aliases, and unknown private formats are tracked. A safe closed ID/state/category/revision/count allowlist prevents false positives. Live violations are zero and mutation expectations exact.
- All five packages passed their adjacent, repeat, race, full-package, vet, lint, gofmt, and diff gates; Export full race passed and global lint returned `0 issues`. Only the manifested Export production/test and four privacy test files changed; no task-state, staging, commit, or push action occurred.

## Task 6 systematic privacy and pre-purged Export closure: root authoritative verification — GREEN

- UTC evidence timestamp: `2026-08-17T23:26:15Z`.
- Root reran the five-package production/privacy union at `-count=1`, `-count=10`, and `-race -count=1`; all packages passed.
- The required nine-package Task 6 selector passed at `-count=10` and `-race -count=1`; unchanged Child 13 RecoveryResult/workspace selectors passed (`1.482s`/`0.079s`).
- Fresh uncached `go test -p 1 ./... -count=1`, `go build ./...`, `go vet ./...`, and `golangci-lint run ./...` passed; lint reported `0 issues`.
- JSON, working/cached diff, and gofmt checks passed; staged count is `0`; `backup_assets.enabled` remains code-default false; forbidden changed path count is `0`; Task 6 fixture container count is `0`; `TEST_POSTGRES_DSN` is absent; protected `.codex/agents/trellis-research.toml` SHA-256 remains `4435ce73197ba1d29d40359a3279b6423f7e4f559a449f934c016808090066c4`.
- Task 6 remains in progress pending one final new independent spec and quality review of this exact snapshot. Task 7 is not started.

## Task 6 typed-result, fmt-star, and grant-artifact re-review — CHANGES REQUIRED

- Evidence recorded at `2026-08-17T23:49:49Z` for the fresh reviews completed before the first repair RED at `23:41:10Z`.
- The reviews confirmed the pre-purged Export production matrix and all owner/runtime/scope boundaries. They found the remaining issues only in test privacy enforcement: Retention treated private grant IDs as safe and emitted whole DeliveryRequest rows; Recovery lost builtin conversion provenance and misparsed `*` width/precision/unknown verbs; Search/Export/Retention/Overlay did not precisely map private typed results by multi-return position; Overlay lacked general same-file typed-helper summaries; Export falsely rejected safe `%*T`/`%.*T`.
- No Critical or new production finding remained.

## Task 6 typed-result, fmt-star, and grant-artifact closure — chronological RED to GREEN

- UTC `2026-08-17T23:41:10Z`: Export privacy selector failed genuinely. Private typed results at second/third result positions were missed, safe scalar/error positions were misbound private, safe `%*T`/`%.*T`/indexed-star `%T` controls were falsely rejected, and private star-width arguments were miscounted.
- UTC `2026-08-17T23:41:13Z`: Overlay privacy selector failed genuinely. Nine typed-helper functions with value/pointer private returns, private results at positions zero/one/two, and helper-to-helper forwarding produced only twenty-two of twenty-nine violations and incomplete root counts.
- UTC `2026-08-17T23:41:16Z`: Recovery full-file/worker privacy selectors failed genuinely. Full mutation returned fifteen versus twenty-one and worker ten versus sixteen, missing builtin `any`/`string` conversions, `%*s`, `%.*q`, unknown `%Z`, and truncated formats.
- UTC `2026-08-17T23:41:30Z`: Search privacy selector failed genuinely. Private value result at index one and pointer result at index two were missed, while the safe error result at index zero was incorrectly bound private.
- UTC `2026-08-17T23:41:59Z`: Retention privacy selector failed genuinely. Live scan safely found two private DeliveryGrant.ID selectors and two whole DeliveryRequest values. Mutation also proved second-result Lease, first-result Request, and third-result Grant positions were misbound. Output contained only location/formatter/type/category.
- UTC `2026-08-17T23:42:58Z`: the identical Search selector passed (`0.007s`). Same-file function summaries are exact per result position, expand grouped/named results, map a single multi-result RHS to exact LHS positions, ignore `_`, and bind only the actual private value/pointer generation result. Live and safe controls remain zero.
- UTC `2026-08-17T23:43:07Z`: the identical Recovery selectors passed. Builtin conversions preserve only private provenance; width/precision star arguments are consumed separately from the formatted value; `%T`/`%%` remain safe; unknown/truncated verbs with private values fail closed. Full mutation is exactly twenty-one, worker sixteen, and live scans zero.
- UTC `2026-08-17T23:43:22Z`: the identical Export selector passed (`0.052s`). Per-position typed result summaries map assignments/declarations exactly. The fmt parser consumes star width/precision arguments and preserves safe formatted-value `%T`; unknown private formats remain fail closed. Live violations are zero and mutation is exact.
- UTC `2026-08-17T23:43:22Z`: the identical Overlay selector passed. Per-FuncDecl positional typed summaries cover value/pointer and named/unnamed multi-results; local calls are mapped before external service summaries; result provenance flows through forwarding and ordinary formatter helpers. Mutation is exactly twenty-nine, safe canary and live file zero.
- UTC `2026-08-17T23:43:22Z`: the identical Retention selector passed (`0.013s`). DeliveryRequest is a private root and DeliveryGrant.ID is no longer safe. Live grant/request diagnostics now expose only state/version/equality facts, never grant/request/ticket IDs; predicates are unchanged. Positional summaries correctly bind private results in first/second/third positions across short/var declarations, assignments, and aliases.
- All five package adjacent/repeat/race/full-package/vet/lint/gofmt/diff gates passed; Export full race and global lint passed with `0 issues`. Only the five manifested test files changed in this round; no product, task-state, staging, commit, or push action occurred.

## Task 6 typed-result, fmt-star, and grant-artifact closure: root authoritative verification — GREEN

- UTC evidence timestamp: `2026-08-17T23:49:49Z`.
- Root reran the five-package production/privacy union at `-count=1`, `-count=10`, and `-race -count=1`; all packages passed.
- The required nine-package Task 6 selector passed at `-count=10` and `-race -count=1`; unchanged Child 13 RecoveryResult/workspace selectors passed (`1.505s`/`0.078s`).
- Fresh uncached `go test -p 1 ./... -count=1`, `go build ./...`, `go vet ./...`, and `golangci-lint run ./...` passed; lint reported `0 issues`.
- JSON, working/cached diff, and gofmt checks passed; staged count is `0`; `backup_assets.enabled` remains false; forbidden path and Task 6 fixture container counts are `0`; `TEST_POSTGRES_DSN` is absent; protected `.codex/agents/trellis-research.toml` SHA-256 remains `4435ce73197ba1d29d40359a3279b6423f7e4f559a449f934c016808090066c4`.
- Task 6 remains in progress pending a completely new independent spec and quality review of this exact snapshot. Task 7 is not started.

## Task 6 helper-provenance, fmt-extra, and live PathError re-review — CHANGES REQUIRED

- Evidence recorded at `2026-08-18T00:19:47Z` for the fresh reviews completed before the first repair RED at `00:06:59Z`.
- The reviews confirmed all production owner semantics, including pre-purged Export, but found privacy-guard gaps in Recovery/Retention fmt star and unconsumed-extra handling; generic and two-layer helper provenance; map-key propagation; Export cross-block assignment scope; and Search/Overlay comparison-bool false positives. They also found live Export and Processing test diagnostics formatting `os.PathError` values carrying private locators.
- No Critical finding or new production defect remained.

## Task 6 helper-provenance, fmt-extra, and live PathError closure — chronological RED to GREEN

- UTC `2026-08-18T00:06:59Z`: Overlay privacy selector failed genuinely with exactly one safe-canary false positive for private-selector comparison/logical results. Unsafe string/numeric/bitwise BinaryExpr mutation controls already remained exact.
- UTC `2026-08-18T00:07:12Z`: Search privacy selector failed genuinely. Generic identity/index-list, two ordinary helper hops, and private map keys produced zero violation, while private comparison/equality/logical booleans were falsely rejected.
- UTC `2026-08-18T00:07:16Z`: Retention privacy selector failed genuinely. Star operands were already caught, but two-layer helpers, private map keys, and private extras after `%T`/`%%` were missed. Live diagnostics remained zero.
- UTC `2026-08-18T00:07:19Z`: the identical Overlay selector passed. Comparison/order/logical BinaryExpr results are safe booleans; every other binary operation still propagates private provenance. Mutation is exactly thirty-two, safe canary and live scan zero.
- UTC `2026-08-18T00:07:40Z`: Export privacy selector failed genuinely. It found one live raw `PathError` plus missed cross-block outer-variable assignment, two-layer helper/formatter, and private map-key mutations.
- UTC `2026-08-18T00:07:56Z`: Recovery full-file/worker privacy selectors failed genuinely. Full mutation returned twenty-one versus twenty-eight and worker sixteen versus twenty-three, missing private star operands, private extras after `%T`/`%%`, multiple extras, and a generic-to-`any` two-layer formatter chain.
- UTC `2026-08-18T00:08:21Z`: Processing scoped privacy selector failed genuinely and safely reported only the two raw-error diagnostic locations. It printed no error text, root, or locator.
- UTC `2026-08-18T00:08:41Z`: the identical Retention selector passed (`0.011s`). Consumed and unsafe argument sets distinguish safe `%T` consumption from unconsumed private extras; `%%` and multiple extras fail closed; star operands remain unsafe. Helper provenance reaches a fixed point through generic layers, formatter aliases remain recognized, and map key/value provenance is preserved.
- UTC `2026-08-18T00:09:15Z`: the identical Export selector passed (`0.051s`). Ordinary `=` updates inherit the declaration scope across inner blocks; helper parameter provenance reaches fixed point; map keys and values propagate. The raw `PathError` diagnostic now emits only `category=read_failed`, safe artifact ID, and an error-presence flag; the predicate remains `err != nil`.
- UTC `2026-08-18T00:11:47Z`: the identical Search selector passed (`0.007s`). Same-file summaries include per-result static kinds plus parameter dependencies to a helper-to-helper fixed point; generic IndexExpr/IndexListExpr calls and map keys propagate only corresponding private arguments. Comparison/order/logical results are safe booleans; concatenation/arithmetic remain private.
- UTC `2026-08-18T00:12:27Z`: Processing's identical selector and final global verification passed. Both predicates remain unchanged; diagnostics now report only error presence and `errors.Is(err, os.ErrNotExist)` booleans. The scoped guard covers exactly two live sites and detects a raw-error mutation.
- UTC `2026-08-18T00:12:34Z`: the identical Recovery selectors passed. The parser returns consumed and unsafe sets: width/precision stars and values are consumed independently, `%T` safely consumes only its own value, `%%` consumes none, and every unconsumed private argument is reported as fmt EXTRA. Local typed/generic/`any` helper parameters propagate monotonically to fixed point; ordinary error results remain untainted. Full mutation is exactly twenty-eight, worker twenty-three, live/safe controls zero.
- All six packages passed adjacent/repeat/race/full-package/vet/lint/gofmt/diff gates; Export full race and global lint passed with `0 issues`. Only the six manifested test files changed; no production, task-state, staging, commit, or push action occurred.

## Task 6 helper-provenance, fmt-extra, and live PathError closure: root authoritative verification — GREEN

- UTC evidence timestamp: `2026-08-18T00:19:47Z`.
- Root reran the six-package production/privacy union at `-count=1`, `-count=10`, and `-race -count=1`; all packages passed.
- The required nine-package Task 6 selector passed at `-count=10` and `-race -count=1`; unchanged Child 13 RecoveryResult/workspace selectors passed (`1.576s`/`0.085s`).
- Fresh uncached `go test -p 1 ./... -count=1`, `go build ./...`, `go vet ./...`, and `golangci-lint run ./...` passed; lint reported `0 issues`.
- JSON, working/cached diff, and gofmt checks passed; staged count is `0`; `backup_assets.enabled` remains false; forbidden path and Task 6 fixture container counts are `0`; `TEST_POSTGRES_DSN` is absent; protected `.codex/agents/trellis-research.toml` SHA-256 remains `4435ce73197ba1d29d40359a3279b6423f7e4f559a449f934c016808090066c4`.
- Task 6 remains in progress pending a completely new independent spec and quality review of this exact snapshot. Task 7 is not started.

## Task 6 deduplicated cleanup and privacy-control final re-review — CHANGES REQUIRED

- Evidence recorded at `2026-08-18T00:50:22Z` for the fresh read-only reviews completed before the repair REDs.
- The reviews found one production defect: Processing appended the same shared deduplicated blob once per artifact, so the second final state update falsely returned `ErrDerivedBlobUnavailable` after the ciphertext was already removed.
- The reviews also found test-privacy enforcement gaps: Search/Export treated shadowed or conditional assignments as definite sanitizers; Search/Export/Retention missed generic void formatter sinks; Export/Recovery/Retention lost parameter provenance in non-first helper results; Search/Export/Retention mis-modeled explicit argument reordering; and Overlay over-tainted helpers returning closed comparison booleans.
- No Critical finding was reported. Export pre-purged handling, Search total row budget and migrated PostgreSQL fixture, Child 13 isolation, default-false behavior, pure uninstalled runtime aggregate, owner ordering, and scope boundaries remained conformant.

## Task 6 deduplicated cleanup and privacy-control closure — chronological RED to GREEN

- UTC `2026-08-18T00:36:09Z`: Retention privacy selector failed genuinely. It missed a generic void sink through two relays and a private map in the second `(int, any)` result through two relays, and falsely classified the unused private first argument of `%[2]T` as fmt EXTRA. Live diagnostics remained zero.
- UTC `2026-08-18T00:36:12Z`: Export privacy selector failed genuinely. It missed a conditional sanitizer, a two-layer generic void sink, and second-result private map key/value relays, while falsely rejecting the safe `%[2]T` reordered control.
- UTC `2026-08-18T00:36:38Z`: Overlay privacy selector failed genuinely with one safe-canary violation for ordinary helpers returning private-derived presence/equality/comparison booleans. Unsafe string, numeric, and bitwise helper results remained exact controls.
- UTC `2026-08-18T00:36:46Z`: Recovery full-file and worker privacy selectors failed genuinely. They missed private map-key and map-value provenance in the second `(int, any)` result after a second relay; safe `(int, error)` controls remained clean.
- UTC `2026-08-18T00:36:47Z`: Search privacy selector failed genuinely. It missed an outer dynamic format hidden by an inner `%T` shadow and a generic void helper chain, and falsely rejected the safe `%[2]T` reordered control.
- UTC `2026-08-18T00:37:22Z`: Processing shared-blob selector failed genuinely. Two artifacts referencing one legal deduplicated blob produced two removal/purge/final-update attempts, a false closed sentinel, and duplicate work on restart.
- UTC `2026-08-18T00:37:43Z`: the identical Overlay selector passed. Per-result helper summaries now distinguish closed boolean results from unsafe derived values; mutation remains exact `35/35`, safe and live scans remain zero.
- UTC `2026-08-18T00:39:16Z`: the identical Retention selector passed (`0.013s`). Void formatter sinks and per-result parameter provenance reach fixed point, map keys and values remain private, and explicit reordering suppresses only genuinely skipped EXTRA arguments.
- UTC `2026-08-18T00:39:50Z`: the identical Recovery selectors passed. Symbolic per-result parameter provenance reaches fixed point through typed/generic relay chains without tainting ordinary errors or safe return positions.
- UTC `2026-08-18T00:40:03Z`: the identical Export selector passed (`0.057s`). Conditional assignments conservatively join provenance; void formatter sinks and per-result relays reach fixed point; explicit argument reordering is modeled without losing unsafe consumed-argument checks.
- UTC `2026-08-18T00:42:32Z`: the identical Search selector passed (`0.008s`). Lexical bindings use `go/types` object identity, void helper parameter taint reaches fixed point, and explicit-index skipped-argument semantics are accurate.
- Processing's identical selector passed after the RED (`0.080s`). The owner now revokes every artifact reference but processes each distinct BlobID exactly once; the regression proves one purge/removal/final update, restart idempotence, and unrelated blob/reference/ciphertext preservation.
- All six package-level adjacent, repeat, race, full-package, vet, lint, gofmt, and diff gates passed. No shared task/evidence or Git action was performed by the repair slices.

## Task 6 deduplicated cleanup and privacy-control closure: root authoritative verification — GREEN

- UTC evidence timestamp: `2026-08-18T00:50:22Z`.
- Root reran the seven-selector six-package union at `-count=1`, `-count=10`, and `-race -count=1`; all packages passed.
- The required nine-package Task 6 selector passed at `-count=10` and `-race -count=1`; unchanged Child 13 RecoveryResult/workspace selectors passed (`1.563s`/`0.079s`).
- Fresh uncached `go test -p 1 ./... -count=1`, `go build ./...`, `go vet ./...`, and `golangci-lint run ./...` passed; lint reported `0 issues`.
- A fresh disposable PostgreSQL 17 matrix passed with `server_version_num=170011`: Content, Catalog, Search migrated schema, Processing behavior/lock-order/non-current authority, Export, Recovery lock-order, Overlay behavior and both canonical recent lock-order regressions, and Retention lifecycle lock order. The fixture used a loopback random port, tmpfs, no bind/host volume, and `--rm`; final inspect was absent, exact-name count was `0`, and credential environment was absent.
- JSON, working/cached diff, untracked whitespace, and gofmt checks passed; staged count is `0`; `backup_assets.enabled` remains code-default false; forbidden changed path count is `0`; Task 6 fixture container count is `0`; `TEST_POSTGRES_DSN` is absent; protected `.codex/agents/trellis-research.toml` SHA-256 remains `4435ce73197ba1d29d40359a3279b6423f7e4f559a449f934c016808090066c4`.
- Task 6 remains in progress pending a completely new independent spec and quality review of this exact snapshot. Task 7 is not started.

## Task 6 closed schema values and control-flow privacy re-review — CHANGES REQUIRED

- Evidence recorded at `2026-08-18T01:25:31Z` for the fresh zero-history read-only spec and quality reviews completed before the repair REDs.
- The quality review found two production contract violations: Content persisted `revocation_reason=source_lifecycle`, which both production migration checks reject, and Catalog persisted `error_code=source_lifecycle`, which its closed DTO parser rejects.
- The independent reviews also found privacy-control gaps: Search conditional format assignment and private-value lexical shadowing; Overlay generic void sinks and formatter alias shadowing; Retention formatter alias shadowing; and Export/Recovery/Retention multi-statement, multi-branch, and multi-parameter result provenance.
- No Critical or Minor finding was reported. Processing shared-blob cleanup, Search row batching/migrated fixtures, owner order and boundedness, Child 13 isolation, default false, pure uninstalled runtime aggregate, and forbidden-scope boundaries remained conformant.

## Task 6 closed schema values and control-flow privacy closure — chronological RED to GREEN

- UTC `2026-08-18T01:09:56Z`: Recovery full-file and worker privacy selectors failed genuinely. Each missed the later private source in one `(int, any)` result across two return branches and a second relay; safe joins remained clean.
- UTC `2026-08-18T01:10:12Z`: Overlay privacy selector failed genuinely with exactly three missing violations: generic `IndexExpr` and `IndexListExpr` void sinks, plus the outer formatter alias lost after an inner nonformatter shadow.
- UTC `2026-08-18T01:10:59Z`: Content lifecycle selector failed genuinely when an equivalent production revocation-reason CHECK rejected `source_lifecycle` during exact-point grant revocation.
- UTC `2026-08-18T01:11:16Z`: Export privacy selector failed genuinely. It missed an extra-statement return, safe/private branch union, and safe-first/private-second multi-source result propagation.
- UTC `2026-08-18T01:11:20Z`: the identical Content selector passed. Lifecycle revocation now uses closed `point_unavailable`; a recreated owner repeats cleanup without changing the settled grant version, reason, or revocation timestamp.
- UTC `2026-08-18T01:12:00Z`: Catalog lifecycle selector failed genuinely. A lifecycle-closed building generation could not pass `generationDTO` or `projectStatus` because `source_lifecycle` was an unknown error code.
- UTC `2026-08-18T01:12:07Z`: Search privacy selector failed genuinely. It missed conditional dynamic/safe format join and misclassified lexical private/safe shadowing.
- UTC `2026-08-18T01:12:19Z`: the identical Overlay selector passed. Generic callee unwrapping covers indexed generic calls, and formatter aliases use explicit lexical block bindings; mutation is exact `38/38`, safe and live scans zero.
- UTC `2026-08-18T01:12:29Z`: Retention privacy selector failed genuinely. It misresolved an outer formatter after inner shadowing and missed a joined safe/private multi-return source.
- UTC `2026-08-18T01:12:31Z`: the identical Recovery selectors passed. Every result position now carries an ordered source-parameter set unioned across all returns, assignments, generic calls, and relays; full mutation is exact `32`, worker mutation `27`, live/safe scans zero.
- UTC `2026-08-18T01:13:23Z`: the identical Export selector passed. Per-result source sets now conservatively cover all return statements, branches, multi-statement bodies, composite/map expressions, and relay layers while preserving safe controls.
- UTC `2026-08-18T01:14:37Z`: the identical Retention selector passed. Formatter aliases are lexical, and per-result source sets union across every return and helper fixed point; private sources win safe/private joins while LifecycleAttempt safe diagnostics remain allowed.
- UTC `2026-08-18T01:16:10Z`: the identical Catalog selector passed. Lifecycle-abandoned building generations now use closed `GenerationErrorBuildAbandoned`; DTO/status parsing and restart/idempotence are preserved without expanding the registry or migrations.
- UTC `2026-08-18T01:16:11Z`: the identical Search selector passed. Format and private-value bindings use lexical object identity; conditional assignment conservatively joins current and branch values; live, mutation, and safe controls remain exact.
- All five repair owners passed their package-level adjacent, repeat, race, full-package, vet, lint, gofmt, and diff gates. No migration, shared task/evidence, staging, commit, or push action occurred in the slices.

## Task 6 closed schema values and control-flow privacy closure: root authoritative verification — GREEN

- UTC evidence timestamp: `2026-08-18T01:25:31Z`.
- Root reran the eight-selector seven-package union at `-count=1`, `-count=10`, and `-race -count=1`; all packages passed.
- The required nine-package Task 6 selector passed at `-count=10` and `-race -count=1`; unchanged Child 13 RecoveryResult/workspace selectors passed (`1.550s`/`0.080s`).
- Fresh uncached `go test -p 1 ./... -count=1`, `go build ./...`, `go vet ./...`, and `golangci-lint run ./...` passed; lint reported `0 issues`.
- The Content exact selector enforces the production revocation-reason allowlist and passed with `point_unavailable`; both SQLite and PostgreSQL migration contracts contain that value. Catalog DTO/status parsing passed with the existing closed build-abandoned code.
- A fresh disposable PostgreSQL 17 matrix passed with `server_version_num=170011`: Content, Catalog, Search migrated schema, Processing behavior/lock-order/non-current authority, Export, Recovery lock-order, Overlay behavior and both canonical recent lock-order regressions, and Retention lifecycle lock order. The fixture used a loopback random port, tmpfs, no bind/host volume, and `--rm`; final inspect was absent, exact-name count was `0`, and credential environment was absent.
- JSON, working/cached diff, untracked whitespace, and gofmt checks passed; staged count is `0`; `backup_assets.enabled` remains code-default false; forbidden changed path count is `0`; Task 6 fixture container count is `0`; `TEST_POSTGRES_DSN` is absent; protected `.codex/agents/trellis-research.toml` SHA-256 remains `4435ce73197ba1d29d40359a3279b6423f7e4f559a449f934c016808090066c4`.
- Task 6 remains in progress pending a completely new independent spec and quality review of this exact snapshot. Task 7 is not started.

## Task 6 database identity and range/control-flow re-review — CHANGES REQUIRED

- Evidence recorded at `2026-08-18T01:57:25Z` for the fresh zero-history read-only spec and quality reviews completed before the repair REDs.
- The quality review found that Catalog and Search source-owner constructors accepted an Indexer backed by a different database, allowing lifecycle admission in database A to cancel an in-process same-point builder owned by database B.
- Both reviews also found privacy-control gaps: all five analyzers missed `RangeStmt` key/value assignments; Export/Overlay/Retention treated conditional formatter or format overwrites as definite; and Overlay generic indexed void sinks plus formatter shadowing remained attackable.
- No Critical or Minor finding was reported. Closed Content/Catalog values, Processing shared-blob cleanup, Search row batching/migrated fixtures, owner boundedness, Child 13 isolation, default false, uninstalled runtime aggregate, and forbidden-scope boundaries remained conformant.

## Task 6 database identity and range/control-flow closure — chronological RED to GREEN

- UTC `2026-08-18T01:44:00Z`: Overlay privacy selector failed genuinely with five missing violations: Range key/value provenance and formatter aliases lost after conditional if/loop/switch overwrites.
- UTC `2026-08-18T01:44:03Z`: Catalog constructor selector failed genuinely. Nil-indexer-db and cross-database indexer cases both returned an owner with no error.
- UTC `2026-08-18T01:44:09Z`: Search constructor selector failed genuinely with the same nil/cross-database acceptance.
- UTC `2026-08-18T01:44:15Z`: Search privacy selector missed both ranged private key/value diagnostics; Recovery full-file and worker selectors each missed seven new range cases.
- UTC `2026-08-18T01:44:55Z`: the identical Overlay selector passed. Range bindings inherit private source kinds, and conditional formatter assignments conservatively join the outer lexical binding; mutation is exact `46/46`, safe and live scans zero.
- UTC `2026-08-18T01:45:09Z`: Export privacy selector failed genuinely with five missing violations: slice range value, map key/value, conditional dynamic-format sanitizer, and conditional formatter sanitizer.
- UTC `2026-08-18T01:45:53Z`: the identical Recovery selectors passed. Slice/channel values and map key/value provenance flow through all formatters and helper parameter sources; full mutation is exact `39`, worker mutation `34`, live/safe scans zero.
- UTC `2026-08-18T01:45:59Z`: the identical Catalog constructor selector passed. The constructor requires a nonnil Indexer database with exact pointer identity; the cross-database same-point builder remains uncanceled.
- UTC `2026-08-18T01:46:00Z`: the identical Search constructor selector passed with the same database-identity invariant.
- UTC `2026-08-18T01:46:01Z`: the identical Search privacy selector passed. Range key/value lexical objects receive conservative container provenance while the safe scalar range control remains zero.
- UTC `2026-08-18T01:46:22Z`: Retention privacy selector failed genuinely. It missed Range key/value and ranged Attempt selectors, erased outer formatter aliases across if/loop/switch, degraded known format joins, and falsely rejected an all-`%T` safe format.
- UTC `2026-08-18T01:47:08Z`: the identical Export selector passed. Range bindings and conditional if/loop/range/switch/type-switch/select assignments are conservatively joined without losing definite safe controls.
- UTC `2026-08-18T01:49:49Z`: the identical Retention selector passed. Declaration-scoped Range provenance and conditional formatter/format joins preserve exact literal candidates, explicit-index/star precision, safe LifecycleAttempt formatting, and all-`%T` controls.
- The external Runtime fixture initially failed the stricter Search constructor, then passed after constructing a production LeaseService, Keyring, and Search Indexer on the identical fixture database. Focused and full Runtime, vet, lint, gofmt, and diff gates passed; no Task 8 wiring was added.
- All repair slices passed their package-level adjacent, repeat, race, full-package, vet, lint, gofmt, and diff gates. No shared task/evidence, staging, commit, or push action occurred in the slices.

## Task 6 database identity and range/control-flow closure: root authoritative verification — GREEN

- UTC evidence timestamp: `2026-08-18T01:57:25Z`.
- Root reran the eight-selector six-package union at `-count=1`, `-count=10`, and `-race -count=1`; all packages passed.
- The required nine-package Task 6 selector passed at `-count=10` and `-race -count=1`; unchanged Child 13 RecoveryResult/workspace selectors passed (`1.602s`/`0.080s`).
- Fresh uncached `go test -p 1 ./... -count=1`, `go build ./...`, `go vet ./...`, and `golangci-lint run ./...` passed; lint reported `0 issues`.
- A fresh disposable PostgreSQL 17 matrix passed with `server_version_num=170011`: Content, Catalog, Search migrated schema, Processing behavior/lock-order/non-current authority, Export, Recovery lock-order, Overlay behavior and both canonical recent lock-order regressions, and Retention lifecycle lock order. The fixture used a loopback random port, tmpfs, no bind/host volume, and `--rm`; final inspect was absent, exact-name count was `0`, and credential environment was absent.
- JSON, working/cached diff, untracked whitespace, and gofmt checks passed; staged count is `0`; `backup_assets.enabled` remains code-default false; forbidden changed path count is `0`; Task 6 fixture container count is `0`; `TEST_POSTGRES_DSN` is absent; protected `.codex/agents/trellis-research.toml` SHA-256 remains `4435ce73197ba1d29d40359a3279b6423f7e4f559a449f934c016808090066c4`.
- Task 6 remains in progress pending a completely new independent spec and quality review of this exact snapshot. Task 7 is not started.

## Task 6 same-underlying-database alias re-review — CHANGES REQUIRED

- Evidence recorded at `2026-08-18T02:26:54Z` for the fresh zero-history reviews completed before the repair REDs.
- The independent spec review reported zero findings. The quality review found that Catalog and Search source-owner constructors used `*gorm.DB` pointer equality, which rejects normal `Session` and `WithContext` handles even though GORM creates a new handle over the same underlying database.
- The quality review's second proposed finding about `%[2]T` leaking the skipped first argument through `fmt` EXTRA output was rejected with local Go 1.26 source evidence. `fmt/print.go:1176-1179` explicitly skips the extra-argument check whenever argument reordering occurred (`if !p.reordered`), so the existing explicit-index safe controls model the runtime correctly and required no edit.
- No Critical or Minor finding was reported. All other Task 6 owner, privacy, boundedness, Child 13, default-false, runtime-installation, and forbidden-scope boundaries remained conformant.

## Task 6 same-underlying-database alias closure — chronological RED to GREEN

- UTC `2026-08-18T02:11:07Z`: `go test ./internal/backupasset/catalog -run '^TestNewSourceLifecycleCatalogRejectsMismatchedIndexerDatabase$' -count=1` failed genuinely. Only the `Session` and `WithContext` same-database clone cases were rejected; nil DB and true cross-database controls remained correct.
- UTC `2026-08-18T02:11:13Z`: the identical Search constructor selector failed for the same two clone cases. Both tests prove the clone pointer differs from the owner handle, retain true cross-database rejection, and prove the rejected cross-database owner never cancels the same-point builder.
- UTC `2026-08-18T02:12:26Z` and `02:12:31Z`: the Catalog and Search selectors additionally failed on an unresolvable GORM handle, freezing the requirement that inability to derive the underlying database identity must fail closed.
- UTC `2026-08-18T02:12:56Z` and `02:13:01Z`: the identical Catalog and Search selectors passed. Each constructor now resolves both handles through `DB()`, rejects any error or nil result, accepts exact shared `*sql.DB` identity, and rejects a genuinely different pool.
- Catalog/Search adjacent lifecycle and late-output selectors, constructor `-count=10`, full-package race, full Catalog/Search, focused and full Runtime, package and global vet, and package and global lint all passed. Lint reported `0 issues`; gofmt and diff checks were clean. Only the four manifested Catalog/Search source-lifecycle files changed; no Runtime, migration, task-state, staging, commit, or push action occurred in the repair slice.

## Task 6 same-underlying-database alias closure: root authoritative verification — GREEN

- UTC evidence timestamp: `2026-08-18T02:26:54Z`.
- Root reran the exact Catalog/Search constructor selectors at `-count=1`, `-count=10`, and `-race -count=3`; all passed. The required nine-package Task 6 union passed at `-count=1`, split `-count=10` including Recovery's `51.929s` run, and `-race -count=1`. Unchanged Child 13 RecoveryResult/workspace selectors passed (`1.548s`/`0.085s`).
- Fresh uncached `go test -p 1 ./... -count=1`, `go build ./...`, `go vet ./...`, and `golangci-lint run ./...` passed; lint reported `0 issues`.
- The first disposable PostgreSQL orchestration stopped before tests because `pg_isready` raced target-database creation; its trap removed the fixture. A second parallel package run reached real tests but exhausted the shared fixture's lock-table memory (`SQLSTATE 53200`) while many packages migrated schemas concurrently; completed packages passed and the trap again removed the fixture. Neither infrastructure run is counted as a product gate.
- A fresh serial (`go test -p 1`) disposable PostgreSQL 17 matrix then passed with `server_version_num=170011`: Content, Catalog, Search behavior plus migrated row-batched cleanup, Processing behavior/lock-order/non-current authority, Export, Recovery behavior/lock-order, Overlay behavior and all three recent lock-order regressions, and Retention lifecycle lock order. The fixture used a loopback random port, `--rm`, no bind or volume, and tmpfs only (`Mounts=[]`, `Binds=null`); final matching container count was `0` and credential/DSN environment was absent.
- Task JSON, working/cached diff, changed-file gofmt, and untracked whitespace checks passed; staged count is `0`; `backup_assets.enabled` remains code-default false; forbidden changed path count is `0`; Task 6 fixture container count is `0`; `TEST_POSTGRES_DSN` is absent; protected `.codex/agents/trellis-research.toml` SHA-256 remains `4435ce73197ba1d29d40359a3279b6423f7e4f559a449f934c016808090066c4`.
- Task 6 remains in progress pending a completely new independent spec and quality review of this exact snapshot. Task 7 is not started.

## Task 6 Content bounded-cardinality re-review — CHANGES REQUIRED

- Evidence recorded at `2026-08-18T03:00:01Z` for the fresh frozen-snapshot reviews completed before the repair RED.
- The independent quality review approved frozen snapshot `4759dbcdc513ced06852e23b901553dfc1582225edf25b2e45416bd745d9c50d` with zero Critical, Important, or Minor findings. It independently confirmed the Catalog/Search underlying-database identity fix, Processing higher-level non-current authority settlement, Go 1.26 reordered-format behavior, all owner/runtime/default/Child 13 boundaries, and focused checks.
- The independent spec review found one Important boundedness defect: Content built all exact-point live broker sessions/read waits under the global broker mutex and loaded every preserved RecoveryResult lease binding into one point/attempt-locking transaction, despite its configured owner batch bound and the absence of a per-point RecoveryResult cardinality cap.
- The proposed Processing lease-binding concern was withdrawn after the review confirmed the explicit non-current-authority regression intentionally grants the higher-level source owner exact point + `processing_job` holder + job-owner revocation authority without applying ordinary worker-use attempt/fence validation. No Critical or Minor finding remained.

## Task 6 Content bounded-cardinality closure — chronological RED to GREEN

- UTC `2026-08-18T02:48:53Z`: `go test ./internal/backupasset/content -run '^TestRecoveryPointSourceLifecycleContentBatchesBrokerAndRecoveryResultProof$' -count=1` failed genuinely. With `batchSize=2`, the first blocked broker release observed all five target grants marked for revocation instead of the deterministic first two, proving unbounded global-mutex work. An earlier `02:48:35Z` run had only a test-fixture enum compile error and is not RED evidence.
- UTC `2026-08-18T02:51:33Z`: the identical selector passed. Content now queries at most one deterministic grant batch under a freshly validated lifecycle attempt, drains only those exact in-memory sessions/read waits, settles their durable grants, and restarts safely after cancellation between batches.
- The same regression seeds five preserved RecoveryResult grants/requests/leases and observes at least three ordered `LIMIT 2` proof queries. Each proof batch runs in its own transaction, re-locks and validates the exact point/lifecycle attempt, classifies missing/unknown/mismatched Content authority closed, validates exact attempt/fence hashes, and leaves every RecoveryResult row unchanged. A second owner converges after partial progress and a third invocation is idempotent.
- Content adjacent lifecycle/cache/privacy selectors, the exact regression at `-count=10` and `-race -count=3`, full Content, package vet, and focused lint passed; lint reported `0 issues` and format/diff checks were clean.

## Task 6 Content bounded-cardinality closure: root authoritative verification — GREEN

- UTC evidence timestamp: `2026-08-18T03:00:01Z`.
- The required nine-package Task 6 selector passed at `-count=1`, `-race -count=1`, and split repeat coverage: the changed Content regression passed at `-count=10`, the other owner packages passed their repeat gates, and the immediately preceding frozen snapshot's unchanged Recovery repeat gate remains `51.929s`. Unchanged Child 13 RecoveryResult/workspace selectors passed (`1.632s`/`0.079s`).
- Fresh uncached `go test -p 1 ./... -count=1` passed, including Content `1.332s`, Recovery `39.683s`, Runtime `7.137s`, Repository `5.690s`, and Database `25.309s`. `go build ./...`, `go vet ./...`, and `golangci-lint run ./...` passed; lint reported `0 issues`.
- A fresh disposable PostgreSQL 17 Content package gate passed (`2.782s`) with `server_version_num=170011`. The fixture used a loopback random port, `--rm`, no bind or volume, and tmpfs only (`Mounts=[]`, `Binds=null`); final matching container count was `0` and credential/DSN environment was absent.
- Working/cached diff, changed-file gofmt, and whitespace checks passed; staged count is `0`; `backup_assets.enabled` remains code-default false; protected `.codex/agents/trellis-research.toml` remains unchanged. Task 6 stays in progress pending a new independent spec and quality review of this exact post-fix snapshot; Task 7 is not started.

## Task 6 preserved-RecoveryResult point binding and Overlay false-zero re-review — CHANGES REQUIRED

- Evidence recorded at `2026-08-18T03:21:26Z` for the fresh frozen-snapshot reviews completed before the two repair REDs.
- The independent spec review found one Important Content authority defect: the bounded preserved-RecoveryResult proof checked grant/lease identity, attempt, and fence but did not join the authoritative RecoveryResult job and plan back to the current source RecoveryPoint. A point-B result grant could therefore be attached to a point-A active `content_session` lease and be incorrectly preserved.
- The independent quality review found one Important Overlay concurrency defect: ordinary invalid-source reconciliation and the lifecycle owner could select the same first saved-search/favorite/tag batch; if the ordinary owner committed first, the lifecycle conditional update returned `RowsAffected=0`. With later rows still active, the runtime aggregate could mistake the all-zero result for completion.
- No Critical or Minor finding was reported. The new Content broker/proof cardinality bounds, Processing higher-level source authority, six-owner ordering, Child 13 isolation, default false, pure uninstalled runtime aggregate, and forbidden-scope boundaries remained conformant.

## Task 6 preserved-RecoveryResult point binding and Overlay false-zero closure — chronological RED to GREEN

- UTC `2026-08-18T03:10:48Z`: `go test ./internal/backupasset/overlay -run '^TestLifecycleOverlayRejectsLostConditionalBatchBeforeFalseZero$' -count=1` failed genuinely. A deterministic competing update consumed the selected first saved-search batch; lifecycle returned an all-zero result with `err=nil` while a later active batch remained.
- UTC `2026-08-18T03:11:19Z`: the identical Overlay selector passed. Saved-search, favorite, and tag-assignment conditional updates now require exact `RowsAffected == len(selectedIDs)`; any lost row returns closed `backupasset.ErrConflict` and the transaction rolls back instead of emitting a false zero.
- UTC `2026-08-18T03:14:06Z`: `go test ./internal/backupasset/content -run '^TestRecoveryPointSourceLifecycleContentRejectsRecoveryResultFromDifferentSourcePoint$' -count=1` failed genuinely. A structurally complete point-B result/job/plan grant attached to a point-A active Content lease passed the existing proof with `err=nil`.
- UTC `2026-08-18T03:14:50Z`: the identical Content selector passed. The bounded proof now resolves the exact grant result, job, and plan and requires the plan RecoveryPoint to equal the lifecycle point while retaining exact grant/lease/attempt/fence checks. Missing or mismatched authority fails closed without mutating grant, request, lease, or Child 13 cleanup state.
- Content proof first keyset-selects only the active lease IDs for one configured batch, so owners with no active Content sessions do not load unrelated Recovery tables. The exact joined proof remains ordered, `LIMIT`-bounded, attempt-revalidated, restartable, and preserves valid RecoveryResult rows.

## Task 6 preserved-RecoveryResult point binding and Overlay false-zero closure: root authoritative verification — GREEN

- UTC evidence timestamp: `2026-08-18T03:21:26Z`.
- Content and Overlay exact/adjacent selectors passed; the changed Content pair passed at `-count=10` and `-race -count=3`; the Overlay regression passed at `-count=20` and `-race -count=10`; full Content and Overlay normal/race packages passed; focused vet and lint reported `0 issues`.
- The required nine-package Task 6 selector passed at `-count=1` and `-race -count=1`; unchanged Child 13 RecoveryResult/workspace selectors passed (`1.587s`/`0.080s`).
- Fresh uncached serial `go test -p 1 ./... -count=1` passed, including Content `1.350s`, Overlay `0.359s`, Recovery `39.346s`, Runtime `7.153s`, Repository `5.715s`, and Database `24.969s`. `go build ./...`, `go vet ./...`, and `golangci-lint run ./...` passed; lint reported `0 issues`.
- A fresh disposable PostgreSQL 17 full Content plus Overlay package gate passed (`2.850s`/`1.158s`) with `server_version_num=170011`. The fixture used a loopback random port, `--rm`, no bind or volume, and tmpfs only (`Mounts=[]`, `Binds=null`); final matching container count was `0` and credential/DSN environment was absent.
- Working/cached diff, changed-file gofmt, and whitespace checks passed; staged count is `0`; `backup_assets.enabled` remains code-default false; protected `.codex/agents/trellis-research.toml` SHA-256 remains `4435ce73197ba1d29d40359a3279b6423f7e4f559a449f934c016808090066c4`. Task 6 remains in progress pending a new independent spec and quality review of this exact post-fix snapshot; Task 7 is not started.

## Task 6 shared-blob publication, cache cardinality, and zero-grant marker re-review — CHANGES REQUIRED

- Evidence recorded at `2026-08-18T03:49:49Z` for the continued independent quality review completed before the three repair REDs.
- The review found one Important Processing concurrency defect: source cleanup counted active shared-blob references before locking the exact blob, while normal publication locked the blob before inserting its reference. A concurrent publisher could therefore commit an active reference after the stale count and still lose its ciphertext/key to cleanup.
- The review found one Important Content boundedness defect: `EvictRecoveryPoint` held the global cache mutex while scanning every writer and entry for a point, collected and sorted every matching key, and only then truncated to `batchSize`. Valid zero-byte memory objects consume no byte/file quota, so point cardinality and per-pass scan/allocation were unbounded.
- The review found one Important Content marker-lifecycle defect: even a point with no delivery grant acquired a draining issue marker in `waitForAssetIssues`, but only a non-empty grant batch entered `drainRecoveryPoint`, the existing marker reclamation path. Successful zero-grant owners therefore retained one marker per point indefinitely.
- No Critical or Minor finding was reported. The preceding Overlay exact-`RowsAffected` and Content authoritative RecoveryResult point proof remained conformant.

## Task 6 shared-blob publication, cache cardinality, and zero-grant marker closure — chronological RED to GREEN

- UTC `2026-08-18T03:29:38Z`: `go test ./internal/backupasset/processing -run '^TestRecoveryPointSourceLifecycleProcessingLocksSharedBlobBeforeReferenceProof$' -count=1` failed genuinely. A deterministic callback published another point's active reference at the blob-query boundary; the target reference was revoked, the other reference stayed active, but the shared blob became unavailable with `ref_count=0` and erased key authority.
- UTC `2026-08-18T03:30:31Z`: the identical Processing selector passed. Cleanup now locks the exact blob before counting active references, matching the normal publisher's blob-before-reference order; a nonzero count is re-proven into the exact `ref_count`, and only an exact zero can enter purge. The regression retains the publisher's active reference, available blob/key, and ciphertext.
- UTC `2026-08-18T03:33:59Z`: `go test ./internal/backupasset/content -run '^TestCacheEvictRecoveryPointZeroByteCardinalityUsesBatchBoundedSelection$' -count=1` failed genuinely. With the same `batchSize=1`, selection allocations grew from `5` for sixteen zero-byte objects to `15` for 4,096 objects, proving full-cardinality collection before truncation.
- UTC `2026-08-18T03:34:53Z`: the identical cache selector passed. The cache now maintains exact per-point entry, writer, and active-lease indexes under the existing mutex. Busy proof is constant-time, and eviction selects and allocates at most `batchSize` entries without scanning or sorting unrelated/global cardinality; all index transitions share materialization, open/close, eviction, reconcile, and shutdown removal paths.
- UTC `2026-08-18T03:41:24Z`: `go test ./internal/backupasset/content -run '^TestContentLifecycleBrokerZeroGrantMarkersAreBounded$' -count=1` failed genuinely with `32` retained issue markers after thirty-two successful zero-grant lifecycle owners.
- UTC `2026-08-18T03:41:43Z`: the identical marker selector passed. After the bounded durable grant and preserved-lease zero proofs, the owner now removes only the exact marker when it is draining with `active=0`; missing, non-draining, or still-active states remain fail closed. The existing canceled in-progress Issue regression also now asserts marker reclamation after its zero-grant exit.
- Processing shared-blob and deduplication selectors passed at `-count=10` and `-race -count=3`. Content cache/marker/source selectors passed at `-count=10` and `-race -count=3`; full Content and Processing normal/race packages passed. The nine-package Task 6 selector passed normal and race gates.

## Task 6 shared-blob publication, cache cardinality, and zero-grant marker closure: root authoritative verification — GREEN

- UTC evidence timestamp: `2026-08-18T03:49:49Z`.
- Fresh uncached serial `go test -p 1 ./... -count=1` passed after the final marker change, including Content `1.385s`, Processing `3.048s`, Export `15.621s`, Recovery `39.668s`, Repository `5.710s`, Runtime `7.089s`, Search `0.933s`, and Database `25.090s`. `go build ./...`, `go vet ./...`, and `golangci-lint run ./...` passed; lint reported `0 issues`.
- A fresh disposable PostgreSQL 17 target matrix passed with `server_version_num=170011`: Content behavior, Processing behavior/archive/lock-order/non-current authority, the new shared-blob lock regression, deduplicated cleanup, cache cardinality, and zero-grant marker selectors. Content passed in `1.505s` and Processing in `15.977s`.
- The PostgreSQL fixture used a random loopback port, `--rm`, no bind or host volume, and tmpfs-only data; the exact matching container count after cleanup was `0`, and the random credential/DSN remained shell-local and was unset.
- Working/cached diff, changed-file gofmt, whitespace, staged, default-false, protected-config, and forbidden-scope checks must be re-frozen after this evidence append. Task 6 remains `in_progress` pending new independent spec and quality approval of that exact final snapshot; Task 7 and Task 8 production installation remain unstarted.

## Task 6 final frozen-snapshot independent reviews — SPEC AND QUALITY APPROVED

- Review snapshot: 139 manifested Go paths plus the complete Task directory, SHA-256 aggregate `4b96050f4dd1a367b8b483272b98c42284f01f181469b13a83397d2cb899369e`; both reviewers independently reproduced the same hash, branch `codex/backup-assets-lifecycle-reconnect`, and staged path count `0`. The spec reviewer also reproduced the hash unchanged at review end.
- Independent SPEC verdict: **APPROVED — zero Critical, Important, or Minor findings.** The review rechecked Content zero-grant marker reclamation and cache point indexes, Processing blob-before-reference proof, Overlay false-zero rejection, authoritative RecoveryResult point binding, nine-package Task 6 behavior, unchanged Child 13 selectors, and all forbidden/default/runtime-installation boundaries.
- Independent QUALITY verdict: **APPROVED — zero Critical, Important, or Minor findings.** The review additionally rechecked Content/Processing focused count/race/package gates, Catalog/Search same-underlying-database identity, all five privacy analyzers, Go 1.26 reordered-format behavior, gofmt/diff cleanliness, default false, and protected configuration integrity.
- Root's final post-freeze nine-package Task 6 race selector passed; unchanged Child 13 RecoveryResult/workspace selectors passed (`1.596s`/`0.080s`). No code or Git state changed after the frozen reviews began.
- Task 6 is now `complete_checked`. Task 7 is not started; Task 8 production installation, Child 15, `000071`, deploy/GA, dependency updates, staging, commit, and push remain unauthorized and untouched.

## Task 7 — Add exact Provider point deletion

### RED

- UTC timestamp: `2026-08-18T12:50:14Z`
- Command: `cd backend && go test ./internal/backupasset/provider -run '^(TestPointDeleter|TestResticExactPointDeletion|TestRsyncExactPointDeletion|TestRcloneExactPointDeletion)' -count=1`
- Exit status: `1`
- Expected failure category: compile-time contract RED caused only by the just-added PointDeleter / exact-deletion tests referencing the missing deletion port, outcomes, and provider deleters.
- Concise output:

  ```text
  internal/backupasset/provider/deletion_test.go:101:11: undefined: DeletePointRequest
  internal/backupasset/provider/deletion_test.go:102:11: undefined: DeletePointResult
  internal/backupasset/provider/deletion_test.go:121:7: undefined: PointDeleter
  internal/backupasset/provider/rsync_deletion_test.go:119:11: undefined: RsyncPointDeleter
  internal/backupasset/provider/rclone_deletion_test.go:172:62: undefined: RcloneNativeExactVersion
  FAIL xirang/backend/internal/backupasset/provider [build failed]
  ```

### GREEN

- UTC timestamp: `2026-08-18T12:57:58Z`
- Command: `cd backend && go test ./internal/backupasset/provider -run '^(TestPointDeleter|TestResticExactPointDeletion|TestRsyncExactPointDeletion|TestRcloneExactPointDeletion)' -count=1`
- Exit status: `0`
- Result: optional PointDeleter registry capability, exact Restic forget+prune, handle-relative Rsync managed-component delete, exact Rclone prefix and frozen native versions, already-absent idempotency, WORM typed block, identity checks, cancel/join, bounded forget output, and no raw locator leakage all pass.
- Concise output: `ok xirang/backend/internal/backupasset/provider 0.008s`

### Adjacent publication/read-adapter boundary selector

- UTC timestamp: `2026-08-18T12:58:00Z`
- Command: `cd backend && go test ./internal/backupasset/provider -run '^(TestProviderPublicationBoundary|Test.*Adapter.*Mutation|TestPublicationSourceBoundaries)' -count=1`
- Exit status: `0`
- Result: read/publication adapters still reject forget/prune/delete; only the separately registered deletion port reaches exact operations; provider sources still do not import api/task/repository/runtime.
- Concise output: `ok xirang/backend/internal/backupasset/provider 0.017s`

### Adjacent retention lifecycle selector

- UTC timestamp: `2026-08-18T12:58:35Z`
- Command: `cd backend && go test ./internal/backupasset/retention -run '^(TestLifecycle|TestExpiry|TestPurge|TestMutableHeadRetirement|TestLease)' -count=1`
- Exit status: `0`
- Result: expiry/purge reach `expired` only after `deleted` or `already_absent` plus a receipt digest; WORM and missing deletion capability stay blocked without clearing the private locator.
- Concise output: `ok xirang/backend/internal/backupasset/retention 0.365s`

### Touched package verification

- UTC timestamp: `2026-08-18T12:59:00Z`
- Command: `cd backend && go test ./internal/backupasset/provider ./internal/backupasset/retention -count=1`
- Exit status: `0`
- Result: full provider and retention packages pass together after Task 7 deletion wiring.
- Concise output:

  ```text
  ok  	xirang/backend/internal/backupasset/provider	1.052s
  ok  	xirang/backend/internal/backupasset/retention	0.501s
  ```

- `gofmt -l` on the Task 7 created/modified Go files is empty. Task 8 runtime installation, Child 15, `000071`, deploy/GA, lockfile, `.github`, `backend/cmd/server/main.go`, and `backup_assets.enabled` default were not touched.

## Task 7 review fix — Rclone joined exit codes, typed missing deleter, prefix escape

### RED

- UTC timestamp: `2026-08-18T13:12:18Z`
- Command: `cd backend && go test ./internal/backupasset/provider -run '^(TestRcloneExactPointDeletionClassifiesPrefixPresenceFromJoinedExitCodes|TestRcloneExactPointDeletionRejectsPrefixPathEscape)' -count=1`
- Exit status: `1`
- Expected failure category: behavioral RED. Prefix presence still classifies `Run` errors as absent/unproven instead of joined exit 0/3, and `/points/{32hex}/../…` locators are accepted and can reach purge.
- Concise output:

  ```text
  run_failed_join_exit_3_already_absent: err=capability unavailable: provider_unavailable, want already_absent
  capability_unavailable_run_join_exit_0_then_3_deleted: Outcome=already_absent, want deleted
  exit_17_is_unknown_not_absent: err=capability unavailable, want presence unknown
  unknown_exit_is_error_not_absent: Outcome=already_absent, want presence unknown
  escaped prefix ".../points/aaa.../../sibling" error=<nil>, want invalid delete request
  ```

- UTC timestamp: `2026-08-18T13:12:18Z`
- Command: `cd backend && go test ./internal/backupasset/retention -run '^TestLifecycleRegistryPointDeletionMissingCapabilityStaysUnproven$' -count=1`
- Exit status: `1`
- Expected failure category: compile-time contract RED caused only by the missing typed `LifecycleBlockedDeletionUnavailable` reason.
- Concise output: `undefined: backupasset.LifecycleBlockedDeletionUnavailable`

- UTC timestamp: `2026-08-18T13:12:18Z`
- Command: `cd backend && go test ./internal/backupasset -run '^TestLifecycleClosedEnumsAndValidators$' -count=1`
- Exit status: `1`
- Expected failure category: compile-time contract RED caused only by the closed blocked-reason enum test referencing the missing typed reason.
- Concise output: `undefined: LifecycleBlockedDeletionUnavailable`

### GREEN

- UTC timestamp: `2026-08-18T13:13:26Z`
- Command: `cd backend && go test ./internal/backupasset/provider -run '^(TestRcloneExactPointDeletionClassifiesPrefixPresenceFromJoinedExitCodes|TestRcloneExactPointDeletionRejectsPrefixPathEscape)' -count=1`
- Exit status: `0`
- Result: prefix presence now classifies joined exit 0/3 only; `Run` failures and capability-unavailable no longer mean absent; `..` remainder/cleaned-path escape is rejected before purge.
- Concise output: `ok xirang/backend/internal/backupasset/provider 0.007s`

- UTC timestamp: `2026-08-18T13:13:26Z`
- Command: `cd backend && go test ./internal/backupasset/retention -run '^TestLifecycleRegistryPointDeletionMissingCapabilityStaysUnproven$' -count=1`
- Exit status: `0`
- Result: missing PointDeleter blocks as typed `deletion_unavailable` and leaves the private locator uncleared.
- Concise output: `ok xirang/backend/internal/backupasset/retention 0.013s`

- UTC timestamp: `2026-08-18T13:13:26Z`
- Command: `cd backend && go test ./internal/backupasset -run '^TestLifecycleClosedEnumsAndValidators$' -count=1`
- Exit status: `0`
- Result: `LifecycleBlockedDeletionUnavailable` is in the closed blocked-reason validators.
- Concise output: `ok xirang/backend/internal/backupasset 0.005s`

### Adjacent and package verification

- UTC timestamp: `2026-08-18T13:13:42Z`
- Commands:

  ```bash
  go test ./internal/backupasset/provider -run '^(TestPointDeleter|TestResticExactPointDeletion|TestRsyncExactPointDeletion|TestRcloneExactPointDeletion)' -count=1
  go test ./internal/backupasset/provider -run '^(TestProviderPublicationBoundary|TestPublicationSourceBoundaries|Test.*Adapter.*Mutation)' -count=1
  go test ./internal/backupasset/retention -run '^(TestExpiryClearsPrivateLocatorOnlyAfterRegistryDeletedOrAlreadyAbsent|TestLifecycle|TestExpiry|TestPurge)' -count=1
  go test ./internal/backupasset/provider ./internal/backupasset/retention ./internal/backupasset -count=1
  ```

- Exit status: `0` for all four.
- Concise output: provider focused `0.009s` / boundary `0.016s`; retention focused `0.365s`; packages `provider 1.108s`, `retention 0.551s`, `backupasset 0.560s`.
- `gofmt -l` on the review-fix Go files is empty. Task 8 Runtime composition was not installed. No commit, stage, or push.

## Task 7 quality review fix — prefix SSH Runtime and full-locator `..`

### RED

- UTC timestamp: `2026-08-18T13:34:48Z`
- Command: `cd backend && go test ./internal/backupasset/provider -run '^(TestRcloneExactPointDeletionCarriesSSHRuntime|TestRcloneExactPointDeletionRejectsMissingRemoteRuntime|TestRcloneExactPointDeletionRejectsPrefixPathEscape)$' -count=1`
- Exit status: `1`
- Expected failure category: compile-time contract RED caused only by the new Runtime tests referencing missing `RclonePrefixDeletionAccess.Command`.
- Concise output: `access.Command undefined (type RclonePrefixDeletionAccess has no field or method Command)`

- UTC timestamp: `2026-08-18T13:34:59Z`
- Command: same selector after adding the `Command` field only
- Exit status: `1`
- Expected failure category: behavioral RED. Prefix invocations still omit Runtime, missing Runtime still reaches delete, and `backup:managed/../other/points/{32hex}` is accepted.
- Concise output:

  ```text
  escaped prefix "backup:managed/../other/points/aaa..." error=<nil>, want invalid delete request
  escaped prefix "backup:../points/aaa..." error=<nil>, want invalid delete request
  stat invocation Runtime=<nil>, want Node.ID=9
  missing Runtime error=<nil>, want invalid delete request
  ```

### GREEN

- UTC timestamp: `2026-08-18T13:35:20Z`
- Command: `cd backend && go test ./internal/backupasset/provider -run '^(TestRcloneExactPointDeletionCarriesSSHRuntime|TestRcloneExactPointDeletionRejectsMissingRemoteRuntime|TestRcloneExactPointDeletionRejectsPrefixPathEscape)$' -count=1`
- Exit status: `0`
- Result: prefix stat/purge now carry `RclonePrefixDeletionAccess.Command` through `invocation.Runtime` and pass `commandSpec` completeness; missing Runtime fail-closes before purge; `..` anywhere in the locator is rejected.
- Concise output: `ok xirang/backend/internal/backupasset/provider 0.006s`

### Adjacent verification

- UTC timestamp: `2026-08-18T13:35:25Z`
- Commands:

  ```bash
  go test ./internal/backupasset/provider -run '^(TestPointDeleter|TestResticExactPointDeletion|TestRsyncExactPointDeletion|TestRcloneExactPointDeletion)' -count=1
  go test ./internal/backupasset/provider -run '^(TestProviderPublicationBoundary|TestPublicationSourceBoundaries)' -count=1
  go test ./internal/backupasset/retention -run '^(TestExpiryClearsPrivateLocatorOnlyAfterRegistryDeletedOrAlreadyAbsent|TestLifecycle|TestExpiry|TestPurge)' -count=1
  ```

- Exit status: `0` for all three (`0.007s`, `0.012s`, `0.354s`).
- `gofmt -l` on the changed Go files is empty. Task 8 was not started. No commit.

## Task 7 quality review fix — native exact version ManagedPrefix

### RED

- UTC timestamp: `2026-08-18T13:45:09Z`
- Command: `cd backend && go test ./internal/backupasset/provider -run '^TestRcloneNativeExactVersionMethodsRejectKeysOutsideManagedPrefix$' -count=1`
- Exit status: `1`
- Expected failure category: behavioral RED. `ProbeExactVersion` accepted a same-bucket key outside `ManagedPrefix` and called HeadObject (`Present:true`).
- Concise output: `outside ProbeExactVersion probe={Present:true Locked:false} err=<nil>, want ErrInvalidState`

### GREEN

- UTC timestamp: `2026-08-18T13:45:26Z`
- Command: `cd backend && go test ./internal/backupasset/provider -run '^TestRcloneNativeExactVersionMethodsRejectKeysOutsideManagedPrefix$' -count=1`
- Exit status: `0`
- Result: same-bucket keys outside `ManagedPrefix` fail closed with no HeadObject/DeleteObject; owned-prefix probe, already-absent, WORM, and delete keep working.
- Concise output: `ok xirang/backend/internal/backupasset/provider 0.005s`

### Adjacent verification

- UTC timestamp: `2026-08-18T13:45:26Z`
- Command: `cd backend && go test ./internal/backupasset/provider -run '^(TestPointDeleter|TestResticExactPointDeletion|TestRsyncExactPointDeletion|TestRcloneExactPointDeletion|TestRcloneNative)' -count=1`
- Exit status: `0`
- Concise output: `ok xirang/backend/internal/backupasset/provider 0.013s`
- `gofmt -l` on the changed Go files is empty. Task 8 was not started. No commit.

## Task 8 — Audit retention, metrics, worker, and runtime composition

### RED

- UTC timestamp: `2026-08-18T13:59:31Z`
- Command: `cd backend && go test ./internal/backupasset/retention ./internal/backupasset/runtime -run '^(TestRetentionWorker|TestAuditRetention|TestRuntime.*Retention)' -count=1`
- Exit status: `1`
- Expected failure category: compile-time contract RED caused only by the missing Task 8 worker/audit/metrics types and Runtime retention composition.
- Concise output:

  ```text
  internal/backupasset/retention/worker_test.go:325:17: undefined: Worker
  internal/backupasset/retention/worker_test.go:521:13: undefined: MetricOutcome
  internal/backupasset/retention/audit_test.go:29:18: undefined: NewAuditRetention
  internal/backupasset/runtime/runtime_test.go:2893:13: runtime.retentionWorker undefined
  internal/backupasset/runtime/runtime_test.go:2896:13: runtime.RetentionPolicies undefined
  FAIL xirang/backend/internal/backupasset/retention [build failed]
  FAIL xirang/backend/internal/backupasset/runtime [build failed]
  ```

### GREEN

- UTC timestamp: `2026-08-18T14:13:41Z`
- Command: `cd backend && go test ./internal/backupasset/retention ./internal/backupasset/runtime -run '^(TestRetentionWorker|TestAuditRetention|TestRuntime.*Retention)' -count=1`
- Exit status: `0`
- Result: retention worker, audit-detail purge, aggregate metrics, and Runtime StartupPass/Run/Shutdown composition are installed. Tombstones are constructed and injected into `NewManagedHistoryResolver` before admission.
- Concise output:

  ```text
  ok  	xirang/backend/internal/backupasset/retention	0.200s
  ok  	xirang/backend/internal/backupasset/runtime	0.108s
  ```

### Adjacent verification

- UTC timestamp: `2026-08-18T14:13:41Z`
- Commands:

  ```bash
  go test ./internal/backupasset/retention ./internal/backupasset/runtime ./internal/backupasset/repository -run 'ManagedHistory|Retention' -count=1
  go test ./internal/backupasset/recovery ./internal/backupasset/runtime -run 'RecoveryResult|RecoveryWorkspaceCleanup' -count=1
  ```

- Exit status: `0` for both (`0.270s`/`0.114s`/`0.198s`, then `1.512s`/`0.077s`).
- `gofmt -l` on the changed Go files is empty. Task 9 was not started. No commit.

### RED (quality review — shutdown order + blocked edge)

- UTC timestamp: `2026-08-18T14:48:01Z`
- Command: `cd backend && go test ./internal/backupasset/retention ./internal/backupasset/runtime -run '^(TestRetentionWorker|TestAuditRetention|TestRuntime.*Retention)' -count=1`
- Exit status: `1`
- Expected failure category: assertion RED only. `TestRetentionWorkerBlockedMetricIsEdgeTriggeredOnNotDueRevisit` revisits a parked blocked attempt and increments `blocked` again. `TestRuntimeRetentionShutdownKeepsOwnersUpUntilWorkerReturns` observes owner Shutdown while an in-flight retention cleanup is still held.
- Concise output:

  ```text
  --- FAIL: TestRetentionWorkerBlockedMetricIsEdgeTriggeredOnNotDueRevisit (0.01s)
      worker_test.go:276: second settle of the same not-due blocked attempt increment blocked=2, want 1
  FAIL xirang/backend/internal/backupasset/retention

  --- FAIL: TestRuntimeRetentionShutdownKeepsOwnersUpUntilWorkerReturns (0.03s)
      runtime_test.go:3073: owner runtimes shut down before the in-flight retention cleanup returned
  FAIL xirang/backend/internal/backupasset/runtime
  ```

### GREEN (quality review — shutdown order + blocked edge)

- UTC timestamp: `2026-08-18T14:48:41Z`
- Command: `cd backend && go test ./internal/backupasset/retention ./internal/backupasset/runtime -run '^(TestRetentionWorker|TestAuditRetention|TestRuntime.*Retention)' -count=1`
- Exit status: `0`
- Result: `Runtime.StopAccepting` stops the retention worker first; `Shutdown` joins it before owner runtimes. `MetricBlocked` is emitted only on the blocked-phase edge. Worker interval wait uses `time.NewTimer` + `Stop()` unless a test injects `After`.
- Concise output:

  ```text
  ok  	xirang/backend/internal/backupasset/retention	0.260s
  ok  	xirang/backend/internal/backupasset/runtime	0.444s
  ```

- `gofmt -l` on the changed Go files is empty. Task 9 was not started. No commit.

## Task 9 — Task HTTP delete becomes archive/unlink

### RED

- UTC timestamp: `2026-08-18T14:58:12Z`
- Command: `cd backend && go test ./internal/task ./internal/api/handlers -run '^(TestTaskArchive|TestTaskDeleteArchivesAndUnlinks|TestTaskDeleteDoesNotRemoveScheduleWhenArchiveFails)' -count=1`
- Exit status: `1`
- Expected failure category: compile-time contract RED in `internal/task` from the missing `ArchiveService` / `ArchiveDependencies` / `ErrTaskArchiveHasDependents` seam, plus behavioral RED in the handler: `DELETE /tasks/:id` still hard-deletes and returns `{"message":"deleted"}` instead of an archive result; an Update-only failure callback does not prevent current `db.Delete`, so the schedule is removed.
- Concise output:

  ```text
  # xirang/backend/internal/task [xirang/backend/internal/task.test]
  internal/task/archive_test.go:24:13: undefined: NewArchiveService
  internal/task/archive_test.go:24:31: undefined: ArchiveDependencies
  internal/task/archive_test.go:140:21: undefined: ErrTaskArchiveHasDependents
  FAIL	xirang/backend/internal/task [build failed]
  --- FAIL: TestTaskDeleteArchivesAndUnlinks
      期望标准归档结果，实际: {Code:200 Data:{Archived:false Unlinked:false ScheduleRemoved:false ProviderBytesDeleted:false}} 原文={"code":200,"message":"deleted","data":null}
  --- FAIL: TestTaskDeleteDoesNotRemoveScheduleWhenArchiveFails
      归档失败期望 500，实际: 200，响应: {"code":200,"message":"deleted","data":null}
  FAIL	xirang/backend/internal/api/handlers
  ```

### GREEN

- UTC timestamp: `2026-08-18T15:00:28Z`
- Command: `cd backend && go test ./internal/task ./internal/api/handlers -run '^(TestTaskArchive|TestTaskDeleteArchivesAndUnlinks|TestTaskDeleteDoesNotRemoveScheduleWhenArchiveFails)' -count=1`
- Exit status: `0`
- Result: `task.ArchiveService` archives/disables, unlinks active `TaskRepositoryLink` while preserving snapshots and the encrypted locator, commits before schedule removal, leaves the schedule on commit failure, stays archived when schedule removal is unproven, rejects live dependents, and never claims or performs Provider byte deletion. HTTP `DELETE /tasks/:id` returns the typed archive envelope through `respondOK`.
- Concise output:

  ```text
  ok  	xirang/backend/internal/task	0.170s
  ok  	xirang/backend/internal/api/handlers	0.063s
  ```

### Adjacent verification

- UTC timestamp: `2026-08-18T15:00:12Z`
- Command: `cd backend && go test ./internal/task ./internal/api/handlers -run '^(TestTaskDelete|TestTaskArchive|TestTaskCreate|TestTaskUpdate)' -count=1`
- Exit status: `0`
- Result: existing Task create/update/delete coverage stays green with HTTP delete now archive/unlink. `gofmt -l` on the Task 9 Go files is empty. Task 10+ was not started. `task.json` was not marked complete_checked. No commit.

## Task 9 complete_checked

Independent spec and quality reviews approved with no Critical/Important findings. Controller independently reran the Task 9 selector to exit 0 before starting Task 10. No commit.

## Task 10 — Lifecycle handlers, routes, RBAC, proofs, and Swagger

### RED

- UTC timestamp: `2026-08-18T15:12:00Z`
- Command: `cd backend && go test ./internal/api/... ./internal/auth ./internal/middleware -run '^(TestBackupRetentionHandler|TestBackupRepositoryLifecycleHandler|TestBackupLifecycleRoutes|Test.*StepUp.*(Purge|HoldRelease)|Test.*RBAC.*Backup)' -count=1`
- Exit status: `1`
- Expected failure category: compile-time/route RED caused by missing Task 10 handler types/routes.
- Concise output:

  ```text
  backup_retention_handler_test.go:32:15: undefined: BackupRetentionPolicyListRequest
  --- FAIL: TestBackupLifecycleRoutes
      router_test.go:355: backup lifecycle route missing: GET /api/v1/backup-retention-policies
  FAIL xirang/backend/internal/api/handlers [build failed]
  ```

### GREEN

- UTC timestamp: `2026-08-18T15:19:45Z`
- Command: `cd backend && go test ./internal/api/... ./internal/auth ./internal/middleware -run '^(TestBackupRetentionHandler|TestBackupRepositoryLifecycleHandler|TestBackupLifecycleRoutes|Test.*StepUp.*(Purge|HoldRelease)|Test.*RBAC.*Backup)' -count=1`
- Exit status: `0`
- Result: every exact lifecycle route is registered; feature-gate-first blocks service calls; strict bodies/cursors reject unknown/private fields; Admin/RBAC/ownership stay fail-closed; 400/404/409/501/503/500 envelopes stay generic; impact/purge revision drift is 409; hold-release and repository-purge proofs are pairwise isolated; audit records actions/counts without reasons/locators/proofs. Swagger was regenerated with pinned `swag@v1.16.6` after handler GREEN. `git diff --check backend/internal/api/docs` is clean.
- Concise output:

  ```text
  ok  	xirang/backend/internal/api	0.065s
  ok  	xirang/backend/internal/api/handlers	0.079s
  ok  	xirang/backend/internal/auth	0.051s
  ok  	xirang/backend/internal/middleware	0.052s [no tests to run]
  ```

### Adjacent verification

- UTC timestamp: `2026-08-18T15:19:20Z`
- Command: `cd backend && go test ./internal/backupasset/retention ./internal/api ./internal/api/handlers -count=1`
- Exit status: `0`
- Result: retention package including HTTP-facing policy impact and purge-plan create/execute compiles and stays green with the new handler/router surface. No commit.

## Task 11 — Config export/import v2

### RED

- UTC timestamp: `2026-08-18T15:35:00Z`
- Command: `cd backend && go test ./internal/api/handlers -run '^TestConfig(ExportV2|ImportV2|ImportV1Compatibility|AssetGraph)' -count=1`
- Exit status: `1`
- Failure category: compile-time RED caused by the just-added contract tests (`config_backup_assets_test.go`) referencing `ConfigHandler.assetProbe` and `configImportedIdentityRefPrefix` before the v2 helper existed. Default export was still `version: "1.0"` and did not emit a document/asset graph.
- Concise output:

  ```text
  config_backup_assets_test.go:198:10: handler.assetProbe undefined
  config_backup_assets_test.go:237:10: handler.assetProbe undefined
  config_backup_assets_test.go:447:14: undefined: configImportedIdentityRefPrefix
  FAIL	xirang/backend/internal/api/handlers [build failed]
  ```

### GREEN

- UTC timestamp: `2026-08-18T15:41:40Z`
- Command: `cd backend && go test ./internal/api/handlers -run '^TestConfig(ExportV2|ImportV2|ImportV1Compatibility|AssetGraph)' -count=1`
- Exit status: `0`
- Result: default export is `2.0` with a 32-hex `document_id`; the asset graph uses stable `repository_ref`/`link_ref`/`policy_ref`/`task_ref`/`hold_ref` and a keyed `identity_ref`; default export omits secrets/locators/reasons/proofs/source IDs; sensitive export adds an encrypted binding envelope only when `include_secrets=true` and audits counts/`with_sensitive` without the envelope; v2 import remaps a shared repository, is idempotent on repeat, rolls back the whole graph on identity conflict or changed mapping, persists disconnected repositories plus revoked stored bindings, and performs no Provider call. Version `1.0` still imports classic nodes only.
- Concise output:

  ```text
  ok  	xirang/backend/internal/api/handlers	0.298s
  ```

### Adjacent verification

- UTC timestamp: `2026-08-18T15:40:50Z`
- Command: `cd backend && go test ./internal/api/handlers -count=1`
- Exit status: `0`
- Result: existing v1 export/import, settings-transition, SSH-key scope, and step-up/grant config tests stay green after treating missing lifecycle tables as an empty v2 graph. No commit.

### Review-fix GREEN

- UTC timestamp: `2026-08-18T15:53:31Z` then `2026-08-18T15:55:58Z`
- Command: `cd backend && go test ./internal/api/handlers -run '^TestConfig(ExportV2|ImportV2|ImportV1Compatibility|AssetGraph)' -count=1` and adjacent `go test ./internal/api/handlers -count=1`
- Exit status: `0`
- Result: policy import now stamps the authenticated actor; imported identity placeholders bind on Connect and rematch after bind; mapping/identity lookups fail closed; changed link/policy mapping and duplicate active-link/scope collisions are 409; rematch can attach a later sensitive binding; export audit records `retention_policy_count`. Independent spec and quality reviews approved with no remaining Critical/Important. No commit.

## Task 12 — Typed frontend lifecycle APIs

### GREEN (initial wrapper slice)

- UTC timestamp: `2026-08-18T16:02:49Z`
- Command: `cd web && npm run test -- --run src/lib/api/backup-repositories-api.test.ts src/lib/api/backup-retention-api.test.ts src/lib/api/client.test.ts src/lib/step-up-storage.test.ts`
- Exit status: `0`
- Result: typed retention/repository lifecycle wrappers, camelCase mapping, isolated `X-Xirang-Step-Up` forwarding, and existing step-up storage isolation pass. `tsc -b --noEmit` also passed.

### Review-fix RED

- UTC timestamp: `2026-08-18T16:10:24Z`
- Command: same Task 12 selector
- Exit status: `1`
- Failure category: behavioral RED from independent spec/quality review — connect mapper invented catalog/lineages/`accessActive`; policy/hold/impact/purge products were fail-open; illegal hold create and blank proofs still POSTed.
- Concise output:

  ```text
  maps connect/import/rebuild results: expected snapshot fields, received nested available BackupRepository
  fails closed on incomplete policy, impact, hold, and purge products: version 2 mapped available
  does not POST illegal hold create or blank step-up proofs: request() called twice
  Tests  3 failed | 36 passed
  ```

### Review-fix GREEN

- UTC timestamp: `2026-08-18T16:11:26Z`
- Command: same Task 12 selector
- Exit status: `0`
- Result: mutation results are DTO-only snapshots; policy rules require version 1 plus selectors and bounds; impact/purge counts must match unique items; hold products are closed; illegal create and blank proofs return blocked without `request()`. Independent spec review: compliant, no remaining Critical/Important. Independent quality review: Approve with residuals (rebuild totals, a few unpinned edges). `tsc -b --noEmit` passed. No commit.

## Task 13 — Repository-management and retention-policy panels

### GREEN (review-fix after independent spec/quality)

- UTC timestamp: `2026-08-18T16:37:14Z`
- Command: `cd web && npm run test -- --run src/features/backup-assets/repository-management-panel.test.tsx src/features/backup-assets/retention-policy-panel.test.tsx src/features/backup-assets/backup-assets-workspace.test.tsx src/features/backup-assets/backup-assets-lifecycle-panels.a11y.test.tsx`
- Exit status: `0`
- Result: 50 tests. Production panels resolve `apiClient` when `api` is omitted; per-row repository actions bind the clicked id; retention CRUD uses `expectedRevision`; purge uses policy `scopeId`; hold uses the selected recovery point; purge/hold/delete are Radix dialogs with typed confirmation and focus return; 409 surfaces as conflict; workspace `repositories.status === "error"` is an alert, not an empty panel; missing methods fail closed; purge `blocked > 0` is partial, not success. `tsc -b --noEmit` passed. Independent spec: compliant. Independent quality: Approve with residuals (no remaining Critical/Important). Controller independently re-ran the same selector. No commit.

## Task 14 — Disaster-recovery behavior and documentation

### RED

- UTC timestamp: `2026-08-18T16:42:00Z`
- Command: `cd backend && go test ./internal/backupasset/retention ./internal/backupasset/repository -run '^TestDisasterRecovery' -count=1`
- Exit status: `1`
- Failure category: compile-time RED — `TestDisasterRecovery*` referenced `ClassifyDisasterRecoveryFact` before the closed fact taxonomy existed.
- Concise output:

  ```text
  undefined: backupasset.ClassifyDisasterRecoveryFact
  undefined: backupasset.DisasterRecoveryControlPlane
  FAIL	xirang/backend/internal/backupasset/retention [build failed]
  FAIL	xirang/backend/internal/backupasset/repository [build failed]
  ```

### GREEN

- UTC timestamp: `2026-08-18T16:48:00Z`
- Command: same Task 14 selector
- Exit status: `0`
- Result: Provider facts rebuild only after Connect/import/review; rebuild requires a decryptable active binding; wrong/missing `DATA_ENCRYPTION_KEY` fails Reconcile, Connect, and rebuild without replacing the binding; overlays/audit/policies/holds stay empty; docs record the matrix. Independent spec: compliant. Independent quality: Approve. Controller independently re-ran the same selector. No commit.

  ```text
  ok  	xirang/backend/internal/backupasset/retention	0.069s
  ok  	xirang/backend/internal/backupasset/repository	0.117s
  ```

## Task 15 — Cross-engine, fault, race, privacy, and full gates

### Review-fix RED then GREEN — retention worker batch/config race

- UTC timestamp: `2026-08-18T16:55:00Z` (RED) / `2026-08-18T16:58:00Z` (GREEN)
- Command: `cd backend && go test ./internal/backupasset/retention -run '^TestRetentionWorkerPeriodicBatchesHonorDynamicConfig$' -count=1`
- Result: `waitFor` returned when claims hit 3 and Shutdown canceled the pass before import/rebuild honored `batch=2`. GREEN waits for `countAttempts==3` and mutex-safe `limits()==(2,2)` before cancel.

### Required PostgreSQL (disposable `postgres:17-alpine`, then destroyed)

- UTC timestamp: `2026-08-18T17:05:00Z`
- Command: `REQUIRE_POSTGRES_MIGRATION_TEST=1 TEST_POSTGRES_DSN=<disposable loopback DSN, never recorded> go test ./internal/database ./internal/backupasset/retention -run 'BackupAssetMigration070|RetentionBehaviorPostgres' -count=1`
- Exit status: `0`
- Result: `TestRetentionBehaviorPostgres` fail-closes when the required flag is set and the DSN is missing; the disposable fixture applied `000070` and ran the shared SQLite/PG behavior runners. Container, tmpfs, and `/tmp/xirang-task15-pg.*` were removed. Later sandboxed `go test` runs must not inherit `TEST_POSTGRES_DSN`.

### Controller re-run after Task 16 product fixes — focused

- UTC timestamp: `2026-08-18T17:25:08Z`
- Command: `cd backend && go test ./internal/backupasset/retention ./internal/backupasset/repository ./internal/backupasset/provider ./internal/task ./internal/api/... -run 'Retention|RetentionPolicy|RecoveryPointHold|Reconnect|Import|Rebuild|Purge|TaskArchive|AuditRetention|DisasterRecovery' -count=1`
- Exit status: `0`
- Concise output:

  ```text
  ok  	xirang/backend/internal/backupasset/retention	0.361s
  ok  	xirang/backend/internal/backupasset/repository	0.860s
  ok  	xirang/backend/internal/backupasset/provider	0.015s
  ok  	xirang/backend/internal/task	0.303s
  ok  	xirang/backend/internal/api	0.070s
  ok  	xirang/backend/internal/api/handlers	0.522s
  ```

### Controller re-run after Task 16 product fixes — race

- UTC timestamp: `2026-08-18T17:25:19Z`
- Command: `cd backend && go test -race ./internal/backupasset/retention ./internal/backupasset/repository ./internal/backupasset/provider ./internal/task -run 'Retention|Reconnect|Import|Purge|TaskArchive' -count=1`
- Exit status: `0`
- Concise output:

  ```text
  ok  	xirang/backend/internal/backupasset/retention	1.612s
  ok  	xirang/backend/internal/backupasset/repository	3.525s
  ok  	xirang/backend/internal/backupasset/provider	1.047s
  ok  	xirang/backend/internal/task	1.711s
  ```

### Privacy / source review

- `rg -n 'RemoveAll|--min-age|forget|--prune' backend/internal/task/retention.go` still matches the pristine-legacy compatibility seam. Those commands run only after `beginRetentionAuthority` returns `legacy=true`. The managed path delegates exact RecoveryPoint IDs and returns.
- `rg -n 'provider_locator|rollback_locator|encrypted_config|fence_token|step_up|grant|ticket|private_key|password' backend/internal/api/handlers/backup_{repository,retention}_handler.go web/src/features/backup-assets/{repository-management-panel,retention-policy-panel}.tsx` → no matches.
- `backup_assets.enabled` CodeDefault remains `"false"`.
- `git diff --name-only origin/main...HEAD` is empty because Child 14 is still uncommitted. Working-tree inspection shows no `.codex/**`, `deploy/`, lockfile, `000071`, or `backend/cmd/server/main.go` edits.

### Full gates

- UTC timestamp: `2026-08-18T17:27:32Z`
- Command: `cd backend && go test ./... && go build ./...`
- Exit status: `0`
- Result: includes `backupasset/retention 0.839s` and `backupasset/runtime 7.844s`.

- UTC timestamp: `2026-08-18T17:27:32Z`
- Command: `cd web && npm run check`
- Exit status: `0`
- Result: typecheck + lint (0 errors, 2 existing warnings) + `Test Files 177 passed` / `Tests 1466 passed` + production build.

- UTC timestamp: `2026-08-18T17:29:03Z`
- Command: `git diff --check`
- Exit status: `0`

- UTC timestamp: `2026-08-18T17:29:20Z`
- Command: `PATH="$HOME/go/bin:/usr/local/go/bin:$PATH" make check`
- Exit status: `0`
- Result: golangci-lint `0 issues`; frontend lint warning-only; backend tests and build green.

- UTC timestamp: `2026-08-18T17:30:43Z`
- Command: `bash scripts/check-doc-freshness.sh`
- Exit status: `0`
- Result: warning only — `backend/internal/api/router.go` vs `backend/README_backend.md`. No invented README/`000071` change. `npm audit` had 0 high vulnerabilities; lockfiles were not changed.

## Task 16 — Independent high-risk review and amendment loop

Findings and dispositions are in `research/review.md`. Critical/Important product fixes:

### C1 GREEN — revoke failure resumes at Revoking

- UTC timestamp: `2026-08-18T17:18:00Z`
- Command: `cd backend && go test ./internal/backupasset/retention -run '^TestLifecycleRevokeFailureResumesAtRevokingNotCleaning$' -count=1`
- Exit status: `0`
- Result: `owner_cleanup_unproven` resumes at `LifecyclePhaseRevoking`. The closed reason cannot be split without `000071`.

### C2 / I3 GREEN — owner fallbacks, startup order, readiness install

- UTC timestamp: `2026-08-18T17:22:47Z` then `2026-08-18T17:24:00Z`
- Commands:
  - `cd backend && go test ./internal/backupasset/runtime -run 'OwnerFallback|InterruptedRunReadiness|StartupManaged' -count=1`
  - `cd backend && go test ./internal/backupasset/runtime ./internal/backupasset/retention ./internal/api/handlers ./internal/task -count=1`
- Exit status: `0`
- Result: missing owner schema is `ErrConflict`; Processing ignores terminal `is_current=false`; Export uses items; Recovery joins plans; readiness installs the managed Task facade; full packages stay green (`runtime 7.444s`, `retention 0.708s`, `handlers 4.816s`, `task 3.009s`).

### I5 GREEN — config v2 refuses unrestorable holds

- UTC timestamp: `2026-08-18T17:21:00Z`
- Command: `cd backend && go test ./internal/api/handlers -run 'ConfigExportV2|ConfigImportV2' -count=1`
- Exit status: `0`
- Result: default export `hold_count` is 0; a non-empty hold list is HTTP 400; current empty-hold exports still import.

### I4 residual / I6 amendment

- I4 (`PurgeService.Execute` bind-then-claim) is recorded in `research/review.md` and is not silently commented away.
- I6 amended `implement.md` §2: disaster-recovery files, `backup_asset_rbac_test.go`, no `retention_worker_test.go`, no `main.go`.

Independent spec and quality reviews of this Task 16 delta follow. No commit.

## Task 16 — Alan P1/P2 blockers

`TEST_POSTGRES_DSN` was unset for every ordinary `go test` below. Task 17 was
not started. Nothing was committed.

### P1 — Rsync/Rclone production delete reconstruction + rclone mux

#### RED

- UTC timestamp: `2026-08-19T02:35:00Z`
- Command: `cd backend && go test ./internal/backupasset/repository -run '^(TestResolveLifecycleDeletePointReconstructsRsyncDeletionAccess|TestResolveLifecycleDeletePointReconstructsRclonePrefixDeletionAccess|TestResolveLifecycleDeletePointReconstructsRcloneNativeDeletionAccess)$' -count=1`
- Exit status: `1`
- Expected failure category: behavioral RED — `ResolveLifecycleDeletePoint` copied generic runtime access, so managed Rsync/Rclone reconstruction failed closed as `deletion_unavailable`.
- Concise output:

  ```text
  ResolveLifecycleDeletePoint rsync: backup asset capability unavailable: deletion_unavailable
  ResolveLifecycleDeletePoint rclone prefix: backup asset capability unavailable: deletion_unavailable
  ResolveLifecycleDeletePoint rclone native: backup asset capability unavailable: deletion_unavailable
  ```

- UTC timestamp: `2026-08-19T02:35:10Z`
- Command: `cd backend && go test ./internal/backupasset/runtime -run '^TestRuntimeRegistersRcloneDeletionMuxForNativeAccess$' -count=1`
- Exit status: `1`
- Expected failure category: behavioral RED — runtime registered only the prefix deleter for `ProviderRclone`.
- Concise output:

  ```text
  runtime rclone native DeletePoint: invalid exact delete point request: invalid Rclone prefix deletion access
  ```

#### GREEN

- UTC timestamp: `2026-08-19T02:37:20Z`
- Commands:
  - `cd backend && go test ./internal/backupasset/repository -run '^(TestResolveLifecycleDeletePointReconstructsRsyncDeletionAccess|TestResolveLifecycleDeletePointReconstructsRclonePrefixDeletionAccess|TestResolveLifecycleDeletePointReconstructsRcloneNativeDeletionAccess)$' -count=1`
  - `cd backend && go test ./internal/backupasset/provider -run '^(TestRclonePointDeleterRoutesNativeAccessToExactVersionClient|TestRclonePointDeleterRoutesPrefixAccessToPrefixDeleter|TestValidCommittedRclonePointPrefixAcceptsPublicationAttemptRoot)$' -count=1`
  - `cd backend && go test ./internal/backupasset/runtime -run '^TestRuntimeRegistersRcloneDeletionMuxForNativeAccess$' -count=1`
- Exit status: `0`
- Result: Resolve now builds `RsyncPointDeletionAccess`, `RclonePrefixDeletionAccess`, and `RcloneNativeDeletionAccess` with a request-scoped exact-version client. Runtime registers `RclonePointDeleter` and routes native deletes to that client.
- Concise output: `ok repository 0.169s` / `ok provider 0.004s` / `ok runtime 0.064s`

### P1 — Archived Task Resume

#### RED

- UTC timestamp: `2026-08-19T01:58:00Z`
- Command: `cd backend && go test ./internal/task -run '^TestTaskArchiveSkipsScheduleReloadForArchivedTask$' -count=1`
- Exit status: `1`
- Expected failure category: behavioral RED — `Resume` ignored `ArchivedAt` and re-enabled the task.
- Concise output: `Resume archived task error=<nil>, want ErrTaskArchived`

- UTC timestamp: `2026-08-19T02:00:00Z`
- Command: `cd backend && go test ./internal/api/handlers -run '^TestTaskResumeRejectsArchivedTask$' -count=1`
- Exit status: `1`
- Expected failure category: behavioral RED — HTTP mapped the archive conflict as 400 before the 409 sentinel path existed.
- Concise output: `archived resume status=400 body={"code":400,"message":"task archived"}, want 409`

#### GREEN

- UTC timestamp: `2026-08-19T02:38:40Z`
- Commands:
  - `cd backend && go test ./internal/task -run '^TestTaskArchiveSkipsScheduleReloadForArchivedTask$' -count=1`
  - `cd backend && go test ./internal/api/handlers -run '^TestTaskResumeRejectsArchivedTask$' -count=1`
- Exit status: `0`
- Result: `Manager.Resume` returns `ErrTaskArchived` without enable/`SyncSchedule`. HTTP maps that sentinel to 409 and leaves the row disabled and archived.
- Concise output: `ok task 0.012s` / `ok handlers 0.058s`

### P1 — Purge impact revision is client-reported

#### RED

- UTC timestamp: `2026-08-19T02:05:00Z`
- Command: `cd backend && go test ./internal/backupasset/retention -run '^(TestPurgeCreatePlanRejectsStaleImpactRevisionAndDeselectedPoints|TestPurgeExecuteRejectsStaleRepositoryPolicyRevision)$' -count=1`
- Exit status: `1`
- Expected failure category: behavioral RED — CreatePlan stored the client revision; Execute claimed after a policy bump.
- Concise output: stale CreatePlan succeeded (`ImpactRevision: 1`); Execute after policy bump claimed 1.

#### GREEN

- UTC timestamp: `2026-08-19T02:38:50Z`
- Command: `cd backend && go test ./internal/backupasset/retention -run '^(TestPurgeCreatePlanRejectsStaleImpactRevisionAndDeselectedPoints|TestPurgeExecuteRejectsStaleRepositoryPolicyRevision)$' -count=1`
- Exit status: `0`
- Result: CreatePlan loads the active repository-scoped policy, requires the current revision, re-selects, and persists the server revision. Execute re-checks that revision. Stale or deselected items fail closed.
- Concise output: `ok retention 0.057s`

### P1 — Content owner fail-open

#### RED

- UTC timestamp: `2026-08-19T01:55:00Z`
- Command: `cd backend && go test ./internal/backupasset/runtime -run '^TestRuntimeOwnerFallbackProofsFailClosedWithoutSchema$' -count=1`
- Exit status: `1`
- Expected failure category: behavioral RED — missing Content schema returned nil.
- Concise output: `missing Content schema error=<nil>, want ErrConflict`

#### GREEN

- UTC timestamp: `2026-08-19T02:38:55Z`
- Command: `cd backend && go test ./internal/backupasset/runtime -run '^TestRuntimeOwnerFallbackProofsFailClosedWithoutSchema$' -count=1`
- Exit status: `0`
- Result: missing db or `BackupAssetDeliveryGrant` table is `ErrConflict` (“Content source lifecycle is unproven”).
- Concise output: `ok runtime 0.056s`

### P1 — Frontend mixed policy edit drops count/calendar

#### RED

- UTC timestamp: `2026-08-19T02:08:00Z`
- Command: `cd web && npm run test -- --run src/features/backup-assets/retention-policy-panel.test.tsx`
- Exit status: `1`
- Expected failure category: behavioral RED — update sent age-only and dropped `count`/`calendar`.
- Concise output: update payload omitted existing count/calendar rules.

#### GREEN

- UTC timestamp: `2026-08-19T02:39:31Z`
- Command: `cd web && npm run test -- --run src/features/backup-assets/retention-policy-panel.test.tsx src/lib/api/backup-retention-api.test.ts`
- Exit status: `0`
- Result: mixed-policy update spreads current `rules` and only replaces `age.keepDays`. Create stays age-only. Hold list API and remount release path pass.
- Concise output: `Test Files 2 passed (2)` / `Tests 16 passed (16)`

### P2 — Hold UI does not load persisted holds

#### RED

- UTC timestamp: `2026-08-19T02:10:00Z`
- Command: `cd backend && go test ./internal/backupasset/retention -run '^TestHoldListReturnsActiveHoldsWithoutEncryptedReasons$' -count=1`
- Exit status: `1`
- Expected failure category: behavioral RED — missing point list returned empty instead of `ErrNotFound`.
- Concise output: `missing point list error=<nil>, want ErrNotFound`

#### GREEN

- UTC timestamp: `2026-08-19T02:38:50Z` then `2026-08-19T02:40:10Z`
- Commands:
  - `cd backend && go test ./internal/backupasset/retention -run '^TestHoldListReturnsActiveHoldsWithoutEncryptedReasons$' -count=1`
  - `PATH="$HOME/go/bin:$PATH" make swag-init`
  - `cd backend && go test ./internal/api/handlers -run '^TestBackupRetentionHandlerDocumentsExactRoutes$' -count=1`
  - `cd backend && go test ./internal/api -run '^TestBackupLifecycleRoutes$' -count=1`
- Exit status: `0`
- Result: `HoldService.List` returns active holds without encrypted reasons. GET `/recovery-points/{id}/holds` uses `respondOK`, the same manage RBAC as create, and generated Swagger now includes GET.
- Concise output: `ok retention 0.057s` / `ok handlers 0.065s` / `ok api 0.060s`

### P2 — Import/Rebuild reconcile only counts

#### RED

- UTC timestamp: `2026-08-19T02:12:00Z`
- Command: `cd backend && go test ./internal/backupasset/repository -run '^(TestReconcileImportsDropsStalePendingCandidates|TestReconcileRebuildsStartsCatalogForAcceptedMissingCatalog)$' -count=1`
- Exit status: `1`
- Expected failure category: behavioral RED — reconcile returned listed counts without repairing or starting rebuild work.

#### GREEN

- UTC timestamp: `2026-08-19T02:38:58Z`
- Command: `cd backend && go test ./internal/backupasset/repository -run '^(TestReconcileImportsDropsStalePendingCandidates|TestReconcileRebuildsStartsCatalogForAcceptedMissingCatalog)$' -count=1`
- Exit status: `0`
- Result: `ReconcileImports` drops stale pending rows against current listing evidence. `ReconcileRebuilds` starts catalog + derived backfill for accepted readable bindings and returns started work, not merely listed rows.
- Concise output: `ok repository 0.126s`

### P2 — Purge bind-then-claim non-atomic

#### RED

- UTC timestamp: `2026-08-19T02:06:00Z`
- Command: `cd backend && go test ./internal/backupasset/retention -run '^TestPurgeExecuteIsAtomicAcrossClaimFailures$' -count=1`
- Exit status: `1`
- Expected failure category: behavioral RED — first claim survived a later claim failure.
- Concise output: `claimed attempts=1`

#### GREEN

- UTC timestamp: `2026-08-19T02:38:50Z`
- Command: `cd backend && go test ./internal/backupasset/retention -run '^TestPurgeExecuteIsAtomicAcrossClaimFailures$' -count=1`
- Exit status: `0`
- Result: bind + all `ClaimTx` + consume share one transaction. Mid-list claim failure leaves zero attempts and a non-executing `ready` plan. No `000071`.
- Concise output: `ok retention 0.057s`

### Contract drift — config v2 holds

Amended `prd.md` R9.1, `design.md` §8.2/§10, and `implement.md` Task 11 to the already-implemented fail-closed contract: export emits empty `recovery_point_holds`; import rejects any non-empty hold list; holds are DB + key DR facts.

### Adjacent touched packages

- UTC timestamp: `2026-08-19T02:41:00Z`
- Command: `cd backend && go test ./internal/backupasset/provider ./internal/backupasset/retention ./internal/task ./internal/backupasset/repository ./internal/backupasset/runtime ./internal/api/handlers -count=1`
- Exit status: `0`
- Concise output: `ok provider 1.175s` / `ok retention 1.090s` / `ok task 3.348s` / `ok repository 6.760s` / `ok runtime 7.674s` / `ok handlers 5.398s`

## Task 16 — Check-agent verification of Alan blockers

Independent check on `codex/backup-assets-lifecycle-reconnect`. `TEST_POSTGRES_DSN`
was unset. Task 17 was not started. `task.json` was not marked ready. Nothing was
committed. `StartFreshCatalogGeneration` is not idempotent: production
`catalog.Indexer.Build` / `beginGeneration` always allocates `sequence+1`.

### Important — ReconcileRebuilds catalog restart + worker-unsafe nil ports

#### RED

- UTC timestamp: `2026-08-19T02:50:00Z`
- Command: `cd backend && env -u TEST_POSTGRES_DSN go test ./internal/backupasset/repository -run '^(TestReconcileRebuildsStartsCatalogForAcceptedMissingCatalog|TestReconcileRebuildsSkipsCompleteCatalogAndRestartsIncomplete|TestReconcileRebuildsIsWorkerSafeWithoutRebuildPorts)$' -count=1`
- Exit status: `1`
- Expected failure category: behavioral RED — `ReconcileRebuilds` started a fresh
  catalog for every accepted candidate, including complete+active generations, and
  failed the whole retention tick when rebuild ports were nil.
- Concise output:

  ```text
  TestReconcileRebuildsSkipsCompleteCatalogAndRestartsIncomplete
    ReconcileRebuilds started=3, want 1
  TestReconcileRebuildsIsWorkerSafeWithoutRebuildPorts
    err=invalid backup asset state: rebuild dependencies unavailable
  ```

#### GREEN

- UTC timestamp: `2026-08-19T02:55:00Z` then `2026-08-19T03:01:00Z`
- Command: `cd backend && env -u TEST_POSTGRES_DSN go test ./internal/backupasset/repository -run '^(TestReconcileRebuildsStartsCatalogForAcceptedMissingCatalog|TestReconcileRebuildsSkipsCompleteCatalogAndRestartsIncomplete|TestReconcileRebuildsIsWorkerSafeWithoutRebuildPorts)$' -count=1`
- Exit status: `0`
- Result: missing catalog still starts and remains eligible until a complete+active
  generation exists. Complete+active and in-progress `building` generations skip.
  Failed/incomplete generations restart. Nil `CatalogRebuild` / `DerivedBackfill`
  returns `started=0, err=nil` so the retention worker does not fail the tick.
  Admin `RebuildAcceptedImports` still requires both ports.
- Concise output: `ok repository 0.101s` then `ok repository 0.120s`

### Native without lineage — verification GREEN

`ResolveLifecycleDeletePoint` already failed closed. Added a regression selector.

- UTC timestamp: `2026-08-19T03:00:00Z`
- Command: `cd backend && env -u TEST_POSTGRES_DSN go test ./internal/backupasset/repository -run '^TestResolveLifecycleDeletePointNativeWithoutLineageFailsClosed$' -count=1`
- Exit status: `0`
- Result: clearing `producing_task_id` returns `deletion_unavailable`. Production
  `rcloneNativeS3SDK` satisfies `RcloneNativeExactVersionDeleter` via compile-time
  assert in `rclone_native_aws_sdk.go`.

### Focused Alan-blocker re-run

- UTC timestamp: `2026-08-19T03:02:10Z`
- Commands (all `env -u TEST_POSTGRES_DSN`, exit `0`):
  - repository delete + import/rebuild selectors including the new guard and native fail-closed tests: `ok 0.193s`
  - provider mux / prefix validators: `ok 0.005s`
  - runtime mux + content schema fail-closed: `ok 0.074s`
  - archived Resume: `ok task 0.015s`
  - purge impact + hold list + atomic claim: `ok retention 0.033s`
  - HTTP archived Resume + hold routes + config v2: `ok handlers 0.065s` / `ok api 0.059s`
  - `cd web && npm run test -- --run src/features/backup-assets/retention-policy-panel.test.tsx src/lib/api/backup-retention-api.test.ts`: `Test Files 2 passed (2)` / `Tests 16 passed (16)`
- Lint/typecheck: `gofmt` clean on edited Go files; `golangci-lint run ./internal/backupasset/repository/... ./internal/backupasset/provider/...` → `0 issues`; `cd web && npm run typecheck` passed; `npm run lint` exit 0 with 3 pre-existing warnings.

### Residuals left

- Production `repository.NewService` in `runtime.go` still does not inject
  `CatalogRebuild` / `DerivedBackfill`. Admin HTTP rebuild fails closed. Worker
  `ReconcileRebuilds` now no-ops safely; the catalog worker remains the live
  missing-catalog owner.
- Hold list error path audits as `AuditActionHoldCreate` (no `HoldList` action).
  Success list does not audit and does not leak encrypted reasons.
- `ReconcileImports` skips a whole repository on listing error and leaves pending
  rows; next tick retries. Isolation, not silent drop.
- `RclonePrefixDeletionAccess.MarkerDigest` is not `json:"-"`; parent
  `AccessBinding.AdapterData` is.

Verdict: Approve with residuals. Task 17 not started. Nothing committed.

## Task 16 — Independent-check residuals closed (2026-08-19)

Controller kept `current_step=task_16_check_residuals_in_progress`. This close-out does
not flip `complete_checked` and does not start Task 17. `TEST_POSTGRES_DSN` was unset.
`gofmt` was applied to edited Go files. Nothing was committed.

### Residual 1 — Production rebuild ports unwired

#### RED

- UTC timestamp: `2026-08-19T03:10:00Z`
- Command: `cd backend && env -u TEST_POSTGRES_DSN go test ./internal/backupasset/runtime -run '^TestRuntimeSearchExposesOneRepositoryPublicationLineageAndWorkerGraph$' -count=1`
- Exit status: `1`
- Expected failure category: composition RED — `repository.NewService` at runtime
  construction (~752) ran before `catalogIndexer` (~1012) and `processingManager`
  (~1028), so `catalogRebuild` / `derivedBackfill` stayed nil and
  `ReconcileRebuilds` no-oped in production.
- Concise output: `production runtime omitted CatalogRebuild / DerivedBackfill ports`

#### GREEN

- UTC timestamp: `2026-08-19T03:17:00Z`
- Command: `cd backend && env -u TEST_POSTGRES_DSN go test ./internal/backupasset/runtime -run '^(TestRuntimeSearchExposesOneRepositoryPublicationLineageAndWorkerGraph|TestCatalogRebuildAdapterCallsIndexerBuild|TestCatalogRebuildAdapterFailsClosedWhenBuildFails|TestDerivedBackfillAdapterQueuesBackgroundWork|TestDerivedBackfillAdapterTreatsNotDeployedAsSuccess)$' -count=1`
- Exit status: `0`
- Result: `SetRebuildPorts` late-injects adapters after catalog/processing exist and
  before `composeRetentionRuntime`. Catalog adapter calls `catalog.Indexer.Build`
  and returns `CatalogRebuildStart{GenerationID}`. Derived adapter queues
  `PriorityBackground` work through existing processing owners; `ErrNotDeployed`
  is nil. Nil ports still no-op for unit tests that never call `SetRebuildPorts`.
- Concise output: `ok runtime 0.079s`
- Files: `backend/internal/backupasset/runtime/runtime.go`,
  `backend/internal/backupasset/runtime/runtime_test.go`,
  `backend/internal/backupasset/runtime/rebuild_ports.go`,
  `backend/internal/backupasset/runtime/rebuild_ports_test.go`,
  `backend/internal/backupasset/repository/service.go`

### Residual 2 — Hold/policy list audits as create/update

#### RED

- UTC timestamp: `2026-08-19T03:10:00Z`
- Commands:
  - `cd backend && env -u TEST_POSTGRES_DSN go test ./internal/backupasset -run '^TestAuditActionRegistryMatchesDesignContract$' -count=1`
  - `cd backend && env -u TEST_POSTGRES_DSN go test ./internal/api/handlers -run '^TestBackupRetentionHandlerListErrorsDoNotAuditCreateOrUpdate$' -count=1`
- Exit status: `1`
- Expected failure category: closed-set + handler RED — `ListHolds` error path
  used `hold_create`; `ListPolicies` error path used `retention_policy_update`.
- Concise output: want includes `retention_policy_list` / `hold_list`; policy
  list error audited as `retention_policy_update`

#### GREEN

- UTC timestamp: `2026-08-19T03:17:00Z`
- Commands (both exit `0`):
  - `cd backend && env -u TEST_POSTGRES_DSN go test ./internal/backupasset -run '^TestAuditActionRegistryMatchesDesignContract$' -count=1`
  - `cd backend && env -u TEST_POSTGRES_DSN go test ./internal/api/handlers -run '^TestBackupRetentionHandlerListErrorsDoNotAuditCreateOrUpdate$' -count=1`
- Result: `AuditActionHoldList="hold_list"` and
  `AuditActionRetentionPolicyList="retention_policy_list"` are in the closed
  registry. List error paths emit those actions, never create/update. Success
  lists still do not audit. No DB migration.
- Concise output: `ok backupasset 0.008s` / `ok handlers 0.057s`
- Files: `backend/internal/backupasset/audit_action.go`,
  `backend/internal/backupasset/audit_action_test.go`,
  `backend/internal/api/handlers/backup_retention_handler.go`,
  `backend/internal/api/handlers/backup_retention_handler_test.go`

### Residual 3 — ReconcileImports skip-all listing failures succeed

#### RED

- UTC timestamp: `2026-08-19T03:10:00Z`
- Command: `cd backend && env -u TEST_POSTGRES_DSN go test ./internal/backupasset/repository -run '^(TestReconcileImportsFailsClosedWhenEveryListingFails|TestReconcileImportsDoesNotDropPendingOnIdentityMismatch)$' -count=1`
- Exit status: `1`
- Expected failure category: fail-open tick — every listing error set
  `listing.skip=true` and returned `attempted=0, err=nil` while pending rows
  remained. Identity mismatch also succeeded without listing proof.
- Concise output: skip-all listing failures succeeded; identity-mismatch listing
  succeeded

#### GREEN

- UTC timestamp: `2026-08-19T03:17:00Z`
- Command: `cd backend && env -u TEST_POSTGRES_DSN go test ./internal/backupasset/repository -run '^(TestReconcileImportsFailsClosedWhenEveryListingFails|TestReconcileImportsIsolatesListingFailuresAndRepairsSuccessfulRepos|TestReconcileImportsDoesNotDropPendingOnIdentityMismatch|TestReconcileImportsDropsStalePendingCandidates)$' -count=1`
- Exit status: `0`
- Result: per-repo isolation remains. Zero successful listings plus at least one
  listing error fails the tick. Identity mismatch / conflict / invalid state do
  not delete pending rows and still count as listing failures. Mixed
  success+skip does not fail the tick and still repairs the successful repo.
- Concise output: `ok repository 0.203s`
- Files: `backend/internal/backupasset/repository/import.go`,
  `backend/internal/backupasset/repository/import_test.go`

### Residual 4 — Prefix MarkerDigest JSON

#### RED

- UTC timestamp: `2026-08-19T03:10:00Z`
- Command: `cd backend && env -u TEST_POSTGRES_DSN go test ./internal/backupasset/provider -run '^TestRclonePrefixDeletionAccessOmitsMarkerDigest$' -count=1`
- Exit status: `1`
- Expected failure category: secret-field JSON leak —
  `RclonePrefixDeletionAccess.MarkerDigest` marshaled as `MarkerDigest`.
- Concise output: leaked `{"MarkerDigest":"bbbb..."}`

#### GREEN

- UTC timestamp: `2026-08-19T03:17:00Z`
- Command: `cd backend && env -u TEST_POSTGRES_DSN go test ./internal/backupasset/provider -run '^TestRclonePrefixDeletionAccessOmitsMarkerDigest$' -count=1`
- Exit status: `0`
- Result: `MarkerDigest` is `json:"-"` and omitted from direct marshal.
- Concise output: `ok provider 0.008s`
- Files: `backend/internal/backupasset/provider/rclone_deletion.go`,
  `backend/internal/backupasset/provider/rclone_deletion_test.go`

### Residual 5 — Frontend eslint warnings in Child 14 panels

#### RED

- UTC timestamp: `2026-08-19T03:10:00Z`
- Command: `cd web && npx eslint src/features/backup-assets/retention-policy-panel.tsx src/features/backup-assets/export-job-panel.tsx`
- Exit status: `0` with 3 warnings
- Expected failure category: lint/a11y — `resolveApi` missing from two
  `useEffect` deps; `tabIndex={0}` on a non-interactive `<ul>`.
- Concise output: `react-hooks/exhaustive-deps` ×2;
  `jsx-a11y/no-noninteractive-tabindex` on `<ul>`

#### GREEN

- UTC timestamp: `2026-08-19T03:22:02Z`
- Commands (both exit `0`, eslint 0 problems):
  - `cd web && npx eslint src/features/backup-assets/retention-policy-panel.tsx src/features/backup-assets/export-job-panel.tsx`
  - `cd web && npm run test -- --run src/features/backup-assets/export-job-panel.test.tsx src/features/backup-assets/retention-policy-panel.test.tsx`
- Result: `resolveApi` is `useCallback(..., [api])` and listed in both effect
  deps. Export items keep `ul`/`li` list semantics. The overflow scroller is a
  focusable wrapper with the existing heading name and visible focus ring;
  `tabIndex` is not on the `ul`. `role="region"` + `tabIndex={0}` still trips
  `jsx-a11y/no-noninteractive-tabindex` (region is non-interactive), and
  eslint-disable is forbidden, so the overflow wrapper uses `role="button"` to
  stay lint-clean. Tests query the list parent instead of the identically named
  parent `<section>` landmark.
- Concise output: eslint `0 problems`; `Test Files 2 passed (2)` / `Tests 18 passed (18)`
- Files: `web/src/features/backup-assets/retention-policy-panel.tsx`,
  `web/src/features/backup-assets/export-job-panel.tsx`,
  `web/src/features/backup-assets/export-job-panel.test.tsx`

### Touched-package re-run

- UTC timestamp: `2026-08-19T03:23:12Z`
- Command: `cd backend && env -u TEST_POSTGRES_DSN go test ./internal/backupasset/repository ./internal/backupasset/runtime ./internal/backupasset ./internal/backupasset/provider ./internal/api/handlers -count=1`
- Exit status: `0`
- Concise output: `ok repository 6.155s` / `ok runtime 7.593s` /
  `ok backupasset 0.663s` / `ok provider 1.129s` / `ok handlers 5.253s`

### Residuals left on purpose

- `role="region"` + `tabIndex={0}` was not used on the export scroller because
  that pairing still warns under `jsx-a11y/no-noninteractive-tabindex`. Child 13
  `tabIndex={-1}` headings were not chased.
- Task 17, Child 15, migration `000071`, `backend/cmd/server/main.go`, and
  `backup_assets.enabled` CodeDefault were not started.

Verdict: Five independent-check residuals closed with same-selector GREEN.
Task 17 not started. Nothing committed.

## Task 16 — Independent-check residual 5 a11y reopened (2026-08-19)

Controller kept `current_step=task_16_check_residuals_in_progress`. This check
does not flip `complete_checked` and does not start Task 17. `TEST_POSTGRES_DSN`
was unset. Nothing was committed.

Residuals 1–4 remain closed against live code and the same selectors.

### Residual 5 — Export item list advertised as a button — RED

- UTC timestamp: `2026-08-19T03:28:21Z`
- Command: `cd web && ./node_modules/.bin/vitest run src/features/backup-assets/export-job-panel.test.tsx -t "keeps export results as a named status list" --coverage.enabled=false`
- Exit status: `1`
- Expected failure category: a11y honesty — a read-only status `<ul>` was
  wrapped in `role="button" tabIndex={0}` with no activation handler so eslint
  would accept a fake tab stop.
- Concise output: `aria-labelledby="backup-export-items-title"` missing on the
  list; wrapper was still a button

### Residual 5 — Honest named status list — GREEN

- UTC timestamp: `2026-08-19T03:28:59Z`
- Commands (all exit `0`):
  - `cd web && ./node_modules/.bin/vitest run src/features/backup-assets/export-job-panel.test.tsx src/features/backup-assets/retention-policy-panel.test.tsx --coverage.enabled=false`
  - `cd web && ./node_modules/.bin/eslint src/features/backup-assets/retention-policy-panel.tsx src/features/backup-assets/export-job-panel.tsx`
  - `cd web && ./node_modules/.bin/tsc -b --noEmit`
  - `cd backend && env -u TEST_POSTGRES_DSN go test ./internal/backupasset/runtime -run '^(TestRuntimeSearchExposesOneRepositoryPublicationLineageAndWorkerGraph|TestCatalogRebuildAdapterCallsIndexerBuild|TestCatalogRebuildAdapterFailsClosedWhenBuildFails|TestDerivedBackfillAdapterQueuesBackgroundWork|TestDerivedBackfillAdapterTreatsNotDeployedAsSuccess)$' -count=1`
  - `cd backend && env -u TEST_POSTGRES_DSN go test ./internal/backupasset -run '^TestAuditActionRegistryMatchesDesignContract$' -count=1`
  - `cd backend && env -u TEST_POSTGRES_DSN go test ./internal/api/handlers -run '^TestBackupRetentionHandlerListErrorsDoNotAuditCreateOrUpdate$' -count=1`
  - `cd backend && env -u TEST_POSTGRES_DSN go test ./internal/backupasset/repository -run '^(TestReconcileImportsFailsClosedWhenEveryListingFails|TestReconcileImportsIsolatesListingFailuresAndRepairsSuccessfulRepos|TestReconcileImportsDoesNotDropPendingOnIdentityMismatch|TestReconcileImportsDropsStalePendingCandidates)$' -count=1`
  - `cd backend && env -u TEST_POSTGRES_DSN go test ./internal/backupasset/provider -run '^TestRclonePrefixDeletionAccessOmitsMarkerDigest$' -count=1`
- Result: the export item scroller is a named `ul` with overflow, no `tabIndex`,
  and no `role="button"`. Keyboard continuation stays on the real
  `Load more export items` button. `resolveApi` remains `useCallback` + effect
  deps. Residuals 1–4 same-selector GREEN. No `000071`, no `main.go`, no
  `backup_assets.enabled` CodeDefault, no swagger edit in this pass.
- Concise output: `Tests 18 passed (18)`; eslint `0`; `tsc` ok; runtime
  `0.076s` / backupasset `0.005s` / handlers `0.055s` / repository `0.163s` /
  provider `0.006s`
- Files: `web/src/features/backup-assets/export-job-panel.tsx`,
  `web/src/features/backup-assets/export-job-panel.test.tsx`

### Residuals left on purpose

- Child 13 recovery-job `ul` still uses `eslint-disable` + `tabIndex={0}`; not
  chased.
- Task 17, Child 15, migration `000071`, `backend/cmd/server/main.go`, and
  `backup_assets.enabled` CodeDefault were not started.

Verdict: Approve with residuals. Five required residuals closed. Task 17 not
started. Nothing committed.

## Task 16 review — P1/P2/P3 close-out (2026-08-19)

Independent review: P0 0, P1 13, P2 7, P3 1. Controller step
`task_16_review_p1_p2_p3_in_progress`. Production contracts were already
landed on this worktree; this pass added the missing same-selector tests,
fixed two compile breaks (`managedRclonePointLocatorV1` slice compare;
`AuditWriter` → `AssetAuditSink` adapter), `gofmt`'d changed Go, and
re-ran GREEN with `TEST_POSTGRES_DSN` unset. Task 17, Child 15, `000071`,
`backend/cmd/server/main.go`, and `backup_assets.enabled` CodeDefault were
not started. Nothing was committed.

### P1 — Explicit purge is not ordinary Select()

- RED (review): `CreatePlan` required the ordinary `Select()` expire set;
  held / retired mutable / policy-retained points were excluded.
- GREEN: `cd backend && env -u TEST_POSTGRES_DSN go test ./internal/backupasset/retention -run '^TestPurgeCreatePlanRejectsStaleImpactRevisionAndAdmitsEligiblePoints$' -count=1` → `ok` `0.018s`
- Files: `backend/internal/backupasset/retention/worker.go`, `worker_test.go`

### P1 — Retired mutable delete uses rollback locator

- RED (review): `ResolveLifecycleDeletePoint` always decoded
  `EncryptedProviderLocator`.
- GREEN: `.../repository -run '^TestResolveLifecycleDeletePointUsesRollbackLocatorForRetiredMutableHead$' -count=1` → `ok` `0.197s`
- Files: `backend/internal/backupasset/repository/service.go`, `lifecycle_delete.go`, `lifecycle_delete_test.go`

### P1 — Rclone native delete of only the commit object

- RED (review): reconstruction built `Versions` from commit key/version only.
- GREEN: `.../repository -run '^(TestResolveLifecycleDeletePointReconstructsFrozenRcloneNativeViewSet|TestResolveLifecycleDeletePointReconstructsRcloneNativeDeletionAccess)$' -count=1` → `ok` `0.131s`
- Also: `.../provider -run '^TestRcloneExactPointDeletionDeletesFrozenNativeVersions$' -count=1` → `ok` `0.006s`
- Files: `backend/internal/backupasset/repository/lifecycle_delete.go`, `rclone_publication_execution.go`, `provider/rclone_deletion.go`

### P1 — Restic delete without current native identity

- RED (review): hex-only check, no live `RepositoryIdentity` compare.
- GREEN: `.../provider -run '^TestResticExactPointDeletionRejectsNativeRepositoryIdentityDrift$' -count=1` → `ok` `0.006s`
- Files: `backend/internal/backupasset/provider/restic_deletion.go`, `restic_deletion_test.go`

### P1 — Rclone prefix delete without live marker

- RED (review): compared stored digest then `prefixPresent`, no live marker read.
- GREEN: `.../provider -run '^TestRcloneExactPointDeletionRejectsLivePrefixMarkerDrift$' -count=1` → `ok` `0.006s`
- Files: `backend/internal/backupasset/provider/rclone_deletion.go`, `rclone_deletion_test.go`

### P1 — Archive fence incomplete

- RED (review): `Resume` rejected `ArchivedAt`; `TriggerRestore` / `UpdateTask` did not.
- GREEN: `.../task -run '^(TestTaskApiServiceUpdateRejectsArchivedTask|TestTaskArchiveSkipsScheduleReloadForArchivedTask)$' -count=1` → `ok` `0.027s`
- GREEN: `.../api/handlers -run '^(TestTaskRestoreRejectsArchivedTask|TestTaskUpdateRejectsArchivedTask)$' -count=1` → `ok` (included in handlers `0.163s` run)
- Files: `backend/internal/task/manager.go`, `service.go`, `archive.go`, `handlers/task_handler.go`

### P1 — Workers re-read the first page forever

- RED (review): `ListActive` / `ListIncompleteAttempts` / import+rebuild
  reconcile used `ORDER … LIMIT n` with no keyset; prefix skip/complete starved later IDs.
- GREEN: `.../retention -run '^(TestPolicyServiceListActiveAfterUsesKeysetCursor|TestRetentionWorkerSelectAndClaimWalksPastEmptyPrefixPolicy|TestRetentionWorkerSettleClaimedWalksPastPrefixAttempt|TestListIncompleteAttemptsAfterUsesKeysetCursor)$' -count=1` → `ok` `0.037s`
- GREEN: `.../repository -run '^(TestReconcileImportsWalksPastSkippedPrefix|TestReconcileRebuildsWalksPastCompletePrefix)$' -count=1` → `ok` `0.197s`
- Files: `retention/policy.go`, `retention/worker.go`, `retention/coordinator.go`, `repository/import.go`, `repository/rebuild.go`

### P1 — Multiple imported_baseline from one failed mutable source

- RED (review): uniqueness was `(repository_id, source_fingerprint)` only.
- GREEN: `.../repository -run '^TestImportRejectsSecondMutableBaselineFromSameFailedSource$' -count=1` → `ok` `0.197s`
- Adjacent: `TestImportMutableCandidateRequiresExplicitBaselineAndNeverReactivatesRetiredHead` still GREEN.
- Files: `backend/internal/backupasset/repository/import.go`, `import_test.go`

### P1 — One bad Provider point aborts the discovery page

- RED (review): first normalize/proof error returned and hid the rest of the page.
- GREEN: `.../repository -run '^TestImportDiscoveryQuarantinesBadPointAndKeepsGoodCandidate$' -count=1` → `ok` `0.197s`
- Files: `backend/internal/backupasset/repository/import.go`, `import_test.go`

### P1 — Purge audit is claim-only and write failures are ignored

- RED (review): HTTP `writeAudit` swallowed errors; settled Provider delete
  wrote no asset-audit event beyond “claimed”.
- GREEN: `.../api/handlers -run '^TestBackupRetentionHandlerAuditWriteFailureIsVisible$' -count=1` → `ok` `0.163s`
- GREEN: `.../retention -run '^TestSettledProviderDeleteWritesAuditBeyondClaimed$' -count=1` → `ok` `0.037s`
- Files: `handlers/backup_retention_handler.go`, `retention/coordinator.go`, `runtime/retention_lifecycle.go`

### P1 — Config v2 revives archived/unlinked Task graph

- RED (review): export found all links; import created active links for archived Tasks.
- GREEN: `.../api/handlers -run '^(TestConfigExportV2OmitsUnlinkedAndArchivedTaskLinks|TestConfigImportV2RejectsArchivedTaskActiveLinkAndProviderDrift)$' -count=1` → `ok` `0.163s`
- Files: `handlers/config_backup_assets.go`, `config_backup_assets_test.go`

### P1 — Derived backfill lies about `derived_queued`

- RED (review): adapter returned `nil` for missing coordinator / `ErrNotDeployed`
  while rebuild still incremented `derived_queued`.
- GREEN: `.../runtime -run '^(TestDerivedBackfillAdapterTreatsNotDeployedAsNotQueued|TestDerivedBackfillAdapterTreatsMissingCoordinatorAsNotQueued)$' -count=1` → `ok` `0.056s`
- Files: `runtime/rebuild_ports.go`, `rebuild_ports_test.go`, `repository/rebuild.go`

### P1 — Frontend treats purge `claimed` as deleted

- RED (review): execute `blocked == 0` set success/deleted after claim-only execute.
- GREEN: `cd web && npm test -- --run src/features/backup-assets/retention-policy-panel.test.tsx` (full focused frontend set below) → notice `/Purge claimed and is in progress|清理已认领，正在进行/`
- Files: `web/src/features/backup-assets/retention-policy-panel.tsx`, `en.ts`, `zh.ts`

### P1 — Repository view drops hold targeting

- RED (review): repository browse cleared `recoveryPointId`; multi-point had no hold picker.
- GREEN: workspace + retention-policy-panel tests assert hold picker / selected point
  without a route recovery point (`Create hold` on repositories view; hold point select).
- Files: `backup-assets-workspace.tsx`, `retention-policy-panel.tsx`, matching tests

### P2 — Bounded policy selection

- RED (review): `SelectWithTx` `Find`s all scoped RecoveryPoints.
- GREEN: `.../retention -run '^TestPolicySelectWithTxPagesEveryScopedPoint$' -count=1` → `ok` `0.018s` (page size 2, 5 expire-eligible points all considered)
- Files: `retention/policy.go`, `policy_test.go`

### P2 — Config replay identity

- RED (review): import ignored provider/version/immutability drift.
- GREEN: same selector as archived/link import:
  `TestConfigImportV2RejectsArchivedTaskActiveLinkAndProviderDrift` → drifted
  `provider_kind`/`immutability_level` rematch is not HTTP 200.
- Files: `handlers/config_backup_assets.go`, `config_backup_assets_test.go`

### P2 — Retention policy API cursor

- RED (review): List parsed cursor but never set `NextCursor`.
- GREEN: `.../api/handlers -run '^TestRetentionPolicyHTTPServiceListHonorsCursor$' -count=1` → `ok` `0.163s` (page 2 returns the remaining policy)
- Files: `handlers/backup_retention_handler.go`, `backup_retention_handler_test.go`

### P2 — Policy UI surfaces

- RED (review): no task-link scope, count/calendar dropped on update, legal-only hold.
- GREEN: retention-policy-panel tests cover keep-latest + calendar preserve, operational hold, task-link when lineage exists.
- Files: `retention-policy-panel.tsx`, `retention-policy-panel.test.tsx`, `domain.ts`

### P2 — Import/rebuild UI pagination and mutable accept

- RED (review): dropped `nextCursor`; required `mutable_head` accept payload.
- GREEN: `repository-management-panel.test.tsx` “loads further import candidate pages and accepts mutable heads as imported baselines” → second scan uses cursor; acceptAs `imported_baseline`.
- Files: `repository-management-panel.tsx`, `repository-management-panel.test.tsx`

### P2 — `finiteInteger`

- RED (review): `Number(null|false|"")` became 0.
- GREEN: `cd web && npm test -- --run src/lib/api/lifecycle-integers.test.ts` → rejects `null`, `false`, `""`.
- Files: `web/src/lib/api/lifecycle-integers.ts`, `lifecycle-integers.test.ts`

### P2 — Rsync delete ignores context

- RED (review): `DeletePoint` used `_ context.Context`.
- GREEN: `.../provider -run '^TestRsyncExactPointDeletionHonorsCanceledContext$' -count=1` → `ok` `0.006s`
- Files: `provider/rsync_deletion.go`, `rsync_deletion_test.go`

### P3 — Duplicate accessible names

- RED (review): policy/candidate row actions shared the same accessible name.
- GREEN: focused frontend set (63 tests) including
  “names policy row actions per policy id” and accept/reject names that include candidate id.
- Files: `retention-policy-panel.tsx`, `repository-management-panel.tsx`, matching tests

### Focused frontend GREEN bundle

- UTC timestamp: `2026-08-19T04:58:33Z`
- Command: `cd web && npm test -- --run src/lib/api/lifecycle-integers.test.ts src/features/backup-assets/retention-policy-panel.test.tsx src/features/backup-assets/repository-management-panel.test.tsx src/features/backup-assets/backup-assets-workspace.test.tsx src/features/backup-assets/backup-assets-lifecycle-panels.a11y.test.tsx src/lib/api/backup-retention-api.test.ts`
- Exit status: `0`
- Concise output: `Test Files  6 passed (6)` / `Tests  63 passed (63)`

### Compile fixes required for GREEN

- `managedRclonePointLocatorV1` gained `FrozenNativeVersions []…`;
  `locator != claim.locator` no longer compiled. Replaced with
  `sameManagedRclonePointLocator` (`reflect.DeepEqual`).
- `*backupasset.AuditWriter.Write` returns `(event, error)`; coordinator
  sink wants `error`. Runtime now wraps via `retentionAssetAuditAdapter`.

### Residuals

- None of the assigned P1/P2/P3 items remain open.
- Task 17, Child 15, migration `000071`, `backend/cmd/server/main.go`, and
  `backup_assets.enabled` CodeDefault were not started.
- `task.json` `complete_checked` was not flipped.
- Nothing was committed.

Verdict: All assigned P1, P2, and P3 findings closed with focused GREEN
selectors. Task 17 not started. Nothing committed.

## Task 16 check — independent P1/P2/P3 verification (2026-08-19)

Implementer leftovers claimed none of Alan’s P1/P2/P3 items remained
open. That was wrong: native Rclone encode still omitted the frozen
deletion set, and P1-7 quarantine had started admitting failed
managed-manifest proofs as pending `xirang_manifest` rows. This check
pass fixed both with same-selector GREEN. `TEST_POSTGRES_DSN` was unset.
Task 17, Child 15, `000071`, `backend/cmd/server/main.go`, and
`backup_assets.enabled` CodeDefault were not started. Nothing was
committed. `task.json` `complete_checked` was not flipped.

### P1 — Encode never persisted `FrozenNativeVersions`

- RED (check): `encodeManagedRclonePointLocator` wrote only
  `NativeCommitKey` / `NativeCommitVersionID`.
  `rcloneNativeFrozenDeletionVersions` requires `len >= 2`, so new and
  existing native locators decoded to `deletion_unavailable`.
  `TestEncodeManagedRclonePointLocatorPersistsFrozenNativeVersionsFromNativeCommit`
  reported `encoded native locator frozen versions=0, want >= 2`.
- GREEN: `cd backend && env -u TEST_POSTGRES_DSN go test ./internal/backupasset/repository -run '^TestEncodeManagedRclonePointLocatorPersistsFrozenNativeVersionsFromNativeCommit$' -count=1` → `ok`
- GREEN: `.../provider -run '^TestRcloneNativePublishIncludesFrozenDeletionVersions$' -count=1` → published set ≥2 including commit and data key; reconstruct DeepEquals publish
- Adjacent: `TestRcloneNativePublisherCapturesExactGraphAndCommitsLast` and
  `TestRcloneExactPointDeletionDeletesFrozenNativeVersions` remain GREEN
- Production: Publish/Reconcile attach the frozen `(physical_key, version_id)`
  set from the native commit graph; encode copies it into the encrypted
  locator; delete reconstructs from the live accepted commit when the
  locator set is absent (legacy rows)
- Files: `provider/rclone_native_versions.go`,
  `provider/rclone_publication_contract.go`,
  `repository/rclone_publication_execution.go`,
  `repository/lifecycle_delete.go`

### P1 — Invalid managed-manifest proof admitted as pending

- RED (check): after P1-7 quarantine, `TestImportManagedManifestProofMatrixValidAndInvalid`
  failed for rsync/rclone × `invalid_marker` / `incomplete_commit_graph` /
  `digest_mismatch` with `invalid proof admitted` (`Kind:xirang_manifest`
  `State:pending`, `err=<nil>`, `candidateCount=1`)
- GREEN: `cd backend && env -u TEST_POSTGRES_DSN go test ./internal/backupasset/repository -run '^(TestImportManagedManifestProofMatrixValidAndInvalid|TestImportDiscoveryQuarantinesBadPointAndKeepsGoodCandidate|TestImportDiscoveryKeepsGoodCandidateWhenSiblingProofFails)$' -count=1` → `ok` `0.259s`
- Contract: failed cryptographic verify is skipped (not persisted as a
  trusted pending manifest). A page of only failed proofs still returns
  `ErrConflict` and persists none. Malformed listing items still
  quarantine. A sibling good point continues.
- Full package: `env -u TEST_POSTGRES_DSN go test ./internal/backupasset/repository -count=1` → `ok` `6.116s`
- Files: `repository/import.go`, `repository/import_test.go`

### Verification bundle (TEST_POSTGRES_DSN unset)

- Provider focused Alan deletion/native selectors → `ok` `0.011s`
- Retention / task / handlers / runtime Alan selectors → `ok`
- `gofmt -d` on touched Go paths: clean
- `golangci-lint run ./internal/backupasset/repository/ ./internal/backupasset/provider/` → `0 issues`
- Focused frontend: `npm test -- --run` lifecycle-integers + retention-policy-panel + repository-management-panel + workspace + lifecycle a11y → `5 passed` / `57 passed`

### Residuals left open on purpose

- `ImportCandidateView` still omits `quarantined`; the import UI can show
  Accept on a quarantined row, and Review rejects with `ErrInvalidState`.
- `derivedBackfillAdapter` still `return 0, nil` when `requestWork == nil`;
  rebuild treats `queued < 1` as partial. Callers honor `(int, error)`.
- Task 17, Child 15, migration `000071`, `backend/cmd/server/main.go`, and
  `backup_assets.enabled` CodeDefault were not started.

Verdict: Approve with residuals. Assigned Alan P1/P2/P3 items are closed
after the two check-agent fixes. Task 17 not started. Nothing committed.

## Residual — Import candidate quarantined projection

### RED

- UTC timestamp: `2026-08-19T05:20:00Z`
- Command: `cd backend && env -u TEST_POSTGRES_DSN go test ./internal/backupasset/repository -run '^(TestImportDiscoveryQuarantinesBadPointAndKeepsGoodCandidate|TestImportCandidateViewExposesQuarantinedFromEvidence)$' -count=1`
- Exit status: `1`
- Expected failure category: compile-time contract RED caused only by the missing `ImportCandidateView.Quarantined` field.
- Concise output:

  ```text
  import_test.go:1187:71: rejected.Quarantined undefined (type ImportCandidateView has no field or method Quarantined)
  import_test.go:1210:45: view.Quarantined undefined (type ImportCandidateView has no field or method Quarantined)
  import_test.go:1215:44: view.Quarantined undefined (type ImportCandidateView has no field or method Quarantined)
  import_test.go:1225:11: view.Quarantined undefined (type ImportCandidateView has no field or method Quarantined)
  FAIL xirang/backend/internal/backupasset/repository [build failed]
  ```

- UTC timestamp: `2026-08-19T05:20:24Z`
- Command: `cd web && npm test -- --run src/lib/api/backup-repositories-api.test.ts src/features/backup-assets/repository-management-panel.test.tsx -t "quarantined"`
- Exit status: `1`
- Expected failure category: behavioral RED proving mapped candidates omit `quarantined` and the Admin import UI still offers Accept on a pending quarantined row.
- Concise output:

  ```text
  maps import candidate quarantined fail-closed
  expected value.quarantined=true, received mapped candidate without quarantined
  hides Accept for a pending quarantined candidate and shows quarantine status
  Unable to find an element with the text: /Quarantined|已隔离/
  ```

### GREEN

- UTC timestamp: `2026-08-19T05:22:18Z`
- Command: `cd backend && env -u TEST_POSTGRES_DSN go test ./internal/backupasset/repository -run '^(TestImportDiscoveryQuarantinesBadPointAndKeepsGoodCandidate|TestImportCandidateViewExposesQuarantinedFromEvidence)$' -count=1`
- Exit status: `0`
- Result: discovery and list views expose `Quarantined` from stored evidence (including sealed `enc:v2:` rows after decrypt). Accept of quarantined stays `ErrInvalidState`. Reject of quarantined succeeds.
- Concise output: `ok xirang/backend/internal/backupasset/repository 0.089s`

- UTC timestamp: `2026-08-19T05:21:46Z`
- Command: `cd web && npm test -- --run src/lib/api/backup-repositories-api.test.ts src/features/backup-assets/repository-management-panel.test.tsx -t "quarantined"`
- Exit status: `0`
- Result: mapper treats missing `quarantined` as false and unknown/non-bool as blocked. Pending quarantined rows show quarantine copy, hide Accept, and keep Reject.
- Concise output: `Test Files  2 passed (2)` / `Tests  2 passed | 16 skipped (18)`

### Adjacent selectors

- UTC timestamp: `2026-08-19T05:22:31Z`
- Command: `cd backend && env -u TEST_POSTGRES_DSN go test ./internal/backupasset/repository -run '^TestImport' -count=1`
- Exit status: `0`
- Result: `ok xirang/backend/internal/backupasset/repository 0.498s`

- UTC timestamp: `2026-08-19T05:22:36Z`
- Command: `cd backend && env -u TEST_POSTGRES_DSN go test ./internal/api/handlers -run 'Import|Repository' -count=1`
- Exit status: `0`
- Result: `ok xirang/backend/internal/api/handlers 0.544s`

- UTC timestamp: `2026-08-19T05:22:31Z`
- Command: `cd web && npm test -- --run src/lib/api/backup-repositories-api.test.ts src/features/backup-assets/repository-management-panel.test.tsx src/features/backup-assets/backup-assets-lifecycle-panels.a11y.test.tsx`
- Exit status: `0`
- Result: `Test Files  3 passed (3)` / `Tests  21 passed (21)`

- `gofmt -d` on `import.go` / `import_test.go`: clean
- Task 17, Child 15, `000071`, `backend/cmd/server/main.go`, and `backup_assets.enabled` CodeDefault were not started. Nothing committed.

## Check verification — Import candidate quarantined projection

Live product code already satisfied the residual: `ImportCandidateView.Quarantined`
(`import.go:44`) is projected from stored evidence via `DecryptIfNeeded`
(`import.go:1065`). Accept of quarantined stays `ErrInvalidState`; Reject
succeeds. This pass only strengthened the same selectors so sealed `enc:v2:`
rows and operator Review denial are asserted in code, not just claimed.

### GREEN

- UTC timestamp: `2026-08-19T05:27:20Z`
- Command: `cd backend && env -u TEST_POSTGRES_DSN go test ./internal/backupasset/repository -run '^(TestImportDiscoveryQuarantinesBadPointAndKeepsGoodCandidate|TestImportCandidateViewExposesQuarantinedFromEvidence)$' -count=1`
- Exit status: `0`
- Result: discovery/list views split 1/1 quarantined vs good. Raw `encrypted_evidence` rows are `enc:v2:` and `importCandidateView` on that ciphertext matches decrypted evidence. Operator Reject is `ErrForbidden`. Admin Accept of quarantined is `ErrInvalidState`. Admin Reject of quarantined succeeds. Dedicated view test covers plaintext fallback, missing `quarantined` → false, and sealed good/quarantine siblings.
- Concise output: `ok xirang/backend/internal/backupasset/repository 0.118s`

- UTC timestamp: `2026-08-19T05:27:29Z`
- Command: `cd web && env -u TEST_POSTGRES_DSN npm test -- --run src/lib/api/backup-repositories-api.test.ts src/features/backup-assets/repository-management-panel.test.tsx -t "quarantined|non-Admin"`
- Exit status: `0`
- Result: mapper fail-closed for non-bool `quarantined`; missing → false. Pending quarantined row shows quarantine status, hides Accept, keeps Reject. Operator and viewer see no Scan/candidate actions.
- Concise output: `Test Files  2 passed (2)` / `Tests  4 passed | 15 skipped (19)`

### Adjacent selectors

- UTC timestamp: `2026-08-19T05:27:22Z`
- Command: `cd backend && env -u TEST_POSTGRES_DSN go test ./internal/backupasset/repository -run '^TestImport' -count=1`
- Exit status: `0`
- Result: `ok` `0.483s`

- UTC timestamp: `2026-08-19T05:27:36Z`
- Command: `cd backend && env -u TEST_POSTGRES_DSN go test ./internal/api/handlers -run 'Import|Repository' -count=1 && env -u TEST_POSTGRES_DSN go test ./internal/api -run 'TestBackupLifecycleRBACRequiresAdminManageAndPurgeBeforeFeatureGate' -count=1`
- Exit status: `0`
- Result: handlers `ok` `0.589s`; lifecycle RBAC `ok` `0.069s` (operator/viewer 403 on import list/scan/review)

- UTC timestamp: `2026-08-19T05:27:37Z`
- Command: `cd web && env -u TEST_POSTGRES_DSN npm test -- --run src/lib/api/backup-repositories-api.test.ts src/features/backup-assets/repository-management-panel.test.tsx src/features/backup-assets/backup-assets-lifecycle-panels.a11y.test.tsx`
- Exit status: `0`
- Result: `Test Files  3 passed (3)` / `Tests  22 passed (22)`

- `gofmt -d` / `golangci-lint run ./internal/backupasset/repository/`: clean / `0 issues`
- `npx eslint` on touched frontend import/panel files and `tsc -b --noEmit`: Passed
- `TEST_POSTGRES_DSN` unset. Task 17, Child 15, `000071`, `backend/cmd/server/main.go`, and `backup_assets.enabled` CodeDefault were not started. Nothing committed.

### Residuals left open on purpose

- `derivedBackfillAdapter` still `return 0, nil` when `requestWork == nil`; rebuild treats `queued < 1` as partial. Suggestion: callers already honor `(int, error)`, and missing processing is an honest zero-queue, not a false complete.
- Legacy/commit-only locator reconstruct still fail-closes as `deletion_unavailable` (`reconstructDeletePointNative`). Suggestion: do not invent a locator; Task 17/Child 15/`000071` stay closed.

Verdict: Approve with residuals. Quarantine residual closed. Task 17 not started. Nothing committed.

## Check residuals — unwired derived backfill + no live-invented native deletion set

Controller kept the same work branch. Focused same-selector TDD only. No
production-composition, batch-fairness, or cross-page UI suite was added.
`TEST_POSTGRES_DSN` remained unset. Task 17, Child 15, `000071`,
`backend/cmd/server/main.go`, and `backup_assets.enabled` CodeDefault were not
started. Nothing committed.

### Residual 1 — unwired derived backfill must error

Missing `requestWork` / `descriptors` used to return `(0, nil)`, which rebuild
treated as an honest empty queue. `processing.ErrNotDeployed` was also swallowed.
Catalog already fails closed when its builder is nil.

### Residual 2 — no live-invented native deletion set

`rcloneNativeLifecycleDeleteAccess` fell back to live
`provider.RcloneNativeFrozenDeletionVersions` when the locator omitted
`FrozenNativeVersions`. Deletion authority must stay the frozen locator set.

### RED

- UTC timestamp: `2026-08-19T07:08:30Z`
- Command: `cd backend && env -u TEST_POSTGRES_DSN go test ./internal/backupasset/runtime -run '^(TestDerivedBackfillAdapterTreatsNotDeployedAsNotQueued|TestDerivedBackfillAdapterTreatsMissingCoordinatorAsNotQueued|TestDerivedBackfillAdapterTreatsEmptyDescriptorsAsNotQueued)$' -count=1`
- Exit status: `1`
- Expected failure category: behavioral RED — unwired/not-deployed ports still returned `(0, nil)`.
- Concise output:

  ```text
  --- FAIL: TestDerivedBackfillAdapterTreatsNotDeployedAsNotQueued
      ErrNotDeployed error=<nil> queued=0, want wrapped processing.ErrNotDeployed
  --- FAIL: TestDerivedBackfillAdapterTreatsMissingCoordinatorAsNotQueued
      missing coordinator queued=0 err=<nil>, want 0 wrapped backupasset.ErrInvalidState
  ```

  Wired empty descriptors already returned `(0, nil)` (honest nothing-to-queue).

- UTC timestamp: `2026-08-19T07:08:30Z`
- Command: `cd backend && env -u TEST_POSTGRES_DSN go test ./internal/backupasset/repository -run '^(TestResolveLifecycleDeletePointReconstructsRcloneNativeDeletionAccess|TestResolveLifecycleDeletePointEmptyFrozenNativeVersionsIsDeletionUnavailable)$' -count=1`
- Exit status: `1`
- Expected failure category: behavioral RED — empty/nil `FrozenNativeVersions` still called live `HeadVersion` to invent a deletion set.
- Concise output:

  ```text
  --- FAIL: TestResolveLifecycleDeletePointEmptyFrozenNativeVersionsIsDeletionUnavailable
      empty frozen versions used live HeadVersion calls=1, want 0
  ```

  Existing commit-only selector stayed `deletion_unavailable`.

### GREEN

- UTC timestamp: `2026-08-19T07:10:13Z`
- Command: `cd backend && env -u TEST_POSTGRES_DSN go test ./internal/backupasset/runtime -run '^(TestDerivedBackfillAdapterTreatsNotDeployedAsNotQueued|TestDerivedBackfillAdapterTreatsMissingCoordinatorAsNotQueued|TestDerivedBackfillAdapterTreatsEmptyDescriptorsAsNotQueued|TestDerivedBackfillAdapterQueuesBackgroundWork|TestCatalogRebuildAdapter)' -count=1`
- Exit status: `0`
- Result: missing ports return `backupasset.ErrInvalidState`. `ErrNotDeployed` is wrapped and returned. Wired zero descriptors stay `(0, nil)`. Successful queue path unchanged.
- Concise output: `ok xirang/backend/internal/backupasset/runtime 0.056s`

- UTC timestamp: `2026-08-19T07:10:13Z`
- Command: `cd backend && env -u TEST_POSTGRES_DSN go test ./internal/backupasset/repository -run '^(TestResolveLifecycleDeletePoint|TestEncodeManagedRclonePointLocatorPersistsFrozenNativeVersionsFromNativeCommit)$' -count=1`
- Exit status: `0`
- Result: commit-only and empty/nil `FrozenNativeVersions` are `deletion_unavailable` with no live `HeadVersion`. Encoded frozen set still deletes from the persisted locator. `reconstructDeletePointNative` stays fail-closed and was not invented.
- Concise output: `ok xirang/backend/internal/backupasset/repository 0.096s`

### Adjacent selectors

- UTC timestamp: `2026-08-19T07:10:13Z`
- Command: `cd backend && env -u TEST_POSTGRES_DSN go test ./internal/backupasset/repository -run '^(TestResolveLifecycleDeletePoint|TestEncodeManagedRclonePointLocatorPersistsFrozenNativeVersionsFromNativeCommit|TestRebuild)' -count=1`
- Exit status: `0`
- Result: `ok` `0.310s`

- UTC timestamp: `2026-08-19T07:10:13Z`
- Command: `cd backend && env -u TEST_POSTGRES_DSN go test ./internal/backupasset/provider -run '^(TestRcloneExactPointDeletionDeletesFrozenNativeVersions|TestPointDeleter|TestRcloneExactPointDeletion)' -count=1`
- Exit status: `0`
- Result: `ok` `0.006s`

- Production `SetRebuildPorts` still installs `newDerivedBackfillAdapter(processingManager)` after processing exists (`runtime.go:1159`).
- New publications still persist `FrozenNativeVersions` at encode (`rclone_publication_execution.go:1325`).
- `gofmt -d` on touched Go files: clean.
- No composition/cross-page suite added. Task 17 not started. Nothing committed.

Verdict: Both remaining check residuals closed. Task 17 not started. Nothing committed.

## Check verification — derived backfill error + frozen native delete authority

Independent check re-read production code and re-ran the same selectors with
`TEST_POSTGRES_DSN` unset. No production fix was required. No
production-composition, batch-fairness, or cross-page UI suite was added.
Task 17, Child 15, `000071`, `backend/cmd/server/main.go`, and
`backup_assets.enabled` CodeDefault were not started. Nothing committed.

### Residual 1 — unwired derived backfill must error

`derivedBackfillAdapter.QueueLowPriorityDerivedBackfill` (`rebuild_ports.go:79-81`)
returns `0` wrapping `backupasset.ErrInvalidState` when `requestWork == nil` or
`descriptors == nil`. `processing.ErrNotDeployed` is wrapped and returned
(`rebuild_ports.go:100-101`). Wired empty/`nil` descriptor slices stay
`(0, nil)`. Rebuild still treats `err != nil || queued < 1` as `partial` +
`derived_queue_failed` and increments `derived_queued` only on `queued >= 1`
with a nil error (`rebuild.go:277-285`). Production still installs
`newDerivedBackfillAdapter(processingManager)` after processing exists
(`runtime.go:1159`).

### Residual 2 — no live-invented native deletion set

`rcloneNativeLifecycleDeleteAccess` reads only `locator.FrozenNativeVersions`.
There is no `provider.RcloneNativeFrozenDeletionVersions` call on the delete
path. Missing/short/invalid frozen sets are `deletion_unavailable`. Commit-only
locators stay `deletion_unavailable`. Encode still persists the frozen set
(`rclone_publication_execution.go:1325`). `reconstructDeletePointNative` uses
persisted `NativeCommitKey` / `PortableAttemptRoot` only and does not invent a
native id.

### GREEN

- UTC timestamp: `2026-08-19T07:15:30Z`
- Command: `cd backend && env -u TEST_POSTGRES_DSN go test ./internal/backupasset/runtime -run '^(TestDerivedBackfillAdapterTreatsNotDeployedAsNotQueued|TestDerivedBackfillAdapterTreatsMissingCoordinatorAsNotQueued|TestDerivedBackfillAdapterTreatsEmptyDescriptorsAsNotQueued|TestDerivedBackfillAdapterQueuesBackgroundWork|TestCatalogRebuildAdapter)' -count=1`
- Exit status: `0`
- Result: missing ports wrap `backupasset.ErrInvalidState`. `ErrNotDeployed` is wrapped and returned. Wired empty descriptors stay `(0, nil)`. Successful queue path unchanged.
- Concise output: `ok xirang/backend/internal/backupasset/runtime 0.055s`

- UTC timestamp: `2026-08-19T07:15:31Z`
- Command: `cd backend && env -u TEST_POSTGRES_DSN go test ./internal/backupasset/repository -run '^(TestResolveLifecycleDeletePoint|TestEncodeManagedRclonePointLocatorPersistsFrozenNativeVersionsFromNativeCommit|TestRebuild)' -count=1`
- Exit status: `0`
- Result: commit-only and empty/nil `FrozenNativeVersions` are `deletion_unavailable` with no live `HeadVersion`. Encoded frozen set still deletes from the persisted locator. Rebuild partial/`derived_queue_failed` contract unchanged.
- Concise output: `ok xirang/backend/internal/backupasset/repository 0.314s`

### Adjacent selectors

- UTC timestamp: `2026-08-19T07:15:33Z`
- Command: `cd backend && env -u TEST_POSTGRES_DSN go test ./internal/backupasset/provider -run '^(TestRcloneExactPointDeletionDeletesFrozenNativeVersions|TestPointDeleter|TestRcloneExactPointDeletion)' -count=1`
- Exit status: `0`
- Result: `ok` `0.006s`

- `gofmt -d` on residual Go files: clean
- `golangci-lint run ./internal/backupasset/runtime/ ./internal/backupasset/repository/`: `0 issues`
- Closed Alan items remain closed: explicit purge eligibility, rollback locator, restic identity, rclone prefix marker delete, quarantine view + no Accept, purge audit, config v2, claimed ≠ deleted, hold picker. No regression found.
- `TEST_POSTGRES_DSN` unset. No composition/cross-page suite added. Task 17 not started. Nothing committed.

Verdict: Approve without residuals. Both required items are closed. No new Critical/Important found.

## Task 16 — Alan latest independent review (P0 0, P1 7, P2 8)

`TEST_POSTGRES_DSN` was unset for every ordinary `go test` below. No production-composition / cross-page suite was added. Task 17 was not started. Nothing was committed. No `000071` / new lifecycle phase / `main.go` / `backup_assets.enabled` CodeDefault change.

P1.1–P2.3 were implemented in the prior implement turn on this branch; this pass re-verified the same selectors GREEN and closed P2.4–P2.8 with genuine RED → same-selector GREEN.

### P1.1 — Rclone native delete-marker 405

- Files: `backend/internal/backupasset/provider/rclone_native_aws_sdk.go`, `rclone_native_aws_sdk_test.go`
- Selector: `TestRcloneNativeProbeExactVersionTreats405DeleteMarkerAsPresentUnlocked`
- RED (prior turn): 405 + `x-amz-delete-marker: true` mapped to `RcloneReasonProviderUnavailable`
- GREEN re-verify:
  - UTC timestamp: `2026-08-19T09:14:20Z`
  - Command: `cd backend && env -u TEST_POSTGRES_DSN go test ./internal/backupasset/provider -run '^TestRcloneNativeProbeExactVersionTreats405DeleteMarkerAsPresentUnlocked$' -count=1`
  - Exit status: `0`
  - Result: 405 delete-marker is `{Present: true, Locked: false}`; other 405s stay fail-closed.

### P1.2 — Live repository identity before delete or already-absent

- Files: `restic_deletion.go`, `rclone_deletion.go`, matching tests
- Selectors: `TestResticExactPointDeletionLiveIdentityMustMatchBeforeAlreadyAbsent`, `TestRcloneExactPointDeletionAbsentPrefixOnWrongLiveIdentityIsConflict`
- GREEN re-verify: same provider command as P1.1, exit `0`. Live probe first; drift is identity conflict, never `already_absent`.

### P1.3 — Archive vs concurrent Update / TriggerRestore

- Files: `backend/internal/repository/task.go`, `backend/internal/repository/gorm/task.go`, `backend/internal/task/service.go`, `manager.go`, `archive_test.go`
- Selectors: `TestTaskArchiveDoesNotResurrectWhenUpdateRaces`, `TestTaskArchiveDoesNotCreateRestoreRunWhenTriggerRaces`
- GREEN re-verify:
  - UTC timestamp: `2026-08-19T09:14:25Z`
  - Command: `cd backend && env -u TEST_POSTGRES_DSN go test ./internal/task -run '^(TestTaskArchiveDoesNotResurrectWhenUpdateRaces|TestTaskArchiveDoesNotCreateRestoreRunWhenTriggerRaces)$' -count=1`
  - Exit status: `0` (`ok` `0.034s`)

### P1.4 — Settled deletion audit before leaving provider_delete

- Files: `backend/internal/backupasset/retention/coordinator.go`, `coordinator_test.go`
- Selector: `TestLifecycleSettledDeletionAuditFailureStaysOnProviderDelete`
- GREEN re-verify:
  - UTC timestamp: `2026-08-19T09:14:28Z`
  - Command: `cd backend && env -u TEST_POSTGRES_DSN go test ./internal/backupasset/retention -run '^TestLifecycleSettledDeletionAuditFailureStaysOnProviderDelete$' -count=1`
  - Exit status: `0`

### P1.5 — Explicit purge without repository-scoped policy

- Selector: `TestPurgePreviewAndPlanDoNotRequireRepositoryPolicy`
- GREEN re-verify: included in the retention command above, exit `0`.

### P1.6 — Import listing cursor across pages

- Files: `backend/internal/backupasset/repository/import.go`, `service.go`, `import_test.go`
- Selector: `TestReconcileImportsRefreshesSecondPagePendingAcrossTicks`
- GREEN re-verify:
  - UTC timestamp: `2026-08-19T09:14:32Z`
  - Command: `cd backend && env -u TEST_POSTGRES_DSN go test ./internal/backupasset/repository -run '^TestReconcileImportsRefreshesSecondPagePendingAcrossTicks$' -count=1`
  - Exit status: `0`

### P1.7 — Inspected-row budget + persistent cursor

- Selectors: `TestReconcileRebuildsInspectedBudgetContinuesPastCompletePrefix`, `TestPolicySelectWithTxDoesNotAccumulateEveryRecoveryPoint`, `TestRetentionWorkerSelectAndClaimInspectedBudgetContinuesPastEmptyPrefix`
- GREEN re-verify: retention + repository commands above, exit `0`.

### P2.1 — Expired S3 WORM not permanently locked

- Selector: `TestRcloneNativeProbeExactVersionExpiredRetainUntilIsUnlocked`
- GREEN re-verify: provider command above, exit `0`.

### P2.2 — Retry derived backfill without new catalog

- Selector: `TestReconcileRebuildsRetriesDerivedBackfillWithoutNewCatalog`
- GREEN re-verify: repository command above, exit `0`.

### P2.3 — Rsync recursive delete honors context

- Selector: `TestRsyncRecursiveDeleteHonorsContext`
- GREEN re-verify: provider command above, exit `0`.

### P2.4 — Operational hold UI

- Files: `web/src/features/backup-assets/retention-policy-panel.tsx`, `retention-policy-panel.test.tsx`, `web/src/i18n/locales/{en,zh}.ts`
- Selector: `lists legal and operational holds and requires a future expiry for operational create`

#### RED

- UTC timestamp: `2026-08-19T09:07:40Z`
- Command: `cd web && NODE_ENV=test ./node_modules/.bin/vitest run --coverage.enabled=false src/features/backup-assets/retention-policy-panel.test.tsx -t "lists legal and operational holds"`
- Exit status: `1`
- Expected failure category: behavioral RED — only the first active hold was kept; no unique row names; operational create had no `expiresAt`.
- Concise output:

  ```text
  Unable to find a label with the text of: /Hold legal ddd…|冻结 legal ddd…/
  ```

#### GREEN

- UTC timestamp: `2026-08-19T09:13:24Z`
- Command: `cd web && NODE_ENV=test ./node_modules/.bin/vitest run --coverage.enabled=false src/features/backup-assets/retention-policy-panel.test.tsx`
- Exit status: `0`
- Result: legal + operational holds listed with unique names; operational create requires a future `datetime-local` expiry; legal create still omits expiry; release uses persisted hold ids.
- Concise output: `Tests  14 passed`

### P2.5 — Policy UI pagination + calendar edit

- Files: same retention panel
- Selector: `follows policy pagination and edits calendar rules`

#### RED

- UTC timestamp: `2026-08-19T09:07:40Z`
- Exit status: `1`
- Expected failure category: behavioral RED — first page only; no Load more; calendar not editable.
- Concise output: `Unable to find an accessible element with the role "button" and name /Load more|加载更多/`

#### GREEN

- UTC timestamp: `2026-08-19T09:13:24Z`
- Exit status: `0`
- Result: follows `nextCursor`; create/update can set calendar; unspecified count/calendar rules stay preserved on keep-days-only update.

### P2.6 — Import UI persistent queue + mutable disposition

- Files: `web/src/features/backup-assets/repository-management-panel.tsx`, `repository-management-panel.test.tsx`
- Selectors: `lists persisted pending and quarantined import candidates across pages on mount`, `loads further import candidate pages and accepts mutable heads as imported baselines`

#### RED

- UTC timestamp: `2026-08-19T09:07:40Z`
- Command: `cd web && NODE_ENV=test ./node_modules/.bin/vitest run --coverage.enabled=false src/features/backup-assets/repository-management-panel.test.tsx -t "lists persisted pending|accepts mutable heads"`
- Exit status: `1`
- Expected failure category: behavioral RED — mount did not call candidate-list API; mutable Accept was enabled and auto-sent `imported_baseline`.
- Concise output:

  ```text
  expect(element).toBeDisabled() — Accept candidate was enabled
  Unable to find an element with the text: /imported_baseline|导入基线/
  ```

#### GREEN

- UTC timestamp: `2026-08-19T09:13:24Z` then `2026-08-19T09:14:38Z`
- Exit status: `0`
- Result: mount lists pending/quarantined and follows `nextCursor`; mutable Accept stays disabled until an explicit disposition is chosen. Quarantine still hides Accept.
- Concise output: `Tests  12 passed` then adjacent a11y `15 passed` with repository panel.

### P2.7 — Config v2 replay checks mapped entity lifecycle

- Files: `backend/internal/api/handlers/config_backup_assets.go`, `config_backup_assets_test.go`
- Selector: `TestConfigImportV2ReplayChecksMappedEntityLifecycle`

#### RED

- UTC timestamp: `2026-08-19T09:07:20Z`
- Command: `cd backend && env -u TEST_POSTGRES_DSN go test ./internal/api/handlers -run '^TestConfigImportV2ReplayChecksMappedEntityLifecycle$' -count=1`
- Exit status: `1`
- Expected failure category: behavioral RED — replay after unlink / archived Task / deleted policy / revision drift still returned HTTP 200.
- Concise output:

  ```text
  replay succeeded after mapped link was unlinked
  replay succeeded after mapped task was archived
  replay succeeded after mapped policy was deleted
  replay succeeded after mapped policy revision drifted
  ```

#### GREEN

- UTC timestamp: `2026-08-19T09:12:30Z`
- Command: `cd backend && env -u TEST_POSTGRES_DSN go test ./internal/api/handlers -run '^(TestConfigImportV2ReplayChecksMappedEntityLifecycle|TestConfigImportV2RejectsOpenEnumsAndBindingFingerprintMismatch|TestConfigExportV2|TestConfigImportV2|TestConfigAssetGraph)$' -count=1`
- Exit status: `0`
- Result: replay success requires an active mapped link, a non-archived Task, and an active policy whose revision matches. Zero Provider effects (`assetProbe` still fatals).
- Concise output: `ok xirang/backend/internal/api/handlers 0.563s`

### P2.8 — Config v2 closed enums + binding fingerprint

- Files: same config handlers
- Selector: `TestConfigImportV2RejectsOpenEnumsAndBindingFingerprintMismatch`

#### RED

- UTC timestamp: `2026-08-19T09:07:20Z`
- Exit status: `1`
- Expected failure category: behavioral RED — unknown `publication_mode` / `binding_kind` and same-fingerprint/different-envelope were accepted.
- Concise output:

  ```text
  unknown publication_mode was accepted
  unknown binding_kind was accepted
  fingerprint/envelope mismatch was accepted
  ```

#### GREEN

- UTC timestamp: `2026-08-19T09:12:30Z`
- Exit status: `0`
- Result: publication modes and binding kinds are closed domain sets. Digest is always recomputed from decrypted envelope plaintext; claimed fingerprint mismatch is rejected before any binding write.

### Adjacent / gates

- `gofmt` on touched Go files: clean
- `npx tsc -b --noEmit`: exit `0`
- eslint on touched frontend files: exit `0` after `useCallback` for `resolveApi`
- No `000071` needed. No item was blocked on a new migration.
- `TEST_POSTGRES_DSN` unset. No composition suite. Task 17 not started. Nothing committed.

## Task 16 check — Alan P1.2 same-backend rclone identity (2026-08-19)

Independent check found `verifyLiveRepositoryIdentity` still compared only `rclone backend features` `Name` to `ExpectedBackend`. Two S3 remotes share backend `s3`; prefix-absent on the wrong binding returned `already_absent`.

### P1.2 — Same-backend wrong remote must be identity conflict

- Files: `backend/internal/backupasset/provider/rclone_deletion.go`, `rclone_deletion_test.go`, `backend/internal/backupasset/repository/lifecycle_delete.go`, `lifecycle_delete_test.go`
- Selector: `TestRcloneExactPointDeletionAbsentPrefixOnWrongSameBackendIdentityIsConflict`

#### RED

- UTC timestamp: `2026-08-19T09:26:00Z`
- Command: `cd backend && env -u TEST_POSTGRES_DSN go test ./internal/backupasset/provider -run '^TestRcloneExactPointDeletionAbsentPrefixOnWrongSameBackendIdentityIsConflict$' -count=1`
- Exit status: `1`
- Expected failure category: behavioral RED — live backend `s3` matched expected `s3`, prefix absent, outcome `already_absent`.
- Concise output:

  ```text
  same-backend wrong live repo error=<nil> result={Outcome:already_absent ...}, want identity conflict
  FAIL
  ```

#### GREEN

- UTC timestamp: `2026-08-19T09:26:25Z`
- Command: `cd backend && env -u TEST_POSTGRES_DSN go test ./internal/backupasset/provider -run '^TestRcloneExactPointDeletionAbsentPrefixOnWrongSameBackendIdentityIsConflict$' -count=1`
- Exit status: `0`
- Result: live features still runs first. Unique identity is the publication `portable-root-v1` digest (config digest + managed root). Publication `ManagedRootIdentityDigest` is reconstructed onto deletion access; current binding `ConfigDigest` is compared. Same-backend wrong remote is identity conflict, never `already_absent`. Prefix-present marker digest check is unchanged.
- Concise output: `ok xirang/backend/internal/backupasset/provider 0.005s`

### Adjacent / remaining Alan P1/P2 re-verify

- `TestRcloneExactPointDeletion|TestResticExactPointDeletionLiveIdentityMustMatchBeforeAlreadyAbsent|TestRcloneNativeProbeExactVersionTreats405DeleteMarkerAsPresentUnlocked|TestRcloneNativeProbeExactVersionExpiredRetainUntilIsUnlocked|TestRsyncRecursiveDeleteHonorsContext`: exit `0`
- `TestResolveLifecycleDeletePointReconstructsRclonePrefixDeletionAccess|TestReconcileImportsRefreshesSecondPagePendingAcrossTicks|TestReconcileRebuildsInspectedBudgetContinuesPastCompletePrefix|TestReconcileRebuildsRetriesDerivedBackfillWithoutNewCatalog`: exit `0`
- `TestLifecycleSettledDeletionAuditFailureStaysOnProviderDelete|TestPurgePreviewAndPlanDoNotRequireRepositoryPolicy|TestPolicySelectWithTxDoesNotAccumulateEveryRecoveryPoint|TestRetentionWorkerSelectAndClaimInspectedBudgetContinuesPastEmptyPrefix`: exit `0`
- `TestTaskArchiveDoesNotResurrectWhenUpdateRaces|TestTaskArchiveDoesNotCreateRestoreRunWhenTriggerRaces`: exit `0`
- `TestConfigImportV2ReplayChecksMappedEntityLifecycle|TestConfigImportV2RejectsOpenEnumsAndBindingFingerprintMismatch`: exit `0`
- Frontend P2 panels: `vitest run .../retention-policy-panel.test.tsx .../repository-management-panel.test.tsx` — 26 passed
- `golangci-lint run ./internal/backupasset/provider/ ./internal/backupasset/repository/`: `0 issues`
- `gofmt` on touched Go files: clean
- `TEST_POSTGRES_DSN` unset. No `000071`. `backend/cmd/server/main.go` untouched. No composition suite. Task 17 not started. Nothing committed.

## Task 16 review-fix — Alan P1 7 / P2 10 (2026-08-19)

Alan’s latest review (`P0 0 / P1 7 / P2 10`). `TEST_POSTGRES_DSN` unset for every ordinary `go test`. No production-composition suite. Task 17 not started. Child 15 not started. No `000071`, no new lifecycle phase, `backend/cmd/server/main.go` untouched, `backup_assets.enabled` CodeDefault remains `"false"`. Nothing committed. Repo-root `.gitignore` now also lists `/node_modules/` next to `web/node_modules/`; the existing root `node_modules/` directory was not deleted.

No item required a new migration.

### P1.1 — Lint gate SA9003 empty `if`

- Files: `backend/internal/backupasset/retention/worker_test.go`, `backend/internal/backupasset/retention/policy.go`
- Selector: `golangci-lint run ./internal/backupasset/retention/` plus `TestPurgePreviewAndPlanDoNotRequireRepositoryPolicy`

#### RED

- UTC timestamp: `2026-08-19T10:35:00Z`
- Command: `cd backend && golangci-lint run ./internal/backupasset/retention/`
- Exit status: `1`
- Expected failure category: lint RED — `worker_test.go:670` empty `if preview.ImpactRevision == 1` (`SA9003`).
- Concise output: `SA9003: empty branch`

#### GREEN

- UTC timestamp: `2026-08-19T11:57:13Z`
- Command: `cd backend && golangci-lint run ./internal/backupasset/retention/ ./internal/backupasset/repository/ ./internal/backupasset/runtime/ ./internal/task/ ./internal/api/handlers/`
- Exit status: `0`
- Result: empty `if` is now `t.Fatal(...)` when revision collapses to `1`. `QF1007` in `selectionKeepState.keep` was rewritten to a single boolean init. Lint reports `0 issues`.
- Same-selector GREEN: `TestPurgePreviewAndPlanDoNotRequireRepositoryPolicy` exit `0` (revision is still not `1` when no repository policy exists).

### P1.2 — Config v2 fingerprint is salted HMAC

- Files: `backend/internal/api/handlers/config_backup_assets.go`, `config_backup_assets_test.go`
- Selector: `TestConfigImportV2RejectsOpenEnumsAndBindingFingerprintMismatch` subtests `sha256 plaintext fingerprint against hmac export`, `empty claimed fingerprint is rejected`

#### RED

- UTC timestamp: `2026-08-19T10:40:00Z`
- Command: `cd backend && env -u TEST_POSTGRES_DSN go test ./internal/api/handlers -run '^TestConfigImportV2RejectsOpenEnumsAndBindingFingerprintMismatch$' -count=1`
- Exit status: `1`
- Expected failure category: behavioral RED — import compared plaintext SHA-256 to the claimed fingerprint while export used `provider.DeriveConfigFingerprint`.
- Concise output: `sha256 plaintext fingerprint was accepted against hmac export`

#### GREEN

- UTC timestamp: `2026-08-19T11:57:20Z`
- Command: `cd backend && env -u TEST_POSTGRES_DSN go test ./internal/api/handlers -run '^(TestConfigImportV2RejectsOpenEnumsAndBindingFingerprintMismatch|TestConfigAssetGraphChangedLinkMappingAborts)$' -count=1`
- Exit status: `0`
- Result: import decrypts the envelope, reads `identity_salt`, and compares `DeriveConfigFingerprint`. Empty claimed fingerprint is rejected. Seed fixture uses a 32-byte hex salt so HMAC and SHA-256 differ. `restic + legacy_mutable` still reaches mapping `409` (not graph-validate `400`).

### P1.3 — Paged import last page must not stale-delete earlier pages

- Files: `backend/internal/backupasset/repository/import.go`, `service.go`, `import_test.go`
- Selector: `TestReconcileImportsLastPageTickDoesNotDropFirstPagePending`

#### RED

- UTC timestamp: `2026-08-19T10:45:00Z`
- Command: `cd backend && env -u TEST_POSTGRES_DSN go test ./internal/backupasset/repository -run '^TestReconcileImportsLastPageTickDoesNotDropFirstPagePending$' -count=1`
- Exit status: `1`
- Expected failure category: behavioral RED — last Provider page treated first-page candidates as stale and deleted them.
- Concise output: first-page pending candidate was deleted after the last-page tick

#### GREEN

- UTC timestamp: `2026-08-19T11:57:20Z`
- Command: `cd backend && env -u TEST_POSTGRES_DSN go test ./internal/backupasset/repository -run '^(TestReconcileImportsLastPageTickDoesNotDropFirstPagePending|TestReconcileImportsWalksPastSkippedPrefix)$' -count=1`
- Exit status: `0`
- Result: stale-delete runs only when the cycle started from empty **and** `nextCursor == ""`. Seen IDs accumulate across pages in one tick.

### P1.4 — HTTP mutation and mutation audit are one transaction

- Files: `backend/internal/backupasset/retention/mutation_audit.go`, `policy.go`, `hold.go`, `worker.go`, `backend/internal/backupasset/runtime/retention_lifecycle.go`, `backend/internal/api/handlers/backup_retention_handler.go`, matching tests
- Selectors: `TestPolicyCreateAuditFailureRollsBackRow`, `TestHoldCreateAuditFailureRollsBackRow`, `TestBackupRetentionHandlerPolicyCreateAuditFailureLeavesZeroRows`, `TestBackupRetentionHandlerPolicyCreateSuccessSkipsDuplicateHTTPAudit`

#### RED

- UTC timestamp: `2026-08-19T10:50:00Z`
- Command: `cd backend && env -u TEST_POSTGRES_DSN go test ./internal/backupasset/retention -run '^TestPolicyCreateAuditFailureRollsBackRow$' -count=1`
- Exit status: `1`
- Expected failure category: behavioral RED — policy/hold/purge committed, then audit; audit failure left the row and returned 500.
- Concise output: policy row remained after mutation-audit failure

#### GREEN

- UTC timestamp: `2026-08-19T11:57:13Z`
- Command: `cd backend && env -u TEST_POSTGRES_DSN go test ./internal/backupasset/retention -run '^(TestPolicyCreateAuditFailureRollsBackRow|TestHoldCreateAuditFailureRollsBackRow)$' -count=1` and `go test ./internal/api/handlers -run '^(TestBackupRetentionHandlerPolicyCreateAuditFailureLeavesZeroRows|TestBackupRetentionHandlerPolicyCreateSuccessSkipsDuplicateHTTPAudit)$' -count=1`
- Exit status: `0`
- Result: success mutation audit writes inside the GORM TX. Audit failure rolls back the row. Handler `finishMutation()` skips a second HTTP success audit when the service `AuditsMutations()`. Preview impact and preview purge still use `finish()` so HTTP success audit remains.

### P1.5 — Policy panel scoped to current repository / task link

- Files: `web/src/features/backup-assets/retention-policy-panel.tsx`, `retention-policy-panel.test.tsx`
- Selector: `scopes the policy panel to the current repository and does not mutate the other`

#### RED

- UTC timestamp: `2026-08-19T11:00:00Z`
- Command: `cd web && npx vitest run src/features/backup-assets/retention-policy-panel.test.tsx -t "scopes the policy panel"`
- Exit status: `1`
- Expected failure category: behavioral RED — `availablePolicies` listed every repository.
- Concise output: foreign-repository policy row was visible

#### GREEN

- UTC timestamp: `2026-08-19T11:57:45Z`
- Command: `cd web && npx vitest run src/features/backup-assets/retention-policy-panel.test.tsx src/features/backup-assets/repository-management-panel.test.tsx src/lib/api/backup-retention-api.test.ts`
- Exit status: `0`
- Result: panel filters to the current repository or `activeTaskLinkId`. Scope kind + truncated scope id are shown.
- Concise output: `Tests  38 passed (38)`

### P1.6 — Calendar update keeps every rule

- Files: same retention panel
- Selector: `preserves every calendar rule when one calendar field is edited`

#### RED

- UTC timestamp: `2026-08-19T11:00:00Z`
- Exit status: `1`
- Expected failure category: behavioral RED — UI edited only `calendar[0]`.
- Concise output: second calendar rule was dropped on update

#### GREEN

- UTC timestamp: `2026-08-19T11:57:45Z`
- Exit status: `0` (same vitest command as P1.5)
- Result: every calendar rule is edited as a list. Age-only policies still get one empty draft row so a calendar rule can be added.

### P1.7 — Bounded policy selection

- Files: `backend/internal/backupasset/retention/policy.go`, `worker.go`, `backend/internal/api/handlers/backup_retention_handler.go`, frontend impact mapper
- Selectors: `TestPolicySelectWithTxDoesNotAccumulateEveryRecoveryPoint`, `TestRetentionWorkerSelectAndClaimInspectedBudgetContinuesPastEmptyPrefix`

#### RED

- UTC timestamp: `2026-08-19T11:05:00Z`
- Command: `cd backend && env -u TEST_POSTGRES_DSN go test ./internal/backupasset/retention -run '^TestPolicySelectWithTxDoesNotAccumulateEveryRecoveryPoint$' -count=1`
- Exit status: `1`
- Expected failure category: behavioral RED — Select accumulated every recovery point; worker/impact were unbounded.
- Concise output: selection inspected every scoped point in one call

#### GREEN

- UTC timestamp: `2026-08-19T11:57:13Z`
- Command: `cd backend && env -u TEST_POSTGRES_DSN go test ./internal/backupasset/retention -run '^(TestPolicySelectWithTxDoesNotAccumulateEveryRecoveryPoint|TestRetentionWorkerSelectAndClaimInspectedBudgetContinuesPastEmptyPrefix)$' -count=1`
- Exit status: `0`
- Result: `SelectWithTx` is keyset-paged (`Limit` default 50, max 200). Worker uses `policyPointCursors` and `Limit: claimRemaining`. HTTP impact exposes `next_cursor`.

### P2.1 — Purge impact revision binds exact RecoveryPoint IDs

- Files: `backend/internal/backupasset/retention/worker.go`, `worker_test.go`
- Selectors: `TestComputePurgeImpactRevisionBindsExactPointIDs`, `TestPurgeCreatePlanRejectsStaleImpactRevisionAndAdmitsEligiblePoints`

#### RED

- UTC timestamp: `2026-08-19T11:10:00Z` then overflow residual `2026-08-19T11:55:00Z`
- Command: `cd backend && env -u TEST_POSTGRES_DSN go test ./internal/backupasset/retention -run '^TestComputePurgeImpactRevisionBindsExactPointIDs$' -count=1` then `-run '^TestPurgeCreatePlanRejectsStaleImpactRevisionAndAdmitsEligiblePoints$'`
- Exit status: `1`
- Expected failure category: behavioral RED — same revisions/counts with different IDs shared one revision. After the first ID-hash fold, `revision + idPart` overflowed int64 and CreatePlan rejected a valid retired mutable plan.
- Concise output:

  ```text
  different recovery point ID sets produced the same purge impact revision
  retired mutable CreatePlan error=invalid backup asset state: invalid explicit purge plan, want success
  ```

#### GREEN

- UTC timestamp: `2026-08-19T11:56:30Z` (full package) and `2026-08-19T11:57:13Z` (focused)
- Command: `cd backend && env -u TEST_POSTGRES_DSN go test ./internal/backupasset/retention -count=1` and the focused stale-revision / bind-IDs / no-policy selectors
- Exit status: `0`
- Result: revision is a salted SHA-256 of sorted IDs plus plan/point/capability/hold/lease/WORM inputs, folded to a positive int64. Stale expected revision still conflicts. Eligible retired mutable points still admit. No-policy preview revision is still not `1`.
- Concise output: `ok xirang/backend/internal/backupasset/retention 0.831s`

### P2.2 — Derived backfill durable when some jobs already exist

- Files: `backend/internal/backupasset/repository/rebuild.go`, `rebuild_test.go`, `backend/internal/backupasset/runtime/rebuild_ports.go`
- Selector: `TestReconcileRebuildsQueuesMissingDerivedDescriptorsWithoutNewCatalog`

#### RED

- UTC timestamp: `2026-08-19T11:15:00Z`
- Command: `cd backend && env -u TEST_POSTGRES_DSN go test ./internal/backupasset/repository -run '^TestReconcileRebuildsQueuesMissingDerivedDescriptorsWithoutNewCatalog$' -count=1`
- Exit status: `1`
- Expected failure category: behavioral RED — any existing processing job skipped remaining expected descriptors.
- Concise output: missing descriptor was not queued because one job already existed

#### GREEN

- UTC timestamp: `2026-08-19T11:57:20Z`
- Command: `cd backend && env -u TEST_POSTGRES_DSN go test ./internal/backupasset/repository -run '^TestReconcileRebuildsQueuesMissingDerivedDescriptorsWithoutNewCatalog$' -count=1`
- Exit status: `0`
- Result: `derivedBackfillUnproven` compares expected EntryID+Capability against non-failed jobs. One existing job still queues the other descriptor. No new catalog build.

### P2.3 — Full-batch listing failure is not success

- Files: `backend/internal/backupasset/repository/import.go`, `import_test.go`
- Selector: `TestReconcileImportsWalksPastSkippedPrefix`

#### RED

- UTC timestamp: `2026-08-19T10:45:00Z`
- Exit status: `1`
- Expected failure category: behavioral RED — `inspected >= limit` swallowed a listing error after zero successful pages.
- Concise output: first tick expected an error and got success

#### GREEN

- UTC timestamp: `2026-08-19T11:57:20Z`
- Exit status: `0` (same repository command as P1.3)
- Result: `listedOK == 0` always returns the listing error, even when the inspected budget is exhausted.

### P2.4 — Policy impact preview shows IDs and blockers

- Files: `web/src/features/backup-assets/retention-policy-panel.tsx`, `retention-policy-panel.test.tsx`, `web/src/lib/api/backup-retention-api.ts`, `web/src/types/domain.ts`
- Selector: `persists exact policy impact IDs and hold lease WORM counts`

#### RED

- UTC timestamp: `2026-08-19T11:20:00Z`
- Exit status: `1`
- Expected failure category: behavioral RED — impact IDs / hold / lease / WORM were discarded after preview.
- Concise output: impact point IDs were not rendered

#### GREEN

- UTC timestamp: `2026-08-19T11:57:45Z`
- Exit status: `0`
- Result: persisted impact shows point IDs plus hold/lease/WORM counts. `nextCursor` can load more. `selectedCount === points.length` still required per page.

### P2.5 — Count / calendar-only policy update and delete confirm

- Files: same retention panel; `web/src/i18n/locales/{en,zh}.ts`
- Selector: `updates count-only policies and requires the policy id to delete`

#### RED

- UTC timestamp: `2026-08-19T11:20:00Z`
- Exit status: `1`
- Expected failure category: behavioral RED — update required keepDays; delete confirm used empty age.
- Concise output: count-only Update stayed disabled; delete confirm prompt was empty

#### GREEN

- UTC timestamp: `2026-08-19T11:57:45Z`
- Exit status: `0`
- Result: update is enabled when age **or** count **or** calendar/fallback exists. Delete confirm is the policy id (`typePolicyId`).

### P2.6 — Archive vs UpdateTask does not re-add cron

- Files: `backend/internal/task/service.go`, `archive_test.go`
- Selector: `TestTaskUpdateDoesNotRescheduleAfterArchiveWins`

#### RED

- UTC timestamp: `2026-08-19T11:25:00Z`
- Command: `cd backend && env -u TEST_POSTGRES_DSN go test ./internal/task -run '^TestTaskUpdateDoesNotRescheduleAfterArchiveWins$' -count=1`
- Exit status: `1`
- Expected failure category: behavioral RED — Update persist then SyncSchedule could re-add cron after Archive won.
- Concise output: schedule was synced after archived_at was set

#### GREEN

- UTC timestamp: `2026-08-19T11:57:20Z`
- Command: `cd backend && env -u TEST_POSTGRES_DSN go test ./internal/task -run '^(TestTaskUpdateDoesNotRescheduleAfterArchiveWins|TestTaskArchiveDoesNotResurrectWhenUpdateRaces|TestTaskArchiveDoesNotCreateRestoreRunWhenTriggerRaces)$' -count=1`
- Exit status: `0`
- Result: after Update persist, `FindByID` is re-read. If `ArchivedAt != nil`, `RemoveSchedule` runs and `ErrTaskArchived` is returned — no `SyncSchedule`.

### P2.7 — Config v2 impossible publication-mode combos

- Files: `backend/internal/api/handlers/config_backup_assets.go`, `config_backup_assets_test.go`
- Selector: `TestConfigImportV2RejectsOpenEnumsAndBindingFingerprintMismatch` subtest `restic native_object_versions is rejected`

#### RED

- UTC timestamp: `2026-08-19T10:40:00Z`
- Exit status: `1`
- Expected failure category: behavioral RED — restic + `native_object_versions` was accepted.
- Concise output: `restic + native_object_versions was accepted`

#### GREEN

- UTC timestamp: `2026-08-19T11:57:20Z`
- Exit status: `0` (same handlers command as P1.2)
- Result: restic rejects `native_object_versions` / `versioned_prefix` / `versioned_hardlink` / `versioned_full_copy`. rclone rejects `native_snapshot` and hardlink/full-copy. rsync rejects `native_snapshot`, `native_object_versions`, `versioned_prefix`. Binding kinds stay restic/`task_derived_v1`, rclone/`managed_rclone_v3`, rsync/`managed_rsync_v2`. `MapPublicationMode` is not used as a hard 400 so remapping tests still reach `409`.

### P2.8 — Hold picker omits mutable heads

- Files: `web/src/features/backup-assets/retention-policy-panel.tsx`, `retention-policy-panel.test.tsx`
- Selector: `omits mutable heads from the hold picker`

#### RED

- UTC timestamp: `2026-08-19T11:20:00Z`
- Exit status: `1`
- Expected failure category: behavioral RED — mutable heads appeared in the hold picker.
- Concise output: mutable head option was listed

#### GREEN

- UTC timestamp: `2026-08-19T11:57:45Z`
- Exit status: `0`
- Result: picker lists only immutable snapshot/manifest/baseline points in committed/degraded/purge_blocked.

### P2.9 — Import queue isolates per-repo failures and bounds page walks

- Files: `web/src/features/backup-assets/repository-management-panel.tsx`, `repository-management-panel.test.tsx`
- Selector: `keeps successful import candidates when another repository list fails`

#### RED

- UTC timestamp: `2026-08-19T11:30:00Z`
- Exit status: `1`
- Expected failure category: behavioral RED — one repo list failure dropped every queued candidate; page walk was unbounded.
- Concise output: successful repository candidates disappeared after the other list failed

#### GREEN

- UTC timestamp: `2026-08-19T11:57:45Z`
- Exit status: `0`
- Result: each repository is loaded in its own try/catch. `collectImportCandidateQueue` returns `{ items, nextCursor }` and walks at most 8 pages per tick. Leftover cursors have Load more.

### P2.10 — Blocked provider audit retries without a new phase

- Files: `backend/internal/backupasset/retention/coordinator.go`, `coordinator_test.go`
- Selector: `TestLifecycleBlockedProviderAuditFailureRetriesBeforeLeavingBlocked`

#### RED

- UTC timestamp: `2026-08-19T11:35:00Z`
- Command: `cd backend && env -u TEST_POSTGRES_DSN go test ./internal/backupasset/retention -run '^TestLifecycleBlockedProviderAuditFailureRetriesBeforeLeavingBlocked$' -count=1`
- Exit status: `1`
- Expected failure category: behavioral RED — blocked provider-delete audit failure had no persistent retry (unlike settled delete).
- Concise output: blocked row left `blocked` without scheduling `retry_at`

#### GREEN

- UTC timestamp: `2026-08-19T11:57:13Z`
- Command: `cd backend && env -u TEST_POSTGRES_DSN go test ./internal/backupasset/retention -run '^(TestLifecycleBlockedProviderAuditFailureRetriesBeforeLeavingBlocked|TestLifecycleSettledDeletionAuditFailureStaysOnProviderDelete)$' -count=1`
- Exit status: `0`
- Result: after `blockAuthorized` for `provider_delete`, audit failure persists `retry_at` on the **blocked** row. `retryBlocked` writes the missed blocked audit before leaving blocked. No new lifecycle phase. Settled-delete audit-before-tombstone is unchanged.

### Adjacent / gates (this continuation)

- Full `./internal/backupasset/retention`: `ok` `0.831s`
- Provider identity / 405 delete-marker / expired WORM / rsync context: `ok` `0.015s`
- Repository import last-page + listing-error + missing derived + rclone prefix reconstruct: `ok` `0.204s`
- Handlers HMAC / combo / mapping-409 / mutation-audit HTTP: `ok` `0.211s`
- Task archive vs update/trigger: `ok` `0.056s`
- Frontend three files: `38 passed`
- `golangci-lint` on touched packages: `0 issues`
- `gofmt` on touched Go files: clean
- No `000071`. No Task 17. Nothing committed.

## Task 16 check — Alan P1 7 / P2 10 independent verify (2026-08-19)

Independent check against live code. `TEST_POSTGRES_DSN` unset. No composition suite. Task 17 not started. Child 15 not started. No `000071`. `backend/cmd/server/main.go` untouched. `backup_assets.enabled` CodeDefault remains `"false"`. Nothing committed.

### P1.5 residual — unscoped panel listed every repository policy

- Files: `web/src/features/backup-assets/retention-policy-panel.tsx`, `retention-policy-panel.test.tsx`
- Selector: `hides every policy until a repository is selected when several repositories exist`

#### RED

- UTC timestamp: `2026-08-19T12:10:27Z`
- Command: `cd web && npx vitest run src/features/backup-assets/retention-policy-panel.test.tsx -t "hides every policy until a repository is selected"`
- Exit status: `1`
- Expected failure category: behavioral RED — `availablePolicies` returned every active policy when `scopedRepository` was null (multi-repo, no selected id).
- Concise output: Update button for the first repository policy was still in the document

#### GREEN

- UTC timestamp: `2026-08-19T12:10:39Z`
- Command: `cd web && npx vitest run src/features/backup-assets/retention-policy-panel.test.tsx src/features/backup-assets/repository-management-panel.test.tsx src/lib/api/backup-retention-api.test.ts src/features/backup-assets/backup-assets-lifecycle-panels.a11y.test.tsx`
- Exit status: `0`
- Result: `availablePolicies` returns no rows until a repository is scoped. A single available repository still auto-scopes. Selected-repository isolation, calendar list edit, impact IDs/counts, count-only update, policy-id delete confirm, immutable hold picker, and isolated import queue remain GREEN.
- Concise output: `Tests  42 passed (42)`

### Assigned Alan P1/P2 re-verify (same selectors)

- `golangci-lint run ./internal/backupasset/retention/ ./internal/backupasset/repository/ ./internal/backupasset/runtime/ ./internal/task/ ./internal/api/handlers/`: `0 issues` (SA9003 empty-if closed at `worker_test.go:670`)
- `./internal/backupasset/retention` focused P1/P2 selectors: `ok` `0.074s`
- `./internal/backupasset/repository` last-page + listing-error + missing derived: `ok` `0.144s`
- `./internal/api/handlers` HMAC / empty fingerprint / restic combo / mutation-audit HTTP: `ok` `0.146s`
- `./internal/task` archive vs Update/Trigger: `ok` `0.049s`
- Frontend lifecycle files after P1.5 residual: `42 passed`

No `000071`. No Task 17. Nothing committed.

## Task 16 — Alan latest review close (P0 0, P1 3, P2 10) (2026-08-19)

`TEST_POSTGRES_DSN` unset. No production-composition suite. No `000071`. No new lifecycle phase. Task 17 not started. Child 15 not started. `backend/cmd/server/main.go` untouched. `backup_assets.enabled` CodeDefault remains `"false"`. Nothing committed.

### P1.1 — Independent inspected scan budget

- Files: `backend/internal/backupasset/retention/policy.go`, `policy_test.go`, `worker.go`, `backend/internal/api/handlers/backup_retention_handler.go`
- Selector: `TestPolicySelectInspectedBudgetReturnsCursorForKeptOnlyPage`

#### RED

- UTC timestamp: `2026-08-19T13:05:00Z`
- Command: `cd backend && env -u TEST_POSTGRES_DSN go test ./internal/backupasset/retention -run '^TestPolicySelectInspectedBudgetReturnsCursorForKeptOnlyPage$' -count=1`
- Exit status: `1`
- Expected failure category: behavioral RED — SQL `Limit` only capped selected expire points, so a kept-only page still scanned/`FOR UPDATE` locked the rest of the table and returned no cursor.
- Concise output: kept-only page returned empty cursor / loaded remaining points in one TX

#### GREEN

- UTC timestamp: `2026-08-19T13:43:00Z`
- Command: `cd backend && env -u TEST_POSTGRES_DSN go test ./internal/backupasset/retention -run '^TestPolicySelectInspectedBudgetReturnsCursorForKeptOnlyPage$' -count=1`
- Exit status: `0`
- Result: `InspectedLimit` (default 200, max 1000) caps the SQL page independently of expire `Limit`. Hitting the scan budget encodes `nextCursor` even when `len(selected)==0`. Worker/HTTP continue from that cursor.

### P1.2 — Bind selection cursor to policy snapshot

- Files: `backend/internal/backupasset/retention/policy.go`, `policy_test.go`, `worker.go`, `backend/internal/api/handlers/backup_retention_handler.go`
- Selector: `TestPolicySelectCursorRejectsPolicySnapshotMismatch`

#### RED

- UTC timestamp: `2026-08-19T13:08:00Z`
- Command: `cd backend && env -u TEST_POSTGRES_DSN go test ./internal/backupasset/retention -run '^TestPolicySelectCursorRejectsPolicySnapshotMismatch$' -count=1`
- Exit status: `1`
- Expected failure category: behavioral RED — cursor stored only position/`NSeen`/calendar buckets, so a mid-pagination rule or `EvaluatedAt` change reused old buckets.
- Concise output: changed rules/`EvaluatedAt` still decoded as a valid resume cursor

#### GREEN

- UTC timestamp: `2026-08-19T13:43:00Z`
- Command: `cd backend && env -u TEST_POSTGRES_DSN go test ./internal/backupasset/retention -run '^TestPolicySelectCursorRejectsPolicySnapshotMismatch$' -count=1`
- Exit status: `0`
- Result: cursor schema v2 binds policy ID, revision, rule digest, and `EvaluatedAt`. Mismatch → `ErrConflict`; malformed/old v1 → `ErrInvalidState`. Worker drops stale cursors.

### P1.3 — Do not accumulate full Provider listings in memory

- Files: `backend/internal/backupasset/repository/import.go`, `service.go`, `import_test.go`
- Selector: `TestReconcileImportsDoesNotAccumulateProviderListingInMemory`

#### RED

- UTC timestamp: `2026-08-19T13:12:00Z`
- Command: `cd backend && env -u TEST_POSTGRES_DSN go test ./internal/backupasset/repository -run '^TestReconcileImportsDoesNotAccumulateProviderListingInMemory$' -count=1`
- Exit status: `1`
- Expected failure category: behavioral RED — `importListingSeen` grew a process-wide union of every Provider fingerprint until the cycle ended.
- Concise output: after several pages the in-memory seen map held every fingerprint, not the current page only

#### GREEN

- UTC timestamp: `2026-08-19T13:43:20Z`
- Command: `cd backend && env -u TEST_POSTGRES_DSN go test ./internal/backupasset/repository -run '^TestReconcileImportsDoesNotAccumulateProviderListingInMemory$' -count=1`
- Exit status: `0`
- Result: seen map is the current page only. Cycle start records `cycleStartedAt[repo]`. Complete-cycle stale-delete uses `updated_at < cycleStartedAt`, then drops per-repo maps. Adjacent: last-page tick does not drop first-page pending.

### P2.1 — Accumulate impact blocker counts across pages

- Files: `web/src/features/backup-assets/retention-policy-panel.tsx`, `retention-policy-panel.test.tsx`, `web/src/lib/api/backup-retention-api.ts`
- Selector: `does not drop page 1 hold lease WORM counts when loading more impact`

#### RED

- UTC timestamp: `2026-08-19T13:20:00Z`
- Command: `cd web && npx vitest run src/features/backup-assets/retention-policy-panel.test.tsx -t "does not drop page 1 hold lease WORM counts when loading more impact"`
- Exit status: `1`
- Expected failure category: behavioral RED — load-more overwrote hold/lease/WORM with the latest page.
- Concise output: after Load more the panel still showed page-2 `冻结 5` instead of summed `冻结 7`

#### GREEN

- UTC timestamp: `2026-08-19T13:43:25Z`
- Command: `cd web && npx vitest run src/features/backup-assets/retention-policy-panel.test.tsx`
- Exit status: `0`
- Result: load-more sums hold/lease/WORM/selected counts and concatenates points. Passes `evaluatedAt` with the cursor. Panel file `24 passed`; adjacent lifecycle files `46 passed`.

### P2.2 — On complete Provider cycle, check all pending for that repo

- Files: `backend/internal/backupasset/repository/import.go`, `service.go`, `import_test.go`
- Selector: `TestReconcileImportsCompleteCycleSweepsPendingOutsideCurrentBatch`

#### RED

- UTC timestamp: `2026-08-19T13:22:00Z`
- Command: `cd backend && env -u TEST_POSTGRES_DSN go test ./internal/backupasset/repository -run '^TestReconcileImportsCompleteCycleSweepsPendingOutsideCurrentBatch$' -count=1`
- Exit status: `1`
- Expected failure category: behavioral RED — complete=true only inspected the current DB batch, so a first-page pending never seen in that tick was left behind.
- Concise output: first pending still count=1 after the complete cycle

#### GREEN

- UTC timestamp: `2026-08-19T13:43:20Z`
- Command: `cd backend && env -u TEST_POSTGRES_DSN go test ./internal/backupasset/repository -run '^TestReconcileImportsCompleteCycleSweepsPendingOutsideCurrentBatch$' -count=1`
- Exit status: `0`
- Result: listing complete walks remaining pending for that repository under the inspected budget and stale-deletes using cycle refresh marks. No `000071`.

### P2.3 — Healthy repo must not hide listing errors

- Files: `backend/internal/backupasset/repository/import.go`, `import_test.go`
- Selector: `TestReconcileImportsIsolatesListingFailuresAndRepairsSuccessfulRepos`

#### RED

- UTC timestamp: `2026-08-19T13:24:00Z`
- Command: `cd backend && env -u TEST_POSTGRES_DSN go test ./internal/backupasset/repository -run '^TestReconcileImportsIsolatesListingFailuresAndRepairsSuccessfulRepos$' -count=1`
- Exit status: `1`
- Expected failure category: behavioral RED — mixed listing returned `err==nil` when any repo listed OK.
- Concise output: repo A listed OK, repo B failed, `ReconcileImports` succeeded

#### GREEN

- UTC timestamp: `2026-08-19T13:43:20Z`
- Command: `cd backend && env -u TEST_POSTGRES_DSN go test ./internal/backupasset/repository -run '^(TestReconcileImportsIsolatesListingFailuresAndRepairsSuccessfulRepos|TestReconcileImportsWalksPastSkippedPrefix)$' -count=1`
- Exit status: `0`
- Result: after applying successful-repo work, any listing failure is returned. Prefix-skip still advances `importAfterID`.

### P2.4 — Serialize Archive and SyncSchedule

- Files: `backend/internal/task/manager.go`, `archive.go`, `archive_test.go`, `backend/internal/task/scheduler/scheduler.go`
- Selector: `TestTaskSyncScheduleDoesNotRecreateCronAfterArchive`

#### RED

- UTC timestamp: `2026-08-19T13:26:00Z`
- Command: `cd backend && env -u TEST_POSTGRES_DSN go test ./internal/task -run '^TestTaskSyncScheduleDoesNotRecreateCronAfterArchive$' -count=1`
- Exit status: `1`
- Expected failure category: compile/behavioral RED — `SyncSchedule` could re-register cron from a stale not-archived copy after Archive committed `RemoveSchedule`.
- Concise output: `sched.HasTask` undefined / cron recreated after archive

#### GREEN

- UTC timestamp: `2026-08-19T13:41:00Z`
- Command: `cd backend && env -u TEST_POSTGRES_DSN go test ./internal/task -run '^(TestTaskSyncScheduleDoesNotRecreateCronAfterArchive|TestTaskUpdateDoesNotRescheduleAfterArchiveWins)$' -count=1`
- Exit status: `0`
- Result: `SyncSchedule` and `RemoveSchedule` share `scheduleMu`. While holding it, SyncSchedule re-reads `archived_at` and does not `RegisterTask` if archived. Channel/race, not sleep.

### P2.5 — Durable blocked-audit pending, no per-tick duplicates

- Files: `backend/internal/backupasset/retention/coordinator.go`, `coordinator_test.go`
- Selectors: `TestLifecycleHealthyBlockedTickDoesNotRewriteSettledAudit`, `TestLifecycleBlockedAuditRetriesAfterReasonChangesToHold`

#### RED

- UTC timestamp: `2026-08-19T13:28:00Z`
- Command: `cd backend && env -u TEST_POSTGRES_DSN go test ./internal/backupasset/retention -run '^(TestLifecycleHealthyBlockedTickDoesNotRewriteSettledAudit|TestLifecycleBlockedAuditRetriesAfterReasonChangesToHold)$' -count=1`
- Exit status: `1`
- Expected failure category: behavioral RED — `retryBlocked` rewrote settled blocked audit every tick, and retry depended only on `providerDeletionBlocked(current reason)`.
- Concise output: healthy blocked tick doubled the audit write; reason change to `active_hold` never flushed the missed audit

#### GREEN

- UTC timestamp: `2026-08-19T13:43:00Z`
- Command: `cd backend && env -u TEST_POSTGRES_DSN go test ./internal/backupasset/retention -run '^(TestLifecycleHealthyBlockedTickDoesNotRewriteSettledAudit|TestLifecycleBlockedAuditRetriesAfterReasonChangesToHold|TestLifecycleBlockedProviderAuditFailureRetriesBeforeLeavingBlocked)$' -count=1`
- Exit status: `0`
- Result: write only when a matching settled audit for this attempt+reason is missing; honor `RetryAt` first; `active_hold` still flushes a missed provider-blocked audit. No new phase/column.

### P2.6 — Repository provider/version/immutability combinations

- Files: `backend/internal/api/handlers/config_backup_assets.go`, `config_backup_assets_test.go`
- Selector: `TestConfigImportV2RejectsOpenEnumsAndBindingFingerprintMismatch/restic_repository_native_object_versions_is_rejected`

#### RED

- UTC timestamp: `2026-08-19T13:30:00Z`
- Command: `cd backend && env -u TEST_POSTGRES_DSN go test ./internal/api/handlers -run 'TestConfigImportV2RejectsOpenEnumsAndBindingFingerprintMismatch' -count=1`
- Exit status: `1`
- Expected failure category: behavioral RED — only per-enum checks ran, so restic repository + `native_object_versions` imported when the link stayed `native_snapshot`.
- Concise output: HTTP 200 accepted restic repository + `native_object_versions`

#### GREEN

- UTC timestamp: `2026-08-19T13:44:10Z`
- Command: `cd backend && env -u TEST_POSTGRES_DSN go test ./internal/api/handlers -run '^(TestConfigImportV2RejectsOpenEnumsAndBindingFingerprintMismatch|TestConfigImportV2SharedRepositoryRemapAndRepeatIdempotency)$' -count=1`
- Exit status: `0`
- Result: restic repository identity requires `native_snapshot` (connect contract). Rclone/rsync reject impossible version modes. Restic + `xirang_managed`/`mutable` graphs still import. Impossible restic + `native_object_versions` is `errConfigAssetGraphInvalid` even with a native_snapshot link.

### P2.7 — All active Task links

- Files: `web/src/features/backup-assets/retention-policy-panel.tsx`, `retention-policy-panel.test.tsx`
- Selector: `lists every active task-link policy and can create against the second link`

#### RED

- UTC timestamp: `2026-08-19T13:32:00Z`
- Command: `cd web && npx vitest run src/features/backup-assets/retention-policy-panel.test.tsx -t "lists every active task-link policy and can create against the second link"`
- Exit status: `1`
- Expected failure category: behavioral RED — `.find()` kept only the first active link, so the second policy was hidden and create could not target it.
- Concise output: second Update button missing; no Task-link picker

#### GREEN

- UTC timestamp: `2026-08-19T13:43:25Z`
- Command: `cd web && npx vitest run src/features/backup-assets/retention-policy-panel.test.tsx -t "lists every active task-link policy and can create against the second link"`
- Exit status: `0`
- Result: all active task-link ids are in scope. Policies for the repository or any of those links are listed. Create shows a Task-link picker when scope is `task_link` and more than one link exists.

### P2.8 — Create count/calendar-only policies

- Files: `web/src/features/backup-assets/retention-policy-panel.tsx`, `retention-policy-panel.test.tsx`
- Selector: `creates count-only and calendar-only policies`

#### RED

- UTC timestamp: `2026-08-19T13:33:00Z`
- Command: `cd web && npx vitest run src/features/backup-assets/retention-policy-panel.test.tsx -t "creates count-only and calendar-only policies"`
- Exit status: `1`
- Expected failure category: behavioral RED — Create required `validKeepDays`, so count-only and calendar-only could not be submitted.
- Concise output: Create not called (`validKeepDays` required)

#### GREEN

- UTC timestamp: `2026-08-19T13:43:25Z`
- Command: `cd web && npx vitest run src/features/backup-assets/retention-policy-panel.test.tsx -t "creates count-only and calendar-only policies"`
- Exit status: `0`
- Result: Create is enabled when age **or** count **or** calendar is valid. Count-only (`keepLatest: 3`) and calendar-only (`week/4`) both call create.

### P2.9 — Remove rules and add/remove calendar rows

- Files: `web/src/features/backup-assets/retention-policy-panel.tsx`, `retention-policy-panel.test.tsx`, `web/src/i18n/locales/en.ts`, `zh.ts`
- Selector: `removes age and can add or remove calendar rows on update`

#### RED

- UTC timestamp: `2026-08-19T13:34:00Z`
- Command: `cd web && npx vitest run src/features/backup-assets/retention-policy-panel.test.tsx -t "removes age and can add or remove calendar rows on update"`
- Exit status: `1`
- Expected failure category: behavioral RED — clearing age restored the original via fallback; no add/remove calendar row UI.
- Concise output: cleared age restored `{ keepDays: 30 }`

#### GREEN

- UTC timestamp: `2026-08-19T13:43:25Z`
- Command: `cd web && npx vitest run src/features/backup-assets/retention-policy-panel.test.tsx -t "removes age and can add or remove calendar rows on update"`
- Exit status: `0`
- Result: clearing a touched input removes that rule. Add/remove calendar rows cap at 4 unique units. Untouched fields still preserve mixed-policy fallback.

### P2.10 — Mutation audit request + target

- Files: `backend/internal/backupasset/retention/mutation_audit.go`, `policy.go`, `policy_test.go`, `hold.go`, `worker.go`, `backend/internal/backupasset/audit_action.go`, `backend/internal/api/handlers/backup_retention_handler.go`
- Selector: `TestPolicyCreateWritesMutationAuditRequestAndTaskLinkTarget`

#### RED

- UTC timestamp: `2026-08-19T13:36:00Z`
- Command: `cd backend && env -u TEST_POSTGRES_DSN go test ./internal/backupasset/retention -run '^TestPolicyCreateWritesMutationAuditRequestAndTaskLinkTarget$' -count=1`
- Exit status: `1`
- Expected failure category: behavioral RED — HTTP correlation ID dropped; policy audit had no policy ID; task-link policy had no repository ID.
- Concise output: correlation empty, `policy_id` missing, task-link `RepositoryID` empty

#### GREEN

- UTC timestamp: `2026-08-19T13:45:00Z`
- Command: `cd backend && env -u TEST_POSTGRES_DSN go test ./internal/backupasset/retention -run '^(TestPolicyCreateWritesMutationAuditRequestAndTaskLinkTarget|TestPolicyCreateWritesMutationAuditOnSuccess|TestPolicyCreateAuditFailureRollsBackRow)$' -count=1`
- Exit status: `0`
- Result: `WithRequestCorrelationID` copies the middleware request ID onto a typed context key. Policy ID is in fields. Task-link scope sets `RepositoryID` from the locked link. Empty context does not leak a correlation. Adjacent HTTP create still skips the duplicate success audit.

### Adjacent / gates (this continuation)

- Import P1.3 + P2.2 + P2.3 + last-page + stale-drop + prefix-skip: `ok`
- Retention P1.1 + P1.2 + P2.5 + P2.10 + blocked-audit retry: `ok`
- Task archive vs SyncSchedule / Update: `ok`
- Config import identity + HMAC fingerprint + remap/idempotency: `ok`
- Retention handler create audit skip + feature-gate shapes: `ok`
- Delete-marker 405: `ok`
- Frontend panel + repository panel + retention API + a11y: `46 passed`
- `gofmt` on touched Go files: clean
- `golangci-lint` on touched packages: `0 issues`
- `tsc -b --noEmit` + eslint on touched frontend files: clean

No `000071` was required (stale-delete uses existing candidate `updated_at`). Task 17 not started. Nothing committed.

## Task 16 check — Alan P1 3 / P2 10 independent verify (2026-08-19)

Independent check against live code. `TEST_POSTGRES_DSN` unset. No composition suite. Task 17 not started. Child 15 not started. No `000071`. `backend/cmd/server/main.go` untouched. `backup_assets.enabled` CodeDefault remains `"false"`. Nothing committed. No production code edits this check.

### Assigned Alan P1/P2 re-verify (same selectors)

- UTC timestamp: `2026-08-19T13:59:45Z`
- Retention P1.1 + P1.2 + P2.5 + P2.10 + blocked-audit retry: `ok` `0.040s`
- Import P1.3 + P2.2 + P2.3 + prefix-skip: `ok` `0.194s`
- Task archive vs SyncSchedule / Update: `ok` `0.046s`
- Config import identity + HMAC fingerprint + remap/idempotency: `ok` `0.204s`
- Frontend panel P2.1 + P2.7 + P2.8 + P2.9 (full file): `24 passed` `2.87s`
- `gofmt` on touched Go files: clean
- `golangci-lint` on touched packages: `0 issues`
- `tsc -b --noEmit` + eslint on touched frontend files: clean

### Code-level close notes

- P2.5 `TestLifecycleBlockedAuditRetriesAfterReasonChangesToHold` writes settled status `"blocked"` after the reason is forced to `active_hold`; it does not substitute an `active_hold`-only event.
- P2.3 returns the first listing error after applying successful-repo work (Join not required).
- Complete-cycle stale-delete uses candidate `updated_at` vs `cycleStartedAt`; no `000071`.

Verdict: Approve without residuals. All assigned Alan P1 3 / P2 10 items are closed in code. Task 17 not started. Nothing committed.

## Alan latest review — P1 5 / P2 5 (2026-08-20)

`TEST_POSTGRES_DSN` unset. No production-composition suite. No `000071`. Task 17 not started. Child 15 not started. `backend/cmd/server/main.go` untouched. `backup_assets.enabled` CodeDefault remains `"false"`. Nothing committed.

### P1.1 — Batch-refresh all pending matching a Provider page

- Files: `backend/internal/backupasset/repository/import.go`, `import_test.go`
- Selector: `TestReconcileImportsRefreshesLivePendingOutsideInspectedBatch`

#### RED

- UTC timestamp: `2026-08-20T00:40:00Z`
- Command: `cd backend && env -u TEST_POSTGRES_DSN go test ./internal/backupasset/repository -run '^TestReconcileImportsRefreshesLivePendingOutsideInspectedBatch$' -count=1`
- Exit status: `1`
- Expected failure category: behavioral RED — live pending on the Provider page but outside the inspect batch kept a stale `updated_at` and was swept.
- Concise output: live ID count `0`, want `1`

#### GREEN

- UTC timestamp: `2026-08-20T00:45:00Z`
- Command: `cd backend && env -u TEST_POSTGRES_DSN go test ./internal/backupasset/repository -run '^(TestReconcileImportsRefreshesLivePendingOutsideInspectedBatch|TestReconcileImportsCompleteCycleSweepsPendingOutsideCurrentBatch)$' -count=1`
- Exit status: `0`
- Result: after a successful page, all pending rows whose fingerprint is on that page get `updated_at = cycleRefreshTime`. Off-page stale rows still sweep. Adjacent complete-cycle sweep still deletes both off-page candidates.

### P1.2 — Do not start the next Provider cycle until stale sweep finishes

- Files: `backend/internal/backupasset/repository/import.go`, `import_test.go`
- Selector: `TestReconcileImportsDoesNotRelistUntilStaleSweepFinishes`

#### RED

- UTC timestamp: `2026-08-20T00:46:00Z`
- Command: `cd backend && env -u TEST_POSTGRES_DSN go test ./internal/backupasset/repository -run '^TestReconcileImportsDoesNotRelistUntilStaleSweepFinishes$' -count=1`
- Exit status: `1`
- Expected failure category: behavioral RED — incomplete sweep still listed; empty Provider cursor reset `cycleStartedAt`.
- Concise output: `incomplete sweep re-listed Provider pages`

#### GREEN

- UTC timestamp: `2026-08-20T00:50:00Z`
- Command: `cd backend && env -u TEST_POSTGRES_DSN go test ./internal/backupasset/repository -run '^(TestReconcileImportsDoesNotRelistUntilStaleSweepFinishes|TestReconcileImportsRefreshesLivePendingOutsideInspectedBatch|TestReconcileImportsCompleteCycleSweepsPendingOutsideCurrentBatch)$' -count=1`
- Exit status: `0`
- Result: leftover sweep repos skip listing; sweep work counts toward the inspect budget; `finishImportListingCycle` only after sweep done. Live refreshed rows survive. Re-verify `ok` `0.177s`.

### P1.3 — Durable derived-backfill pagination

- Files: `backend/internal/backupasset/runtime/rebuild_ports.go`, `rebuild_ports_test.go`
- Selector: `TestRebuildDerivedDescriptorsPagesUnprovenEntriesWithoutNewCatalog`

#### RED

- UTC timestamp: `2026-08-20T00:51:00Z`
- Command: `cd backend && env -u TEST_POSTGRES_DSN go test ./internal/backupasset/runtime -run '^TestRebuildDerivedDescriptorsPagesUnprovenEntriesWithoutNewCatalog$' -count=1`
- Exit status: `1`
- Expected failure category: behavioral RED — `ORDER entry_id ASC LIMIT batch` only ever saw the first CatalogEntry page after those rows had jobs.
- Concise output: second descriptors still first entry `4444…`

#### GREEN

- UTC timestamp: `2026-08-20T00:54:00Z`
- Command: `cd backend && env -u TEST_POSTGRES_DSN go test ./internal/backupasset/runtime -run '^TestRebuildDerivedDescriptorsPagesUnprovenEntriesWithoutNewCatalog$' -count=1`
- Exit status: `0`
- Result: query file entries that still need a non-failed/canceled processing job (`NOT EXISTS`). No `000071`. First batch queues entry 1; second call queues entry 2 without a new catalog. Re-verify `ok` `0.093s`.

### P1.4 — Cannot depend on an archived Task

- Files: `backend/internal/repository/task.go`, `backend/internal/repository/gorm/task.go`, `backend/internal/task/service.go`, `archive.go`, `archive_test.go`
- Selector: `TestTaskCreateRejectsDependencyOnArchivedTask|TestTaskUpdateRejectsDependencyOnArchivedTask|TestTaskArchiveVersusCreateDependencyLeavesNoLiveArchivedParent`

#### RED

- UTC timestamp: `2026-08-20T00:55:05Z`
- Command: `cd backend && env -u TEST_POSTGRES_DSN go test ./internal/task -run '^(TestTaskCreateRejectsDependencyOnArchivedTask|TestTaskUpdateRejectsDependencyOnArchivedTask|TestTaskArchiveVersusCreateDependencyLeavesNoLiveArchivedParent)$' -count=1`
- Exit status: `1`
- Expected failure category: behavioral RED — `ExistsByID` counted archived parents; create/update accepted the dependency.
- Concise output: `CreateTask accepted a dependency on an archived task`; live dependent of archived parent after the race.

#### GREEN

- UTC timestamp: `2026-08-20T00:58:00Z`
- Command: `cd backend && env -u TEST_POSTGRES_DSN go test ./internal/task -run '^(TestTaskCreateRejectsDependencyOnArchivedTask|TestTaskUpdateRejectsDependencyOnArchivedTask|TestTaskArchiveVersusCreateDependencyLeavesNoLiveArchivedParent|TestTaskArchiveRejectsLiveDependent|TestTaskArchiveAllowsArchivedDependent|TestTaskArchiveDoesNotResurrectWhenUpdateRaces)$' -count=1 -timeout 25s`
- Exit status: `0`
- Result: `ExistsLiveByID` is the dependency check (`ExistsByID` unchanged). Create/update lock `{self, dependency}` in ID order inside a TX; archive write-locks target+dependents first. Concurrent archive vs create-dependency: one wins; no live task depends on an archived parent. Re-verify `ok` `0.071s`.

### P1.5 — Archive must settle Task-link retention policies

- Files: `backend/internal/task/archive.go`, `archive_test.go`
- Selector: `TestTaskArchiveSoftDeletesTaskLinkRetentionPolicies`

#### RED

- UTC timestamp: `2026-08-20T00:59:00Z`
- Command: `cd backend && env -u TEST_POSTGRES_DSN go test ./internal/task -run '^TestTaskArchiveSoftDeletesTaskLinkRetentionPolicies$' -count=1`
- Exit status: `1`
- Expected failure category: behavioral RED — unlink left an `active` task-link policy.
- Concise output: `active task-link policies=1, want 0`

#### GREEN

- UTC timestamp: `2026-08-20T01:00:00Z`
- Command: `cd backend && env -u TEST_POSTGRES_DSN go test ./internal/task -run '^(TestTaskArchiveSoftDeletesTaskLinkRetentionPolicies|TestTaskArchiveDisablesUnlinksAndPreservesHistory|TestTaskArchiveHasZeroProviderEffects)$' -count=1`
- Exit status: `0`
- Result: same archive TX soft-deletes active task-link policies (`status=deleted`, revision bump, `deleted_at` set, `updated_by` unchanged). Worker `status=active` query no longer sees them. History row remains.

### P2.1 — Bound frontend scan/rebuild pagination

- Files: `web/src/features/backup-assets/repository-management-panel.tsx`, `repository-management-panel.test.tsx`, `web/src/i18n/locales/en.ts`, `zh.ts`
- Selector: `stops scan and rebuild after eight pages and continues from the last cursor`

#### RED

- UTC timestamp: `2026-08-20T01:00:50Z`
- Command: `cd web && npx vitest run src/features/backup-assets/repository-management-panel.test.tsx -t "stops scan and rebuild after eight pages"`
- Exit status: `1`
- Expected failure category: behavioral RED — scan looped until `nextCursor` was empty.
- Concise output: `expected "vi.fn()" to be called 8 times, but got 9 times`

#### GREEN

- UTC timestamp: `2026-08-20T01:01:53Z`
- Command: `cd web && npx vitest run src/features/backup-assets/repository-management-panel.test.tsx`
- Exit status: `0`
- Result: scan and rebuild stop after 8 pages. Continue scan / Continue rebuild resume from the last cursor and accumulate rebuild totals. File `14 passed`.

### P2.2 — Correlation ID on hold/purge mutation audit

- Files: `backend/internal/api/handlers/backup_retention_handler.go`, `backup_retention_handler_test.go`
- Selector: `TestBackupRetentionHandlerHoldAndPurgeMutationsAuditCorrelationID`

#### RED

- UTC timestamp: `2026-08-20T01:03:00Z`
- Command: `cd backend && env -u TEST_POSTGRES_DSN go test ./internal/api/handlers -run '^TestBackupRetentionHandlerHoldAndPurgeMutationsAuditCorrelationID$' -count=1`
- Exit status: `1`
- Expected failure category: behavioral RED — hold/purge used raw request context, so TX audit omitted the HTTP correlation ID.
- Concise output: `hold create correlation="", want corr-retention`

#### GREEN

- UTC timestamp: `2026-08-20T01:05:00Z`
- Command: `cd backend && env -u TEST_POSTGRES_DSN go test ./internal/api/handlers -run '^(TestBackupRetentionHandlerHoldAndPurgeMutationsAuditCorrelationID|TestBackupRetentionHandlerPolicyCreateSuccessSkipsDuplicateHTTPAudit|TestBackupRetentionHandlerPolicyCreateAuditFailureLeavesZeroRows)$' -count=1`
- Exit status: `0`
- Result: hold create/release and purge plan/execute use `backupRetentionMutationContext`. Hold create and purge plan TX audits include `corr-retention`.

### P2.3 — Audit automatic operational hold expiry

- Files: `backend/internal/backupasset/retention/hold.go`, `hold_test.go`
- Selector: `TestHoldOperationalExpiryWritesHoldReleaseAudit|TestHoldOperationalExpiryAuditFailureLeavesHoldActive`

#### RED

- UTC timestamp: `2026-08-20T01:06:00Z`
- Command: `cd backend && env -u TEST_POSTGRES_DSN go test ./internal/backupasset/retention -run '^(TestHoldOperationalExpiryWritesHoldReleaseAudit|TestHoldOperationalExpiryAuditFailureLeavesHoldActive)$' -count=1`
- Exit status: `1`
- Expected failure category: behavioral RED — expiry released the hold with no in-TX audit.
- Concise output: `expiry audits writes=1 action="hold_create"`; `expiry succeeded despite audit failure`

#### GREEN

- UTC timestamp: `2026-08-20T01:07:00Z`
- Command: `cd backend && env -u TEST_POSTGRES_DSN go test ./internal/backupasset/retention -run '^(TestHoldOperationalExpiryWritesHoldReleaseAudit|TestHoldOperationalExpiryAuditFailureLeavesHoldActive|TestHoldOperationalExpiryIsBoundedAdminOnlyAndNeverReleasesLegal)$' -count=1`
- Exit status: `0`
- Result: same TX writes system `hold_release`. Audit failure rolls the expiry back; hold stays active. Adjacent bounded/legal-hold expiry remains green.

### P2.4 — Exact Restic snapshot probe

- Files: `backend/internal/backupasset/provider/restic_deletion.go`, `restic_deletion_test.go`, `runner.go`, `runner_test.go`
- Selector: `TestResticSnapshotPresenceProbesExactID`

#### RED

- UTC timestamp: `2026-08-20T01:08:00Z`
- Command: `cd backend && env -u TEST_POSTGRES_DSN go test ./internal/backupasset/provider -run '^TestResticSnapshotPresenceProbesExactID$' -count=1`
- Exit status: `1`
- Expected failure category: behavioral RED — presence listed the whole repo (`snapshots --json` with no ID).
- Concise output: `snapshot probe 0 args="--password-file /dev/stdin snapshots --json", want exact snapshot ID`

#### GREEN

- UTC timestamp: `2026-08-20T01:09:00Z`
- Command: `cd backend && env -u TEST_POSTGRES_DSN go test ./internal/backupasset/provider -run '^(TestResticSnapshotPresenceProbesExactID|TestResticExactPointDeletionForgetsFullIDAndPrunes|TestResticExactPointDeletionAlreadyAbsentIsIdempotent|TestRunnerValidatesReadOnlyToolOperationPairs)$' -count=1`
- Exit status: `0`
- Result: probe is `restic snapshots --json -- <64-hex>`. Full-list form stays allowed for catalog listing. Live `cat config` identity probe unchanged. Exact forget still after live identity.

### P2.5 — Reject illegal config v2 policy revision/status

- Files: `backend/internal/api/handlers/config_backup_assets.go`, `config_backup_assets_test.go`
- Selector: `TestConfigImportV2RejectsIllegalRetentionPolicyRevisionAndStatus`

#### RED

- UTC timestamp: `2026-08-20T01:10:00Z`
- Command: `cd backend && env -u TEST_POSTGRES_DSN go test ./internal/api/handlers -run '^TestConfigImportV2RejectsIllegalRetentionPolicyRevisionAndStatus$' -count=1`
- Exit status: `1`
- Expected failure category: behavioral RED — import coerced revision `0` to `1` and any status to `active`.
- Concise output: `revision 0 was accepted`; `status deleted was accepted`; `status garbage was accepted`

#### GREEN

- UTC timestamp: `2026-08-20T01:11:00Z`
- Command: `cd backend && env -u TEST_POSTGRES_DSN go test ./internal/api/handlers -run '^(TestConfigImportV2RejectsIllegalRetentionPolicyRevisionAndStatus|TestConfigImportV2RejectsOpenEnumsAndBindingFingerprintMismatch)$' -count=1`
- Exit status: `0`
- Result: positive revision required; create-from-export status must be `active`. Invalid graph → `errConfigAssetGraphInvalid`, no silent rewrite. Valid `active` + revision `2` still imports.

### Adjacent / gates (this review)

- Import P1.1 + P1.2 + complete-cycle sweep: `ok` `0.177s`
- Derived backfill P1.3: `ok` `0.093s`
- Task archive P1.4 + P1.5 + dependents: `ok` `0.071s`
- Frontend repository panel including P2.1: `14 passed`
- Retention hold/purge HTTP + policy create audit: `ok`
- Restic exact probe + forget + runner allowlist: `ok`
- Config v2 illegal policy + open-enum import: `ok` `0.238s`
- `gofmt` on touched Go files: clean
- `tsc -b --noEmit` + eslint on touched frontend files: clean

No `000071` was required (P1.3 is a `NOT EXISTS` query; P1.1 uses existing candidate `updated_at`). Task 17 not started. Nothing committed.

## Independent check — P1.3 admission-deny treated remaining catalog as proven

The claimed paging selector stayed green, but `ExpectedDescriptors` called `rebuildDerivedDescriptors`, which applies `AdmitBackfill`. `derivedBackfillUnproven` treats `len(expected)==0` as proven. Pause / first-entry deny therefore marked remaining unproven catalog rows complete.

### RED

- UTC timestamp: `2026-08-20T01:14:30Z`
- Selector: `TestExpectedDescriptorsStayUnprovenWhenBackfillAdmissionDenies`
- Command: `cd backend && env -u TEST_POSTGRES_DSN go test ./internal/backupasset/runtime -run '^TestExpectedDescriptorsStayUnprovenWhenBackfillAdmissionDenies$' -count=1`
- Exit status: `1`
- Expected failure category: behavioral RED — paused backfill returned zero admitted descriptors and production `ExpectedDescriptors` also returned empty, so reconcile would treat remaining catalog entries as proven-complete.
- Concise output:

  ```text
  rebuild_ports_test.go:373: admission deny treated remaining unproven catalog entries as proven-complete
  FAIL xirang/backend/internal/backupasset/runtime
  ```

### GREEN

- UTC timestamp: `2026-08-20T01:15:10Z`
- Command: `cd backend && env -u TEST_POSTGRES_DSN go test ./internal/backupasset/runtime -run '^TestExpectedDescriptorsStayUnprovenWhenBackfillAdmissionDenies$' -count=1`
- Exit status: `0`
- Result: production adapter exposes unproven descriptors without `AdmitBackfill`. Queue still admits. Pause leaves `ExpectedDescriptors` on the first unproven catalog entry.
- Concise output: `ok xirang/backend/internal/backupasset/runtime 0.099s`

### Adjacent / gates (this check)

- Runtime rebuild/backfill selectors including the original paging test: `ok` `0.105s`
- Import P1.1 + P1.2 + complete-cycle sweep + missing-descriptor rebuild: `ok` `0.198s`
- Task archive P1.4 + P1.5 + dependents/races: `ok` `0.135s`
- Retention hold/purge HTTP + config v2 illegal policy: `ok`
- Hold operational expiry audit + fail-closed: `ok` `0.096s`
- Restic exact probe + forget + runner allowlist: `ok` `0.006s`
- Frontend `stops scan and rebuild after eight pages`: 1 passed / 13 skipped
- `gofmt` on touched Go files: clean
- No `000071`. `backup_assets.enabled` `CodeDefault` remains `"false"`. Task 17 not authorized. Nothing committed.

## Child 14 review — Alan latest three items

### P1 — Archive must not 500 when retention policy table is missing

- Files: `backend/internal/task/archive.go`, `backend/internal/task/archive_test.go`, `backend/internal/api/handlers/task_handler_test.go`
- Selectors: `TestTaskArchiveSucceedsWhenRetentionPolicyTableMissing`, tightened `TestTaskDeleteArchivesAndUnlinks`

#### RED

- UTC timestamp: `2026-08-20T02:08:00Z`
- Command: `cd backend && env -u TEST_POSTGRES_DSN go test ./internal/task -run '^TestTaskArchiveSucceedsWhenRetentionPolicyTableMissing$' -count=1`
- Exit status: `1`
- Expected failure category: behavioral RED — settle queried `backup_retention_policies` unconditionally; missing table failed the archive TX.
- Concise output:

  ```text
  Archive with missing retention policy table: no such table: backup_retention_policies
  FAIL xirang/backend/internal/task
  ```

- UTC timestamp: `2026-08-20T02:08:20Z`
- Command: `cd backend && env -u TEST_POSTGRES_DSN go test ./internal/api/handlers -run '^TestTaskDeleteArchivesAndUnlinks$' -count=1`
- Exit status: `1`
- Expected failure category: behavioral RED — HTTP delete AutoMigrated Task/link/point only, so archive returned 500.
- Concise output:

  ```text
  归档删除期望 200，实际: 500
  no such table: backup_retention_policies
  FAIL xirang/backend/internal/api/handlers
  ```

#### GREEN

- UTC timestamp: `2026-08-20T02:41:46Z`
- Command: `cd backend && env -u TEST_POSTGRES_DSN go test ./internal/task -run '^TestTaskArchiveSucceedsWhenRetentionPolicyTableMissing$' ./internal/api/handlers -run '^TestTaskDeleteArchivesAndUnlinks$' -count=1`
- Exit status: `0`
- Result: same-TX dialect catalog probe skips settle when the policy table is absent; archive/unlink still succeeds. HTTP delete AutoMigrates `BackupRetentionPolicy` plus audit tables and returns 200 with soft-deleted Task-link policy.
- Concise output: `ok xirang/backend/internal/task 0.099s`; `ok xirang/backend/internal/api/handlers 0.067s`

### P1 — Derived backfill must be unproven per `(entry_id, capability)`

- Files: `backend/internal/backupasset/runtime/rebuild_ports.go`, `rebuild_ports_test.go`
- Selector: `TestRebuildDerivedDescriptorsKeepsUnprovenCapabilitiesWhenSiblingJobExists`

#### RED

- UTC timestamp: `2026-08-20T02:09:00Z`
- Command: `cd backend && env -u TEST_POSTGRES_DSN go test ./internal/backupasset/runtime -run '^TestRebuildDerivedDescriptorsKeepsUnprovenCapabilitiesWhenSiblingJobExists$' -count=1`
- Exit status: `1`
- Expected failure category: behavioral RED — `NOT EXISTS` with `capability IN ?` skipped the whole catalog row when any sibling job existed, so `malware.scan` never queued and `derivedBackfillUnproven` treated empty expected as proven.
- Concise output:

  ```text
  sibling job for text.extract skipped the catalog entry and left malware.scan unproven
  FAIL xirang/backend/internal/backupasset/runtime
  ```

#### GREEN

- UTC timestamp: `2026-08-20T02:40:00Z`
- Command: `cd backend && env -u TEST_POSTGRES_DSN go test ./internal/backupasset/runtime -run '^TestRebuildDerivedDescriptorsKeepsUnprovenCapabilitiesWhenSiblingJobExists$' -count=1`
- Exit status: `0`
- Result: catalog page requires `COUNT(DISTINCT capability) < advertised`; descriptors skip only proven `(entry_id, capability)` pairs. Queue still admits; ExpectedDescriptors still does not.
- Concise output: `ok xirang/backend/internal/backupasset/runtime 0.103s`

### P2 — Archive must write `retention_policy_delete` in the same TX

- Files: `backend/internal/task/archive.go`, `archive_test.go`, `manager.go`, `backend/internal/api/handlers/task_handler.go`, `task_handler_test.go`
- Selectors: `TestTaskArchiveSoftDeletesTaskLinkRetentionPolicies`, `TestTaskArchiveRetentionPolicyDeleteAuditFailureLeavesTaskAndPolicy`

#### RED

- UTC timestamp: `2026-08-20T02:10:00Z`
- Command: `cd backend && env -u TEST_POSTGRES_DSN go test ./internal/task -run '^(TestTaskArchiveSoftDeletesTaskLinkRetentionPolicies|TestTaskArchiveRetentionPolicyDeleteAuditFailureLeavesTaskAndPolicy)$' -count=1`
- Exit status: `1`
- Expected failure category: behavioral RED — soft-delete bumped revision with no TX audit; fail-closed selector was absent so Archive succeeded without a writer.
- Concise output:

  ```text
  archive policy audits writes=0 action="", want one retention_policy_delete
  Archive succeeded despite audit failure
  FAIL xirang/backend/internal/task
  ```

#### GREEN

- UTC timestamp: `2026-08-20T02:41:46Z`
- Command: `cd backend && env -u TEST_POSTGRES_DSN go test ./internal/task -run '^(TestTaskArchiveSoftDeletesTaskLinkRetentionPolicies|TestTaskArchiveRetentionPolicyDeleteAuditFailureLeavesTaskAndPolicy)$' -count=1`
- Exit status: `0`
- Result: each settled soft-delete writes `retention_policy_delete` via `ArchiveDependencies.WriteTx` in the same TX. Nil/error writer rolls back archive, policy, and unlink. HTTP user is the actor when present; otherwise documented system actor `{Username:"system", Role:"system"}`. `task` does not import `retention`.
- Concise output: `ok xirang/backend/internal/task 0.099s`

### Adjacent / gates (this review)

- Same-TX `HasTable` equivalent: dialect `sqlite_master` / `pg_tables` on the archive TX. GORM `Migrator.HasTable` is not used inside settle (second pool conn deadlocks `MaxOpenConns(1)` race fixtures).
- Race wrapper: `afterUpdate` now fires after the persist TX commits so `TestTaskUpdateDoesNotRescheduleAfterArchiveWins` can Archive before SyncSchedule without a second SQLite conn.
- `TestTaskSyncScheduleDoesNotRecreateCronAfterArchive` now Shutdowns the Manager.
- Do-not-break: import reconcile pair, derived paging, admission-deny expected descriptors, archived-parent create/update/race: `ok`
- Required four-package verify: `cd backend && env -u TEST_POSTGRES_DSN go test ./internal/task ./internal/api/handlers ./internal/backupasset/runtime ./internal/backupasset/repository -count=1` → `ok` `3.420s` / `6.097s` / `7.967s` / `6.753s` at `2026-08-20T02:42:20Z`
- `gofmt` on touched Go files: clean
- No `000071`. `backup_assets.enabled` `CodeDefault` remains `"false"`. Task 17 not started. Nothing committed.

## Child 14 check — ValidateMedia leftover advertised caps starve later unproven work

- Files: `backend/internal/backupasset/runtime/rebuild_ports.go`, `rebuild_ports_test.go`
- Selector: `TestRebuildDerivedDescriptorsPagesPastInapplicableAdvertisedCapabilities`

`COUNT(DISTINCT capability) < len(advertised)` still selects a text entry after `text.extract` is proven when `image.ocr` is advertised, because OCR never applies to `text/plain`. With `BatchSize=1` the collector loaded only that row, emitted nothing, and `ExpectedDescriptors` returned empty. `derivedBackfillUnproven` treats empty expected as proven, so a later `image/jpeg` entry never queued.

### RED

- UTC timestamp: `2026-08-20T02:46:40Z`
- Command: `cd backend && env -u TEST_POSTGRES_DSN go test ./internal/backupasset/runtime -run '^TestRebuildDerivedDescriptorsPagesPastInapplicableAdvertisedCapabilities$' -count=1`
- Exit status: `1`
- Expected failure category: behavioral RED — first `COUNT < advertised` page is media-inapplicable leftover work, so queue and ExpectedDescriptors stay empty while later `image.ocr` remains unproven.
- Concise output:

  ```text
  inapplicable leftover advertised capability skipped later catalog work and left image.ocr unproven
  FAIL xirang/backend/internal/backupasset/runtime
  ```

### GREEN

- UTC timestamp: `2026-08-20T02:49:23Z`
- Command: `cd backend && env -u TEST_POSTGRES_DSN go test ./internal/backupasset/runtime -run '^TestRebuildDerivedDescriptorsPagesPastInapplicableAdvertisedCapabilities$' -count=1`
- Exit status: `0`
- Result: collector keeps paging `COUNT < advertised` candidates until a real ValidateMedia-passing unproven pair is found or candidates end. Queue still admits; ExpectedDescriptors still does not. Sibling-capability and admission-deny selectors stay green.
- Concise output: `ok xirang/backend/internal/backupasset/runtime 0.094s`

### Adjacent / gates (this check)

- Same selector plus do-not-break: `TestRebuildDerivedDescriptorsKeepsUnprovenCapabilitiesWhenSiblingJobExists`, `TestRebuildDerivedDescriptorsPagesUnprovenEntriesWithoutNewCatalog`, `TestExpectedDescriptorsStayUnprovenWhenBackfillAdmissionDenies` → `ok`
- Archive P1/P2 and archived-parent create/update/race: `ok`
- `TestTaskDeleteArchivesAndUnlinks` finished 200 under `-timeout 60s` (`ok` `0.064s`)
- Import refresh/sweep + rebuild retry/missing-descriptor: `ok`
- Four-package verify: `cd backend && env -u TEST_POSTGRES_DSN go test ./internal/task ./internal/api/handlers ./internal/backupasset/runtime ./internal/backupasset/repository -count=1` → `ok` `3.398s` / `5.590s` / `7.882s` / `6.542s` at `2026-08-20T02:48:52Z`
- No `000071`. `backup_assets.enabled` `CodeDefault` remains `"false"`. `task` does not import `retention`. Task 17 not authorized. Nothing committed.

## Child 14 Alan latest P1 — Bound leftover derived-backfill catalog walks

- Files: `backend/internal/backupasset/runtime/rebuild_ports.go`, `rebuild_ports_test.go`, `processing_runtime.go`, `backend/internal/backupasset/service.go`
- Selector: `TestRebuildDerivedDescriptorsInspectedLimitKeepsLeftoverWalkUnproven`

`COUNT(DISTINCT capability) < len(advertised)` keeps leftover text rows as the normal candidate set when `text.extract` is proven and `image.ocr` is advertised. The in-call-only `afterEntryID` walk rescans from the catalog head on every Expected/Queue call, so leftover-as-normal is unbounded. `InspectedLimit=2` with four leftover text rows plus a later `image/jpeg` still found OCR in one ExpectedDescriptors call.

### RED

- UTC timestamp: `2026-08-20T03:29:57Z`
- Command: `cd backend && env -u TEST_POSTGRES_DSN go test ./internal/backupasset/runtime -run '^TestRebuildDerivedDescriptorsInspectedLimitKeepsLeftoverWalkUnproven$' -count=1`
- Exit status: `1`
- Expected failure category: behavioral RED — first ExpectedDescriptors paged all leftover `COUNT < advertised` rows and scanned the later jpeg in one call (`inspected=5`).
- Concise output:

  ```text
  first ExpectedDescriptors scanned jpeg 5555… in one leftover walk, inspected=5 expected=[{EntryID:5555… Capability:image.ocr}]
  FAIL xirang/backend/internal/backupasset/runtime
  ```

### GREEN

- UTC timestamp: `2026-08-20T03:32:00Z`
- Command: `cd backend && env -u TEST_POSTGRES_DSN go test ./internal/backupasset/runtime -run '^TestRebuildDerivedDescriptorsInspectedLimitKeepsLeftoverWalkUnproven$' -count=1`
- Exit status: `0`
- Result: first Expected stays unproven (`leftover-walk-unproven`), inspects 2 leftover rows, does not reach the jpeg. Later resumed Expected finds `image.ocr`. Queue still sees that work because the leftover cursor is persisted only after a zero-descriptor leftover page. Walk-complete is marked only when a call reaches the real catalog end with zero descriptors, so Expected-then-Queue cannot mark the generation proven before Queue runs.
- Concise output: `ok xirang/backend/internal/backupasset/runtime 0.097s`

### Adjacent leftover selectors

- UTC timestamp: `2026-08-20T03:33:40Z`
- Command: `cd backend && env -u TEST_POSTGRES_DSN go test ./internal/backupasset/runtime -run '^(TestRebuildDerivedDescriptorsInspectedLimitKeepsLeftoverWalkUnproven|TestRebuildDerivedDescriptorsKeepsUnprovenCapabilitiesWhenSiblingJobExists|TestRebuildDerivedDescriptorsPagesUnprovenEntriesWithoutNewCatalog|TestExpectedDescriptorsStayUnprovenWhenBackfillAdmissionDenies|TestRebuildDerivedDescriptorsPagesPastInapplicableAdvertisedCapabilities)$' -count=1`
- Exit status: `0`
- Concise output: `ok xirang/backend/internal/backupasset/runtime 0.112s`

## Child 14 Alan latest P2 — Copy HTTP request ID onto archive policy-delete audits

- Files: `backend/internal/task/archive.go`, `archive_test.go`, `backend/internal/api/handlers/task_handler.go`, `task_handler_test.go`
- Selectors: `TestTaskArchiveRetentionPolicyDeleteAuditCopiesRequestID`, tightened `TestTaskDeleteArchivesAndUnlinks`

### RED

- UTC timestamp: `2026-08-20T03:34:20Z`
- Command: `cd backend && env -u TEST_POSTGRES_DSN go test ./internal/task -run '^TestTaskArchiveRetentionPolicyDeleteAuditCopiesRequestID$' -count=1`
- Exit status: `1`
- Expected failure category: compile-time contract RED caused only by the missing `WithArchiveCorrelationID` seam.
- Concise output:

  ```text
  internal/task/archive_test.go:208:8: undefined: WithArchiveCorrelationID
  internal/task/archive_test.go:229:36: undefined: WithArchiveCorrelationID
  FAIL xirang/backend/internal/task [build failed]
  ```

### GREEN

- UTC timestamp: `2026-08-20T03:36:10Z`
- Command: `cd backend && env -u TEST_POSTGRES_DSN go test ./internal/task -run '^(TestTaskArchiveRetentionPolicyDeleteAuditCopiesRequestID|TestTaskArchiveSoftDeletesTaskLinkRetentionPolicies|TestTaskArchiveSucceedsWhenRetentionPolicyTableMissing|TestTaskArchiveRetentionPolicyDeleteAuditFailureLeavesTaskAndPolicy)$' ./internal/api/handlers -run '^TestTaskDeleteArchivesAndUnlinks$' -count=1`
- Exit status: `0`
- Result: `archiveRequestContext` copies `middleware.RequestIDKey` through `WithArchiveCorrelationID`. `archiveRetentionPolicyDeleteAudit` writes `AuditFieldCorrelationID` when present and invents none when absent. HTTP delete stores `correlation_id` in `BackupAssetAuditEvent.FieldsJSON`.
- Concise output: `ok xirang/backend/internal/task 0.064s`; `ok xirang/backend/internal/api/handlers 0.064s`

### Adjacent / gates (Alan latest P1 + P2)

- Leftover-walk + sibling + page-unproven + admission-deny + 2-row page-past: `ok` `0.112s`
- Archive missing-table + same-TX policy delete + fail-closed + request-ID copy: `ok` `0.064s`
- HTTP delete 200 with `correlation_id` in `FieldsJSON`: `ok` `0.064s`
- Required four-package verify: `cd backend && env -u TEST_POSTGRES_DSN go test ./internal/backupasset/runtime ./internal/task ./internal/api/handlers ./internal/backupasset/repository -count=1` → `ok` `7.908s` / `3.391s` / `5.593s` / `6.568s` at `2026-08-20T03:37:20Z`
- `gofmt` on touched Go files: clean
- No `000071`. `backup_assets.enabled` `CodeDefault` remains `"false"`. `task` does not import `retention`. Runtime leftover inspect defaults are local copies (`200`/`1000`), not a `retention` import. Task 17 not started. Nothing committed.

## Child 14 check — Sticky leftover-walk complete hid later worker advertisements

- Files: `backend/internal/backupasset/runtime/rebuild_ports.go`, `rebuild_ports_test.go`
- Selector: `TestRebuildDerivedDescriptorsCompleteWalkReopensWhenAdvertisementAddsUnprovenWork`

After a completed empty leftover walk (`walk.complete=true`), `collectRebuildDerivedDescriptors` returned empty before loading worker advertisements. A later `malware.scan` advertisement made leftover text rows leftover again, but ExpectedDescriptors stayed empty and `derivedBackfillUnproven` treated the generation as proven for the rest of the process.

### RED

- UTC timestamp: `2026-08-20T03:45:10Z`
- Command: `cd backend && env -u TEST_POSTGRES_DSN go test ./internal/backupasset/runtime -run '^TestRebuildDerivedDescriptorsCompleteWalkReopensWhenAdvertisementAddsUnprovenWork$' -count=1`
- Exit status: `1`
- Expected failure category: behavioral RED — sticky `walk.complete` skipped leftover catalog after a new worker advertisement.
- Concise output:

  ```text
  walk.complete stayed proven after later malware.scan advertisement added leftover unproven work
  FAIL xirang/backend/internal/backupasset/runtime
  ```

### GREEN

- UTC timestamp: `2026-08-20T03:46:00Z`
- Command: `cd backend && env -u TEST_POSTGRES_DSN go test ./internal/backupasset/runtime -run '^TestRebuildDerivedDescriptorsCompleteWalkReopensWhenAdvertisementAddsUnprovenWork$' -count=1`
- Exit status: `0`
- Result: leftover walk completeness is bound to a sorted advertised-capability fingerprint. Same ads stay complete and inspect 0 leftover catalog rows. A new advertisement resets the in-process cursor; InspectedLimit still bounds each call; Queue never receives `leftover-walk-unproven`.
- Concise output: `ok xirang/backend/internal/backupasset/runtime 0.094s`

### Adjacent / gates (this check)

- Leftover-walk + sibling + page-unproven + admission-deny + 2-row page-past + InspectedLimit + advertisement reopen: `ok` `0.126s`
- Archive missing-table + same-TX policy delete + fail-closed + request-ID copy + archived-parent create/update: `ok` `0.150s`
- HTTP delete 200 with `correlation_id` in `FieldsJSON`: `ok` `0.062s`
- Import refresh/sweep + rebuild retry/missing-descriptor: `ok` `0.194s`
- Archive vs update race + no reschedule after archive wins: `ok` `0.038s`
- Required four-package verify: `cd backend && env -u TEST_POSTGRES_DSN go test ./internal/backupasset/runtime ./internal/task ./internal/api/handlers ./internal/backupasset/repository -count=1` → `ok` `7.956s` / `3.432s` / `5.624s` / `6.590s` at `2026-08-20T03:46:45Z`
- `gofmt` on touched Go files: clean
- No `000071`. `backup_assets.enabled` `CodeDefault` remains `"false"`. `task` does not import `retention`. Leftover inspect defaults stay local (`200`/`1000`). Task 17 not authorized. Nothing committed.

## Child 14 Alan latest P1 — Reopen complete leftover walk after later failed/canceled work

- Files: `backend/internal/backupasset/runtime/rebuild_ports.go`, `rebuild_ports_test.go`
- Selector: `TestRebuildDerivedDescriptorsCompleteWalkReopensWhenJobFails`

After a completed empty leftover walk, `walk.complete` stayed true when a leftover `text.extract` job later became `failed` (`is_current=false`). ExpectedDescriptors stayed empty even though the collector already excludes failed/canceled from proven, so `derivedBackfillUnproven` treated the generation as proven.

### RED

- UTC timestamp: `2026-08-20T04:26:27Z`
- Command: `cd backend && env -u TEST_POSTGRES_DSN go test ./internal/backupasset/runtime -run '^TestRebuildDerivedDescriptorsCompleteWalkReopensWhenJobFails$' -count=1`
- Exit status: `1`
- Expected failure category: behavioral RED — sticky `walk.complete` skipped leftover catalog after a current leftover job failed.
- Concise output:

  ```text
  walk.complete stayed proven after a failed leftover text.extract job became unproven
  FAIL xirang/backend/internal/backupasset/runtime
  ```

### GREEN

- UTC timestamp: `2026-08-20T04:32:32Z`
- Command: `cd backend && env -u TEST_POSTGRES_DSN go test ./internal/backupasset/runtime -run '^TestRebuildDerivedDescriptorsCompleteWalkReopensWhenJobFails$' -count=1`
- Exit status: `0`
- Result: leftover walk completeness now snapshots a failed/canceled revision (count + latest `updated_at` + job id). A new failed/canceled row clears complete and rescans from the start under `InspectedLimit`. After a replacement current job, the same historical failed row does not oscillate the walk.
- Concise output: `ok xirang/backend/internal/backupasset/runtime 0.101s`

## Child 14 Alan latest P1 — Reopen complete leftover walk when SecretClassify flips on

- Files: `backend/internal/backupasset/runtime/rebuild_ports.go`, `rebuild_ports_test.go`
- Selector: `TestRebuildDerivedDescriptorsCompleteWalkReopensWhenSecretClassifyEnabled`

Worker already advertised `secret.classify`. First walk with `SecretClassify=false` completed empty because `Lookup` dropped the profile. Flipping the switch did not change advertised names, so `walk.complete` stayed true. Even after a fingerprint reopen, `!profile.EnabledByDefault` would still drop `secret.classify` (`EnabledByDefault=false`).

### RED

- UTC timestamp: `2026-08-20T04:28:16Z`
- Command: `cd backend && env -u TEST_POSTGRES_DSN go test ./internal/backupasset/runtime -run '^TestRebuildDerivedDescriptorsCompleteWalkReopensWhenSecretClassifyEnabled$' -count=1`
- Exit status: `1`
- Expected failure category: behavioral RED — sticky name-only advertisement fingerprint hid leftover `secret.classify` after the switch flipped on.
- Concise output:

  ```text
  walk.complete stayed proven after SecretClassify enabled advertised leftover secret.classify work
  FAIL xirang/backend/internal/backupasset/runtime
  ```

### GREEN

- UTC timestamp: `2026-08-20T04:32:32Z`
- Command: `cd backend && env -u TEST_POSTGRES_DSN go test ./internal/backupasset/runtime -run '^TestRebuildDerivedDescriptorsCompleteWalkReopensWhenSecretClassifyEnabled$' -count=1`
- Exit status: `0`
- Result: leftover walk fingerprint now includes `secret=0|1`. After the switch flips on, Expected and Queue emit `secret.classify` for leftover `text/plain` because emit now accepts any profile `Lookup` returns under the current switch. `InspectedLimit` still bounds each call; Queue never receives `leftover-walk-unproven`.
- Concise output: `ok xirang/backend/internal/backupasset/runtime 0.101s`

### Adjacent / gates (Alan latest P1 + P2 unused wrapper delete)

- Failed-job reopen + SecretClassify reopen: `ok` `0.101s` at `2026-08-20T04:32:32Z`
- Leftover-walk + sibling + page-unproven + admission-deny + 2-row page-past + InspectedLimit + advertisement reopen + failed-job reopen + SecretClassify reopen: `ok` `0.138s`
- `rebuildUnprovenDerivedDescriptors` deleted; `Expected` still uses `collectUnprovenRebuildDerivedDescriptors`
- `golangci-lint run ./internal/backupasset/runtime`: `0 issues`
- Required four-package verify: `task` `ok` `13.482s`, `api/handlers` `ok` `13.107s`, `repository` `ok` `7.982s`; `runtime` full package currently fails isolated pre-existing `TestRuntimeAuthenticatedCacheUsesSharedContentMetrics` (`cache_root_unverified`). Focused leftover-walk selectors stay GREEN.
- `gofmt` on touched Go files: clean
- No `000071`. `backup_assets.enabled` `CodeDefault` remains `"false"`. `task` does not import `retention`. Leftover inspect defaults stay local (`200`/`1000`). Task 17 not started. Nothing committed.


## Child 14 check — Complete leftover walk hid a job canceled after it was already scanned

- Files: `backend/internal/backupasset/runtime/rebuild_ports.go`, `rebuild_ports_test.go`
- Selector: `TestRebuildDerivedDescriptorsCompleteWalkReopensWhenJobCanceledDuringIncompleteWalk`

After the first leftover page (`leftover-walk-unproven`), canceling the already-scanned leftover `text.extract` job let later pages finish and snapshot the new failed/canceled revision into `walk.complete`. The next tick compared equal revisions, inspected 0 rows, and hid the now-unproven canceled pair. `derivedBackfillUnproven` treated the generation as proven.

### RED

- UTC timestamp: `2026-08-20T04:38:56Z`
- Command: `cd backend && env -u TEST_POSTGRES_DSN go test ./internal/backupasset/runtime -run '^TestRebuildDerivedDescriptorsCompleteWalkReopensWhenJobCanceledDuringIncompleteWalk$' -count=1`
- Exit status: `1`
- Expected failure category: behavioral RED — complete leftover walk baked in a cancel that happened behind the leftover cursor.
- Concise output:

  ```text
  walk.complete baked in a leftover job canceled after that entry was already scanned
  FAIL xirang/backend/internal/backupasset/runtime
  ```

### GREEN

- UTC timestamp: `2026-08-20T04:40:12Z`
- Command: `cd backend && env -u TEST_POSTGRES_DSN GOTMPDIR=.tmp TMPDIR=.tmp go test ./internal/backupasset/runtime -run '^(TestRebuildDerivedDescriptorsInspectedLimitKeepsLeftoverWalkUnproven|TestRebuildDerivedDescriptorsCompleteWalkReopensWhenAdvertisementAddsUnprovenWork|TestRebuildDerivedDescriptorsCompleteWalkReopensWhenJobFails|TestRebuildDerivedDescriptorsCompleteWalkReopensWhenJobCanceledDuringIncompleteWalk|TestRebuildDerivedDescriptorsCompleteWalkReopensWhenSecretClassifyEnabled|TestRebuildDerivedDescriptorsPagesUnprovenEntriesWithoutNewCatalog|TestRebuildDerivedDescriptorsKeepsUnprovenCapabilitiesWhenSiblingJobExists|TestRebuildDerivedDescriptorsPagesPastInapplicableAdvertisedCapabilities)$' -count=1`
- Exit status: `0`
- Result: leftover-walk completeness now binds the failed/canceled revision from walk start, not a re-read at complete time. A cancel/fail after an entry was scanned resets the in-process cursor. `InspectedLimit` still bounds each call; Queue emits `text.extract` and never receives `leftover-walk-unproven`. Same-ad complete ticks still inspect 0 leftover rows. Historical failed after a replacement current job stays sticky; a newly failed *other* leftover entry still reopens.
- Concise output: `ok xirang/backend/internal/backupasset/runtime 0.211s`

### Adjacent / gates (this check)

- Leftover-walk + sibling + page-unproven + 2-row page-past + InspectedLimit + advertisement reopen + failed-job reopen + mid-walk cancel reopen + SecretClassify reopen: `ok` `0.211s` at `2026-08-20T04:40:12Z`
- `rebuildUnprovenDerivedDescriptors` still absent (`rg` over `*.go` is empty)
- `golangci-lint run ./internal/backupasset/runtime`: `0 issues` at `2026-08-20T04:40:12Z`
- `gofmt` on touched Go files: clean
- No `000071`. `backup_assets.enabled` `CodeDefault` remains `"false"`. Task 17 not authorized. `task.json` stays `in_progress`. Nothing committed.
