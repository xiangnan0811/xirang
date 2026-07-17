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
- Keep version numbers in lockstep across SQLite and PostgreSQL. The current
  latest migration is `000064_backup_asset_rsync_publication_contract`.
- Prefer plain SQL migrations over `AutoMigrate`. `RunMigrations` embeds the
  SQL files and executes them at startup.
- Make migrations safe for existing installations. Use `IF EXISTS` or
  `IF NOT EXISTS` where the engine supports it, and write comments when a
  migration is cleaning up historical drift.
- Only add pre-migration Go fixups for historical schema drift that cannot be
  expressed safely in SQL. Existing example: `fixupLegacyPolicyBwlimit` in
  `backend/internal/database/migrator.go`.

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
  current baseline includes `000062`, `000063_backup_asset_publication_contract`,
  and `000064_backup_asset_rsync_publication_contract`; later versions must
  remain paired. After a native managed RecoveryPoint or tombstone exists,
  schema down must fail closed rather than deleting publication history or
  Provider facts.

## Scenario: PostgreSQL Timestamp Scan-Location Parity

### 1. Scope / Trigger

- Trigger: changing PostgreSQL connection construction, timestamp-bearing
  models, UTC migration coverage, or a migration that introduces a PostgreSQL
  `TIMESTAMPTZ` column.
- Applies to `backend/internal/database/database.go`, pgx codec registration,
  GORM's PostgreSQL dialector, and the PostgreSQL migration-parity CI job.

### 2. Signatures

- Connection helper: `openPostgresSQLDB(dsn string) (*sql.DB, error)`.
- CI regression gate:
  `go test ./internal/database -run 'Test(BackupAssetMigration0(62|63|64)Postgres|PostgresTimestamptzScanUsesConfiguredUTC)' -count=1`.
- Required pgx registrations per physical connection:
  `pgtype.TimestampCodec{ScanLocation: scanLocation}` and
  `pgtype.TimestamptzCodec{ScanLocation: scanLocation}`.

### 3. Contracts

- PostgreSQL DSNs default `timezone` to `UTC` when the caller did not specify
  one; `scanLocation` is loaded from that effective setting.
- GORM must receive the `*sql.DB` returned by `openPostgresSQLDB`, rather than
  opening an unrelated pool through the dialector DSN.
- Register **both** `timestamp` and `timestamptz` codecs in pgx's `AfterConnect`
  hook. Configuring only `timestamp` leaves `TIMESTAMPTZ` scans vulnerable to
  `time.Local` on newer Go/pgx combinations.
- SQLite/PostgreSQL migration parity for backup assets covers 000062, 000063,
  and 000064. A new paired migration must be added to this regex deliberately;
  it must never be silently omitted from the PostgreSQL gate.

### 4. Validation & Error Matrix

| Condition | Expected result |
|---|---|
| PostgreSQL DSN has no timezone | Use `UTC` for server runtime parameter and scan location. |
| DSN timezone cannot be loaded | `Open` fails before creating a GORM database. |
| `TIMESTAMPTZ '...+00'` is scanned while `TZ=Asia/Shanghai` | Returned `time.Time` has `Location()==time.UTC` and preserves the instant. |
| Only `TimestampCodec` is registered | Invalid: `TIMESTAMPTZ` may scan in `time.Local`; add `TimestamptzCodec`. |
| A backup-asset migration is absent from the parity regex | Invalid CI contract; extend the regex and add an integration test. |

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
- PostgreSQL migration tests must exercise paired apply/down contracts for
  000062, 000063, and 000064.
- Run the CI regex above with `REQUIRE_POSTGRES_MIGRATION_TEST=1`; a skipped
  PostgreSQL test is not completion evidence.

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
