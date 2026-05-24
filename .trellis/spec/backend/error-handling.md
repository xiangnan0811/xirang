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

Do not expose raw SQL, encryption, SSH private key, token, command output,
SFTP/file content, Docker output, diagnostic evidence, exported config payloads,
or stack-like error details to clients. For current user-facing messages, the
codebase mostly uses Simplified Chinese strings.

---

## Scenario: SSH Fleet Doctor Diagnostics

### 1. Scope / Trigger

- Trigger: adding or changing node Doctor diagnostics, diagnostic result fields,
  or frontend mapping for Doctor results.
- Applies to `POST /api/v1/nodes/:id/doctor` and any server-side diagnostic
  runner used by that endpoint.

### 2. Signatures

- Route: `POST /api/v1/nodes/:id/doctor` under `/api/v1`.
- Middleware: authenticated route plus `middleware.RBAC("nodes:test")` and
  `middleware.OwnershipNodeCheck`.
- Backend response shape is a standard `Response` envelope whose `data` contains
  `node_id`, `node_name`, `generated_at`, and `checks`.
- Each check item has `check`, `status`, `evidence`, and `suggestion`.
- Check statuses are `pass`, `warn`, `fail`, and `skip`.

### 3. Contracts

- The endpoint is diagnose-only and read-only. Do not create directories, mutate
  node status, update SSH key usage, or trigger remediation.
- The API must not accept arbitrary command strings or caller-selected check
  definitions. All remote commands must be selected from server-side allowlists.
- Expected checks include SSH/auth/known_hosts classification, sudo availability,
  backup directory existence/writability, disk space, required tools, and probe
  status when available.
- Evidence must be sanitized and concise. Do not return passwords, private keys,
  tokens, proxy endpoints, hostnames, raw paths, raw SQL/encryption details, raw
  command text, diagnostic output, or full command output that may contain
  credentials.

### 4. Validation & Error Matrix

| Condition | Expected result |
|---|---|
| Unknown node | `respondNotFound`. |
| Non-empty request body or custom command/check input | `respondBadRequest`; do not parse or echo caller-supplied command text. |
| Auth config invalid | Return structured failed/skipped checks, not a 500. |
| known_hosts/auth/network/handshake failure | Return structured failed/skipped checks, not a 500. |
| Database lookup failure for owned node context | `respondInternalError` with generic client message. |
| Remote diagnostic command is not allowlisted | Reject before opening a session or return an internal diagnostic error; never execute it. |

### 5. Good/Base/Bad Cases

- Good: `POST /nodes/:id/doctor` with no body returns sanitized check items and leaves node/task/remote filesystem state unchanged.
- Base: invalid SSH credentials produce `auth`/`ssh` failures plus skipped SSH-dependent checks such as `sudo`, `tools`, `backup_dir`, and `disk`.
- Bad: accepting `{ "command": "whoami" }`, using caller-selected checks, creating remote directories, or returning raw SSH/private-key/proxy output.

### 6. Tests Required

- Handler tests for rejecting arbitrary diagnostic input, including chunked/non-empty bodies.
- Tests for allowlist enforcement around remote diagnostic commands and path/tool arguments.
- Tests for common diagnostic statuses such as auth failure, SSH classification, probe failure, and settings-derived disk thresholds.
- Tests that sensitive evidence is redacted and long output stays concise.
- Router/RBAC tests asserting the endpoint is registered and protected by `nodes:test` plus ownership middleware.

### 7. Wrong vs Correct

Wrong:

```go
var req struct { Command string `json:"command"` }
_ = c.ShouldBindJSON(&req)
output, err := session.CombinedOutput(req.Command)
```

Correct:

```go
if !doctorRequestBodyAllowed(c) {
    respondBadRequest(c, "Doctor only supports server-side allowlisted diagnostics")
    return
}
output, err := runDoctorCommand(ctx, client, "sudo -n true 2>&1")
```

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

## Scenario: Backup Confidence Read Model

### 1. Scope / Trigger

- Trigger: adding or changing the backup confidence endpoint, confidence scoring,
  evidence/reason contracts, or frontend confidence mappers.
- Applies to `GET /api/v1/overview/backup-confidence`, policy/task-run/drill
  evidence aggregation, and frontend types that expose confidence data.

### 2. Signatures

- Route: `GET /api/v1/overview/backup-confidence` under `/api/v1`.
- Middleware: authenticated route plus `middleware.RBAC("tasks:read")`.
- Handler signature: `func (h *BackupConfidenceHandler) Get(c *gin.Context)`.
- Backend response shape is a standard `Response` envelope whose `data` contains
  `generated_at`, `summary`, and `items`.
- Confidence statuses: `healthy`, `warning`, `at_risk`, and `insufficient`.
- Each item must include `status`, numeric `score`, `reasons`, `evidence`, and
  `next_steps`.

### 3. Contracts

- The endpoint is read-only; do not persist confidence history in the MVP.
- Aggregate at policy scope first. Include node target identifiers/names only as
  safe DTO fields; do not serialize full `Node`, `Task`, `Policy`, or executor
  config models from this endpoint.
- Missing restore drill evidence is not healthy. It should add a
  `drill_missing` reason and drive the item to `insufficient` unless a stronger
  failure already makes it `at_risk`.
- Recent failed backup/task runs, RPO over limit, failed or non-confidence-eligible
  drills, verification failures/warnings, and unresolved integrity/drill/verify
  alerts must affect status and reasons.
- Every non-healthy item must include at least one actionable `next_steps` entry.
- Evidence messages should explain the signal but remain sanitized. Do not expose
  node hosts, usernames, passwords, private keys, executor configs, raw SQL,
  encryption errors, or stack traces.

### 4. Validation & Error Matrix

| Condition | Expected result |
|---|---|
| No enabled non-template policies visible to the caller | Return `items: []` and zeroed summary. |
| Operator has no owned nodes | Return empty confidence data, not all policies. |
| Missing task runs | Add missing backup evidence/reasons instead of returning 500. |
| Missing restore drill evidence | Return item with `drill_missing` and `insufficient`. |
| Evidence query fails for reasons other than not found | Return standard `respondInternalError`. |
| Unknown/missing auth role reaches ownership filtering | Fail closed via existing ownership helpers. |

### 5. Tests Required

- Backend handler tests must cover healthy evidence, recent backup failure,
  RPO exceeded, missing drill evidence, failed drill evidence, integrity/verify
  alert influence, next-step presence for non-healthy statuses, and sensitive
  field exclusion.
- Router/RBAC tests must assert the endpoint is registered and protected by
  `tasks:read`.
- Frontend API tests must assert snake_case fields map to camelCase fields,
  especially `at_risk`, `next_steps`, `observed_at`, `task_run_id`, and
  `last_backup_at`.
- UI tests should verify the Backups page provides a visible confidence entry
  and shows non-healthy reasons/next steps.

### 6. Wrong vs Correct

Wrong:

```go
respondOK(c, policy) // leaks full policy graph and does not explain status
```

Correct:

```go
respondOK(c, backupConfidenceResponse{Items: []backupConfidenceItem{item}})
```

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
- Evidence error fields must be sanitized before storage/return; do not expose tokens, SSH secrets, private keys, raw stack traces, raw SQL/encryption errors, command output/text, endpoints, hostnames, or raw paths.
- Task/task-run log, task-run detail, and WebSocket task-log backfill read endpoints must apply response-time sanitization to stored runtime evidence so legacy rows cannot bypass current write-time sanitizers.
- Remote command helpers that surface errors outside their package must hide non-empty command output before wrapping or logging the error; use a stable placeholder rather than raw stdout/stderr, paths, hosts, endpoints, or tokens.

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

---

## Scenario: Health Incident Timeline Read Model

### 1. Scope / Trigger

- Trigger: adding or changing the health incident timeline endpoint, source aggregation, severity/resource grouping, ownership filtering, or frontend timeline mappers.
- Applies to `GET /api/v1/overview/health-incident-timeline`, alert/task-run/delivery/anomaly/node metric/backup health aggregation, and UI contracts that render timeline groups.

### 2. Signatures

- Route: `GET /api/v1/overview/health-incident-timeline` under `/api/v1`.
- Query: optional `window_hours`; default 72, maximum 168.
- Middleware: authenticated route plus `middleware.RBAC("tasks:read")`.
- Handler signature: `func (h *HealthIncidentTimelineHandler) Get(c *gin.Context)`.
- Backend response shape is a standard `Response` envelope whose `data` contains `generated_at`, `window_hours`, `summary`, and `groups`.
- Group fields include `id`, `severity`, `resource`, `last_seen_at`, `event_count`, `likely_cause`, `source_types`, `next_actions`, and `signals`.
- Severity values are `critical`, `warning`, and `info`; source types include `alert`, `task_failure`, `notification_failure`, `anomaly`, `probe`, `metric`, `backup_stale`, and `backup_degraded`.

### 3. Contracts

- The endpoint is read-only; do not create incident rows, mutate alerts/tasks/nodes, retry notifications, or trigger remediation.
- Aggregate only safe DTO fields. Do not serialize full `Node`, `Task`, `Policy`, executor config, alert delivery integration config, or raw log bodies from this endpoint.
- Node-scoped aggregation for task runs must join `task_runs -> tasks`; `TaskRun` has no direct `node_id`.
- Operator visibility must fail closed through ownership helpers: owned node IDs only; if no owned nodes, return an empty timeline; do not expose `node_id=0` platform alerts to operators.
- Platform/service events may use `node_id=0`; treat them as platform resources for admin/viewer visibility, never as node-owned events.
- Every returned group must include at least one `next_actions` entry with a non-empty `href` and a concise `likely_cause` derived from sanitized source data.
- Keep grouping deterministic by resource/source identity, and sort returned groups by `last_seen_at` descending.

### 4. Validation & Error Matrix

| Condition | Expected result |
|---|---|
| `window_hours` is missing, non-numeric, zero, or negative | Use the default 72-hour window. |
| `window_hours` exceeds 168 | Clamp to 168 hours. |
| Operator has no owned nodes | Return `groups: []` and zeroed summary. |
| Unknown/missing auth role reaches ownership filtering | Fail closed via existing ownership helpers and return standard internal error if role context is invalid. |
| Source table query fails | Return `respondInternalError`; do not return partial timeline data as successful. |
| Source rows contain sensitive error text | Sanitize or summarize messages before placing them in `likely_cause` or `signals[].message`. |
| No recent source rows are visible | Return an empty timeline with a current `generated_at`. |

### 5. Good/Base/Bad Cases

- Good: a failed task run joined to its task and a related alert collapse into one task resource group with critical severity, node/policy identifiers, sorted signals, and links to the task run/logs or related resource.
- Base: an operator with one owned node sees only that node's alerts, task failures, metrics, and backup staleness; platform alerts and unowned node events are absent.
- Bad: returning raw alert rows plus task-run rows as separate ungrouped arrays, exposing all nodes to operators, or persisting an `Incident` lifecycle record for this MVP.

### 6. Tests Required

- Backend handler tests must cover aggregation grouping, `last_seen_at` sorting, severity escalation, task-run-to-task resource association, and next-action presence.
- Backend ownership tests must assert operators see only owned node events and no `node_id=0` platform events; no-owned-node operators receive an empty timeline.
- Backend tests should cover at least task failures, unresolved alerts, node/metric/probe signals, and backup stale/degraded signals when those sources are changed.
- Router/RBAC tests must assert the endpoint is registered and protected by `tasks:read`.
- Frontend API tests must assert snake_case fields map to camelCase fields, including `last_seen_at`, `event_count`, `source_types`, `next_actions`, `occurred_at`, `task_run_id`, `node_id`, and `policy_id`.
- UI tests must assert the Overview page renders at least one group with severity, resource, likely cause, event count, and a next-action link plus loading/empty/error states when changed.

### 7. Wrong vs Correct

Wrong:

```go
respondOK(c, gin.H{"alerts": alerts, "task_runs": runs})
```

Correct:

```go
respondOK(c, healthIncidentTimelineResponse{Groups: groupedTimeline})
```
