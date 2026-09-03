# Database Guidelines

> Database patterns and conventions for this project.

---

## Overview

The backend uses GORM with SQLite as the default database and PostgreSQL as a
supported production option. Database opening and pool tuning live in
`backend/internal/database/database.go`; schema migrations are embedded and run
through `golang-migrate` in `backend/internal/database/migrator.go`.

Models are centralized in `backend/internal/model/models.go`. Current code
leans on GORM model tags for sizes, indexes, defaults, JSON field names, and
hooks. Sensitive fields are encrypted/decrypted through model hooks and
`backend/internal/secure/crypto.go`.

---

## Query Patterns

- Prefer GORM queries with explicit error checks. Handler code should return
  `respondInternalError(c, err)` for DB failures and map missing records to
  `respondNotFound` when appropriate.
- Use `WithContext(ctx)` in service/domain packages that receive a context, as
  in `backend/internal/dashboards/service.go`. Handler-only legacy code often
  uses `h.db` directly; do not introduce more direct context-free service code
  when a request context is available.
- Use `Preload` deliberately for response graphs. Examples:
  `NodeHandler.List` preloads `SSHKey` before sanitizing nodes, and task
  handlers preload `Node`, `Policy`, and related execution records when needed.
- Use transactions for multi-row or multi-table changes. Examples:
  `dashboards.Service.Delete`, node batch deletion, and config import all group
  dependent writes.
- For settings lookups, follow `settings.Service.resolveValue`: use
  `Limit(1).Find` when an empty result is not exceptional, to avoid noisy GORM
  `record not found` logs.

---

## Migrations

- Add paired migration files for both database engines:
  `backend/internal/database/migrations/sqlite/<version>_<name>.up.sql`,
  `.down.sql`, and the matching `postgres/` files.
- Keep version numbers in lockstep across SQLite and PostgreSQL. The current latest migration is `000076_provider_native_version_reference_reason`.
- Prefer plain SQL migrations over `AutoMigrate`. `RunMigrations` embeds the SQL files and executes them at startup.
- Make migrations safe for existing installations. Use `IF EXISTS` or
  `IF NOT EXISTS` where the engine supports it, and write comments when a
  migration is cleaning up historical drift.
- Only add pre-migration Go fixups for historical schema drift that cannot be
  expressed safely in SQL. Existing example: `fixupLegacyPolicyBwlimit` in
  `backend/internal/database/migrator.go`.
- `RunMigrations` rejects every dirty version before fixups, regardless of
  legacy environment values. The service process must not call migration
  `Force`, auto-clean metadata, or retry over a dirty version.
- For a clean version at or beyond 000069, validate the minimum Recovery schema
  before fixups and validate it again after `Up`. At 000072 or newer, also
  validate the final TaskRun compatibility triggers and PostgreSQL constraint.
  At 000073 or newer, validate both the exact plain-text grant constraints and
  the semantics of the downgrade-admission trigger/function. At 000074 or newer,
  also validate the durable Drill columns, active-run partial unique index, and
  downgrade-admission trigger/function; a same-named no-op database object is
  schema drift, not startup authorization.
  Missing objects are typed, sanitized schema drift and never authorization for
  forward writes.

---

## Naming Conventions

- Tables and columns use snake_case. GORM defaults are acceptable when they
  produce the intended name.
- Add explicit `gorm:"column:..."` tags when the historical database contract
  differs from the Go field name. Example: `Policy.BwLimit` maps to `bwlimit`,
  not `bw_limit`.
- Index names are descriptive and table-oriented, for example
  `idx_node_logs_node_created` and `idx_alerts_dedup`.
- JSON names on models also use snake_case and are part of the API contract.
  Keep frontend API mappers in sync when adding or renaming fields.

---

## Durable Schema Contracts

- Managed SSH key least-privilege metadata lives on `ssh_keys` as nullable or
  permissive-by-default fields: `disabled BOOLEAN/INTEGER NOT NULL DEFAULT
  false`, `expires_at` nullable timestamp, and text list fields
  `allowed_purposes`, `allowed_node_ids`, and `allowed_node_tags` with empty
  string defaults. Empty scope fields are a compatibility contract meaning
  unrestricted for that dimension.
- SSH key list fields are stored as normalized comma-separated text because the
  scope checks run in Go against a specific key/node/purpose. Do not use SQL
  substring matching as the source of truth for authorization decisions.
- `credential_audit_events` is the domain-specific credential-use event table.
  It stores safe actor/resource identifiers, action/purpose labels, outcome,
  sanitized error text, and sanitized JSON metadata; it must not store raw
  credentials, decrypted executor config, terminal streams, command output, or
  file contents.
- `credential_audit_events.metadata` is text JSON on both SQLite and PostgreSQL
  for cross-engine parity. Add explicit columns and indexes for fields that need
  filtering instead of relying on database-specific JSON querying.
- Config export/import must preserve SSH key scope columns (`disabled`,
  `expires_at`, `allowed_purposes`, `allowed_node_ids`, `allowed_node_tags`) even
  when `include_secrets=false`; `private_key` remains secret-only export data.
- `policies.pre_hook` and `policies.post_hook` are secret-bearing command
  fields. They are encrypted/decrypted by `model.Policy` hooks, hidden from
  non-admin policy responses, and must not be overwritten by non-admin update
  requests whose forms received hidden/empty hook fields.
- Built-in application-profile hooks for policies are rendered at task runtime
  from encrypted app credentials. Do not persist generated credential-bearing
  hook commands in `policies.pre_hook` or `policies.post_hook`; only admin-supplied
  manual hook overrides belong in those columns.
- Sensitive runtime settings such as `smtp.password` and
  `metrics.remote_bearer_token` are encrypted by `settings.Service` before
  persistence and decrypted at the service boundary for `GetEffective` /
  `GetAll`. New secret-like settings must be added to the service registry with
  `Sensitive: true` and to the v1-to-v2 encryption migration allowlist.
- Overview traffic queries rely on paired `task_runs` indexes
  `idx_task_runs_started_at` and `idx_task_runs_status_finished_at`. Preserve
  both SQLite/PostgreSQL definitions and matching down migrations when changing
  traffic-window predicates or index names.
- Backup-asset schema changes are paired across SQLite and PostgreSQL. The
  current baseline includes `000062` through
  `000076_provider_native_version_reference_reason`;
  later versions must remain paired. After durable Search or publication facts,
  or live content-delivery state exists, schema down must fail closed rather
  than deleting history, Provider facts, grants, reservations, or leases.
- `task_runs.node_id_snapshot` has a closed product contract. Ordinary TaskRun
  writes must freeze a positive node ID matching the live Task at creation;
  `task_id` and the snapshot are immutable. Snapshot `0` is not authority: it is
  the migration-owned `legacy_unknown` sentinel only for retained terminal
  orphan history in `success`, `failed`, `canceled`, `warning`, or `skipped`.
  Its status is immutable, executable consumers reject it explicitly, and
  active/unknown orphan rows fail migration atomically.
- Paired 000072 up converges repaired pre-69 upgrades and already-clean 69-71
  upgrades on those semantics. A used 000072 down must reject through
  `schema_migrations` admission before version mutation whenever any
  `legacy_unknown` row exists; pristine down alone may return cleanly to 000071.
- Paired 000073 adds exactly the `plain_text` / `text_v2` / `range_policy=none`
  delivery-grant product for the existing `safe_preview_v1` action. It must not
  relax renderer/profile, action/range, classification, truncation, step-up,
  audit, or budget products. SQLite rebuilds must preserve every grant/request
  row, foreign key, index, trigger, and unrelated constraint; PostgreSQL must
  replace only the four named renderer/profile product constraints inside one
  transaction. Both direct down and migration-metadata admission must reject
  while any grant uses either 000073-only value.
- Paired 000074 adds durable Drill ownership/lease fields and a partial unique
  index that permits at most one `pending|running|retrying` Drill TaskRun per
  task. Duplicate historical active rows fail migration cleanly before dirty
  metadata is written; after TaskRun/Evidence are repaired together, the same
  migration is directly retryable. Used down is protected by metadata admission
  and preserves version 74 clean.
- Paired 000075 adds durable native Rclone exact-version evidence for each
  recovery point. Current locators carry aggregate owned/reference counts and
  digests; legacy v1 locators remain readable as one exact-version set that is
  both owned and referenced. Loaders and deletion guards must fail closed on
  malformed or mixed locator formats rather than treating absent table rows as
  proof that no references exist.
- A 000075 down migration is allowed only when the evidence table is empty.
  Both direct down and `schema_migrations` downgrade admission reject used
  downs atomically, preserving the migration version, evidence rows, and table.
- Paired 000076 adds `provider_native_version_referenced` to the lifecycle
  blocked-reason product so a proven pre-effect native-version dependency can
  retry without reserving later publication. SQLite rebuilds only
  `recovery_point_lifecycle_attempts` and must preserve its rows, checks,
  foreign keys, indexes, and the 000070 downgrade-admission trigger;
  PostgreSQL replaces only the named blocked-reason check. Both direct down and
  `schema_migrations` admission reject used downs atomically and preserve clean
  version 76.

## Scenario: Rclone Native Version Evidence And Deletion Reservation

### 1. Scope / Trigger

- Trigger: changing rclone native publication, lifecycle delete, deletion
  reservation, or paired migrations `000075` / `000076`.
- Applies to `recovery_point_rclone_native_versions`, lifecycle blocked
  reasons, and repository-scoped publication reservation.

### 2. Signatures

- Persist/load: `persistManagedRcloneNativeVersionEvidenceTx`,
  `loadManagedRcloneNativeVersionEvidenceTx`.
- Intersection: `rejectManagedRcloneNativeReferenceIntersectionTx`.
- Reservation: `rejectManagedRcloneNativeDeletionReservationTx`.
- Provider errors: `ErrDeletePointNativeVersionReferenced`,
  `ErrDeletePointIdentityConflict`.
- Schema: paired SQLite/PostgreSQL `000075_rclone_native_version_evidence` and
  `000076_provider_native_version_reference_reason`.

### 3. Contracts

- Current locators store aggregate owned/reference counts and digests. Exact
  versions live in `recovery_point_rclone_native_versions` as HMAC identities.
  Legacy v1 locators remain readable as one set that is both owned and
  referenced.
- Deletion may delete only the owned set. A live sibling whose reference set
  intersects that owned set must block as
  `provider_native_version_referenced`.
- An in-flight `preparing` sibling has no durable evidence yet. Intersection
  must still wait with `ErrDeletePointNativeVersionReferenced`, never
  `ErrDeletePointIdentityConflict`.
- `provider_native_version_referenced` is a pre-effect dependency wait and
  must stay off the deletion-reservation blacklist so the preparing
  publication can still `RecordProviderCommit`. `identity_conflict` and
  unproven provider-delete blocks remain reserving.
- Loaders and guards fail closed on malformed or mixed locators. Absent
  evidence rows are not proof that no references exist.
- Used 000075/000076 downs fail closed through metadata admission.

### 4. Validation & Error Matrix

| Condition | Expected result |
|---|---|
| Sibling `preparing` during delete of an older point | `ErrDeletePointNativeVersionReferenced`; blocked reason `provider_native_version_referenced`; no reservation. |
| Sibling committed/verifying whose references include this owned version | Same referenced wait; no reservation. |
| Missing, mixed, or HMAC-drifted sibling evidence | `ErrDeletePointIdentityConflict`; reservation occupied. |
| In-flight provider delete or unproven blocked delete | Reservation occupied; later `Prepare` / `RecordProviderCommit` conflict. |
| Used 000075 or 000076 `Steps(-1)` | Error; version stays clean and evidence/reason rows are unchanged. |

### 5. Good/Base/Bad Cases

- Good: point B is `preparing`, delete of A waits as referenced, B still
  commits, and only a later real reference keeps A blocked.
- Base: no other live native points; delete proceeds with owned-only access.
- Bad: classify `preparing` as `identity_conflict` so A's reservation blocks
  B's commit and both stay stuck.

### 6. Tests Required

- Combination: B already prepared → delete A returns referenced, not identity
  conflict → persist `blocked` + `provider_native_version_referenced` → B
  `RecordProviderCommit` reaches `verifying`.
- Reservation table: referenced blocked reason does not conflict `Prepare`;
  `identity_conflict` does.
- Paired 000075/000076 used-down fail-closed on SQLite and required
  PostgreSQL.

### 7. Wrong vs Correct

Wrong:

```go
if otherPoint.State == string(backupasset.RecoveryPointPreparing) {
    return lifecycleDeleteIdentityConflict("publication is preparing")
}
```

Correct:

```go
if otherPoint.State == string(backupasset.RecoveryPointPreparing) {
    return fmt.Errorf("%w: managed Rclone native publication is preparing",
        provider.ErrDeletePointNativeVersionReferenced)
}
```

## Scenario: TaskRun Snapshot Compatibility and Startup Drift

### 1. Scope / Trigger

- Applies when changing `task_runs`, node deletion, task execution/recovery
  authority, or startup migration handling at versions 000069 and newer.
- Trigger this scenario for both fresh databases and upgrades crossing 000069
  or 000072 on SQLite and PostgreSQL.

### 2. Signatures

- Startup boundary: `RunMigrations(*gorm.DB, dbType string) error`.
- Product boundary: `TaskRun.NodeIDSnapshot`,
  `TaskRunActiveStatuses()`, and `IsTaskRunNodeSnapshotAuthoritative(uint)`.
- Typed failures: `ErrMigrationDirty` and `ErrMigrationSchemaDrift`.

### 3. Contracts

- Ordinary TaskRun creation freezes the live Task's positive node ID; later
  execution, restore, drill, publication, and admission queries require the
  matching positive snapshot and the expected Task ID/status.
- Migration 000069 may retain only orphaned terminal history as snapshot `0`.
  Migration 000072 closes ordinary writes and makes that legacy status
  immutable. Active, unknown-status, or nonpositive live-Task rows reject the
  migration atomically.
- Startup never forces dirty metadata. Clean versions >=69 are validated before
  fixups and after migration; versions >=72 also require the final triggers and
  PostgreSQL constraint.

### 4. Validation & Error Matrix

| Condition | Expected result |
|---|---|
| Dirty version, regardless of legacy env value | Return `ErrMigrationDirty`; preserve metadata and schema. |
| Clean version >=69 with missing minimum object | Return sanitized `ErrMigrationSchemaDrift` before fixups. |
| Terminal orphan crossing 000069 | Preserve row with snapshot `0`; never use it as authority. |
| Active/unknown orphan or mismatched live TaskRun | Reject upgrade atomically. |
| Used 000072 down with any snapshot `0` row | Reject before migration version mutation. |

### 5. Good/Base/Bad Cases

- Good: pre-69 and clean 69-71 paths converge at 72 with paired definitions and
  only authoritative positive snapshots entering executable flows.
- Base: pristine 72 down returns cleanly to 71.
- Bad: infer a deleted node for orphan history, accept snapshot `0` as wildcard,
  or repair dirty metadata through `Force` during service startup.

### 6. Tests Required

- Run shared SQLite and required real-PostgreSQL 000069/000072 contract suites,
  including both upgrade paths, single/batch node deletion, atomic rejection,
  used/pristine down, dirty-env matrix, and `search_path` schema-drift checks.
- Exercise every executable consumer with legacy-zero, mismatched, and matching
  snapshots; include repetition/race gates for admission and migration code.

### 7. Wrong vs Correct

Wrong:

```go
db.Where("task_id = ? AND status = ?", taskID, "success")
```

Correct:

```go
db.Where("task_id = ? AND node_id_snapshot = ? AND status = ?",
    taskID, task.NodeID, model.TaskRunStatusSuccess)
```

## Scenario: Atomic Multi-Node Drill And Recovery Admission

### 1. Scope / Trigger

- Trigger: changing Task/Drill reservation, TaskRun execution entry, Recovery
  node leases, sandbox selection, or `NodeWriteAdmission` wiring.
- Applies to both the Drill source node and sandbox node on SQLite and
  PostgreSQL; a Drill transport being configured does not relax this boundary.

### 2. Signatures

- `AdmitTaskTx(context.Context, *gorm.DB, uint) error`.
- `EnterTaskExecutionTx(context.Context, *gorm.DB, uint, uint, time.Time) error`.
- `AdmitDrillTx(context.Context, *gorm.DB, sourceNodeID, sandboxNodeID uint) error`.
- `EnterDrillExecutionTx(context.Context, *gorm.DB, runID, sandboxNodeID uint, startedAt time.Time) error`.
- `AdmitRecoveryTx(context.Context, *gorm.DB, nodeID uint) error`.

### 3. Contracts

- Drill reservation sorts and deduplicates source/sandbox node IDs, locks every
  node boundary in that deterministic order, and checks both Recovery leases in
  the same transaction that creates TaskRun and Evidence.
- Drill execution derives the immutable source from TaskRun, locks the same
  source/sandbox set, rechecks both leases, and commits pending-to-running plus
  Evidence start atomically. A lease acquired after reservation but before start
  therefore blocks remote side effects.
- Recovery admission rejects an active Drill whose source or Evidence sandbox
  is the requested node. Active on either side remains fail-closed until durable
  reconciliation repairs a split pair.
- Missing admission wiring returns the sanitized unavailable sentinel even when
  a Drill transport is configured. Process-local ownership is never a substitute
  for the database boundary.

### 4. Validation & Error Matrix

| Condition | Expected result |
|---|---|
| Recovery lease exists on source or sandbox before Drill reservation | Drill reservation conflicts; no TaskRun/Evidence pair is created. |
| Recovery lease appears on either node after reservation but before start | Start transaction rolls back; TaskRun/Evidence remain pending. |
| Active Drill exists on a node as source or sandbox | Recovery admission conflicts; no lease is created. |
| Source and sandbox are the same node | Lock and inspect the boundary once; preserve all other checks. |
| Admission implementation is nil or a node boundary is missing | Fail closed with unavailable; do not create or start a run. |

### 5. Good/Base/Bad Cases

- Good: source and sandbox are locked in ascending ID order, both leases are
  rechecked at start, and exactly one side wins a concurrent Drill/Recovery race.
- Base: source equals sandbox; the deduplicated one-node transaction succeeds
  only when that node has no active Recovery lease.
- Bad: check only `TaskRun.node_id_snapshot`, check the sandbox outside the
  caller transaction, or rely on in-memory Drill ownership.

### 6. Tests Required

- Cover source and sandbox lease conflicts at reservation and start, same-node
  deduplication, caller rollback, missing admission wiring, and immutable
  sandbox mismatch.
- Run both race orders repeatedly on SQLite and required real PostgreSQL:
  Recovery-first blocks Drill; Drill-first blocks Recovery. PostgreSQL coverage
  must run in the required parity job rather than being skipped without a DSN.
- A race test must hold the winner transaction uncommitted until the contender
  has reached the same database boundary. SQLite asserts a real retryable
  `BUSY`/`LOCKED` result; PostgreSQL asserts `wait_event_type = 'Lock'` and that
  `pg_blocking_pids` contains the exact holder PID. A shared start channel or a
  sequentially pre-seeded lease is not sufficient concurrency evidence.
- At execution start, a Recovery-first case represents the documented
  legacy/out-of-band lease appearing after reservation: normal
  `AdmitRecoveryTx` cannot legally win while the pending Drill pair is active.
  Name and assert this case separately from normal Recovery admission.

### 7. Wrong vs Correct

Wrong:

```go
// Checks only the source and leaves the sandbox outside the reservation.
if err := admission.AdmitTaskTx(ctx, tx, task.NodeID); err != nil { return err }
```

Correct:

```go
if err := admission.AdmitDrillTx(ctx, tx, task.NodeID, evidence.SandboxNodeID); err != nil {
    return err
}
// Create TaskRun and Evidence before committing this same transaction.
```

## Scenario: PostgreSQL Timestamp Scan-Location Parity

### 1. Scope / Trigger

- Trigger: changing PostgreSQL connection construction, timestamp-bearing
  models, UTC migration coverage, or a migration that introduces a PostgreSQL
  `TIMESTAMPTZ` column.
- Applies to `backend/internal/database/database.go`, pgx codec registration,
  GORM's PostgreSQL dialector, and the PostgreSQL migration-parity CI job.

### 2. Signatures

- Connection helper: `openPostgresSQLDB(dsn string) (*sql.DB, error)`.
- Required PostgreSQL runner:
  `scripts/run-required-postgres-tests.sh <suite-label> <go-package> <test-selector>`.
- Migration selector:
  `^(Test.*Migration.*Postgres.*|TestPostgresTimestamptzScanUsesConfiguredUTC)$`.
- Export behavior gate:
  `go test ./internal/backupasset/export -run '^TestExportBehaviorPostgres$' -count=1`.
- Required pgx registrations per physical connection:
  `pgtype.TimestampCodec{ScanLocation: scanLocation}` and
  `pgtype.TimestamptzCodec{ScanLocation: scanLocation}`.

### 3. Contracts

- PostgreSQL DSNs default `timezone` to `UTC` when the caller did not specify
  one; `scanLocation` is loaded from that effective setting.
- A PostgreSQL `schema_migrations` existence probe that precedes an unqualified
  read must use `pg_catalog.to_regclass('schema_migrations')`, so both queries
  resolve through the same connection `search_path`. Never count an unscoped
  `information_schema.tables` row and then read an unrelated schema.
- GORM must receive the `*sql.DB` returned by `openPostgresSQLDB`, rather than
  opening an unrelated pool through the dialector DSN.
- Register **both** `timestamp` and `timestamptz` codecs in pgx's `AfterConnect`
  hook. Configuring only `timestamp` leaves `TIMESTAMPTZ` scans vulnerable to
  `time.Local` on newer Go/pgx combinations.
- The migration selector is semantic, not a version whitelist: every PostgreSQL
  migration test following the `Test...Migration...Postgres...` naming contract
  is selected automatically. The runner first executes `go test -list` with the
  exact selector, prints every selected test and its count, rejects an empty
  selection, requires `TEST_POSTGRES_DSN`, then executes the same selector.

### 4. Validation & Error Matrix

| Condition | Expected result |
|---|---|
| PostgreSQL DSN has no timezone | Use `UTC` for server runtime parameter and scan location. |
| DSN timezone cannot be loaded | `Open` fails before creating a GORM database. |
| `TIMESTAMPTZ '...+00'` is scanned while `TZ=Asia/Shanghai` | Returned `time.Time` has `Location()==time.UTC` and preserves the instant. |
| Only `TimestampCodec` is registered | Invalid: `TIMESTAMPTZ` may scan in `time.Local`; add `TimestamptzCodec`. |
| The selector matches zero tests | Fail before execution; an empty green command is not parity evidence. |
| A PostgreSQL migration test does not follow the semantic naming contract | Invalid test contract; rename it and make the workflow freshness test fail until selected. |

### 5. Good/Base/Bad Cases

- Good: `AfterConnect` registers both codecs with the same `scanLocation`, and
  a real PostgreSQL test passes with a non-UTC process timezone.
- Base: a caller explicitly selects an IANA PostgreSQL timezone; both codecs
  use that same location consistently.
- Bad: relying on `gorm.io/driver/postgres` to register a scan location for
  `TIMESTAMPTZ` after replacing its connection pool.

### 6. Tests Required

- `TestPostgresTimestamptzScanUsesConfiguredUTC` must run against a real
  PostgreSQL service with `TZ` set to a non-UTC value and assert both location
  and RFC3339 value.
- PostgreSQL migration tests must exercise every paired apply/down contract,
  including 000074 clean preflight/retry, down admission, and schema drift.
- `TestRunMigrationsPostgresDirtyCheckUsesSearchPath` must prove an unrelated
  sibling schema does not interfere while a search-path-visible dirty row still
  fails closed.
- `TestPostgresMigrationCISelectorIncludesDrillRecovery074` must prove the active
  workflow calls the reusable runner and the exact selector lists all three
  required 000074 PostgreSQL contracts.
- Run the required selector above with `REQUIRE_POSTGRES_MIGRATION_TEST=1`; a
  skipped PostgreSQL test is not completion evidence.
- Run `TestExportBehaviorPostgres` with `REQUIRE_POSTGRES_EXPORT_TEST=1`; a
  missing `TEST_POSTGRES_DSN` is a failed required gate, never SQLite evidence.

### 7. Wrong vs Correct

Wrong:

```go
conn.TypeMap().RegisterType(&pgtype.Type{
    Name: "timestamp", OID: pgtype.TimestampOID,
    Codec: &pgtype.TimestampCodec{ScanLocation: scanLocation},
})
```

Correct:

```go
conn.TypeMap().RegisterType(&pgtype.Type{Name: "timestamp", OID: pgtype.TimestampOID,
    Codec: &pgtype.TimestampCodec{ScanLocation: scanLocation}})
conn.TypeMap().RegisterType(&pgtype.Type{Name: "timestamptz", OID: pgtype.TimestamptzOID,
    Codec: &pgtype.TimestamptzCodec{ScanLocation: scanLocation}})
```

## Scenario: Policy Hooks And Sensitive Settings At Rest

### 1. Scope / Trigger

- Trigger: adding or changing policy hook persistence, app-aware policy creation
  or execution, settings registry entries that can contain secrets, or encryption
  migration coverage.
- Applies to `model.Policy`, `PolicyHandler`, task execution hook rendering,
  `settings.Service`, `system_settings`, app credentials, and
  `bootstrap.MigrateEncryptionV1ToV2`.

### 2. Signatures

- Policy DB columns: `policies.pre_hook`, `policies.post_hook`.
- Policy API fields: `pre_hook`, `post_hook`, visible only to admin responses.
- Settings registry field: `SettingDef.Sensitive`.
- Sensitive settings currently include `smtp.password` and
  `metrics.remote_bearer_token`.
- Encryption prefixes: `enc:v1:` for legacy values and `enc:v2:` for current
  writes.

### 3. Contracts

- Policy model hooks encrypt non-empty `PreHook` and `PostHook` before save and
  decrypt them after find. Handlers and task execution should work with plaintext
  model values after GORM loads.
- Non-admin policy list/detail/create/update responses must return empty
  `pre_hook` and `post_hook` fields, even when stored hooks exist.
- Non-admin policy updates must preserve existing stored hooks. Hidden or empty
  hook fields from a non-admin client are not authorization to clear hooks.
- Non-admin requests with non-empty `pre_hook` or `post_hook` must fail with
  `respondForbidden`.
- Admin requests may create, update, or clear manual hook fields, subject to the
  existing hook command validation.
- App-profile-generated hooks must be rendered only at task runtime from
  encrypted app credential config. Persisting those rendered commands would copy
  database passwords or tokens into policy hook columns.
- Sensitive settings persisted through `settings.Service.Update` or
  `UpdateWithTx` must be encrypted before `system_settings` upsert, while empty
  sensitive values may remain empty.
- `GetEffective` must not replace an expired cache entry with env/default values
  when the DB lookup fails; return the stale cache if available.
- V1-to-V2 encryption migration must count and re-encrypt all sensitive setting
  keys plus policy/app-credential secret columns.

### 4. Validation & Error Matrix

| Condition | Expected result |
|---|---|
| Operator reads a policy with stored hooks | Response contains empty `pre_hook` and `post_hook`. |
| Operator updates normal policy fields and sends empty hooks | Existing hooks remain unchanged in the DB. |
| Operator sends a non-empty hook | 403 through `respondForbidden`; no mutation. |
| Admin sends a valid hook | Persist encrypted at rest and return plaintext in admin response. |
| Built-in app profile is selected with a credential | Validate credential existence; leave generated hooks out of stored policy fields. |
| Sensitive setting is updated with a non-empty value | Store `enc:v2:` value and return plaintext through settings service reads. |
| DB lookup fails during `GetEffective` and stale cache exists | Return stale cached value, not env/default fallback. |
| Legacy `enc:v1:` value exists in a covered column/key | Migration re-encrypts to `enc:v2:` and count goes to zero. |

### 5. Good/Base/Bad Cases

- Good: an admin stores `pre_hook="echo prepare"`; DB contains encrypted text,
  admin reads plaintext, operator reads an empty hook, and operator updates
  `enabled=false` without clearing the stored hook.
- Base: a MySQL app-aware policy stores `app_profile` and `app_credential_id`;
  runtime renders the dump/cleanup hooks just before execution.
- Bad: rendering `mysqldump -p<password>` during policy create/update and saving
  it into `policies.pre_hook`.

### 6. Tests Required

- Handler tests for admin visibility, non-admin hidden responses, non-admin
  update preservation, and non-admin non-empty hook rejection.
- Integration tests for app-aware policies proving generated hooks are not
  persisted while runtime rendering still executes.
- Model/service tests proving policy hooks and sensitive settings are encrypted
  at rest and plaintext only at the model/service boundary.
- Bootstrap tests proving V1 values in policy, app credential, integration proxy,
  and sensitive settings are counted and migrated.

### 7. Wrong vs Correct

Wrong:

```go
p.PreHook, p.PostHook, _ = profile.RenderHooks(appProfile, access.Config())
tx.Save(&p)
```

Correct:

```go
if _, err := profile.ResolveAppProfileAccess(db, credentialID); err != nil {
    return err
}
// Store only app_profile/app_credential_id. Render generated hooks at runtime.
```

---

## Scenario: Backup Asset Search Projection And User Overlays

### 1. Scope / Trigger

- Trigger: changing backup-asset search normalization, projection/index
  publication, query/cursor behavior, content/OCR ingest, saved searches,
  favorites, tags, recent access, or their settings/runtime/API wiring.
- Applies to paired migration `000065`, `backupasset/search`,
  `backupasset/overlay`, backup-asset runtime composition, Search/Overlay
  handlers, and the mandatory PostgreSQL parity job.

### 2. Signatures

- Paired DDL:
  `migrations/{sqlite,postgres}/000065_backup_asset_search.{up,down}.sql`.
- Independent domains: `KeyDomainSearchToken="search_token"` and
  `LeaseHolderSearchIndex="search_index"`; Search still signs cursors with the
  existing independent Cursor Signing domain.
- Query route: `POST /api/v1/asset-search`; AST/scope/cursor stay in the body.
- Owner-tag candidate port:
  `TagResolver.CandidateRefs(ctx, ownerID, name, authorizedPointIDs, limit)`.
- Future publication port:
  `ContentIndexIngest.PublishContentProjection(ctx, projection)` and
  `RevokeContentProjection(ctx, projection)`.
- Asset overlay authorization:
  `AuthorizeAsset(ctx, tx *gorm.DB, actor, AssetRef)`.

### 3. Contracts

- Product search semantics are computed in Go: Unicode NFKC, full case fold,
  slash/path segments, Han bigrams, Latin tokens, extension/date tokens,
  integer ranking, stable sort, grouping, and cursor binding. Database-native
  FTS/collation is not the product contract.
- SQL/posting and owner-tag candidates are bounded before private Catalog
  hydration. Tag candidates receive only the already-authorized selected point
  IDs. `any` unions available positive fields; an unselective negative branch
  may scan only up to the configured hard ceiling, then returns resource limit.
- Search Token HMACs bind field/kind/normalizer/key version. Search/Entry/
  Cursor/Audit/future Derived keys are never reused; KEK rotation rewraps
  without changing Search tokens.
- Secret/unknown content or OCR is three-valued without an exact unexpired
  `asset.secret_reveal` proof. HMAC postings never become a returned content hit
  without the excerpt resolver's real-match verification.
- A classification change removes both content/OCR posting families, advances
  both field rows to the new classification revision, clears the sibling
  excerpt ref, and leaves that sibling unavailable until republished.
- Search cursors bind user/role/scope/query/key/point/generation/projection/
  classification/owner-tag revision/proof. Payloads contain no query, token,
  path, name, tag, label, or snippet.
- Favorite, bulk favorite, tag assignment, and recent mutations authorize the
  target inside the same transaction before idempotency replay or writes.
  Overlays never create a hold, change retention, copy source metadata, or
  write Provider bytes.
- `backup_assets.enabled` remains false by default. Disabled paths stop before
  Search-key, projection, proof lookup, audit mutation, or Provider access.

### 4. Validation & Error Matrix

| Condition | Expected result |
|---|---|
| SQLite/PostgreSQL 000065 definitions diverge | Migration parity test fails; do not merge. |
| Search Token is missing/lost or version differs | Projection unavailable; never regenerate silently or use old postings. |
| Query has unknown schema/op/field, bad exact scope, or exceeds a limit | Reject the whole query with a typed safe error. |
| Positive candidates exceed the configured ceiling | Return resource limit; never truncate and claim a complete result. |
| Latest authorized point is unindexed | Report real building/failed/unavailable coverage; never fall back to an older point. |
| Secret/unknown content lacks exact proof | Content/OCR truth is unknown; no hit/count/suggestion/snippet fact. |
| Tag definitions/assignments change between pages | Owner-tag digest changes and the cursor is stale. |
| Asset ownership is lost before an overlay transaction authorizes | Mutation fails with safe not-found/forbidden and writes nothing. |
| Used 000065 down is requested | Atomic guard rejects; schema/version/data remain unchanged. |

### 5. Good/Base/Bad Cases

- Good: an Operator's tag query resolves bounded refs only inside that
  Operator's authorized producing lineages, evaluates in Go, and returns a
  cursor bound to the owner-tag revision.
- Base: a metadata-only query on a complete generation returns exact total and
  may emit bounded metadata suggestions; content capability can remain false.
- Bad: load every document before applying `MaxCandidates`, authorize a
  favorite before starting its transaction, or retain OCR postings after a
  content classification change.

### 6. Tests Required

- Paired SQLite/real-PostgreSQL apply, pristine down, used-down atomic rejection,
  FK/check/unique/index/model/UTC parity; required DSN must not skip.
- Normalization/property, Search Token independence/loss/rewrap/replacement,
  candidate preselection, path proximity, grouping/order/page concatenation,
  coverage, and every cursor-staleness binding.
- Secret Kleene logic, exact/wrong-purpose/expired proof, resolver failure,
  metadata-only suggestions, and query/log/audit/cursor plaintext scans.
- Content ingest source/fence/classification CAS, sibling invalidation,
  replace/revoke rollback, and no plaintext/ciphertext ownership.
- Overlay owner isolation, transaction-bound authorization, quota races,
  idempotency, tag candidates/revision, tombstone/broken/recent cleanup, and
  no hold/retention/Provider mutation on SQLite and real PostgreSQL.

### 7. Wrong vs Correct

Wrong:

```go
if err := assets.AuthorizeAsset(ctx, actor, ref); err != nil {
    return err
}
return db.Transaction(func(tx *gorm.DB) error { return tx.Create(&favorite).Error })
```

Correct:

```go
return db.Transaction(func(tx *gorm.DB) error {
    if err := assets.AuthorizeAsset(ctx, tx, actor, ref); err != nil {
        return err
    }
    return tx.Create(&favorite).Error
})
```

---

## Scenario: Backup Asset Content Delivery Ledger

### 1. Scope / Trigger

- Trigger: changing backup-asset delivery grants, cookie/session bindings,
  request or scope accounting, content-session leases, content audit
  idempotency/retry, or migration `000066` and later migrations that depend on
  it.
- Applies to paired `000066_backup_asset_content` SQL, content models, the
  migration fixture, and the cross-engine budget behavior fixture.

### 2. Signatures

- Paired DDL:
  `migrations/{sqlite,postgres}/000066_backup_asset_content.{up,down}.sql`.
- Tables: `backup_asset_delivery_grants`,
  `backup_asset_delivery_requests`, and `backup_asset_delivery_usage`.
- Audit uniqueness:
  `idx_backup_asset_audit_events_content_grant_action(grant_id, action)` for
  the four content actions.
- Migration tests: `TestBackupAssetMigration066SQLite` and
  `TestBackupAssetMigration066Postgres`.
- Behavior tests: `TestContentBehaviorSQLite` and
  `TestContentBehaviorPostgres`.
- Atomic request/audit boundaries: `BudgetService.RecordBlocked` and
  `BudgetService.Finalize`.
- Final audit/lease reconciliation: `Reconciler.Startup`,
  `Reconciler.Reconcile`, and `ContentAuditService.FlushGrant`.

### 3. Contracts

- `000066` accepts exactly one `backup_asset` resource: RecoveryPoint,
  Catalog generation, and entry IDs are all non-null; RecoveryResult columns
  stay null and have no Child 13 foreign key or enabled resource kind.
- A grant persists only the SHA-256 cookie-secret hash. Public delivery ID,
  internal grant ID, session JTI, proof, action, renderer/profile, source
  fingerprint, lease fence hash, absolute/idle expiry, and budgets are distinct
  bindings; private fields remain `json:"-"` in models.
- The composite Catalog foreign key is
  `(catalog_generation_id, entry_id, recovery_point_id)` with `RESTRICT`.
  `lease_id` references one `content_session` lease with `RESTRICT`.
- SQL checks enforce the whole action/renderer/profile/range/classification/
  proof product. Download requires exact `asset.download`; secret or unknown
  preview requires exact `asset.secret_reveal`; non-secret preview has no proof.
- Request reservations and global/provider/user usage counters are committed
  atomically before source I/O. Counters stay non-negative and cannot exceed
  request, cumulative, request-count, or in-flight bounds on either engine.
- A terminal request transition and its safe audit counters are one database
  transaction. `RecordBlocked` accounts the blocked attempt when it inserts the
  request; `Finalize` accounts success/failure when it releases reservations.
  The Broker must not perform a second post-finalization audit-counter write:
  that split loses crash summaries and double-counts successful requests.
- Final read/download audit is emitted only after the grant is
  `revoked|expired|closed`, `in_flight=0`, and any retry deadline is due. Its
  total `byte_count` is the sum of persisted request `response_bytes`; Range
  count/bytes remain the subset of effective or blocked Range attempts.
  Request-row count must equal `audit_request_count` before emission.
- Audit state/counters are one closed SQL product. `none` has zero counters and
  no failure/retry fields; `pending|emitted|retry_wait|failed` have a non-zero,
  outcome-balanced request summary no larger than the grant request count.
  Range count is no larger than the audit request count, zero Range count has
  zero Range bytes, and Range bytes are no larger than charged delivered bytes.
  Only retry/failed states have a non-empty failure code, positive attempt
  count, and `audit_next_attempt_at`.
- Persisting an audit retry uses the loaded grant version and audit state as a
  compare-and-swap fence. A stale failure path must never reopen an already
  `emitted` final summary or overwrite a newer retry attempt.
- Crash recovery revokes old grants before they can authorize another read.
  A still-valid short `content_session` fence is safely deferred, not treated
  as an unrecoverable startup failure; terminal active leases and failed
  releases are retried on later reconciliation passes until takeover/release
  or the absolute-deadline reconciler fences them. State reconciliation keeps
  running while the feature is disabled/not ready, but an explicit shutdown
  fence cancels and joins the managed run loop before final cleanup and stops
  periodic database mutation before schema drain/down.
- All timestamps are UTC-compatible (`DATETIME`/`TIMESTAMPTZ`). Run the real
  PostgreSQL migration and behavior packages serially against a shared DSN so
  their destructive fixtures cannot overlap.
- Down is a guarded operation. Any grant, request, usage row, or
  `content_session` lease aborts before schema mutation. Only a proven runtime
  drain may remove terminal ephemeral state and then run pristine down to
  `000065`; otherwise retain `000066` and ship a forward repair.

### 4. Validation & Error Matrix

| Condition | Expected result |
|---|---|
| Resource is empty, dual, unknown, or RecoveryResult | Insert/service validation fails closed. |
| Catalog tuple crosses point or generation | Composite foreign key rejects the grant. |
| Action/proof/renderer/profile/classification combination is impossible | Table CHECK and service validator reject the whole product. |
| Cookie secret rather than its 64-hex hash reaches persistence | Reject; no plaintext-secret column or model JSON exposure is allowed. |
| Concurrent reservations exceed any scope or grant bound | Excess transactions fail; successful totals remain at or below every bound. |
| Process crashes between request completion and a separate audit update | Impossible by contract: finalization and audit counters commit or roll back together. |
| Active grant has pending audit, or retry deadline is in the future | Do not emit a final summary; keep it in the bounded backlog. |
| Audit request rows/counters disagree or a request is non-terminal | `FlushGrant` fails closed without writing an incomplete append-only event. |
| Stale audit retry races a completed or newer retry transition | Version/state CAS rejects the stale update; `emitted` never reopens. |
| Short crash lease still holds its old fence | Revoke the grant, defer takeover without failing startup, and retry after short expiry. |
| Terminal lease release returns a real error | Surface the error and retry the still-active lease on the next reconciliation pass. |
| Feature is disabled but runtime is still running | Reconcile terminal state/leases/audit without enabling tickets, content reads, or cache. |
| Runtime shutdown/schema-drain fence is set | Cancel and join the periodic loop within the caller deadline; explicit bounded shutdown cleanup owns the final pass and down does not start after a join failure. |
| Required PostgreSQL DSN is absent | Test fails when the corresponding `REQUIRE_POSTGRES_*_TEST=1`; skip is not completion evidence. |
| Down sees any delivery state or content lease | Down aborts before dropping the audit index or tables. |
| Pristine or explicitly drained schema is downgraded | Return exactly to `000065` without changing Catalog/Search/Provider facts. |

### 5. Good/Base/Bad Cases

- Good: twelve concurrent 10-byte requests under a five-request/50-byte bound
  commit exactly five reservations on both SQLite and PostgreSQL, then finalize
  without negative or leaked counters.
- Base: an active non-secret text preview grant binds one Catalog tuple, one
  lease, a hash-only cookie secret, closed representation fields, and UTC TTLs.
- Good: a canceled read atomically finalizes its reservation and increments one
  failure summary under the same bounded detached cleanup context; a restart
  cannot observe the request as terminal with its audit still absent.
- Base: a full GET contributes response bytes to `byte_count` and zero Range
  count/bytes; a 206 contributes to both total and Range bytes.
- Bad: storing a generic resource locator, accepting both AssetRef and
  RecoveryResultRef, charging bytes only after a read, or deleting live rows in
  a down migration.
- Bad: flushing an active grant, ignoring `audit_next_attempt_at`, writing audit
  counters after `Finalize` returns, or never retrying a terminal lease after a
  failed takeover/release. Also bad: gating state reconciliation on feature
  readiness or letting it continue after the shutdown/schema-drain fence.

### 6. Tests Required

- Apply real paired SQL from `000065`, assert tables/columns/checks/FKs/indexes,
  exercise valid and invalid rows, and prove model/UTC parity on both engines.
- Prove pristine down, every used-down family, atomic schema/data/index
  preservation, explicit safe drain, and the existing `000065` used-down guard.
- Run the two PostgreSQL packages as separate commands with required flags and
  a real PostgreSQL service; do not combine destructive fixtures in one
  multi-package `go test` invocation.
- Use transaction barriers for reservation/finalize/replay/cancel/crash races
  and assert exact successful totals plus non-negative persisted counters.
- Prove blocked/success/failure audit counters commit with their request state,
  duplicate finalization cannot count twice, crash reconciliation creates one
  failure summary, active grants do not flush, and retry deadlines are honored.
- Prove full-response bytes and Range-only bytes remain distinct, audit retry
  persistence errors are returned, and invalid audit state/counter/retry
  products are rejected by both paired migrations.
- Exercise short-held crash fences and release failures: startup must revoke and
  safely defer the former, while periodic reconciliation retries both terminal
  lease families. Use the real `LeaseService` to prove a disabled-but-running
  Content runtime releases an expired short lease, and prove the shutdown fence
  cancels/joins the run loop and suppresses later periodic mutation. Include the
  focused race suite.

### 7. Wrong vs Correct

Wrong:

```sql
-- A generic resource and post-read accounting bypass closed authorization.
CREATE TABLE delivery_grants (resource_id TEXT, cookie_secret TEXT);
UPDATE usage SET delivered_bytes = delivered_bytes + :bytes;
```

Correct:

```sql
CHECK (resource_kind = 'backup_asset'
       AND recovery_point_id IS NOT NULL
       AND catalog_generation_id IS NOT NULL
       AND entry_id IS NOT NULL
       AND recovery_job_id IS NULL
       AND recovery_result_id IS NULL)
-- Reserve grant and scope budgets transactionally before opening the source.
```

Wrong:

```go
_, err := budget.Finalize(ctx, intent)
if err == nil {
    err = audit.RecordRead(ctx, summary) // crash gap and duplicate-count race
}
```

Correct:

```go
// Finalize changes request/budget state and the closed audit counters in the
// same transaction. The reconciler emits only the terminal, due summary.
_, err := budget.Finalize(ctx, intent)
```

---

## Scenario: Backup Asset Processing Queue and Derived Store

### 1. Scope / Trigger

- Applies to paired `000067_backup_asset_processing`, processing models,
  Worker/RP lease fencing, one-use grants, atomic Derived manifests, and
  cross-engine behavior changes.

### 2. Signatures

- Paired DDL:
  `migrations/{sqlite,postgres}/000067_backup_asset_processing.{up,down}.sql`.
- Closed job state type: `processing.ProcessingState`.
- Transactional lease renewal:
  `LeaseService.RenewTx(ctx, tx, RenewLeaseRequest) (LeaseFence, error)`.
- Engine behavior entry points: `TestProcessingBehaviorSQLite` and
  `TestProcessingBehaviorPostgres`.
- Atomic Derived publication entry point:
  `ArtifactSink.CommitManifest(context.Context, CommitManifestRequest)`.
- Bounded conflict helper: `ArtifactSink.retryManifestConflicts`; it recognizes
  SQLite busy/table locks plus PostgreSQL serialization/deadlock conflicts and
  stops on caller cancellation.
- Required PostgreSQL migration coverage uses the reusable semantic runner and
  selector from "PostgreSQL Timestamp Scan-Location Parity"; do not add a
  version whitelist here.
- Required Processing behavior selector:
  `^(TestProcessingBehaviorPostgres|TestArchiveMemberBehaviorPostgres)$`.

### 3. Contracts

- SQLite and PostgreSQL use the same closed Processing states and separate
  transition revision, stable error, retry, cancel, supersede, and expiry
  products. Partial unique indexes enforce one current job per `work_key`, one
  current attempt per job, and one active interest per owner tuple.
- A Worker pull owns both a short Worker lease and a `processing_job`
  RecoveryPoint lease. A takeover creates a new attempt/fence; old grants,
  heartbeats, uploads, and manifest commits remain invalid forever.
- Activation secrets are persisted only as hashes and are one-use. Input and
  Sink request counts, bytes, and in-flight reservations are transactionally
  bounded on both engines.
- Derived blobs use the independent `derived_store` key domain, per-blob random
  DEKs, authenticated chunks, opaque locators, and explicit references.
  Search projection revoke must succeed before reference/key/ciphertext
  destruction; late output and tampered ciphertext fail closed.
- A projected manifest commit has two retry-sensitive database boundaries:
  reading/decrypting projection evidence before `PreparePublish`, and the
  caller transaction that commits uploads/blobs/artifacts plus the Search
  projection. Both boundaries use the same bounded, context-aware transient
  conflict policy. Retrying only the transaction is insufficient because a
  concurrent writer can lock the Derived blob evidence query first.
- `PreparedDerivedProjection.PublishTx` is idempotent by artifact-set ID and
  writes only through the supplied transaction, so a rolled-back transient
  transaction may reuse the prepared publication. Validation, fence, source,
  policy, and other semantic errors are not retryable.
- Pristine down is allowed only with no Processing/Worker/Derived/updater rows
  and no active `processing_job` lease. Used down aborts before any table or
  index is removed; retain `000067` and ship a forward repair.

### 4. Validation & Error Matrix

| Condition | Expected result |
|---|---|
| Same canonical `work_key` is requested concurrently | One current job; interests coalesce without an in-memory-only lock contract. |
| Worker or RecoveryPoint lease/fence is stale | Heartbeat, grant use, upload, projection and manifest publication fail closed. |
| Last interest is removed | Revoke both grants before `cancel_requested`; one remaining interest preserves the shared job. |
| Manifest member, digest, MIME, count, size, completeness, source or policy differs | Reject the entire set and destroy/reconcile invisible staging. |
| Search projection revoke fails | Retain Derived reference, wrapped DEK and ciphertext; do not continue destruction. |
| Projection evidence read or manifest upload/blob update returns a transient SQLite lock | Retry with the bounded conflict policy; honor caller cancellation and never spin indefinitely. |
| Two callers retry the same current projected manifest concurrently | Exactly one transaction commits the terminal job/artifact-set state; the loser returns a closed semantic conflict without a second publication. |
| Manifest validation, source/policy/fence, or payload binding fails | Fail immediately; do not classify the semantic failure as a database conflict. |
| PostgreSQL boolean fixture receives integer SQL literals | Invalid test fixture; bind Go `bool` values so the intended CHECK is exercised. |
| A shared-memory SQLite DSN is reused by `go test -count=N` | Invalid test isolation; include a per-open atomic sequence and keep engine state per test run. |
| Required PostgreSQL DSN is absent or unreachable | Gate fails or remains explicitly `not_executed`; SQLite/text inspection is not parity evidence. |

### 5. Good/Base/Bad Cases

- Good: a current Worker heartbeat renews its attempt, RecoveryPoint lease and
  live grants in one transaction; a takeover gets a new fence and old output
  can never publish.
- Good: deterministic SQLite callbacks inject one lock into the Derived blob
  evidence query and one into the upload state update; bounded retries still
  produce exactly one committed projection under concurrent callers.
- Base: no Worker transport/capability is configured, so Core remains ready and
  returns informational `not_deployed` without persisting noisy failed jobs.
- Bad: delete a Derived key/blob before Child 7 projection revoke, use `0/1`
  boolean literals in shared PostgreSQL fixtures, or derive an in-memory test
  DSN from `t.Name()` alone when the suite must pass with `-count`.
- Bad: protect only `db.Transaction(...)` with retries while leaving
  `prepareProjectionEvidence` as a one-shot database read.

### 6. Tests Required

- Migration tests: `TestBackupAssetMigration067SQLite` and
  `TestBackupAssetMigration067Postgres` plus used-down/model/check/FK
  parity cases.
- Behavior tests: `TestProcessingBehaviorSQLite` and
  `TestProcessingBehaviorPostgres`, both calling one shared contract suite.
- `TestConcurrentAtomicProjectionRetriesCommitExactlyOnce` must inject
  `sqlite3.ErrLocked` at both the pre-transaction Derived blob query and the
  in-transaction upload update, assert one injection at each boundary, and
  prove exactly one durable winner under `-race` and repetition.
- The real PostgreSQL gate sets `REQUIRE_POSTGRES_MIGRATION_TEST=1` and
  `REQUIRE_POSTGRES_PROCESSING_TEST=1`; a missing `TEST_POSTGRES_DSN` is a
  failed required gate, never SQLite evidence.

### 7. Wrong vs Correct

Wrong cross-engine fixture and repeated-test DSN:

```go
_, _ = db.Exec(`INSERT INTO artifact_sets (projection_required) VALUES (0)`)
dsn := "file:" + strings.ReplaceAll(t.Name(), "/", "_") + "?mode=memory&cache=shared"
```

Correct:

```go
_, _ = db.Exec(`INSERT INTO artifact_sets (projection_required) VALUES (?)`, false)
dsn := fmt.Sprintf("file:%s-%d?mode=memory&cache=shared",
    strings.ReplaceAll(t.Name(), "/", "_"), processingTestDBSequence.Add(1))
```

Wrong retry boundary:

```go
fields, err := sink.prepareProjectionEvidence(ctx, descriptor, artifacts, uploads, identities)
if err != nil {
    return result, err // a transient blob-table lock escapes before the transaction retry
}
err = sink.retryManifestConflicts(ctx, func() error { return sink.db.Transaction(commit) })
```

Correct retry boundaries:

```go
err = sink.retryManifestConflicts(ctx, func() error {
    fields, classification, err = sink.prepareProjectionEvidence(ctx, descriptor, artifacts, uploads, identities)
    return err
})
if err == nil {
    err = sink.retryManifestConflicts(ctx, func() error { return sink.db.Transaction(commit) })
}
```

---

## Scenario: Used Migration Down Admission Before Dirty Versioning

### 1. Scope / Trigger

- Trigger: a migration down guard protects durable state and is run through
  `golang-migrate` rather than executed directly as SQL.
- Applies to paired SQLite/PostgreSQL migrations whose used down must preserve
  the current clean `schema_migrations` version as well as schema and data.

### 2. Signatures

- Runner boundary: `migrator.Steps(-1)`.
- Metadata table: `schema_migrations(version, dirty)`.
- Recovery 000069 admission trigger:
  `trg_backup_asset_recovery_downgrade_admission` on `schema_migrations`.

### 3. Contracts

- `golang-migrate` calls `SetVersion(target, true)` before the down body. A
  down-script guard alone therefore cannot preserve the old version if it
  rejects after that call.
- A paired metadata admission trigger must reject a downgrade target below the
  protected version while the complete used-state guard is nonempty. The
  SQLite `DELETE` plus `INSERT` and PostgreSQL `TRUNCATE` plus `INSERT` occur
  in the driver transaction, so rejecting the insert rolls it back to the
  previous clean version.
- The trigger must permit target versions at or above the protected version so
  future forward migrations and a down from a later version to the protected
  version continue to work. A pristine down removes the trigger before the
  protected tables disappear.

### 4. Validation & Error Matrix

| Condition | Expected result |
|---|---|
| Used `Steps(-1)` | Error; previous version remains clean and every table, index, trigger, and row snapshot is unchanged. |
| Pristine `Steps(-1)` | Down succeeds, target version is clean, protected schema and admission trigger are removed. |
| Latch-only or purge-to-empty use | Error; the permanent latch still blocks the metadata transition. |
| Later forward migration or down to the protected version | Admission trigger permits the metadata write. |

### 5. Good/Base/Bad Cases

- Good: a recovery row causes the metadata insert for version 68 to fail and
  leaves version 69 clean without executing the down body.
- Base: a never-used 000069 schema reaches 000068 clean and removes its
  admission trigger.
- Bad: test the down SQL via `db.Exec` only, then assume its first statement
  also protects `schema_migrations` under `migrator.Steps(-1)`.

### 6. Tests Required

- Exercise the complete used-state matrix through `migrator.Steps(-1)` on
  SQLite and required real PostgreSQL; compare version/dirty, tables,
  definitions, indexes, triggers, and row counts before and after refusal.
- Prove pristine down restores the preceding version clean and removes the
  admission trigger.
- Run the required PostgreSQL migration parity selector with
  `REQUIRE_POSTGRES_MIGRATION_TEST=1`; a skipped DSN is not parity evidence.

### 7. Wrong vs Correct

Wrong:

```sql
-- This runs after golang-migrate has already committed target=68, dirty=true.
SELECT RAISE(ABORT, 'used down blocked');
```

Correct:

```sql
CREATE TRIGGER trg_backup_asset_recovery_downgrade_admission
BEFORE INSERT ON schema_migrations
WHEN NEW.version < 69 AND /* complete used-state guard */
BEGIN
    SELECT RAISE(ABORT, 'used down blocked');
END;
```

---

## Scenario: Backup Asset GA Installation Schema (000071)

### 1. Scope / Trigger

- Trigger: changing GA installation, inventory-run, or repository-conflict
  tables, their used-down guards, or the production writers of
  `ready` / `enablement_succeeded_at`.
- Applies to paired
  `migrations/{sqlite,postgres}/000071_backup_asset_ga.{up,down}.sql`,
  `model/backup_asset_migration.go`, `InventoryService.MaterializeReadiness`,
  and `RecordEnablementSucceeded`.

### 2. Signatures

- Tables: `backup_asset_installations` (singleton `slot=1`),
  `backup_asset_inventory_runs`, `backup_asset_repository_conflicts`.
- Closed class: `fresh|existing`. Closed readiness:
  `unknown|blocked|ready|acknowledged`. Closed conflict kinds:
  `shared_restic_identity`, `task_repository_mismatch`, `capability_gap`,
  `command_unsupported`.
- Admission trigger: `trg_backup_asset_ga_downgrade_admission` on
  `schema_migrations` for `NEW.version < 71`.
- The semantic CI selector must continue to list
  `TestBackupAssetMigration071Postgres`.
- Check script: `scripts/check-backup-asset-migration.sh`.

### 3. Contracts

- Used-down fails closed when any installation row is `ready` or
  `acknowledged`, `enablement_succeeded_at` is set, or any conflict row
  exists. Pristine empty down still applies.
- Production must write the latches the guard reads: passing computed
  readiness materializes stored `ready`; successful enable stamps
  `enablement_succeeded_at` once. DryRun persist stays `unknown` and does
  not stamp.
- Class never reverses `existing` → `fresh`. Fresh ack columns stay null.
- Conflict `repository_id` is empty or 32-hex. Digests are empty or 64-hex.
  `counts_json` / `task_ids_json` must be non-empty valid JSON.
- Do not add locators, identity keys, proofs, or `SnapshotFileIndex` columns.

### 4. Validation & Error Matrix

| Condition | Expected result |
|---|---|
| Pristine `Steps(-1)` from 71 | Down succeeds; tables and trigger removed; version 70 clean. |
| Installation `ready` or `acknowledged` | Used-down rejected; version 71 stays clean. |
| `enablement_succeeded_at` set, readiness still `unknown` | Used-down rejected. |
| Any `backup_asset_repository_conflicts` row | Used-down rejected. |
| `unknown`/`blocked`, no conflicts, no stamp | Pristine-style down allowed (failed unused inventory). |
| Required PostgreSQL DSN missing | Not a skipped pass; start a disposable engine or fail the gate. |

### 5. Good/Base/Bad Cases

- Good: enable persist stamps `enablement_succeeded_at`, so later schema
  down cannot erase the only durable enablement proof.
- Base: empty GA tables down to 000070.
- Bad: testing used-down only by seeding `ready` in SQL while production
  never writes the column.

### 6. Tests Required

- SQLite and required PostgreSQL
  `BackupAssetMigration071*` families: ReadyInstallation,
  AcknowledgedInstallation, RepositoryConflict, SuccessfulEnablement,
  pristine down.
- Inventory tests that DryRun does not self-promote or stamp, and that
  MaterializeReadiness / RecordEnablementSucceeded do.
- `scripts/check-backup-asset-migration.sh`.

### 7. Wrong vs Correct

Wrong:

```go
_ = service.DryRun(ctx) // persist unknown, never stamp, then enable
// 000071 down still succeeds because used-down latches were never written
```

Correct:

```go
if err := service.MaterializeReadiness(ctx, passingSnapshot); err != nil {
    return err
}
persist = func() error {
    if err := service.RecordEnablementSucceeded(ctx); err != nil {
        return err
    }
    return persistSettings()
}
```

---

## Common Mistakes

- Do not add a migration for only one database engine. SQLite and PostgreSQL
  must stay aligned.
- Do not ignore `Find`, `First`, `Create`, `Save`, `Delete`, or transaction
  errors. The P2 backend quality work explicitly fixed silent handler errors.
- Do not expose raw model values containing secrets. Use model hooks plus
  response sanitizers such as `Node.Sanitized()` before returning nodes.
- Do not rely on GORM defaults when a column already has a historical spelling.
  The `policies.bwlimit` compatibility fix exists because `bw_limit` drifted
  from the later migration contract.
- Do not manually encrypt/decrypt sensitive fields in handlers. Keep encryption
  at model/service boundaries so every caller gets the same behavior.
- Do not add a used-down latch that production never writes. `000071`
  `ready` and `enablement_succeeded_at` must be stamped by
  `MaterializeReadiness` / `RecordEnablementSucceeded`, not only by tests.
