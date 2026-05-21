# P3 Control-Plane Follow-Up Hardening

## Goal

Reduce the next highest remaining control-plane blast radius by gating sensitive config export (`GET /config/export?include_secrets=true`) with a short-lived, row-backed credential access grant before the backend loads or serializes secret-bearing configuration records.

This slice should extend the existing P2/P3 grant primitive from terminal open and config import to the export side of the same high-risk settings surface, without changing the stored SSH credential model or drifting into P4 architecture work.

## Selected Surface

**Sensitive config export** is selected as the next executable P3 slice.

### Why this surface

- It has the largest direct credential exfiltration blast radius among the remaining bounded REST candidates: `include_secrets=true` can serialize node passwords/private keys, SSH private keys, task executor config, and DB-backed system setting values.
- It is already admin-only and conditionally TOTP step-up-gated, so a grant check is additive and compatible with current admin workflows.
- It is system-scoped like config import: the existing `CredentialAccessGrant` table can represent `(user, role, action="config.export", purpose="config_export")` with null resource IDs and no migration.
- Enforcement can run before `ConfigHandler.Export` performs DB reads and response construction, so direct API clients cannot bypass the UI.
- Other remaining candidates are better follow-ups: snapshot restore is task-scoped/admin-only but asset-mutation focused, while task trigger/batch command creation need role/ownership/multi-resource grant semantics beyond this bounded slice.

## Requirements

### Backend enforcement

- Add stable grant tuple constants:
  - action: `config.export`
  - purpose: `config_export`
- Add `POST /credential-access-grants/config-export` behind primary auth, `RequireRole("admin")`, and the existing step-up validation used by grant requests.
- The config-export grant request must:
  - accept a bounded `reason` and optional `requested_ttl_seconds`;
  - reuse existing reason sanitizer and TTL bounds;
  - create an active self-grant with null `node_id`, `task_id`, and `policy_id`;
  - return the existing safe grant DTO;
  - write only existing safe grant audit metadata (`stage`, `operation`, `status`, `ttl_seconds`, `grant_id`, `self_grant`).
- Add middleware for sensitive config export that:
  - checks only when `include_secrets=true`;
  - requires an active system-scoped grant matching current user, current role, `config.export`, `config_export`, active/approved status, and unexpired UTC expiry;
  - rejects missing, expired, revoked, denied, wrong-user, wrong-role, wrong-action, wrong-purpose, or wrong-resource grants fail-closed with the existing `CREDENTIAL_GRANT_REQUIRED` response shape;
  - writes safe blocked/use grant audit evidence;
  - runs before `ConfigHandler.Export` loads/serializes nodes, keys, tasks, settings, or any secret-bearing fields.
- Keep plain `GET /config/export` unchanged: admin-only, no step-up/grant beyond current behavior, no secret-bearing fields in the response.
- Keep existing `RequireStepUpIf(... include_secrets=true ...)` in place; the grant is additive and must not replace auth, admin RBAC, step-up, audit, body/response safety, or config export filtering.

### Frontend UX/API

- Add frontend domain/API support for `config.export` / `config_export` grant payloads.
- If exposing the sensitive export flow in UI, add a separate, clearly labeled admin action for exporting with secrets rather than changing the existing safe export button.
- The sensitive export flow must:
  - open an accessible reason dialog before requesting the grant;
  - validate non-empty and max-length reason locally;
  - call `ensureStepUpProof()`;
  - request `requestConfigExportCredentialGrant(token, { reason, requestedTtlSeconds: 600 }, proof)`;
  - then call `exportConfig(token, true, proof)` and download the returned payload;
  - keep grant ID/material/status, reason text after close, and exported payload out of `localStorage` and `sessionStorage`.
- Render errors through React text rendering and existing sanitized error helpers; grant-required denials must not be treated as session expiry.
- Preserve current safe export behavior and labels unless adding explicit sensitive-export labels.

### Audit and secret safety

- Config export success audit must continue to record only safe booleans/counts such as `with_sensitive`, `node_count`, `key_count`, `policy_count`, `task_count`, and `setting_count`.
- Grant request/activation/use/block events must not include exported payloads, private keys, passwords, bearer tokens, step-up proofs, OTP/recovery values, executor config, endpoints/proxies, hostnames, node addresses, file contents, raw SQL, or command text/output.
- Tests and docs must use neutral markers only; do not add fake secret-shaped, host-shaped, endpoint-shaped, or command-output-shaped strings.

### Tests and validation

- Backend tests must cover:
  - config-export grant request success, admin/step-up/reason/TTL validation, DTO shape, null resource scope, and safe audit metadata;
  - sensitive export denial without a grant before handler execution/serialization;
  - sensitive export success with a valid grant;
  - rejection for expired, revoked, denied, wrong-user, wrong-role, wrong-action, wrong-purpose, and wrong-resource grants;
  - unchanged safe export behavior without `include_secrets=true`;
  - config import and terminal grants cannot authorize sensitive config export.
- Frontend tests must cover:
  - mapper/type/API wrapper support for `config.export` / `config_export`;
  - sensitive export dialog reason validation;
  - step-up + grant + `exportConfig(token, true, proof)` flow;
  - sanitized grant errors and no session-expiry redirect;
  - storage safety: no grant material, reason, status, or exported payload in `localStorage`/`sessionStorage`.
- Trellis context files must validate before implementation starts.

## Acceptance Criteria

- [ ] Current code/research confirms sensitive config export is selected and explains why task trigger/batch command/snapshot restore are deferred.
- [ ] `POST /credential-access-grants/config-export` creates a short-lived active, system-scoped, admin self-grant only after valid step-up and sanitized reason/TTL validation.
- [ ] `GET /config/export?include_secrets=true` requires primary admin auth, existing TOTP step-up, and an active matching `config.export` / `config_export` grant before `ConfigHandler.Export` loads or serializes secret-bearing records.
- [ ] `GET /config/export` without `include_secrets=true` remains unchanged and does not require a grant.
- [ ] Missing/expired/revoked/denied/wrong-user/wrong-role/wrong-action/wrong-purpose/wrong-resource grants fail closed with sanitized machine-readable grant-required responses and safe blocked audit evidence.
- [ ] Grant lifecycle/use/block audit metadata and config export audit metadata contain only sanitizer-compatible labels, IDs, counts, booleans, and statuses.
- [ ] Frontend API/domain mapping supports config export grants; any sensitive export UI uses local-only prompt state and does not persist grant/export material in browser storage.
- [ ] Backend and frontend tests cover the new grant flow, denial matrix, unchanged safe export, and secret-safety/storage-safety requirements.
- [ ] Trellis validation, targeted backend tests, full backend tests, targeted frontend tests, full frontend check, `git diff --check`, and added-line forbidden-string scan pass before completion.

## Definition of Done

- Sensitive config export is grant-gated end to end without changing the stored credential model or adding schema/migration.
- Existing config import, terminal grant, safe config export, and normal admin workflows continue to work.
- PRD, `implement.jsonl`, and `check.jsonl` validate.
- Implementation is checked by `trellis-check`; any findings are resolved or explicitly justified.
- No unrelated refactor or P4 architecture work is introduced.

## Technical Approach

- Extend `backend/internal/api/handlers/credential_access_grant.go` with config-export constants, request type/handler, operation label, and a middleware parallel to `RequireConfigImportCredentialGrant`.
- Register `POST /credential-access-grants/config-export` in `backend/internal/api/router.go` and insert config-export grant enforcement after existing `RequireStepUpIf(... include_secrets=true ...)` and before `configHandler.Export`.
- Keep `ConfigHandler.Export` response logic focused on export construction and existing counts-only audit; use route middleware to fail closed before DB reads for sensitive export.
- Extend frontend types/mappers/API wrapper in `web/src/types/domain.ts` and `web/src/lib/api/credential-access-grants-api.ts`.
- Add an explicit sensitive export prompt/action in `web/src/components/config-export-import.tsx` only if needed to exercise the protected flow from UI; keep existing safe export unchanged.
- Add/update i18n strings in `web/src/i18n/locales/{zh,en}.ts` and tests in existing backend/frontend test files.

## Decision (ADR-lite)

**Context**: Remaining P3 candidate surfaces include sensitive config export, SSH key export, task trigger/restore, snapshot restore, and batch command creation. The next slice should be high-impact, bounded, additive, and implementable with existing P2/P3 primitives.

**Decision**: Gate sensitive config export (`include_secrets=true`) with a system-scoped `config.export` / `config_export` credential access grant, while leaving safe export unchanged.

**Consequences**: This closes the most direct bulk secret exfiltration path with minimal schema and role-design risk. It intentionally defers task/batch/operator grant semantics and snapshot restore task-scoped grants to later P3 follow-ups.

## Out of Scope

- SSH CA, Vault/KMS, external secret brokers, terminal/session recording, command-level approval, WebAuthn/passkeys, device trust, configurable policy UI.
- Broad grant policy engine or user-configurable grant matrix.
- Changing stored SSH credential encryption, SSH key scope semantics, or requiring remote host changes.
- Role/ownership-aware grants for operator task triggers or batch command creation.
- Snapshot-ID-level grant resources or migration-backed new grant resource columns.
- Recording raw secrets, command text/output, file contents, endpoints, hostnames, imported/exported payloads, executor config, or system setting values in grant/audit/log/UI/test evidence.

## Research References

- [`research/remaining-control-plane-surfaces.md`](research/remaining-control-plane-surfaces.md) — compares remaining surfaces and recommends sensitive config export as the next bounded P3 slice.

## Technical Notes

- Task directory: `.trellis/tasks/05-21-p3-control-plane-follow-up-hardening`.
- Branch: `security/p3-control-plane-followups`.
- Key backend files: `backend/internal/api/router.go`, `backend/internal/api/handlers/credential_access_grant.go`, `backend/internal/api/handlers/config_handler.go`, `backend/internal/api/handlers/credential_access_grant_test.go`.
- Key frontend files: `web/src/components/config-export-import.tsx`, `web/src/lib/api/credential-access-grants-api.ts`, `web/src/lib/api/config-api.ts`, `web/src/types/domain.ts`, `web/src/i18n/locales/{zh,en}.ts`, related tests.
- Relevant specs: `.trellis/spec/backend/quality-guidelines.md`, `.trellis/spec/backend/error-handling.md`, `.trellis/spec/backend/logging-guidelines.md`, `.trellis/spec/frontend/state-management.md`, `.trellis/spec/frontend/type-safety.md`, `.trellis/spec/frontend/component-guidelines.md`, `.trellis/spec/frontend/a11y-guidelines.md`.
