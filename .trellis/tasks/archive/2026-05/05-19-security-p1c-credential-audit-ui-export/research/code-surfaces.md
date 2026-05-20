# Research: P1c credential audit UI/export code surfaces

- **Query**: Research code surfaces for implementing admin-only credential audit event listing/export APIs and a frontend UI for filtering, browsing, viewing safe details, and exporting credential-use audit events.
- **Scope**: internal
- **Date**: 2026-05-20

## 1. Existing backend data/API patterns

### Files found

| File Path | Description |
|---|---|
| `backend/internal/model/models.go:421-444` | `CredentialAuditEvent` model and secret-safety comment. |
| `backend/internal/database/migrations/sqlite/000059_ssh_key_scope_credential_audit.up.sql:9-43` | SQLite table/indexes for `credential_audit_events`. |
| `backend/internal/database/migrations/postgres/000059_ssh_key_scope_credential_audit.up.sql:11-45` | PostgreSQL table/indexes for `credential_audit_events`. |
| `backend/internal/credentialaudit/audit.go:15-40` | Outcome constants and `Event` write DTO. |
| `backend/internal/credentialaudit/audit.go:144-176` | `Write` creates `model.CredentialAuditEvent` and bounds/sanitizes scalar fields. |
| `backend/internal/credentialaudit/audit.go:208-281` | Metadata sanitizer: max 16 entries, denied key/value substrings, supports scalar/string-list types. |
| `backend/internal/credentialaudit/audit.go:291-310` | Error-message sanitizer redacts after `输出:`, `output:`, `stdout:`, `stderr:` markers. |
| `backend/internal/credentialaudit/audit_test.go:15-161` | Writer tests for `CreatedAt`, error redaction, metadata/key/value dropping, field bounds. |
| `backend/internal/api/handlers/helpers.go:31-84` | Handler helper to write credential audit from Gin context; fallback credential source/kind helpers. |
| `backend/internal/api/handlers/helpers.go:96-115` | Safe credential-audit error/outcome classification helpers. |
| `backend/internal/api/handlers/audit_handler.go:18-190` | Existing generic audit list/export handler pattern. |
| `backend/internal/api/handlers/audit_handler_test.go:19-120` | Generic audit list filter/pagination and CSV export tests. |
| `backend/internal/api/router.go:101-113` | Existing handler construction area. |
| `backend/internal/api/router.go:257-258` | Existing generic `/audit-logs` list/export routes with `RBAC("audit:read")`. |
| `backend/internal/api/router.go:308-314` | Admin-only settings/config routes using `RequireRole("admin")`. |
| `backend/internal/middleware/rbac.go:9-43` | Role permission map; only `admin` currently has `audit:read`, app-credential read/write. |
| `backend/internal/api/router_test.go:53-105` | Route registration assertions include audit/settings risk routes. |
| `backend/internal/api/router_test.go:107-153` | Example admin-only RBAC test for settings security-risk summary. |
| `backend/internal/api/app_credential_rbac_test.go:94-233` | Full admin-vs-nonadmin RBAC route test pattern. |
| `backend/internal/api/handlers/settings_handler.go:435-465` | Settings risk summary aggregates recent credential audit events. |
| `backend/internal/api/handlers/settings_handler.go:468-535` | Existing high-risk credential audit action list and action labels. |

### Data model and writer behavior

`CredentialAuditEvent` already stores the fields needed for list/export:

- IDs: `id`, `user_id`, `ssh_key_id`, `node_id`, `task_id`, `task_run_id`, `policy_id` (`models.go:425-437`).
- Actor/context: `username`, `role`, `client_ip`, `user_agent`, `created_at` (`models.go:427-443`).
- Event attributes: `action`, `purpose`, `credential_kind`, `credential_source`, `outcome`, `error_message`, `metadata` (`models.go:429-440`).

The model comment is load-bearing: it says the table “must never contain raw secrets, terminal streams, command output, or executor config” (`models.go:421-423`). The writer enforces this for newly written events, but a list/export handler should still avoid trusting the raw `metadata` string blindly.

The writer sanitizes fields before persisting:

- Default outcome is `success` when empty (`audit.go:148-151`).
- Metadata is marshaled from `sanitizeMetadata` (`audit.go:152-156`).
- Scalar fields are passed through `util.SanitizeMessage` and bounded (`audit.go:157-175`).
- Metadata denied keys include `private`, `password`, `token`, `secret`, `credential`, `config`, `output`, `stream`, `command`, `content`, `payload` (`audit.go:263-271`).
- Metadata denied values include the same plus `bearer`, `authorization:` (`audit.go:273-281`).

Existing tests directly assert secret safety:

- Raw command output in `ErrorMessage` is replaced with `[REDACTED_OUTPUT]` (`audit_test.go:51-84`).
- Sensitive metadata keys/values are dropped while safe fields like `format` remain (`audit_test.go:86-161`).

### Existing generic audit list/export pattern

`AuditHandler` is the closest backend template:

- `List` builds a GORM query, parses pagination with `parsePagination`, counts total, applies pagination, then returns `respondPaginated` (`audit_handler.go:45-65`).
- `ExportCSV` reuses the same filter builder, caps export size to 5000, sets `Content-Type: text/csv; charset=utf-8`, sets `Content-Disposition`, writes UTF-8 BOM, writes headers/rows via `encoding/csv` (`audit_handler.go:84-140`).
- Filters are exact-match or simple transforms: `username`, `role`, uppercase `method`, path `LIKE`, `status_code`, `user_id`, RFC3339 `from`/`to` (`audit_handler.go:142-190`).
- Pagination helper lives in `helpers.go:235-289`: `page/page_size`, legacy `limit/offset`, sort allowlist, default sort order `desc`.
- `respondPaginated` envelope shape is `code`, `message`, `data`, `total`, `page`, `page_size` (`response.go:18-52`).

### Existing admin/RBAC pattern

Routes that are explicitly admin-only use `middleware.RequireRole("admin")`, e.g. settings and config export/import (`router.go:308-314`). Generic audit uses `RBAC("audit:read")` (`router.go:257-258`), and the current RBAC map only grants `audit:read` to admin (`rbac.go:10-43`). For P1c’s explicit “admin-only” acceptance criterion, the most literal existing pattern is `RequireRole("admin")`.

### Existing credential audit producers

Credential audit records are already produced by many surfaces. Useful action/purpose examples:

| Producer | Action(s) / Purpose | Files |
|---|---|---|
| SSH key export | `ssh_key.export`, `ssh_key_export` | `backend/internal/api/handlers/ssh_key_handler.go:714-727` |
| SSH key test connection | `ssh_key.test_connection` | `backend/internal/api/handlers/ssh_key_handler.go:563-568` |
| Config export | `config.export`, `config_export` | `backend/internal/api/handlers/config_handler.go:65-75`, `228-243` |
| File browser | `file_browser.list`, `file_browser.preview`, `file_browser` | `backend/internal/api/handlers/file_handler.go:333-352` |
| Docker volumes | `docker_volumes.discover`, `docker_volumes` | `backend/internal/api/handlers/docker_handler.go:92-115` |
| Node Doctor | `node.doctor.run`, `node_test` | `backend/internal/api/handlers/node_doctor_handler.go:176-206` |
| Node migration preflight | `node_migration.preflight`, `node_migration` | `backend/internal/api/handlers/node_migrate_preflight_handler.go:397-422` |
| Terminal | `terminal.open`, `terminal.failure`, `terminal.close` | `backend/internal/api/handlers/terminal_handler.go:134-146`, action sites in `248-524` |
| Task triggers | `task.manual_trigger`, `task.restore_trigger`, `task.batch_trigger` | `backend/internal/api/handlers/task_handler.go:455-488`, `602-639`, `716-727` |
| Batch command | `batch_command.create`, `batch_command` | `backend/internal/api/handlers/batch_handler.go:150-163` |
| Recovery drill | `drill.trigger`, `drill.phase` | `backend/internal/api/handlers/policy_handler.go:944-969`, `backend/internal/task/drill.go:565-585` |
| Probes/metrics | `probe.ssh`, `probe.metrics` | `backend/internal/probe/prober.go:154-156`, `304-390` |
| Node logs | `node_logs.collect` | `backend/internal/nodelogs/ssh_runner.go:86-112` |
| Task runtime credential use | `task.credential.use` | `backend/internal/task/executor/ssh_connect.go:101-140`, `backend/internal/task/executor/executor.go:557-588` |

Settings’ security-risk summary currently treats these as high-risk actions: `ssh_key.export`, `file_browser.list`, `file_browser.preview`, `docker_volumes.discover`, `config.export`, `node.doctor.run`, `node_migration.preflight`, `probe.ssh`, `probe.metrics`, `node_logs.collect`, `terminal.open`, `terminal.failure`, `task.manual_trigger`, `task.restore_trigger`, `task.batch_trigger`, `task.credential.use`, `batch_command.create`, `drill.trigger`, `drill.phase` (`settings_handler.go:468-489`). It labels them in Chinese in `credentialActionLabel` (`settings_handler.go:492-535`).

### Backend files to add/modify for P1c

| File Path | Expected role |
|---|---|
| `backend/internal/api/handlers/credential_audit_handler.go` | New list/export handler, modeled after `audit_handler.go` but using `model.CredentialAuditEvent` and a safe response DTO. |
| `backend/internal/api/handlers/credential_audit_handler_test.go` | Handler tests for filters, pagination, CSV/JSON export, safe metadata rendering/export. |
| `backend/internal/api/router.go` | Instantiate/register new routes near generic audit routes. |
| `backend/internal/api/router_test.go` or new `backend/internal/api/credential_audit_rbac_test.go` | Route registration and admin-vs-nonadmin RBAC. |
| `backend/internal/api/handlers/settings_handler.go` | Optional source for action allowlist/labels if the handler exports action metadata; no required schema change found. |

No migration appears necessary for list/export because `credential_audit_events` already exists with indexed filter columns.

## 2. Existing frontend UI/API patterns

### Files found

| File Path | Description |
|---|---|
| `web/src/lib/api/audit-api.ts:1-129` | Generic audit API client, snake_case mapper, paginated request, CSV blob export. |
| `web/src/pages/audit-page.tsx:17-179` | Audit page state, time-range filters, load/export functions. |
| `web/src/pages/audit-page.tsx:181-364` | Audit page UI: cards/table, mobile cards, filters, badges, pagination, empty state. |
| `web/src/pages/audit-page.test.tsx:85-283` | Audit page tests for filters, pagination, empty state, CSV export success/errors. |
| `web/src/types/domain.ts:558-569` | Existing `AuditLogRecord` domain type. |
| `web/src/lib/api/core.ts:94-165` | `request<T>()` envelope unwrapping and `ApiError`. |
| `web/src/lib/api/core.ts:167-187` | `fetchWithFallback` for blob/download endpoints. |
| `web/src/lib/api/core.ts:189-210` | `PaginatedEnvelope` and `unwrapPaginated`. |
| `web/src/lib/api/client.ts:1-67` | Central API client composition. |
| `web/src/router-pages.tsx:29-31` | Lazy audit page export pattern. |
| `web/src/router.tsx:97-100` | Audit route registration pattern. |
| `web/src/components/layout/navigation.ts:38-150` | Sidebar/mobile nav item definitions and `adminOnly` filtering. |
| `web/src/components/layout/desktop-sidebar.tsx:22-103` | Desktop sidebar renders `getVisibleNavItems(role)`. |
| `web/src/components/layout/mobile-navigation.tsx:25-34`, `199-220` | Mobile drawer renders role-filtered nav items. |
| `web/src/components/ui/dialog.tsx:83-120` | Radix dialog title/close-button primitives; title required by spec. |
| `web/src/components/config-export-import.tsx:20-37` | JSON download via `Blob` + object URL. |
| `web/src/components/ssh-key-export-dialog.tsx:130-152` | Direct `fetch` file download with auth header and selectable format/scope. |
| `web/src/lib/api/ssh-keys-api.ts:199-206` | Export URL builder for SSH key downloads. |
| `web/src/i18n/locales/zh.ts:1334-1366` | Existing audit page i18n keys. |
| `web/src/i18n/locales/en.ts:1334-1366` | Existing audit page i18n keys. |
| `web/src/lib/api/settings-api.ts:26-124` | Security-risk mapper pattern with private raw types, finite fallback, enum normalization. |
| `web/src/lib/api/settings-api.test.ts:4-76` | Mapper test pattern for unknown code/severity and numeric fallback. |

### Generic audit UI/API pattern

The existing audit API keeps raw wire types private and maps snake_case to camelCase:

- `AuditLogResponse` is local to `audit-api.ts` (`audit-api.ts:5-16`).
- `mapAuditLog` maps `user_id`, `status_code`, `client_ip`, `user_agent`, `created_at` into `AuditLogRecord` (`audit-api.ts:18-31`).
- `buildAuditQuery` serializes filters to `URLSearchParams` (`audit-api.ts:33-75`).
- `getAuditLogs` uses `request<PaginatedEnvelope<...>>()` and `unwrapPaginated` (`audit-api.ts:77-98`).
- `exportAuditLogsCSV` uses `fetchWithFallback`, checks `response.ok`, parses error detail, then returns a `Blob` (`audit-api.ts:100-127`).

The existing audit page supplies the UI template for P1c:

- Time ranges are local finite values (`all | 1h | 24h | 7d | 30d`) converted to RFC3339 ISO strings (`audit-page.tsx:19-58`).
- It handles token absence, 403, generic error toasts (`audit-page.tsx:97-127`, `129-164`).
- Export uses `URL.createObjectURL`, `a.download`, `URL.revokeObjectURL` (`audit-page.tsx:147-154`).
- UI uses labeled/aria-labeled filter controls, `Button`, `Badge`, `EmptyState`, `Pagination`, responsive mobile cards plus desktop table (`audit-page.tsx:181-364`).
- Tests already mock `apiClient`, auth context, toasts, object URLs, and link click (`audit-page.test.tsx:47-68`, `206-283`).

### Navigation/router pattern

`navigation.ts` has `adminOnly?: boolean` and filters with `!item.adminOnly || role === "admin"` (`navigation.ts:23-30`, `148-150`). Existing admin-only nav items include automation rules and credentials (`navigation.ts:89-95`, `131-138`). The current generic audit nav item is **not** admin-only (`navigation.ts:111-116`), so a new P1c frontend surface that must be hidden from non-admin normal navigation should either:

1. add a dedicated `adminOnly: true` nav item/route (e.g. `/app/credential-audit`), or
2. render an admin-only sub-entry from inside an existing admin-only container.

A dedicated route is the lower-risk match to the acceptance criterion because the current `/app/audit` item is visible to all roles.

### Frontend files to add/modify for P1c

| File Path | Expected role |
|---|---|
| `web/src/types/domain.ts` | Add `CredentialAuditEventRecord`, outcome/action/domain metadata types. |
| `web/src/lib/api/credential-audit-api.ts` | New API module with raw private wire types, mapper, list query builder, export blob methods. |
| `web/src/lib/api/credential-audit-api.test.ts` | Mapper/query tests for snake_case, nullable IDs, unknown action/outcome, metadata sanitization/defaults. |
| `web/src/lib/api/client.ts` | Spread `createCredentialAuditApi()` into `apiClient`. |
| `web/src/pages/credential-audit-page.tsx` | New admin-only browsing/filtering/export UI, borrowing `AuditPage` table/card/export patterns. |
| `web/src/pages/credential-audit-page.test.tsx` | UI tests for filters, pagination, detail view, export, 403 handling. |
| `web/src/router-pages.tsx` | Lazy export for the new page. |
| `web/src/router.tsx` | Register `/app/credential-audit` route or chosen path. |
| `web/src/components/layout/navigation.ts` | Add admin-only nav item under `observe` if using a dedicated route. |
| `web/src/components/layout/navigation.test.ts`, `mobile-navigation.test.tsx` | Update/verify admin-only visibility if nav changes affect snapshots/assertions. |
| `web/src/i18n/locales/zh.ts`, `web/src/i18n/locales/en.ts` | Add page labels, filter labels, empty/export/detail strings, action/outcome labels. |

## 3. Proposed endpoint contract and filters

### Backend route contract

Recommended route names mirror the existing resource name and avoid overloading generic audit logs:

| Method | Path | Middleware | Purpose |
|---|---|---|---|
| `GET` | `/api/v1/credential-audit-events` | `AuthMiddleware`, `AuditLogger`, `APIRateLimit`, `MaxBodySize`, `RequireRole("admin")` | Paginated safe list. |
| `GET` | `/api/v1/credential-audit-events/export` | same | Download filtered export. |

The list handler can follow `AuditHandler.List` exactly: `buildQuery`, `parsePagination`, `Count`, `applyPagination`, DTO mapping, `respondPaginated`.

### Suggested list query parameters

| Query param | Type | Existing backing field / behavior |
|---|---|---|
| `page` | int | `parsePagination` (`helpers.go:245-282`). |
| `page_size` | int | Default 50; `parsePagination` currently caps at 500. |
| `sort_by` | string | Allowlist: `id`, `created_at`, `username`, `role`, `action`, `purpose`, `credential_kind`, `outcome`. |
| `sort_order` | `asc|desc` | Existing helper defaults to `desc`. |
| `username` | string | Exact `username = ?`, like generic audit (`audit_handler.go:145-147`). |
| `role` | string | Exact `role = ?`. |
| `user_id` | uint | Exact `user_id = ?`, parse like `audit_handler.go:164-168`. |
| `action` | string | Exact `action = ?`; action list can be sourced from settings labels (`settings_handler.go:468-535`). |
| `purpose` | string | Exact `purpose = ?`. |
| `credential_kind` | string | Exact `credential_kind = ?`. |
| `credential_source` | string | Exact or prefix/contains. If using contains, escape `%`/`_` like `audit_handler.go:154-157`. |
| `outcome` | `success|failure|blocked` | Exact `outcome = ?`; unknown input can be ignored or treated as no filter to match current handler style. |
| `ssh_key_id` | uint | Exact nullable ID field. |
| `node_id` | uint | Exact nullable ID field. |
| `task_id` | uint | Exact nullable ID field. |
| `task_run_id` | uint | Exact nullable ID field. |
| `policy_id` | uint | Exact nullable ID field. |
| `from` | RFC3339 | `created_at >= ?`, reuse `parseRFC3339` (`audit_handler.go:170-190`). |
| `to` | RFC3339 | `created_at <= ?`. |

### Suggested list response DTO

Do not return `model.CredentialAuditEvent` directly because `Metadata` is a raw JSON string. Return a handler DTO with parsed/safe metadata:

```go
type credentialAuditEventResponse struct {
    ID               uint           `json:"id"`
    UserID           uint           `json:"user_id"`
    Username         string         `json:"username"`
    Role             string         `json:"role"`
    Action           string         `json:"action"`
    Purpose          string         `json:"purpose"`
    CredentialKind   string         `json:"credential_kind"`
    CredentialSource string         `json:"credential_source"`
    SSHKeyID         *uint          `json:"ssh_key_id,omitempty"`
    NodeID           *uint          `json:"node_id,omitempty"`
    TaskID           *uint          `json:"task_id,omitempty"`
    TaskRunID        *uint          `json:"task_run_id,omitempty"`
    PolicyID         *uint          `json:"policy_id,omitempty"`
    Outcome          string         `json:"outcome"`
    ErrorMessage     string         `json:"error_message,omitempty"`
    Metadata         map[string]any `json:"metadata"`
    ClientIP         string         `json:"client_ip"`
    UserAgent        string         `json:"user_agent"`
    CreatedAt        time.Time      `json:"created_at"`
}
```

`Metadata` should be `{}` for empty/invalid/non-object JSON. The mapping layer should drop unsafe metadata keys/values again before response/export, even though the writer sanitizes at insert time.

### Suggested export contract

Use the same `buildQuery` as list, like generic audit export (`audit_handler.go:84-103`).

| Query param | Behavior |
|---|---|
| All list filters | Same semantics as list. |
| `format` | `csv` default; `json` optional if implementing both CSV/JSON. |
| `page_size` or `limit` | Default 1000, max 5000, matching generic audit export (`audit_handler.go:87-100`). |

CSV response should mirror generic audit export:

- `Content-Type: text/csv; charset=utf-8`
- `Content-Disposition: attachment; filename="credential-audit-events-YYYYMMDD-HHMMSS.csv"`
- UTF-8 BOM for Excel (`audit_handler.go:113-116`)
- `encoding/csv` writer

Suggested CSV columns:

```text
id,created_at,username,role,action,purpose,credential_kind,credential_source,outcome,user_id,ssh_key_id,node_id,task_id,task_run_id,policy_id,client_ip,user_agent,error_message,metadata
```

`metadata` should be compact JSON generated from the safe metadata map, not the raw DB string.

For JSON export, two existing patterns are available:

- Config export returns a JSON envelope through `respondOK` and frontend creates a file (`config_handler.go:245-255`, `config-export-import.tsx:20-29`).
- SSH key export returns a direct downloadable JSON response with `Content-Disposition` (`ssh_key_handler.go:743-749`).

For a download UI, the direct blob approach is consistent with `audit-api.ts:100-127`; a JSON attachment can be returned as `application/json; charset=utf-8` with `Content-Disposition` and the same safe DTO rows plus `exported_at`/`filters` metadata.

## 4. Secret-safety constraints and metadata rendering/export rules

### Related specs

| Spec | Relevant constraint |
|---|---|
| `.trellis/spec/backend/logging-guidelines.md:68-82` | Do not log passwords, private keys, TOTP secrets, JWTs, recovery codes, encryption keys, SMTP/webhook/bearer tokens, raw endpoints, decrypted values, command output, exported config payloads, file contents, Docker output/volume names, Doctor evidence, executor config, or credential-audit metadata that may contain raw remote evidence. |
| `.trellis/spec/backend/error-handling.md:71-74` | Do not expose raw SQL, encryption details, SSH private keys, tokens, command output, file content, Docker output, diagnostic evidence, exported config payloads, or stack-like details to clients. |
| `.trellis/spec/frontend/type-safety.md:21-24` | Cross-module domain types live in `domain.ts`; raw wire types should stay private to API modules. |
| `.trellis/spec/frontend/type-safety.md:61-70` | Do not use `any`, do not pass raw snake_case API objects into components, normalize at API boundary. |
| `.trellis/spec/frontend/type-safety.md:204-225` | SSH key scope UI must not show hostnames, username+host details, private keys, passwords, credential audit metadata, or mutation actions from risk cards. |
| `.trellis/spec/frontend/type-safety.md:264-314` | Settings risk summary must preserve P1 credential-operation codes, render backend-provided advisory text as-is, and not enrich with hostnames/credentials/raw endpoints/credential-audit metadata/remediation links. |
| `.trellis/spec/frontend/a11y-guidelines.md:21-49` | Icons need `aria-hidden`, icon-only buttons need accessible names, form controls need labels, Radix dialogs need `DialogTitle`. |
| `.trellis/spec/guides/cross-layer-thinking-guide.md:20-49` | Define exact formats across DB/service/API/frontend boundaries before implementation. |

### Existing tests that define secret-safety boundaries

| Test | Constraint |
|---|---|
| `backend/internal/api/handlers/config_handler_test.go:143-249` | Default config export omits secrets and credential audit metadata does not contain exported payload/key material. |
| `backend/internal/api/handlers/config_handler_test.go:251-347` | Sensitive config export is admin-only; blocked/success audit metadata does not contain sensitive payload/field names. |
| `backend/internal/api/handlers/file_handler_validate_test.go:328-378` | File browser audit must not persist raw path/content/output; safe `path_hash`/counts only. |
| `backend/internal/api/handlers/docker_handler_test.go:31-66` | Docker audit must not persist remote output or volume names; safe count/warning/stage only. |
| `backend/internal/api/handlers/node_doctor_handler_test.go:162-180` | Doctor audit must not copy diagnostic evidence, host, node name, or sensitive words into metadata/error. |
| `backend/internal/api/handlers/node_handler_test.go:259-319` | Migration preflight audit must not copy diagnostic host/path/evidence; safe IDs/counts only. |
| `backend/internal/api/handlers/settings_handler_test.go:147-229` | Security-risk summary should not expose seeded fake secret values, file names/content/output, Docker output/volume names, or host IPs. |

### Rendering/export rules for P1c

1. **Never expose raw `model.CredentialAuditEvent.Metadata` as a string.** Parse JSON and emit/render only a safe object.
2. **Reapply output-time metadata filtering.** Drop metadata keys containing `private`, `password`, `token`, `secret`, `credential`, `config`, `output`, `stream`, `command`, `content`, `payload`; drop string values containing those markers plus `bearer` or `authorization:`. This mirrors `credentialaudit/audit.go:263-281`.
3. **Only render safe scalar/list metadata.** Keep strings, booleans, finite numbers, and arrays of safe strings. For unknown objects, either omit or stringify only after the same denied-value checks and length bounds.
4. **Bound displayed/exported metadata.** Writer uses max 16 entries and field bounds; the output mapper should keep this bounded behavior for legacy/corrupt rows.
5. **Treat `error_message` as potentially sensitive legacy data.** New writes sanitize it (`audit.go:291-310`), but exports/details should not add raw remote evidence. If implementing an output sanitizer, use existing `util.SanitizeMessage` plus output-marker redaction semantics.
6. **Display IDs, not enriched resource secrets.** It is safe to show `node_id`, `ssh_key_id`, `task_id`, `task_run_id`, `policy_id`; do not enrich the UI with node hostnames, username+host strings, private key names plus host details, raw endpoints, or executor config.
7. **No mutation/remediation actions from event details.** The P1c page is for review/export; avoid buttons that disable/delete/rotate/re-scope keys or mutate nodes/tasks from the credential audit detail view.
8. **Action labels may be humanized, but raw action should remain visible safely.** Existing backend labels in `credentialActionLabel` are Chinese-only and settings-specific; frontend can define i18n labels while preserving a safe raw action string for filtering/export.
9. **Frontend mappers must normalize unknown values.** Suggested domain: `CredentialAuditOutcome = "success" | "failure" | "blocked" | "unknown"`; unknown outcomes map to `unknown`, not `success`. Known actions can map to a union plus `other` while preserving `rawAction` for display/export.
10. **Detail UI should use an accessible dialog or disclosure.** If using Radix dialog, include `DialogTitle` and `DialogDescription`; decorative icons need `aria-hidden`.

## 5. Test targets and verification commands

### Backend test targets

| File | Tests to add/update |
|---|---|
| `backend/internal/api/handlers/credential_audit_handler_test.go` | List filters for `action`, `purpose`, `outcome`, `username`, `user_id`, `ssh_key_id`, `node_id`, `task_id`, `task_run_id`, `policy_id`, `from`, `to`; pagination/sort; invalid numeric filters ignored or safely handled consistent with existing handlers. |
| `backend/internal/api/handlers/credential_audit_handler_test.go` | CSV export uses same filters, sets `text/csv`, writes safe header, caps at 5000, includes BOM, excludes unsafe metadata/error content. |
| `backend/internal/api/handlers/credential_audit_handler_test.go` | JSON export, if added, returns/downloads safe DTO rows and not raw metadata string. |
| `backend/internal/api/handlers/credential_audit_handler_test.go` | Invalid/legacy metadata JSON maps to `{}`; metadata with forbidden keys/values is dropped from list/export output. |
| `backend/internal/api/router_test.go` | Route registration for `GET /api/v1/credential-audit-events` and `/export`. |
| `backend/internal/api/credential_audit_rbac_test.go` or `router_test.go` | Admin can access list/export; operator/viewer receive 403. Pattern can follow `app_credential_rbac_test.go:94-233` or `router_test.go:107-153`. |
| `backend/internal/credentialaudit/audit_test.go` | Only needed if sanitizer is exported/changed; existing writer tests already cover core persistence safety. |

### Frontend test targets

| File | Tests to add/update |
|---|---|
| `web/src/lib/api/credential-audit-api.test.ts` | Raw snake_case response maps to camelCase domain fields; nullable IDs remain `undefined/null` safely; numeric fields never become `NaN`. |
| `web/src/lib/api/credential-audit-api.test.ts` | Unknown `outcome` maps to `unknown`; unknown action maps to safe fallback while preserving safe raw action text if desired. |
| `web/src/lib/api/credential-audit-api.test.ts` | Metadata mapper accepts object/JSON-string/missing values and drops unsafe keys/values (`private_key`, `password`, `token`, `command`, `content`, `payload`, output markers). |
| `web/src/lib/api/credential-audit-api.test.ts` | Query builder serializes filters to backend names (`ssh_key_id`, `task_run_id`, `credential_kind`, `page_size`, etc.). |
| `web/src/pages/credential-audit-page.test.tsx` | Initial load, filter changes, time range, pagination, empty state, refresh. Reuse mocking style from `audit-page.test.tsx:47-68`. |
| `web/src/pages/credential-audit-page.test.tsx` | Detail view renders safe metadata and does not render seeded secret/path/output strings. |
| `web/src/pages/credential-audit-page.test.tsx` | CSV/JSON export calls API with current filters and handles success, 403, generic errors like `audit-page.test.tsx:206-283`. |
| `web/src/components/layout/navigation.test.ts`, `mobile-navigation.test.tsx` | If adding a nav item, assert it is visible for admin and hidden for operator/viewer via `adminOnly`. |
| `web/src/i18n/locales/{zh,en}.ts` | Keep new keys in both locales; `npm run check` will catch syntax/type issues. |

### Verification commands

Targeted backend checks:

```bash
cd /Users/weibo/Code/xirang/backend && go test ./internal/credentialaudit ./internal/api/handlers ./internal/api -count=1
```

Full backend check:

```bash
cd /Users/weibo/Code/xirang/backend && go test ./... -count=1
```

Frontend targeted checks can use Vitest for the new API/page tests, then the standard project check:

```bash
cd /Users/weibo/Code/xirang/web && npm run check
```

## Caveats / Not found

- `python3 ./.trellis/scripts/task.py current --source` reported no active current task, but the user supplied the active task path explicitly; research was written under that supplied path.
- No existing backend credential audit list/export handler was found.
- No existing frontend credential audit event UI/API module was found; only generic audit log UI/API and Settings risk-summary aggregate references exist.
- No external docs search was needed; this task is internal code-surface research.
