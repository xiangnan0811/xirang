# P1c Credential Audit Listing, Filters, and Export UI

## Goal

Make credential-use audit events directly reviewable by administrators through secret-safe backend list/export APIs and an admin-only frontend UI, so P1/P1b credential-use coverage can be investigated beyond the Settings high-level risk summary.

## Requirements

- Add admin-only backend list and export endpoints for `credential_audit_events`.
- Register dedicated routes under `/api/v1/credential-audit-events` and `/api/v1/credential-audit-events/export` using authenticated admin-only middleware.
- Return a safe response DTO instead of exposing `model.CredentialAuditEvent` directly.
- Support pagination and sorting with an allowlist: `id`, `created_at`, `username`, `role`, `action`, `purpose`, `credential_kind`, and `outcome`.
- Support practical filters: `username`, `role`, `user_id`, `action`, `purpose`, `credential_kind`, `credential_source`, `outcome`, `ssh_key_id`, `node_id`, `task_id`, `task_run_id`, `policy_id`, `from`, and `to`.
- Export filtered credential audit events as CSV using the same filter semantics as the list endpoint, capped to a bounded maximum.
- Ensure list and export output re-sanitize metadata at response time, even though writes are already sanitized.
- Treat invalid, legacy, non-object, or unsafe metadata as empty or partially dropped safe metadata.
- Treat `error_message` as potentially legacy-sensitive and avoid exposing raw remote output, stack-like details, file content, config payloads, terminal streams, or command output.
- Add frontend API mappers that normalize snake_case fields, nullable IDs, unknown outcomes/actions, pagination envelopes, filters, and export blobs.
- Add an admin-only frontend UI surface for credential audit browsing, filtering, pagination, safe detail viewing, refresh, and CSV export.
- Hide the normal frontend navigation path from non-admin roles.
- Reuse existing Audit page patterns for responsive cards/table, empty states, pagination, export affordances, toasts, and accessibility.
- Add i18n strings in both Chinese and English for page title, filters, outcomes, actions, export, empty/error states, and detail labels.

## Acceptance Criteria

- [ ] Admin users can list credential-use audit records with pagination, sorting, and all MVP filters.
- [ ] Admin users can export the currently filtered credential-use audit records as CSV.
- [ ] Non-admin roles receive backend denial for list/export and do not see a normal frontend navigation entry.
- [ ] API responses and exports never include raw private keys, passwords, tokens, decrypted setting values, executor config, terminal streams, command output, file contents, Docker output/volume names, diagnostic evidence, exported config payloads, raw endpoints, or host-sensitive enriched details.
- [ ] Unsafe metadata keys/values are dropped again at response/export time; invalid metadata maps to an empty object.
- [ ] Frontend mappers safely normalize snake_case fields, unknown action/outcome values, nullable IDs, metadata, and pagination.
- [ ] Frontend detail UI renders only safe IDs and safe metadata, without resource mutation/remediation actions.
- [ ] Tests cover backend filters, pagination/sort, RBAC, export safety, frontend mapping, rendering, filter/export behavior, and admin-only navigation visibility.

## Definition of Done

- Backend targeted tests pass for credential audit, handlers, and route/RBAC coverage.
- Full backend tests pass with `go test ./... -count=1`.
- Frontend check passes with `npm run check`.
- Trellis task validation passes.
- Implementation is reviewed by `trellis-check` before commit/PR.

## Technical Approach

Implement a dedicated credential audit resource rather than overloading generic HTTP audit logs.

- Backend:
  - Add `backend/internal/api/handlers/credential_audit_handler.go` modeled after `AuditHandler`.
  - Use a query builder shared by list/export for filters and date range parsing.
  - Use `parsePagination`, `applyPagination`, and `respondPaginated` for list behavior.
  - Use a safe DTO mapper that parses `Metadata` JSON into a bounded map and reapplies the credential-audit forbidden key/value rules.
  - Add CSV export with UTF-8 BOM, `Content-Disposition`, and compact JSON for safe metadata only.
  - Register routes in `backend/internal/api/router.go` near audit routes with admin-only access.
- Frontend:
  - Add `CredentialAuditEventRecord` and related domain types to `web/src/types/domain.ts`.
  - Add `web/src/lib/api/credential-audit-api.ts` with private wire types, mapper, query builder, list, and export methods.
  - Compose the API into `apiClient`.
  - Add a dedicated lazy route/page, likely `/app/credential-audit`, with an `adminOnly` nav item under the observability/security area.
  - Reuse `audit-page.tsx` UI patterns for filters, responsive result presentation, pagination, toasts, CSV download, and errors.

## Decision (ADR-lite)

**Context**: Credential audit events are security-sensitive because they prove credential use across SSH, file, Docker, config export, diagnostic, task, probe, and node-log surfaces. Administrators need searchable/exportable visibility, but the UI/API must not become a second leak path for old or malformed metadata.

**Decision**: Build a dedicated admin-only credential audit list/export API and frontend page with output-time sanitization, safe DTOs, bounded metadata, and no enriched host/credential details or mutation actions.

**Consequences**: This gives admins a focused investigation surface while preserving P1/P1b secret-safety guarantees. The first MVP intentionally stays review/export-only; active remediation and step-up enforcement remain for P1d or later tasks.

## Out of Scope

- No schema migration unless implementation discovers an unavoidable missing index or column.
- No mutation actions from the credential audit page, such as deleting events, rotating keys, disabling keys, changing scopes, deleting nodes, or triggering remediation.
- No resource enrichment with hostnames, username+host strings, raw endpoints, key material, executor config, command text, command output, file paths, file content, Docker volume names, or diagnostic evidence.
- No real-time streaming or WebSocket feed for audit events.
- No dashboard charts or anomaly scoring beyond list/detail/export.
- No step-up authentication changes; those belong to P1d.
- No attempt to retroactively cleanse already persisted database rows beyond output-time sanitization.

## Research References

- [`research/code-surfaces.md`](research/code-surfaces.md) — maps existing backend model/writer/audit patterns, frontend Audit page/API patterns, endpoint/filter contracts, secret-safety rules, and verification targets.

## Technical Notes

- Existing `CredentialAuditEvent` already contains IDs, actor/context fields, action/purpose/kind/source/outcome, error message, metadata, client IP, user agent, and created time.
- Existing writer sanitization is necessary but not sufficient for display/export; response mapping must defensively re-sanitize legacy/corrupt rows.
- Generic `AuditHandler` provides the backend pagination/filter/CSV template.
- Existing frontend `audit-api.ts` and `audit-page.tsx` provide the API mapper, paginated list, CSV export, filter UI, mobile card, desktop table, empty state, pagination, and toast patterns.
- Existing navigation supports `adminOnly` entries; use it so non-admin users do not see the normal credential audit entry.
- Keep raw wire API types private to the credential audit API module and expose camelCase domain records to React components.
- Use IDs rather than enriched names/hosts for linked resources to reduce accidental sensitive detail exposure.
