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
  latest migration is `000059_ssh_key_scope_credential_audit`.
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
