# Error Handling

> How errors are handled in this project.

---

## Overview

API responses use the unified envelope in
`backend/internal/api/handlers/response.go`:

```go
Response{Code: 0, Message: "ok", Data: data}
Response{Code: http.StatusBadRequest, Message: msg, Data: nil}
```

Handlers should use response helpers instead of ad hoc `c.JSON` response maps.
Internal errors are logged server-side and returned to clients as the generic
message from `respondInternalError`.

---

## Error Types

- Domain packages use sentinel errors when handlers need stable HTTP mapping.
  Examples: `dashboards.ErrNotFound`, `dashboards.ErrConflict`,
  `dashboards.ErrInvalidMetric`, and `escalation.ErrNotFound`.
- Validation functions usually return plain `error` values with user-facing
  messages. Handlers map them to `respondBadRequest`.
- Database not-found cases should use `errors.Is(err, gorm.ErrRecordNotFound)`
  and then return the package sentinel or a 404 response.
- Use `%w` when adding context to errors that may be inspected later. Existing
  migration and database open paths wrap failures this way.

---

## Error Handling Patterns

- Handler flow is normally: parse ID with `parseID`, bind JSON with
  `ShouldBindJSON`, validate domain input, call a service/query, map errors,
  return through `respondOK` or another helper.
- Service packages should return errors, not write HTTP responses. The handler
  layer owns HTTP status and response shape. Example:
  `dashboard_handler.go` maps `dashboards` errors in `mapServiceErr`.
- Use fail-closed behavior for auth/ownership uncertainty. Example:
  `ownershipNodeFilter` returns `errUnknownRole` when the Gin context has no
  recognized role.
- Canceled request/query errors are not automatically business failures. The
  GORM logger wrapper in `database/gorm_logger.go` suppresses
  `context.Canceled` and `context.DeadlineExceeded` query noise.

---

## API Error Responses

- Bad input: `respondBadRequest(c, "message")`.
- Unauthenticated: `respondUnauthorized(c, "message")`.
- Unauthorized role or ownership: `respondForbidden(c, "message")`.
- Missing resource: `respondNotFound(c, "message")`.
- Duplicate/conflict: `respondConflict(c, "message")`.
- Upstream/backend dependency failure that is safe to expose: use a specific
  helper such as `respondBadGateway`.
- Accepted async work: `respondAccepted(c, data)`.
- Temporarily unavailable server resources: `respondServiceUnavailable(c,
  "message")`.
- Feature not implemented for the active runtime/backend: `respondNotImplemented(c,
  "message")`.
- Unexpected server error: `respondInternalError(c, err)`. This logs the error
  with module `api` and the route path, then returns a generic 500 envelope.

Do not expose raw SQL, encryption, SSH private key, token, or stack-like error
details to clients. For current user-facing messages, the codebase mostly uses
Simplified Chinese strings.

---

## Scenario: Bulk Alert Resolution API

### 1. Scope / Trigger

- Trigger: adding or changing an API that mutates multiple alert rows or exposes a cross-layer request/response contract.
- Applies to endpoints that accept either an explicit alert set or a scoped resource target and then update all matching unresolved alerts.

### 2. Signatures

- Route: `POST /api/v1/alerts/bulk-resolve` under `/api/v1`.
- Middleware: authenticated route plus `middleware.RBAC("alerts:write")`.
- Handler signature: `func (h *AlertHandler) BulkResolve(c *gin.Context)`.
- Backend request shape:

```go
type bulkResolveAlertsRequest struct {
    AlertIDs []uint `json:"alert_ids"`
    NodeID   *uint  `json:"node_id"`
}
```

- Backend response shape:

```go
type bulkResolveAlertsResponse struct {
    ResolvedCount int64 `json:"resolved_count"`
    SkippedCount  int64 `json:"skipped_count"`
}
```

### 3. Contracts

- Exactly one target mode must be supplied:
  - `alert_ids`: explicit alert IDs; deduplicate IDs and ignore zero before mutation.
  - `node_id`: node ID target; update unresolved alerts for that node only.
- Mutation contract: only rows where `status != "resolved"` may be updated.
- Resolved rows must be skipped, not deleted or reprocessed.
- Update fields: `status = "resolved"`, `retryable = false`, and `updated_at = now`.
- Response counts:
  - `resolved_count`: number of rows actually updated.
  - `skipped_count`: authorized target count minus updated row count.

### 4. Validation & Error Matrix

- Missing both `alert_ids` and `node_id` -> `respondBadRequest`.
- Providing both `alert_ids` and `node_id` -> `respondBadRequest`.
- `node_id == 0` -> `respondBadRequest`.
- `alert_ids` becomes empty after zero filtering -> `respondBadRequest`.
- Any explicit alert ID does not exist -> `respondNotFound` before mutation.
- Authenticated user lacks ownership for any target node -> `respondForbidden` before mutation.
- Database read/update failure -> `respondInternalError` with generic client message.

### 5. Good/Base/Bad Cases

- Good: admin posts `{ "alert_ids": [1, 2, 2, 3] }`; duplicate IDs are collapsed, unresolved alerts are resolved, already resolved alerts contribute to `skipped_count`.
- Base: operator posts `{ "node_id": 7 }`; ownership is checked once for node 7, then only unresolved alerts on node 7 are updated.
- Bad: operator posts an alert ID from a node they do not own; return 403 and leave all requested alerts unchanged.

### 6. Tests Required

- Handler test for explicit IDs: assert dedupe, unresolved-only update, `retryable=false`, and skipped count for already resolved rows.
- Handler test for node target: assert only the target node's unresolved alerts change.
- Authorization tests for explicit alert IDs and node target: assert 403 and no mutation.
- Router test: assert `/api/v1/alerts/bulk-resolve` is registered before dynamic alert routes and is protected by `alerts:write`.
- Frontend/API test: assert camelCase `alertIds` maps to `alert_ids` and `resolved_count` maps to `resolvedCount`.

### 7. Wrong vs Correct

#### Wrong

Loop over selected alerts in the frontend and call `POST /alerts/:id/resolve` repeatedly. This causes partial UI-visible mutations, duplicates ownership checks in many requests, and cannot report skipped rows consistently.

#### Correct

Expose one bulk endpoint, validate the whole target set first, perform the unresolved-only update inside one backend transaction, and return aggregate counts through the standard response envelope.

---

## Common Mistakes

- Do not add new raw `c.JSON(http.Status..., gin.H{"error": ...})` responses in
  handlers; use `response.go` helpers.
- When a response needs a new HTTP status, add a named helper in `response.go`
  first, then update handler tests to assert the standard envelope.
- Do not swallow database errors. If a query can fail, check `.Error` or
  `RowsAffected` as appropriate.
- Do not return raw `err.Error()` for internal server errors. Wrap/log it and
  return the generic internal-error response.
- Do not treat missing auth context as admin. Ownership helpers explicitly avoid
  that shortcut.
- Do not let client-aborted dashboard/API requests pollute error logs. Preserve
  the context-aware GORM logger behavior.

---

## Scenario: Restore Drill Evidence Contract

### 1. Scope / Trigger

- Trigger: adding or changing restore drill evidence persistence, policy drill summaries, task-run detail evidence, or frontend mapping for drill evidence.
- Applies to `restore_drill_evidences`, `GET /api/v1/policies`, `GET /api/v1/policies/:id`, `GET /api/v1/task-runs/:id`, drill execution, and frontend API mappers that expose drill evidence.

### 2. Signatures

- Database table: `restore_drill_evidences` with one row per drill `task_run_id`.
- Unique index: `idx_restore_drill_evidences_task_run` on `task_run_id`.
- Policy response field: `latest_drill` on policy list/detail objects.
- Task-run detail response field: `drill_evidence` on `GET /api/v1/task-runs/:id`.
- Frontend domain types: `PolicyLatestDrillSummary`, `RestoreDrillEvidence`, and `TaskRunTriggerType` including `"drill"`.

### 3. Contracts

- `restore_drill_evidences` stores identity fields: `policy_id`, `task_id`, `task_run_id`, optional `source_task_run_id`, optional `snapshot_ref`, `sandbox_node_id`, `sandbox_node_name`, and `sandbox_path`.
- Top-level evidence status fields: `status`, `failed_step`, `confidence_eligible`, `started_at`, `finished_at`, and `duration_ms`.
- Phase fields must stay explicit: `restore_status`, `restore_started_at`, `restore_finished_at`, `restore_error`, `verify_status`, `verify_started_at`, `verify_finished_at`, `verify_error`, `post_verify_status`, `post_verify_finished_at`, `post_verify_error`, `cleanup_status`, `cleanup_started_at`, `cleanup_finished_at`, and `cleanup_error`.
- `latest_drill` contains only scan-friendly summary fields: `task_run_id`, `status`, `started_at`, `finished_at`, `duration_ms`, `failed_step`, and `confidence_eligible`.
- `drill_evidence` contains the full structured evidence record for the requested task run when a row exists; omit it or return null for non-drill or legacy runs without evidence.
- Only completed successful drills with successful or skipped cleanup may set `confidence_eligible=true`. Failed, canceled, pending, post-verify-failed, cleanup-failed, or abnormally terminated drills must set `confidence_eligible=false`.
- Evidence error fields must be sanitized before storage/return; do not expose tokens, SSH secrets, private keys, raw stack traces, or raw SQL/encryption errors.

### 4. Validation & Error Matrix

| Condition | Expected result |
|---|---|
| Drill restore path is blank | Use the default sandbox path or reject configuration at the existing drill config boundary. |
| Drill restore path is `/`, `/etc`, `/usr`, `/bin`, `/sbin`, `/boot`, `/dev`, `/proc`, `/sys`, `/run`, `/var/run`, or any subpath of those directories | Reject before restore/cleanup; record failed evidence with `failed_step="restore_path"` or `"cleanup_boundary"` when execution reached evidence recording. |
| Sandbox node is missing, unreachable, or unauthorized by drill config | Do not create positive confidence evidence; return/record a failed drill. |
| Restore phase fails | Set `status="failed"`, `failed_step="restore"`, `restore_status="failed"`, sanitized `restore_error`, and `confidence_eligible=false`. |
| Pre-verify or verify fails | Set `status="failed"`, `failed_step="pre_verify"` or `"verify"`, `verify_status="failed"`, sanitized `verify_error`, and `confidence_eligible=false`. |
| Post-verify fails | Set `status="failed"`, `failed_step="post_verify"`, `post_verify_status="failed"`, sanitized `post_verify_error`, and `confidence_eligible=false`. |
| Cleanup fails or cleanup boundary validation fails | Set `status="failed"`, `failed_step="cleanup"` or `"cleanup_boundary"`, `cleanup_status="failed"`, sanitized `cleanup_error`, and `confidence_eligible=false`. |
| Evidence row is absent for task-run detail | Return the task run successfully without `drill_evidence`; absence is not a 500. |
| Evidence query fails for reasons other than not found | Return the standard internal-error response and keep raw DB details out of the client response. |

### 5. Good/Base/Bad Cases

- Good: a drill restores to a sandbox path, verify/post-verify pass, cleanup succeeds, task-run detail returns full `drill_evidence`, and policy list shows `latest_drill.confidence_eligible=true`.
- Base: a legacy drill task run has no evidence row; task-run detail still returns the run, and policy `latest_drill` remains null until structured evidence exists.
- Bad: a cleanup failure leaves `status="success"` or `confidence_eligible=true`; Backup Confidence would treat an unsafe drill as proof.

### 6. Tests Required

- Migration tests or startup coverage must include both SQLite and PostgreSQL migration files for `000058_restore_drill_evidence` and later versions.
- Backend drill tests must assert success evidence, restore/verify/post-verify failure evidence, cleanup failure evidence, `confidence_eligible=false` for unsafe outcomes, and sandbox path rejection for forbidden directories and subpaths.
- Handler tests must assert policy list/detail includes `latest_drill` and task-run detail includes `drill_evidence` when present.
- Handler tests must assert task-run detail without evidence still succeeds.
- Frontend API tests must assert snake_case fields map to camelCase fields, including `latest_drill`, `drill_evidence`, `post_verify_finished_at`, and `trigger_type="drill"`.
- UI tests must cover the normal task-run detail path loading full evidence from `GET /api/v1/task-runs/:id`, not only list-row fallback data.

### 7. Wrong vs Correct

Wrong:

```json
{
  "status": "success",
  "last_error": "cleanup failed: rm -rf /var/run/app failed",
  "confidence_eligible": true
}
```

Correct:

```json
{
  "status": "failed",
  "failed_step": "cleanup",
  "confidence_eligible": false,
  "cleanup_status": "failed",
  "cleanup_error": "清理失败: <sanitized error>"
}
```
