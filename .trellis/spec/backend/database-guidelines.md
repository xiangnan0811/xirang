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
  latest migration is `000061_task_runs_traffic_indexes`.
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
