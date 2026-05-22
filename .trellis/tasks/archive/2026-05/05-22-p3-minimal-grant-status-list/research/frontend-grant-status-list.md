# Research: Frontend minimal credential access grant status/list UI

- **Query**: Research the frontend implementation approach for a minimal admin-only credential access grant status/list UI in the Xirang repo. Focus on existing credential audit page/API patterns, navigation visibility for admin-only pages, API client pagination/filter patterns, domain typing for `CredentialAccessGrant`, i18n/test patterns, and safe fields/storage constraints.
- **Scope**: internal
- **Date**: 2026-05-22

## Findings

### Files Found

| File Path | Description |
|---|---|
| `web/src/types/domain.ts` | Defines `CredentialAccessGrant` plus action/purpose/status unions and credential audit domain types. |
| `web/src/lib/api/credential-access-grants-api.ts` | Existing grant creation API wrapper and snake_case-to-camelCase mapper for grant DTOs. |
| `web/src/lib/api/credential-access-grants-api.test.ts` | Mapper/request tests for grant creation APIs and fallback behavior. |
| `web/src/lib/api/credential-audit-api.ts` | Closest credential-related paginated admin list API wrapper; query serialization, `PaginatedEnvelope`, mapper sanitization, and CSV export. |
| `web/src/lib/api/credential-audit-api.test.ts` | Tests for credential audit mapping, redaction, query serialization, and export error handling. |
| `web/src/pages/credential-audit-page.tsx` | Closest admin-only credential-related list UI: filters, stats, table/card layout, pagination, details dialog, CSV export, and page-local admin redirect. |
| `web/src/pages/credential-audit-page.test.tsx` | Page tests for filters, pagination, safe detail dialog content, CSV export, 403 handling, and non-admin direct access. |
| `web/src/pages/audit-page.tsx` | Similar paginated audit list/export UI pattern. |
| `web/src/components/layout/navigation.ts` | Canonical nav registry with `adminOnly` and `getVisibleNavItems(role)`. |
| `web/src/components/layout/navigation.test.ts` | Tests that admin-only nav entries are visible for admin and hidden for operator/viewer. |
| `web/src/components/layout/desktop-sidebar.tsx` | Desktop navigation consumer of `getVisibleNavItems(role)`. |
| `web/src/components/layout/mobile-navigation.tsx` | Mobile tab navigation consumer of `getVisibleNavItems(role)`. |
| `web/src/pages/more-page.tsx` | Mobile “More” navigation consumer of `getVisibleNavItems(role)`. |
| `web/src/components/ui/command-palette.tsx` | Command palette navigation consumer of `getVisibleNavItems(role)`. |
| `web/src/components/ui/command-palette.test.tsx` | Test pattern for alternate navigation surfaces hiding admin-only entries. |
| `web/src/router.tsx` | `/app` route tree and existing lazy route registration for credential audit. |
| `web/src/router-pages.tsx` | Lazy page exports for route-level pages. |
| `web/src/components/protected-route.tsx` | Auth-only route guard; it does not enforce role-based access. |
| `web/src/lib/api/client.ts` | API client composition; includes credential audit and credential access grants API factories. |
| `web/src/lib/api/core.ts` | Shared request/envelope handling, `PaginatedEnvelope`, `unwrapPaginated`, and grant-required error helpers. |
| `web/src/components/ui/pagination.tsx` | Shared pagination component used by audit-style pages. |
| `web/src/i18n/index.ts` | i18n setup with lazy-loaded `zh`/`en` locale modules and `<html lang>` sync. |
| `web/src/i18n/locales/en.ts` | English locale keys, including `credentialAudit`. |
| `web/src/i18n/locales/zh.ts` | Chinese locale keys, including `credentialAudit`. |
| `web/src/lib/date-utils.ts` | Shared timestamp formatting utilities such as `formatTime`. |
| `web/src/components/web-terminal.tsx` | Existing terminal grant prompt pattern: local reason state, step-up proof, grant request, retry, and no grant persistence. |
| `web/src/components/web-terminal.test.tsx` | Storage-safety and grant-required prompt tests. |
| `web/src/components/config-export-import.tsx` | Existing admin config import/export grant prompt flow. |
| `web/src/components/snapshot-browser.tsx` | Existing snapshot restore grant flow. |
| `web/src/components/restore-confirm-dialog.tsx` | Existing task restore grant flow with one-shot step-up proof and local reason validation. |
| `backend/internal/api/router.go` | Current backend route inventory for credential audit list/export and credential access grant creation endpoints. |
| `backend/internal/model/models.go` | Backend `CredentialAccessGrant` model and safe-field comment. |
| `backend/internal/api/handlers/credential_access_grant.go` | Backend grant DTO, status/action/purpose constants, creation handlers, reason sanitization, and active grant matching. |
| `backend/internal/api/handlers/credential_audit_handler.go` | Backend credential audit paginated list/export handler used by the frontend credential audit page. |
| `backend/internal/api/handlers/helpers.go` | Backend pagination query semantics (`page`, `page_size`, sort whitelist). |
| `backend/internal/api/handlers/response.go` | Backend paginated response envelope shape. |
| `backend/internal/database/migrations/sqlite/000060_credential_access_grants.up.sql` | SQLite grant table/index columns. |
| `backend/internal/database/migrations/postgres/000060_credential_access_grants.up.sql` | PostgreSQL grant table/index columns. |
| `.trellis/spec/frontend/type-safety.md` | Frontend grant DTO/domain mapping and storage-safety contract. |
| `.trellis/spec/frontend/state-management.md` | Frontend server state and credential grant prompt state contracts. |
| `.trellis/spec/frontend/quality-guidelines.md` | Frontend test/a11y expectations for new pages/dialogs. |
| `.trellis/spec/frontend/a11y-guidelines.md` | `runAxe` smoke test pattern for pages and portals. |
| `.trellis/spec/backend/quality-guidelines.md` | Backend credential access grant safe-field and storage contract. |

### Code Patterns

#### Existing grant domain type

- `CredentialAccessGrant` is already present as a frontend domain type with camelCase fields: requester and approver IDs/labels, action/purpose/status, optional resource IDs, reason, TTL, lifecycle timestamps, and revocation fields (`web/src/types/domain.ts:603`).
- Action union values are `terminal.open`, `config.import`, `config.export`, `snapshot.restore`, `task.restore_trigger`, and `unknown`; purpose union values are `terminal`, `config_import`, `config_export`, `snapshot`, `task_restore`, and `unknown`; status union values are `requested`, `approved`, `active`, `denied`, `expired`, and `revoked` (`web/src/types/domain.ts:603`).
- Existing grant code uses the domain type in components/API wrappers rather than exposing raw snake_case DTOs to pages (`web/src/lib/api/credential-access-grants-api.ts:1`).

#### Existing grant API wrapper

- `CredentialAccessGrantResponse` is private to `credential-access-grants-api.ts` and mirrors backend snake_case fields such as `requester_user_id`, `node_id`, `task_id`, `requested_ttl_seconds`, `approved_at`, and `revoked_by_user_id` (`web/src/lib/api/credential-access-grants-api.ts:4`).
- `mapCredentialAccessGrant` maps all wire fields to the camelCase `CredentialAccessGrant` domain shape and uses finite numeric fallbacks plus positive optional IDs (`web/src/lib/api/credential-access-grants-api.ts:67`).
- Unknown grant actions and purposes map to `unknown`, and unknown statuses map to the non-authorizing fallback `expired` (`web/src/lib/api/credential-access-grants-api.ts:42`, `web/src/lib/api/credential-access-grants-api.ts:47`, `web/src/lib/api/credential-access-grants-api.ts:52`).
- Existing grant creation methods call `request<CredentialAccessGrantResponse>()`, send `Authorization` via `token`, send step-up proof through request options, include JSON bodies with backend snake_case fields, and return mapped grant domain objects (`web/src/lib/api/credential-access-grants-api.ts:94`).
- Current frontend grant creation methods cover terminal, config import, config export, snapshot restore, and task restore. They use these paths: `POST /credential-access-grants/terminal`, `/config-import`, `/config-export`, `/snapshot-restore`, and `/task-restore` (`web/src/lib/api/credential-access-grants-api.ts:101`, `web/src/lib/api/credential-access-grants-api.ts:119`, `web/src/lib/api/credential-access-grants-api.ts:136`, `web/src/lib/api/credential-access-grants-api.ts:153`, `web/src/lib/api/credential-access-grants-api.ts:171`).
- The composed `apiClient` already includes both `createCredentialAuditApi()` and `createCredentialAccessGrantsApi()` (`web/src/lib/api/client.ts:4`, `web/src/lib/api/client.ts:50`).

#### Closest existing paginated credential list API

- `CredentialAuditQueryOptions` models string, numeric, time-range, pagination, and sort options using camelCase property names (`web/src/lib/api/credential-audit-api.ts:32`).
- `buildCredentialAuditQuery` serializes frontend camelCase fields to backend snake_case query params such as `credential_kind`, `credential_source`, `ssh_key_id`, `task_run_id`, `page_size`, `sort_by`, and `sort_order` (`web/src/lib/api/credential-audit-api.ts:281`).
- `getCredentialAuditEvents` calls `request<PaginatedEnvelope<CredentialAuditEventResponse[]>>()`, then `unwrapPaginated`, then maps each row into a domain object (`web/src/lib/api/credential-audit-api.ts:323`).
- `PaginatedEnvelope<T>` in `core.ts` contains `code`, `message`, `data`, `total`, `page`, and `page_size`; `unwrapPaginated` returns `{items,total,page,pageSize}` and defaults `page` to `1` and `pageSize` to `20` (`web/src/lib/api/core.ts:215`).
- The generic audit API uses the same `PaginatedEnvelope`/`unwrapPaginated` pattern for `/audit-logs` (`web/src/lib/api/audit-api.ts:3`).
- Backend pagination helpers accept `page`, `page_size`, legacy `limit`/`offset`, `sort_by`, and `sort_order`, cap page size at 500, and require sort fields to appear in an allowed whitelist (`backend/internal/api/handlers/helpers.go:249`).

#### Existing admin-only credential list page pattern

- `CredentialAuditPage` is the closest page-level model for an admin-only credential-related list/status surface (`web/src/pages/credential-audit-page.tsx:105`).
- The page obtains `token` and `role` from `useAuth()`, keeps rows/total/page/loading/exporting/selected/filter values in local React state, and uses `pageSize = 30` (`web/src/pages/credential-audit-page.tsx:28`, `web/src/pages/credential-audit-page.tsx:105`).
- Numeric filter text is parsed by `positiveFilter`, returning only positive finite numbers or `undefined` (`web/src/pages/credential-audit-page.tsx:100`).
- Time presets are converted to RFC3339 `from`/`to` values by `resolveTimeRange` (`web/src/pages/credential-audit-page.tsx:60`).
- `buildFilters(nextPage, exportMode)` constructs typed API options, sets `pageSize` to 30 for normal listing, 5000 for export, and sorts by `created_at desc` (`web/src/pages/credential-audit-page.tsx:146`).
- `load(nextPage)` calls the API wrapper, updates `rows`, `total`, and `page`, and handles 403 with a localized admin-only permission toast (`web/src/pages/credential-audit-page.tsx:171`).
- Auto-load uses a `useRef` key over token/role/filter values to avoid repeated fetch loops; the effect exits and resets the ref when token is missing or role is not admin (`web/src/pages/credential-audit-page.tsx:221`).
- Direct non-admin access is handled page-locally with `<Navigate to="/app/overview" replace />` (`web/src/pages/credential-audit-page.tsx:253`).
- The page renders a mobile card grid, a desktop table with `scope="col"` headers, a shared `Pagination`, and a detail dialog with `DialogTitle`, `DialogDescription`, `DialogBody`, `DialogFooter`, and `DialogCloseButton` (`web/src/pages/credential-audit-page.tsx:401`, `web/src/pages/credential-audit-page.tsx:423`, `web/src/pages/credential-audit-page.tsx:464`, `web/src/pages/credential-audit-page.tsx:468`).
- The detail dialog is read-only and displays safe mapped fields/metadata; page tests assert it does not render dangerous mutation buttons (`web/src/pages/credential-audit-page.tsx:476`, `web/src/pages/credential-audit-page.test.tsx:198`).

#### Route and navigation visibility

- `ProtectedRoute` only checks authentication and redirects unauthenticated users to login; it does not inspect the user role (`web/src/components/protected-route.tsx:6`).
- Existing admin-only page access for credential audit is enforced inside the page, not centrally in `router.tsx` (`web/src/pages/credential-audit-page.tsx:253`).
- Route-level pages are lazy-exported from `router-pages.tsx`; credential audit is exported as `CredentialAuditPage = lazy(() => import("@/pages/credential-audit-page"))` (`web/src/router-pages.tsx:32`).
- `/app/credential-audit` is registered under the protected `/app` route in `router.tsx` and rendered through `LazyPage` (`web/src/router.tsx:102`).
- Navigation visibility is centralized in `getVisibleNavItems(role)`, which filters out `adminOnly` entries unless `role === "admin"` (`web/src/components/layout/navigation.ts:156`).
- Existing admin-only nav items include `/app/automation-rules`, `/app/credential-audit`, and `/app/credentials` (`web/src/components/layout/navigation.ts:89`, `web/src/components/layout/navigation.ts:117`, `web/src/components/layout/navigation.ts:140`).
- Desktop sidebar, mobile navigation, More page, and command palette consume `getVisibleNavItems(role)` rather than reading `navItems` directly (`web/src/components/layout/desktop-sidebar.tsx:22`, `web/src/components/layout/mobile-navigation.tsx:25`, `web/src/pages/more-page.tsx:13`, `web/src/components/ui/command-palette.tsx:23`).
- `navigation.test.ts` verifies admin-only nav entries are visible for admin and hidden for operator/viewer (`web/src/components/layout/navigation.test.ts:4`).
- `command-palette.test.tsx` verifies an alternate navigation surface hides admin-only entries for non-admin roles (`web/src/components/ui/command-palette.test.tsx:73`).

#### i18n and display utilities

- i18n lazily loads `zh` and `en` resources from `web/src/i18n/locales`, falls back to Chinese, and syncs the document language (`web/src/i18n/index.ts:9`, `web/src/i18n/index.ts:53`, `web/src/i18n/index.ts:62`).
- Existing credential audit translations live under the `credentialAudit` namespace in both English and Chinese locale files (`web/src/i18n/locales/en.ts:1370`, `web/src/i18n/locales/zh.ts:1370`).
- Credential audit keys include page title/description, filters, error messages, time ranges, outcome labels, table columns, detail dialog labels, metadata labels, and export messages (`web/src/i18n/locales/en.ts:1371`, `web/src/i18n/locales/zh.ts:1371`).
- Shared timestamp formatting uses `formatTime(input)` to return `YYYY-MM-DD HH:mm:ss`, `-` for missing input, and the raw input string if parsing fails (`web/src/lib/date-utils.ts:6`).
- Credential audit API mapping currently formats `created_at` with `formatTime` before rendering (`web/src/lib/api/credential-audit-api.ts:277`).

#### Test patterns

- Grant API tests mock `request` from `core`, verify full snake_case-to-camelCase mapping, invalid numeric fallback, known tuple preservation, unknown action/purpose/status fallback, and request body/token/step-up proof behavior for each creation endpoint (`web/src/lib/api/credential-access-grants-api.test.ts:1`).
- Credential audit page tests hoist mocked auth/API/toast state, render the page, assert filter options are passed to the API, assert pagination calls page 2 with `pageSize: 30`, assert safe details rendering, assert CSV export behavior, assert 403 export toast, and assert non-admin direct access does not load data (`web/src/pages/credential-audit-page.test.tsx:9`, `web/src/pages/credential-audit-page.test.tsx:117`, `web/src/pages/credential-audit-page.test.tsx:165`, `web/src/pages/credential-audit-page.test.tsx:198`, `web/src/pages/credential-audit-page.test.tsx:223`, `web/src/pages/credential-audit-page.test.tsx:261`, `web/src/pages/credential-audit-page.test.tsx:277`).
- Web terminal grant tests verify grant-required close handling, reason prompt, grant request, retry, sanitized close details, empty reason validation, and that storage does not contain `CREDENTIAL_GRANT_REQUIRED` or the submitted reason (`web/src/components/web-terminal.test.tsx:144`, `web/src/components/web-terminal.test.tsx:175`).
- Frontend quality specs state that new top-level pages or non-trivial dialogs have matching `*.a11y.test.tsx` smoke tests using the `runAxe` helper (`.trellis/spec/frontend/quality-guidelines.md:65`).
- A11y guideline examples use `runAxe(container)` for normal components and `runAxe(document.body)` for portal-rendered dialogs (`.trellis/spec/frontend/a11y-guidelines.md:71`, `.trellis/spec/frontend/a11y-guidelines.md:82`).

#### Existing grant prompt and storage constraints

- Terminal grant prompt state is component-local; a grant-required WebSocket close opens the reason dialog and does not clear step-up proof or call parent disconnect (`web/src/components/web-terminal.test.tsx:144`).
- Task restore grant flow uses local `grantReason`, validates non-empty and max length 240, requests `ensureStepUpProof({ persist: false, reuseCached: false })`, calls `requestTaskRestoreCredentialGrant`, then performs restore with the same proof (`web/src/components/restore-confirm-dialog.tsx:41`, `web/src/components/restore-confirm-dialog.tsx:55`, `web/src/components/restore-confirm-dialog.tsx:69`).
- The frontend state spec says grant rows live on the backend and the frontend must not store grant IDs, grant material, reason text, or grant-required status in `localStorage` or `sessionStorage` (`.trellis/spec/frontend/state-management.md:86`).
- The frontend type-safety spec says raw grant types stay private to the API module, components consume camelCase domain fields, unknown status degrades to a safe non-authorizing value, and reason text must remain bounded and not be enriched with hostnames, endpoints, credentials, command text, or terminal output (`.trellis/spec/frontend/type-safety.md:220`).

### Safe Fields / Storage Constraints

- Backend `model.CredentialAccessGrant` is documented as containing only safe actor/resource identifiers, bounded sanitized reason text, lifecycle state, and timestamps (`backend/internal/model/models.go:446`).
- Backend grant DTO fields are: `id`, requester user/username/role, `action`, `purpose`, optional `node_id`/`task_id`/`policy_id`, `reason`, `status`, `requested_ttl_seconds`, `requested_at`, optional approval fields, `expires_at`, optional revocation fields, `created_at`, and `updated_at` (`backend/internal/api/handlers/credential_access_grant.go:96`).
- SQLite and PostgreSQL migrations store the same scalar grant fields and indexes; no credential, token, proof, command, output, endpoint, proxy, or host field is present in the table definition (`backend/internal/database/migrations/sqlite/000060_credential_access_grants.up.sql:1`, `backend/internal/database/migrations/postgres/000060_credential_access_grants.up.sql:3`).
- Backend grant spec states: “Grants are row-backed authorization records, not bearer tokens. Do not issue a signed grant token or store grant material in client-controlled state” (`.trellis/spec/backend/quality-guidelines.md:467`).
- Backend grant spec also states: “Never store secrets, tokens, step-up proofs, OTP/recovery values, commands, terminal streams, command output, file contents, exported payloads, raw SQL, endpoint/proxy values, or host-sensitive strings in grant rows, responses, audit events, or logs” (`.trellis/spec/backend/quality-guidelines.md:469`).
- Frontend grant storage-safety tests already assert no grant-required marker or submitted reason appears in `localStorage`/`sessionStorage` during the terminal grant flow (`web/src/components/web-terminal.test.tsx:175`).

### Existing Backend Availability Relevant to Frontend

- Current backend routes include admin-only credential audit list/export routes: `GET /credential-audit-events` and `GET /credential-audit-events/export` (`backend/internal/api/router.go:263`).
- Current backend credential access grant routes are creation-only `POST` endpoints for terminal, config import/export, snapshot restore, and task restore; all are behind `middleware.RequireRole("admin")` (`backend/internal/api/router.go:265`).
- No existing backend `GET /credential-access-grants` or status/list route was found in `router.go` or `credential_access_grant.go`.
- No existing frontend list/status method for credential access grants was found in `credential-access-grants-api.ts`; the current wrapper only requests/creates grants (`web/src/lib/api/credential-access-grants-api.ts:94`).

### Related Specs

- `.trellis/spec/frontend/type-safety.md` — Credential access grant mapping contract: raw DTO fields, camelCase domain fields, numeric/status fallback behavior, machine-readable grant-required errors, and tests (`.trellis/spec/frontend/type-safety.md:204`).
- `.trellis/spec/frontend/state-management.md` — Server state pagination wrapper pattern and credential grant prompt storage constraints (`.trellis/spec/frontend/state-management.md:56`, `.trellis/spec/frontend/state-management.md:71`).
- `.trellis/spec/frontend/quality-guidelines.md` — Page/dialog test and a11y smoke expectations (`.trellis/spec/frontend/quality-guidelines.md:65`).
- `.trellis/spec/frontend/a11y-guidelines.md` — `runAxe` usage for components/pages and portal dialogs (`.trellis/spec/frontend/a11y-guidelines.md:71`).
- `.trellis/spec/backend/quality-guidelines.md` — Credential access grant backend contract and forbidden fields (`.trellis/spec/backend/quality-guidelines.md:451`).

### External References

None. This was an internal codebase research task.

## Caveats / Not Found

- No existing frontend credential access grant status/list page was found.
- No existing frontend API method for listing credential access grants was found; `credential-access-grants-api.ts` currently contains creation/request methods only.
- No existing backend `GET /credential-access-grants` route was found; current grant routes are admin-only creation `POST` endpoints.
- Existing credential audit list/export routes and page provide the closest implemented admin-only credential-related list pattern, but they list credential audit events, not grant rows.
