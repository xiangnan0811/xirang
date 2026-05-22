# Research: Backend minimal credential access grant status/list API

- **Query**: Research the backend implementation approach for a minimal admin-only credential access grant status/list API in the Xirang repo. Focus on existing `CredentialAccessGrant` model/DTO mapping, existing credential audit list handlers, pagination/filter patterns, route/RBAC style, and safe fields to expose.
- **Scope**: internal
- **Date**: 2026-05-22

## Findings

### Files Found

| File Path | Description |
|---|---|
| `backend/internal/model/models.go` | Defines `model.CredentialAccessGrant` and documents that grant rows must contain only safe actor/resource identifiers, bounded sanitized reason text, lifecycle state, and timestamps. |
| `backend/internal/api/handlers/credential_access_grant.go` | Existing grant handler: request DTOs, constants for grant statuses/actions/purposes, `credentialGrantDTO`, grant creation, active-grant matching, expiry marking, reason/identity sanitization, and credential audit writes. |
| `backend/internal/api/handlers/credential_audit_handler.go` | Existing admin credential audit list/export handler with filters, pagination, sorting, response DTO mapping, and sanitization patterns. |
| `backend/internal/api/handlers/audit_handler.go` | Generic audit list/export handler with similar filtering, pagination, and RFC3339 time parsing patterns. |
| `backend/internal/api/handlers/helpers.go` | Shared pagination helpers: `parsePagination` and `applyPagination`. |
| `backend/internal/api/handlers/response.go` | Shared response envelopes: `Response`, `PaginatedResponse`, `respondOK`, `respondCreated`, and `respondPaginated`. |
| `backend/internal/api/router.go` | Route registration and RBAC style for credential audit and credential access grant endpoints. |
| `backend/internal/middleware/rbac.go` | Role/permission middleware; `RequireRole("admin")` returns 403 when current role is not exactly admin. |
| `backend/internal/database/migrations/sqlite/000060_credential_access_grants.up.sql` | SQLite table/index definition for `credential_access_grants`. |
| `backend/internal/database/migrations/postgres/000060_credential_access_grants.up.sql` | PostgreSQL table/index definition for `credential_access_grants`. |
| `backend/internal/api/handlers/credential_access_grant_test.go` | Tests for grant request DTOs, admin/step-up validation, safe audit metadata, matching by user/action/purpose/resource/status/expiry, and fixture creation. |
| `backend/internal/api/handlers/credential_audit_handler_test.go` | Tests for credential audit filtering, pagination, sorting, DTO mapping, and sanitization. |
| `backend/internal/api/router_test.go` | Route-level RBAC tests for credential grant terminal route and credential audit event route. |
| `web/src/lib/api/credential-access-grants-api.ts` | Frontend mapper for existing grant response fields; useful as a consumer-side field inventory. |
| `web/src/types/domain.ts` | Frontend domain type and constrained unions for grant action/purpose/status. |
| `.trellis/spec/backend/quality-guidelines.md` | Backend contract for credential access grants, including allowed safe fields and route/RBAC/step-up constraints. |
| `.trellis/spec/frontend/type-safety.md` | Frontend contract for grant API raw fields and domain mapping. |

### Code Patterns

#### Existing `CredentialAccessGrant` persistence model

- The model comment states the intended data boundary: `CredentialAccessGrant` is a short-lived operation-bound JIT grant and “must only contain safe actor/resource identifiers, bounded sanitized reason text, lifecycle state, and timestamps” (`backend/internal/model/models.go:446`).
- Current model fields are all scalar IDs/labels/status/timestamps: `ID`, `RequesterUserID`, `RequesterUsername`, `RequesterRole`, `Action`, `Purpose`, optional `NodeID`/`TaskID`/`PolicyID`, `Reason`, `Status`, `RequestedTTLSeconds`, `RequestedAt`, optional approver/revocation fields, `ExpiresAt`, `CreatedAt`, and `UpdatedAt` (`backend/internal/model/models.go:449`).
- Grant migrations create the same table columns and indexes for SQLite and PostgreSQL, including indexes on requester, action, purpose, node/task/policy IDs, status, requested/expires timestamps, approver/revoker IDs, and an operation index over `(action, purpose, node_id)` (`backend/internal/database/migrations/sqlite/000060_credential_access_grants.up.sql:1`, `backend/internal/database/migrations/postgres/000060_credential_access_grants.up.sql:3`).

#### Existing grant statuses/actions/purposes

- Status constants: `requested`, `approved`, `active`, `denied`, `expired`, `revoked` (`backend/internal/api/handlers/credential_access_grant.go:24`).
- Action constants: `terminal.open`, `config.import`, `config.export`, `snapshot.restore`, `task.restore_trigger` (`backend/internal/api/handlers/credential_access_grant.go:32`).
- Purpose constants for config grant types are `config_import` and `config_export`; other grant flows use `sshutil.PurposeTerminal`, `sshutil.PurposeSnapshot`, and `sshutil.PurposeTaskRestore` (`backend/internal/api/handlers/credential_access_grant.go:32`).
- Frontend domain unions mirror these action/purpose/status values and add `unknown` for action/purpose fallback only (`web/src/types/domain.ts:603`).

#### Existing DTO mapping for safe fields

- Backend response DTO `credentialGrantDTO` exposes only the scalar model fields listed above; it does not expose credentials, tokens, step-up proof material, command/output content, endpoint/proxy values, or node host data (`backend/internal/api/handlers/credential_access_grant.go:96`).
- `toCredentialGrantDTO` maps model fields directly into the DTO, preserving optional pointer fields with `omitempty` tags for `node_id`, `task_id`, `policy_id`, approval/revocation timestamps, and user IDs (`backend/internal/api/handlers/credential_access_grant.go:604`).
- Existing creation handlers respond with `respondCreated(c, toCredentialGrantDTO(grant))` after storing a self-approved active grant (`backend/internal/api/handlers/credential_access_grant.go:159`, `backend/internal/api/handlers/credential_access_grant.go:181`, `backend/internal/api/handlers/credential_access_grant.go:203`, `backend/internal/api/handlers/credential_access_grant.go:245`, `backend/internal/api/handlers/credential_access_grant.go:276`).
- Frontend raw mapper currently expects the same snake_case fields: `requester_user_id`, `requester_username`, `requester_role`, `action`, `purpose`, resource IDs, `reason`, `status`, `requested_ttl_seconds`, timestamps, approver/revoker fields, `created_at`, and `updated_at` (`web/src/lib/api/credential-access-grants-api.ts:4`).

#### Grant creation and sanitization behavior already in place

- Request handlers call `validateGrantRequest`, which enforces step-up proof, validates the requester context against the DB role, sanitizes reason text, and normalizes TTL (`backend/internal/api/handlers/credential_access_grant.go:318`).
- `createActiveSelfGrant` writes the grant using the authenticated admin as requester and approver, sets `Status` to `active`, stores UTC `RequestedAt`/`ApprovedAt`/`ExpiresAt`, and writes credential audit events for request and activation (`backend/internal/api/handlers/credential_access_grant.go:343`).
- Reason text is trimmed/sanitized, must be non-empty, capped at 240 runes, and rejected when it contains secret/output/command/host/endpoint/proxy-shaped markers (`backend/internal/api/handlers/credential_access_grant.go:576`, `backend/internal/api/handlers/credential_access_grant.go:706`, `backend/internal/api/handlers/credential_access_grant.go:780`).
- Identity labels are normalized through `normalizeCredentialGrantIdentity`, which uses the same free-text sanitizer and truncates to the configured max length (`backend/internal/api/handlers/credential_access_grant.go:790`).

#### Existing credential audit list handler pattern

- `CredentialAuditHandler.List` builds a filtered GORM query, parses pagination with default page size 50 and allowed sort fields, counts total, applies pagination, maps records through a safe response DTO, and returns `respondPaginated` (`backend/internal/api/handlers/credential_audit_handler.go:138`).
- Credential audit allowed sorts are an explicit whitelist: `id`, `created_at`, `username`, `role`, `action`, `purpose`, `credential_kind`, and `outcome` (`backend/internal/api/handlers/credential_audit_handler.go:31`).
- `buildQuery` applies exact-match string filters for `username`, `role`, `action`, `purpose`, `credential_kind`, `credential_source`, and `outcome`; exact numeric filters for `user_id`, `ssh_key_id`, `node_id`, `task_id`, `task_run_id`, and `policy_id`; and RFC3339 `from`/`to` filters on `created_at` (`backend/internal/api/handlers/credential_audit_handler.go:240`).
- DTO mapping sanitizes scalar strings and metadata/error content before returning list data (`backend/internal/api/handlers/credential_audit_handler.go:294`).
- Tests cover filters, pagination, sorting, and mapping expectations with `page_size=1&sort_by=created_at&sort_order=asc` and a full set of filters (`backend/internal/api/handlers/credential_audit_handler_test.go:19`).

#### Shared pagination/filter style

- `parsePagination` prefers `page`/`page_size`, falls back to `limit`/`offset`, caps page size at 500, and only accepts `sort_by` values present in an allowed-sort whitelist; `sort_order` is `asc` only when query value is exactly `asc`, otherwise `desc` (`backend/internal/api/handlers/helpers.go:249`).
- `applyPagination` applies `Order(p.SortBy + " " + p.SortOrder).Offset(offset).Limit(p.PageSize)` (`backend/internal/api/handlers/helpers.go:292`).
- `respondPaginated` emits the standard envelope `{code,message,data,total,page,page_size}` (`backend/internal/api/handlers/response.go:44`).
- `parseRFC3339` trims input and returns a zero `time.Time` for empty or invalid values; existing list handlers silently ignore invalid date filters (`backend/internal/api/handlers/audit_handler.go:180`).

#### Route/RBAC style for admin-only endpoints

- Router initializes `credentialAuditHandler` and `credentialAccessGrantHandler` near other handlers (`backend/internal/api/router.go:103`).
- Secured API group applies primary auth, audit logger, rate limit, and max body size before resource routes (`backend/internal/api/router.go:127`).
- Current credential audit list/export routes are admin-only via `middleware.RequireRole("admin")` (`backend/internal/api/router.go:263`).
- Current credential access grant creation routes are `POST /credential-access-grants/{terminal,config-import,config-export,snapshot-restore,task-restore}` and are also admin-only via `middleware.RequireRole("admin")` (`backend/internal/api/router.go:265`).
- `RequireRole("admin")` checks exact current role and returns 403 with `{"error":"权限不足"}` when the user is not admin (`backend/internal/middleware/rbac.go:87`).
- Route tests show the existing pattern: viewer receives 403 for credential grant terminal route, admin without step-up receives a machine-readable `STEP_UP_REQUIRED`, and admin with step-up receives 201 (`backend/internal/api/router_test.go:164`).

#### Existing grant lookup/status semantics

- `findActiveCredentialGrant` validates DB/claims/action/purpose, reloads the user role from DB, requires the current DB role and claim role to match admin, searches grants by requester user/role/action/purpose plus exact resource tuple, and only considers `active` or `approved` statuses first (`backend/internal/api/handlers/credential_access_grant.go:486`).
- It orders active/approved candidates by `expires_at desc, id desc`, marks expired active/approved grants as `expired`, and returns the first unexpired grant (`backend/internal/api/handlers/credential_access_grant.go:500`).
- If no active/approved grant authorizes the operation, it checks the latest inactive grant status among `revoked`, `denied`, and `expired`, ordered by `updated_at desc, expires_at desc, id desc` (`backend/internal/api/handlers/credential_access_grant.go:518`, `backend/internal/api/handlers/credential_access_grant.go:533`).
- `applyCredentialGrantMatch` treats absent resource IDs as `IS NULL`, so system-scoped, node-scoped, task-scoped, and policy-scoped tuples stay distinct (`backend/internal/api/handlers/credential_access_grant.go:548`).
- Tests cover valid active grants, wrong user, wrong action, wrong purpose, wrong node/task/scope, revoked/denied/expired statuses, and role changes (`backend/internal/api/handlers/credential_access_grant_test.go:557`, `backend/internal/api/handlers/credential_access_grant_test.go:615`, `backend/internal/api/handlers/credential_access_grant_test.go:668`, `backend/internal/api/handlers/credential_access_grant_test.go:733`).

### Safe Fields to Expose

Based on the existing model comment, backend DTO, frontend mapper, and backend grant spec, the already-exposed safe field set is:

| JSON Field | Source | Notes |
|---|---|---|
| `id` | `CredentialAccessGrant.ID` | Scalar row identifier. |
| `requester_user_id` | `RequesterUserID` | Actor ID. |
| `requester_username` | `RequesterUsername` | Sanitized/bounded label from creation path. |
| `requester_role` | `RequesterRole` | Sanitized/bounded role label. |
| `action` | `Action` | Constrained operation string. |
| `purpose` | `Purpose` | Constrained purpose string. |
| `node_id` | `NodeID` | Optional resource identifier only. |
| `task_id` | `TaskID` | Optional resource identifier only. |
| `policy_id` | `PolicyID` | Optional resource identifier only. |
| `reason` | `Reason` | Creation path stores sanitized/bounded text; spec treats it as safe backend text. |
| `status` | `Status` | Lifecycle state. |
| `requested_ttl_seconds` | `RequestedTTLSeconds` | Numeric TTL. |
| `requested_at` | `RequestedAt` | Timestamp. |
| `approved_at` | `ApprovedAt` | Optional timestamp. |
| `approver_user_id` | `ApproverUserID` | Optional actor ID. |
| `approver_username` | `ApproverUsername` | Sanitized/bounded label from creation path. |
| `expires_at` | `ExpiresAt` | Timestamp. |
| `revoked_at` | `RevokedAt` | Optional timestamp. |
| `revoked_by_user_id` | `RevokedByUserID` | Optional actor ID. |
| `created_at` | `CreatedAt` | Timestamp. |
| `updated_at` | `UpdatedAt` | Timestamp. |

Fields explicitly outside the existing safe grant boundary: secrets, tokens, step-up proofs, OTP/recovery values, commands, terminal streams, command output, file contents, exported payloads, raw SQL, endpoint/proxy values, host-sensitive strings, decrypted credential material, SSH private keys/passwords, and raw node host/endpoint values (`.trellis/spec/backend/quality-guidelines.md:467`).

### Related Specs

- `.trellis/spec/backend/quality-guidelines.md` — Credential access grant backend contract: table/model name, terminal route/RBAC/step-up requirements, stable tuple fields, safe-field restrictions, fail-closed matching semantics, and required audit metadata (`.trellis/spec/backend/quality-guidelines.md:451`).
- `.trellis/spec/frontend/type-safety.md` — Frontend grant API contract: raw snake_case fields, camelCase domain mapping, constrained status/action/purpose handling, and safe handling of sanitized `reason` (`.trellis/spec/frontend/type-safety.md:211`).

### External References

None. This was an internal codebase research task.

## Caveats / Not Found

- No existing backend `GET /credential-access-grants` list or status route was found. Current backend grant routes are creation-only `POST` endpoints (`backend/internal/api/router.go:265`).
- No existing backend handler method named `List` or `Status` for `CredentialAccessGrantHandler` was found in `credential_access_grant.go`.
- Existing grant DTO mapping does not re-sanitize DB-loaded legacy grant rows on output; it maps stored values directly (`backend/internal/api/handlers/credential_access_grant.go:604`). Existing creation paths sanitize reason and identity labels before storage (`backend/internal/api/handlers/credential_access_grant.go:330`, `backend/internal/api/handlers/credential_access_grant.go:790`).
- The existing `(action, purpose, node_id)` migration index does not include `task_id` or `policy_id`, although the table has separate indexes for both (`backend/internal/database/migrations/sqlite/000060_credential_access_grants.up.sql:29`).
