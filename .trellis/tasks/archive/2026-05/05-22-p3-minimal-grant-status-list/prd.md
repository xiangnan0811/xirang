# P3 minimal grant status list

## Goal

Add a minimal read-only, admin-only credential access grant status/list surface so operators can inspect recent JIT credential grants without adding new approval workflows or expanding grant semantics. This is a transitional P3 visibility slice after the shipped admin-only grant enforcement flows and before operator-owned/multi-resource enforcement work.

## Requirements

### Backend API

- Add an admin-only `GET /api/v1/credential-access-grants` list endpoint under the existing secured API group.
- The endpoint must be read-only and must not create, approve, deny, revoke, refresh, or otherwise mutate grants.
- Reuse existing authentication/RBAC style: primary auth from the secured group plus `RequireRole("admin")` on the route.
- Return the standard paginated response envelope used by existing list handlers: `code`, `message`, `data`, `total`, `page`, and `page_size`.
- Reuse the existing grant DTO field boundary where possible, exposing only safe scalar grant metadata:
  - `id`
  - `requester_user_id`
  - `requester_username`
  - `requester_role`
  - `action`
  - `purpose`
  - `node_id`
  - `task_id`
  - `policy_id`
  - `reason`
  - `status`
  - `requested_ttl_seconds`
  - `requested_at`
  - `approved_at`
  - `approver_user_id`
  - `approver_username`
  - `expires_at`
  - `revoked_at`
  - `revoked_by_user_id`
  - `created_at`
  - `updated_at`
- Support minimal exact-match filters needed for support/debugging:
  - `status`
  - `action`
  - `purpose`
  - `requester_user_id`
  - `requester_username`
  - `requester_role`
  - `node_id`
  - `task_id`
  - `policy_id`
  - `from`
  - `to`
- Interpret `from`/`to` as RFC3339 filters on `created_at`, matching existing audit list behavior.
- Support existing pagination inputs and caps through shared helpers.
- Support an explicit safe sort whitelist: `id`, `created_at`, `updated_at`, `requested_at`, `expires_at`, `status`, `action`, `purpose`, `requester_username`, and `requester_role`.
- Default sorting should surface recent grants first.
- Do not expose credentials, tokens, step-up proofs, OTP/recovery values, imported/exported payloads, command text/output, target paths, endpoint/proxy values, hostnames, raw SQL, file contents, terminal streams, or decrypted secret material.
- Keep output strings bounded/safe for legacy rows loaded from the database, without broadening the persisted grant model.

### Frontend API and UI

- Add a typed frontend list API method for `GET /credential-access-grants` that keeps raw snake_case response types private to the API module and returns camelCase `CredentialAccessGrant` domain objects.
- Preserve existing safe mapper behavior for known grant actions, purposes, and statuses; unknown statuses must remain non-authorizing.
- Add a minimal admin-only read page for credential access grants.
- Expose the page through the existing lazy route/navigation patterns used by other admin-only pages.
- Non-admin users who directly navigate to the page should be redirected away consistently with existing admin-only pages.
- The UI should provide:
  - filter controls for status/action/purpose/resource/requester/date filters,
  - paginated list rendering,
  - desktop table and mobile-friendly card layout where consistent with existing list pages,
  - a read-only detail view for the safe fields above.
- The UI must not add mutation controls such as approve, deny, revoke, refresh, retry, policy edit, reviewer routing, or workflow actions.
- The UI must not persist grant rows, reason text, step-up proofs, filter drafts containing sensitive values, or grant status payloads to `localStorage` or `sessionStorage`.
- Keep UI copy concise and operator-facing; do not document process history in tracked user docs for this slice unless an existing security/admin doc requires a factual API update.

## Acceptance Criteria

- [ ] Admin users can request `GET /api/v1/credential-access-grants` and receive paginated safe grant DTOs.
- [ ] Non-admin users receive 403 for the backend list endpoint.
- [ ] Backend list tests cover pagination, sort whitelist/default sort, supported filters, and RFC3339 date filters.
- [ ] Backend response tests prove unsafe credential/control-plane material is not exposed.
- [ ] Frontend API tests cover paginated response unwrapping, query serialization, snake_case to camelCase mapping, nullable fields, and unknown action/purpose/status fallbacks.
- [ ] Admin UI renders grant rows, filters, pagination, and a read-only safe-field detail view.
- [ ] Non-admin direct navigation to the UI redirects away.
- [ ] UI tests prove no grant DTOs, reason text, step-up proofs, or grant payloads are persisted to browser storage.
- [ ] No approve/revoke/deny/workflow controls are introduced.
- [ ] Backend and frontend verification commands pass for touched areas, with full project checks run before commit.

## Definition of Done

- Backend tests for the new list endpoint pass.
- Frontend tests for the API mapper/list page pass.
- Full backend test suite and frontend check suite pass before commit.
- Trellis task is validated, started, implemented, checked, committed, archived, and recorded in the session journal.
- PR is created, CI is monitored to green, merged, and release automation is monitored through the published release and Docker image workflow.

## Technical Approach

### Backend

Implement a `List` method on the existing credential access grant handler, mirroring the structure of the credential audit list handler:

1. Start from `model.CredentialAccessGrant`.
2. Apply exact-match filters for scalar grant fields and RFC3339 `created_at` range filters.
3. Parse pagination with the shared helper and an explicit sort whitelist.
4. Count total rows before applying pagination.
5. Map rows through the existing safe grant DTO boundary, adding output-side bounding/sanitization for legacy free-text labels/reasons if needed.
6. Return `respondPaginated`.
7. Register `GET /credential-access-grants` next to the existing grant request routes with `RequireRole("admin")`.

### Frontend

Extend the existing grant API module and add a small admin-only status page:

1. Add list option/query types and a `listCredentialAccessGrants` API method.
2. Reuse the existing grant response mapper so all components consume `CredentialAccessGrant` domain objects.
3. Add a lazy page route and admin navigation entry using the same patterns as credential audit.
4. Build the UI as a read-only list with filters, pagination, and a detail dialog/card.
5. Keep all state in React memory only and avoid browser storage.
6. Add focused Vitest/Testing Library coverage for API mapping, page behavior, admin redirect, and storage safety.

## Decision (ADR-lite)

**Context**: The shipped P3 grant slices now cover several admin-only credential-use operations, but the remaining enforcement work requires operator-owned and multi-resource semantics. A status/list surface is useful immediately for support visibility and does not require changing authorization semantics.

**Decision**: Implement a minimal read-only admin grant status/list API/UI before adding operator-owned manual trigger grants. Keep the list safe-field-only, paginated, filterable, and non-mutating.

**Consequences**: This delivers low-risk visibility while preserving the current grant table shape. It intentionally does not solve approval workflows, revocation UX, operator grants, batch grants, policy routing, SIEM/ChatOps integration, or P4 architecture work.

## Out of Scope

- Grant approval, denial, revocation, refresh, retry, reviewer routing, or workflow state transitions.
- Operator-owned grant issuance/enforcement for manual task trigger.
- Multi-resource grant enforcement for batch trigger or batch command creation.
- Command parsing, command policy language, command approval, or storing command text.
- SSH CA, Vault/KMS/external brokers, terminal/session recording, WebAuthn/passkeys, device trust, or configurable grant policy UI.
- Export endpoints for grants.
- New database schema or migration changes.
- Persisting grant list/filter/detail state to browser storage.

## Research References

- [`research/backend-grant-status-list.md`](research/backend-grant-status-list.md) — backend model/DTO, route/RBAC, pagination/filter, and safe-field inventory for grant listing.
- [`research/frontend-grant-status-list.md`](research/frontend-grant-status-list.md) — frontend API mapper, route/navigation, admin page, testing, and storage-safety patterns for grant listing.
- [`.trellis/tasks/archive/2026-05/05-22-p3-grant-semantics-planning/prd.md`](../archive/2026-05/05-22-p3-grant-semantics-planning/prd.md) — prior semantics decision to use this as the transitional low-risk P3 slice.

## Technical Notes

- Current task directory: `.trellis/tasks/05-22-p3-minimal-grant-status-list`.
- Current safe grant model boundary is `model.CredentialAccessGrant` plus the existing grant DTO mapping.
- Current backend grant routes are creation-only; this task adds the first read/list route.
- Existing credential audit list/export handlers provide the closest backend pagination/filtering pattern.
- Existing credential audit page provides the closest frontend route/admin/list/detail pattern.
- The implementation must preserve the rule that credential access grants are row-backed authorization records, not bearer tokens or client-stored grant material.
